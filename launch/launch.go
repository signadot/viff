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
//	Agent       → which agent a prompt is for, and how it works (absent: the runner's default)
//	Environment → what the run can reach, fixed when it starts (systems.Environment)
//	Autonomy    → what happens to each step within it: denied, asked about (approvals,
//	              deadline, answerers, labels), or allowed; the last row is the dial
//	Runner      → where it runs
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

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// AsSubject is the one value `as` takes: the owner of the record the firing is about.
const AsSubject = "subject"

// Spec is the declarative launch descriptor. It is a *template*: string fields may contain
// $vars (e.g. "$subject") that Bind expands against a concrete event, turning a generic action
// into a specific launch. All fields are optional; a runner supplies defaults for any left blank.
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
	// Agent is the agent a prompt is for, and how it works; absent is the runner's default.
	// Its name and its settings take $vars.
	Agent *runner.Agent `tony:"field=agent,omitzero"`
	// Person is reserved for a person doing the work; no runner takes one yet.
	Person string `tony:"field=person,omitzero"`
	// Environment is what the run can reach and the variables its shell is given
	// (systems.Environment). A block's attributes and the vars take $vars, so a charter can
	// check out `$subject.value.repo`; a shape does not, and neither does the network: a
	// pattern says how far a run may reach, and payload text must not widen it.
	Environment systems.Environment `tony:"field=environment,omitzero"`
	// Autonomy is what happens to each step of the task (task.Autonomy): the first row that
	// matches it denies it, asks about it (how many approvals, who answers, how long the
	// question stands, what silence settles on, what it says about itself) or allows it, and
	// the last row, which names no step, decides the rest. The trigger's `gates:` are the
	// FIRING's and run once before the action; the autonomy decides every step of the task it
	// starts. Absent asks before every step.
	//
	// Not expanded: a row's match names a step and its fields, and a $var in one could only
	// name a repository or a ref chosen by whoever last wrote the entity being matched.
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
	// party for this spawn, and only the charter that derefs `$data.severity` knows what shape
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
	// Runner is WHERE this launch runs: `verse.runners.<name>`, an entity installed with
	// `verse runner add`. Empty means the verse's own default runner.
	//
	// A name rather than a url, and that indirection is the point: a url on the spawn would
	// spread the deployment through the charters, so every charter wanting the gpu pool would
	// carry its address and moving the pool would mean editing all of them. It is the same
	// indirection `provider:` and `resolver:` already are. A launch naming a runner that is not
	// installed is REFUSED rather than run somewhere else — see versed.runnerFor.
	Runner string `tony:"field=runner,omitzero"`
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
	// Asker is who `as: subject` resolved to at the firing: the subject record's owner.
	// Engine-owned like Subject and Chain, and more strictly: the engine CLEARS it on every
	// launch and sets it only from `as`, because a charter that could write it would name whoever
	// it liked to act as.
	Asker string `tony:"field=asker,omitzero"`
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
	// settled. Engine-owned like Asker, and as strictly: the engine clears it on every launch and
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

// Binding is the variables a firing binds: `subject`, the subject's fields, `owner`, …
type Binding map[string]string

// Bind expands the template against a binding and returns the concrete spec. Pure: the
// receiver is not changed, and nothing in the result aliases it. Engine-owned fields (Chain,
// Occurrence, Asker, Passed) are not carried: the engine sets them.
func (s Spec) Bind(b Binding) Spec {
	out := Spec{
		Prompt:  expand(s.Prompt, b),
		Runner:  expand(s.Runner, b),
		Subject: expand(s.Subject, b),
		Timeout: s.Timeout,
		Script:  s.Script,
		Person:  s.Person,
	}
	out.Agent = bindAgent(s.Agent, b)
	out.Environment = bindEnvironment(s.Environment, b)
	out.Retain = s.Retain
	// The contract is a pattern a reader of the result applies, not text about the thing the
	// firing is about: it is carried across as it was written.
	out.Answer = s.Answer
	// VirtualKey is carried and NOT expanded: it names a credential drawn from a fixed set, so a
	// $var in one could only name a key that does not exist — or one chosen by whoever last
	// wrote the entity being matched, which would let payload text decide whose budget this
	// firing spends.
	out.VirtualKey = s.VirtualKey
	// The autonomy is copied and not expanded: a row's match is a pattern over what the task
	// does, and its answerers are names the question is held out to.
	out.Autonomy = s.Autonomy.Clone()
	// As is carried and not expanded: it takes one word (AsSubject), and payload text deciding
	// whose authority a task runs with is the thing it exists to prevent. Asker is NOT carried —
	// it is the engine's answer to As, set after binding (trigger.launch).
	out.As = s.As
	// Credential is carried and not expanded, for VirtualKey's reason: payload text deciding
	// which system a token is good for is what declaring it exists to prevent. Passed is NOT
	// carried, as Asker is not: it is the engine's account of the firing's gates.
	if s.Credential != nil {
		c := *s.Credential
		out.Credential = &c
	}
	return out
}

