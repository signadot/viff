// Package trigger is the ECA core: it observes the entity-verse, matches triggers
// against each change, gates the ones that match, and fires their actions. This is
// the loop that makes SDLC flows *emergent* — no one authors the path; it falls
// out of triggers reacting to state that other actions write.
//
// The match grammar is deliberately tiny. "When" is the complexity sink, so it
// stays a trivial conjunction (system/kind/op/field-equality) and grows only as a
// real charter demands — resisting the reinvention of a workflow language inside
// the condition.
package trigger

import (
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/process"
)

// Trigger is one ECA rule — fully declarative (JSON-serializable, no Go closure), so a
// charter can be installed over the wire, persisted, or reflected as an entity, and the
// engine interprets it directly (there is no separate compiled form). When every Match
// matches an observed change and every Gate resolves to proceed, the Action fires. Matches
// are conjunctive (all-of); an empty Matches matches everything, an empty Gates fires with
// no approval.
type Trigger struct {
	Name    string           `json:"name"`
	Matches []entity.Pattern `json:"matches"` // conjunctive (all-of)
	Gates   []Gate           `json:"gates"`   // conjunctive (all must proceed)
	Action  Action           `json:"action"`
}

// matchesEvent reports whether every Match predicate matches ev (all-of).
func (t Trigger) matchesEvent(ev entity.Event) bool {
	for _, m := range t.Matches {
		if !m.MatchEvent(ev) {
			return false
		}
	}
	return true
}

// opensTo reports whether any Match opts into the given system (used by the
// ignition guard to require opt-in for the reflected process:* namespace).
func (t Trigger) opensTo(system string) bool {
	for _, m := range t.Matches {
		if m.System == system {
			return true
		}
	}
	return false
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
// Bindings available to templates: $subject is the matched entity's ref, and each field of
// the changed payload is bound by name (e.g. $flow, $stage). So a completion trigger can
// `set` on `setRef: "$flow"` — the flow subject the process carried — advancing a different
// entity than the one that fired.
type Action struct {
	Set    map[string]any `json:"set,omitempty"`
	SetRef string         `json:"setRef,omitempty"` // target for Set ("" = the matched entity)
	Spawn  *launch.Spec   `json:"spawn,omitempty"`
}

// apply runs the action against a matched event: apply the Set (a state transition, on
// SetRef or the matched entity) then spawn (if any). name is the firing trigger's name,
// used to derive the spawned process id. Writing entities advances state — which wakes the
// next trigger, closing the loop.
func (a Action) apply(name string, ev entity.Event, e *Engine) {
	b := bindingFor(ev)
	if a.Set != nil {
		target := ev.Ref
		if a.SetRef != "" {
			ref, err := entity.ParseRef(launch.Expand(a.SetRef, b))
			if err != nil {
				return // unresolved/invalid target (e.g. no $flow on this event) → skip
			}
			target = ref
		}
		// Atomic read-modify-write: carry the target's current payload and overlay Set. Via
		// Modify (compare-and-swap) so two triggers advancing the same entity can't lose
		// each other's fields.
		e.Store().Modify(target, func(cur entity.Payload) entity.Payload {
			p := clonePayload(cur)
			for k, v := range a.Set {
				p[k] = v
			}
			return p
		})
	}
	if a.Spawn != nil {
		spec := a.Spawn.Bind(b)
		if spec.Subject == "" {
			spec.Subject = ev.Ref.String()
		}
		// who-axis: if the spawn declares a governed provider selection, resolve it and bind
		// the chosen doer into Driver before spawn — no live swap (reassign = cancel+respawn).
		// Orthogonal to the controlPolicy that then transports Driver.
		if spec.Provider != nil {
			driver, ok := e.selectProvider(spec.Provider, ev.Ref)
			if !ok {
				return // no eligible doer / selection did not resolve — do not spawn
			}
			spec.Driver = driver
		}
		e.Spawn(spawnID(name, ev.Ref), spec)
	}
}
