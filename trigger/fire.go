package trigger

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tonyeval "github.com/signadot/tony-format/go-tony/eval"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/schedule"
	"github.com/signadot/verse/task"
)

// countFire records that one entity was acted on. It is a counter and nothing more: what
// bounds a runaway is the cycle throttle (causal.go), which recognizes the loop rather than
// running out of a number.
//
// There WAS a budget here — maxFires, engine-wide, cumulative, never reset — and it was the
// only thing standing between a rule soup and a store filling up. It is gone, for two reasons
// beyond redundancy. It bounded an engine's LIFETIME rather than any loop, so one looping rule
// exhausted the allowance for every other rule and the engine then stopped firing everything,
// permanently. And a paced cycle is now a legitimate thing to write — a control loop through a
// gate or a ticker — which burns such a budget every lap, so any finite setting was a timer on
// the charter's death rather than a safety limit.
//
// What it also bounded, and nothing does now, is fan-out WIDTH: one firing over 200 members is
// 200 actions, and depth correctly sees that as width. If that wants bounding it belongs per
// firing, refused loudly, because width is a property of a firing and not of an engine's
// lifetime. Nothing in the tree ever set the budget (it defaulted to unbounded and no server
// configured it), so removing it changes no deployed behaviour.
func (e *Engine) countFire() {
	e.mu.Lock()
	e.fired++
	e.mu.Unlock()
}

// fire names what the firing is about, resolves the trigger's gates (conjunctively), and
// runs its action if they all proceed. All blocking gates are posed *concurrently* — so a
// trigger's gates appear together and approvers can act in parallel — then fire waits for
// every one; any denial stops the firing.
//
// The subject is resolved BEFORE any gate, because it is named at the instant When fires:
// a long approval then cannot slide it (and, under fan-out, cannot change the member set
// between asking and acting), and a firing that resolves to nothing never troubles an
// approver. Gates are per FIRING, not per subject — a broadcast raises one approval.
// spawnLabel names the task a firing WILL start if its gates pass. spawnID is a pure
// function of the trigger name and the subject, both settled before the gate is raised, so
// this is the very id the spawn will use — not a guess and not a second scheme. apply() calls
// the same function, and a test pins the two together so they cannot drift apart.
//
// Empty when the action has no spawn: a set-only trigger starts no task, and inventing an
// id for one would be worse than saying nothing. Note the id is what the task WILL be
// called — the gate may be denied, and then nothing by that name is ever created.
func (e *Engine) spawnLabel(t Trigger, subjects []entity.Ref) string {
	if t.Action.Spawn == nil || len(subjects) == 0 {
		return ""
	}
	return spawnID(e.idOwner(), t.Name, subjects[0])
}

// fireSubjects starts one firing per selected member, settling first what each one's causal
// chain is and whether it may run at all (see causeFor and throttled — the throttle is asked
// per SUBJECT, because a chain is per subject).
func (e *Engine) fireSubjects(ctx context.Context, t Trigger, c entity.Commit, subjects []entity.Ref) {
	// Worked out once for the rule rather than per subject: it walks the rule's document, and
	// the answer is the same for every member of a fan-out.
	within := firingWithin(t)
	for _, subj := range subjects {
		parent := e.causeFor(t, c, subj)
		// The causing commit rides ALONGSIDE the causal parent rather than replacing it: parent
		// is zeroed when this firing begins a fresh chain, and c is the commit that caused it
		// either way. See occasion.
		oc := occasion{commit: c, parent: parent, snap: &snapCache{}, within: within,
			reader: e.reader(t.Name)}
		// Before the throttle, which counts the trip and marks the chain reported: a firing a
		// suspension holds is not one the throttle should have seen.
		if held := e.holding(t.Name, subj); len(held) > 0 {
			e.tracef("%s: %s is suspended (%s); not firing", t.Name, subj, strings.Join(held, ", "))
			continue
		}
		if e.throttled(t, subj, parent) {
			if e.tripPace <= 0 {
				continue // no pace configured: refuse outright, and the chain ends here
			}
			// Pace it: hold the firing for D and then let it through, so a tripped chain
			// runs at a dull steady rate rather than at the speed of the store. The wait is
			// in this goroutine, not on the dispatch path — see pauseFor.
			go func(subj entity.Ref, oc occasion) {
				if !e.pauseFor(ctx, subj, e.tripPace) {
					return
				}
				e.fireOne(ctx, t, oc, subj)
			}(subj, oc)
			continue
		}
		go e.fireOne(ctx, t, oc, subj)
	}
}

