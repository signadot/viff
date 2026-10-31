package entity

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
	"github.com/signadot/tony-format/go-tony/system/libctl"
	logdapi "github.com/signadot/tony-format/go-tony/system/logd/api"
)

// A pump is the store's one watch on a prefix, shared by every subscriber to that prefix.
//
// ONE WATCH PER PREFIX, NOT PER SUBSCRIBER. The rules of a charter read a few slices — the
// tasks, the questions, the pull requests — and a hundred rules on one slice are a hundred
// subscribers to one pump: one logd watch, one opening picture, one re-establish on a drop, one
// copy of the slice, and on each commit one change walk per distinct pattern among them rather
// than one per rule. A subscriber that joins a pump already open is handed its opening picture
// from the pump's copy, in the join itself: no read, no slot in the line (watchGate).
//
// THE PUMP NEVER WAITS ON A SUBSCRIBER. A commit that finds a subscriber's channel full goes
// into its backlog, in order, and is handed over as the subscriber makes room: a burst a rule
// is slow to take still reaches it one commit per commit, which an edge needs (changesAt). A
// backlog that is full too marks the subscriber dropped rather than stalling every other
// subscriber behind it; what the dropped one next receives, behind its backlog, is the net of
// everything since its last commit, marked as a resync, which is what a re-established watch's
// State already looks like to it (Commit.resync).
//
// A pump ends when its last subscriber leaves. One that has had a second joiner in its life
// lingers a moment first (pumpLinger): a rule replaced under its name reopens the same prefix at
// once, and the pump it would find is the one it just left. One nobody joined twice — a
// question's watch on its own subject (untilMoot) — ends at once, so that churn leaves nothing
// behind.
type pump struct {
	s      *LogdStore
	path   string   // the watched path, as the session names it
	segs   []string // the same as segments, root first: what nests an event into the document
	ctx    context.Context
	cancel context.CancelFunc

	// opened is closed once the first establish has answered, and openErr is its answer: a
	// joiner that found the pump being opened waits on the one, then reads the other.
	opened  chan struct{}
	openErr error

	mu   sync.Mutex
	subs []*sub
	// joined counts every subscriber that ever joined, for the linger rule above.
	joined int
	// ended says the pump is out of the store's map, where no join can find it, and its
	// subscribers are detached.
	ended bool
	// doc is the watched subtree as a root-rooted document, maintained by APPLYING each watch
	// event: a State (initial or reconnect) replaces it; a Patch is applied with tony.Patch.
	// The patch's structure is never interpreted — which operations a delta is spelled with,
	// and in what order, is logd's to choose — it is applied opaquely and entities are read out
	// of the reconstructed object.
	doc *ir.Node
	// prev is the document the last emission was taken against, which every subscriber current
	// through it holds as its own prev (sub.prev): those share one walk per pattern. It starts
	// EMPTY, so the first emission reports everything standing at the prefix.
	prev *ir.Node
	// rev is the commit the reconstructed doc now stands at, stamped on every event the next
	// emission makes. Without it a subtree event carried no revision at all while a root event
	// carried its commit — the same stream, one half of it unable to say WHEN. Nothing
	// downstream could then ask a question about a change at the commit it happened at, which
	// is the whole affordance AsOf exists to offer.
	rev int64
	// pictured is whether the first opening picture has been taken in; picture is closed when
	// it has, for a joiner that has to know what it showed (waitIfAbsent false).
	pictured bool
	picture  chan struct{}
	// absent is whether the last picture showed the path holding nothing, and no patch has
	// arrived since: the answer a joiner that would rather be refused than wait is given.
	absent bool
	// reseed is whether a subscriber is dropped and waiting for room; the loop's reseed timer
	// runs while it is.
	reseed bool

	// standing is the watch progress is asked of: the one open now, and none while it is being
	// re-established.
	standingMu sync.Mutex
	standing   *libctl.Watch
}

