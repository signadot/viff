// Package launch is the wire form of an action: a declarative, serializable
// description of a task to launch when a trigger fires — the "action as data",
// not a Go closure. This is what lets an action take the deployment shape: a Spec
// can cross a network to a task runtime (taskd), be persisted, or be
// reflected as an entity (triggers-as-data). Nothing here depends on the task
// substrate; it is pure data + binding.
//
// Every field is a *reference* to something named elsewhere (a vector, a grant, an
// environment) rather than the thing inlined — the same index-not-warehouse
// discipline as the rest of the verse: the spec points at native semantics; the
// runtime that builds the task resolves the references against its own
// registries. It maps onto the task substrate's axes:
//
//	Driver      → which runtime/Driver builds the task (LLM harness, scripted, …)
//	Prompt        → the prompt/task the driver is seeded with
//	Vector      → the gate vector (the autonomy dial) — soft, slidable
//	Resolver    → how ASK gates are answered (human/auto/scm/…)
//	Grant       → hard authority — what it may do at all
//	Environment → the sandbox/workspace floor — unbypassable
//
// The authority axis is the GRANT, not the "scope": "scope" is taken everywhere else in
// verse for the entity-store isolation overlay (entity.Store.Scope, config.Scope, --scope,
// VERSE_SCOPE, connect's nonce), and this field is the one place it meant authority. The
// word the type (task.Grant) and the delegation check (Grant.Subset) already use is the
// right one.
package launch

//go:generate go tool tony-codegen -dir .

import (
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/task"
)

