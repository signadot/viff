package trigger

import (
	"context"
	"sort"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// flowTask is one LIVE task: its id (for store-driven reap by ref), the subject it is
// about, and its governing handle (the control target — a *Task locally, a remote handle
// behind a runner URL later). The engine holds handles, not concrete tasks.
type flowTask struct {
	id      string
	subject string // the resolved subject this task is about — a flow is a query over these
	h       task.Handle
	// hooks is the spawning action, carried so the engine can fire its completion hook (onX)
	// when THIS task reaches a terminal status — scoped to this task, not every task
	// about the subject. nil for Adopt (a resumed task has no charter action to hook).
	hooks *Action
	// chain is this spawn's position in its trigger's action tree, as a KPATH: the trigger
	// name at the top, then review.onDone, review.onDone.onFailed, … So it is not a private
	// naming scheme but the address of the spawning Action inside the charter — which is
	// what makes a hook's spawn id distinct and deterministic, and what a reader (or, later,
	// a resume) can navigate back to the action that owns it.
	chain string
	// occurrence is the commit that caused the firing this task belongs to (launch.Spec's
	// Occurrence), 0 where none did. A completion hook reads at it, off the task's record; this
	// is the engine's own copy for a task whose record does not say, because nothing mirrored
	// one before the hook ran — a launch the engine refused, whose task never reached a runner.
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
// cancelled it, for the record. The engine is verse's own, so it is the system's.
const engineActor = "system:trigger"

// track registers a live task and arms its lifecycle-driven reaper. A task a suspension covers
// is held as it is tracked — a task re-attached after a restart, or one run by hand about a
// suspended subject — since nothing else will pause it.
func (e *Engine) track(fp flowTask) {
	held := e.settled(mirror.TriggerOf(fp.chain), subjectOf(fp.subject))
	fp.suspended = held
	e.liveMu.Lock()
	if why := e.dropped; why != "" {
		// A firing that was already past its gate when the work was dropped: what it started
		// has no program left to belong to.
		e.liveMu.Unlock()
		fp.h.Cancel(why, engineActor)
		return
	}
	e.live[fp.id] = fp
	e.liveMu.Unlock()
	if len(held) > 0 {
		fp.h.Pause(engineActor)
		e.recordHold(fp)
	}
	go e.reapOnDone(fp) // retire it when it ends
}

// Adopt registers an already-running task (id + governing handle) under a flow — the
// resume path on restart, when a remote-hosted task outlived verse. It re-tracks it
// exactly as Spawn does a fresh one, so the resumed task is steerable (`verse flow` say,
// cancel, suspend) and reaped from the flow when it ends — not merely observed. subject
// "" is a no-op.
func (e *Engine) Adopt(subject, id string, h task.Handle) {
	e.AdoptChain(subject, id, "", h)
}

// AdoptChain is Adopt for a task that came from the charter: chain is the kpath of the
// action that spawned it, read back off its task entity. The engine navigates to that
// action and re-attaches its completion hooks, so a resumed task finishes the way it
// would have — its onDone fires, and a sequence it belongs to advances.
//
// This is what a chain being a LOOKUP KEY into the charter is for. Before it, resume tracked
// a task with no hooks at all, which meant a completion after a restart silently ended a
// chain: docs/trigger-model.md records that as a limitation, and it is why `onOrphaned`
// never fires (the only orphan is written by this very reconcile, on a fresh engine).
//
// Rehydration is best-effort by nature, and the failures are ordinary rather than alarming:
// a trigger removed while its task ran, or a charter edited so the chain no longer
// resolves. Both mean there is no action left to run, which is the correct outcome — the
// task is still adopted, so it stays steerable and is still reaped.
func (e *Engine) AdoptChain(subject, id, chain string, h task.Handle) {
	if h == nil || subject == "" {
		return
	}
	fp := flowTask{id: id, subject: subject, h: h, chain: chain}
	if chain != "" {
		if t, ok := e.triggerNamed(mirror.TriggerOf(chain)); ok {
			if act, ok := navigate(t, chain); ok {
				fp.hooks = act
			} else {
				e.tracef("adopt %s: chain %s no longer resolves; no hooks rehydrated", id, chain)
			}
		} else {
			e.tracef("adopt %s: trigger %s is gone; no hooks rehydrated", id, mirror.TriggerOf(chain))
		}
	}
	e.track(fp)
}

// reapOnDone retires a task from its flow's tracking once it reaches a terminal
// status — the lifecycle-driven reaper for scoped (subtree) mode, where no root dispatch
// loop runs so the store-driven reap never fires. Reaping is purely task bookkeeping,
// so it hangs off the task's own completion (its handle's bus). NOTE: it can race a
// fast task that finishes before this subscribes (Subscribe is live-from-now, no ring
// replay) — in baseline mode reap (store-driven, via the root watch) is the race-free
// path; this is the backstop and the scoped-mode path. The range also exits if the
// task closes its bus, so this cannot outlive p.
func (e *Engine) reapOnDone(fp flowTask) {
	ch, unsub := fp.h.Subscribe()
	defer unsub()
	var status string
	for ev := range ch {
		if ev.Kind == task.EventStatus && task.TerminalStatus(ev.Text) {
			status = ev.Text
			break
		}
	}
	// Retire whether or not we saw a terminal status (a closed bus means the task is gone).
	// Apply the completion hook only when we both observed a terminal status AND won the retire
	// (retireHandle is atomic, so the store-driven reapLoop/reap and this can't double-fire).
	got, ok := e.retireHandle(fp.id, fp.h)
	if ok && status != "" {
		e.applyHook(got, task.Status(status), e.taskEvent(got, task.Status(status)))
	}
}

// retire atomically removes a live task by id, returning its flowTask. Whoever retires it
// first (the store-driven reapLoop/reap, or the bus-driven reapOnDone) is the one that applies
// its completion hook — the once-guard for exactly-once hook application.
func (e *Engine) retire(id string) (flowTask, bool) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	fp, ok := e.live[id]
	if ok {
		delete(e.live, id)
	}
	return fp, ok
}

// retireHandle is retire scoped to a specific handle: it removes the entry only if the tracked
// handle still matches (not a successor that reused the id). The reapOnDone path uses it.
func (e *Engine) retireHandle(id string, h task.Handle) (flowTask, bool) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if fp, ok := e.live[id]; ok && fp.h == h {
		delete(e.live, id)
		return fp, true
	}
	return flowTask{}, false
}

