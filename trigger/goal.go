package trigger

import (
	"context"
	"fmt"
	"time"

	"github.com/signadot/verse/entity"
)

// A goal gives an SDLC flow an explicit target: a durable record that OWNS the charter
// pursuing it, RECOGNIZES success or failure, and stays legible the whole way ("what is
// this waiting on, and is it still reachable"). It adds goal-chasing the emergent way — a
// goal drives by *owning charter*, not by reconciling: the doing stays with the owned
// triggers reacting to state; the goal only owns them, latches success/failure, and holds a
// live status.
//
// The terminal is not new machinery — it is the temporal gate pattern `until[doneWhen,
// deadline]` (see engine.go awaitUntil): doneWhen holds first ⇒ done; the deadline elapses
// first ⇒ unachievable. status is the live Monitor fluent (retractable); the latches are
// monotonic events (a downstream trigger reacting to "the goal was reached" never thrashes).
//
// A goal is a flow with a destination: its owned triggers spawn processes tracked as
// flow(s), so goal steering reuses PauseFlow/ResumeFlow/CancelFlow/SteerFlow keyed by the
// goal's subject — no new steering path.

// GoalDef is the wire form of a goal — fully declarative, installable over the wire.
type GoalDef struct {
	Name     string    `json:"name"`
	Subject  string    `json:"subject,omitempty"`  // the entity this goal is about (steering key)
	DoneWhen *Formula  `json:"doneWhen"`           // success predicate (a fact/quorum atom in v1)
	Deadline string    `json:"deadline,omitempty"` // v1 fail-latch: elapsed with doneWhen unmet ⇒ unachievable
	Triggers []Trigger `json:"triggers,omitempty"` // the OWNED charter that does the doing
	// Plan (an optional generation-SOURCE that compiles to Triggers) is deferred; the
	// record is shaped to accept it without a breaking change.
}

// Goal is the installed runtime record.
type Goal struct {
	Def       GoalDef
	trigNames []string // owned trigger names, for reap on terminal
	cancel    context.CancelFunc
}

// goalRef / goalDoneRef / goalFailedRef name a goal's entities. goal:instance carries the
// live status + terminal latch; goal:done / goal:failed are the ordinary fact a charter
// trigger may match ({system:"goal", kind:"done"|"failed"}) to advance a subject — emergent,
// opt-in; the goal never sets a subject field itself.
func goalRef(name string) entity.Ref     { return entity.Ref{System: "goal", Kind: "instance", ID: name} }
func goalDoneRef(name string) entity.Ref { return entity.Ref{System: "goal", Kind: "done", ID: name} }
func goalFailedRef(name string) entity.Ref {
	return entity.Ref{System: "goal", Kind: "failed", ID: name}
}

// AddGoal installs a goal: it owns and installs the goal's triggers (the doing), seeds the
// goal:instance entity, and starts the lifecycle monitor over until[doneWhen, deadline].
func (e *Engine) AddGoal(d GoalDef) error {
	if d.Name == "" {
		return fmt.Errorf("goal: name required")
	}
	if d.DoneWhen == nil {
		return fmt.Errorf("goal %q: doneWhen required", d.Name)
	}
	if d.Deadline != "" {
		if _, err := time.ParseDuration(d.Deadline); err != nil {
			return fmt.Errorf("goal %q: bad deadline %q: %w", d.Name, d.Deadline, err)
		}
	}

	e.goalsMu.Lock()
	if _, exists := e.goals[d.Name]; exists {
		e.goalsMu.Unlock()
		return fmt.Errorf("goal %q already exists (remove it first)", d.Name)
	}
	// Install the owned triggers (the doing). Roll back on any failure so a goal never
	// half-installs its charter.
	var names []string
	for _, td := range d.Triggers {
		if err := e.AddTrigger(td); err != nil {
			for _, n := range names {
				e.RemoveTrigger(n)
			}
			e.goalsMu.Unlock()
			return fmt.Errorf("goal %q: installing trigger %q: %w", d.Name, td.Name, err)
		}
		names = append(names, td.Name)
	}
	base := e.bgctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	g := &Goal{Def: d, trigNames: names, cancel: cancel}
	e.goals[d.Name] = g
	e.goalsMu.Unlock()

	// Seed the durable goal:instance entity. terminal=false is the terminal-once guard the
	// commit CAS predicates on. waitingOn is computed BEFORE the Modify — Unmet reads the
	// store (a read lock), and the Modify closure already holds the store's write lock (a
	// non-reentrant RWMutex), so evaluating it inside would self-deadlock.
	waiting := waitingLabels(d.DoneWhen.Unmet(e.store))
	status := "active"
	if len(waiting) > 0 {
		status = "blocked"
	}
	e.store.Modify(goalRef(d.Name), func(cur entity.Payload) entity.Payload {
		p := clonePayload(cur)
		p["name"] = d.Name
		if d.Subject != "" {
			p["subject"] = d.Subject
		}
		p["status"] = status
		p["terminal"] = false
		if d.Deadline != "" {
			p["deadline"] = d.Deadline
		}
		p["waitingOn"] = waiting
		return p
	})
	go e.runGoal(ctx, g)
	return nil
}