// pose is a gate's question in words, expanded against the subject this firing settled on.
//
// The environment is the subject and nothing else: its address as `subject.name` and its payload
// as `subject.value`, which is the same payload the action's own references read (refPayload), so
// a sentence and the patch beneath it can never disagree about what they saw.
//
// It is the EXPRESSIONS that are borrowed from go-tony, not its operations: `$[…]` is evaluated
// against that environment, and `!exec`, `!file` and the rest of the eval registry are nowhere in
// reach of a charter, because nothing here runs the tool that would register them.
//
// A sentence that will not expand does not stop the question. The words are dropped and the
// failure is kept on the rule (trouble.go), so its author sees that the charter is broken
// while the people it waits on are still asked, in the terms `reason` has always given.
func (e *Engine) pose(rule string, g Gate, oc occasion, subj entity.Ref) string {
	if g.Pose == "" {
		return ""
	}
	env := map[string]any{"subject": map[string]any{
		"name":  subj.String(),
		"value": tonyeval.ToAny(e.refPayload(oc.ev, subj, fromSubject)),
	}}
	said, err := tonyeval.ExpandString(g.Pose, env)
	if err != nil {
		e.tracef("%s: gate %s: its pose did not expand: %v", g.Name, subj, err)
		e.failed(rule+"."+g.Name+".pose", subj, fmt.Errorf("the pose did not expand: %w", err))
		return ""
	}
	return said
}

// answerersOf is who a gate's question is held out to at this firing: the names the gate wrote,
// and — where it reads them from the subject (AnswerersFrom) — the names at that field, one or a
// list. What the subject contributes is held to the checks a gate's own names meet at install,
// asked now because it could not be asked then: a name somebody wrote into a record is exactly
// as likely to be a typo, a group that was removed, or one person short of the threshold.
func (e *Engine) answerersOf(g Gate, oc occasion, subj entity.Ref) ([]string, error) {
	if _, reads := g.answerersPath(); !reads {
		return g.Answerers, nil
	}
	v := readField(e.refPayload(oc.ev, subj, fromSubject), g.AnswerersFrom)
	var named []string
	switch {
	case v == nil:
		// NOT SAID IS NOT NOBODY. A record that names no answerers leaves the question to the
		// gate's own names, or open to whoever the docket admits — the ask that names nobody in
		// particular. What a record DOES say is held to account below, an empty list included.
		return g.Answerers, nil
	case v.Type == ir.StringType:
		named = []string{v.String}
	case v.Type == ir.ArrayType:
		for _, el := range v.Values {
			if el.Type != ir.StringType {
				return nil, fmt.Errorf("answerersFrom %s: %s holds a %v where a name goes",
					g.AnswerersFrom, subj, el.Type)
			}
			named = append(named, el.String)
		}
	default:
		return nil, fmt.Errorf("answerersFrom %s: %s holds a %v, not a name or a list of them",
			g.AnswerersFrom, subj, v.Type)
	}
	if len(named) == 0 {
		return nil, fmt.Errorf("answerersFrom %s: %s names nobody", g.AnswerersFrom, subj)
	}
	all := append(append([]string(nil), g.Answerers...), named...)
	if err := e.checkGateAnswerers(all, g.Need, g.Options); err != nil {
		return nil, fmt.Errorf("answerersFrom %s: %w", g.AnswerersFrom, err)
	}
	return all, nil
}

