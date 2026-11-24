package trigger

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/task"
)

// Running a sequence.
//
// The facts below are not decoration. A parallel branch is woken by a `when`, and a `when`
// matches facts — so a sequence must PUBLISH its position or the breadth axis (independent
// triggers) cannot observe the depth axis (this) at all. They are also where re-fire
// idempotence stands: between two elements NO task is live, so a registry-only check has a
// hole at exactly every step boundary, which is where a re-fire is most likely to land.
//
// Everything lands under the reserved root on the subject, in its charter half, keyed by
// trigger:
//
//	@sys:
//	  charter:
//	    review:
//	      step:  2         elements completed
//	      steps: 3         len(seq), known at install
//	      shape: "1f2c…"   hash of the declared sequence
//	      done:  true      latched when step reaches steps
//	      ended: failed    the terminal status an off-ramp stopped it at, or `error`
//	      reason: "…"      with `error`: why the element after a completion could not run
//
// `done` and `ended` are the two ways a run CONCLUDES, and a run that concluded either way does
// not run again at this shape (seqConcluded). They are separate fields rather than one, because
// they are separate facts about the subject: `@sys.charter.review.done` is a documented thing
// to write a rule about, and "it finished" must not start meaning "it stopped somewhere".
//
// One reserved name rather than a family of flat keys, because the payload is the AUTHOR's
// document — `flow` on a task entity is the same kind of reservation. The `charter` half
// keeps rule bookkeeping apart from what viff records about the ENTITY (@sys.meta, where the
// lifecycle marker lives): they grow differently — a key per rule against a field per lifecycle
// concern — and flat they collided, since a trigger NAMED `vanishing` would have written its
// progress exactly where the marker goes. See entity.ReservedRoot and docs/trigger-model.md.

// ViffRoot and CharterKey address the engine's own bookkeeping: @sys.charter.<trigger>. The
// reservation is the ENTITY's (entity.ReservedRoot), aliased here because the charter grammar
// is what a reader of this package is looking for.
const (
	ViffRoot   = entity.ReservedRoot
	CharterKey = entity.CharterKey
)

// Progress field names under @sys.charter.<trigger>.
const (
	seqStep  = "step"
	seqSteps = "steps"
	seqShape = "shape"
	seqDone  = "done"
	seqEnded = "ended"
	// seqReason says why a run ended at seqEndedError: the error of the element that could not
	// continue off its task's completion (failResumed).
	seqReason = "reason"
)

// seqEndedError is what `ended` holds when an element resumed off a task's completion failed,
// rather than a task reaching an off-ramp status.
const seqEndedError = "error"

// startSeq begins carrier's sequence about subj, unless one is already running for it.
func (e *Engine) startSeq(chain string, carrier Action, oc occasion, subj entity.Ref, src refSource) {
	// CLAIM FIRST, then decide — not the other way round, and not because it reads better.
	// Checking `done` before claiming leaves a window: a firing reads "not done", the run
	// already in progress finishes and RELEASES, and the reader then wins a free claim and
	// starts a second run. That interleaving is not hypothetical; it reproduced about one run
	// in three.
	//
	// The rule it teaches: every predicate guarding a decision must be evaluated UNDER the
	// exclusive thing, or the exclusion guards nothing.
	if !e.claimRun(chain, subj, entity.Obj{"occurrence": oc.commit.Rev}) {
		e.tracef("seq %s: another run holds the claim for %s", chain, subj)
		return
	}
	// From here the decision is exclusive. `done` is a POLICY about repeating; the claim is a
	// MECHANISM for exclusion. They answer different questions, and fusing them is what forced
	// the autonomy to be maximally strict back when the mechanism could not be trusted.
	shape := e.shapeOf(chain)
	if e.seqConcluded(chain, subj, shape) {
		e.tracef("seq %s: already ran to a conclusion for %s at this shape; not re-running", chain, subj)
		e.releaseRun(chain, subj)
		return
	}
	e.clearConclusion(chain, subj)
	e.publishSeq(chain, oc.parent, subj, entity.Obj{
		seqStep:  0,
		seqSteps: len(carrier.Seq),
		seqShape: shape,
	})
	e.advanceSeq(chain, carrier, 0, oc, subj, src, false)
}