// A sub is one subscriber to a pump: a question (its pattern) and a channel the answers go
// out on.
type sub struct {
	pattern []*kpath.KPath // the question, as changesAt takes it
	key     string         // the pattern's spelling: subscribers that share it share a walk
	out     chan Commit
	// progress is whether this subscriber asked to hear the pump's progress answers
	// (WatchSubtreeProgress); a plain one has no use for a commit carrying no change.
	progress bool
	// prev is the document this subscriber's last commit was taken against, nil before any:
	// equal to the pump's prev while it is current, and behind it while it is dropped or has
	// just joined, when its next commit is the net of the difference.
	prev *ir.Node
	// sent is the commit of the last commit sent, which a consumer that applied it holds the
	// place as of — and as of every commit after it that changed nothing at the pattern. It
	// rides each commit as since (Commit.since).
	sent int64
	// silent is a subscriber that joined before the picture under noInit: the picture is taken
	// into its prev and not said, so its first commit is the first change after it.
	silent bool
	// backlog holds, in order, the commits handed over while out was full, up to pumpBacklog
	// of them: the consumer reads them as it makes room (drain), and the stream stays one
	// commit per commit through a burst it is slow to take.
	backlog []Commit
	// dropped is a subscriber the backlog could not hold more for; its prev stands where its
	// last commit left it, and the reseed delivers the net since, behind the backlog.
	dropped bool
	// mark is a progress answer this subscriber could not hear (progress, dropped): it is said
	// once the subscriber is current again, at the commit it is then current through.
	mark bool
}

// room is whether a commit can be handed to sb now: on out, or into the backlog.
func (sb *sub) room() bool {
	return len(sb.backlog) < pumpBacklog || len(sb.out) < cap(sb.out)
}

// drain moves the backlog onto out, in order, as far as out has room.
func (sb *sub) drain() {
	for len(sb.backlog) > 0 {
		select {
		case sb.out <- sb.backlog[0]:
			sb.backlog[0] = Commit{}
			sb.backlog = sb.backlog[1:]
		default:
			return
		}
	}
	sb.backlog = nil
}

// pumpLinger is how long a pump that has had a second joiner outlives its last subscriber. A
// var, so a test can shorten it.
var pumpLinger = time.Second

// pumpReseedAfter is how often a backlog is drained and a dropped subscriber tried again.
const pumpReseedAfter = 100 * time.Millisecond

// pumpBacklog is how many commits a subscriber may fall behind, past its channel, before the
// stream nets what follows: the point where a slow consumer costs memory rather than edges. A
// var, so a test can shorten it.
var pumpBacklog = 4096

// pumpOpens counts the pumps that established a watch of their own: an instrument for the
// test that holds two subscribers on one prefix open one watch.
var pumpOpens atomic.Int64

// subtreeHealAfter and subtreeHealMax bound how long a subtree watch can be DEAD WITHOUT
// KNOWING IT before it finds out.
//
// A watch can end for reasons the client did not cause: logd drops a watcher whose buffer it
// cannot drain, because Broadcast runs on the committing goroutine and will not block on a slow
// consumer (server/watch.go, "fail it loudly"). Since go-tony v0.0.112 that arrives as a proper
// terminal watch event carrying a resume commit, so the ordinary path below — the events channel
// closing, and the re-watch that follows — learns about it promptly and this timer never fires.
//
// It exists for the case where nothing arrives to be learned from. Before v0.0.112 the failure
// was sent as an ERROR RESPONSE stamped with the watch's id, and libctl routes anything without
// an Event to its table of in-flight REQUESTS, where a watch id has not appeared since its own
// request completed — so it was logged as "dropping response with no matching request" and
// dropped. The watch was then ORPHANED: gone on the server, alive here, its channel silent
// forever, and a trigger over that slice quietly stopped firing with no error anywhere a charter
// author would see. Measured against a server of that vintage, a slice taking sustained writes
// stalled partway through and never moved again.
//
// That specific hole is closed upstream. This stays because the shape of it is not specific: a
// consumer that can only ever be told is a consumer that waits forever whenever the telling is
// what breaks. So the watch does not only wait — a whole interval of silence, from a slice that
// may merely be idle or from one whose watch is gone, is answered by ASKING THE WATCH
// (libctl Watch.Progress): one small request the server answers with its head, which a watch
// that is gone does not answer. Only then is it re-established, whose State resyncs the document
// and whose diff against the last snapshot emits whatever was missed. Re-establishing at every
// silence instead was measured on staging: most slices see an event about once a minute, so
// ~390 watches each paid a State of their whole subtree every minute, 13 a second, a third of
// logd's read work (git-issue v13rvrcth12krdjbvdn0). The interval DOUBLES while nothing arrives,
// to subtreeHealMax, so an idle slice is asked less and less often; any event resets it.
//
// healProbeWithin is how long the ask may take before silence is read as death. Generous
// against a loaded store, since a false death costs the State the ask exists to avoid.
//
// Vars, not consts, so a test can shorten them: the failure they answer takes thousands of
// writes to provoke, and waiting out the production interval to observe the cure would make
// the test useless.
var (
	subtreeHealAfter = 30 * time.Second
	subtreeHealMax   = 5 * time.Minute
	healProbeWithin  = 10 * time.Second
)

