// Package launch is the wire form of an action: a declarative, serializable description of a
// task to launch when a trigger fires — the "action as data", not a Go closure. This is what
// lets an action take the deployment shape: a Spec can cross a network to a runner, be
// persisted, or be reflected as an entity (triggers-as-data). Nothing here depends on the task
// substrate; it is pure data + binding.
//
// Every field is a *reference* to something named elsewhere, or text the authoring party wrote
// for this spawn — the same index-not-warehouse discipline as the rest of the verse: the spec
// points at native semantics; the runner that builds the task resolves the references against
// its own offer. It maps onto the spawn spec's axes:
//
//	Prompt      → the job, for an agent; or
//	Script      → a script, which is its own doer
//	Agent       → which agent a prompt is for, and how it works (absent: verse's default agent)
//	Environment → what the run can reach, fixed when it starts (systems.Environment)
//	Autonomy    → what happens to each step within it: denied, asked about (approvals,
//	              deadline, answerers, labels), or allowed; the last row is the dial
//
// Nothing says where it runs: verse finds a runner that offers the doer and holds the
// environment (versed's routing).
//
// What a run can reach is its environment, never the autonomy: a turn of the dial decides
// within it and never widens it, and the runner refuses what the environment did not give.
package launch

//go:generate go tool tony-codegen -dir .

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	tonyeval "github.com/signadot/tony-format/go-tony/eval"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/exprs"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// AsSubject is the one value `as` takes: the owner of the record the firing is about.
const AsSubject = "subject"