// RemoveGoal stops a goal's monitor and reaps its owned triggers. Reports whether it
// existed. It does NOT retract a latch already committed (terminals are monotonic); it just
// stops pursuing. Used for teardown and tests.
func (e *Engine) RemoveGoal(name string) bool {
	e.goalsMu.Lock()
	g, ok := e.goals[name]
	if ok {
		delete(e.goals, name)
	}
	e.goalsMu.Unlock()
	if !ok {
		return false
	}
	g.cancel()
	e.reapGoalTriggers(g)
	return true
}

// Goals returns the installed goal defs (the goal roster) for `verse goals`.
func (e *Engine) Goals() []GoalDef {
	e.goalsMu.Lock()
	defer e.goalsMu.Unlock()
	out := make([]GoalDef, 0, len(e.goals))
	for _, g := range e.goals {
		out = append(out, g.Def)
	}
	return out
}

// runGoal drives the goal's lifecycle from ONE watch over doneWhen: every relevant change
// updates the live status, and the terminal is decided in the same loop — doneWhen true ⇒
// done, deadline elapsed ⇒ unachievable (done wins a tie). On a terminal it commits the latch
// (terminal-once) and reaps the owned triggers.
//
// The single watch is a simplification, not a constraint: status and terminal are decided from
// the same evaluation, so splitting them would mean re-deriving one from the other. (It once
// also worked around a session admitting only one watch per path; since go-tony v0.0.85 a
// session may hold several, each keyed by its request id.)
func (e *Engine) runGoal(ctx context.Context, g *Goal) {
	done := g.Def.DoneWhen

	// Arm the deadline (restart-safe: the absolute instant is stamped on goal:instance, so a
	// restart honors elapsed wall time rather than sliding it forward).
	var deadlineC <-chan time.Time
	if g.Def.Deadline != "" {
		dur, _ := time.ParseDuration(g.Def.Deadline) // validated in AddGoal
		at := e.deadlineAt(goalRef(g.Def.Name), dur)
		timer := time.NewTimer(at.Sub(e.now()))
		defer timer.Stop()
		deadlineC = timer.C
	}

	// Fast path: already satisfied at install.
	if ok, w := done.Eval(e.store); ok {
		e.commitGoal(g, "done", w)
		return
	}

	// Scope the watch to this function: on ANY return (done, unachievable, ctx end) stopWatch
	// tears down the Monitor's goroutine and its subtree watch. Without this a settled goal
	// leaks a monitor goroutine (and, on docd, a live watch) until engine shutdown — and the
	// monitor could block permanently on send once its one-slot buffer fills with no reader.
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	mon := NewMonitor(done, e.store)
	ch := mon.Watch(watchCtx) // exactly the subtrees doneWhen reads; no root watch
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadlineC:
			// The clock struck. Done wins the tie: re-check before defaulting to unachievable.
			if ok, w := done.Eval(e.store); ok {
				e.commitGoal(g, "done", w)
				return
			}
			e.commitGoal(g, "unachievable", nil)
			return
		case st, ok := <-ch:
			if !ok {
				return
			}
			if st.True {
				e.commitGoal(g, "done", st.Witness)
				return
			}
			e.updateGoalStatus(g, done)
		}
	}
}

// updateGoalStatus refreshes the live fluent on goal:instance while the goal is active:
// status + the waitingOn list (the unmet leaves of doneWhen). Never touches a terminal goal.
func (e *Engine) updateGoalStatus(g *Goal, done *Formula) {
	waiting := waitingLabels(done.Unmet(e.store))
	e.store.Modify(goalRef(g.Def.Name), func(cur entity.Payload) entity.Payload {
		if t, _ := cur["terminal"].(bool); t {
			return cur // already latched — leave it (terminal is monotonic)
		}
		p := clonePayload(cur)
		p["waitingOn"] = waiting
		if len(waiting) > 0 {
			p["status"] = "blocked"
		} else {
			p["status"] = "active"
		}
		return p
	})
}