// subtreeReopens counts the subtree watches re-established after they ended or fell silent: an
// instrument for the test that holds a quiet, live watch is asked and not replaced.
var subtreeReopens atomic.Int64

// askProgress asks a watch for its progress off the pump's loop, within healProbeWithin, and
// answers nil for a head and the error for no answer.
func askProgress(ctx context.Context, w *libctl.Watch) <-chan error {
	done := make(chan error, 1)
	go func() {
		pctx, cancel := context.WithTimeout(ctx, healProbeWithin)
		defer cancel()
		_, err := w.Progress(pctx)
		done <- err
	}()
	return done
}

// watchPattern subscribes to the pump at `prefix`, starting it when there is none, and answers
// the subscriber's stream, the leave that ends it, and the pump's progress. progress is whether
// the caller asks the watch's progress (WatchSubtreeProgress): such a pump opens ahead of the
// line (watchGate), and such a subscriber hears the progress answers in its stream.
func (s *LogdStore) watchPattern(prefix []string, at *kpath.KPath, waitIfAbsent, noInit, progress bool) (<-chan Commit, func(), func(context.Context) (int64, error), error) {
	prefix = trimEmpty(prefix)
	// The path as segments as well as a kpath: every event is rooted at the watched path and
	// the document is rooted at the store root, so the segments are what bridges the two (see
	// nestAt).
	segs := append([]string{s.root}, prefix...)
	path := seg(s.root)
	for _, p := range prefix {
		path += "." + seg(p)
	}
	sb := &sub{pattern: []*kpath.KPath{at}, key: at.String(), out: make(chan Commit, 256), progress: progress}
	p, err := s.join(path, segs, sb, noInit)
	if err != nil {
		return nil, nil, nil, err
	}
	// THE PICTURE IS ALWAYS WAITED FOR by the pump, and a subscriber that would rather be
	// refused — a person naming a path at a command line (WatchSubtreeAt) — is told what it
	// showed. Until step 3 makes the answer a phase of a handle, the telling waits here.
	if !waitIfAbsent {
		select {
		case <-p.picture:
		case <-p.ctx.Done():
			p.leave(sb)
			return nil, nil, nil, fmt.Errorf("watching %s: %w", path, p.ctx.Err())
		}
		p.mu.Lock()
		absent := p.absent
		p.mu.Unlock()
		if absent {
			p.leave(sb)
			return nil, nil, nil, fmt.Errorf("watching %s: nothing stands there", path)
		}
	}
	return sb.out, func() { p.leave(sb) }, p.progress, nil
}

