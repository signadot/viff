package trigger

import (
	"context"
	"errors"
	"sort"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/launch"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/task"
)

// flowTask is one LIVE task: its id (for reap, by its record's ref), the subject it is
// about, and its governing handle (the control target — a *Task locally, a remote handle
// behind a runner URL later). The engine holds handles, not concrete tasks.
type flowTask struct {
	id      string
	subject string // the resolved subject this task is about — a flow is a query over these
	h       task.Handle
	// hooks is the spawning action, carried so the engine can fire its completion hook (onX)
	// when THIS task reaches a terminal status — scoped to this task, not every task
	// about the subject. nil for a task not spawned here: one adopted with a chain has its
	// action looked up by it when it ends (hooksOf), and one with none has no action.
	hooks *Action
	// within is what the hooks read by (occasion.within): the narrowing of the firing that
	// spawned the task, worked out from the rule that fired, so that a hook reads what its own
	// rule reads whatever has replaced that rule since. Only with hooks; nil reads whole.
	within [][]string
	// chain is this spawn's position in its trigger's action tree, as a KPATH: the trigger
	// name at the top, then review.onDone, review.onDone.onFailed, … So it is not a private
	// naming scheme but the address of the spawning Action inside the charter — which is
	// what makes a hook's spawn id distinct and deterministic, and what a reader (or, later,
	// a resume) can navigate back to the action that owns it.
	chain string
	// occurrence is the commit that caused the firing this task belongs to (launch.Spec's
	// Occurrence), 0 where none did. It names the firing a run was about, on the record and in a
	// hook's trouble (recordHookTrouble). The engine's own copy says which run a record at the
	// task's id is about (reapEnded), and stands in for a record that does not say, because
	// nothing mirrored one before the hook ran — a launch the engine refused, whose task never
	// reached a runner.
	occurrence int64
	// WHOSE HOLD it is at its boundary, since a pause is one bool in the runtime and two parties
	// take it: an operator's `flow suspend --task` (operator), and the suspensions that paused it
	// (suspended). A lift resumes only what nothing else holds, and `flow resume --task` does not
	// resume what a suspension holds. See heldBy.
	operator  bool
	suspended []string
}

// heldBy is who holds the task, the operator first: empty when nothing does.
func (fp flowTask) heldBy() []string {
	var out []string
	if fp.operator {
		out = append(out, HeldByOperator)
	}
	return append(out, fp.suspended...)
}

// HeldByOperator is how a task's `heldBy` names an operator's `flow suspend --task`.
const HeldByOperator = "operator"

func (e *Engine) Spawn(id string, spec launch.Spec) task.Handle {
	return e.spawnHooked(id, spec, nil, "")
}

// engineActor is the name the engine acts under on a task's handle: who paused, resumed or
// cancelled it, for the record. The engine is viff's own, so it is the system's.
const engineActor = "system:trigger"

// track registers a live task. Its record ends it: reap retires it and runs its completion hook
// when the record reaches a terminal status, and nothing else does. A task a suspension covers
// is held as it is tracked — a task re-attached after a restart, or one run by hand about a
// suspended subject — since nothing else will pause it.
//
// WORK THAT NEVER STARTED IS NOT TRACKED. A launch refused before anything ran (task.NeverStarted)
// has already ended, and it may have no record for reap to see: a spawn refused because its id
// stands as another party's is deliberately not mirrored. So its end is taken here, as `failed`
// with the refusal for its reason, and its hook runs as a started task's would.
//
// It answers false for a task tracked after the engine's work was dropped (Stop, CancelLive),
// which is cancelled and is not the engine's.
func (e *Engine) track(fp flowTask) bool {
	if why, never := task.NeverStarted(fp.h); never {
		e.liveMu.Lock()
		dropped := e.dropped != ""
		e.liveMu.Unlock()
		if !dropped { // dropped work runs no hooks (CancelLive)
			e.owe()
			go e.applyHook(fp, task.Failed, e.neverStarted(fp, why))
		}
		return true
	}
	held := e.settled(mirror.TriggerOf(fp.chain), subjectOf(fp.subject))
	fp.suspended = held
	e.liveMu.Lock()
	if why := e.dropped; why != "" {
		// A firing that was already past its gate when the work was dropped: what it started
		// has no program left to belong to.
		e.liveMu.Unlock()
		fp.h.Cancel(why, engineActor)
		return false
	}
	e.live[fp.id] = fp
	e.liveMu.Unlock()
	if len(held) > 0 {
		fp.h.Pause(engineActor)
		e.recordHold(fp)
	}
	go e.reapEnded(fp)
	return true
}