// Spec is the declarative launch descriptor. It is a *template*: string fields may contain
// expressions (e.g. "$[subject.ref]") that Bind expands against a concrete event, turning a
// generic action into a specific launch. All fields are optional; a runner supplies defaults for any left blank.
//
//tony:schemagen=verse-launch-spec,notag,comment=Doc,lineComment=Note
type Spec struct {
	// Doc and Note are the comments this was WRITTEN with — the lines above it, and the one
	// beside it. See Trigger.Doc: they ride the node rather than being fields of the document,
	// and they exist so a charter read back is the one its author wrote.
	Doc  []string `tony:"-"`
	Note []string `tony:"-"`
	// Prompt is the job, written for an agent. Exactly one of Prompt and Script.
	Prompt string `tony:"field=prompt,omitzero"`
	// Script is a script's text, its own doer; data reaches it as the environment's vars, and
	// it is not expanded (bindSpec).
	Script string `tony:"field=script,omitzero"`
	// Agent is the agent a prompt is for, and how it works; absent is verse's default agent
	// (`defaultAgent`), bound at the spawn.
	// Its name and its settings take expressions.
	Agent *runner.Agent `tony:"field=agent,omitzero"`
	// Person is reserved for a person doing the work; no runner takes one yet.
	Person string `tony:"field=person,omitzero"`
	// Environment is what the run can reach and the variables its shell is given
	// (systems.Environment). A block's attributes and the vars take expressions, so a charter
	// can check out `$[subject.value.repo]`; a shape does not, and neither does the network: a
	// pattern says how far a run may reach, and payload text must not widen it.
	Environment systems.Environment `tony:"field=environment,omitzero"`
	// Autonomy is what happens to each step of the task (task.Autonomy): the first row that
	// matches it denies it, asks about it (how many approvals, who answers, how long the
	// question stands, what silence settles on, what it says about itself) or allows it, and
	// the last row, which names no step, decides the rest. The trigger's `gates:` are the
	// FIRING's and run once before the action; the autonomy decides every step of the task it
	// starts. Absent asks before every step.
	//
	// Not expanded: a row's match names a step and its fields, and an expression in one could
	// only name a repository or a ref chosen by whoever last wrote the entity being matched.
	Autonomy task.Autonomy `tony:"field=autonomy,omitzero"`
	// Timeout bounds the run: a duration ("1h"). Verse enforces it by cancelling, so it holds
	// on every runner. Empty is no bound.
	Timeout string `tony:"field=timeout,omitzero"`
	// VirtualKey is the ADDRESS of the model credential this work runs under —
	// `acme.keys.review` — and the node standing there holds the key's name.
	//
	// AN ADDRESS RATHER THAN A NAME, because verse already has one namespace and it is the
	// tree. THE READ IS THE AUTHORITY: versed resolves this as WHOEVER NAMED IT
	// (versed.credentialAt) before the task starts, so a charter naming an address its own
	// owner may not read is refused rather than run; what reaches the runner is the key's
	// name, as the doer's `verse.portkey/virtual-key` annotation. THE NODE HOLDS THE NAME, so
	// re-pointing a budget is a write and every charter naming that address follows.
	//
	// Empty = the runner's default credential.
	VirtualKey string `tony:"field=virtualKey,omitzero"`
	// Answer is this spawn's ANSWER CONTRACT: the shape its final message must have, as a
	// tony match pattern (a schema's `accept` clause, or a bare pattern, which is the same
	// thing without the wrapper). nil = no contract, and the run ends wherever the doer stops.
	//
	// It is inline beside Prompt rather than a name to resolve: it is written by the authoring
	// party for this spawn, and only the charter that reads `data.severity` knows what shape
	// it needs. It is a DOCUMENT rather than the text of one: a contract is dense with
	// operators (`!or`, `!irtype`), and `!raw` (entity.Raw) carries it as structure, readable
	// field by field on the rule's own entity. It is not advice: it is checked when the doer
	// stops, fed back when it fails, and fails the task when it cannot be met, which is what
	// makes the deref downstream total.
	Answer *ir.Node `tony:"field=answer,omitzero"`
	// Retain is how many events of this task's read plane are kept for a reader that resumes —
	// the window a `?since=` below which gets a resync marker instead of the story. 0 = the
	// runtime's default. Per-spawn because the right number is a property of the WORK: a task
	// that narrates every token and one that reports four boundaries an hour want windows two
	// orders of magnitude apart. It bounds memory, so it is a cost the author is choosing.
	Retain int `tony:"field=retain,omitzero"`
	// Subject is the flow this launch belongs to (an entity ref, e.g. a PR). The engine tracks
	// a flow's tasks by subject so it can be steered/cancelled as a unit. Usually the concrete
	// matched ref, not a template.
	Subject string `tony:"field=subject,omitzero"`
	// Chain is the kpath of the ACTION that spawned this, inside its trigger: "review" for the
	// trigger's own spawn, "review.onDone" for its completion hook. Engine-owned like Subject —
	// a charter that writes it is overwritten — and carried here only because the Spawner is
	// what calls mirror.Reflect, which owns the task entity's payload. It is what lets a task
	// say where in its trigger's action tree it came from.
	Chain string `tony:"field=chain,omitzero"`
	// Occurrence is the store revision this run is ABOUT: the commit that woke the firing.
	// Engine-owned like Subject and Chain — a charter that writes it is overwritten.
	//
	// A task id names the WORK, deliberately: it is derived from the chain and the subject so
	// that a re-fire is idempotent, a completion hook is scoped to the run that raised it, and
	// `verse flow demo:pr:1` has something to address. Nothing named the RUN, and one record
	// per id would let a second run overwrite the first (git-issue gjv063rrh12ksvgnfsn0). A
	// revision rather than a sequence number or a timestamp: an operator asking "which run" is
	// holding a commit, not a count. Zero when nothing caused it — an ad-hoc `verse task run`
	// stamps the store's own revision at spawn instead.
	//
	// It rides to the RECORD and stops there: not a value a charter can read (see
	// deriveMayReference), but what a reader of the record uses to tell one run from the next.
	Occurrence int64 `tony:"field=occurrence,omitzero"`
	// Refusal is why the ENGINE refused this launch, "" for a launch it did not refuse: a
	// reference in the spawn's strings that reaches past what the firing bound. Engine-owned,
	// and no field of a spec written by hand.
	//
	// The Spawner is still called, so the refusal is recorded where every other refusal is: a
	// Spawner given one STARTS NOTHING, writes the task's record as failed with this reason, as
	// it does for a launch it refuses itself, and answers a failed handle (task.FailedHandle).
	Refusal string `tony:"-"`
	// As says whose authority the task runs with beside the rule's: `subject`, the owner of the
	// record the firing is about — whoever wrote the ask a rule answers. Empty, the task runs as
	// the rule alone.
	//
	// ON THE ASKER'S AUTHORITY. The task acts as a pair (entity.Pair) whose authority is the
	// subject's owner's alone: a consequence is its raiser's. A rule's author cannot use that to
	// act as anybody whose records the rule selects, because a rule saying `as: subject` may
	// select only records in a place its installer owns (checked at install): an asker's record
	// there was written into a place the charter was installed to serve.
	As string `tony:"field=as,omitzero"`
	// ActsFor is who `as: subject` resolved to at the firing: the subject record's owner.
	// Engine-owned like Subject and Chain, and more strictly: the engine CLEARS it on every
	// launch and sets it only from `as`, because a charter that could write it would name whoever
	// it liked to act as.
	ActsFor string `tony:"field=actsFor,omitzero"`
	// Credential asks verse to vouch for this task to a system outside it: a short-lived token,
	// signed by this verse as an issuer, that says which rule's gate let the work through and how:
	// on the ballots people cast, on its deadline, or as a gate written `auto`. Absent, the task
	// gets none.
	//
	// DECLARED, AND NEVER DERIVED. What a rule's work may reach is written in the charter, where
	// it is read and reviewed with the rule; it is not expanded, so text in the subject cannot
	// choose the audience. It is honoured for the spawn the gate admitted, a rule's own, and not
	// for a completion hook's or a sequence step's, which start later than the answer was given.
	Credential *Credential `tony:"field=credential,omitzero"`
	// Passed is the gates the firing passed before this launch: which questions, and how each
	// settled. Engine-owned like ActsFor, and as strictly: the engine clears it on every launch and
	// sets it only from the firing's own gates, because it is what a credential vouches for.
	Passed []Passed `tony:"field=passed,omitzero"`
}