// join adds sb to the pump at path, starting the pump when there is none. A joiner of a pump
// already open is seeded from the pump's copy; one that finds the pump being opened waits for
// the opening to answer, and shares its failure.
func (s *LogdStore) join(path string, segs []string, sb *sub, noInit bool) (*pump, error) {
	s.pumpsMu.Lock()
	p, found := s.pumps[path]
	if !found {
		ctx, cancel := context.WithCancel(s.ctx)
		p = &pump{s: s, path: path, segs: segs, ctx: ctx, cancel: cancel,
			opened: make(chan struct{}), picture: make(chan struct{}), doc: ir.Null()}
		s.pumps[path] = p
	}
	p.mu.Lock()
	p.subs = append(p.subs, sb)
	p.joined++
	if noInit {
		// Joining at the next change: the picture, in or to come, is taken in and not said.
		if p.pictured {
			sb.prev = p.doc
		} else {
			sb.silent = true
		}
	}
	p.mu.Unlock()
	s.pumpsMu.Unlock()
	if !found {
		if err := p.open(); err != nil {
			return nil, err
		}
	} else {
		select {
		case <-p.opened:
		case <-s.ctx.Done():
			p.leave(sb)
			return nil, fmt.Errorf("watching %s: %w", path, s.ctx.Err())
		}
		if p.openErr != nil {
			p.leave(sb)
			return nil, p.openErr
		}
	}
	// A late joiner's opening picture: from the pump's copy, in this call. A commit the pump
	// emitted between the append above and here has seeded it already (sub.prev set).
	p.mu.Lock()
	if p.pictured && sb.prev == nil {
		p.deliver(sb, true)
	}
	p.mu.Unlock()
	return p, nil
}

// open establishes the pump's first watch, in line when the pump is, and starts the loop. A
// failure takes the pump out of the store's map and is the answer of every join waiting on it.
func (p *pump) open() error {
	held := false
	if p.inLine() {
		if err := p.s.gate.take(p.ctx); err != nil {
			p.fail(fmt.Errorf("watching %s: %w", p.path, err))
			return p.openErr
		}
		held = true
	}
	w, err := p.s.establish(p.ctx, p.path, &libctl.WatchOptions{WaitIfAbsent: true})
	if err != nil {
		if held {
			p.s.gate.give()
		}
		p.fail(err)
		return p.openErr
	}
	p.stand(w)
	pumpOpens.Add(1)
	close(p.opened)
	go p.run(w, held)
	return nil
}

// fail is an opening that did not: the pump leaves the store's map, its subscribers are
// detached (their channels never spoke and are not closed, since their joins answer the
// error instead), and the joins waiting on it are released with it.
func (p *pump) fail(err error) {
	p.s.pumpsMu.Lock()
	p.mu.Lock()
	delete(p.s.pumps, p.path)
	p.ended = true
	p.subs = nil
	p.mu.Unlock()
	p.s.pumpsMu.Unlock()
	p.openErr = err
	close(p.opened)
	p.cancel()
}

// leave removes sb and closes its stream; the last to leave ends the pump, after its linger.
func (p *pump) leave(sb *sub) {
	p.mu.Lock()
	n := len(p.subs)
	for i, x := range p.subs {
		if x == sb {
			p.subs = append(p.subs[:i], p.subs[i+1:]...)
			close(sb.out)
			break
		}
	}
	left := len(p.subs)
	ended := p.ended
	linger := p.joined >= 2
	p.mu.Unlock()
	if left > 0 || n == left || ended {
		return
	}
	if linger {
		time.AfterFunc(pumpLinger, p.endIfIdle)
		return
	}
	p.endIfIdle()
}

// endIfIdle ends the pump if it still has no subscriber: out of the store's map first, under
// the lock a join takes to find it, so no join finds a pump that is ending.
func (p *pump) endIfIdle() {
	p.s.pumpsMu.Lock()
	p.mu.Lock()
	if len(p.subs) == 0 && !p.ended {
		delete(p.s.pumps, p.path)
		p.ended = true
		p.cancel()
	}
	p.mu.Unlock()
	p.s.pumpsMu.Unlock()
}

// inLine is whether the pump stands in the line to open (watchGate), asked at each establish:
// it does unless a subscriber asked its progress (WatchSubtreeProgress), whose reads wait on
// the watch and are not to wait on the rest of the line — whether that subscriber opened the
// pump or joined it.
func (p *pump) inLine() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, sb := range p.subs {
		if sb.progress {
			return false
		}
	}
	return true
}