// deadlineOf is the clock a gate's question stands under at this firing: the gate's own, or —
// where it reads the date from the subject (Deadline.AtFrom) — that date, resolved to the instant
// the question carries. A date the record does not hold, or holds in no form a clock can read, is
// a question that would never settle as its asker meant, so it is refused like a name nobody can
// ask (answerersOf).
func (e *Engine) deadlineOf(g Gate, v entity.Payload) (*task.Deadline, error) {
	if g.Deadline == nil || g.Deadline.AtFrom == "" {
		return g.Deadline, nil
	}
	// A record that gives no date gives no clock: the question stands until it is answered, as a
	// gate with no deadline does. A date it gives that no clock reads is refused (DateAt).
	if readField(v, g.Deadline.AtFrom) == nil {
		return nil, nil
	}
	at, err := DateAt(v, g.Deadline.AtFrom)
	if err != nil {
		return nil, err
	}
	// AtFrom rides WITH the instant: whoever arms the clock reads the date again as it arms, so
	// an edit landing between this firing and the posting is not lost to a question that had
	// not been written yet when the edit was followed (versed armQuestion).
	return &task.Deadline{At: at.UTC().Format(time.RFC3339Nano), AtFrom: g.Deadline.AtFrom,
		Default: g.Deadline.Default}, nil
}

// DateAt reads the RFC 3339 instant at a field of a subject's value: what a gate's
// `deadline.atFrom` names, read at the firing, while the question stands, and when it is armed.
func DateAt(v entity.Payload, path string) (time.Time, error) {
	f := readField(v, path)
	if f == nil || f.Type != ir.StringType {
		return time.Time{}, fmt.Errorf("deadline atFrom %s: no date there", path)
	}
	at, err := time.Parse(time.RFC3339, f.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("deadline atFrom %s: %q is not an RFC 3339 time (%v)", path, f.String, err)
	}
	return at, nil
}

