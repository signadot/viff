package trigger

import "fmt"

// checkLatch refuses a spawning action that can never stop spawning.
//
// A trigger is evaluated at every commit that touches what it reads, so what ends a firing is
// not the dispatcher but the charter. A store write ends itself: rewriting the same value
// produces no commit, so it raises no next evaluation. A SPAWN does not — it is not a store
// write, nothing about it is inert on repetition, and the engine's dedupe holds only while the
// task is LIVE (Engine.spawnHooked). Once it is reaped, the next commit spawns again, and
// the one after that.
//
// So a spawning action has to take its subject OUT of the selection: write a field the
// subject's predicate constrains, and the next evaluation does not select it. The canonical
// shape already does — spawn, then `onDone: {patch: {stage: reviewed}}` against a subject
// asking for `stage: open` — which is why hooks and sequence elements count as latches: the
// task is live in the meantime, and its completion advances the subject.
//
// What it decides is whether the subject's predicate and the action's post-state are
// UNSATISFIABLE — can the subject still match once the action has run. That relation is
// deselect.go, which answers in three parts: it latches, it definitely does not, or the check
// cannot see. Only the middle one is refused. Here is what to say when it is.
func checkLatch(c Condition, a Action) error {
	if !spawns(a) {
		return nil
	}
	pred := c.Subject.Predicate()
	if pred == nil {
		return fmt.Errorf("this action spawns, and its subject has no payload predicate to " +
			"latch: a spawn is not a store write, so nothing about it is inert on repetition, " +
			"and the engine's dedupe lasts only while the task is live. Give the subject a " +
			"predicate the action advances past — `value: {stage: open}` with a patch (or an " +
			"`onDone`) writing `stage` — so the next evaluation does not select it again")
	}
	verdict := latchOf(pred, a)
	if verdict == latchHolds || verdict == latchUnsure {
		return nil
	}
	reads := readPaths(pred)
	if verdict == latchUntouched {
		return fmt.Errorf("this action spawns but writes nothing its subject reads (%s), so "+
			"every commit that touches it spawns again: a spawn is not a store write and the "+
			"engine's dedupe lasts only while the task is live. Patch one of those paths — "+
			"here, or in an `onDone`, which is the ordinary shape: spawn the work, and let its "+
			"completion advance the subject past the state that asked for it", pathList(reads))
	}
	return fmt.Errorf("this action spawns and writes what its subject reads (%s), but what it "+
		"writes still SATISFIES the predicate, so the subject stays selected and the next commit "+
		"that touches it spawns again. Writing the value the predicate already asks for is a "+
		"latch for a store write — rewriting a value raises no commit — and not for a spawn, "+
		"which repeats however little changed. Advance the path to a value the predicate refuses "+
		"(`value: {stage: open}` with `patch: {stage: reviewed}`), or ask for the ABSENCE of what "+
		"the action writes (`value: !not {validated: null}` with `patch: {validated: …}`), which "+
		"latches whatever value lands", pathList(reads))
}

// spawns reports whether an action starts a task that only the SUBJECT PREDICATE can bound.
// Two kinds of spawn are excluded, both because something else already bounds them:
//
// A HOOK's spawn runs once per completion of the thing above it, so it is bounded by that.
//
// A SEQUENCE's spawns are bounded by the sequence's own durable claim: startSeq refuses to
// re-run a sequence that has completed for this subject at this shape, and that claim is a
// fact in the store rather than a live-task check, so it survives the reap. A seq is the
// answer to this problem one level down, which is why demanding a subject latch of it as well
// would be demanding the same guarantee twice.
func spawns(a Action) bool { return a.Spawn != nil }