// Spec is the declarative launch descriptor. It is a *template*: string fields may
// contain $vars (e.g. "$subject") that Bind expands against a concrete event,
// turning a generic action into a specific launch. All fields are optional; a
// runtime supplies defaults for any left blank.
//
//tony:schemagen=verse-launch-spec,notag,comment=Doc,lineComment=Note
type Spec struct {
	// Doc and Note are the comments this was WRITTEN with — the lines above it, and the one
	// beside it. See Trigger.Doc: they ride the node rather than being fields of the document,
	// and they exist so a charter read back is the one its author wrote.
	Doc    []string `tony:"-"`
	Note   []string `tony:"-"`
	Driver string   `tony:"field=driver,omitzero"`
	Prompt string   `tony:"field=prompt,omitzero"`
	// Frame is the standing instructions the Prompt is read in — the agent's system prompt.
	//
	// It is INLINE, alongside Prompt, rather than a reference like the axes below, and the
	// cut is request-versus-governance rather than an exception to the discipline: Prompt
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
	// VirtualKey is the ADDRESS of the model credential this work runs under —
	// `acme.keys.review` — and the node standing there holds the key.
	//
	// AN ADDRESS RATHER THAN A NAME, because verse already has one namespace and it is the
	// tree. A name would need a registry beside it, per runtime, agreeing across deployments
	// and carrying no permissions of its own.
	//
	// THE READ IS THE AUTHORITY. Spending a credential has a cost, so who may spend it is a
	// permission — and the one verse already has answers it, with no group or list invented
	// for keys. versed resolves this as WHOEVER NAMED IT (versed.credentialAt) before the task
	// starts, so a charter naming an address its own owner may not read is refused rather than
	// run. Conventions for where keys live are the deployment's; verse imposes none.
	//
	// THE NODE HOLDS THE KEY, so rotating one is a write and every charter naming that address
	// follows untouched. The cost is that the store holds bearer strings, and a rule that may
	// read one may also derive it into an io's place and have it said out loud — so the perms
	// on these nodes are the whole of the protection, and one wants the fencing verse's own
	// half has. It is payable because a Portkey virtual key is bounded by its own budget and
	// route; a provider key is not, and does not belong here.
	//
	// Empty = the runtime's default credential, which is every spawn written before this.
	VirtualKey string `tony:"field=virtualKey,omitzero"`
	// Answer is this spawn's ANSWER CONTRACT: the shape its final message must have, as a
	// tony match pattern (a schema's `accept` clause, or a bare pattern, which is the same
	// thing without the wrapper). nil = no contract, and the run ends wherever the model
	// stops.
	//
	// It is inline alongside Frame and Prompt rather than a name to resolve, for the reason
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
	// when the model stops, fed back when it fails, and fails the task when it cannot be
	// met. That last part is what makes the deref downstream total: `patch: {class:
	// $data.class}` can no longer fail at patch time, because a task that could not
	// produce `class` did not succeed.
	Answer *ir.Node `tony:"field=answer,omitzero"`
	// Retain is how many events of this task's read plane are kept for a reader that
	// resumes — the window a `?since=` below which gets a resync marker instead of the
	// story. In events, 0 = the runtime's default.
	//
	// It is per-spawn because the right number is a property of the WORK and nothing else
	// knows it. A task that narrates every token and one that reports four boundaries an
	// hour want windows two orders of magnitude apart, and a package-level constant is
	// wrong for both. The charter that spawns it is the party that knows which it is — the
	// same argument Frame makes, applied to how much of the past is worth keeping.
	//
	// It bounds memory, so it is a cost the author is choosing: a long window on a chatty
	// task is paid for in bytes per live task, and the default is deliberately modest
	// rather than generous.
	Retain int `tony:"field=retain,omitzero"`
	// Calls is the work itself: a list of actions to take in order, for work with one right
	// answer. A merge is four git commands, and a charter that knows them should not have to
	// pay a model to rediscover them.
	//
	// It is the ONE field on this spec that is not a reference to something named elsewhere,
	// and the exception earns itself: there is nothing to name. An action list is not a
	// vocabulary a runtime holds — it is this spawn's own content, the way Prompt is, and
	// carrying it as a name would mean installing every list somewhere first.
	//
	// Its PRESENCE selects the driver (`task.DriverCalls`), so a spawn writing calls need not
	// also write `driver:` — and one that writes a contradicting driver is refused rather than
	// silently having one of the two ignored.
	//
	// It supersedes the tony document that used to ride inside Prompt. A field can be checked
	// when the trigger fires, shown by `verse charter`, and expanded per value — so a `$ref`
	// with a quote in it is an argument rather than something that has to survive being
	// spliced into text and parsed back out.
	Calls  []Call `tony:"field=calls,omitzero"`
	Vector string `tony:"field=vector,omitzero"`
	// Gates are the task's own gates, over the default `vector:` names — one rule per action
	// shape, each with the question it poses (task.Gate: a threshold, options, a deadline).
	// The trigger's `gates:` are the FIRING's and run once before the action; these run at
	// every boundary of the task it starts. Written out rather than named because a named
	// vector is deliberately a bare default (task.NamedVector), and a threshold or a clock on
	// a name would want a name per combination.
	//
	// Not expanded: a gate is a predicate over an action, and a $var in one could only name a
	// tool or a path chosen by whoever last wrote the entity being matched — the widening-by-
	// binding risk Tools is not expanded for either.
	Gates       []task.Gate `tony:"field=gates,omitzero"`
	Resolver    string      `tony:"field=resolver,omitzero"`
	Grant       string      `tony:"field=grant,omitzero"`
	Environment string      `tony:"field=environment,omitzero"`
	// Runner is WHERE this launch runs: `verse.runners.<name>`, an entity installed with
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
	// engine tracks a flow's tasks by subject so it can be steered/cancelled as
	// a unit. Usually the concrete matched ref, not a template.
	Subject string `tony:"field=subject,omitzero"`
	// Chain is the kpath of the ACTION that spawned this, inside its trigger: "review" for
	// the trigger's own spawn, "review.onDone" for its completion hook. Engine-owned like
	// Subject — a charter that writes it is overwritten — and carried here only because the
	// Spawner is what calls mirror.Reflect, which owns the task entity's payload. It is
	// what lets a task say where in its trigger's action tree it came from.
	Chain string `tony:"field=chain,omitzero"`
	// Doer is WHO the selection picked: the winning provider's entity ref
	// ("provider:treat:fixer"), or the bare name for an inline `eligible` list, which names no
	// entity. Engine-owned like Subject and Chain — a charter that writes it is overwritten,
	// because it is an answer and not a request.
	//
	// It is separate from Driver because the driver stopped identifying the doer. Two doers
	// that differ in frame, tools and grant while sharing `driver: llm` is the case a
	// Fragment exists to serve, so a task entity showing only the driver would report two
	// quite different spawns as the same work by the same agent.
	Doer string `tony:"field=doer,omitzero"`
	// Occurrence is the store revision this run is ABOUT: the commit that woke the firing.
	// Engine-owned like Subject, Chain and Doer — a charter that writes it is overwritten.
	//
	// A task id names the WORK, deliberately: it is derived from the chain and the subject so
	// that a re-fire is idempotent, a completion hook is scoped to the run that raised it, and
	// `verse flow demo:pr:1` has something to address. Nothing named the RUN, and one record
	// per id meant a second run overwrote the first — a staging record read `ended` fifty
	// minutes BEFORE its `started`, carrying the result of one run beside the status of
	// another (git-issue gjv063rrh12ksvgnfsn0).
	//
	// A revision rather than a sequence number or a timestamp: an operator asking "which run"
	// is holding a commit, not a count, and two runs a millisecond apart on one subject is
	// exactly the case this exists for. Zero when nothing caused it — an ad-hoc `verse task
	// run` stamps the store's own revision at spawn instead, since that is the same question
	// asked where there is no firing.
	//
	// It rides to the RECORD and stops there. A derive that referenced its cause would
	// reintroduce the cycle the strata exist to rule out (see deriveMayReference), so this is
	// not a value a charter can read — it is what a reader of the record uses to tell one run
	// from the next.
	Occurrence int64 `tony:"field=occurrence,omitzero"`
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

// Call is one action of a call list, as a charter writes it.
//
// It is the wire twin of task.Call, and the duplication is the one this package already lives
// by: a spec is a template full of $vars, and the runtime form is the resolved value. The
// difference here is just the fatal vocabulary — strings on this side, checked and typed when
// the engine builds the launch, so a charter that wrote `fatal: [sideways]` fails its FIRING
// rather than producing a task whose stop condition silently never matches.
//
//tony:schemagen=verse-launch-call,notag
type Call struct {
	Tool  string         `tony:"field=tool"`
	Input map[string]any `tony:"field=input,omitzero"`
	// Fatal names which outcomes of this call end the list: `err`, `blocked`, `nonzero`.
	// ABSENT means all of them — a list is ordered because its calls depend on each other —
	// and `fatal: []` means this call ends nothing, which is what a sweep says. See
	// task.Call.Fatal for why it is per call, and why the three states need a pointer.
	Fatal *[]string `tony:"field=fatal,omitzero"`
}

// Provider declares the who-axis as a governed 1-of-N selection: the eligible doers (a
// hard set, like Grant) and the surface that selects among them (the soft who-dial, like
// Vector). Absent = today's fixed Driver.
//
// Eligibility takes one of two forms: an inline `Eligible` list of doer ids (simple), or
// the registry form `Capability` (+ optional `Where`) which selects the `provider.<cap>.*`
// entities whose attributes match — attribute-rich, discoverable doers resolved at fire
// time. A matched provider entity carries the `driver` bound into the spawn.
//
//tony:schemagen=verse-launch-provider,notag
type Provider struct {
	Eligible   []string       `tony:"field=eligible,omitzero"`   // inline doer ids (option == driver)
	Capability string         `tony:"field=capability,omitzero"` // registry: select provider.<capability>.*
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
		Prompt:      expand(s.Prompt, b),
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
	// which tools an agent is handed would widen its authority by binding.
	// Copied rather than shared so a bound spec cannot alias the charter's slice.
	out.Tools = append([]string(nil), s.Tools...)
	// VirtualKey is carried and NOT expanded, for Tools' reason and more sharply. It names a
	// credential the runtime holds, drawn from a fixed set, so a $var in one could only name a
	// key that does not exist — or one chosen by whoever last wrote the entity being matched,
	// which would let payload text decide whose budget this firing spends.
	out.VirtualKey = s.VirtualKey
	out.Gates = append([]task.Gate(nil), s.Gates...)
	out.Calls = bindCalls(s.Calls, b)
	return out
}

// bindCalls expands the ARGUMENTS of each call, and nothing else.
//
// This is the whole practical difference between a list in a field and a list inside the
// prompt. A prompt is expanded as one blob, so `$ref` reaching `git merge --ff-only $ref` has to
// survive being substituted into text that is then parsed back as a document — and a branch name
// containing a quote breaks the parse rather than the command. Here each value is expanded as a
// value: it becomes an argument, whatever is in it.
//
// The tool name is NOT expanded, on exactly the argument Tools makes above: it names something
// from a fixed vocabulary, so a $var in one could only name a tool that does not exist or one
// chosen by whoever last wrote the entity being matched. Nor is `fatal`, which is vocabulary too.
func bindCalls(cs []Call, b Binding) []Call {
	if len(cs) == 0 {
		return nil
	}
	out := make([]Call, 0, len(cs))
	for _, c := range cs {
		// The pointer is COPIED rather than shared: Bind is pure, and a bound spec aliasing
		// the charter's own slice would let one firing's edit reach every later one.
		nc := Call{Tool: c.Tool}
		if c.Fatal != nil {
			f := append([]string(nil), *c.Fatal...)
			if f == nil {
				f = []string{}
			}
			nc.Fatal = &f
		}
		if c.Input != nil {
			nc.Input = make(map[string]any, len(c.Input))
			for k, v := range c.Input {
				nc.Input[k] = expandAny(v, b)
			}
		}
		out = append(out, nc)
	}
	return out
}

// expandAny expands $vars in every string reachable from v, so a nested argument is bound like a
// top-level one. A charter writing `input: {env: {REF: $ref}}` means the same thing by it either
// way, and a rule that stopped at the first level would be a rule nobody could remember.
func expandAny(v any, b Binding) any {
	switch t := v.(type) {
	case string:
		return expand(t, b)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = expandAny(e, b)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = expandAny(e, b)
		}
		return out
	default:
		return v
	}
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
