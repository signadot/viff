package trigger

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir/kpath"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/process"
)

// AddTrigger installs a trigger into the charter. It validates each gate's mode and
// rejects a duplicate name. The engine interprets the trigger directly — there is no
// separate compiled form.
func (e *Engine) AddTrigger(t Trigger) error {
	if err := validTriggerName(t.Name); err != nil {
		return err
	}
	if err := t.Condition.validate(); err != nil {
		return fmt.Errorf("trigger %q: %w", t.Name, err)
	}
	if err := t.Action.validate(); err != nil {
		return fmt.Errorf("trigger %q: %w", t.Name, err)
	}
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

// ChainSep bounds a process id's CHAIN from its SUBJECT: `review.onDone@demo:pr:1` is
// "the onDone hook of trigger review, about demo:pr:1". It is '@' because the chain is a
// kpath (dots and, once actions hold lists, brackets) and the ref is an address (colons),
// so the boundary needs a character neither grammar spends — and because '@' survives a
// URL path segment and a shell word intact.
//
// A chain cannot contain '@' (validTriggerName rejects it, and every descent appends a
// Go field name), so the FIRST '@' is always the boundary, whatever the ref carries.
const ChainSep = "@"

// hashSep marks the disambiguating hash that a lossy safeSeg rewrite forces. Its own
// character, so nothing in the id has to guess whether a '-' is structure or part of a
// name — the defect the previous `name + "-" + ref` form had, since trigger names
// routinely carry '-' (ship-reviewed).
const hashSep = "+"

// spawnID makes a legible, collision-resistant, URL- and shell-safe process id from a
// CHAIN (the kpath of the spawning action inside its trigger — "review", "review.onDone",
// "review.onDone.onFailed") and the full ref. It routes through /processes/{id} (one path
// segment), so it must not contain path-breaking characters, and it gets pasted back onto
// a command line, so it must not contain shell globs either.
//
// It keys off the whole ref (not just the id, so demo:pr:1 and other:pr:1 differ), and
// when safeSeg had to rewrite a character — a lossy step where a/b and a_b would otherwise
// collapse — it appends a short hash of the raw ref to keep distinct refs distinct.
func spawnID(chain string, ref entity.Ref) string {
	s := ref.String()
	safe := safeSeg(s)
	if safe == s {
		return chain + ChainSep + safe // no rewrite → no collision possible
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%s%s%s%s%08x", chain, ChainSep, safe, hashSep, h.Sum32())
}

// chainOf splits a spawn id back into its chain and the (safeSeg'd) subject it is about.
// Not the inverse of spawnID — safeSeg is lossy and the hash is not a ref — but the chain
// half IS exact, which is what reads a process's position in its trigger's action tree.
func chainOf(id string) (chain string, rest string, ok bool) {
	chain, rest, ok = strings.Cut(id, ChainSep)
	return chain, rest, ok
}

// validTriggerName rejects a name that cannot be the root segment of a chain kpath. A
// chain is a real kpath — `review.onDone` addresses the Action at `.onDone` inside trigger
// `review` — so the root must be one bare field: no '.', no bracket of any kind, nothing
// kpath would have to QUOTE (a quote is not legal unescaped in a URL path), and nothing a
// shell reads as a glob.
//
// This also closes a latent collision in the old ':'-joined form, where a trigger named
// `review:done` produced the same spawn id as trigger `review`'s done hook.
func validTriggerName(name string) error {
	if name == "" {
		return fmt.Errorf("trigger name is empty")
	}
	kp, err := kpath.Parse(name)
	if err != nil {
		return fmt.Errorf("trigger name %q is not a kpath field: %w", name, err)
	}
	// One segment, a plain field, and it must render back to itself — which is what
	// rejects a name kpath would quote, without this having to know the quoting rules.
	if kp == nil || kp.Next != nil || kp.Field == nil || kp.Wild() || kp.String() != name {
		return fmt.Errorf("trigger name %q must be a single bare kpath field: a chain "+
			"addresses the action inside a trigger (review.onDone), so the root cannot "+
			"contain '.', brackets, or anything needing quotes", name)
	}
	if i := strings.IndexAny(name, ChainSep+hashSep+"*?[]{}()\\ \t"); i >= 0 {
		return fmt.Errorf("trigger name %q contains %q, which a process id built on it "+
			"would spend as punctuation — %q bounds the subject, %q marks a hash, and the "+
			"rest are shell globs that make an id unpasteable", name, name[i], ChainSep, hashSep)
	}
	return nil
}

// safeSeg makes s usable as a single URL path segment: a process id routes through
// /processes/{id}, which matches one segment, so characters that break a path
// (slashes, fragment/query, whitespace) become '_'. It also strips process.GateSep,
// which does not break a path but delimits a gate id from its owner — an owner id
// carrying one would make `resolve` split it in the wrong place and route the answer
// to a process that does not exist. It is lossy — see spawnID, which disambiguates
// with a hash when a rewrite occurs.
func safeSeg(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', '#', '?', '%', ' ', '\t', '\n':
			return '_'
		}
		if strings.ContainsRune(process.GateSep, r) {
			return '_'
		}
		return r
	}, s)
}

// payloadBinding binds each field of the changed payload by its name (e.g. $flow, $stage,
// $path). It carries no $subject: resolving a Subject expression ("$flow") needs these
// bindings, so the subject cannot be among them without circularity.
func payloadBinding(ev entity.Event) launch.Binding {
	b := launch.Binding{}
	for k, v := range ev.To {
		b[k] = fmt.Sprint(v)
	}
	return b
}

// bindingFor builds the template binding for a firing: the changed payload's fields, plus
// $subject — the RESOLVED subject the action is about, which coincides with the matched ref
// only when no Subject was declared.
//
// $subject is set LAST, deliberately: it is the one reserved binding, and a payload field
// happening to be called "subject" must not shadow it. (It used to be set first and
// overwritten, which is why a goal:done fact carrying a `subject` field silently redirected
// $subject — see goal.go, which now also publishes `flow` for that job.)
func bindingFor(ev entity.Event, subject entity.Ref) launch.Binding {
	b := payloadBinding(ev)
	b["subject"] = subject.String()
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
