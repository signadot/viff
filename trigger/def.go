package trigger

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/process"
)

// Def is the wire form of a trigger — fully declarative, no Go closure — so a
// charter can be installed over the wire, persisted, or reflected as an entity.
// This is what makes "the trigger carries a spec" possible: the action is data.
type Def struct {
	Name    string       `json:"name"`
	Matches []EventMatch `json:"matches"` // conjunctive (all-of)
	Gates   []GateSpec   `json:"gates"`   // conjunctive (all must proceed)
	Action  ActionSpec   `json:"action"`
}

// GateSpec is the wire form of a gate: a name and a mode, or a temporal Formula. When
// Formula is set the gate is monitored on the store's watch stream (see Monitor) rather
// than posed to a resolver; Mode is then optional (defaults to a blocking gate).
type GateSpec struct {
	Name    string   `json:"name,omitempty"`
	Mode    string   `json:"mode,omitempty"` // auto | ask | park
	Formula *Formula `json:"formula,omitempty"`
}

// ActionSpec is a declarative action: optionally set fields on an entity (a state
// transition) and/or spawn a process (a launch.Spec). It is the "action as data"
// that a Go-closure action can't be.
//
// Bindings available to templates: $subject is the matched entity's ref, and each
// field of the changed payload is bound by name (e.g. $flow, $stage). So a
// completion trigger can `set` on `setRef: "$flow"` — the flow subject the process
// carried — advancing a different entity than the one that fired.
type ActionSpec struct {
	Set    map[string]any `json:"set,omitempty"`
	SetRef string         `json:"setRef,omitempty"` // target for Set ("" = the matched entity)
	Spawn  *launch.Spec   `json:"spawn,omitempty"`
}

// AddDef compiles a declarative Def into a Trigger and installs it, recording the
// Def for `trigger list`.
func (e *Engine) AddDef(d Def) error {
	var gates []Gate
	for _, gs := range d.Gates {
		mode := gs.Mode
		if mode == "" && gs.Formula != nil {
			mode = "ask" // a temporal gate blocks (is monitored) until its formula holds
		}
		m, err := ParseMode(mode)
		if err != nil {
			return err
		}
		gates = append(gates, Gate{Name: gs.Name, Mode: m, Formula: gs.Formula})
	}
	t := Trigger{Name: d.Name, Matches: d.Matches, Gates: gates, Action: compileAction(d.Name, d.Action)}
	e.trigMu.Lock()
	for _, existing := range e.defs {
		if existing.Name == d.Name {
			e.trigMu.Unlock()
			return fmt.Errorf("trigger %q already exists (remove it first)", d.Name)
		}
	}
	e.triggers = append(e.triggers, t)
	e.defs = append(e.defs, d)
	e.trigMu.Unlock()
	// In subtree (scoped) mode the engine runs no root loop: give this trigger its own
	// subtree watch. In baseline mode the dispatch loop already sees every change.
	if e.subtree {
		return e.watchDefSubtree(t, d)
	}
	return nil
}

// RemoveTrigger removes a trigger and its def by name. Reports whether it existed.
// Builds fresh slices, so a concurrent dispatch (which snapshots under the lock) is
// unaffected.
func (e *Engine) RemoveTrigger(name string) bool {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	found := false
	var nt []Trigger
	for _, t := range e.triggers {
		if t.Name == name {
			found = true
			continue
		}
		nt = append(nt, t)
	}
	var nd []Def
	for _, d := range e.defs {
		if d.Name != name {
			nd = append(nd, d)
		}
	}
	e.triggers, e.defs = nt, nd
	return found
}

// Def returns one installed trigger def by name.
func (e *Engine) Def(name string) (Def, bool) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	for _, d := range e.defs {
		if d.Name == name {
			return d, true
		}
	}
	return Def{}, false
}

// compileAction turns an ActionSpec into a runnable action closure: apply the Set
// (a state transition, on SetRef or the matched entity) then spawn (if any).
func compileAction(name string, act ActionSpec) Action {
	return func(ev entity.Event, e *Engine) {
		b := bindingFor(ev)
		if act.Set != nil {
			target := ev.Ref
			if act.SetRef != "" {
				ref, err := entity.ParseRef(launch.Expand(act.SetRef, b))
				if err != nil {
					return // unresolved/invalid target (e.g. no $flow on this event) → skip
				}
				target = ref
			}
			// Atomic read-modify-write: carry the target's current payload and overlay
			// Set. Via Modify (compare-and-swap) so two triggers advancing the same
			// entity can't lose each other's fields.
			e.Store().Modify(target, func(cur entity.Payload) entity.Payload {
				p := clonePayload(cur)
				for k, v := range act.Set {
					p[k] = v
				}
				return p
			})
		}
		if act.Spawn != nil {
			spec := act.Spawn.Bind(b)
			if spec.Subject == "" {
				spec.Subject = ev.Ref.String()
			}
			// who-axis: if the spawn declares a governed provider selection, resolve it and
			// bind the chosen doer into Driver before spawn — no live swap (reassign =
			// cancel+respawn). Orthogonal to the controlPolicy that then transports Driver.
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
}

// spawnID makes an opaque, collision-resistant, URL-safe process id from the trigger
// name and the full ref. It routes through /processes/{id} (one path segment), so it
// must not contain path-breaking characters. It keys off the whole ref (not just the
// id, so demo:pr:1 and other:pr:1 differ), and when safeSeg had to rewrite a
// character — a lossy step where a/b and a_b would otherwise collapse — it appends a
// short hash of the raw ref to keep distinct refs distinct.
func spawnID(name string, ref entity.Ref) string {
	s := ref.String()
	safe := safeSeg(s)
	if safe == s {
		return name + "-" + safe // no rewrite → no collision possible
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%s-%s-%08x", name, safe, h.Sum32())
}

// safeSeg makes s usable as a single URL path segment: a process id routes through
// /processes/{id}, which matches one segment, so characters that break a path
// (slashes, fragment/query, whitespace) become '_'. It is lossy — see spawnID, which
// disambiguates with a hash when a rewrite occurs.
func safeSeg(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', '#', '?', '%', ' ', '\t', '\n':
			return '_'
		}
		return r
	}, s)
}

// bindingFor builds the template binding for an event: $subject = the matched ref,
// plus each changed-payload field by its name (e.g. $flow, $stage).
func bindingFor(ev entity.Event) launch.Binding {
	b := launch.Binding{"subject": ev.Ref.String()}
	for k, v := range ev.To {
		b[k] = fmt.Sprint(v)
	}
	return b
}

// Defs returns the installed declarative triggers (the charter).
func (e *Engine) Defs() []Def {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	out := make([]Def, len(e.defs))
	copy(out, e.defs)
	return out
}

// ParseMode maps a gate mode name to a process.Mode.
func ParseMode(s string) (process.Mode, error) {
	switch s {
	case "auto", "":
		return process.Auto, nil
	case "ask":
		return process.Ask, nil
	case "park", "hold":
		return process.Park, nil
	default:
		return process.Auto, fmt.Errorf("unknown gate mode %q (want auto|ask|park)", s)
	}
}