// clearConclusion removes what a run before left on the subject of how it concluded. A run
// starts with none: what a run at an earlier shape left says nothing of this one, and read as
// this one's it is a run that has concluded (settleRun). Each is removed at its path, where it
// stands (clearHookFailure says why), so a subject that never ran the sequence is not written.
func (e *Engine) clearConclusion(chain string, subj entity.Ref) {
	facts := e.seqFacts(chain, subj)
	trig := mirror.TriggerOf(chain)
	for _, field := range []string{seqDone, seqEnded, seqReason} {
		if entity.Get(facts, field) == nil {
			continue
		}
		if _, _, err := e.caused(chain, 0).Write(entity.Write{At: subj.At(ViffRoot, CharterKey, trig, field)}); err != nil {
			e.logger.Warn("a sequence's earlier conclusion could not be cleared from its subject",
				"chain", chain, "subject", subj.String(), "field", field, "err", err.Error())
		}
	}
}

// advanceSeq runs elements from i onward until one SPAWNS — at which point the sequence waits
// for that task to complete — or until the sequence ends.
//
// The loop exists because an element need not spawn: `seq: [{patch: …}, {patch: …}]` is two
// ordered writes, and an element that starts no task completes immediately. Without the
// loop such an element would stall the sequence forever, waiting for a completion that no
// task will ever report.
//
// resumed says the elements run off a task's completion (resumeSeq) rather than off a firing
// (startSeq). There an element is a completion hook in all but name — it reads the task's facts,
// and the viff as it is when the task ended — and a failure is one too: see failResumed.
func (e *Engine) advanceSeq(chain string, carrier Action, i int, oc occasion, subj entity.Ref, src refSource, resumed bool) {
	for ; i < len(carrier.Seq); i++ {
		el := carrier.Seq[i]
		elChain := seqChain(chain, i)
		h, err := el.applyOne(elChain, oc, subj, e, src)
		if errors.Is(err, errHeld) {
			// The element's own hooked spawn is held — a task of it is still ending about this
			// subject, its onFailed not yet run — which is no failure of the element and no element
			// that ran: this run releases, and the next commit tries again.
			e.tracef("seq %s: element %d is held (%v); sequence stops", chain, i, err)
			e.releaseRun(chain, subj)
			return
		}
		if errors.Is(err, errParked) {
			// A suspension holds what the element would start. Its writes have landed and its
			// launch waits for the lift, so the element is in flight as one whose task is running
			// is: the run keeps its claim, and the task the lift starts resumes it when it ends.
			e.tracef("seq %s: element %d is parked; the sequence waits for the lift", chain, i)
			if resumed {
				e.clearHookFailure(subj, elChain)
			}
			return
		}
		if err != nil && resumed {
			e.failResumed(chain, elChain, oc, subj, err)
			return
		}
		if err != nil {
			// A failed write ends the sequence where it stands. The step count is left at the
			// last element that really completed, so the facts do not claim progress that did
			// not happen.
			//
			// Deliberately NOT recorded as `ended`, which is the difference between this and an
			// off-ramp (stopSeq): a write that failed started nothing, and the reasons it fails
			// are the transient ones — a reference that did not resolve at this commit, a store
			// that was unavailable. Recording it would make a moment permanent. So this releases
			// and lets the next commit try again, which costs an evaluation and no task.
			e.tracef("seq %s: element %d failed (%v); sequence stops", chain, i, err)
			e.releaseRun(chain, subj)
			return
		}
		if resumed {
			e.clearHookFailure(subj, elChain)
		}
		if h != nil {
			return // in flight; its completion resumes us
		}
		e.publishSeq(chain, oc.parent, subj, entity.Obj{seqStep: i + 1})
	}
	e.finishSeq(chain, carrier, oc, subj, src)
}