// reapEnded takes the end of a task whose record was terminal before the task was tracked. The
// task's mirror is attached before the task is handed to track, so a task that ends at once can
// have its terminal record pass reap ahead of the registration reap looks for, and nothing
// would write that record again. One read of the record, after the registration, closes that.
//
// The record must be this run's: an id names the work, and the run before left its terminal
// record at the same address. A run's mirror replaces that record before the task is tracked
// (mirror.Reflect), and where the firing's commit is known the record has to name it too. A task
// tracked with no firing behind it has only the first of the two, and a mirror whose opening
// write did not land leaves the earlier run's record to be taken for this one's.
//
// A read the store did not answer is asked again, for as long as the engine runs.
func (e *Engine) reapEnded(fp flowTask) {
	// Once the engine is open, and no sooner: this is the read that takes an end reap let pass
	// while it was not.
	if !e.awaitOpen() {
		return
	}
	ref := mirror.Ref(fp.id)
	var ent entity.Entity
	for wait := 250 * time.Millisecond; ; wait = min(2*wait, 8*time.Second) {
		got, ok, err := e.store.Get(ref)
		if err == nil {
			if !ok {
				return
			}
			ent = got
			break
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		ctx := e.bgctx
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	status := entity.Str(ent.Payload, "status")
	if !task.TerminalStatus(status) {
		return
	}
	if fp.occurrence != 0 && mirror.OccurrenceOf(ent.Payload) != fp.occurrence {
		return
	}
	if got, ok := e.retireHandle(fp.id, fp.h); ok {
		e.applyHook(got, task.Status(status), entity.Event{Ref: ref, To: ent.Payload})
	}
}

// Adopt registers an already-running task (id + governing handle) under a flow — the
// resume path on restart, when a remote-hosted task outlived viff. It re-tracks it
// exactly as Spawn does a fresh one, so the resumed task is steerable (`viff flow` say,
// cancel, suspend) and reaped from the flow when it ends — not merely observed. subject
// "" is a no-op.
func (e *Engine) Adopt(subject, id string, h task.Handle) {
	e.AdoptChain(subject, id, "", h)
}

// AdoptChain is Adopt for a task that came from the charter: chain is the kpath of the
// action that spawned it, read back off its task entity. It is what the task's end is looked up
// by: when the record ends, the engine navigates the chain to its action and runs what the status
// calls for, so a resumed task finishes the way it would have — its onDone fires, and a sequence
// it belongs to advances (applyHook).
//
// The lookup is made when the task ends and not here, because a task is re-attached at start
// ahead of the charter that holds its rule.
//
// It answers whether the engine took the task: an engine that was stopped takes none (Stop).
func (e *Engine) AdoptChain(subject, id, chain string, h task.Handle) bool {
	if h == nil || subject == "" {
		return false
	}
	return e.track(flowTask{id: id, subject: subject, h: h, chain: chain})
}

// hooksOf is the action whose completion hooks a task's end runs: the one its firing spawned it
// from, or for a task this engine did not spawn — one re-attached after a restart, or one whose
// end is settled with no task at all (Settle) — the action its chain addresses in the charter as
// it stands. A rule removed while its task ran, or a charter edited so the chain no longer
// resolves, leaves no action to run, which is the correct outcome and not an error.
func (e *Engine) hooksOf(fp flowTask) *Action {
	if fp.hooks != nil || fp.chain == "" {
		return fp.hooks
	}
	t, ok := e.Rule(mirror.TriggerOf(fp.chain)) // armed or pending: the task ran, whatever its rule waits on
	if !ok {
		e.tracef("task %s: rule %s is gone; no hook runs", fp.id, mirror.TriggerOf(fp.chain))
		return nil
	}
	act, ok := navigate(t, fp.chain)
	if !ok {
		e.tracef("task %s: chain %s no longer resolves; no hook runs", fp.id, fp.chain)
		return nil
	}
	return act
}

// retire atomically removes a live task by id, returning its flowTask. Whoever retires it
// (reap, or reapEnded for a record terminal before the task was tracked) is the one that
// applies its completion hook — the once-guard for it.
func (e *Engine) retire(id string) (flowTask, bool) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	fp, ok := e.live[id]
	if ok {
		delete(e.live, id)
		e.ends++ // owed from here, until applyHook has taken it (Busy)
	}
	return fp, ok
}

// retireHandle is retire scoped to a specific handle: it removes the entry only if the tracked
// handle still matches, and not a successor that took the id.
func (e *Engine) retireHandle(id string, h task.Handle) (flowTask, bool) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if fp, ok := e.live[id]; ok && fp.h == h {
		delete(e.live, id)
		e.ends++ // as retire
		return fp, true
	}
	return flowTask{}, false
}