// applyHook fires the completion hook for a terminated task's status (if any), scoped to
// exactly that task: the hook (a full Action) runs about the task's own subject. The chain
// name descends by status, so a hook's own spawn gets a distinct deterministic id. ev carries the
// completed task:instance payload, so the hook's templates can read $status/$flow/$result.
func (e *Engine) applyHook(fp flowTask, status task.Status, ev entity.Event) {
	subject, serr := entity.ParseAddr(fp.subject)
	// WHAT COMMIT THIS READS AT: the one that caused the firing this task belongs to.
	//
	// A hook is woken by a task finishing rather than by a write, so it has no cause of its
	// own — and for a while that was taken to mean it had no revision to read at either, which
	// left `$subject.value`, `$subject.commit`, `!get-path` and `!list-paths` unresolvable in
	// every onDone/onFailed/onCancelled/onOrphaned there has ever been. That is the whole read
	// vocabulary, in the half of a rule where the interesting writes live: a `derive` of a
	// release note from what shipped could not be written down (git-issue hjxd56jy).
	//
	// The firing's commit is the right one to offer, and not merely an available one. It is
	// already what an action's path reads resolve at — "read at the commit that caused the
	// firing, so a rule held at a gate for a day still writes what it was evaluated against"
	// — so a hook reading there makes the rule ONE statement about one moment rather than two
	// about different ones. A charter whose task does not write its own subject then has the
	// author's intent exactly: that moment and the moment the task finished agree.
	//
	// It is read off the RECORD (`occurrence`, the revision a run is about) rather than
	// carried in memory, which is what makes it survive a restart: a task resumed by
	// AdoptChain is hooked by an engine that never saw the firing, and the record still says.
	//
	// This is the read only. The causal PARENT stays 0 — a hook does begin a fresh chain for
	// the cycle throttle, which is causal.go's argument and a different question from which
	// world the references see.
	// A hook's snapshot is shared and narrowed like a firing's — it is one: the rule is the one
	// whose chain this is, and a hook that reads twice must read one world (see occasion.snap).
	// A rule that has since been removed narrows to nothing and reads whole, which is the same
	// answer it gave before there was a narrowing to work out.
	hookOf := mirror.TriggerOf(fp.chain)
	var hookWithin [][]string
	if t, ok := e.Trigger(hookOf); ok {
		hookWithin = firingWithin(t)
	}
	oc := occasion{ev: ev, commit: entity.Commit{Rev: mirror.OccurrenceOf(ev.To)},
		snap: &snapCache{}, within: hookWithin, reader: e.reader(hookOf)}
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
	if fp.hooks == nil {
		return
	}
	hook := fp.hooks.hookFor(status)
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
	if err := hook.apply(chain, oc, []entity.Ref{subj}, e, fromCause); err != nil {
		e.recordHookFailure(mirror.Ref(fp.id), chain, err)
	}
}