func (p *pump) stand(w *libctl.Watch) {
	p.standingMu.Lock()
	p.standing = w
	p.standingMu.Unlock()
}

// progress asks the standing watch to say, in its stream, when it is current through the head,
// and answers the head; see WatchSubtreeProgress.
func (p *pump) progress(ctx context.Context) (int64, error) {
	p.standingMu.Lock()
	cur := p.standing
	p.standingMu.Unlock()
	if cur == nil {
		return 0, fmt.Errorf("watching %s: the watch is being re-established", p.path)
	}
	return cur.Progress(ctx)
}

// apply takes one watch event into doc, under p.mu.
func (p *pump) apply(ev *logdapi.WatchEvent) {
	if ev.Commit > p.rev {
		p.rev = ev.Commit
	}
	switch {
	case ev.Patch != nil:
		// A patch that fails to apply leaves doc unchanged and is reported; a later State (on
		// reconnect) resyncs it. The patch is never decoded to recover by hand (applyDelta). A
		// patch that TOOK THE PATH AWAY is applied like any other — that is how this copy
		// comes to hold nothing — so the Absent riding on it needs no branch of its own.
		p.doc = p.s.applyDelta(p.doc, nestAt(ev.Patch, p.segs))
		p.absent = ev.Absent // the delta took the path away
	case ev.State != nil || ev.Absent:
		p.doc = nestAt(stateOf(ev), p.segs) // full current state of the subtree
		p.absent = ev.Absent
	}
}

// emit hands every subscriber what the event just applied changed at its pattern, under p.mu.
// state is whether that event was a State, whose changes are the net of every commit since
// the last picture rather than one commit's own (Commit.resync).
//
// ONE WALK PER PATTERN among the subscribers current through prev: the changes are the same
// for all of them, and only the since/resync bookkeeping is each one's, so they are handed
// ONE SLICE, and the nodes in it, to share. A consumer that narrows a commit copies it
// (Commit.Only) and does not write into what it was handed. A subscriber behind prev — just
// joined, or dropped — is walked from where it stands, on its own.
//
// It runs once per watch event, never once per burst. Coalescing — apply N deltas, walk once —
// is exact for a LEVEL, since the merged diff still reports each entity's current state, and
// lossy for an EDGE: a value that appears at one commit and disappears at the next leaves no
// trace in a diff spanning both, and a transition the stream never reported cannot be recovered
// downstream. So the walk is per commit, and the cost is a subtree walk per commit rather than
// per burst.
//
// A commit that changes nothing at a subscriber's pattern sends it nothing rather than an
// empty commit: a consumer of one slice has no use for the revisions where its slice stood
// still.
func (p *pump) emit(state bool) {
	root := p.s.root
	after := childByKey(p.doc, root)
	shared := map[string][]Event{}
	for _, sb := range p.subs {
		// Nothing to say to one current through doc already (seeded silently at this very
		// picture), and nothing to be done for one dropped with no room yet: the reseed tries
		// it when there is, rather than a walk of the whole slice for it on every commit.
		if sb.silent || sb.prev == p.doc || (sb.dropped && !sb.room()) {
			continue
		}
		var changes []Event
		if sb.prev == p.prev {
			ch, ok := shared[sb.key]
			if !ok {
				// With no delta to prune it: a State replaces the document wholesale, so only
				// the two documents can say what differs. It compares with ir.Node.DeepEqual,
				// not reflect.DeepEqual — a node carries Parent links that say where it sits
				// rather than what it holds, and a document rebuilt from a fresh read wires
				// them anew, which reflect would count as every node changing.
				ch = changesAt(sb.pattern, childByKey(p.prev, root), after, nil, p.rev)
				shared[sb.key] = ch
			}
			changes = ch
		} else {
			changes = changesAt(sb.pattern, childByKey(sb.prev, root), after, nil, p.rev)
		}
		p.send(sb, changes, state || sb.prev != p.prev)
	}
	p.prev = p.doc
}