// hookOccasion is the occasion a hook of rule runs at: the head, about ev (applyHook). Its reads
// are narrowed by the rule armed or pending, as hooksOf finds it: a hook paid before the engine
// opens, when nothing is armed, reads as narrowly as one paid after. A hook the firing carried
// is narrowed by that firing's rule instead (flowTask.within, in applyHook).
func (e *Engine) hookOccasion(rule string, ev entity.Event) occasion {
	var within [][]string
	if t, ok := e.Rule(rule); ok {
		within = firingWithin(t)
	}
	return occasion{ev: ev, commit: entity.Commit{Rev: e.store.Head()},
		snap: &snapCache{}, within: within, reader: e.reader(rule)}
}

// applyHook fires the completion hook for a terminated task's status (if any), scoped to
// exactly that task: the hook (a full Action) runs about the task's own subject. The chain
// name descends by status, so a hook's own spawn gets a distinct deterministic id. ev carries the
// completed task:instance payload, so the hook's expressions can read status, flow and result.
func (e *Engine) applyHook(fp flowTask, status task.Status, ev entity.Event) {
	if !e.beginHook() {
		return // the engine was stopped: its host takes what the task held (Stop)
	}
	defer e.hookDone()
	subject, serr := entity.ParseAddr(fp.subject)
	// WHAT COMMIT THIS READS AT: the head, the viff as it is when the task ends.
	//
	// A hook is woken by a task finishing rather than by a write, so it has no cause of its
	// own — and for a while that was taken to mean it had no revision to read at either, which
	// left `$subject.value`, `$subject.commit`, `!get-path` and `!list-paths` unresolvable in
	// every onDone/onFailed/onCancelled there has ever been (git-issue hjxd56jy).
	// It then read at the commit its rule fired at, off the record's `occurrence`, so that a
	// rule was one statement about one moment. That moment is history, and the store keeps
	// history for its compaction cutoff only: a task that ran past it lost its hook to a read
	// the store rightly refused (entity.ErrReplayTooOld), and a capture of that moment on the
	// task's record (signadot/viff#76) narrowed the loss without removing it, at a copy of
	// the rule's places on every write of every hooked task (git-issue 8mzvx61xha9xres1xdn0).
	//
	// The head is always there to read: no cutoff reaches it and no restart loses it, since
	// nothing but the store is needed. A hook patches the subject as it is now, so the subject
	// as it is now is also what its patch means. A value from the firing's moment is the
	// firing's to write, on the subject beside its spawn, where the hook reads it back
	// (docs/reference/action.md, Bindings); the claim the spawn took keeps the rule from
	// writing over it in between (applyOne). What makes the hook survive a restart is as it
	// was: the chain on the record names the action, which is looked up when the task ends.
	//
	// Head, as every read the engine makes of the viff as it stands (selectsNow, a lift's
	// reArm): what this process has been told of, which the task's own terminal write is, with
	// no round trip. A write another process made that the watch has not delivered yet is one the
	// next commit brings, as for any firing.
	//
	// This is the read only. The causal PARENT stays 0 — a hook does begin a fresh chain for
	// the cycle throttle, which is causal.go's argument and a different question from which
	// world the references see.
	// A hook's snapshot is shared and narrowed like a firing's — it is one: the rule is the one
	// whose chain this is, and a hook that reads twice must read one world (see occasion.snap).
	// A rule that has since been removed narrows to nothing and reads whole, which is the same
	// answer it gave before there was a narrowing to work out.
	hookOf := mirror.TriggerOf(fp.chain)
	oc := e.hookOccasion(hookOf, ev)
	// THE SUBJECT IS RELEASED ONCE THE HOOK HAS RUN, however it ran: the claim the spawn took on
	// it (applyOne) comes off on every path out of here, and the rule may fire about the subject
	// again. After the hook's own writes, so that there is no moment in which the claim is free
	// and what the hook had to say is unwritten (the order stopSeq keeps, for the same reason).
	// Released whether or not the action still carries a hook: a resumed task's is looked up in
	// the charter as it is now, and a hook removed while its task ran would otherwise leave the
	// claim standing, and the rule refusing the subject once the hook is added back. A claim
	// that is not there deletes as a no-op, and a sequence's run claim lives at the carrier's
	// chain, never at a task's.
	//
	// AND WHAT THE HOLD REFUSED IS OWED: a firing refused while the task ran, or between its end
	// and this release — the hook's own write is one, when its commit is dispatched before the
	// release lands — is one evaluation at the head now (reconsider). For the rule's own action
	// only: a hook's hooked spawn is held at its own chain, which no firing is refused for.
	if serr == nil && fp.chain != "" {
		defer func() {
			if e.released(fp.chain, subject, fp.chain == hookOf) {
				e.reconsider(hookOf, subject)
			}
		}()
	}
	// A completed SEQUENCE ELEMENT advances its sequence instead of firing a hook: the list
	// already says what follows, which is why an element may not carry its own onDone. Only
	// on `done` — any other terminal status is an off-ramp, and takes the hook path below so
	// the element's own onFailed/onCancelled can run and the sequence stops there (fail-stop).
	if status == task.Done && serr == nil {
		if e.resumeSeq(fp.chain, subject, oc) {
			return
		}
	}
	// An off-ramp ENDS the sequence, so the run ends here and has to be written down as ended.
	// Before the two returns below, because a sequence stops at an off-ramp whether or not the
	// element carries a hook for it — a failure nobody wrote an `onFailed` for is still a
	// failure, and the run behind it is still over.
	if status != task.Done && serr == nil {
		e.stopSeq(fp.chain, subject, status)
	}
	act := e.hooksOf(fp)
	if act == nil {
		return
	}
	hook := act.hookFor(status)
	if hook == nil {
		return
	}
	if serr != nil {
		return
	}
	subj := subject
	// Descend the chain by the hook's FIELD name, not by the status: `review.onDone` is the
	// kpath of the action that is about to run, so the id it spawns under is its address in
	// the charter. (An empty field cannot happen — hookFor already returned non-nil only for
	// a terminal status — but a chain is a wire-visible id, so do not build a malformed one.)
	seg := hookField(status)
	if seg == "" {
		return
	}
	// Appended directly rather than via kpath.Join, which reads its PREFIX as a single
	// segment and so quotes a multi-segment chain: Join("review.onDone", "onFailed") is
	// `"review.onDone".onFailed`, a valid kpath naming one field with a dotted name — and
	// a quote is not legal unescaped in a URL path. A field segment appends with '.'.
	chain := fp.chain + "." + seg
	if fp.hooks != nil {
		// The firing's action, and so the firing's reads (flowTask.within). A sequence's next
		// element above is the rule's as it stands, and reads by it.
		oc.within = fp.within
	}
	err := hook.apply(chain, oc, []entity.Ref{subj}, e, fromCause)
	if errors.Is(err, ErrClosing) {
		// Refused whole, as its charter is being taken down: no fault of the hook's, and nothing
		// is written about it on a subject that outlives the charter.
		e.tracef("%s: %s: not run, its charter is being taken down", chain, subj)
		return
	}
	if err != nil {
		e.recordHookFailure(mirror.Ref(fp.id), subj, chain, mirror.OccurrenceOf(ev.To), err)
		return
	}
	e.clearHookFailure(subj, chain)
}

