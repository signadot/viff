package trigger

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/task"
)

// Opening an engine, and settling what the last viff left it.
//
// AN ENGINE FIRES NOTHING AND TAKES NO TASK'S END UNTIL IT IS OPEN. It opens once its charter is
// restored, the tasks the last viff left it have been handed over, and the claims that viff
// left it are settled. Until then its rules stand pending and its tasks stand live, whatever
// their records say. So while it settles, nothing else of the engine runs: every claim it finds
// under its owner prefix is the last viff's, a task that is live is one that viff left
// running, and a rule is in the charter or it is not.
//
// A CLAIM MEANS VIFF OWES SOMETHING: a hook to run when a task ends, or a sequence to carry on.
// While viff runs, the debt is paid when the task's record ends (reap). A claim the last viff
// left with no task behind it — it ended between a task's end and what was owed for it, or the
// task ended with it — is paid here.
//
// It is paid by ending the task again, from its record, through the path a live task's end
// takes (applyHook): the rule's hook runs, a sequence advances or stops, and the claim is
// released. So a hook may run a second time, where viff ended after it ran and before the
// claim came off. A sequence's element is not started a second time by a second end of the
// element before it, since an end advances a run only while `step` has not passed its element
// (resumeSeq).

// AwaitTasks says the engine's host will hand it the tasks the last viff left (AdoptChain,
// Adopt) and say when they are in (TasksIn): the engine does not open before. Called ahead of
// ObserveScoped, and ahead of anything that could hand the engine a task; asked again, it changes
// nothing. An engine whose host says nothing is handed none.
func (e *Engine) AwaitTasks() {
	if e.tasksIn != nil {
		return
	}
	e.tasksIn = make(chan struct{})
	e.opened = make(chan struct{}) // shut from here on, for a task handed over ahead of the start
	e.started = make(chan struct{})
}

// sayStarted says the engine has its context.
func (e *Engine) sayStarted() {
	if e.started != nil {
		e.startedOnce.Do(func() { close(e.started) })
	}
}

// TasksIn says the tasks the last viff left this engine are all in.
func (e *Engine) TasksIn() {
	if e.tasksIn != nil {
		e.tasksInOnce.Do(func() { close(e.tasksIn) })
	}
}

// Opened is closed once the engine is open. nil for an engine nobody started.
func (e *Engine) Opened() <-chan struct{} { return e.opened }

// isOpen reports whether the engine is open: one nobody started is.
func (e *Engine) isOpen() bool {
	if e.opened == nil {
		return true
	}
	select {
	case <-e.opened:
		return true
	default:
		return false
	}
}

// awaitOpen waits until the engine is open, answering false where it stops first.
func (e *Engine) awaitOpen() bool {
	if e.opened == nil {
		return true
	}
	if e.started != nil {
		<-e.started // the context is the start's to set
	}
	ctx := e.bgctx
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-e.opened:
		return true
	case <-ctx.Done():
		return false
	}
}

// open opens the engine: after its charter and its tasks are in, it settles the claims the last
// viff left, and then lets the rules arm and the ends be taken.
func (e *Engine) open(ctx context.Context) {
	for _, in := range []<-chan struct{}{e.Restored(), e.tasksIn} {
		if in == nil {
			continue
		}
		select {
		case <-in:
		case <-ctx.Done():
			return
		}
	}
	// The launches left parked first: a claim waiting on one is in flight, and not a debt.
	if !e.begin() {
		return // the engine was stopped before it opened
	}
	settled := e.restoreParked(ctx) && e.settleLeft(ctx)
	e.work.Done()
	if !settled {
		return
	}
	close(e.opened)
	// An end that was let pass is taken now, by the read each tracked task waits to make
	// (reapEnded), and a pass arms the rules. A launch left parked that nothing holds now starts.
	e.releaseLaunches()
	e.kick()
}

// settleLeft settles every claim standing under this engine's owner prefix, spawn claims and
// then the run claims that still stand, since an element's own claim settles its run. A claim
// left unsettled because the store did not answer for it, or for what says how to pay it, holds
// its subject for as long as this viff instance runs. So the claims are listed and settled again until
// the store has answered for each, for as long as the engine runs, and the engine stays shut
// meanwhile. A read the store refused is not asked again (unsettled).
func (e *Engine) settleLeft(ctx context.Context) bool {
	for wait := 250 * time.Millisecond; ; wait = min(2*wait, 8*time.Second) {
		claims, err := entity.BelowErr(e.store, func(r entity.Ref) bool {
			return r.ChildOf(entity.ViffRoot, mirror.System, mirror.ClaimKind)
		}, 1, entity.ViffRoot, mirror.System, mirror.ClaimKind)
		e.settleUnread = false
		if err != nil {
			e.logger.Warn("the claims the last viff left could not be listed", "err", err)
			e.settleUnread = true
		}
		for _, spawns := range []bool{true, false} {
			for _, claim := range claims {
				if ctx.Err() != nil {
					return false
				}
				if OwnerOf(claim.Ref.Name()) != e.idOwner() {
					continue
				}
				if (entity.Str(claim.Payload, mirror.ClaimOfField) == mirror.ClaimOfSpawn) == spawns {
					e.settleClaim(claim)
				}
			}
		}
		if !e.settleUnread {
			return true
		}
		e.logger.Warn("the claims the last viff left could not all be read; the engine stays shut and asks again")
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
	}
}

