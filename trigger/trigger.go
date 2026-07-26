// Package trigger is the ECA core: it observes the entity-verse, matches triggers
// against each change, gates the ones that match, and fires their actions. This is
// the loop that makes SDLC flows *emergent* — no one authors the path; it falls
// out of triggers reacting to state that other actions write.
//
// Two grammars carry the whole model, and keeping them apart is the point:
//
//	Match   — POINT-IN-TIME: which entities satisfy this right now. The atom, and the
//	          leaf of every Formula. A Subject and a flow address are bare Matches.
//	Formula — TEMPORAL: matches composed with operators (quorum/fact/until/deadline)
//	          over the verse's evolution. A Condition's When, a Gate's Formula, and a
//	          Goal's DoneWhen are Formulas, differing only in consequence.
//
// Name entities at an instant → a Match; relate the verse over time → a Formula. See
// docs/trigger-model.md.
package trigger

import (
	"fmt"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/process"
)

// Trigger is one ECA rule — fully declarative (JSON-serializable, no Go closure), so a
// charter can be installed over the wire, persisted, or reflected as an entity, and the
// engine interprets it directly (there is no separate compiled form). When the Condition
// holds and every Gate resolves to proceed, the Action fires. Gates are conjunctive
// (all-of); an empty Gates fires with no approval.
//
//tony:schemagen=verse-trigger,notag
type Trigger struct {
	Name      string    `tony:"field=name"`
	Condition Condition `tony:"field=condition"`
	Gates     []Gate    `tony:"field=gates,omitzero"` // conjunctive (all must proceed)
	Action    Action    `tony:"field=action"`
}

// Condition is the ECA "cause": what fires the trigger, and what the firing is about.
// When and Subject are grouped rather than nested because the Subject is resolved at the
// instant When fires — there is no subject before the match — yet neither field owns the
// other. Two halves of "when X, about Y", in two DIFFERENT grammars: When is temporal,
// Subject is point-in-time.
//
//tony:schemagen=verse-condition,notag,codec=custom
type Condition struct {
	// When fires the trigger. Its atom form is a single match, dispatched on the edge of
	// one change event; temporal operators relate the verse's evolution instead.
	When *Formula `tony:"field=when"`
	// Subject is what the firing is ABOUT, selected at the fire instant — never
	// accumulated, so it carries no temporal operators. Omitted = the matched entity; an
	// address names one entity; a system+kind selector is a pattern — matched against the
	// firing's own subject (a reflected process's `flow`) when it has one, else the SET of
	// matching entities in the store, one action per member.
	Subject Match `tony:"field=subject,omitzero"`
}

// validate reports whether the condition is installable. It is deliberately strict: the
// "when" is where complexity hides, so a charter that cannot mean what it says fails at
// install rather than silently matching nothing.
func (c Condition) validate() error {
	if c.When == nil {
		return fmt.Errorf(`condition.when required (use {} to match everything)`)
	}
	if !c.When.IsMatchAtom() {
		return fmt.Errorf("condition.when carries temporal operators, which are not evaluated " +
			"yet: a temporal when has no change to dispatch on, so it needs a long-lived monitor " +
			"over the subtrees it reads (see docs/trigger-model-plan.md). Use a point-in-time " +
			"match here; temporal composition works today in a gate's `formula` and a goal's " +
			"`doneWhen`")
	}
	if err := c.When.Req.Facts.Validate(); err != nil {
		return fmt.Errorf("condition.when: %w", err)
	}
	if len(c.Subject) == 0 {
		return nil // the matched entity
	}
	if err := c.Subject.Validate(); err != nil {
		return fmt.Errorf("condition.subject: %w", err)
	}
	if _, names := c.Subject.Names(); names {
		return nil // one entity, named
	}
	if c.Subject.Selects() {
		return nil // a pattern: matched against the firing's own subject (a process's flow), else fanned out
	}
	return fmt.Errorf(`condition.subject must be an address ("demo:pr:1", "demo:pr:*") or name ` +
		`both a system and a kind to select a set (it has to name a definite kind to select and ` +
		`spawn for)`)
}

// matchesEvent reports whether an observed change fires this trigger — the edge face of
// When. A temporal When has no edge and is resolved by Monitor instead.
func (t Trigger) matchesEvent(ev entity.Event) bool {
	return t.Condition.When.MatchEvent(ev)
}