// failResumed ends a sequence whose element, resumed off a task's completion, failed.
//
// It is the same event as a lost completion hook — the rule could not continue from its firing's
// world — so it is written where one is: @sys.charter.<rule>.hook on the subject, naming the
// element's chain and the task that completed, and `hook` on that task's record
// (recordHookFailure). The next clean hook or element of the rule removes it.
//
// And the run ENDS, with the reason, as an off-ramp ends it (stopSeq). advanceSeq's other
// failure path releases and lets the next commit try again, because off a firing the reasons are
// transient. Off a completion they are not: the element reads the finished task's facts, and a
// missing `data` field does not appear in an answer already given, nor does any commit wake a
// run that is waiting on a completion. Left at its step count the run would be dead and look
// alive. Ended, it is concluded at this shape like any off-ramp (seqConcluded).
//
// The record goes down before the claim comes back, for the reason stopSeq gives.
func (e *Engine) failResumed(chain, elChain string, oc occasion, subj entity.Ref, err error) {
	e.tracef("seq %s: element %s failed off a completion (%v); sequence ends", chain, elChain, err)
	if mirror.IsTask(oc.ev.Ref) {
		e.recordHookFailure(oc.ev.Ref, subj, elChain, occurrenceOf(oc), err)
	} else {
		e.recordHookTrouble(subj, elChain, "", occurrenceOf(oc), err)
	}
	e.publishSeq(chain, 0, subj, entity.Obj{seqEnded: seqEndedError, seqReason: err.Error()})
	e.releaseRun(chain, subj)
}

// occurrenceOf is the firing a hook's or a resumed element's trouble is about: the commit the
// rule fired at, off the task's record where the occasion is a task's completion, and the firing's
// own commit where a sequence of bare patches finished off the firing itself.
func occurrenceOf(oc occasion) int64 {
	if mirror.IsTask(oc.ev.Ref) {
		return mirror.OccurrenceOf(oc.ev.To)
	}
	return oc.commit.Rev
}

// finishSeq fires the carrier's own onDone — the hook that means "after the whole sequence"
// rather than "after one task" — and latches `done`.
//
// `done` is PUBLISHED rather than derived, because a match compares a value against a pattern
// and never one field of a payload against another: a join written as `step == steps` cannot
// be expressed, and one written as `step: 3` silently stops meaning "finished" the moment the
// sequence gains an element.
//
// THE HOOK, THEN `done`, THEN THE CLAIM. `done` says the sequence and its onDone have run, so a
// run found holding its claim says by its facts what it is owed (settleRun): `step` at `steps`
// and no `done` is a run whose onDone is owed, and `done` is a run that owes nothing but its
// claim. That second is also what a firing leaves that claimed a run already concluded and went
// no further (startSeq), and the two are settled alike.
func (e *Engine) finishSeq(chain string, carrier Action, oc occasion, subj entity.Ref, src refSource) {
	defer e.releaseRun(chain, subj)
	defer e.publishSeq(chain, oc.parent, subj, entity.Obj{seqStep: len(carrier.Seq), seqDone: true})
	if carrier.OnDone != nil {
		hook := chain + "." + "onDone"
		if err := carrier.OnDone.apply(hook, oc, []entity.Ref{subj}, e, src); err != nil {
			// Record it against the task whose completion got us here. A sequence can also
			// finish without one — every element being a bare patch completes immediately — and
			// then ev is the original triggering change, whose ref is the subject rather than a
			// task. There is no task to hang it on in that case, so only the subject says.
			if mirror.IsTask(oc.ev.Ref) {
				e.recordHookFailure(oc.ev.Ref, subj, hook, occurrenceOf(oc), err)
			} else {
				e.recordHookTrouble(subj, hook, "", occurrenceOf(oc), err)
			}
			return
		}
		e.clearHookFailure(subj, hook)
	}
}