// unsettled notes a claim left unsettled for a read that failed with err. A read the store did
// not answer is asked again (settleLeft). One it refused is not: asking again gets the same
// answer, and the claim stands with the engine open.
func (e *Engine) unsettled(err error) {
	if !errors.Is(err, entity.ErrForbidden) {
		e.settleUnread = true
	}
}

// errRestarted is why a run stops at an element that spawns nothing: it reads the facts of the
// task before it, or of the firing, and a restart has neither to hand.
var errRestarted = errors.New("viff restarted before this element ran")

// settleClaim pays what one claim the last viff left is owed, and reports whether there was
// anything to pay. The claim is one this engine's rules took: its id carries the engine's owner
// prefix (Owner).
//
// A hooked spawn's claim is settled by its own task, whose id is the claim's. A sequence's run
// claim is settled by its element in flight, the one at `step`:
//
//	the run is recorded ended, or done   the claim is released, and nothing runs
//	its task's record says done          the run advances, and the next element runs
//	it says anything else terminal       the run stops, and the element's off-ramp hook runs
//	it spawns, and no record says an end as failed, orphaned: the run stops, its onFailed runs
//	it spawns nothing                    the run stops, ended in error, saying why
//	every element has completed, and
//	the run is not recorded done         the sequence's onDone is owed, and runs
//
// THE RECORD IS THE CLAIM'S OWN TASK'S only where its `occurrence` is no earlier than the
// claim's: a task's id is the same from one run to the next, so the record found at it may be
// of the run before, where viff ended between taking the claim and starting the task.
//
// A claim whose rule is in the charter and not armed is settled as any other: its task ran. One
// whose rule is not in the charter is released, and nothing runs.
//
// A claim is paid only while it still stands as it was listed: settling one claim can release
// another, and a hook it runs can take that address again.
func (e *Engine) settleClaim(claim entity.Entity) bool {
	id := claim.Ref.Name()
	chain := entity.Str(claim.Payload, "chain")
	subj, err := entity.ParseAddr(entity.Str(claim.Payload, "flow"))
	if err != nil || chain == "" {
		// Nothing says what it is a claim of: nothing is owed that can be found.
		e.write(claim.Ref.At(), nil)
		return true
	}
	if spawnID(e.idOwner(), chainID(chain), subj) != id {
		return false // another engine's: this one's rules take no claim at that id
	}
	// Still standing as listed.
	now, ok, err := e.store.Get(claim.Ref)
	if err != nil {
		e.logger.Warn("a claim could not be settled: it could not be read", "claim", id, "err", err)
		e.unsettled(err)
		return false
	}
	if !ok || entity.Str(now.Payload, takenField) != entity.Str(claim.Payload, takenField) {
		return false
	}
	since := mirror.OccurrenceOf(claim.Payload)
	if entity.Str(claim.Payload, mirror.ClaimOfField) == mirror.ClaimOfSpawn {
		if _, live := e.Live(id); live {
			return false // its end pays it
		}
		if e.parkedAt(chain, subj) {
			return false // a suspension holds its launch, and the lift starts the task that pays it
		}
		return e.settleTask(id, subj, chain, since)
	}
	return e.settleRun(subj, chain, since)
}