// recordHookFailure persists a completion hook's failure onto the task whose completion
// fired it, under a `hook` record:
//
//	hook: { chain: review.onDone, status: failed, err: ".[data.severity]: …" }
//
// A hook is where an unverifiable claim turns into a state transition, so its failure is the
// one an operator most needs to see — and a trace line is invisible to everything that reads
// the viff.
//
// AND ONTO THE SUBJECT, in the rule's own bookkeeping (recordHookTrouble), because the subject is
// where somebody looks when it does not advance, and a failure only the task's record shows left
// one standing at the stage the rule set with nothing on it saying why. Not in a field the author
// owns: `stage` and the rest are untouched, so `value: {stage: X}` matches as it did.
//
// Writing to the task entity is safe despite the mirror owning that payload: the hook runs on
// the record's end, which is the last thing the mirror writes there, and this write merges
// `hook` into it.
//
// `hook` is one reserved name on an entity the engine and the mirror already co-own, the way
// `viff` is one reserved root on a subject. status is the mirror's field and is left alone.
func (e *Engine) recordHookFailure(ref, subj entity.Ref, chain string, occurrence int64, err error) {
	e.recordHookTrouble(subj, chain, ref.Name(), occurrence, err)
	e.recordTaskHookFailure(ref, chain, err)
}