// stopSeq ends the run an OFF-RAMP element belonged to: a sequence element that reached any
// terminal status but `done`. It is a no-op for a chain that addresses no sequence element,
// which is every ordinary completion hook.
//
// The run's conclusion is what stops it running again (seqConcluded), and not its claim: a
// claim left standing would hold the subject, and a repeat policy that turns on whether
// anybody restarted the server is not a policy.
//
// The record goes down BEFORE the claim comes back, for the reason startSeq takes the claim
// before reading `done`: between the two there is a window, and whatever is not yet written in
// it is a decision the next firing gets to make wrong. Ordered this way there is no state in
// which the claim is free and the run's conclusion is unrecorded.
//
// `step` is not touched: advanceSeq advances it only when an element COMPLETES, so it already
// names the last one that did, and the element that failed is `step`+1.
func (e *Engine) stopSeq(chain string, subj entity.Ref, status task.Status) {
	carrier, idx, ok := seqIndexOf(chain)
	if !ok {
		return
	}
	// Only while `step` has not passed the element, as an advance is (resumeSeq): an end run a
	// second time is not the run's to stop.
	if step, ok := e.seqStep(carrier, subj); ok && step > idx {
		return
	}
	// No causing commit — an off-ramp is downstream of a task finishing, which is where a
	// causal chain ends (causal.go), the same parent resumeSeq publishes progress under.
	e.publishSeq(carrier, 0, subj, entity.Obj{seqEnded: string(status)})
	e.releaseRun(carrier, subj)
}

// resumeSeq continues the sequence a just-completed element belonged to. Returns false if the
// chain does not address a sequence element, in which case the ordinary hook path applies.
func (e *Engine) resumeSeq(chain string, subj entity.Ref, oc occasion) bool {
	// oc is the completion's occasion, built by applyHook: the finished task's event for its
	// references, and the head for reads at a revision — the viff as it is now that the task
	// has ended, so an element sees what the elements before it wrote. A resumed element begins
	// a fresh causal chain (oc.parent is 0).
	carrierChain, idx, ok := seqIndexOf(chain)
	if !ok {
		return false
	}
	// The run's next steps are work that may launch, counted until they have run: a close waits
	// for them, and a run whose element ends once its charter is being taken down ends here,
	// cancelled, and starts nothing.
	if !e.begin() {
		e.stopSeq(chain, subj, task.Cancelled)
		return true
	}
	defer e.work.Done()
	oc.begun = true
	t, ok := e.Rule(mirror.TriggerOf(chain))
	if !ok {
		e.tracef("seq %s: trigger gone; sequence cannot continue", chain)
		e.releaseRun(carrierChain, subj)
		return true // it WAS a sequence element; there is just nothing left to advance into
	}
	carrier, ok := navigate(t, carrierChain)
	if !ok {
		e.tracef("seq %s: carrier %s no longer resolves; sequence stops", chain, carrierChain)
		e.releaseRun(carrierChain, subj)
		return true
	}
	// The charter may have moved under a running sequence — an inserted element shifts every
	// index below it, so the successor at seq[idx+1] would be a DIFFERENT action than the one
	// this sequence started under. The shape hash is what notices.
	if want := e.shapeOf(carrierChain); !e.shapeMatches(carrierChain, subj, want) {
		e.tracef("seq %s: the trigger's sequence changed while it was running; stopping rather "+
			"than running whatever now sits at element %d", carrierChain, idx+1)
		e.releaseRun(carrierChain, subj)
		return true
	}
	// AN ELEMENT'S END ADVANCES THE RUN ONLY WHILE `step` HAS NOT PASSED THAT ELEMENT. An end can
	// be run a second time: viff ended after the run had advanced and before the element's own
	// claim was released, and the claim found at the next start is settled by ending its task
	// again (settleClaim). Advancing again would run the next element beside its own live task.
	// So the second end does nothing here, and the element's claim is released by the caller.
	// A `step` that was not written (publishSeq) leaves the run to advance as it would have.
	if step, ok := e.seqStep(carrierChain, subj); ok && step > idx {
		return true
	}
	e.publishSeq(carrierChain, oc.parent, subj, entity.Obj{seqStep: idx + 1})
	// A resumed sequence is downstream of the task that just completed, so its elements
	// read that task's facts — result and the rest — exactly as a hook does.
	e.advanceSeq(carrierChain, *carrier, idx+1, oc, subj, fromCause, true)
	return true
}

