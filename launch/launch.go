// Package launch is the wire form of an action: a declarative, serializable
// description of a process to launch when a trigger fires — the "action as data",
// not a Go closure. This is what lets an action take the deployment shape: a Spec
// can cross a network to a process runtime (processd), be persisted, or be
// reflected as an entity (triggers-as-data). Nothing here depends on the process
// substrate; it is pure data + binding.
//
// Every field is a *reference* to something named elsewhere (a vector, a grant, an
// environment) rather than the thing inlined — the same index-not-warehouse
// discipline as the rest of the verse: the spec points at native semantics; the
// runtime that builds the process resolves the references against its own
// registries. It maps onto the process substrate's axes:
//
//	Driver      → which runtime/Driver builds the process (LLM harness, scripted, …)
//	Task        → the prompt/task the driver is seeded with
//	Vector      → the gate vector (the autonomy dial) — soft, slidable
//	Resolver    → how ASK gates are answered (human/auto/scm/…)
//	Grant       → hard authority — what it may do at all
//	Environment → the sandbox/workspace floor — unbypassable
//
// The authority axis is the GRANT, not the "scope": "scope" is taken everywhere else in
// verse for the entity-store isolation overlay (entity.Store.Scope, config.Scope, --scope,
// VERSE_SCOPE, connect's nonce), and this field is the one place it meant authority. The
// word the type (process.Grant) and the delegation check (Grant.Subset) already use is the
// right one. See docs/roadmap/expose.md.
package launch

//go:generate go tool tony-codegen -dir .

import (
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
)