// recordTaskHookFailure is the task's half of recordHookFailure.
func (e *Engine) recordTaskHookFailure(ref entity.Ref, chain string, err error) {
	// AND SAID OUT LOUD. It was traced, which is off by default — so the paragraph above,
	// about this being the failure an operator most needs to see, was true of the RECORD and
	// not of the log. An operator who does not already suspect a hook has nothing to go on:
	// the subject simply did not advance (git-issue 91jktrr3h12ksa8fgsn0).
	e.logger.Warn("completion hook failed: the firing wrote nothing",
		"chain", chain, "task", ref.String(), "err", err.Error())
	e.modify(ref, func(cur entity.Payload) entity.Payload {
		p := clonePayload(cur)
		p["hook"] = entity.Obj{"chain": chain, "status": "failed", "err": err.Error()}
		return p.Payload()
	})
}

// hookTrouble is the field under @sys.charter.<trigger> on a subject that says the rule's
// completion hook failed about it.
const hookTrouble = "hook"

// recordHookTrouble writes a completion hook's failure onto the SUBJECT, under the rule's own
// bookkeeping:
//
//	viff:
//	  charter:
//	    fix:
//	      hook: {chain: fix.onDone, task: fix@…, occurrence: 4480, err: "…"}
//
// occurrence is the commit the rule fired at, off the task's record: which firing's hook this
// was, and not where the hook read, which is the head.
//
// One per rule, so two rules whose hooks fail about one subject do not overwrite each other, and
// a charter can match on its own rule's trouble — "when fix's hook is lost, ask a person" — as it
// matches on a sequence's progress beside it (seq.go). It stands until a hook of the same rule
// about the subject runs cleanly (clearHookFailure).
//
// Written as the hook's own writes are (patchEntity), so it is the rule's to write where the
// hook's patch would have been, and it is no progress: a completion's write is not held by a
// suspension, and begins no causal chain. task is "" where no task's completion ran the hook.
func (e *Engine) recordHookTrouble(subj entity.Ref, chain, taskID string, occurrence int64, err error) {
	rec := entity.Obj{"chain": chain, "task": taskID, "occurrence": occurrence, "err": err.Error()}
	patch := entity.Obj{ViffRoot: entity.Obj{CharterKey: entity.Obj{
		mirror.TriggerOf(chain): entity.Obj{hookTrouble: rec}}}}.Payload()
	if werr := e.patchEntity(chain, 0, subj, subj, patch); werr != nil {
		e.logger.Warn("a completion hook's failure could not be recorded on its subject",
			"chain", chain, "subject", subj.String(), "err", werr.Error())
	}
}

// clearHookFailure removes the rule's hook trouble from the subject once a hook of the same rule
// about it has run cleanly: what it said is no longer why the subject stands where it does. It
// writes only where there is something to remove, so a clean hook costs one read.
func (e *Engine) clearHookFailure(subj entity.Ref, chain string) {
	trig := mirror.TriggerOf(chain)
	ent, ok, err := e.reader(trig).Get(subj)
	if err != nil || !ok {
		return
	}
	if entity.Get(entity.Get(entity.Get(entity.Get(ent.Payload, ViffRoot), CharterKey), trig), hookTrouble) == nil {
		return
	}
	// A REMOVAL AT ITS PATH, rather than a `!delete` patched into the subject: a patch is written
	// back whole through Modify, which drops a field only at the top of the payload, so a removal
	// three levels down would merge away to nothing. As the rule, like the write it undoes.
	at := subj.At(ViffRoot, CharterKey, trig, hookTrouble)
	if _, _, werr := e.caused(chain, 0).Write(entity.Write{At: at}); werr != nil {
		e.logger.Warn("a completion hook's old failure could not be cleared from its subject",
			"chain", chain, "subject", subj.String(), "err", werr.Error())
	}
}