// seqStep is how many elements of the run at chain have completed about subj, as the subject
// says it; ok is false where the subject says nothing.
func (e *Engine) seqStep(chain string, subj entity.Ref) (int, bool) {
	return stepIn(e.seqFacts(chain, subj))
}

func stepIn(facts entity.Payload) (int, bool) {
	n := ir.Uncomment(entity.Get(facts, seqStep))
	if n == nil || n.Type != ir.NumberType || n.Int64 == nil {
		return 0, false
	}
	return int(*n.Int64), true
}

// seqConcluded reports whether this sequence has ALREADY RUN TO A CONCLUSION for this subject
// at this shape. It is a repeat policy and nothing else: overlap is the claim's job (claimRun),
// and separating the two is what let each shrink to something defensible.
//
// It no longer consults the live registry or the step count, because the claim covers both
// windows — including the one between elements, where nothing is live and the registry had
// nothing to say. What is left is the question the registry could never answer: should a
// sequence that already ran run again?
//
// TWO CONCLUSIONS, one autonomy. A run that finished is `done`; a run that stopped at an off-ramp
// is `ended`. Asking about both here is what lets a stopped run's claim be released (stopSeq),
// which is what keeps the mechanism
// and the autonomy apart — the separation startSeq's own comment names as the thing that let each
// shrink.
//
// Scoped to the shape, so editing the sequence frees it — the same signal that stops a live
// sequence walking into a charter that moved under it, and the one escape hatch a stopped run
// has that does not depend on restarting anything.
func (e *Engine) seqConcluded(chain string, subj entity.Ref, shape string) bool {
	p := e.seqFacts(chain, subj)
	if p == nil {
		return false
	}
	d := entity.Get(p, seqDone)
	if (d == nil || !d.Bool) && entity.Str(p, seqEnded) == "" {
		return false
	}
	if got := entity.Str(p, seqShape); got != "" && got != shape {
		return false
	}
	return true
}

// seqFacts reads viff.<trigger-of-chain> off the subject, or nil.
func (e *Engine) seqFacts(chain string, subj entity.Ref) entity.Payload {
	facts, err := e.readSeqFacts(chain, subj)
	if err != nil {
		// SAID, not folded into "no facts". These facts decide where a sequence has got to, so
		// a read that did not happen looks exactly like a step that has not run.
		e.logger.Warn("a sequence's facts could not be read; it will be judged as though the "+
			"subject carries none", "chain", chain, "subject", subj.String(), "err", err)
		return nil
	}
	return facts
}

// readSeqFacts is seqFacts for a caller that tells no facts from facts unread.
func (e *Engine) readSeqFacts(chain string, subj entity.Ref) (entity.Payload, error) {
	ent, ok, err := e.reader(mirror.TriggerOf(chain)).Get(subj)
	if err != nil || !ok {
		return nil, err
	}
	return entity.Get(entity.Get(entity.Get(ent.Payload, ViffRoot), CharterKey), mirror.TriggerOf(chain)), nil
}

// shapeMatches reports whether the sequence a subject is partway through is still the one the
// charter declares.
func (e *Engine) shapeMatches(chain string, subj entity.Ref, want string) bool {
	p := e.seqFacts(chain, subj)
	if p == nil {
		return true // nothing recorded: nothing to contradict
	}
	got := entity.Str(p, seqShape)
	return got == "" || got == want
}

// publishSeq patches progress under the reserved root. It goes through the same patch path an
// action's own write does, which is exactly why `patch` had to replace `set`: a shallow
// overlay here would replace the whole `viff` key and erase a sibling trigger's progress.
//
// It carries the firing's causal chain for the same reason (see causal.go): progress lands on
// the SUBJECT, under a root a charter can match on — `viff.review.done` is a documented thing
// to write a rule about — so a progress write can wake a rule exactly as an action's own write
// can, and a hop it did not count would be a hop the throttle cannot see. parent is 0 wherever
// the sequence resumed off a task completing, which is the world coming back and the right
// place for a chain to end.
func (e *Engine) publishSeq(chain string, parent int64, subj entity.Ref, fields entity.Obj) {
	patch := entity.Obj{ViffRoot: entity.Obj{CharterKey: entity.Obj{mirror.TriggerOf(chain): fields}}}.Payload()
	if err := e.patchRecord(chain, parent, subj, patch); err != nil {
		e.tracef("seq %s: publishing progress on %s failed: %v", chain, subj, err)
	}
}