// commitGoal latches a terminal outcome ("done"|"unachievable") onto goal:instance —
// terminal-once via a CAS predicated on terminal=false, so a done/deadline race lands exactly
// one outcome. On a successful latch it writes the ordinary goal:done / goal:failed fact
// (which a charter trigger may match) and reaps the owned triggers. A refused CAS means the
// goal already terminated — a no-op.
func (e *Engine) commitGoal(g *Goal, outcome string, witness []entity.Ref) {
	ref := goalRef(g.Def.Name)
	cur, _ := e.store.Get(ref)
	if t, _ := cur.Payload["terminal"].(bool); t {
		return // already terminal
	}
	next := clonePayload(cur.Payload)
	next["terminal"] = true
	next["outcome"] = outcome
	next["status"] = outcome
	next["waitingOn"] = []string{}
	if ids := refIDs(witness); len(ids) > 0 {
		next["witness"] = ids
	}

	committed := true
	if cc, ok := e.store.(entity.ConditionalCommitter); ok {
		var err error
		committed, err = cc.CommitIf(ref, next, entity.Precondition{Ref: ref, Pattern: map[string]any{"terminal": false}})
		if err != nil {
			e.tracef("goal %s: terminal commit error: %v", g.Def.Name, err)
			return
		}
	} else {
		// Best-effort terminal-once for a store without conditional commit.
		e.store.Modify(ref, func(p entity.Payload) entity.Payload {
			if t, _ := p["terminal"].(bool); t {
				committed = false
				return p
			}
			return next
		})
	}
	if !committed {
		return // a concurrent commit latched first — honor it
	}

	// The monotonic latch fact a charter may react to.
	if outcome == "done" {
		fact := entity.Payload{"goal": g.Def.Name}
		if g.Def.Subject != "" {
			fact["subject"] = g.Def.Subject
		}
		if ids := refIDs(witness); len(ids) > 0 {
			fact["witness"] = ids
		}
		e.store.Put(goalDoneRef(g.Def.Name), fact)
	} else {
		fact := entity.Payload{"goal": g.Def.Name, "reason": "deadline"}
		if g.Def.Subject != "" {
			fact["subject"] = g.Def.Subject
		}
		e.store.Put(goalFailedRef(g.Def.Name), fact)
	}
	e.tracef("goal %s: %s (witness %d facts) — reaping owned charter", g.Def.Name, outcome, len(witness))
	e.reapGoalTriggers(g)
}

// reapGoalTriggers removes the goal's owned triggers (the doing) — no orphaned charter once
// the goal is terminal. (Scope-based isolation via DeleteScope is the versed-level
// enhancement; at the engine level the owned triggers are removed by name.)
func (e *Engine) reapGoalTriggers(g *Goal) {
	for _, n := range g.trigNames {
		e.RemoveTrigger(n)
	}
}

// waitingLabels renders unmet requirements as short tags for the goal's waitingOn list.
func waitingLabels(reqs []*Requirement) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Label())
	}
	return out
}

// --- Goal steering: a goal is a flow with a destination, so reuse the flow control surface,
// keyed by the goal's subject. ---

// PauseGoal / ResumeGoal / CancelGoal / SteerGoal steer a goal's in-flight work by delegating
// to the flow keyed by the goal's subject. Report how many processes were signalled (0 if the
// goal has no subject or no in-flight work).
func (e *Engine) PauseGoal(name string) int  { return e.onGoalFlow(name, e.PauseFlow) }
func (e *Engine) ResumeGoal(name string) int { return e.onGoalFlow(name, e.ResumeFlow) }
func (e *Engine) CancelGoal(name string) int { return e.onGoalFlow(name, e.CancelFlow) }

func (e *Engine) SteerGoal(name, text, by string) int {
	subj, ok := e.goalSubject(name)
	if !ok {
		return 0
	}
	return e.SteerFlow(subj, text, by)
}

func (e *Engine) onGoalFlow(name string, fn func(subject string) int) int {
	subj, ok := e.goalSubject(name)
	if !ok {
		return 0
	}
	return fn(subj)
}

func (e *Engine) goalSubject(name string) (string, bool) {
	e.goalsMu.Lock()
	g, ok := e.goals[name]
	e.goalsMu.Unlock()
	if !ok || g.Def.Subject == "" {
		return "", false
	}
	return g.Def.Subject, true
}