// fireOne is one firing: hold the subject, resolve the gates conjunctively, and act if they
// all proceed.
//
// The subject is settled before any gate — selected from the snapshot the rule was evaluated
// against — which is what lets a gate say what it is deciding about, and stops a long approval
// sliding the answer underneath itself.
func (e *Engine) fireOne(ctx context.Context, t Trigger, oc occasion, subj entity.Ref) {
	// One firing at a time per subject, for as long as the WORK lasts — which is two things,
	// because a firing can outlive its own function call:
	//
	//   claimFlight  covers the firing itself: the gates being answered, the action applied.
	//   working      covers what it started: a spawned task still running.
	//
	// Every commit meanwhile re-selects the subject, since the write that would release it is
	// exactly what has not happened yet. Neither check changes what RUNS — spawnHooked already
	// refuses a second task under a live id — but together they stop a firing for work
	// already under way from posing a question and spending budget before being told so.
	// And again here, because a paced firing waits out the throttle before it arrives.
	if held := e.holding(t.Name, subj); len(held) > 0 {
		e.tracef("%s: %s is suspended (%s); not firing", t.Name, subj, strings.Join(held, ", "))
		return
	}
	if e.working(t, subj) {
		e.tracef("%s: %s already has work running; not firing again", t.Name, subj)
		return
	}
	fl, ok := e.claimOrDefer(t.Name, subj, &oc)
	if !ok {
		return
	}
	defer e.endFlight(ctx, t.Name, subj)

	subjects := []entity.Ref{subj} // the gate and motion identities are over the firing's set
	var wg sync.WaitGroup
	var mu sync.Mutex
	allProceed := true
	// The gates that actually REFUSED, which is not the same set as "the gates that did not
	// proceed": a resolver that errored, or a monitor that ended before its motion committed,
	// said nothing. Applying a refusal's consequence to those would write a rejection nobody
	// made.
	var denied []Gate
	// And the ones that were passed, by the gate's place in the rule, with how each settled:
	// what the rule's own spawn carries, for a credential to vouch for (launch.Spec.Passed).
	passed := map[int]launch.Passed{}

	// WHO EACH GATE ASKS, settled before any is asked. A gate reading its answerers from the
	// subject can find a name that does not parse, a group that is not there, or fewer people
	// than its threshold — a question nobody could settle. Posing the others meanwhile would put
	// half a firing on the docket for a decision that cannot complete, so the firing stops here,
	// before anybody is asked, and says why. Not a refusal: nobody said no, so no onDenied runs.
	// And the clock each stands under, for the same reason: a date the record does not hold is
	// a question that would never settle as its asker meant.
	answerers := map[int][]string{}
	deadlines := map[int]*task.Deadline{}
	// And the id each is asked under, which is the docket's to say: the one standing open, or a
	// new one where the last was answered (askingID).
	ids := map[int]string{}
	for i, gate := range t.AllGates() {
		if m, _ := gate.mode(); !m.Blocks() {
			continue
		}
		gname := gate.Name
		if gname == "" {
			gname = t.Name
		}
		id, err := e.askingID(gateID(e.idOwner(), t.Name, gname, subjects))
		if err != nil {
			e.tracef("%s: gate %s on %s: the docket could not be read, so nothing is asked: %v",
				t.Name, gname, subj, err)
			return
		}
		ids[i] = id
		who, err := e.answerersOf(gate, oc, subj)
		if err != nil {
			e.tracef("%s: gate %s on %s: who it asks could not be settled, so nothing is asked: %v",
				t.Name, gate.Name, subj, err)
			return
		}
		answerers[i] = who
		clock, err := e.deadlineOf(gate, e.refPayload(oc.ev, subj, fromSubject))
		if err != nil {
			e.tracef("%s: gate %s on %s: its deadline could not be settled, so nothing is asked: %v",
				t.Name, gate.Name, subj, err)
			return
		}
		deadlines[i] = clock
	}

	for gi, gate := range t.AllGates() {
		m, _ := gate.mode() // validated at AddTrigger
		gname := gate.Name
		if gname == "" {
			gname = t.Name
		}
		if !m.Blocks() { // auto: passes without a resolver, and is on the firing's account as that
			mu.Lock()
			passed[gi] = launch.Passed{Gate: gname, How: launch.PassedAuto}
			mu.Unlock()
			continue
		}
		d := task.GateQuestion{
			ID: ids[gi], Origin: t.Name, Gate: gname,
			Reason: fmt.Sprintf("trigger %s gate %s on %s", t.Name, gname, subj),
			// And the same thing in words, when the gate wrote any. `reason` says which rule
			// asked; `pose` says what is being decided, which is the half a person needs.
			Pose: e.pose(t.Name, gate, oc, subj),
			// What the approver is deciding about, and what saying yes starts. One subject,
			// named exactly: the question is about this entity and no other.
			Subject: subj.String(),
			Spawns:  e.spawnLabel(t, subjects),
			// The named layer for a FIRING's gates, which a spawn has had all along and a
			// rule did not. Empty leaves the question to the standing layer, and then to
			// the docket a person reads.
			// And WHICH scope is asking, for the same reason a spawn carries it: the
			// question is about this engine's subject, which outside this scope is an entity
			// that does not exist.
			Scope: e.scope,
			// And which CHARTER's rule is asking. Origin above is the rule's NAME, which is
			// unique in a charter and not across them, so a docket that printed it alone
			// left the reader to guess which `review` this was.
			At: e.At(),
			// How the question settles: the gate's own options and threshold, and the clock
			// it stands under. Empty is the question every gate has always asked, yes or no
			// from one person; the tally and the clock are whoever answers' to keep.
			Options:  gate.options(),
			Deadline: deadlines[gi],
			// And who it is held out to, when the gate says: its own names, and the
			// subject's where it reads them from there (answerersOf, above).
			Answerers: answerers[gi],
			Labels:    gate.Labels,
		}
		wg.Add(1)
		go func(gi int, g Gate, d task.GateQuestion, gname string) {
			defer wg.Done()
			// A gate asks its question of a WORLD, and the answer applies to that world.
			// While the question stands, the subject's own subtree is watched beside it: the
			// moment the rule would no longer select the subject, the wait is cancelled with
			// that as its cause, the question leaves the docket saying so, and nothing was
			// decided — a moot question is not a refusal, so no onDenied runs.
			//
			// Not hypothetical: a merge proposal was swept to `state: stale` while its gate
			// stood open, the ballot already cast carried, and a branch the sweep had retired
			// was merged (git-issue 8wt1ta8r).
			// And while it stands, a date the question reads from the subject follows the
			// subject: whoever may edit the record moves the clock by editing the date. The
			// question's own record is moved, not re-posed, and only while it is pending.
			var follow func()
			if g.Deadline != nil && g.Deadline.AtFrom != "" && d.Deadline != nil {
				last := d.Deadline.At
				follow = func() {
					cur, ok, err := e.store.Get(subj)
					if err != nil || !ok {
						return
					}
					at, err := DateAt(cur.Payload, g.Deadline.AtFrom)
					if err != nil {
						e.tracef("gate %s/%s on %s: the date moved to one no clock reads, so it stands at %s: %v",
							t.Name, gname, subj, last, err)
						return
					}
					want := at.UTC().Format(time.RFC3339Nano)
					if want == last {
						return
					}
					if e.moveDeadline == nil {
						return
					}
					if err := e.moveDeadline(d.Scope, d.ID, at); err != nil {
						e.tracef("gate %s/%s on %s: moving its deadline to %s: %v", t.Name, gname, subj, want, err)
						return
					}
					e.tracef("gate %s/%s on %s: its deadline moved from %s to %s", t.Name, gname, subj, last, want)
					last = want
				}
			}
			wctx, stop := e.untilMoot(ctx, t, subj, follow)
			defer stop()
			res, err := e.ask(wctx, fl, t, subj, d)
			e.tracef("gate %s/%s on %s → %s by %s", t.Name, gname, subj, proceedWord(res.Proceed), res.By)
			if err == nil && res.Proceed {
				// Answered yes — about a subject the rule may have stopped selecting between
				// the asking and the answer. Then the yes is about work that will not be
				// taken, and the firing withdraws rather than acts.
				if subj != (entity.Ref{}) && !e.stillSelects(t, subj) {
					e.tracef("gate %s/%s: %s left the selection after it was answered → withdrawn", t.Name, gname, subj)
					mu.Lock()
					allProceed = false
					mu.Unlock()
					return
				}
				mu.Lock()
				passed[gi] = passedBy(gname, d.ID, res)
				mu.Unlock()
				return
			}
			mu.Lock()
			allProceed = false
			if err == nil { // answered, and the answer was no
				denied = append(denied, g)
			}
			mu.Unlock()
		}(gi, gate, d, gname)
	}
	wg.Wait()

	if !allProceed {
		// A no is a decision, and until this it was a decision with no consequence in the
		// store: nothing was written, so the subject stayed in the state that asked the
		// question, and the next commit to touch it for any reason re-fired the rule and
		// asked again what somebody had already answered. The
		// latch a well-formed rule carries could not help — it lives in the action, and on
		// a deny the action never runs.
		//
		// So the charter says what a no means, and the engine writes nothing of its own
		// invention. A gate with no onDenied behaves exactly as it did (advise names it at
		// install), which is what keeps this additive.
		for _, g := range denied {
			if g.OnDenied == nil {
				continue
			}
			gname := g.Name
			if gname == "" {
				gname = t.Name
			}
			// Addressed as its place in the charter, like a completion hook's chain: the
			// id it writes or spawns under is where a reader can go and find the words.
			// What fails in it is kept on the rule by apply, per subject.
			if err := g.OnDenied.apply(t.Name+"."+gname+".onDenied", oc, subjects, e, fromSubject); err != nil {
				e.tracef("gate %s/%s onDenied: %v", t.Name, gname, err)
			}
		}
		return // some gate denied — no budget consumed
	}
	// Counted only for a firing that proceeds, so a denied gate leaves no trace of a firing
	// that did not happen. One firing, one entity acted on.
	e.countFire()
	e.tracef("fire %s on %s", t.Name, subj)
	// ev.Rev is this firing's place in its causal chain, settled by causeFor before the firing
	// started — including whether a gate paces it. Nothing here needs to revisit that.
	//
	// A trigger firing has no privileged cause: the change that dispatched it is a doorbell,
	// so the action's references read the subject (see refSource).
	//
	// What fails in it is kept on the rule by apply, per subject (trouble.go): the firing has no
	// task to write a failure on, and its author is who can mend it.
	for gi := range t.AllGates() {
		if p, ok := passed[gi]; ok {
			oc.passed = append(oc.passed, p)
		}
	}
	_ = t.Action.apply(t.Name, oc, subjects, e, fromSubject)
}