// recordShapes fingerprints every sequence in a trigger, ONCE, at install — keyed by the
// chain of the action carrying it.
//
// Computed here rather than on each completion for two reasons. The shape is a constant of the
// charter, so recomputing it per step is work for nothing; and — the reason it is not merely
// tidy — the generated ToTonyIR runs the action's nodes through ir.FromMap, which RE-PARENTS
// them. Hashing an installed trigger from the completion path therefore mutates nodes another
// goroutine may be reading, which the race detector duly caught. At install the trigger is not
// yet live and nothing else holds it.
func (e *Engine) recordShapes(t Trigger) {
	shapes := map[string]string{}
	collectShapes(t.Name, &t.Action, shapes)
	if len(shapes) == 0 {
		return
	}
	e.shapeMu.Lock()
	for k, v := range shapes {
		e.shapes[k] = v
	}
	e.shapeMu.Unlock()
}

// forgetShapes drops a removed trigger's fingerprints.
func (e *Engine) forgetShapes(name string) {
	e.shapeMu.Lock()
	for k := range e.shapes {
		if mirror.TriggerOf(k) == name {
			delete(e.shapes, k)
		}
	}
	e.shapeMu.Unlock()
}

// shapeOf returns the fingerprint recorded for a sequence carrier, or "" if it holds none.
func (e *Engine) shapeOf(chain string) string {
	e.shapeMu.Lock()
	defer e.shapeMu.Unlock()
	return e.shapes[chain]
}

// collectShapes walks an action tree, recording a hash at every chain that carries a sequence.
// A trigger may hold more than one — nesting a seq inside a seq is rejected, but a completion
// hook of the root action may carry its own.
func collectShapes(chain string, a *Action, into map[string]string) {
	if len(a.Seq) > 0 {
		into[chain] = seqShapeHash(a.Seq)
	}
	for i := range a.Seq {
		collectShapes(seqChain(chain, i), &a.Seq[i], into)
	}
	for _, h := range []struct {
		field string
		act   *Action
	}{
		{"onDone", a.OnDone},
		{"onFailed", a.OnFailed},
		{"onCancelled", a.OnCancelled},
	} {
		if h.act != nil {
			collectShapes(chain+"."+h.field, h.act, into)
		}
	}
}

// seqShapeHash fingerprints a declared sequence, so a charter edit under a RUNNING sequence is
// detectable. A hash rather than a stored copy of the actions: these tasks can run for a
// very long time, and a retained snapshot per live sequence would be a retention floor the
// store could never compact past. Detecting that the shape moved is all refusing to continue
// needs — reconstructing the old one is not.
func seqShapeHash(seq []Action) string {
	h := fnv.New32a()
	for i, el := range seq {
		n, err := el.ToTonyIR()
		if err != nil {
			// Unrenderable is its own fingerprint: it must not collide with a real shape.
			fmt.Fprintf(h, "%d:<unrenderable>", i)
			continue
		}
		b, err := codec.Marshal(n, codec.Tony)
		if err != nil {
			fmt.Fprintf(h, "%d:<unmarshalable>", i)
			continue
		}
		fmt.Fprintf(h, "%d:%s", i, b)
	}
	return fmt.Sprintf("%08x", h.Sum32())
}

// chainUnder reports whether target lies at or below prefix, comparing SEGMENTS rather than
// characters — so `review.seq` does not match a trigger named `review.sequel` (which cannot
// exist today, since a trigger name may not contain a dot, but the check should not depend on
// that being true forever).
func chainUnder(prefix, target string) bool {
	p, err := kpath.Parse(prefix)
	if err != nil {
		return false
	}
	t, err := kpath.Parse(target)
	if err != nil {
		return false
	}
	return p.MatchesPrefix(t)
}

