package trigger

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"
)

// A RULE OF THE CHARTER IS ARMED OR PENDING. Armed, it is in e.triggers with its watches
// asked of the store, and fires on what they deliver. Pending, it is in e.pending with the
// CAUSE it waits on: it stands by name, as an installed rule does, and fires on nothing. The
// causes are the ones that can clear without the rule's record changing — a stratum it names
// not installed yet, no runner offering what its spawn asks, a read the vetting needs that did
// not happen, a vet that passed at install and fails now that the world has moved, and the
// pass itself, for a rule just placed or just lifted. Everything else an install refuses,
// where the author is present to hear it (vet).
//
// THE STORE IS NOT A CAUSE. An armed rule's watches are handles (entity.Watch), opened by the
// store behind the arm — in line, established, asked again after a refusal or a drop — and
// the rule is armed from the moment it has them: what the store is doing for each is read
// off the handle (watching), and is what `arming:` says. Nothing moves between the lists
// when a watch drops or is refused.
//
// The reconciler is what clears a cause: a pass over every pending rule, in the order they
// were placed, vetting each again and arming the ones that pass. A pass runs when something
// that could clear a cause happens — a rule placed, a stratum installed, a declaration set, a
// suspension lifted, and what versed sees: a runner registered or gone — and once a minute
// regardless (git-issue wv2pp5j0).

// ErrNoOffer marks an offer check's refusal of a spawn no runner offers at all, as against one
// some runner offers and none takes: the first is held for a runner that may register, the
// second is refused where the author is present.
var ErrNoOffer = errors.New("no runner offers it")

// cause is why a pending rule is not armed.
type cause struct {
	kind causeKind
	why  string // the words: the stratum's name, the runner refusal, the read's or the vet's error
}

type causeKind int

const (
	causeStratum causeKind = iota // the stratum it names is not installed
	causeRunner                   // no runner offers what its spawn asks
	causeRead                     // a read the vetting needs did not happen
	causeRefused                  // it does not pass now, as the world stands since its install
	causePersist                  // its record is being written; no pass touches it until it is
	causePass                     // placed, or back from a lift; the pass it kicked arms it
)

// String is the cause as status and an install's answer say it.
func (c cause) String() string {
	switch c.kind {
	case causeStratum:
		return "waiting for stratum " + c.why
	case causeRunner:
		return "waiting for a runner: " + c.why
	case causeRead:
		return "waiting on a read: " + c.why
	case causeRefused:
		return "refused: " + c.why
	case causePersist:
		return "being written down"
	default:
		if c.why != "" {
			return "waiting for a pass to arm it: " + c.why
		}
		return "waiting for a pass to arm it"
	}
}

// held is a pending rule with its cause, and the generation it was placed in: a rule removed
// and installed again under the same name while a pass was opening the first one's watches is
// not the rule that pass vetted, and arm answers no to it (git-issue wv2pp5j0).
type held struct {
	Trigger
	cause cause
	gen   uint64
	// settled is closed once a pass has answered for this entry: it armed, it was removed, or
	// its cause became one a pass does not clear by itself (settles). What a wait on a rule
	// just placed hangs on (Ready).
	settled chan struct{}
	done    bool
}

// hold puts a rule among the pending ones with this cause. Called with trigMu HELD.
func (e *Engine) hold(t Trigger, c cause) {
	e.placed++
	e.pending = append(e.pending, held{Trigger: t, cause: c, gen: e.placed, settled: make(chan struct{})})
}

// passing is whether a cause is one the next pass clears on its own, with nothing else to
// happen first: a rule just placed, or back from a lift, or being written down.
func (c cause) passing() bool { return c.kind == causePass || c.kind == causePersist }

// settle closes a pending entry's settled, once. Called with trigMu HELD.
func (e *Engine) settle(i int) {
	if !e.pending[i].done {
		e.pending[i].done = true
		close(e.pending[i].settled)
	}
}

// Pending is one rule that fires on nothing as the engine reports it: its name and why. From
// Pendings, a pending rule and its cause; from Arming, an armed rule and what the store is
// doing for its watch.
type Pending struct {
	Rule  string
	Cause string
}

// reconcileEvery is the backstop: a pass this long after the last, kicked or not.
const reconcileEvery = time.Minute

// Kick asks for a pass over the pending rules: something that could clear a cause happened
// that the engine does not see for itself — a runner or an io registered, a session back.
func (e *Engine) Kick() { e.kick() }

// kick asks for a pass. With the reconciler running it is asked of it; without — an engine
// nobody started, as a check's or a test's — the pass is made here and now, which opens no
// watches, since only a started engine in subtree mode has any to open.
func (e *Engine) kick() {
	if !e.reconciling.Load() {
		e.pass()
		return
	}
	select {
	case e.kicks <- struct{}{}:
	default: // a pass is already asked for
	}
}

// reconcile is the reconciler's loop: a pass per kick, and one every reconcileEvery.
func (e *Engine) reconcile(ctx context.Context) {
	every := time.NewTimer(e.every)
	defer every.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.kicks:
		case <-every.C:
		}
		e.pass()
		if !every.Stop() {
			select {
			case <-every.C:
			default:
			}
		}
		every.Reset(e.every)
	}
}