// passedBy is a gate that asked and was passed, as its resolution says it was: on a ballot somebody cast, or on its
// default when the clock ran out. A resolution that is neither (a resolver set to let every
// question through) leaves How empty, which vouches for nothing.
func passedBy(gate, question string, res task.Resolution) launch.Passed {
	p := launch.Passed{Gate: gate, Question: question, Choice: res.Chosen()}
	switch {
	case res.By == task.ByDeadline:
		p.How = launch.PassedDeadline
	case res.Cast():
		p.How, p.By, p.Via, p.Authenticated = launch.PassedBallots, res.By, res.Via, res.Authenticated
	}
	return p
}

// untilMoot derives the context a firing waits on: cancelled, with the reason as its cause,
// the moment the rule would no longer select the subject. The subject's own subtree is
// watched, and stillSelects is asked on each change — it answers TRUE when it cannot tell,
// so a shutdown mid-read does not settle a live question on a guess.
//
// The cause is what the docket says: the registry renders it as the question's `why` when
// the last waiter leaves (task.Registry, dropReason), so a reader sees that the question
// stopped being about anything, in the rule's own terms, at the change that made it so.
//
// changed, when not nil, runs on each commit to the subject that leaves it selected: what a
// standing question reads from its subject (a date it settles at) is followed while it stands.
func (e *Engine) untilMoot(ctx context.Context, t Trigger, subj entity.Ref, changed func()) (context.Context, func()) {
	wctx, cancel := context.WithCancelCause(ctx)
	stop := func() { cancel(nil) }
	if subj == (entity.Ref{}) {
		return wctx, stop
	}
	st := SubtreeAt(subj.Segs()...)
	ch, unsub, err := e.store.WatchSubtree(st.Depth(), st.Path()...)
	if err != nil {
		return wctx, stop
	}
	go func() {
		defer unsub()
		for {
			select {
			case <-wctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				if !e.stillSelects(t, subj) {
					cancel(fmt.Errorf("%s no longer selects %s, so the question it asked is moot", t.Name, subj))
					return
				}
				if changed != nil {
					changed()
				}
			}
		}
	}()
	return wctx, stop
}

