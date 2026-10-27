// Package mirror reflects a running Task's state into the entity-verse as a
// task:instance:<id> entity — one-way, index-not-warehouse. This is what makes
// "observe another task" the same act as "observe an entity": once a task
// is in the verse, a trigger can match on its state like any other entity.
//
// Reflected entities live in the "task" system, which the trigger engine keeps
// OUT of the default match set — meta-observation is opt-in, so the loop cannot
// ignite on its own machinery.
package mirror

import (
	"cmp"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// System is the entity-verse system reflected tasks live under. Keep it out
// of any default/ambient trigger match set.
const System = "task"

// The reflected status vocabulary is task.Status (running/completed/cancelled/
// failed/orphaned), carried on the entity payload as the "status" field; its
// Terminal() decides when to stop projecting.

// InstanceKind is the kind a mirrored task lives under. Named, because a reader selecting
// instances has to say so — the system also holds claims, which carry no status and would fall
// through a check that only looks at one.
const InstanceKind = "instance"

// Ref is the entity that mirrors task id.
func Ref(id string) entity.Ref { return entity.VerseRef(System, InstanceKind, id) }

// HeldByField is who holds a task at its boundary, on its record: `operator` for `flow suspend --task`,
// and the ids of the suspensions that paused it. Absent when nothing does.
const HeldByField = "heldBy"

// RunKind is the kind ONE RUN lives under, as against the id's latest state.
//
// `task:instance:<id>` is a live view keyed by the work: an id names the chain and the
// subject, deliberately, so a re-fire reuses it — and so a second run overwrites the first. A
// record was found on staging reading `ended` fifty minutes BEFORE its `started`, carrying one
// run's answer beside another's status, because that is what overwriting field-by-field
// produces (git-issue gjv063rrh12ksvgnfsn0).
//
// So a run also writes itself down at an address only it has. The two are not redundant: the
// instance answers "what is this work doing", the run answers "what happened that time", and
// only the second survives the next attempt.
const RunKind = "run"

// RunRef addresses one run: the task's id and the revision that run is about.
func RunRef(id string, occurrence int64) entity.Ref {
	return entity.VerseRef(System, RunKind, id+"@"+strconv.FormatInt(occurrence, 10))
}

// writeRun records this run at its own address — at creation, so a run that never finishes
// still leaves a trace, and at the terminal, so the outcome is what stands there afterwards.
//
// Twice per RUN rather than once per event: the instance record is the live view and pays the
// per-narration cost already, and a run record that tracked every transition would double the
// busiest writer in the system to say nothing the instance is not saying better.
//
// NOTHING PRUNES THESE, and that is a decision rather than an omission. Growth is one small
// record per completed run — bounded by runs, not by events or by time — and both ways to
// remove them already exist: `verse data rm`, and a charter matching on age that writes
// `verse.meta.vanishing` and acts on the edge. Choosing a retention count here would be
// picking a number for how much history is worth keeping before anybody has felt the want of
// it, in the one place a reader cannot override.
//
// An occurrence of 0 writes nothing: without it there is no address that distinguishes this
// run from the next, and a second record at one address is the bug this is fixing.
func writeRun(store entity.Store, id string, occurrence int64, st entity.Obj) {
	if occurrence == 0 {
		return
	}
	write(store, id, st, RunRef(id, occurrence))
}

// Minted describes a credential verse signed for a task: the system it is for, the `sub` a
// target matches on, the token's own id, and when it stops being good. The token is not here.
type Minted struct {
	Audience string
	Sub      string
	JTI      string
	Expires  string
}

// Meta is the provenance Reflect publishes alongside a task's own state: where it runs
// and what it is about. Grouped rather than passed as four positional strings, which is
// both unreadable at the call site and easy to transpose.
type Meta struct {
	// Subject is the flow this task belongs to. It is carried on the entity as "flow"
	// so a completion trigger can advance that subject when the task finishes, and does
	// not collide with the $subject binding (the matched ref).
	Subject string
	// Occurrence is the store revision this RUN is about — the commit that woke the firing,
	// or the store's own revision when a person typed `verse task run`. Zero when neither is
	// known.
	//
	// A task record is one entity per id, and an id names the work rather than the run, so
	// without this a second run overwrites the first and the fields of two runs sit side by
	// side with nothing to tell them apart (git-issue gjv063rrh12ksvgnfsn0).
	Occurrence int64
	// Runner is the task-control backend hosting it — "local" or a remote URL — so a
	// restart can decide resume-or-orphan.
	Runner string
	// RunnerName is the runner ENTITY a spawn named — `verse.runners.<name>` — when it named
	// one. Empty for a spawn that took the verse's default.
	//
	// It is stamped BESIDE Runner rather than instead of it, and that split is the one place
	// this design can go quietly wrong. Runner is what a restart can act on with nothing else
	// present; RunnerName is what a person reads and what a later change can re-resolve.
	// Stamping only the name would make a restart depend on the runner entity still existing,
	// so removing one would orphan running work by making it unreconstructible — a deletion
	// with an action at a distance.
	RunnerName string
	// StartedAt is when the task began, RFC3339. Empty on a fresh spawn — Reflect stamps
	// the clock — and carried back verbatim by a re-attach, which is the only caller that
	// knows this task is older than this verse.
	StartedAt string
	// Started is the record's `started`, carried back verbatim by a re-attach as StartedAt is,
	// so the run's record still says when the run started. Empty is now.
	Started string
	// Chain is the kpath of the action that spawned it, inside its trigger: "review",
	// "review.onDone". Empty for a task with no trigger behind it (`verse task run`).
	Chain string
	// Prompt is what the task was asked to do, already bound. Small, and the task's
	// identity to a reader — so it belongs on the index next to what the task produced,
	// not only on the read plane that outlives nothing.
	Prompt string
	// Agent is the agent the launch was for, as the spawn wrote it: its name (none for the
	// runner's default) and its settings, never its annotations, which are the runner's plumbing.
	// Nil for a script (Script). The record carries it from the first write, so a task that never
	// started says what was asked; once the runner reports, its resolved agent and the settings it
	// ran with replace it (AgentRecord), so a running or finished task says what ran.
	Agent *runner.Agent
	// Script is whether the work is a script, which has no agent.
	Script bool
	// Git is the repositories the run works in, and where each started (runner.Repo): the facts
	// of the `git` system, on the record so a reader asking what this run worked on need not
	// read the charter that spawned it, which may since have changed. What may leave each is
	// the autonomy's, beside it.
	Git []runner.Repo
	// ActsFor is the person the run acts for, the record's `actsFor`: beside its rule, where the
	// rule said `as: subject` (the owner of the record that fired it), or alone, where a person ran
	// it or an ask's `then` did. Published so "whose authority is this running with" is read off the
	// record rather than worked out from a pair of names nobody sees. It is whom
	// task.PrincipalAnswerer names.
	ActsFor string
	// Answerers is whom the task's questions naming nobody are held out to (task.Launch.Answerers),
	// published as the record's `answerers` so a restart keeps it. Empty is the docket.
	Answerers []string
	// As is the principal the runner acts as, when the runner is known at spawn — a runner
	// reached by url carries its identity on its entity. The mirror then, at the first
	// reflection, lists it as a runner, grants it the task, and writes every report as it.
	// Empty for a local task, whose mirror is the system's.
	As string
	// HeldBy says who holds the task at its boundary — `operator` for `flow suspend --task`, and the
	// suspensions that paused it — asked at every write, since the engine holding the task is
	// who knows and this record is replaced whole at each one. Nil holds nothing.
	HeldBy func() []string
	// SlidBy is the last autonomy the control route relayed to this task: the actor the route
	// checked, and the autonomy's summary (task.Autonomy.Summary) it relayed. by is "" where none
	// was.
	SlidBy func() (by, summary string)
	// Autonomy is the spawn's own autonomy, which a task's questions are posed under. A question a
	// worker raises by a row's name is held out to THAT row's answerers, whatever the frame
	// says: the row is the charter's, and the runner does not get to soften it.
	Autonomy task.Autonomy
	// AutonomyNow is the task's autonomy as it stands, where it can change while it runs (a `task
	// autonomy` slide): what the control route set, read when a question is posed. Nil is
	// Autonomy, as the spawn wrote it.
	AutonomyNow func() task.Autonomy
	// Arm starts the clock on a question a FRAME posed with a deadline, when set. The record
	// is written here; the timer that settles it is the server's, so the server hands one in.
	Arm func(questionID, within string)
	// Credential is the credential verse minted for the task, where it minted one: what it is
	// good for and until when, and never the token. It is verse's own account that a token went
	// out, beside the one the system it is shown to keeps.
	Credential *Minted
}

// TriggerOf returns the trigger a chain belongs to — its root kpath segment. The chain is
// a kpath into that trigger's Action, so the root is everything up to the first descent.
func TriggerOf(chain string) string {
	if i := strings.IndexAny(chain, ".[{("); i >= 0 {
		return chain[:i]
	}
	return chain
}

// Reflect subscribes to p's read plane and mirrors its state into store as a
// task:instance:<id> entity, until p reaches a terminal status. It projects
// significant transitions (pending gate, last action, status) plus a bounded view
// of the task's output — the latest narration ("say") and last tool output
// ("lastOutput") as previews, and the full final answer ("result") on completion.
// It stays an index, not a warehouse: the running fine grain (and any large output)
// lives on the read plane; the entity keeps the outcome, durably, so `entity get
// task:instance:<id>` shows what a task said and produced even after its
// ephemeral read plane is gone. Projection is one-way: an entity is a view, never a
// handle (control still goes through the task API).
//
// The provenance a task carries beyond its own state is Meta.
// RecordFields is every field a task record carries — the flat vocabulary a completion hook's
// `$name` resolves against, since a hook is handed the task's own payload.
//
// It is a LIST BESIDE THE WRITES rather than a derivation of them, and the failure mode of that
// is what decides how it may be used: a field added below and not added here is missing from
// this list, so nothing may be REFUSED on the strength of it. What it is for is advice — a hook
// naming something no task record ever holds is almost always a field of the SUBJECT, written in
// the one place the subject's fields are not flat (git-issue 91jktrr3h12ksa8fgsn0), and saying so
// at install costs nothing if this list is a name short.
//
// `hook` is the engine's, written onto the same record when a hook fails (trigger.recordHookFailure).
var RecordFields = map[string]bool{
	"actsFor": true, "answerers": true, "chain": true, "credential": true, "cursor": true, "data": true,
	"agent": true, "ended": true, "err": true, "flow": true, "git": true, "hook": true,
	"lastAction": true, "lastOutput": true, "observed": true, "occurrence": true,
	"paused": true, "pending": true, "autonomy": true, "autonomyBy": true, "prompt": true, "refused": true, "result": true, "script": true,
	"runner": true, "runnerName": true, "say": true, "started": true, "startedAt": true,
	"status": true, "trigger": true,
	HeldByField: true,
}

func Reflect(store entity.Store, id string, h task.Handle, m Meta) {
	ch, unsub := h.Subscribe()
	// WHEN, and only twice: `started` here, `ended` at the terminal status below. A record
	// stamped on every update would be an entity that changes whenever anything narrates,
	// which wakes every trigger watching this slice for no fact anybody wrote — and the two
	// instants somebody actually asks about are the two ends. RFC3339, the same spelling
	// `verse.meta.vanishing` and a source's `lastReflect` use.
	st := entity.Obj{"status": string(task.Running), "started": cmp.Or(m.Started, nowStamp())}
	if m.Subject != "" {
		st["flow"] = m.Subject
	}
	if m.Occurrence > 0 {
		st["occurrence"] = m.Occurrence
	}
	if m.Runner != "" { // which runner runs this — so a restart can resume-or-orphan
		st["runner"] = m.Runner
	}
	// And which installed runner it was NAMED as, so an operator can select by the name they
	// wrote. It keeps working for a runner that has since been removed, which is when they
	// most want it — the url above is what makes the work still reachable then.
	if m.RunnerName != "" {
		st["runnerName"] = m.RunnerName
	}
	// When this task began, so `verse runner` can say when a runner was last used and a
	// runner nobody names is visible as such. Written once, at the first reflection, and
	// carried across a re-attach rather than re-stamped — a resumed task did not start
	// again, and a clock that restarts on every restart measures the verse, not the work.
	if m.StartedAt != "" {
		st["startedAt"] = m.StartedAt
	} else {
		st["startedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	// Where this task came from in the charter. `chain` is the spawning action's kpath
	// inside its trigger (review, review.onDone, …) and `trigger` is its root segment,
	// published separately because a payload predicate reads VALUES, not fragments of an id:
	// "every task of the review trigger" is then `value: {trigger: review}` rather than a
	// glob over ids.
	if m.Chain != "" {
		st["chain"] = m.Chain
		st["trigger"] = TriggerOf(m.Chain)
	}
	if m.Prompt != "" {
		st["prompt"] = m.Prompt
	}
	switch {
	case m.Script:
		st["script"] = true
	case m.Agent != nil:
		st["agent"] = AgentRecord(m.Agent.Name, m.Agent.Settings)
	default:
		st["agent"] = AgentRecord("", nil) // the runner's default, named when it reports
	}
	if m.ActsFor != "" {
		st["actsFor"] = m.ActsFor
	}
	if len(m.Answerers) > 0 {
		st["answerers"] = m.Answerers
	}
	if c := m.Credential; c != nil {
		st["credential"] = entity.Obj{"audience": c.Audience, "sub": c.Sub, "jti": c.JTI, "expires": c.Expires}
	}
	if len(m.Git) > 0 {
		st["git"] = gitOf(m.Git)
	}
	// The task's autonomy, verse's part with it (what silence settles on, the labels): on the
	// record so that a verse that restarts while the task runs elsewhere still poses its
	// questions under it (PolicyOf), and so a reader sees what the run asks about.
	if p := m.autonomy(); len(p) > 0 {
		st["autonomy"] = p
	}
	// WHICH RUN this record is about. A firing knows — it is the commit that woke it, carried
	// on Meta — and anything else learns it here, from the commit its own first write landed
	// at: "the revision this run began at", the same question asked where there is no cause
	// to point at. Free, because the write happens either way, and it costs the spawn path no
	// round trip (Store.Rev() opens a watch when none is live; asking it there put a network
	// call in front of the record and let a task's first gate question outrun it).
	//
	// It rides on st, so every later write carries it: the first record cannot hold it —
	// nothing knows the commit until the write returns — and every one after does.
	// The first write REPLACES what stands at this address; every one after it merges.
	//
	// An id names the work, so a new run writes to the record the last one left — and a write
	// is a patch, so `started` and `status` from this run landed on top of `ended`, `err`,
	// `result` and `lastOutput` from the one before. That is how a record comes to say it
	// started at 20:42 and ended at 20:41: not a clock, not a race, just two runs merged into
	// one document with nothing marking where one stopped. entity.Raw is named for exactly
	// this — "a merge would leave fields of the version before it standing in the version
	// after" — and a task record is the document it describes.
	//
	// Only the FIRST write: the rest are this run's own transitions, and each is meant to land
	// on what it wrote a moment ago.
	if rev := project(store, id, st, m); m.Occurrence == 0 && rev > 0 {
		st["occurrence"] = rev
	}
	writeRun(store, id, occurrenceOf(st), st)
	// A TASK THE SYSTEM'S RULE SPAWNED CARRIES THE SYSTEM'S RECORD. A person's task is theirs by
	// the write above; the system's write records no owner, and the row above holds
	// `child-owner` beneath it out to everyone at any depth — so with no record of its own that
	// line reaches inside the task, and whatever somebody adds beneath it is theirs
	// (entity.OwnedForSystem).
	if p := entity.PrincipalOf(store); p != "" {
		made := []entity.Ref{Ref(id)}
		if occ := occurrenceOf(st); occ != 0 {
			made = append(made, RunRef(id, occ))
		}
		for _, ref := range made {
			if err := entity.OwnedForSystem(store, ref, p); err != nil {
				fmt.Fprintf(os.Stderr, "verse: task %s: %s has no record of its own: %v\n", id, ref, err)
			}
		}
	}
	// A runner known at spawn is granted the task now, before its first report lands, and
	// the reports are written as it from the first frame: the assignment is the owner's act,
	// made at the moment of assigning.
	if m.As != "" {
		claimed(asPrincipal(store, entity.SystemMirror), id, st, m.As)
	}
	go func() {
		// A stream ending is not the same fact as a task ending, and treating them as one
		// is what let a mirror stop forever: the connection died, Reflect returned, and the
		// entity kept saying `running` about work that had long since finished.
		//
		// The terminal is what says the task ended — always the last frame, never dropped
		// and never absent (see task/event.go). A channel that closes without one closed
		// for a reason of its own, and the answer is to resume from the cursor this loop has
		// been keeping all along.
		attempt := 0
		claimant := m.As
		for {
			ended, heard := drainInto(store, id, st, m, h, ch, &claimant)
			unsub()
			if ended {
				return
			}
			if heard {
				attempt = 0 // it worked for a while; the next outage starts its own count
			}
			if attempt >= reconnectAttempts {
				// Gave up, and says so. The entity keeps `observed: false` rather than
				// reverting to a confident `running` nobody is watching.
				return
			}
			// Said BEFORE the wait, not after it: a reader asking during the outage is
			// exactly the reader who needs to know the index is not current.
			st["observed"] = false
			project(store, id, st, m)
			time.Sleep(backoff(attempt))
			attempt++
			since, _ := st["cursor"].(int64)
			ch, unsub = h.SubscribeSince(since)
		}
	}()
}

// launchAsk is the ask of the autonomy's row a frame's question is about, by the row's name:
// verse's part of it, which the runner never read, comes from here. Nil where the row has no
// shape, or the question is not a row's.
func launchAsk(m Meta, ev task.Event) *task.Ask {
	if r, ok := m.autonomy().Named(ev.Gate); ok {
		return r.Then.Ask
	}
	return nil
}

// autonomy is the task's autonomy as it stands: AutonomyNow where the server keeps it, else the
// spawn's.
func (m Meta) autonomy() task.Autonomy {
	if m.AutonomyNow != nil {
		if p := m.AutonomyNow(); p != nil {
			return p
		}
	}
	return m.Autonomy
}

// AutonomyOf reads a task's autonomy back off its record, for a verse that restarts while the task
// runs elsewhere: what it poses the task's questions under from then on.
func AutonomyOf(p entity.Payload) task.Autonomy {
	n := entity.Get(p, "autonomy")
	if n == nil {
		return nil
	}
	var pol task.Autonomy
	if err := gomap.FromTonyIR(n, &pol); err != nil {
		return nil
	}
	return pol
}

// AgentRecord is an agent as a task's record writes it: flat, its name (absent for a default not
// yet resolved) beside its settings, as a spawn writes one.
func AgentRecord(name string, settings map[string]string) entity.Obj {
	out := entity.Obj{}
	if name != "" {
		out["name"] = name
	}
	for k, v := range settings {
		out[k] = v
	}
	return out
}

// AgentOf is who a task's record says ran it (AgentRecord): its agent, or that it is a script.
func AgentOf(p entity.Payload) (agent *runner.Agent, script bool) {
	if v, _ := entity.ToObj(p)["script"].(bool); v {
		return nil, true
	}
	if n := entity.Get(p, "agent"); n != nil {
		var a runner.Agent
		if codec.FromNode(n, &a) == nil {
			return &a, false
		}
	}
	return nil, false
}

// resolved puts on the record which agent ran and with what, as the runner says it, where it
// says: in place of what the spawn asked for.
func resolved(h task.Handle, st entity.Obj) {
	if name, settings := h.Agent(); name != "" && name != "script" {
		st["agent"] = AgentRecord(name, settings)
	}
}

// CredentialOf is what a task's record says of the credential minted for it (Meta.Credential),
// read back for a verse that re-attaches to it after a restart; nil where it says none.
func CredentialOf(p entity.Payload) *Minted {
	c := entity.Get(p, "credential")
	if c == nil {
		return nil
	}
	return &Minted{Audience: entity.Str(c, "audience"), Sub: entity.Str(c, "sub"),
		JTI: entity.Str(c, "jti"), Expires: entity.Str(c, "expires")}
}

// GitOf is the repositories a task's record says it works in (Meta.Git), read back for a verse
// that re-attaches to it after a restart.
func GitOf(p entity.Payload) []runner.Repo {
	n := entity.Get(p, "git")
	if n == nil {
		return nil
	}
	var repos []runner.Repo
	if err := gomap.FromTonyIR(n, &repos); err != nil {
		return nil
	}
	return repos
}

// withVersesPart is a row's question with verse's part of it put in: how long it stands, what
// silence settles on and how many approvals it needs are the row's, whatever the frame says.
// The question is the worker's; the rule it is decided by is the charter's, and a frame that
// set its own deadline, default or need would loosen it. A question that is no row's, a task's
// own `question-ask` or a conversation's turn, keeps what its frame says.
func withVersesPart(m Meta, ev task.Event) task.Event {
	r, ok := m.autonomy().Named(ev.Gate)
	if !ok {
		return ev
	}
	ev.Deadline, ev.Options = nil, nil
	a := r.Then.Ask
	if a == nil {
		return ev
	}
	if a.Within != "" || a.Default != "" {
		ev.Deadline = &task.Deadline{Within: a.Within, Default: a.Default}
	}
	if a.Need > 1 {
		ev.Options = []task.Option{{ID: task.Proceed, Need: a.Need}, {ID: task.Deny, Need: 1}}
	}
	return ev
}

// asPosed is a frame's question as it was posed, with whom it is held out to and its labels: a
// question already on record keeps the record's, so a frame replayed after a restart, under a
// autonomy replaced since, does not re-pose it under the new one; a question not yet posed takes
// verse's part of its row (withVersesPart, answerersFor, labelsFor).
func asPosed(docket entity.Store, m Meta, ev task.Event) (task.Event, []string, map[string]string, entity.Payload, entity.Payload) {
	e, ok, err := docket.Get(QuestionRef(ev.ID))
	if err != nil || !ok {
		ev = withVersesPart(m, ev)
		then, els := followsFor(m, ev)
		return ev, answerersFor(m, ev), labelsFor(m, ev), then, els
	}
	ev.Options = OptionsOf(e.Payload)
	ev.Deadline = nil
	if within, dflt, _ := DeadlineOf(e.Payload); within != "" || dflt != "" {
		ev.Deadline = &task.Deadline{Within: within, Default: dflt}
	}
	return ev, GranteesTo(e.Payload), LabelsOf(e.Payload), entity.Get(e.Payload, ThenField), entity.Get(e.Payload, ElseField)
}

// followsFor is what the row's ask starts on a yes and on a no, before the task hears the answer.
func followsFor(m Meta, ev task.Event) (then, els entity.Payload) {
	if a := launchAsk(m, ev); a != nil {
		return a.Then, a.Else
	}
	return nil, nil
}

// labelsFor is what the row's ask says about its question for whoever watches asks.
func labelsFor(m Meta, ev task.Event) map[string]string {
	if a := launchAsk(m, ev); a != nil {
		return a.Labels
	}
	return nil
}

// answerersFor is who a worker-raised gate's question is held out to: the spawn's gate of that
// name, when it has answerers of its own — the charter's autonomy, which the frame cannot soften
// — then whoever the frame named, which is a worker asking somebody, then whom the task's
// questions naming nobody go to (Meta.Answerers). task.PrincipalAnswerer is put as whom it acts for.
func answerersFor(m Meta, ev task.Event) []string {
	names := ev.Answerers
	if a := launchAsk(m, ev); a != nil && len(a.Answerers) > 0 {
		names = a.Answerers
	}
	if len(names) == 0 {
		names = m.Answerers
	}
	return WithPrincipal(names, m.ActsFor)
}

// WithPrincipal is answerers with task.PrincipalAnswerer put as actsFor, whom the task acts for.
// Where it acts for nobody (a firing, as its rule, or the system) the name is dropped, so a
// question naming nobody else is the docket's: only the `question-ask` tool and a turn reach here that
// way, since a gate naming it is refused at the spawn (CheckPrincipal).
func WithPrincipal(answerers []string, actsFor string) []string {
	if !task.NamesPrincipal(answerers) {
		return answerers
	}
	if entity.IsSystem(actsFor) {
		actsFor = ""
	}
	out := make([]string, 0, len(answerers))
	for _, a := range answerers {
		if a != task.PrincipalAnswerer {
			out = append(out, a)
		} else if actsFor != "" && !slices.Contains(answerers, actsFor) {
			out = append(out, actsFor)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ActsFor is whom a task launched as principal acts for: the asker of a pair, or a person or
// agent who launched it as themselves. A firing, launched as its rule, and the system act for
// nobody.
func ActsFor(principal string) string {
	if asker, _, ok := entity.PairOf(principal); ok {
		if entity.IsSystem(asker) {
			return ""
		}
		return asker
	}
	return Launcher(principal)
}

// CheckPrincipal refuses an autonomy whose rows name task.PrincipalAnswerer for a task that acts
// for nobody: asked, they would be the docket's, which is not what they say.
func CheckPrincipal(p task.Autonomy, actsFor string) error {
	if (actsFor == "" || entity.IsSystem(actsFor)) && p.NamesPrincipal() {
		return fmt.Errorf("a row names %s, and this task acts for nobody: it runs as its rule or "+
			"the system, so name who answers, or spawn it `as: subject`", task.PrincipalAnswerer)
	}
	return nil
}

// Launcher is the person or agent who launched a task as themselves, from the launch's
// principal: nobody for a firing, launched as its rule, or for a pair, which acts for its asker.
func Launcher(principal string) string {
	if p, err := entity.ParsePrincipal(principal); err == nil && (p.Kind == entity.UserKind || p.Kind == entity.AgentKind) {
		return p.String()
	}
	return ""
}

// LocalRunner is Meta.Runner for a task hosted in this process, whose frames are verse's own.
const LocalRunner = "local"

// settleRelayed records a relayed task's decision frame.
//
// WHERE THE ANSWER ROUTE ACCEPTED AN ANSWER, the question already says everything — who, what
// they chose, their ballot — and the frame is the worker's acknowledgement that it arrived:
// nothing is touched. The frame's verdict is not read, so it cannot differ.
//
// WHERE NOBODY WAS ASKED, the worker settled it, and the verdict is the frame's because there
// is no other: by the runtime's own name where the frame says the runtime did it (auto, the
// deadline, a cancel — names that are nobody's), and otherwise by the worker of record, never
// the name the frame carries. It casts NO BALLOT: a ballot is an answerer's, and the worker is
// not one — and an echo that outruns the route's own write (settle delivers first) would
// otherwise leave the runner a ballot beside the answerer's.
func settleRelayed(docket entity.Store, questionID string, res task.Resolution, claimant string) {
	ref := QuestionRef(questionID)
	if e, ok, err := docket.Get(ref); err == nil && ok &&
		entity.Str(e.Payload, "status") == QuestionAnswered && entity.Str(e.Payload, "by") != "" {
		return
	}
	if res.Cast() {
		res.By = claimant
	}
	rec := answerRecord(res)
	delete(rec, "ballots")
	writeAt(docket, ref, rec)
}

// slidBy is who a relayed task's slide is recorded as by: the actor the control route relayed
// this autonomy for (Meta.SlidBy, by the autonomy's summary), and the worker of record for a slide
// to an autonomy nobody relayed. The relayed slide is not used up by the first frame that matches
// it: the runner reports the slide itself and the worker echoes it, and both are the same slide.
func slidBy(m Meta, summary, claimant string) string {
	if m.SlidBy != nil {
		if by, relayed := m.SlidBy(); by != "" && relayed == summary {
			return by
		}
	}
	return claimant
}

// mayClaim reports whether a name on a running status is one a worker claims as: somebody, and
// not a name verse makes. A remote runner's stream is held to it here, since what follows is a
// grant to that name and every projection written as it.
func mayClaim(by string) bool {
	switch entity.KindOf(by) {
	case entity.SystemKind, entity.ViaKind:
		return false
	}
	return by != ""
}

// asPrincipal is the store's view acting as principal, and the store itself where it
// attributes nothing — a test's raw store, which stamps and checks nothing and should not
// begin to because the mirror asked.
func asPrincipal(store entity.Store, principal string) entity.Store {
	if entity.PolicyOf(store) == nil {
		return store
	}
	return entity.As(store, principal)
}

// claimed records that a worker took the task: the worker is listed as a runner principal,
// and holds `read-write` and `control` on the task's record and on this run's — the two
// records its reports are projected onto — and on nothing else. The grant is the task
// owner's act, carried out by versed on their behalf (entity.AddGrant); a question the task
// raises carries its own record, and the grant does not reach it.
func claimed(system entity.Store, id string, st entity.Obj, by string) {
	if entity.PolicyOf(system) == nil {
		return
	}
	_ = entity.Note(system, by)
	refs := []entity.Ref{Ref(id)}
	if occ := occurrenceOf(st); occ > 0 {
		refs = append(refs, RunRef(id, occ))
	}
	for _, ref := range refs {
		_ = entity.AddGrant(system, ref, entity.Grant{
			At: ref.Segs(),
			To: map[entity.Level][]string{entity.ReadWrite: {by}, entity.Control: {by}},
		})
	}
}

// reconnectAttempts bounds how long a mirror keeps trying to reopen a stream that keeps
// dying. With the backoff below it is a few minutes — long enough to outlast a runner
// restart, short enough that a task on a runner that is never coming back does not hold a
// goroutine and a retry loop forever. A stream that delivers anything at all resets it.
const reconnectAttempts = 40

// backoff grows to a ceiling and stays there. A mirror reconnecting to a runner that is down
// should not hammer it on the way.
func backoff(attempt int) time.Duration {
	d := time.Duration(1<<min(attempt, 5)) * 250 * time.Millisecond
	return min(d, 8*time.Second)
}

// drainInto projects one subscription's frames, reporting whether the TASK ended (true) or
// merely the stream did (false).
//
// WHO WRITES. The task's record is written as the party working the task: the spawn's
// principal until a worker claims it, and the worker from then on — the claimant, named by the
// runner on the claim's running frame, and remembered across a reconnect in claimant. A
// question is posed and settled as the docket (entity.SystemDocket) whoever is working the task,
// since versed poses it and the decision that settles it travelled by the answer path, where the
// answerer was checked; the worker's grant is on its task and must reach no question, and a
// projection written as the worker would pass exactly the check that grant is meant to fail. The
// worker's grant at the claim is the mirror's own act (entity.SystemMirror).
//
// WHO A FRAME SAYS DID SOMETHING IS NOT THE FRAME'S TO SAY, where the frame came from a worker.
// A hosted task's frames are made in this process, by routes that checked their actor, and are
// trusted. A relayed task's — a remote runner's — are whatever the worker
// wrote, and a `by` on one is a name the worker chose: a decision "by" the answerer a gate is
// held out to, a slide "by" the docket. AND WHAT WAS DECIDED IS NOT THE FRAME'S EITHER, where
// verse already holds it: an answer the route accepted is on the question, verdict, ballot and
// all, and a worker reporting another verdict under it would leave a record saying the
// answerer chose what they did not. So a relayed task's decision is settled from what verse
// holds (settleRelayed), and its slide named from what verse relayed (slidBy).
func drainInto(store entity.Store, id string, st entity.Obj, m Meta, h task.Handle, ch <-chan task.Event, claimant *string) (ended, heard bool) {
	docket, projector := asPrincipal(store, entity.SystemDocket), asPrincipal(store, entity.SystemMirror)
	relayed := m.Runner != "" && m.Runner != LocalRunner
	if *claimant != "" {
		store = asPrincipal(store, *claimant)
	}
	{
		for ev := range ch {
			if !heard {
				// The first frame of a resumed stream is the only proof it is working, and
				// nothing else can stand in for it — a channel that is merely open may be
				// one that is about to close.
				heard = true
				if st["observed"] == false {
					// Gone from the document, because the document is what gets stored. This
					// said `nil, not delete: a write MERGES`, which was true when a bare null
					// meant removal — since baa0c459 it is data, so it was writing
					// `observed: null` where it meant to write nothing at all.
					delete(st, "observed")
					project(store, id, st, m)
				}
			}
			if ev.Cursor > 0 { // high-water for resume: verse re-subscribes ?since=cursor
				st["cursor"] = ev.Cursor
			}
			switch ev.Kind {
			case task.EventPending: // now blocked on a gate — visible to other tasks
				// A REF to the question rather than a copy of it. The question is its own
				// entity (see question.go), so the gate name, mode, tool and derived effects
				// live there; duplicating any of them here would be a second spelling of one
				// fact, and the entity is an index.
				st["pending"] = QuestionRef(ev.ID).String()
				// A frame carries no subject — the deriver that knew it ran on another
				// machine — so it comes from what this mirror already holds: the run's flow.
				subject := entity.Str(st.Payload(), "flow")
				var answerers []string
				var labels map[string]string
				var then, els entity.Payload
				ev, answerers, labels, then, els = asPosed(docket, m, ev)
				posed := PostFrameQuestion(docket, ev, id, subject, answerers, labels, then, els)
				// The clock, if the frame set one: the question is the worker's, but the
				// timer that settles it runs where the record is kept.
				if posed && ev.Deadline != nil && ev.Deadline.Within != "" && m.Arm != nil {
					m.Arm(ev.ID, ev.Deadline.Within)
				}
			case task.EventDecision:
				// nil, not delete: a write MERGES, so a field left out of the payload stays
				// as it was. Saying "this is gone" is the writer's job, and it is the whole
				// difference between a gate that clears and a task that looks blocked
				// forever. (Under whole-value replace this was `delete` and read the same.)
				// Settle the record only if this decision ANSWERED a standing question.
				// A step the autonomy allowed outright reports a decision without ever having
				// been pending, and settling that would mint a `gate:question:` entity for a
				// question nobody was ever asked.
				// AND ONLY THE QUESTION THIS TASK IS WAITING ON. A frame names the question it
				// decides, and one naming another task's would settle that one.
				if st["pending"] == QuestionRef(ev.ID).String() {
					res := task.Resolution{Choice: ev.Choice, Proceed: ev.Proceed, By: ev.By, Note: ev.Text}
					if relayed {
						settleRelayed(docket, ev.ID, res, *claimant)
					} else {
						AnswerQuestion(docket, ev.ID, res)
					}
				}
				delete(st, "pending") // see write: a replace stores st, so absence is absence
				st["lastAction"] = ev.Tool
			case task.EventExec:
				st["lastAction"] = ev.Tool
				if ev.Text != "" { // the last action's output (already a bounded summary)
					st["lastOutput"] = ev.Text
				}
			case task.EventSay: // an utterance; only the task's OWN words are the preview
				if ev.By == "" { // By set = a say-to folded in, not the agent's narration
					st["say"] = preview(ev.Text)
				}
				if ev.ID != "" { // words said to it, which it has now taken: that ask is heard
					HearSay(docket, ev.ID, id)
				}
			case task.EventStatus:
				// paused/resumed ride the same channel as lifecycle status but are
				// transient control acks, not a lifecycle state — project them as a
				// `paused` flag so `status` stays running→completed/cancelled.
				switch ev.Text {
				case "paused":
					st["paused"] = true
				case "resumed":
					st["paused"] = false
				default:
					// A CLAIM: a worker took the task and named itself. The grant a spawn makes
					// to its runner is written now, naming the party that actually turned up,
					// and every report from here on is written as them.
					if ev.Text == string(task.Running) && mayClaim(ev.By) && *claimant == "" {
						claimed(projector, id, st, ev.By)
						*claimant = ev.By
						store = asPrincipal(store, ev.By)
					}
					st["status"] = ev.Text
					// Started: the runner has resolved the agent, so the record says what runs
					// in place of what was asked.
					if ev.Text == string(task.Running) {
						resolved(h, st)
					}
					if task.Status(ev.Text).Terminal() {
						st["ended"] = nowStamp()
						if r := h.Result(); r != "" { // keep the full final answer, durably
							st["result"] = r
							if d := ParseResult(r); d != nil {
								st["data"] = d
							}
						}
						// WHY it ended, which the status alone never says: the frame's own word
						// first, since the plane is what this record mirrors, and the handle's
						// account only where the frame carried none. The handle's is a second
						// read against the runner, and a runner whose snapshot has less to say
						// than its stream did, or that cannot be asked just now, was losing the
						// reason between the frame and the record.
						e := ev.Err
						if e == "" {
							e = h.Err()
						}
						if e != "" {
							st["err"] = e
						}
						// What its proxy turned away, by destination and how often: the runner's
						// account, which no frame carries.
						if r := h.Refused(); len(r) > 0 {
							st["refused"] = r
						}
						// And which agent ran and with what, the runner's defaults filled in,
						// said by the runner that ran it.
						resolved(h, st)
						project(store, id, st, m)
						// And the run's own record, which the next run does not overwrite.
						writeRun(store, id, occurrenceOf(st), st)
						// Whatever it was still asking, it will never hear the answer to.
						// A relayed task may end with an EventStatus and no EventDecision, so
						// nothing else would ever settle those questions and the docket
						// would keep offering them for a run that is over.
						OrphanQuestions(docket, id)
						return true, heard // the TASK ended; the entity remains, terminal
					}
				}
				project(store, id, st, m)
				continue
			case task.EventAutonomy: // the policy replaced — project it so `entity get` shows it
				by := ev.By
				if relayed {
					by = slidBy(m, ev.Text, *claimant)
				}
				if by != "" {
					st["autonomyBy"] = by
				}
				// The autonomy as it now stands, verse's part with it, which the frame does not
				// carry: the slide's route set it before relaying it.
				if p := m.autonomy(); len(p) > 0 {
					st["autonomy"] = p
				} else {
					st["autonomy"] = entity.Removed()
				}
			default: // boundary / steer / child / gap: not projected (index, not warehouse)
				continue
			}
			project(store, id, st, m) // the payload is cloned on the way out; safe to reuse st
		}
	}
	return false, heard
}

// write projects the accumulated state onto the task's entity. A failed write is
// DROPPED, deliberately and here rather than invisibly at each call: Reflect runs on the
// task's own event goroutine with nobody to return to, and a task must not stall
// because its index could not be updated. What it costs is that the entity can lag the
// task it mirrors — the index is a projection, and the read plane remains the record.
// occurrenceOf reads the revision a record is about, or 0 when it does not say.
func occurrenceOf(st entity.Obj) int64 {
	if v, ok := st["occurrence"].(int64); ok {
		return v
	}
	return 0
}

// OccurrenceOf is occurrenceOf for a record READ BACK — the payload rather than the object
// on its way in. Same field, same answer, and 0 when the record does not say.
//
// It is exported because a completion hook needs it: the revision a run is about is the
// commit that caused the firing the run belongs to, which is the revision the hook's own
// references resolve at (Engine.applyHook). Reading it off the record rather than carrying
// it in memory is what makes that work for a task resumed after a restart, whose engine
// never saw the firing.
func OccurrenceOf(p entity.Payload) int64 {
	n := ir.Uncomment(entity.Get(p, "occurrence"))
	if n == nil || n.Type != ir.NumberType || n.Int64 == nil {
		return 0
	}
	return *n.Int64
}

// nowStamp is the one spelling of an instant on a task record. UTC, because a reader
// comparing two tasks should not have to know where each runtime was standing.
func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

// write puts the record OVER whatever stands at its address — a REPLACE, not a merge — and
// answers the commit it landed at, or 0 if it did not land.
//
// Replacing at every write rather than only at a run's first one, because st is the whole
// document: the drain loop mutates it and writes it entire, so a merge was never adding
// anything a replace does not carry. What it was adding was fields from BEFORE — an id names
// the work, so a new run writes where the last one left off, and `started`/`status` landed on
// top of `ended`/`err`/`result` without displacing them. That is a record saying it began at
// 20:42 and ended at 20:41, which staging produced.
//
// Doing it at one write would have been a rule to remember; doing it at every write makes a
// record that describes two runs UNCONSTRUCTIBLE, which is the difference between a fix and a
// discipline. It also means the mirror owns this address outright: nothing else writes a live
// task record (the engine's hook annotation lands after the terminal, and reconcile runs when
// no mirror is draining), so there is nothing here for a merge to preserve.
//
// A field is removed by DELETING IT FROM st, never by writing entity.Removed(): under a
// replace the whole document is stored as data, so a `!delete` marker would be kept as a value
// rather than executed as an instruction.
func write(store entity.Store, id string, st entity.Obj, at ...entity.Ref) int64 {
	ref := Ref(id)
	if len(at) == 1 {
		ref = at[0]
	}
	_, rev, err := store.Write(entity.Write{At: ref.At(), Value: entity.Raw(st.Payload())})
	if err != nil {
		return 0
	}
	return rev
}

// project is write for a live task's record, with who holds the task asked as it is written:
// the record is replaced whole, so a hold not carried on every write is a hold forgotten.
func project(store entity.Store, id string, st entity.Obj, m Meta) int64 {
	if m.HeldBy != nil {
		if by := m.HeldBy(); len(by) > 0 {
			st[HeldByField] = by
		} else {
			delete(st, HeldByField)
		}
	}
	return write(store, id, st)
}

// ParseResult is the structured face of a driver's final answer: the answer document
// projected beside the text as `data` so a charter can deref into it ($data.severity)
// instead of pattern-matching prose. nil when the answer is not structured, and `result`
// always carries the text either way — the two are one value with two faces, not
// alternatives.
//
// It accepts only an object or a list, for the reason answer.Extract gives: a bare word
// parses fine as a tony string, so without that gate `data` would be present for practically
// every answer and would signal nothing.
//
// This is what makes a reference the declaration: a charter that derefs $data.severity has
// said it expects structure, and gets a loud failure when the agent returned prose instead —
// no schema to declare up front, and no reliance on the agent reporting its own failure
// honestly. A spawn that wants that failure moved EARLIER, onto the task rather than onto
// the patch, declares an answer contract (launch.Spec.Answer) — and the two agree by
// construction, because the contract validates exactly the document this returns.
//
// It is exported because the mirror is not the only writer that has to answer this question:
// the trigger engine synthesizes a completed task's payload when it beats the mirror to
// the terminal (see trigger.Engine.taskEvent), and if the two derived `data` differently a
// hook would deref against a different document than the one the store ends up holding. The
// answer contract's check is a third such reader, which is why the extraction itself lives
// in one place rather than being written out again here.
// It comes back AS DATA, escaped, and that is not a detail of this function — it is the
// whole reason a projection may hold an agent's document at all.
//
// Extract parses the final message AS TONY, so whatever the agent wrote is what comes back,
// tags included. An agent asked to satisfy a contract written in `!or` and `!irtype` echoes
// that shape often enough — it is being shown the pattern and asked for something like it —
// and the result is a document full of merge operators nobody wrote deliberately. Stored
// unescaped, those are executed rather than kept, and the damage is not local: the operator
// lands in the log, every later read replays the log, and from then on reading ANY entity
// fails with "irtype patching null gave cannot patch with irtype operation" while writes
// keep succeeding. That is a store that cannot be read and cannot be repaired, which is a
// staging deployment lost twice in one day (see
// entity.TestAnOperatorTagInAPayloadDoesNotPoisonTheStore).
//
// Here rather than at the two writers, because both of them store what this returns and
// there is nothing else to do with it — this function's whole job is to hand the store a
// document, so handing it a storable one is the job. For the ordinary answer it is a no-op:
// AsData escapes tagged values, and a document with no tags has none.
func ParseResult(text string) entity.Payload {
	n := answer.Extract(text)
	if n == nil {
		return nil
	}
	return entity.AsData(n)
}

// preview bounds a narration line for the index; the full text is on the read plane
// (and the full final answer is projected separately as "result").
func preview(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// ClaimKind is the kind a run CLAIM lives under: task:claim:<chain>@<subject>. It shares
// the task system deliberately — the engine keeps that system out of the default trigger
// match set, so writing a claim cannot wake the very trigger that took it. A claim is
// machinery, and the ignition guard is where machinery belongs.
const ClaimKind = "claim"

// ClaimRef is the entity a run is claimed at. The id is the spawn id, so the claim and the
// task it guards share one address — there is nothing to keep in step.
func ClaimRef(id string) entity.Ref { return entity.VerseRef(System, ClaimKind, id) }

// IsTask and IsTaskish answer "is this address one of mine" — the question that used to be
// asked as `ref.System == mirror.System`, and cannot be any more.
//
// The first segment of a verse-owned address is the reserved root, so a comparison against
// `System` alone would be true of a `task:instance:x` in the world's half — an entity anybody
// may write — and the caller would be reading somebody's fixture as the mirror of a running
// task. Each namespace answers for itself, here, where the address is built.
func IsTask(ref entity.Ref) bool { return ref.ChildOf(entity.VerseRoot, System, InstanceKind) }

// IsTaskish is the whole task namespace: an instance, its run record, its claim.
func IsTaskish(ref entity.Ref) bool { return ref.Under(entity.VerseRoot, System) }

// gitOf is a spawn's repositories as the record carries them, in the words the spawn used
// (`repo`, `from`), so a reader of the record and a reader of the charter see one vocabulary.
func gitOf(repos []runner.Repo) []any {
	out := make([]any, 0, len(repos))
	for _, r := range repos {
		o := entity.Obj{"repo": r.Repo}
		if r.From != "" {
			o["from"] = r.From
		}
		out = append(out, o)
	}
	return out
}