// Spec is the declarative launch descriptor. It is a *template*: string fields may
// contain $vars (e.g. "$subject") that Bind expands against a concrete event,
// turning a generic action into a specific launch. All fields are optional; a
// runtime supplies defaults for any left blank.
//
//tony:schemagen=verse-launch-spec,notag
type Spec struct {
	Driver string `tony:"field=driver,omitzero"`
	Task   string `tony:"field=task,omitzero"`
	// Frame is the standing instructions the Task is read in — the agent's system prompt.
	//
	// It is INLINE, alongside Task, rather than a reference like the axes below, and the
	// cut is request-versus-governance rather than an exception to the discipline: Task
	// and Frame are both text the authoring party wrote for this spawn, while Vector,
	// Grant, Resolver and Environment name semantics the runtime holds. A frame is not a
	// permission — a model may ignore every word of it — so making it a reference would
	// index the one field with nothing to look up.
	//
	// It is per-spawn because the two things a package-level default gets wrong are both
	// per-spawn facts: whether the agent's last message is prose for a person or the value
	// a `patch: {severity: $result.severity}` will lift out of it, and which of the tools
	// on offer are worth reaching for under this spawn's vector. A charter that derefs
	// $result has to be able to say what shape it expects, and only the charter knows.
	//
	// A driver definition carrying a default frame per driver name is the natural next
	// step and would not replace this: the complaint that produced this field is one
	// driver needing different frames on different spawns.
	Frame string `tony:"field=frame,omitzero"`
	// Tools narrows the driver's tool set to those named; empty = all of them. Unlike
	// Frame it is not advice — a tool that is not offered cannot be called — but it is
	// still not authority: Grant is what refuses an action. A spawn usually wants both,
	// because a tool withheld never comes up, while a tool offered-but-ungranted costs a
	// turn being refused.
	Tools []string `tony:"field=tools,omitzero"`
	// Answer is this spawn's ANSWER CONTRACT: the shape its final message must have, as a
	// tony match pattern (a schema's `accept` clause, or a bare pattern, which is the same
	// thing without the wrapper). nil = no contract, and the run ends wherever the model
	// stops.
	//
	// It is inline alongside Frame and Task rather than a name to resolve, for the reason
	// Frame gives: it is written by the authoring party for this spawn, and only the charter
	// that derefs `$data.severity` knows what shape it needs.
	//
	// It is a DOCUMENT rather than the text of one, which it could not be until a rule could
	// be stored as structure: a contract is dense with operators (`!or`, `!irtype`), a write
	// is a tony patch, and a payload full of operators used to be executed by the store
	// rather than stored by it. `!raw` (entity.Raw) is the escape that settled that, and the
	// same escape carries this — so a contract is the same kind of thing as the `when` it
	// sits beside, readable field by field on the rule's own entity, rather than a string
	// something has to re-parse to ask a question about.
	//
	// Unlike Frame it is not advice. A frame can ask for a shape and be ignored — that is
	// exactly what was observed, three firings out of three — while a contract is checked
	// when the model stops, fed back when it fails, and fails the process when it cannot be
	// met. That last part is what makes the deref downstream total: `patch: {class:
	// $data.class}` can no longer fail at patch time, because a process that could not
	// produce `class` did not succeed.
	Answer *ir.Node `tony:"field=answer,omitzero"`
	// Retain is how many events of this process's read plane are kept for a reader that
	// resumes — the window a `?since=` below which gets a resync marker instead of the
	// story. In events, 0 = the runtime's default.
	//
	// It is per-spawn because the right number is a property of the WORK and nothing else
	// knows it. A process that narrates every token and one that reports four boundaries an
	// hour want windows two orders of magnitude apart, and a package-level constant is
	// wrong for both. The charter that spawns it is the party that knows which it is — the
	// same argument Frame makes, applied to how much of the past is worth keeping.
	//
	// It bounds memory, so it is a cost the author is choosing: a long window on a chatty
	// process is paid for in bytes per live process, and the default is deliberately modest
	// rather than generous.
	Retain      int    `tony:"field=retain,omitzero"`
	Vector      string `tony:"field=vector,omitzero"`
	Resolver    string `tony:"field=resolver,omitzero"`
	Grant       string `tony:"field=grant,omitzero"`
	Environment string `tony:"field=environment,omitzero"`
	// Runner is WHERE this launch runs: `runners:<kind>:<name>`, an entity installed with
	// `verse runner add`. Empty means the verse's own default runner.
	//
	// A name rather than a url, and that indirection is the point: a url on the spawn would
	// spread the deployment through the charters, so every charter wanting the gpu pool would
	// carry its address and moving the pool would mean editing all of them. It is the same
	// indirection `provider:` and `resolver:` already are.
	//
	// A launch naming a runner that is not installed is REFUSED rather than run somewhere
	// else — see versed.runnerFor.
	Runner string `tony:"field=runner,omitzero"`
	// Subject is the flow this launch belongs to (an entity ref, e.g. a PR). The
	// engine tracks a flow's processes by subject so it can be steered/cancelled as
	// a unit. Usually the concrete matched ref, not a template.
	Subject string `tony:"field=subject,omitzero"`
	// Chain is the kpath of the ACTION that spawned this, inside its trigger: "review" for
	// the trigger's own spawn, "review.onDone" for its completion hook. Engine-owned like
	// Subject — a charter that writes it is overwritten — and carried here only because the
	// Spawner is what calls mirror.Reflect, which owns the process entity's payload. It is
	// what lets a process say where in its trigger's action tree it came from.
	Chain string `tony:"field=chain,omitzero"`
	// Doer is WHO the selection picked: the winning provider's entity ref
	// ("provider:treat:fixer"), or the bare name for an inline `eligible` list, which names no
	// entity. Engine-owned like Subject and Chain — a charter that writes it is overwritten,
	// because it is an answer and not a request.
	//
	// It is separate from Driver because the driver stopped identifying the doer. Two doers
	// that differ in frame, tools and grant while sharing `driver: llm` is the case a
	// Fragment exists to serve, so a process entity showing only the driver would report two
	// quite different spawns as the same work by the same agent.
	Doer string `tony:"field=doer,omitzero"`
	// Provider, when set, makes the who-axis a GOVERNED SELECTION rather than a fixed
	// authoring-time value: the engine selects among the eligible doers via the Select
	// surface, reifies it as a motion, and overlays the winner's Fragment — driver, frame,
	// tools, grant — onto this spec before spawn (no live swap — reassign = cancel+respawn).
	// Orthogonal to the RUNNER that then transports the chosen Driver.
	//
	// A spawn that carries one must also declare Grant: a doer may bring its own, and that is
	// only checkable against a ceiling the charter stated. See docs/provider-selection.md.
	Provider *Provider `tony:"field=provider,omitzero"`
}

