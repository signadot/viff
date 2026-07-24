package trigger

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/process"
)

// AddTrigger installs a trigger into the charter. It validates each gate's mode and
// rejects a duplicate name. The engine interprets the trigger directly — there is no
// separate compiled form.
func (e *Engine) AddTrigger(t Trigger) error {
	for _, g := range t.Gates {
		if _, err := g.mode(); err != nil {
			return err
		}
	}
	e.trigMu.Lock()
	for _, existing := range e.triggers {
		if existing.Name == t.Name {
			e.trigMu.Unlock()
			return fmt.Errorf("trigger %q already exists (remove it first)", t.Name)
		}
	}
	e.triggers = append(e.triggers, t)
	e.trigMu.Unlock()
	// In subtree (scoped) mode the engine runs no root loop: give this trigger its own
	// subtree watch. In baseline mode the dispatch loop already sees every change.
	if e.subtree {
		return e.watchTriggerSubtree(t)
	}
	return nil
}

// RemoveTrigger removes a trigger by name. Reports whether it existed. Builds a fresh
// slice, so a concurrent dispatch (which snapshots under the lock) is unaffected.
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
	e.triggers = nt
	return found
}

// Trigger returns one installed trigger by name.
func (e *Engine) Trigger(name string) (Trigger, bool) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	for _, t := range e.triggers {
		if t.Name == name {
			return t, true
		}
	}
	return Trigger{}, false
}

// Triggers returns the installed triggers (the charter).
func (e *Engine) Triggers() []Trigger {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	out := make([]Trigger, len(e.triggers))
	copy(out, e.triggers)
	return out
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
