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
//	Doer      → who does the work: an agent the runner offers, or a script, and what it works
//	            with (git repositories and trackers, network, environment, model)
//	Prompt    → the job, written for whoever does it
//	Policy    → what happens to each step: denied, asked about (approvals, deadline,
//	            answerers, labels), or allowed; the last row is the dial
//	Runner    → where it runs
//
// What a run works with is what its doer is set up with — a repository checked out, a
// network it may reach — and never a dial: the runner refuses what the spawn did not give.
package launch

//go:generate go tool tony-codegen -dir .

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/runner"
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
	// Prompt is the job, written for whoever does it. A script's doer reads it as its standing
	// instructions; an agent's as the prompt.
	Prompt string `tony:"field=prompt,omitzero"`
	// Doer is who does the work and what it works with (runner.Doer): `agent: verse` or another
	// agent the runner offers, or `script: "…"`, with `with: {git, git-issue, env, model}` and the runner's annotations. The repositories under `git` are checked out for
	// it, and what may leave each is the policy's: a push no row allows or asks about is read
	// only. `repo`, `from` and `update` take $vars, so a charter can say `update:
	// [$subject.head]`; `create` and `delete` do not: a pattern says how far a run may reach,
	// and payload text must not widen what a run may push. A script is expanded as the prompt
	// is, since it is this spawn's own text.
	Doer runner.Doer `tony:"field=doer,omitzero"`
	// Policy is what the task is given and what happens to each step of it (task.Policy): a
	// verb no row names is not the task's, the first row that matches a step denies it, asks
	// about it (how many approvals, who answers, how long the question stands, what silence
	// settles on, what it says about itself) or allows it, and the last row, which names no
	// step, decides the calls of named verbs no row above matched. The trigger's `gates:` are
	// the FIRING's and run once before the action; the policy decides every step of the task
	// it starts. Absent gives the task no verb of any system; a firing that already answered
	// its own gates allows the verbs it names, `{match: {system: git}, then: allow}`, and does
	// not ask again for each action.
	//
	// Not expanded: a row's match names a step and its fields, and a $var in one could only
	// name a repository or a ref chosen by whoever last wrote the entity being matched — the
	// widening-by-binding risk `create` patterns are not expanded for either.
	Policy task.Policy `tony:"field=policy,omitzero"`
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
		Doer:    bindDoer(s.Doer, b),
	}
	out.Retain = s.Retain
	// The contract is a pattern a reader of the result applies, not text about the thing the
	// firing is about: it is carried across as it was written.
	out.Answer = s.Answer
	// VirtualKey is carried and NOT expanded: it names a credential drawn from a fixed set, so a
	// $var in one could only name a key that does not exist — or one chosen by whoever last
	// wrote the entity being matched, which would let payload text decide whose budget this
	// firing spends.
	out.VirtualKey = s.VirtualKey
	// The policy is copied and not expanded: a row's match is a pattern over what the task
	// does, and its answerers are names the question is held out to.
	out.Policy = s.Policy.Clone()
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

// bindDoer expands what a charter has reason to build out of the firing — which agent, its
// model and effort, which repository, which branch it starts from, which tracker, the values
// of its environment — and copies the rest.
//
// A SCRIPT'S TEXT IS NOT EXPANDED. It is a program, and a value spliced into it is part of the
// program: a branch named `x;curl …` reflected from a remote would run. Data reaches a script
// as its environment instead, `with: {env: {BRANCH: $branch}}`, which the shell reads as
// `"$BRANCH"` and never parses. It also leaves the script's own `$b` to the shell. The network
// is not expanded either: what a run may reach is the charter's to say, never the subject's.
//
// Copied, never shared: a bound spec aliasing the charter's slices would let one firing's edit
// reach every later one.
func bindDoer(d runner.Doer, b Binding) runner.Doer {
	out := runner.Doer{Agent: expand(d.Agent, b), Script: d.Script}
	out.With.Model, out.With.Effort = expand(d.With.Model, b), expand(d.With.Effort, b)
	if d.With.Env != nil {
		out.With.Env = make(map[string]string, len(d.With.Env))
		for k, v := range d.With.Env {
			out.With.Env[k] = expand(v, b)
		}
	}
	if d.Annotations != nil {
		out.Annotations = make(map[string]string, len(d.Annotations))
		for k, v := range d.Annotations {
			out.Annotations[k] = v
		}
	}
	for _, r := range d.With.Git {
		out.With.Git = append(out.With.Git, runner.Repo{Repo: expand(r.Repo, b), From: expand(r.From, b)})
	}
	for _, t := range d.With.GitIssue {
		out.With.GitIssue = append(out.With.GitIssue, runner.Tracker{Repo: expand(t.Repo, b)})
	}
	if n := d.With.Network; n != nil {
		out.With.Network = &runner.Network{Mode: n.Mode, Allow: slices.Clone(n.Allow)}
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

// AnnotationVirtualKey is the doer annotation the verse runner reads its Portkey virtual key
// from: the name verse resolved a spawn's `virtualKey` address to.
const AnnotationVirtualKey = "verse.portkey/virtual-key"

// CheckAnnotations refuses a doer whose annotations name the virtual key. Verse writes that one
// itself, from `virtualKey`, once whoever named the address may read it (versed.credentialAt):
// a key written straight into the annotations would be spent without that read.
func CheckAnnotations(d runner.Doer) error {
	if _, ok := d.Annotations[AnnotationVirtualKey]; ok {
		return fmt.Errorf("doer.annotations: %s is verse's to write: name the credential's address "+
			"as `virtualKey`, and verse resolves it as whoever named it", AnnotationVirtualKey)
	}
	return nil
}