// clonePayload reads p back as a mutable object builder (nil, or a non-object payload →
// empty), so a Modify can extend the current value without mutating the store's copy. The
// caller converts once, with .Payload(), when it returns.
//
// Field by field as NODES, not through entity.ToObj: a map[string]any has nowhere to put a
// tag, so reading a payload back through Go values silently rewrites what it carries — a
// stored rule losing `!glob "hotfix-*"` down to the string, and its
// `tmp: !delete null` down to a null, which the write would then apply as a tombstone. A
// record that holds a document has to survive being extended by one field. Each node is
// cloned because the builder's result is parented into a fresh document, and the copy it came
// from belongs to the store.
//
// What is built from one of these is data on its way back in. Through Modify that is taken
// care of (it escapes what it writes); a builder written STRAIGHT back has to say so itself,
// with entity.AsData — closedMotion is the one that does.
func clonePayload(p entity.Payload) entity.Obj {
	out := entity.Obj{}
	for k, v := range entity.Fields(p) {
		out[k] = v.Clone()
	}
	return out
}

// refIDs renders a witness as its entity ref strings (audit trail on the motion).
func refIDs(refs []entity.Ref) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.String())
	}
	return out
}

// matchText renders a match pattern the way the charter that declared it is written, so
// the operator can compare the message against their own file rather than translate Go's
// map formatting back into tony.
func matchText(m map[string]any) string {
	b, err := codec.Marshal(m, codec.Tony)
	if err != nil {
		return fmt.Sprintf("%v", m)
	}
	return strings.Join(strings.Fields(string(b)), " ")
}

