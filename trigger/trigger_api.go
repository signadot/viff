package trigger

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/tony-format/go-tony/ir/kpath"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/process"
)

// AddTrigger installs a trigger into the charter — durably: the rule is written into the
// verse as its trigger:rule:<name> entity, so it is still there after a restart (see
// charter.go). It validates each gate's mode and rejects a duplicate name. The engine
// interprets the trigger directly — there is no separate compiled form.
func (e *Engine) AddTrigger(t Trigger) error { return e.addTrigger(t, true) }

// addTrigger installs a trigger, writing it to the durable charter when durable.
//
// Two callers pass false, for the same reason: the rule is already written down elsewhere.
// LoadCharter's rules came out of the store. A GOAL's owned triggers are carried in the
// goal's own record and reinstalled with it (LoadGoals), so writing them out again would
// give one rule two owners and two lifetimes — the standalone copy would survive the reap
// that ends the goal, and keep firing for something nobody is pursuing any more.
func (e *Engine) addTrigger(t Trigger, durable bool) error {
	if err := validTriggerName(t.Name); err != nil {
		return err
	}
	// A patch arrives as a raw node — the generated codec passes it through untouched — so
	// the JSON spelling of an operator (`{"tmp": {"delete": null}}`) is still a plain object
	// at this point. desugar turns it into the tag the mergeops actually read, which is what
	// keeps a .json charter and a .tony charter one grammar rather than two (the same job
	// opNode does for a Match's operators). Done once, at install, so the firing path sees
	// only the canonical tagged form.
	desugarPatches(&t.Action)
	if err := t.Condition.validate(); err != nil {
		return fmt.Errorf("trigger %q: %w", t.Name, err)
	}
	if err := t.Action.validate(); err != nil {
		return fmt.Errorf("trigger %q: %w", t.Name, err)
	}
	if err := t.Action.noNestedSeq(false); err != nil {
		return fmt.Errorf("trigger %q: %w", t.Name, err)
	}
	for _, g := range t.Gates {
		if _, err := g.mode(); err != nil {
			return err
		}
	}
	// LEVEL DISCIPLINE: a subject inside its stratum's pattern, a derive inside some strictly
	// higher one's. Syntactic like the rest, and neither reads the store.
	//
	// Before the latch check for the reason that check gives for being last — a charter wrong in
	// some other way should be told about THAT — and a rule acting outside the level it declared
	// is another way, and a more specific one.
	//
	// The verdict is held rather than acted on, because a rule whose stratum is not installed yet
	// is not refused (see below) and must still face every check that does not depend on the
	// ordering.
	ok, levelErr := e.checkLevels(t)
	if levelErr != nil {
		return fmt.Errorf("trigger %q: %w", t.Name, levelErr)
	}
	// LAST, and fatal. A charter that cannot stop spawning is refused rather than logged: it
	// is not a style question but a rule with no end state, and the operator installing it is
	// the one person positioned to fix it.
	//
	// Last so that a charter which is wrong in some other way is told about THAT. A nested
	// sequence also fails this check, and "your action re-spawns" would be a true answer to
	// the wrong question.
	if err := checkLatch(t.Condition, t.Action); err != nil {
		return fmt.Errorf("trigger %q: %w", t.Name, err)
	}
	// A rule naming a stratum that is not installed yet is HELD rather than refused: a rule and
	// a stratum are separate entities, either can arrive first, and making that order matter is
	// exactly what one-entity-per-rule exists to avoid. It is written down and installed when
	// its stratum arrives — having passed everything above, so holding defers the ORDERING
	// question and nothing else.
	if !ok {
		// The name is unique across the WHOLE charter, held rules included — otherwise two
		// installs of one name would both be waiting, and whichever stratum arrived first
		// would decide which rule the name meant.
		e.trigMu.Lock()
		if err := e.nameTaken(t.Name); err != nil {
			e.trigMu.Unlock()
			return err
		}
		e.pending = append(e.pending, t)
		e.trigMu.Unlock()
		if durable {
			if err := e.persist(t); err != nil {
				e.unhold(t.Name)
				return err
			}
		}
		e.logger.Warn("rule held: it names a stratum that is not installed",
			"trigger", t.Name, "stratum", t.Stratum,
			"note", "it is written down and will install when the stratum arrives; it does not fire until then")
		return nil
	}
	// Advisory checks: a charter can mean what it says and still not mean what its author
	// intended. These name it and install it anyway.
	for _, w := range advise(t) {
		e.logger.Warn("charter", "trigger", t.Name, "note", w)
	}
	e.trigMu.Lock()
	if err := e.nameTaken(t.Name); err != nil {
		e.trigMu.Unlock()
		return err
	}
	e.triggers = append(e.triggers, t)
	e.trigMu.Unlock()
	// Persist before observing: in subtree mode the watch is what makes the trigger fire,
	// so a rule that cannot be made durable never starts. (In baseline mode the dispatch
	// loop already reads the charter, so a firing can race a failed persist and be rolled
	// back after it — the same way removing a trigger stops FURTHER firings rather than
	// cancelling one in flight.)
	if durable {
		if err := e.persist(t); err != nil {
			e.RemoveTrigger(t.Name) // an installed rule the next restart would lose is worse than none
			return err
		}
	}
	e.recordShapes(t)
	// In subtree (scoped) mode the engine runs no root loop: give this trigger its own
	// subtree watch. In baseline mode the dispatch loop already sees every change.
	if e.subtree {
		return e.watchTriggerSubtree(t)
	}
	return nil
}