// settleRun settles a sequence's run claim at chain about subj, taken at the commit since.
func (e *Engine) settleRun(subj entity.Ref, chain string, since int64) bool {
	// A task of the run that is live carries it on when it ends: the element an earlier settle
	// advanced the run to.
	for _, fp := range e.liveTasks() {
		if c, _, ok := seqIndexOf(fp.chain); ok && c == chain && fp.subject == subj.String() {
			return false
		}
	}
	// And so does one whose launch a suspension holds, when the lift starts it.
	if e.parkedIn(chain, subj) {
		return false
	}
	rule := mirror.TriggerOf(chain)
	var carrier *Action
	if t, ok := e.Rule(rule); ok {
		carrier, _ = navigate(t, chain)
	}
	if carrier == nil || len(carrier.Seq) == 0 {
		e.tracef("settle %s: no sequence stands at the chain; its claim on %s is released", chain, subj)
		e.releaseRun(chain, subj)
		return true
	}
	// Where the run has got to is the subject's to say, and a subject unread says nothing: the
	// claim stands, to be settled again (settleLeft).
	facts, err := e.readSeqFacts(chain, subj)
	if err != nil {
		e.logger.Warn("a run's claim could not be settled: its subject could not be read", "chain", chain, "subject", subj.String(), "err", err)
		e.unsettled(err)
		return false
	}
	if got := entity.Str(facts, seqShape); got != "" && got != e.shapeOf(chain) {
		e.tracef("settle %s: the sequence changed while it was running; its claim on %s is released", chain, subj)
		e.releaseRun(chain, subj)
		return true
	}
	if d := entity.Get(facts, seqDone); entity.Str(facts, seqEnded) != "" || d != nil && d.Bool {
		// The run's conclusion is on record, written after everything it owed but its claim: a
		// stop ahead of the release (stopSeq), with the off-ramp's hook its element's own
		// claim's to pay, and `done` after the sequence's onDone (finishSeq).
		e.releaseRun(chain, subj)
		return true
	}
	step, _ := stepIn(facts)
	last := len(carrier.Seq) - 1
	if step > last {
		// Every element completed: the sequence's own onDone, about what a finish is about: the
		// end of the run's last task, or the subject where it had none.
		ev := entity.Event{Ref: subj}
		if ent, ok, err := e.store.Get(subj); err == nil && ok {
			ev.To = ent.Payload
		}
		for i := last; i >= 0; i-- {
			if carrier.Seq[i].Spawn == nil {
				continue
			}
			ref := mirror.Ref(spawnID(e.idOwner(), chainID(seqChain(chain, i)), subj))
			if ent, ok, err := e.store.Get(ref); err == nil && ok && mirror.OccurrenceOf(ent.Payload) >= since {
				ev = entity.Event{Ref: ref, To: ent.Payload}
			}
			break
		}
		e.finishSeq(chain, *carrier, e.hookOccasion(rule, ev), subj, fromCause)
		return true
	}
	el, elChain := carrier.Seq[step], seqChain(chain, step)
	if el.Spawn == nil {
		e.recordHookTrouble(subj, elChain, "", 0, errRestarted)
		e.publishSeq(chain, 0, subj, entity.Obj{seqEnded: seqEndedError, seqReason: errRestarted.Error()})
		e.releaseRun(chain, subj)
		return true
	}
	return e.settleTask(spawnID(e.idOwner(), chainID(elChain), subj), subj, elChain, since)
}

// settleTask ends the task at id again, from its record, as the end of a task the engine tracks
// is taken, and reports whether it did. A record that stands with no end is given one, failed
// and orphaned: nothing is running the task and nothing will say how it ended. Where no record
// of this run stands (since), none is written, and the end is handed to the hook as it would
// have read.
func (e *Engine) settleTask(id string, subj entity.Ref, chain string, since int64) bool {
	ref := mirror.Ref(id)
	st := entity.Obj{}
	ent, ok, err := e.store.Get(ref)
	if err != nil {
		e.logger.Warn("a claim could not be settled: its task's record could not be read", "task", id, "err", err)
		e.unsettled(err)
		return false
	}
	if ok && mirror.OccurrenceOf(ent.Payload) < since {
		ok = false // the run before's
	}
	if ok {
		if o := entity.ToObj(ent.Payload); o != nil {
			st = o
		}
	}
	status, _ := st["status"].(string)
	if !task.TerminalStatus(status) {
		end := mirror.Orphaned(mirror.OrphanedNoAccount)
		if ok {
			e.write(ref.At(), end.Payload())
		}
		for k, v := range end {
			st[k] = v
		}
		if _, said := st["flow"]; !said {
			st["flow"] = subj.String()
		}
		if _, said := st["chain"]; !said {
			st["chain"] = chain
		}
		status = string(task.Failed)
	}
	e.owe()
	e.applyHook(flowTask{id: id, subject: subj.String(), chain: chain}, task.Status(status), entity.Event{Ref: ref, To: st.Payload()})
	return true
}

// Owner is the prefix on every id this engine's rules mint: empty for the root package's.
func (e *Engine) Owner() string { return e.idOwner() }

// OwnerOf is the owner prefix an id carries, the Owner of the engine whose rule minted it. It
// ends at the last AtSep ahead of the id's ChainSep, since neither stands in a chain.
func OwnerOf(id string) string {
	head, _, _ := strings.Cut(id, ChainSep)
	return head[:strings.LastIndex(head, AtSep)+1]
}