// deliver walks one subscriber from where it stands to doc and sends it the difference: a
// joiner's opening picture, or a dropped subscriber's catch-up. resync is what the commit says
// of itself (Commit.resync).
func (p *pump) deliver(sb *sub, resync bool) {
	root := p.s.root
	p.send(sb, changesAt(sb.pattern, childByKey(sb.prev, root), childByKey(p.doc, root), nil, p.rev), resync)
}

// send hands sb changes at doc without waiting: a subscriber that cannot take them, on its
// channel or in its backlog, is marked dropped, to be tried again by the reseed, and no change
// marks it current. A subscriber brought current that owes a progress mark is handed it here,
// at the commit it is now current through.
func (p *pump) send(sb *sub, changes []Event, resync bool) {
	if len(changes) > 0 {
		if !p.hand(sb, Commit{Rev: p.rev, Changes: changes, since: sb.sent, resync: resync}) {
			sb.dropped = true
			p.reseed = true
			return
		}
		sb.sent = p.rev
	}
	sb.prev = p.doc
	sb.dropped = false
	if sb.mark {
		p.markProgress(sb, p.rev)
	}
}

// hand puts c on sb's channel, or behind what is already waiting in its backlog, without
// waiting; false is a subscriber with room for neither. The backlog is drained first, so the
// order of commits is the order they were handed.
func (p *pump) hand(sb *sub, c Commit) bool {
	sb.drain()
	if len(sb.backlog) == 0 {
		select {
		case sb.out <- c:
			return true
		default:
		}
	}
	if len(sb.backlog) >= pumpBacklog {
		return false
	}
	sb.backlog = append(sb.backlog, c)
	p.reseed = true // drained as the consumer makes room
	return true
}

// markProgress tells sb it is current through commit, without waiting: one that cannot hear
// it now is owed it, and hears it with its catch-up (send).
func (p *pump) markProgress(sb *sub, commit int64) {
	if !p.hand(sb, Commit{Rev: commit, since: sb.sent, progress: true}) {
		sb.dropped = true
		sb.mark = true
		p.reseed = true
		return
	}
	sb.sent = commit
	sb.mark = false
}

// retry drains every backlog and tries every dropped subscriber again, under p.mu, and says
// whether anything is still waiting.
func (p *pump) retry() bool {
	p.reseed = false
	for _, sb := range p.subs {
		sb.drain()
		if sb.dropped {
			p.deliver(sb, true)
		}
		if len(sb.backlog) > 0 {
			p.reseed = true
		}
	}
	return p.reseed
}