// Credential is what a spawn asks verse to vouch for its task with.
//
//tony:schemagen=verse-launch-credential,notag
type Credential struct {
	// Audience is the system the token is for — `github`, `jira` — as the token's `aud`. A
	// system that checks a token checks that it was minted for it and not another.
	Audience string `tony:"field=audience"`
}

// How a firing's gate settled (Passed.How).
const (
	// PassedBallots: people cast the ballots the gate needed.
	PassedBallots = "ballots"
	// PassedDeadline: nobody answered in time, and the gate's default stood.
	PassedDeadline = "deadline"
	// PassedAuto: the gate was written `auto` and asked nobody.
	PassedAuto = "auto"
)

// Passed is one gate the firing passed, as a credential states it.
//
//tony:schemagen=verse-launch-passed,notag
type Passed struct {
	// Gate is the gate's name in its rule; Question the id of the question it posed, when it
	// posed one.
	Gate     string `tony:"field=gate,omitzero"`
	Question string `tony:"field=question,omitzero"`
	// How it settled: PassedBallots, PassedDeadline or PassedAuto.
	How string `tony:"field=how"`
	// Choice is the option that won, when the gate offered more than proceed.
	Choice string `tony:"field=choice,omitzero"`
	// By is who settled it, for ballots: the principals that voted, joined. Via is the surface
	// the ballots came in on (slack, cli), and Authenticated whether that surface vouched for
	// them.
	By            string `tony:"field=by,omitzero"`
	Via           string `tony:"field=via,omitzero"`
	Authenticated string `tony:"field=authenticated,omitzero"`
}

// Binding is what a spawn's expressions are evaluated against: the names a firing binds, each to
// the value a tony expression reads (`subject`, as {ref, name, value}, and the fields of the
// payload the firing read), as eval.ToAny gives them.
type Binding map[string]any

// exprOptions is how a firing's expressions are evaluated: a spawn's strings, a gate's pose, and
// the values of a patch and a derive (ExpandValue).
//
// NO CLOCK: a firing's launch is retried, and parked and released, and has to say the same thing
// each time, so the builtins that read the time are not callable. NO DOCUMENT READS: go-tony's
// script functions read the document an expression stands in, or the process's environment, and a
// charter reaches other data through `!get-path` and `!list-paths`, whose reads a rule tracks.
// They are callable only where an expansion is given a position (ExpandValue's is the value's
// own), and are refused by name wherever they are. AN OBJECT OR A LIST IS TONY in a string, the
// format the rest of verse writes, so a prompt carrying a ledger carries something a person and a
// parser can both read.
var exprOptions = tonyeval.EvalOptions{
	DisableBuiltins: []string{"now", "date", "getpath", "listpath", "getenv", "whereami"},
	Composite:       tonyeval.CompositeTony,
}

// Expand is s with its expressions expanded against b, as Bind expands a spawn's strings, under
// the same options: what else a firing says in words, a gate's pose, reads as its spawn does.
func Expand(s string, b Binding) (string, error) {
	return tonyeval.ExpandStringWithOptions(s, b, &exprOptions)
}

// ExpandValue is the value s stands for in a document a firing writes, a patch's or a derive's:
// where s is one `.[…]` and nothing else, the expression's value, typed (a number stays a number,
// an object an object); otherwise s with its `$[…]` expanded, a string. It carries no tag: the
// tag belongs to the place s stood in, which the caller keeps.
func ExpandValue(s string, b Binding) (*ir.Node, error) {
	return tonyeval.ExpandIRWithOptions(ir.FromString(s), b, &exprOptions)
}