// spawnHooked is Spawn with completion hooks: hooks is the spawning action (its onX fire when
// this task ends), chain is this spawn's position in a hook chain (for deterministic nested
// ids). Public Spawn passes nil/"" — an ordinary spawn with no chaining.
func (e *Engine) spawnHooked(id string, spec launch.Spec, hooks *Action, chain string) task.Handle {
	return e.startHooked(id, spec, hooks, chain, e.spawn)
}

// startHooked is spawnHooked with what starts the task named: the Spawner, or for a launch the
// engine refuses itself, a task that has already failed — which passes every check a spawn
// does and is tracked the same way, so its completion hooks run as a started task's would.
func (e *Engine) startHooked(id string, spec launch.Spec, hooks *Action, chain string,
	start func(id string, spec launch.Spec) task.Handle) task.Handle {
	if e.spawn == nil {
		e.tracef("no spawner configured; cannot spawn %q", id)
		return nil
	}
	// THE BACKSTOP every spawn passes through: a suspended subject starts nothing.
	subj, _ := entity.ParseAddr(spec.Subject)
	if held := e.settled(mirror.TriggerOf(chain), subj); len(held) > 0 {
		e.tracef("spawn %s: suspended (%s); not spawning", id, strings.Join(held, ", "))
		return nil
	}
	// Idempotent per id: spawnID is deterministic per (trigger, subject), so a re-fire about
	// the same subject must not start a second task. The runtime would not stop it — a
	// runnerd Spawn overwrites its registry entry while the old task keeps running — so
	// the engine, which owns the live registry, is where this belongs.
	e.liveMu.Lock()
	if fp, running := e.live[id]; running {
		e.liveMu.Unlock()
		e.tracef("task %s already live (subject %s); not respawning", id, fp.subject)
		return fp.h
	}
	e.liveMu.Unlock()
	// ONCE-EVER ABOUT A TICK. A trigger fires on what already matches when it opens, so a
	// restart or a re-install re-fires the tick standing under its schedule, and the dedupe
	// above ends with the task. A tick's address never recurs — the schedule and the instant it
	// was due — so a task record standing for this id, live or done, is the spawn having
	// happened. The general rule for a world subject stays what it was: a subject may come
	// round again, and the latch is the charter's to write.
	if schedule.IsTickSubject(subj.Segs()) {
		if _, ran, err := e.store.Get(mirror.Ref(id)); err == nil && ran {
			e.tracef("task %s already ran (subject %s is a tick, which never recurs); not respawning", id, spec.Subject)
			return nil
		}
	}

	e.tracef("spawn task %s (doer=%s): %s", id, spec.Who(), spec.Prompt)
	h := start(id, spec)
	if h != nil && spec.Subject != "" {
		e.track(flowTask{id: id, subject: spec.Subject, h: h, hooks: hooks, chain: chain, occurrence: spec.Occurrence})
	}
	return h
}