// pass tries every pending rule once, in the order they were placed.
func (e *Engine) pass() {
	e.trigMu.Lock()
	snap := make([]held, len(e.pending))
	copy(snap, e.pending)
	e.trigMu.Unlock()
	for _, h := range snap {
		if e.bgctx != nil && e.bgctx.Err() != nil {
			return
		}
		e.try(h)
	}
}

// try vets one pending rule again and arms it if it passes. A rule refused now — its stratum
// arrived and its subject is outside it, say, or the runner that offered its spawn left and
// another's ceiling is lower — stays pending with the refusal as its cause: it was accepted,
// its record stands, and the world that refuses it may change back. It is reported once, as
// the cause changes. A rule still held has its cause brought up to date. One that passes asks
// the store for its watches and is armed with the handles, at once: the store opens them
// behind the arm, and a removal that lands meanwhile wins (arm).
func (e *Engine) try(h held) {
	if h.cause.kind == causePersist {
		return // its install has it: written down first, then placed for the pass it kicks
	}
	hold, err := e.vet(h.Trigger, e.OwnerOf(h.Name))
	if err != nil {
		if e.setCause(h.Name, cause{causeRefused, err.Error()}) {
			e.logger.Warn("pending rule refused: it does not pass as the world stands; it fires on nothing until it does",
				"trigger", h.Name, "waited", h.cause.String(), "err", err)
		}
		return
	}
	if hold != nil {
		e.setCause(h.Name, *hold)
		return
	}
	var rw *ruleWatches
	if e.subtree {
		subtrees, err := e.watchedSubtrees(h.Trigger)
		if err != nil {
			e.setCause(h.Name, cause{causeRefused, err.Error()})
			return
		}
		if rw, err = e.openWatches(h.Trigger, subtrees); err != nil {
			// A request the store cannot make at all, which asking again does not change: a
			// refusal, where the store's own refusal of a watch is a phase on the handle.
			e.setCause(h.Name, cause{causeRefused, "the store cannot watch it: " + err.Error()})
			return
		}
	}
	if !e.arm(h, rw) {
		rw.stop()
		return
	}
	if h.cause.kind != causePass {
		// Advised as it arms: what a rule held for its stratum crosses is not known until then.
		for _, w := range append(advise(h.Trigger), e.adviseDeriveClobber(h.Trigger)...) {
			e.logger.Warn("charter", "trigger", h.Name, "note", w)
		}
		e.logger.Info("pending rule armed: what it waited for is here", "trigger", h.Name, "waited", h.cause.String())
	}
}

// arm moves the pending rule a pass vetted to the armed ones, with its watches (none in
// baseline mode), answering false where it is no longer pending — removed meanwhile, or
// removed and placed again under its name, which is another rule.
func (e *Engine) arm(h held, rw *ruleWatches) bool {
	name := h.Name
	e.trigMu.Lock()
	i := e.pendingIndex(name)
	if i < 0 || e.pending[i].gen != h.gen {
		e.trigMu.Unlock()
		return false
	}
	e.settle(i)
	e.pending = slices.Delete(slices.Clone(e.pending), i, i+1)
	e.triggers = append(e.triggers, h.Trigger)
	old := e.watches[name]
	if rw != nil {
		e.watches[name] = rw
	} else {
		delete(e.watches, name)
	}
	e.trigMu.Unlock()
	old.stop()
	return true
}

// pendingIndex is where the pending rule of this name stands, -1 for none. Called with trigMu
// HELD.
func (e *Engine) pendingIndex(name string) int {
	for i, h := range e.pending {
		if h.Name == name {
			return i
		}
	}
	return -1
}

// setCause brings a pending rule's cause up to date, answering whether that changed it; a
// rule no longer pending is left alone.
func (e *Engine) setCause(name string, c cause) bool {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	i := e.pendingIndex(name)
	if i < 0 || e.pending[i].cause == c {
		return false
	}
	if e.pending[i].cause.passing() && !c.passing() {
		e.settle(i) // the pass answered: it waits on something else now
	}
	e.pending[i].cause = c
	return true
}

// Pendings is every pending rule with its cause, by name.
func (e *Engine) Pendings() []Pending {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	out := make([]Pending, 0, len(e.pending))
	for _, h := range e.pending {
		out = append(out, Pending{Rule: h.Name, Cause: h.cause.String()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rule < out[j].Rule })
	return out
}

// Arming is every armed rule whose watches are not all open, sorted by name, each with what
// the store is doing for the first that is not (watching). Empty on a verse that is up.
func (e *Engine) Arming() []Pending {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	var out []Pending
	for _, t := range e.triggers {
		if ok, doing := e.watchingLocked(t.Name); !ok {
			out = append(out, Pending{Rule: t.Name, Cause: doing})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rule < out[j].Rule })
	return out
}

// Held returns the pending rules: each is written down, occupies its name and fires on nothing
// until what it waits for is here (Pendings says what).
func (e *Engine) Held() []Trigger {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	return e.pendingTriggers()
}

// pendingTriggers is the pending rules without their causes, for the walks over the charter
// whole. Called with trigMu HELD.
func (e *Engine) pendingTriggers() []Trigger {
	out := make([]Trigger, 0, len(e.pending))
	for _, h := range e.pending {
		out = append(out, h.Trigger)
	}
	return out
}