// neverStarted is the event a completion hook reads for work that never started (track): the
// facts the engine holds itself, over whatever record stands at the task's id. A refused launch
// may have no record, or one its mirror has not written yet, and a hook must bind `status`,
// `err`, `flow` and `chain` either way.
func (e *Engine) neverStarted(fp flowTask, why string) entity.Event {
	ref := mirror.Ref(fp.id)
	st := entity.Obj{}
	if ent, ok, _ := e.store.Get(ref); ok {
		if o := entity.ToObj(ent.Payload); o != nil {
			st = o
		}
	}
	st["status"] = string(task.Failed)
	delete(st, mirror.OrphanedField) // the run before may have ended so; this one was refused
	if why != "" {
		st["err"] = why
	}
	if fp.subject != "" {
		st["flow"] = fp.subject
	}
	if fp.chain != "" {
		st["chain"] = fp.chain
	}
	// And the commit the firing was about, where the record does not say it: applyHook names the
	// firing by it, and a launch the engine refused fails before anything has mirrored its task.
	if _, ok := st["occurrence"]; !ok && fp.occurrence != 0 {
		st["occurrence"] = fp.occurrence
	}
	return entity.Event{Ref: ref, To: st.Payload()}
}

// SubjectOf returns the subject a live task is about, or "" if the engine does not know
// it — a task spawned directly by `task run` genuinely has none, and one that has
// already been reaped is no longer live. A flow is a query over these subjects, so this is
// that query at its narrowest setting: one task.
func (e *Engine) SubjectOf(id string) string {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	return e.live[id].subject
}

// Flows lists the distinct subjects with in-flight tasks — a QUERY over the live
// registry, not a maintained index. Terminal tasks are reaped, so a subject drops off
// once its work finishes; the handles behind it are this engine's live tasks (control
// targets), not a durable membership view.
func (e *Engine) Flows() []string {
	e.liveMu.Lock()
	seen := make(map[string]bool, len(e.live))
	for _, fp := range e.live {
		seen[fp.subject] = true
	}
	e.liveMu.Unlock()
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out) // deterministic for a CLI listing
	return out
}