// recordHookFailure persists a completion hook's failure onto the task whose completion
// fired it, under a `hook` record:
//
//	hook: { chain: review.onDone, status: failed, err: "$data.severity is not present …" }
//
// A hook is where an unverifiable claim turns into a state transition, so its failure is the
// one an operator most needs to see — and a trace line is invisible to everything that reads
// the verse. Written here rather than onto the SUBJECT because the failure is a fact about
// this task's completion, not about the PR: the subject deliberately did not advance, and
// putting an error in a field a charter matches on is what would make `value: {stage: X}`
// quietly stop matching.
//
// Writing to the task entity is safe despite the mirror owning that payload, because a
// store write MERGES: a field the mirror never names survives its re-Put (see mirror.go,
// which relies on the same property for `pending`). That also makes it ordering-independent,
// which matters — this can run before the mirror's own terminal write, since the two race.
//
// `hook` is one reserved name on an entity the engine and the mirror already co-own, the way
// `verse` is one reserved root on a subject. status is the mirror's field and is left alone.
func (e *Engine) recordHookFailure(ref entity.Ref, chain string, err error) {
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

// taskEvent synthesizes the store event for a completed task — the bus-driven reapOnDone
// path has no entity.Event, so it reads the task:instance payload to give the hook the
// same bindings the store-driven path has.
//
// The mirror entity is NOT authoritative for the terminal facts. reapOnDone and
// mirror.Reflect are two independent subscribers to the same task bus, so when this path
// wins the retire — which for a fast task it reliably does, the engine's reader getting
// there first — the store still holds the PRE-terminal projection. The hook then bound
// $status to "running" and found no $result at all, which is exactly the payload a
// completion hook exists to read. The store-driven path never had this problem: mirror
// writes status and result in one commit (see mirror.go), so the event it dispatches on
// already carries both.
//
// So the facts this path knows first-hand are overlaid on whatever the mirror has managed to
// write: the terminal status this reap just observed, and the handle's final answer. flow and
// chain come from the engine's own record for the same reason — a hook must bind them even
// when the mirror has written nothing about this task yet.
func (e *Engine) taskEvent(fp flowTask, status task.Status) entity.Event {
	ref := mirror.Ref(fp.id)
	ev := entity.Event{Ref: ref}
	st := entity.Obj{}
	if ent, ok, _ := e.store.Get(ref); ok {
		if o := entity.ToObj(ent.Payload); o != nil {
			st = o
		}
	}
	st["status"] = string(status)
	if fp.h != nil {
		if r := fp.h.Result(); r != "" {
			st["result"] = r
			// Derive `data` here too, with the mirror's own function: a hook that derefs
			// $data.x must resolve against the same document the store ends up holding,
			// and this path can run before the mirror has written anything.
			if d := mirror.ParseResult(r); d != nil {
				st["data"] = d
			}
		}
		if er := fp.h.Err(); er != "" {
			st["err"] = er
		}
	}
	if fp.subject != "" {
		st["flow"] = fp.subject
	}
	if fp.chain != "" {
		st["chain"] = fp.chain
	}
	// And the commit the firing was about, where the record does not say it yet, for the same
	// reason: applyHook reads at it. A launch the engine refused fails before anything has
	// mirrored its task, so without this the hook read at revision 0, and a hook reading
	// `$subject.value` failed saying a task the charter started was one nothing started.
	if _, ok := st["occurrence"]; !ok && fp.occurrence != 0 {
		st["occurrence"] = fp.occurrence
	}
	ev.To = st.Payload()
	return ev
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
// Flows()/matchFlow see only in-flight work — no lingering completed handles. It is
// store-driven: the same task:instance change a completion trigger reacts to also
// releases the handle. Keyed by task id, so this is one delete — no scan of subjects, and
// none of the "never filter in place" hazard the per-subject slices used to carry (the
// control ops copied a slice header under the lock and iterated it unlocked).
func (e *Engine) reap(ev entity.Event) {
	if !mirror.IsTaskish(ev.Ref) {
		return
	}
	status := entity.Str(ev.To, "status")
	if !task.TerminalStatus(status) {
		return
	}
	if fp, ok := e.retire(ev.Ref.Name()); ok {
		e.applyHook(fp, task.Status(status), ev)
	}
}

// reapLoop is the store-driven completion path for scoped (subtree) mode: it watches the
// task:instance mirror subtree and, on each terminal status, retires the task and fires its
// completion hook. Opened in ObserveScoped BEFORE any spawn (WatchSubtree replays then edges), so
// it is race-free for a fast task — the property reapOnDone's live-from-now bus lacks. The
// atomic retire makes it and the reapOnDone backstop exactly-once. Replayed old terminals are
// harmless: retire finds no live entry for a task this engine never tracked.
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

// ReconcileOrphans retires task:instance entities that a previous run left
// non-terminal: a fresh runtime holds no handle for them (runnerd is in-memory, so
// a restart kills every task), so their last durable state is marked orphaned
// rather than left claiming to run. Returns how many were reconciled.
//
// Call it once at monolith startup, before Start. Do NOT call it in a split
// deployment where the task runtime outlives the engine — there the tasks are
// real and reconnectable, not orphans.
func (e *Engine) ReconcileOrphans() int {
	n := 0
	// Every kind the task system holds, deliberately — instances AND run records (see
	// mirror.RunKind). A run whose runtime died IS orphaned, and its record is the only place
	// that fact can still be told; leaving it saying `running` forever would make the history
	// less honest than the live view beside it. Claims carry no status and fall through.
	for _, ent := range entity.Below(e.store, mirror.IsTaskish, 2, entity.VerseRoot, mirror.System) {
		status := entity.Str(ent.Payload, "status")
		if status == "" || task.TerminalStatus(status) {
			continue
		}
		e.write(ent.Ref.At("status"), entity.Val(string(task.Orphaned)))
		n++
	}
	return n
}