// opensTo reports whether the condition opts into the given system (used by the ignition
// guard to require opt-in for the reflected process:* namespace). It asks which subtrees
// When reads, so it holds for a temporal When over several systems too.
func (t Trigger) opensTo(system string) bool {
	for _, st := range t.Condition.When.Subtrees() {
		if st[0] == system {
			return true
		}
	}
	return false
}

// resolveSubject names the entities this firing is about, at the instant it fires. It
// yields 0, 1, or (once fan-out lands) N refs; a process/flow/goal always has exactly one
// subject, so the engine acts once per resolved ref.
func (t Trigger) resolveSubject(ev entity.Event, store entity.Store) []entity.Ref {
	m := t.Condition.Subject
	if len(m) == 0 {
		return []entity.Ref{ev.Ref} // the matched entity
	}
	if ref, names := m.Names(); names {
		return []entity.Ref{ref} // one entity, named — a `set` may bring it into existence
	}
	// A pattern (an id glob, or a system:kind selector). If the firing event carries its OWN
	// subject — a reflected process's `flow` — the pattern resolves against THAT one ref: a
	// process about demo:pr:1, matched by a `demo:pr:*` subject, advances demo:pr:1. The pattern
	// both names which flows this trigger acts on and picks the specific one — no back-reference
	// to a prior trigger, just the subject grammar applied to the process's subject. Otherwise
	// the pattern selects over the store (broadcast, one action per member).
	if flow := entity.Str(ev.To, "flow"); flow != "" {
		fref, err := entity.ParseRef(flow)
		if err != nil || !m.MatchRef(fref) {
			return nil // the carried subject is unresolvable, or isn't what this trigger acts on
		}
		return []entity.Ref{fref}
	}
	return m.Select(store) // a selection over what exists — one action per member
}

// Gate is one decision point on a trigger firing: a named mode, or a temporal Formula. A
// trigger's gates are conjunctive — every one must resolve positively for the action to
// fire (e.g. a deploy needing both a security and a release-manager approval). When Formula
// is set the gate is monitored on the store's watch stream (see Monitor) rather than posed
// to a resolver; Mode is then optional (defaults to a blocking gate).
//
//tony:schemagen=verse-trigger-gate,notag
type Gate struct {
	Name    string   `tony:"field=name,omitzero"`
	Mode    string   `tony:"field=mode,omitzero"` // auto | ask | park
	Formula *Formula `tony:"field=formula,omitzero"`
}

// mode resolves the gate's autonomy mode. A temporal gate (Formula set) with no explicit
// mode blocks — it is monitored until its formula holds; an empty mode is otherwise auto.
func (g Gate) mode() (process.Mode, error) {
	m := g.Mode
	if m == "" && g.Formula != nil {
		m = "ask"
	}
	return ParseMode(m)
}