// matchFlow selects the live tasks a subject-Match addresses. This IS the flow
// primitive: a flow is not a stored key but the answer to "which live tasks are about
// entities matching this". A payload predicate (`value`) is applied by reading the subject
// entity.
func (e *Engine) matchFlow(m Match) []flowTask {
	e.liveMu.Lock()
	all := make([]flowTask, 0, len(e.live))
	for _, fp := range e.live {
		all = append(all, fp)
	}
	e.liveMu.Unlock()

	hasPredicate := m.HasPredicate()
	out := make([]flowTask, 0, len(all))
	for _, fp := range all {
		ref, err := entity.ParseAddr(fp.subject)
		if err != nil || !m.MatchRef(ref) {
			continue
		}
		if hasPredicate {
			// Unreadable is treated as non-matching: a field predicate that cannot be
			// evaluated must not select the flow. Skipping is the conservative answer.
			ent, ok, _ := e.store.Get(ref)
			if !ok || !m.MatchEvent(entity.Event{Ref: ref, To: ent.Payload}) {
				continue
			}
		}
		out = append(out, fp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// LiveTask is one live task as a flow listing reads it: its id, the rule of this engine that
// spawned it ("" for a task run by hand), the subject it is about, and who holds it.
type LiveTask struct {
	ID      string
	Rule    string
	Subject string
	HeldBy  []string
}

func (fp flowTask) live() LiveTask {
	return LiveTask{ID: fp.id, Rule: mirror.TriggerOf(fp.chain), Subject: fp.subject, HeldBy: fp.heldBy()}
}

// SelectFlow is the live tasks a selection names, sorted by id, signalling none of them. A
// selection is spelled as a suspension is — a charter, a rule of it, a subject — so what a
// listing shows is exactly what a suspension in the same words would hold now.
func (e *Engine) SelectFlow(sel Suspension) []LiveTask {
	at := e.atSegs()
	var out []LiveTask
	for _, fp := range e.liveTasks() {
		lt := fp.live()
		if sel.CoversCharter(at, lt.Rule) && sel.CoversSubject(subjectOf(fp.subject)) {
			out = append(out, lt)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Live is one live task by id, when this engine tracks it.
func (e *Engine) Live(id string) (LiveTask, bool) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	fp, ok := e.live[id]
	return fp.live(), ok
}

// HoldTask takes or lifts an operator's hold on one live task — an ad-hoc stop of that run,
// distinct from cancel, which is destructive, and from a suspension, which is durable. Taken, the
// task holds at its next boundary and says the hold is the operator's (heldBy), so a
// suspension's lift leaves it held. Lifted, the task runs from its boundary — unless a suspension
// holds it too, when the operator's hold is lifted and the task stays held; the route refuses that
// case before it gets here (TaskSuspended). Out-of-band control, honored at the boundary. False
// when the task is not live here.
func (e *Engine) HoldTask(id string, hold bool) (LiveTask, bool) {
	var held []string
	if !hold {
		fp, ok := e.Live(id)
		if !ok {
			return LiveTask{}, false
		}
		held = e.settled(fp.Rule, subjectOf(fp.Subject))
	}
	fp, ok := e.setHold(id, func(fp *flowTask) {
		fp.operator = hold
		if !hold {
			fp.suspended = held
		}
	})
	if !ok {
		return LiveTask{}, false
	}
	switch {
	case hold:
		fp.h.Pause(engineActor)
	case len(held) == 0:
		fp.h.Resume(engineActor)
	}
	e.recordHold(fp)
	return fp.live(), true
}

// Handle is the handle one live task is controlled through, when this engine tracks it.
func (e *Engine) Handle(id string) (task.Handle, bool) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	fp, ok := e.live[id]
	return fp.h, ok
}

// CancelLive cancels every live task this engine tracks, and every one tracked after it, for
// the owner of an engine whose program is going away with the store it runs over — a scope
// being deleted. It answers the ids it cancelled.
//
// NO COMPLETION HOOK RUNS for them. Each is forgotten before its handle is cancelled, so the
// reaper that would apply its onCancelled finds nothing to retire: a hook is part of the program
// being deleted, and one run now would write into, or spawn work into, a scope that is gone.
func (e *Engine) CancelLive(reason string) []string {
	e.liveMu.Lock()
	live := e.live
	e.live = map[string]flowTask{}
	e.dropped = reason
	e.liveMu.Unlock()
	ids := make([]string, 0, len(live))
	for id, fp := range live {
		fp.h.Cancel(reason, engineActor)
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// TaskSuspended is what suspends one live task by id: what resuming it is refused naming, since
// resuming it would run covered work.
func (e *Engine) TaskSuspended(id string) []string {
	e.liveMu.Lock()
	fp, ok := e.live[id]
	e.liveMu.Unlock()
	if !ok {
		return nil
	}
	return e.holding(mirror.TriggerOf(fp.chain), subjectOf(fp.subject))
}

// HeldBy is who holds a live task, empty when nothing does or the task is not live here.
func (e *Engine) HeldBy(id string) []string {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	return e.live[id].heldBy()
}

// reap retires a task when its reflected entity reaches a terminal status, so
// Flows()/matchFlow see only in-flight work — no lingering completed handles. The same
// task:instance change a completion trigger reacts to also releases the handle. Keyed by task
// id, so this is one delete, with no scan of subjects.
func (e *Engine) reap(ev entity.Event) {
	if !mirror.IsTaskish(ev.Ref) {
		return
	}
	status := entity.Str(ev.To, "status")
	if !task.TerminalStatus(status) {
		return
	}
	if !e.isOpen() {
		return // taken once the engine is open (reapEnded)
	}
	// Retired here, in the order the records ended; the hook runs beside the loop, since a hook
	// can launch a task and one task's launch should not hold up another task's end.
	if fp, ok := e.retire(ev.Ref.Name()); ok {
		go e.applyHook(fp, task.Status(status), ev)
	}
}

// reapLoop is the completion path: it watches the task records and, on each terminal status,
// retires the task and fires its completion hook. A task ends when its record says so and only
// then, so a task whose event stream drops stays live until its mirror has written how it
// ended. Replayed old terminals are harmless: retire finds no live entry for a task this engine
// never tracked.
func (e *Engine) reapLoop(ctx context.Context, ch <-chan entity.Commit, unsub func()) {
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case c, ok := <-ch:
			if !ok {
				return
			}
			for _, ev := range c.Changes {
				e.reap(ev)
			}
		}
	}
}