// Provider declares the who-axis as a governed 1-of-N selection: the eligible doers (a
// hard set, like Grant) and the surface that selects among them (the soft who-dial, like
// Vector). Absent = today's fixed Driver.
//
// Eligibility takes one of two forms: an inline `Eligible` list of doer ids (simple), or
// the registry form `Capability` (+ optional `Where`) which selects the `provider:<cap>:*`
// entities whose attributes match — attribute-rich, discoverable doers resolved at fire
// time. A matched provider entity carries the `driver` bound into the spawn.
//
//tony:schemagen=verse-launch-provider,notag
type Provider struct {
	Eligible   []string       `tony:"field=eligible,omitzero"`   // inline doer ids (option == driver)
	Capability string         `tony:"field=capability,omitzero"` // registry: select provider:<capability>:*
	Match      map[string]any `tony:"field=match,omitzero"`      // registry: a tony match over provider attributes
	// Select is the who-dial: how the doer is picked among the eligible options —
	// "auto" (a rule picks the first eligible), "ask" (one party picks; a threshold of 1),
	// or "vote" (the group picks; ≥ Need). Empty defaults to auto. It is the who-axis
	// counterpart of a whether-gate's Formula; both compile to the same governed motion.
	Select string `tony:"field=select,omitzero"`
	Need   int    `tony:"field=need,omitzero"`   // vote: plurality threshold
	Within string `tony:"field=within,omitzero"` // vote/ask: selection deadline (e.g. "4h") — no pick by then = no spawn
}

// bind expands $vars in the template-ish fields (eligible ids, capability). Select/Need
// and the Match pattern are carried as-is (the engine evaluates Match as a tony match
// over provider entities via tony.Match; $var expansion inside a match is future work).
func (p *Provider) bind(b Binding) *Provider {
	if p == nil {
		return nil
	}
	np := &Provider{Select: p.Select, Need: p.Need, Within: p.Within, Capability: expand(p.Capability, b), Match: p.Match}
	for _, e := range p.Eligible {
		np.Eligible = append(np.Eligible, expand(e, b))
	}
	return np
}

// Binding maps template variable names to concrete values — built when a trigger
// fires ("subject" → the matched entity's ref). It is the generic→specific bind.
type Binding map[string]string

// Bind expands $vars in every string field of the spec against b, returning the
// instantiated (specific) spec. Unbound $vars are left as-is.
func (s Spec) Bind(b Binding) Spec {
	out := Spec{
		Driver:      expand(s.Driver, b),
		Task:        expand(s.Task, b),
		Frame:       expand(s.Frame, b),
		Vector:      expand(s.Vector, b),
		Resolver:    expand(s.Resolver, b),
		Runner:      expand(s.Runner, b),
		Grant:       expand(s.Grant, b),
		Environment: expand(s.Environment, b),
		Subject:     expand(s.Subject, b),
		Provider:    s.Provider.bind(b),
	}
	// Retain is a number, so there is nothing to expand — but it is carried, and forgetting
	// to carry it here is exactly the failure this construction invites: Bind is a
	// field-by-field copy, so a field added to Spec and not added here is silently dropped
	// on every charter spawn while every direct construction of a Spec keeps working.
	out.Retain = s.Retain
	// Answer is not expanded either, and for a plainer reason than Tools: it is a pattern,
	// not text, and $var expansion inside a match is the same future work Provider.Match is
	// waiting on. It is carried across as it was written.
	out.Answer = s.Answer
	// Tools are NOT expanded. Every other string here is a template because a charter has
	// reason to build it out of the firing — but a tool name is drawn from a fixed set the
	// driver defines, so a $var in one could only ever name a tool that does not exist, or
	// name one chosen by whoever last wrote the entity being matched. The second is the
	// interesting case: this list is adjacent to authority, and letting payload text pick
	// which tools an agent is handed is the widening-by-binding risk authority.md names.
	// Copied rather than shared so a bound spec cannot alias the charter's slice.
	out.Tools = append([]string(nil), s.Tools...)
	return out
}

// Expand substitutes $vars in s from b — exported for callers building strings
// (e.g. a target ref) from a binding, outside a Spec.
func Expand(s string, b Binding) string { return expand(s, b) }

// expand replaces each "$name" with b[name]. Longer names are substituted first so
// "$subject" is not shadowed by a shorter "$sub" binding. This is a v0 stand-in for
// the richer reference/derivation grammar ("$subject.pr" via a deref) noted for
// later — deliberately trivial, since the "when"/binding is a complexity sink.
func expand(s string, b Binding) string {
	if s == "" || !strings.Contains(s, "$") {
		return s
	}
	keys := make([]string, 0, len(b))
	for k := range b {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		s = strings.ReplaceAll(s, "$"+k, b[k])
	}
	return s
}