// Action is what a trigger does when it fires and its gates proceed — declarative data,
// not a Go closure: optionally set fields on an entity (a state transition) and/or spawn a
// process (a launch.Spec). The engine interprets it via apply.
//
// It has no target of its own: what an action acts on is the Condition's Subject. (This
// replaced a `setRef` field and a charter-set `spawn.subject`, which expressed the same
// thing twice, by side channel, and only existed because $subject was wired to the matched
// entity rather than to the subject.)
//
// Bindings available to templates: each field of the changed payload by name ($stage,
// $path, …), and $subject — the RESOLVED subject, set last so a payload field cannot
// shadow it.
//
//tony:schemagen=verse-action,notag
type Action struct {
	// Patch is applied to the subject's payload with tony.Patch — the WRITE half of the
	// vocabulary whose read half is a Match. It merges rather than assigns, so a nested
	// write touches one subtree instead of replacing its parent, and it can carry the
	// mergeop operators (`!delete`, `!replace`, …) that a field overlay had no way to say.
	//
	// It replaced `set`, a shallow top-level overlay. The name went with the semantics:
	// this deep-merges, deletes and replaces, and "set" describes one of those. (The read
	// side keeps `fields` beside `payload` because requiring an object there is what makes
	// `fields: "stage=open"` an install error; nothing here needs that guard — `patch: 42`
	// is a legitimate whole-value replace.) See docs/patch-plan.md.
	// Spelled entity.Payload, which is what it IS. That was impossible until go-tony
	// v0.0.95: the resolver could not follow an alias ("Payload is not a named type"), so
	// every schemagen'd field had to say *ir.Node and the domain vocabulary was absent from
	// exactly the declarations a reader of the schema reads. Fixed, so the name is back.
	Patch entity.Payload `tony:"field=patch,omitzero"`
	Spawn *launch.Spec   `tony:"field=spawn,omitzero"`

	// Seq runs its elements IN ORDER, each starting when the previous one completes. It is
	// the only construct the model needed for sequencing, because parallelism already has
	// one: independent triggers matching the same condition. The two axes stay orthogonal in
	// the id space — breadth is distinct roots sharing a subject, depth is extension of one
	// chain — which is why there is no `par`. See docs/seq-plan.md.
	//
	// The sequence owns the DONE slot of each element: an element may carry onFailed /
	// onCancelled / onOrphaned as off-ramps (firing one ends the sequence), but not onDone,
	// because the list already says what comes next. This action's own onDone fires after the
	// WHOLE sequence, so a sequence is a unit with its own completion and nesting composes.
	Seq []Action `tony:"field=seq,omitzero"`

	// Completion hooks: when this action's Spawn reaches a terminal status, the matching
	// hook fires as a full Action (set + spawn + its own onX) about the same subject — so a
	// chain (review → merge → approve) reads top-to-bottom in one trigger. The engine applies
	// it scoped to exactly this spawn's process (see Engine.applyHook / hookFor). onOrphaned is
	// inert until resume rehydrates hooks (see docs/completion-hooks.md).
	OnDone      *Action `tony:"field=onDone,omitzero"`      // status done (success)
	OnFailed    *Action `tony:"field=onFailed,omitzero"`    // status failed
	OnCancelled *Action `tony:"field=onCancelled,omitzero"` // status cancelled
	OnOrphaned  *Action `tony:"field=onOrphaned,omitzero"`  // status orphaned
}

// hookFor returns the completion hook for a terminal status (nil if none). The four terminal
// statuses map to the four onX fields; non-terminal statuses have no hook.
func (a *Action) hookFor(s process.Status) *Action {
	switch s {
	case process.Done:
		return a.OnDone
	case process.Failed:
		return a.OnFailed
	case process.Cancelled:
		return a.OnCancelled
	case process.Orphaned:
		return a.OnOrphaned
	}
	return nil
}

// hookField is hookFor's other half: the NAME of the field a terminal status fires, which
// is the segment a chain kpath descends by. `review` + status done → `review.onDone`, which
// is the address of that hook inside the trigger — so a chain reads back as a lookup into
// the charter rather than as a private naming scheme. Empty for a non-terminal status.
func hookField(s process.Status) string {
	switch s {
	case process.Done:
		return "onDone"
	case process.Failed:
		return "onFailed"
	case process.Cancelled:
		return "onCancelled"
	case process.Orphaned:
		return "onOrphaned"
	}
	return ""
}

// hasHooks reports whether any completion hook is set — used to require a spawn for them.
func (a *Action) hasHooks() bool {
	return a.OnDone != nil || a.OnFailed != nil || a.OnCancelled != nil || a.OnOrphaned != nil
}

// validate reports whether the action is installable (called from AddTrigger). A completion
// hook needs a process to complete, so any onX requires a spawn; each hook is itself an action
// and is validated recursively.
func (a Action) validate() error {
	// A hook needs something that can COMPLETE. That used to mean a spawn; a sequence
	// completes too, when its last element does.
	if a.hasHooks() && a.Spawn == nil && len(a.Seq) == 0 {
		return fmt.Errorf("action.onX requires a spawn or a seq — a completion hook needs " +
			"something that finishes")
	}
	if len(a.Seq) > 0 && a.Spawn != nil {
		return fmt.Errorf("action has both `spawn` and `seq`: which one the sequence waits " +
			"for would be a coin flip. Put the spawn in the sequence as its first element")
	}
	for i, el := range a.Seq {
		// The sequence owns each element's done slot — the list already says what comes
		// next, so a second answer in the same place is a contradiction, not a composition.
		if el.OnDone != nil {
			return fmt.Errorf("seq[%d] carries onDone: a sequence already says what follows "+
				"an element. Use the next element, or this action's own onDone to run after "+
				"the WHOLE sequence", i)
		}
		if err := el.validate(); err != nil {
			return fmt.Errorf("seq[%d]: %w", i, err)
		}
	}
	for _, h := range []*Action{a.OnDone, a.OnFailed, a.OnCancelled, a.OnOrphaned} {
		if h == nil {
			continue
		}
		if err := h.validate(); err != nil {
			return err
		}
	}
	return nil
}