// run drains the pump's watch, re-establishing it whenever it ends, until the pump's ctx ends.
// held is whether a slot in the line is held for the opening picture.
func (p *pump) run(w *libctl.Watch, held bool) {
	s := p.s
	// A slot is held from an establish until the opening picture is in; give is the one way
	// it goes back: on the picture, on a failed establish, or when the pump ends.
	give := func() {
		if held {
			held = false
			s.gate.give()
		}
	}
	defer func() {
		give()
		// The pump ends: out of the map if it is not already (a store closing), and every
		// subscriber still on it hears its stream close.
		s.pumpsMu.Lock()
		p.mu.Lock()
		if !p.ended {
			delete(s.pumps, p.path)
			p.ended = true
		}
		subs := p.subs
		p.subs = nil
		p.mu.Unlock()
		s.pumpsMu.Unlock()
		for _, sb := range subs {
			close(sb.out)
		}
	}()
	// A progress event is current through its commit: nothing is applied, and the
	// subscribers that asked are told, in order with every commit before it.
	progressed := func(commit int64) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if commit > p.rev {
			p.rev = commit
		}
		for _, sb := range p.subs {
			if !sb.progress {
				continue
			}
			if sb.dropped {
				sb.mark = true // heard with its catch-up, at a commit at or past this one
				continue
			}
			p.markProgress(sb, commit)
		}
	}
	// take applies one event and emits what it changed. The first picture seeds the silent
	// subscribers without a word and is announced to a joiner waiting to hear what it showed.
	take := func(ev *logdapi.WatchEvent) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.apply(ev)
		if !p.pictured {
			p.pictured = true
			for _, sb := range p.subs {
				if sb.silent {
					sb.silent = false
					sb.prev = p.doc
				}
			}
			close(p.picture)
		}
		p.emit(ev.Patch == nil)
	}
	backoff := 100 * time.Millisecond
	// A watch can die WITHOUT SAYING SO, and then this loop would wait on a channel nothing
	// will ever send to again. See subtreeHealAfter: the heal timer is what bounds how long
	// that can last.
	//
	// NOT WHILE THE OPENING PICTURE IS PENDING. From an establish until its State (or Absent)
	// arrives, the watch is a read the store is serving, and a probe sent then queues behind
	// that very read and answers nothing about the watch; a slow picture — the condition the
	// line (watchGate) exists for — would read as a dead watch and send it round again. So the
	// timer stands still (quiet) from each establish until the first event, which starts it
	// (alive). A session that dies meanwhile is the heartbeat's to end, and ends this stream
	// with it.
	heal := time.NewTimer(subtreeHealAfter)
	defer heal.Stop()
	healAfter := subtreeHealAfter
	// quiet stops the heal, and drains a firing nobody took.
	quiet := func() {
		if !heal.Stop() {
			select {
			case <-heal.C:
			default:
			}
		}
	}
	// alive holds the heal off for a whole interval from now, and forgets any backoff it
	// accrued: the watch has just shown itself alive.
	alive := func() {
		healAfter = subtreeHealAfter
		quiet()
		heal.Reset(healAfter)
	}
	quiet() // the first picture is pending
	// reseed fires while a subscriber is dropped; nil while none is.
	var reseed *time.Timer
	var reseedC <-chan time.Time
	armReseed := func() {
		p.mu.Lock()
		need := p.reseed
		p.mu.Unlock()
		if need && reseed == nil {
			reseed = time.NewTimer(pumpReseedAfter)
			reseedC = reseed.C
		}
	}
	defer func() {
		if reseed != nil {
			reseed.Stop()
		}
	}()
	// probe is a heal's ask in flight on w, nil while there is none; swallow counts the
	// progress events those asks draw, which are the pump's and not the subscribers'.
	var probe <-chan error
	swallow := 0
	for {
		// Drain the current watch until it ends. A clean end is a force-end (a mount/unmount
		// overlapping this subtree changed membership → WatchEndedError, reason
		// "session_mounted" or "session_unmounted" since go-tony v0.0.122, one
		// "membership_changed" before it) or a dropped connection — NOT a terminal condition.
		// It is re-established below rather than returned from, and the reason does not
		// change that: every ending upstream defines is re-establishable, and re-watching is
		// the whole response. It would matter to code deciding whether the content it is
		// about to see SHOULD differ; this loop only has to catch up.
	drain:
		for {
			select {
			case <-p.ctx.Done():
				w.Close()
				return
			case <-reseedC:
				reseed, reseedC = nil, nil
				p.mu.Lock()
				again := p.retry()
				p.mu.Unlock()
				if again {
					armReseed()
				}
			case <-heal.C:
				// Quiet for a whole interval. Either the slice is idle or the watch is dead,
				// and the watch itself is asked which: one request, off this loop so the
				// stream keeps draining meanwhile. The timer stands still until it answers,
				// or does not, below.
				if probe == nil {
					probe = askProgress(p.ctx, w)
					// Counted as sent, not as answered: the progress event an ask draws can
					// reach the stream before the ask's own answer is read here.
					swallow++
				}
			case err := <-probe:
				probe = nil
				// Back off while nothing changes either way, so an idle slice is asked less
				// often and a dark one is not re-established every interval.
				healAfter = min(healAfter*2, subtreeHealMax)
				heal.Reset(healAfter)
				if err != nil {
					break drain // no answer: dead, or as good as; re-establish
				}
			case ev, ok := <-w.Events():
				if !ok {
					break drain // watch ended; re-establish
				}
				if ev.Progress && swallow > 0 {
					// A heal's ask answered, which the answer itself already said. Not an
					// event about the slice, so neither the heal nor a subscriber hears it.
					swallow--
					p.mu.Lock()
					if ev.Commit > p.rev {
						p.rev = ev.Commit
					}
					p.mu.Unlock()
					continue
				}
				alive()
				if ev.ReplayComplete {
					continue // live watch: no replay phase to complete
				}
				if ev.Progress {
					progressed(ev.Commit)
					armReseed()
					continue
				}
				if ev.Patch == nil {
					give() // the opening picture is in, whichever establish asked for it
				}
				take(ev)
				armReseed()
			}
		}
		// The watch ended (force-end or drop). Re-establish on the same path — it re-composes
		// against the CURRENT mount set — with backoff. The fresh State event replaces doc with
		// current truth, and the emission closes any gap against the last snapshot.
		p.stand(nil)
		w.Close()
		give() // a watch that ended before its picture came gives its slot back here
		if p.ctx.Err() != nil {
			return
		}
		for {
			// RE-WATCH AND RECONCILE, rather than resuming from the commit the ended event
			// names. logd offers an exact resume point (WatchEndedError.Commit) and libctl's
			// own doc says who it is for: "a snapshot-diffing consumer needs neither and can
			// just re-watch and reconcile against its last snapshot." That is precisely this
			// pump — it holds prev and reports the difference — and measured, the choice is
			// not close. Resuming replays the missed deltas one at a time into a stream that
			// is already the bottleneck: a 2000-entity slice reached 894 of 2000 in three
			// minutes. Re-initializing sends ONE State carrying net state, and the same slice
			// completed in 32s.
			//
			// IN LINE AGAIN: every watch a drop ended wants back at once, and the line is what
			// makes that a few at a time (watchGate).
			if p.inLine() {
				if s.gate.take(p.ctx) != nil {
					return
				}
				held = true
			}
			nw, err := s.establish(p.ctx, p.path, &libctl.WatchOptions{WaitIfAbsent: true})
			if err == nil {
				w = nw
				p.stand(nw)
				// An ask still out on the watch that ended answers for nobody, and the
				// progress event it would have drawn never reaches this stream.
				probe, swallow = nil, 0
				quiet() // the picture is pending again; its arrival starts the heal
				subtreeReopens.Add(1)
				backoff = 100 * time.Millisecond
				break
			}
			give()
			select {
			case <-p.ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 5*time.Second)
		}
	}
}