// binder expands a spec's strings against one binding, and keeps what would not expand.
type binder struct {
	b       Binding
	refused Refusals
}

// Refusals is what Bind would not expand, one Refusal per field.
type Refusals []Refusal

// Refusal is a field Bind would not expand, by the name CheckByHand gives it, and why.
type Refusal struct {
	Field string
	Err   error
}

// Error is each refusal as `field: why`, in field order.
func (r Refusals) Error() string {
	out := make([]string, len(r))
	for i, f := range r {
		out[i] = f.Field + ": " + f.Err.Error()
	}
	return strings.Join(out, "; ")
}

// text is s expanded, or s as written where it will not expand, which is kept as field's
// refusal: an absent name, field or index on a plain path (`$[data.summary]` where data has no
// summary), an expression that does not compile, or one that calls the clock.
//
// A `$` that does not open `$[` is not an expression, so `$HOME` and `$5` stay as written
// whatever the binding holds.
func (x *binder) text(field, s string) string {
	if !strings.Contains(s, "[") {
		return s
	}
	out, err := Expand(s, x.b)
	if err != nil {
		x.refused = append(x.refused, Refusal{Field: field, Err: err})
		return s
	}
	return out
}

// Bind expands the template against a binding and returns the concrete spec, and what would not
// expand, by field, as Refusals. Pure: the receiver is not changed, and nothing in the result
// aliases it. Engine-owned fields (Chain, Occurrence, ActsFor, Passed) are not carried: the
// engine sets them.
//
// A spec with a refusal is still whole, the refused strings as written: a caller that must not
// launch on one (a firing) records it as a failed task, and that record is the spec.
func (s Spec) Bind(b Binding) (Spec, error) {
	x := &binder{b: b}
	out := Spec{
		Prompt:  x.text("prompt", s.Prompt),
		Subject: x.text("subject", s.Subject),
		Timeout: s.Timeout,
		Script:  s.Script,
		Person:  s.Person,
	}
	out.Agent = x.agent(s.Agent)
	out.Environment = x.environment(s.Environment)
	out.Retain = s.Retain
	// The contract is a pattern a reader of the result applies, not text about the thing the
	// firing is about: it is carried across as it was written.
	out.Answer = s.Answer
	// VirtualKey is carried and NOT expanded: it names a credential drawn from a fixed set, so a
	// expression in one could only name a key that does not exist — or one chosen by whoever last
	// wrote the entity being matched, which would let payload text decide whose budget this
	// firing spends.
	out.VirtualKey = s.VirtualKey
	// The autonomy is copied and not expanded: a row's match is a pattern over what the task
	// does, and its answerers are names the question is held out to.
	out.Autonomy = s.Autonomy.Clone()
	// As is carried and not expanded: it takes one word (AsSubject), and payload text deciding
	// whose authority a task runs with is the thing it exists to prevent. ActsFor is NOT carried —
	// it is the engine's answer to As, set after binding (trigger.launch).
	out.As = s.As
	// Credential is carried and not expanded, for VirtualKey's reason: payload text deciding
	// which system a token is good for is what declaring it exists to prevent. Passed is NOT
	// carried, as ActsFor is not: it is the engine's account of the firing's gates.
	if s.Credential != nil {
		c := *s.Credential
		out.Credential = &c
	}
	if len(x.refused) > 0 {
		sort.Slice(x.refused, func(i, j int) bool { return x.refused[i].Field < x.refused[j].Field })
		return out, x.refused
	}
	return out, nil
}

// CheckByHand is what a spawn written by hand — `verse task run -f`, an ask's `then: {spawn}` —
// has to be beside Launch.Check: nothing fires for it, so it sets none of what a firing sets
// (chain, occurrence, as, actsFor, credential, passed), and an expression, `$[…]`, in a field that
// names something is refused rather than left as written, since there is nothing to bind it. The
// prompt and the vars are prose and shell values, and are left as written.
func (s Spec) CheckByHand() error {
	for _, f := range []struct {
		field string
		set   bool
	}{
		{"chain", s.Chain != ""}, {"occurrence", s.Occurrence != 0}, {"as", s.As != ""}, {"actsFor", s.ActsFor != ""},
		{"credential", s.Credential != nil}, {"passed", len(s.Passed) > 0},
	} {
		if f.set {
			return fmt.Errorf("%s: a firing's, and nothing fires for a spawn written by hand", f.field)
		}
	}
	bound := map[string]string{"subject": s.Subject}
	if s.Agent != nil {
		bound["agent.name"] = s.Agent.Name
		for k, v := range s.Agent.Settings {
			bound["agent."+k] = v
		}
	}
	for name, blocks := range s.Environment.Systems {
		for i, b := range blocks {
			for k, v := range b.Attributes {
				bound[fmt.Sprintf("environment.systems.%s[%d].%s", name, i, k)] = v
			}
		}
	}
	for _, field := range slices.Sorted(maps.Keys(bound)) {
		if exprs.Templated(bound[field]) {
			return fmt.Errorf("%s: an expression is bound when a rule fires, and nothing fires for a spawn written by hand", field)
		}
	}
	return nil
}