// noNestedSeq rejects a second `seq` anywhere below one. Stated over CHAINS rather than over
// elements — "no chain may contain two seq segments" — because an element's onFailed holding
// a sequence nests just as much as an element holding one directly, only further away.
//
// It is what keeps re-fire idempotence a PREDICATE rather than a search: with at most one seq
// per chain, "the sequence this live process belongs to" is a unique ancestor rather than a
// stack of them (docs/seq-plan.md).
func (a Action) noNestedSeq(inSeq bool) error {
	if len(a.Seq) > 0 {
		if inSeq {
			return fmt.Errorf("a sequence inside a sequence: no chain may carry two `seq` " +
				"segments, because then which sequence a running process belongs to has more " +
				"than one answer")
		}
		inSeq = true
	}
	for _, el := range a.Seq {
		if err := el.noNestedSeq(inSeq); err != nil {
			return err
		}
	}
	for _, h := range []*Action{a.OnDone, a.OnFailed, a.OnCancelled, a.OnOrphaned} {
		if h == nil {
			continue
		}
		if err := h.noNestedSeq(inSeq); err != nil {
			return err
		}
	}
	return nil
}

// apply runs the action once per resolved subject: apply the Set (a state transition on
// that subject) then spawn (if any). chain is this action's kpath inside its trigger (the
// trigger name at the top, `review.onDone` for a hook), used to derive the spawned process
// id. Writing entities advances state — which wakes the next trigger, closing the loop.
func (a Action) apply(chain string, ev entity.Event, subjects []entity.Ref, e *Engine) {
	for _, subj := range subjects {
		h, err := a.applyOne(chain, ev, subj, e)
		if err != nil {
			continue // already traced; this subject does not advance
		}
		// A sequence starts once the carrier's own write has landed. It cannot also have
		// spawned (validate rejects spawn+seq), so there is nothing in flight to race.
		if len(a.Seq) > 0 && h == nil {
			e.startSeq(chain, a, ev, subj)
		}
	}
}

// applyOne is apply for exactly one subject: the write, then the spawn. It returns the
// spawned process (nil when the action starts none) and the write's error, which is what lets
// a sequence tell "this element is running" from "this element is already finished" — the
// distinction advanceSeq turns on.
func (a Action) applyOne(chain string, ev entity.Event, subj entity.Ref, e *Engine) (process.ProcHandle, error) {
	if a.Patch != nil {
		if err := e.patchEntity(subj, a.Patch); err != nil {
			// Fail the firing rather than continuing into the spawn: a process started about
			// a state the trigger FAILED to establish is worse than no process at all, and it
			// would be the harder failure to read afterwards.
			e.tracef("trigger %s: patch on %s failed (%v); not spawning", chain, subj, err)
			return nil, err
		}
	}
	if a.Spawn == nil {
		return nil, nil
	}
	spec := a.Spawn.Bind(bindingFor(ev, subj))
	// The subject is the engine's to set, not the charter's: a process is about exactly
	// one subject, and which one is the Condition's answer.
	spec.Subject = subj.String()
	// who-axis: if the spawn declares a governed provider selection, resolve it and bind
	// the chosen doer into Driver before spawn — no live swap (reassign = cancel+respawn).
	// Orthogonal to the controlPolicy that then transports Driver.
	if spec.Provider != nil {
		driver, ok := e.selectProvider(spec.Provider, subj)
		if !ok {
			return nil, nil // no eligible doer / selection did not resolve — do not spawn
		}
		spec.Driver = driver
	}
	// The chain rides the spec so the SPAWNER can mirror it: mirror.Reflect owns the process
	// entity's payload and re-Puts it on every event, so a field the engine wrote afterwards
	// would be clobbered. Engine-owned, like Subject above.
	spec.Chain = chain
	// Keyed by the SUBJECT, not the matched entity, so a re-fire about the same subject is
	// idempotent (and, under fan-out, each member gets its own deterministic id). The action
	// carries its own completion hooks (onX): they ride the spawned process's flowProc and
	// fire when it reaches a terminal status. chain is this action's kpath, so a hook's own
	// spawn — or a sequence's next element — gets a distinct, deterministic id.
	hooks := a
	return e.spawnHooked(spawnID(chainID(chain), subj), spec, &hooks, chain), nil
}
