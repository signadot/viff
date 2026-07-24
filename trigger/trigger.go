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
	"github.com/example/infra/verse/entity"
	"github.com/example/infra/verse/process"
)

// EventMatch is a conjunctive predicate over one entity change. Empty dimensions
// are wildcards; Fields require key==value in the event's To payload.
type EventMatch struct {
	System string `json:"system,omitempty"`
	Kind   string `json:"kind,omitempty"`
	// Op filters on the kind of change: "set" (created-or-updated) or "delete".
	// Create and update are not distinguished (see entity.Op) — to fire "only the
	// first time," match a state field the action then advances (a latch), not Op.
	Op     entity.Op      `json:"op,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// Matches reports whether ev satisfies every non-empty dimension.
func (m EventMatch) Matches(ev entity.Event) bool {
	if m.System != "" && m.System != ev.Ref.System {
		return false
	}
	if m.Kind != "" && m.Kind != ev.Ref.Kind {
		return false
	}
	if m.Op != "" && m.Op != ev.Op {
		return false
	}
	for k, want := range m.Fields {
		if ev.To == nil || ev.To[k] != want {
			return false
		}
	}
	return true
}

// Action is what a trigger does when it fires and its gates proceed. It receives
// the matched event and the engine, so it can write entities (advancing state,
// which wakes the next trigger — closing the loop) and/or spawn processes.
type Action func(ev entity.Event, e *Engine)

// Gate is one decision point on a trigger firing: a named mode. A trigger's gates
// are conjunctive — every one must resolve positively for the action to fire (e.g.
// a deploy needing both a security and a release-manager approval).
type Gate struct {
	Name string
	Mode process.Mode // Auto: pass; Ask/Park: pose to the resolver, pass iff proceed

	// Formula, when non-nil, makes this a temporal gate: a temporal-logic formula over
	// store facts (Requirement leaves, temporal operators), monitored on the store's
	// watch stream by a Monitor rather than posed to the resolver. nil = a plain gate
	// resolved via Mode (unchanged). See fire + Monitor.
	Formula *Formula
}

// Trigger is one ECA rule: when every Match matches an observed change and every
// Gate resolves to proceed, Action fires. Matches are conjunctive (all-of); an
// empty Matches matches everything, an empty Gates fires with no approval.
type Trigger struct {
	Name    string
	Matches []EventMatch
	Gates   []Gate
	Action  Action
}

// matchesEvent reports whether every Match predicate matches ev (all-of).
func (t Trigger) matchesEvent(ev entity.Event) bool {
	for _, m := range t.Matches {
		if !m.Matches(ev) {
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