// RemoveTrigger removes a trigger by name. Reports whether it existed. Builds a fresh
// slice, so a concurrent dispatch (which snapshots under the lock) is unaffected.
//
// It also STOPS THE TRIGGER'S OBSERVATION, which in subtree mode is not the charter's
// business but a set of live goroutines: each trigger holds its own subtree watch, so a
// removal that only edits the charter leaves those watchers running and the removed trigger
// keeps firing — invisibly, since nothing lists it any more. Dropping it from the dispatch
// snapshot is enough only for the root loop, where observation is shared and the charter
// really is the whole answer.
//
// And it DELETES THE RULE'S CHARTER ENTITY, which is the same argument one level out: a
// removal that only stops the live engine leaves the rule standing in the store, and the
// next start reinstalls what an operator removed.
func (e *Engine) RemoveTrigger(name string) bool {
	e.trigMu.Lock()
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
	if found {
		e.forgetShapes(name)
		e.stopWatchers(name)
	}
	// A HELD rule is removable too, and by the same name: it was written down, so leaving it
	// behind would install it the moment its stratum arrived — a rule coming back to life
	// after an operator removed it, which is the failure deleting the charter entity exists
	// to prevent, one step earlier.
	var np []Trigger
	for _, t := range e.pending {
		if t.Name == name {
			found = true
			continue
		}
		np = append(np, t)
	}
	e.pending = np
	e.trigMu.Unlock()
	if found {
		e.forget(name) // the durable copy — off the charter lock, since it is a store write
	}
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

// CheckFields reports a field name a rule does not have, before the rule is decoded.
//
// It exists because an unknown field DECODES SILENTLY: gomap ignores what it does not
// recognise and has no strict-unmap option, so `startum: sdlc` installs cleanly into the
// implicit stratum and reports success — the one field that says which guarantees apply is
// exactly what a misspelling discards. Every field below has that character to some degree
// (`derve:` is a derive that never happens, `gatse:` an approval nobody is asked for), and the
// failure is always the same shape: the charter does less than it says, quietly.
//
// It is a check at the DECODE BOUNDARY rather than in the codec, because the codec is
// generated. Depth stops where the shape stops being this package's: a spawn is a launch.Spec,
// and a patch or a derive is an arbitrary payload whose keys are the author's.
func CheckFields(node *ir.Node) error {
	return checkFields(node, "trigger", map[string]bool{
		"name": true, "stratum": true, "condition": true, "gates": true, "action": true,
	}, func(field string, val *ir.Node) error {
		switch field {
		case "condition":
			return checkFields(val, "condition", map[string]bool{
				"subject": true, "on": true, "when": true,
			}, nil)
		case "action":
			return checkAction(val)
		}
		return nil
	})
}

// checkAction is CheckFields for an action, recursing where an action nests one: a sequence's
// elements and the four completion hooks, each of which is a full action.
func checkAction(node *ir.Node) error {
	return checkFields(node, "action", map[string]bool{
		"patch": true, "derive": true, "spawn": true, "seq": true,
		"onDone": true, "onFailed": true, "onCancelled": true, "onOrphaned": true,
	}, func(field string, val *ir.Node) error {
		switch field {
		case "seq":
			if val.Type != ir.ArrayType {
				return nil // shape is validate's to report, in its own words
			}
			for i, el := range val.Values {
				if err := checkAction(unwrap(el)); err != nil {
					return fmt.Errorf("seq[%d]: %w", i, err)
				}
			}
		case "onDone", "onFailed", "onCancelled", "onOrphaned":
			if err := checkAction(val); err != nil {
				return fmt.Errorf("%s: %w", field, err)
			}
		}
		return nil
	})
}

// checkFields names any key outside the known set, and descends where descend says to.
func checkFields(node *ir.Node, what string, known map[string]bool, descend func(string, *ir.Node) error) error {
	node = unwrap(node)
	if node == nil || node.Type != ir.ObjectType {
		return nil // not an object: the decoder's complaint to make, in its own terms
	}
	for i, field := range node.Fields {
		name := field.String
		if !known[name] {
			want := make([]string, 0, len(known))
			for k := range known {
				want = append(want, k)
			}
			sort.Strings(want)
			return fmt.Errorf("%s has no field %q (want any of %s) — an unknown field decodes "+
				"silently, so this would install as a rule that does less than it says",
				what, name, strings.Join(want, ", "))
		}
		if descend != nil {
			if err := descend(name, unwrap(node.Values[i])); err != nil {
				return err
			}
		}
	}
	return nil
}

// deriveID is the id of the entity a firing derives: a function of (rule, subject) and nothing
// else, which is what makes a re-fire idempotent and what retires "two triggers, one field" for
// everything derived — rule names are unique across the charter and the id is injective in the
// subject, so no two rules and no two subjects can address the same derived entity.
//
// It is spawnID's rule, deliberately: a derived entity and a spawned process are the same object
// in different clothes — a thing brought into being by one firing, about exactly one subject —
// and giving them two id schemes would be two answers to one question. They cannot collide: a
// process id addresses a process, a derived id addresses an entity at <system>:<kind>.
func deriveID(chain string, ref entity.Ref) string { return spawnID(chain, ref) }

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

// gateID makes the id of a trigger gate's question, deterministically from the trigger, the
// gate, and WHAT THE FIRING IS ABOUT — never from a counter.
//
// A counter made every firing a new question, which was invisible while a firing needed a
// fresh edge on the trigger's own subtree to happen at all. It stops being invisible the
// moment a trigger is re-evaluated rather than re-edged: the same decision, about the same
// subjects, lands on the docket again and again, and an approver answers a queue of identical
// questions. Derived from the subjects instead, a re-firing poses the id already standing
// there and JOINS it (process.Registry.Resolve) — one entry, one decision, delivered to every
// firing waiting on it.
//
// It HASHES the subject list rather than spelling it: the id crosses a URL path and a command
// line (see spawnID and safeSeg), and a broadcast's subject list is unbounded. Subjects arrive
// sorted (Match.Select), so the same firing always hashes the same.
func gateID(trigger, gate string, subjects []entity.Ref) string {
	h := fnv.New32a()
	for _, s := range subjects {
		_, _ = h.Write([]byte(s.String()))
		_, _ = h.Write([]byte{0}) // a separator no ref can hold, so [ab,c] and [a,bc] differ
	}
	return fmt.Sprintf("trigger:%s:%s%s%08x",
		safeSeg(trigger), safeSeg(gate), process.GateSep, h.Sum32())
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
func payloadBinding(payload entity.Payload) launch.Binding {
	b := launch.Binding{}
	// Fields of an object payload only: a scalar payload (42, true) has no named parts to
	// bind, so a template's $vars simply find nothing rather than binding the whole value
	// to some invented name.
	for k, v := range entity.ToObj(payload) {
		b[k] = fmt.Sprint(v)
	}
	return b
}

// bindingFor builds the template binding for an action: the fields of the payload its
// references read (see refSource), plus $subject — the RESOLVED subject it is about.
//
// $subject is set LAST, deliberately: it is the one reserved binding, and a payload field
// happening to be called "subject" must not shadow it. (It used to be set first and
// overwritten, which is why a goal:done fact carrying a `subject` field silently redirected
// $subject — see goal.go, which now also publishes `flow` for that job.)
func bindingFor(payload entity.Payload, subject entity.Ref) launch.Binding {
	b := payloadBinding(payload)
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

// desugarPatches rewrites the tag-keyed operator spelling into tags across an action and its
// completion hooks. Recursive because an Action is: a hook is a full action with its own patch.
func desugarPatches(a *Action) {
	if a == nil {
		return
	}
	if a.Patch != nil {
		a.Patch = desugar(a.Patch)
	}
	// A derive is a patch on the derived entity, so it carries the same operators and needs
	// the same desugaring — otherwise `{"tmp": {"delete": null}}` in a .json charter would
	// reach the store as a literal object where the .tony spelling wrote a deletion.
	if a.Derive != nil {
		a.Derive = desugar(a.Derive)
	}
	for _, h := range []*Action{a.OnDone, a.OnFailed, a.OnCancelled, a.OnOrphaned} {
		desugarPatches(h)
	}
}