// ── claiming a run ───────────────────────────────────────────────────────────

// claimRun takes the exclusive claim on a sequence for a subject, atomically. It reports
// whether this caller won it.
//
// The claim is what makes overlap impossible rather than unlikely. Engine.dispatch ends in
// `go e.fire(...)`, so two firings genuinely race, and the check they used to race on lived in
// a Go map — which settles nothing across two goroutines that both read "absent", and which a
// restart forgets entirely. A create-only CommitIf settles it in the store: exactly one create
// wins, and the loser does nothing.
//
// It is held for the WHOLE sequence, not per element, which is what closes the gap between
// elements where no task is live and the registry therefore had nothing to say.
//
// A hooked spawn's hold on its subject is the same claim (applyOne), marked on its payload,
// since a restart settles the two by different rules (Settle): more is what the mark adds.
func (e *Engine) claimRun(chain string, subj entity.Ref, more entity.Obj) bool {
	ref := mirror.ClaimRef(spawnID(e.idOwner(), chainID(chain), subj))
	// `taken` is which claim this is: a claim's address is the same from one run to the next, and
	// a release is of the claim it read (releaseRun).
	payload := entity.Obj{"flow": subj.String(), "chain": chain, takenField: time.Now().UTC().Format(time.RFC3339Nano)}
	for k, v := range more {
		payload[k] = v
	}
	won, _, err := e.store.Write(entity.Write{At: ref.At(), Value: payload.Payload(), When: []entity.Require{entity.Created()}})
	if err != nil {
		e.tracef("seq %s: claiming %s failed: %v", chain, ref, err)
		return false
	}
	return won
}

// releaseRun drops the claim when a sequence ends, however it ends. A claim that outlives its
// run would block the next one forever, so every exit path releases.
//
// THE RELEASE ALWAYS LANDS, AND TAKES ONLY THE CLAIM IT READ. The claim is read first and removed
// on the condition that it is still what was read, which `taken` makes this claim and no other. So a release may be asked
// again: where the store did not answer, the removal may or may not have landed, and by the next
// attempt another run may hold a claim at the same address, which the condition leaves alone.
//
// It is tried here, so that a caller finds the claim gone when this returns, which is the
// ordinary case and what the result reports. Where the store did not take it, it is asked again
// in the background for as long as the engine runs, and late runs once it has landed: nothing
// else removes a claim of this viff's while it is up, so a release that was dropped is a subject the rule
// never fires about again.
func (e *Engine) releaseRun(chain string, subj entity.Ref, late ...func()) bool {
	ref := mirror.ClaimRef(spawnID(e.idOwner(), chainID(chain), subj))
	var when []entity.Require
	read := false
	release := func() error {
		if !read {
			ent, ok, err := e.store.Get(ref)
			if err != nil {
				return err
			}
			if !ok {
				return nil // nothing stands there
			}
			when = []entity.Require{{Pattern: entity.Unchanged(ent.Payload)}}
			read = true
		}
		// Not committed is released too: the claim this read is no longer the one standing.
		_, _, err := e.store.Write(entity.Write{At: ref.At(), Value: nil, When: when}) // nil removes it
		return err
	}
	err := release()
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false // the store is closed
	}
	e.logger.Warn("a claim's release did not land; it is being asked again", "at", ref.String(), "err", err)
	go func() {
		ctx := e.bgctx
		if ctx == nil {
			ctx = context.Background() // not started (a direct call in a test)
		}
		for wait := 250 * time.Millisecond; ; wait = min(2*wait, 8*time.Second) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			err := release()
			if errors.Is(err, context.Canceled) {
				return
			}
			if err == nil {
				for _, f := range late {
					f()
				}
				return
			}
		}
	}()
	return false
}

// takenField is the field of a claim that says when it was taken.
const takenField = "taken"
