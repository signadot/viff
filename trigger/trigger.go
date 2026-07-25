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
type Trigger struct {
	Name      string    `json:"name"`
	Condition Condition `json:"condition"`
	Gates     []Gate    `json:"gates,omitempty"` // conjunctive (all must proceed)
	Action    Action    `json:"action"`
}

// Condition is the ECA "cause": what fires the trigger, and what the firing is about.
// When and Subject are grouped rather than nested because the Subject is resolved at the
// instant When fires — there is no subject before the match — yet neither field owns the
// other. Two halves of "when X, about Y", in two DIFFERENT grammars: When is temporal,
// Subject is point-in-time.
type Condition struct {
	// When fires the trigger. Its atom form is a single match, dispatched on the edge of
	// one change event; temporal operators relate the verse's evolution instead.
	When *Formula `json:"when"`
	// Subject is what the firing is ABOUT, selected at the fire instant — never
	// accumulated, so it carries no temporal operators. Omitted = the matched entity; a
	// ref or $var ("$flow") = that one entity; a system+kind selector = the SET of
	// matching entities, one action per member.
	Subject Match `json:"subject,omitempty"`
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
	if _, deferred := c.Subject.Ref(); deferred {
		return nil // a $var, resolved (and re-checked as an address) at the fire instant
	}
	if _, names := c.Subject.Names(); names {
		return nil // one entity, named
	}
	if c.Subject.Selects() {
		return nil // a set: the engine fans out, one action per member
	}
	return fmt.Errorf(`condition.subject must be an address ("demo:pr:1", "demo:pr:*"), a $var ` +
		`("$flow"), or name both a system and a kind to select a set (it has to name a definite ` +
		`kind to select and spawn for)`)
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
	if tmpl, deferred := m.Ref(); deferred {
		// A $var: expand against the changed payload ($flow, $stage, …), then read the result
		// as an address, so an expansion is held to the same grammar an authored string is.
		expanded, err := ParseExpr(launch.Expand(tmpl, payloadBinding(ev)))
		if err != nil {
			return nil // unresolved/invalid (e.g. no $flow on this event) → the firing is a no-op
		}
		// A partial address is a no-op, never a widening. An unexpanded "$flow" or a payload
		// carrying a half-written ref would otherwise leave dimensions unset — and an unset
		// dimension is a WILDCARD, so the firing would fan out across a kind or a system
		// instead of doing nothing.
		if _, names := expanded.Names(); !names && !expanded.Selects() {
			return nil
		}
		m = expanded
	}
	if ref, names := m.Names(); names {
		return []entity.Ref{ref} // one entity, named — a `set` may bring it into existence
	}
	return m.Select(store) // a selection over what exists — one action per member
}

// Gate is one decision point on a trigger firing: a named mode, or a temporal Formula. A
// trigger's gates are conjunctive — every one must resolve positively for the action to
// fire (e.g. a deploy needing both a security and a release-manager approval). When Formula
// is set the gate is monitored on the store's watch stream (see Monitor) rather than posed
// to a resolver; Mode is then optional (defaults to a blocking gate).
type Gate struct {
	Name    string   `json:"name,omitempty"`
	Mode    string   `json:"mode,omitempty"` // auto | ask | park
	Formula *Formula `json:"formula,omitempty"`
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
// Bindings available to templates: each field of the changed payload by name ($flow,
// $stage, $path), and $subject — the RESOLVED subject, set last so a payload field cannot
// shadow it.
type Action struct {
	Set   map[string]any `json:"set,omitempty"`
	Spawn *launch.Spec   `json:"spawn,omitempty"`
}

// apply runs the action once per resolved subject: apply the Set (a state transition on
// that subject) then spawn (if any). name is the firing trigger's name, used to derive the
// spawned process id. Writing entities advances state — which wakes the next trigger,
// closing the loop.
func (a Action) apply(name string, ev entity.Event, subjects []entity.Ref, e *Engine) {
	for _, subj := range subjects {
		b := bindingFor(ev, subj)
		if a.Set != nil {
			// Atomic read-modify-write: carry the subject's current payload and overlay Set.
			// Via Modify (compare-and-swap) so two triggers advancing the same entity can't
			// lose each other's fields.
			e.Store().Modify(subj, func(cur entity.Payload) entity.Payload {
				p := clonePayload(cur)
				for k, v := range a.Set {
					p[k] = v
				}
				return p
			})
		}
		if a.Spawn == nil {
			continue
		}
		spec := a.Spawn.Bind(b)
		// The subject is the engine's to set, not the charter's: a process is about exactly
		// one subject, and which one is the Condition's answer.
		spec.Subject = subj.String()
		// who-axis: if the spawn declares a governed provider selection, resolve it and bind
		// the chosen doer into Driver before spawn — no live swap (reassign = cancel+respawn).
		// Orthogonal to the controlPolicy that then transports Driver.
		if spec.Provider != nil {
			driver, ok := e.selectProvider(spec.Provider, subj)
			if !ok {
				continue // no eligible doer / selection did not resolve — do not spawn
			}
			spec.Driver = driver
		}
		// Keyed by the SUBJECT, not the matched entity, so a re-fire about the same subject
		// is idempotent (and, under fan-out, each member gets its own deterministic id).
		e.Spawn(spawnID(name, subj), spec)
	}
}