// nestAt puts a watch event's node under the segments of the path it is rooted at, which is
// what makes it part of a document rooted further up.
//
// ONE ROOTING (session protocol 2). Every event of a watch — the State it opens on and each
// Patch after it — is rooted AT THE WATCHED PATH, with that path's own segments stripped: a
// watch on verse.demo.pr sees top-level fields that are ids. The documents this store reads
// entities out of are rooted at the store root, because a Ref is addressed from there and
// nothing below the root is fixed for the store as a whole. Nesting is the difference
// between the two rootings, and it is the same for both event shapes.
//
// A nil node is a path holding nothing and nests as a null, which reads out as no entities.
func nestAt(node *ir.Node, segs []string) *ir.Node {
	if node == nil {
		node = ir.Null()
	}
	for i := len(segs) - 1; i >= 0; i-- {
		node = ir.FromMap(map[string]*ir.Node{segs[i]: node})
	}
	return node
}

// stateOf is the value a state event opens on: what it carries, or — when the path holds
// nothing — an empty object. ABSENCE IS SAID BY THE WIRE, not spelled as a null: null is a
// value a path can hold, so a document seeded with one where nothing stands could not tell
// an entity holding null from a slice that is empty.
func stateOf(ev *logdapi.WatchEvent) *ir.Node {
	if ev.Absent {
		return ir.FromMap(map[string]*ir.Node{})
	}
	return ev.State
}