// Who is who does the work, as a record says it: the agent's name, "script", or "" for a spawn
// as written that names no agent, which takes verse's default agent when it is bound.
func (s Spec) Who() string {
	switch {
	case s.Script != "":
		return "script"
	case s.Agent != nil:
		return s.Agent.Name
	}
	return ""
}

// agent expands which agent, its model and its effort, and copies its annotations.
func (x *binder) agent(a *runner.Agent) *runner.Agent {
	if a == nil {
		return nil
	}
	out := &runner.Agent{Name: x.text("agent.name", a.Name)}
	if a.Settings != nil {
		out.Settings = make(map[string]string, len(a.Settings))
		for _, k := range slices.Sorted(maps.Keys(a.Settings)) {
			out.Settings[k] = x.text("agent."+k, a.Settings[k])
		}
	}
	if a.Annotations != nil {
		out.Annotations = maps.Clone(a.Annotations)
	}
	return out
}

// environment expands what a charter has reason to build out of the firing — which
// repository, which branch it starts from, which tracker, the values of its variables — and
// copies the rest.
//
// A SCRIPT'S TEXT IS NOT EXPANDED. It is a program, and a value spliced into it is part of the
// program: a branch named `x;curl …` reflected from a remote would run. Data reaches a script
// as its variables instead, `environment: {vars: {BRANCH: $[branch]}}`, which the shell reads as
// `"$BRANCH"` and never parses. Shapes are not expanded, and nothing of the network is: what a
// run may reach is the charter's to say, never the subject's.
//
// Copied, never shared: a bound spec aliasing the charter's maps would let one firing's edit
// reach every later one.
func (x *binder) environment(e systems.Environment) systems.Environment {
	var out systems.Environment
	if e.Vars != nil {
		out.Vars = make(map[string]string, len(e.Vars))
		for _, k := range slices.Sorted(maps.Keys(e.Vars)) {
			out.Vars[k] = x.text("environment.vars."+k, e.Vars[k])
		}
	}
	if e.Systems != nil {
		out.Systems = systems.Systems{}
		for _, name := range slices.Sorted(maps.Keys(e.Systems)) {
			for i, blk := range e.Systems[name] {
				nb := systems.Block{Attributes: make(map[string]string, len(blk.Attributes)), Verbs: make(map[string][]systems.Shape, len(blk.Verbs))}
				for _, k := range slices.Sorted(maps.Keys(blk.Attributes)) {
					v := blk.Attributes[k]
					if name != "network" {
						v = x.text(fmt.Sprintf("environment.systems.%s[%d].%s", name, i, k), v)
					}
					nb.Attributes[k] = v
				}
				for verb, shapes := range blk.Verbs {
					for _, sh := range shapes {
						nb.Verbs[verb] = append(nb.Verbs[verb], maps.Clone(sh))
					}
				}
				out.Systems[name] = append(out.Systems[name], nb)
			}
		}
	}
	return out
}

// AnnotationVirtualKey is the agent annotation the verse runner reads its Portkey virtual key
// from: the name verse resolved a spawn's `virtualKey` address to.
const AnnotationVirtualKey = "verse.portkey/virtual-key"

// CheckAnnotations refuses an agent whose annotations name the virtual key. Verse writes that one
// itself, from `virtualKey`, once whoever named the address may read it (versed.credentialAt):
// a key written straight into the annotations would be spent without that read.
func CheckAnnotations(a *runner.Agent) error {
	if a == nil {
		return nil
	}
	if _, ok := a.Annotations[AnnotationVirtualKey]; ok {
		return fmt.Errorf("agent.annotations: %s is verse's to write: name the credential's address "+
			"as `virtualKey`, and verse resolves it as whoever named it", AnnotationVirtualKey)
	}
	return nil
}