// CheckByHand is what a spawn written by hand — `verse task run -f`, an ask's `then: {spawn}` —
// has to be beside Launch.Check: nothing fires for it, so it sets none of what a firing sets
// (chain, occurrence, as, asker, credential, passed), and a `$` in a field that names something
// — where a firing would bind one (Bind) and it can mean nothing else — is refused rather than
// left as written, since there is nothing to bind it. The prompt and the vars are prose and shell
// values, where a `$` is as likely literal (`$HOME`, `$5`), and are left as written, as a charter
// leaves a `$key` it has no binding for.
func (s Spec) CheckByHand() error {
	for _, f := range []struct {
		field string
		set   bool
	}{
		{"chain", s.Chain != ""}, {"occurrence", s.Occurrence != 0}, {"as", s.As != ""}, {"asker", s.Asker != ""},
		{"credential", s.Credential != nil}, {"passed", len(s.Passed) > 0},
	} {
		if f.set {
			return fmt.Errorf("%s: a firing's, and nothing fires for a spawn written by hand", f.field)
		}
	}
	bound := map[string]string{"runner": s.Runner, "subject": s.Subject}
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
		if strings.Contains(bound[field], "$") {
			return fmt.Errorf("%s: a `$` is bound when a rule fires, and nothing fires for a spawn written by hand", field)
		}
	}
	return nil
}

// Who is who does the work, as a record says it: the agent's name, "script", or "" for the
// runner's default agent.
func (s Spec) Who() string {
	switch {
	case s.Script != "":
		return "script"
	case s.Agent != nil:
		return s.Agent.Name
	}
	return ""
}

// bindAgent expands which agent, its model and its effort, and copies its annotations.
func bindAgent(a *runner.Agent, b Binding) *runner.Agent {
	if a == nil {
		return nil
	}
	out := &runner.Agent{Name: expand(a.Name, b)}
	if a.Settings != nil {
		out.Settings = make(map[string]string, len(a.Settings))
		for k, v := range a.Settings {
			out.Settings[k] = expand(v, b)
		}
	}
	if a.Annotations != nil {
		out.Annotations = maps.Clone(a.Annotations)
	}
	return out
}

// bindEnvironment expands what a charter has reason to build out of the firing — which
// repository, which branch it starts from, which tracker, the values of its variables — and
// copies the rest.
//
// A SCRIPT'S TEXT IS NOT EXPANDED. It is a program, and a value spliced into it is part of the
// program: a branch named `x;curl …` reflected from a remote would run. Data reaches a script
// as its variables instead, `environment: {vars: {BRANCH: $branch}}`, which the shell reads as
// `"$BRANCH"` and never parses. Shapes are not expanded, and nothing of the network is: what a
// run may reach is the charter's to say, never the subject's.
//
// Copied, never shared: a bound spec aliasing the charter's maps would let one firing's edit
// reach every later one.
func bindEnvironment(e systems.Environment, b Binding) systems.Environment {
	var out systems.Environment
	if e.Vars != nil {
		out.Vars = make(map[string]string, len(e.Vars))
		for k, v := range e.Vars {
			out.Vars[k] = expand(v, b)
		}
	}
	if e.Systems != nil {
		out.Systems = systems.Systems{}
		for name, blocks := range e.Systems {
			for _, blk := range blocks {
				nb := systems.Block{Attributes: make(map[string]string, len(blk.Attributes)), Verbs: make(map[string][]systems.Shape, len(blk.Verbs))}
				for k, v := range blk.Attributes {
					if name != "network" {
						v = expand(v, b)
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

// Expand is expand for callers outside the package: the spec's own $var rule applied to one
// string — a hook's patch value, a say's text.
func Expand(s string, b Binding) string { return expand(s, b) }

// expand replaces every `$key` in s with the binding's value, longest key first, so `$subject`
// is not read as `$sub` followed by `ject`. A key the binding lacks is left as written.
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
