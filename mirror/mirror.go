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
	"strings"
	"time"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/entity"
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
func Ref(id string) entity.Ref { return entity.Ref{System: System, Kind: InstanceKind, ID: id} }

// Meta is the provenance Reflect publishes alongside a task's own state: where it runs
// and what it is about. Grouped rather than passed as four positional strings, which is
// both unreadable at the call site and easy to transpose.
type Meta struct {
	// Subject is the flow this task belongs to. It is carried on the entity as "flow"
	// so a completion trigger can advance that subject when the task finishes, and does
	// not collide with the $subject binding (the matched ref).
	Subject string
	// Runner is the task-control backend hosting it — "local", "broker", or a remote URL — so a
	// restart can decide resume-or-orphan.
	Runner string
	// RunnerName is the runner ENTITY a spawn named — `runners:<kind>:<name>` — when it named
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
	// Chain is the kpath of the action that spawned it, inside its trigger: "review",
	// "review.onDone". Empty for a task with no trigger behind it (`verse task run`).
	Chain string
	// Prompt is what the task was asked to do, already bound. Small, and the task's
	// identity to a reader — so it belongs on the index next to what the task produced,
	// not only on the read plane that outlives nothing.
	Prompt string
	// Doer, Grant and Tools are the ENVELOPE the task runs under: which doer a governed
	// selection picked, and what the resulting launch may do.
	//
	// They are published for every spawn, not only voted ones, because the surface that would
	// otherwise carry them does not exist for most of them. `select: auto` opens no motion at
	// all — deliberately, since an auto pick has no voter and casts no ballot — so a doer
	// chosen that way appeared on no docket, and the task entity showed only a driver that
	// two different doers can share. A reader asking "what was this allowed to do" had
	// nowhere to look.
	//
	// Grant is the NAME a charter or a provider wrote ("read-only"), not the resolved
	// predicates: the name is the vocabulary a person reads, writes and greps for, and the
	// predicates are already what crosses to the runtime. Empty Tools means the driver's full
	// set, exactly as on the spec.
	Doer  string
	Grant string
	Tools []string
	// Resolver is the binding a spawn NAMED to answer this run's gates, published on the
	// entity so "who is watching this" is readable beside what the work may do.
	Resolver string
	// Owner answers "which standing binding covers this subject", for a question the spawn
	// named nobody for. It is a callback rather than a call because mirror must not import
	// the resolver package: resolver reads the charter's match vocabulary, trigger reads
	// mirror, and the cycle closes. So the party that knows all three supplies the lookup.
	//
	// Nil means no standing layer is consulted, which is what a caller with no store view
	// of bindings should pass.
	//
	// TODO: cut the cycle rather than routing around it, before the resolver work
	// multiplies this. Two symptoms already: an optional field three call sites build by
	// hand lost the standing layer for resumed tasks in the commit that added it (see
	// versed.Server.reflect), and the resolver system and instance kind are spelled here as
	// private constants because the real ones cannot be imported — one fact, two spellings.
	//
	// The edge to cut is resolver → trigger, not trigger → mirror. Trigger uses Reflect,
	// Meta and ParseResult, which is the projection itself and not addressing, so no
	// extraction of refs into a leaf package would help; it would move a constant and leave
	// the dependency. Resolver imports trigger for exactly one thing — Match, the entity
	// selector — which delegates to entity.Pattern and is called "the atom of the trigger
	// model" while being a selector over entities that trigger happens to own. Lifting it
	// out is the change that is right on its own merits, and it makes mirror → resolver
	// legal as a side effect.
	Owner func(subject string) string
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
func Reflect(store entity.Store, id string, h task.Handle, m Meta) {
	ch, unsub := h.Subscribe()
	// A brokered task starts in the pre-running `unclaimed` state — it is hosted on
	// the broker awaiting an executor; the claim emits an EvStatus{running} that moves it
	// on. Local/remote tasks execute immediately, so they start `running`.
	initial := task.Running
	if m.Runner == "broker" {
		initial = task.Unclaimed
	}
	// WHEN, and only twice: `started` here, `ended` at the terminal status below. A record
	// stamped on every update would be an entity that changes whenever anything narrates,
	// which wakes every trigger watching this slice for no fact anybody wrote — and the two
	// instants somebody actually asks about are the two ends. RFC3339, the same spelling
	// `verse.meta.vanishing` and a source's `lastReflect` use.
	st := entity.Obj{"status": string(initial), "started": nowStamp()}
	if m.Subject != "" {
		st["flow"] = m.Subject
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
	// The envelope: who was picked, and what the launch may do. See Meta.
	if m.Doer != "" {
		st["doer"] = m.Doer
	}
	if m.Grant != "" {
		st["grant"] = m.Grant
	}
	if len(m.Tools) > 0 {
		st["tools"] = m.Tools
	}
	if m.Resolver != "" {
		st["resolver"] = m.Resolver
	}
	// A dial-in runner (broker) has a worker-facing base URL distinct from how verse
	// controls it: publish it so a worker can be told where to dial in (the mint→publish→
	// dial loop) rather than being pre-configured. Local/remote-taskd handles have none.
	if d, ok := h.(interface{ DialURL() string }); ok {
		if u := d.DialURL(); u != "" {
			st["dialURL"] = u
		}
	}
	write(store, id, st)
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
		for {
			ended, heard := drainInto(store, id, st, m, h, ch)
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
			write(store, id, st)
			time.Sleep(backoff(attempt))
			attempt++
			since, _ := st["cursor"].(int64)
			ch, unsub = h.SubscribeSince(since)
		}
	}()
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
func drainInto(store entity.Store, id string, st entity.Obj, m Meta, h task.Handle, ch <-chan task.Frame) (ended, heard bool) {
	{
		for ev := range ch {
			if !heard {
				// The first frame of a resumed stream is the only proof it is working, and
				// nothing else can stand in for it — a channel that is merely open may be
				// one that is about to close.
				heard = true
				if st["observed"] == false {
					st["observed"] = nil // nil, not delete: a write MERGES
					write(store, id, st)
				}
			}
			if ev.Cursor > 0 { // high-water for resume: verse re-subscribes ?since=cursor
				st["cursor"] = ev.Cursor
			}
			switch ev.Kind {
			case task.EvPending: // now blocked on a gate — visible to other tasks
				// A REF to the question rather than a copy of it. The question is its own
				// entity (see question.go), so the gate name, mode, tool and derived effects
				// live there; duplicating any of them here would be a second spelling of one
				// fact, and the entity is an index.
				st["pending"] = QuestionRef(ev.ID).String()
				// A frame carries no subject and no owner — the deriver that knew them ran
				// on another machine — so they come from what this mirror already holds:
				// the run's flow, the binding its spawn named, and failing that the
				// standing layer covering the subject.
				subject := entity.Str(st.Payload(), "flow")
				owner := m.Resolver
				if owner == "" && subject != "" && m.Owner != nil {
					owner = m.Owner(subject)
				}
				PostFrameQuestion(store, ev, id, subject, owner)
			case task.EvDecision:
				// nil, not delete: a write MERGES, so a field left out of the payload stays
				// as it was. Saying "this is gone" is the writer's job, and it is the whole
				// difference between a gate that clears and a task that looks blocked
				// forever. (Under whole-value replace this was `delete` and read the same.)
				// Settle the record only if this decision ANSWERED a standing question.
				// A gate the vector allowed outright reports a decision without ever having
				// been pending, and settling that would mint a `gate:question:` entity for a
				// question nobody was ever asked.
				if st["pending"] != nil {
					AnswerQuestion(store, ev.ID, task.Resolution{Proceed: ev.Proceed, By: ev.By, Note: ev.Text})
				}
				st["pending"] = nil
				st["lastAction"] = ev.Tool
			case task.EvExec:
				st["lastAction"] = ev.Tool
				if ev.Text != "" { // the last action's output (already a bounded summary)
					st["lastOutput"] = ev.Text
				}
			case task.EvSay: // an utterance; only the task's OWN words are the preview
				if ev.By == "" { // By set = a say-to folded in, not the agent's narration
					st["say"] = preview(ev.Text)
				}
			case task.EvStatus:
				// paused/resumed ride the same channel as lifecycle status but are
				// transient control acks, not a lifecycle state — project them as a
				// `paused` flag so `status` stays running→completed/cancelled.
				switch ev.Text {
				case "paused":
					st["paused"] = true
				case "resumed":
					st["paused"] = false
				default:
					st["status"] = ev.Text
					if task.Status(ev.Text).Terminal() {
						st["ended"] = nowStamp()
						if r := h.Result(); r != "" { // keep the full final answer, durably
							st["result"] = r
							if d := ParseResult(r); d != nil {
								st["data"] = d
							}
						}
						if e := h.Err(); e != "" { // status says only THAT it ended, never why
							st["err"] = e
						}
						write(store, id, st)
						// Whatever it was still asking, it will never hear the answer to.
						// A brokered task ends with an EvStatus and no EvDecision, so
						// nothing else would ever settle those questions and the docket
						// would keep offering them for a run that is over.
						OrphanQuestions(store, id)
						return true, heard // the TASK ended; the entity remains, terminal
					}
				}
				write(store, id, st)
				continue
			case task.EvVector: // an autonomy slide — project the dial so `entity get` shows it
				st["vector"] = ev.Mode
				if ev.By != "" {
					st["vectorBy"] = ev.By
				}
			default: // boundary / steer / child / gap: not projected (index, not warehouse)
				continue
			}
			write(store, id, st) // the payload is cloned on the way out; safe to reuse st
		}
	}
	return false, heard
}

// BindResolver mints the record of a resolver watching one run: `resolvers:instance:<name>@<id>`.
//
// At SPAWN rather than at the first question, because a resolver reads context before the ask
// — that is the whole reason it is attached rather than handed a question — so the thing that
// says who is watching has to exist from the moment there is something to watch.
//
// It carries the binding and the origin as fields as well as in its address, because an
// address is for finding one and a field is for selecting many: "every instance of the
// security reviewer" is a predicate, not a glob.
func BindResolver(store entity.Store, binding, name, taskID string) {
	Watch(store, binding, name, Ref(taskID))
}

// Watch mints the record of one binding watching one origin, and does nothing if it already
// exists.
//
// AN INSTANCE FOLLOWS ITS ORIGIN, NOT ITS BINDING. "An instance lives for the duration of what
// it resolves, and the origin decides what that is" — so a binding watching a subject and
// three runs about it has four instances, each living exactly as long as the thing it watches.
// That is also what carrying a cursor pair means: two positions as two records rather than two
// fields, so there is no place for a merged order to be invented.
//
// Which plane an instance is on follows from its origin and is not stored beside it: an origin
// under `task:instance` is a run, whose position is a cursor, and anything else is a
// subject, whose position is a revision. One fact, one spelling.
//
// Create-only, and the precondition is the whole point rather than an optimisation: this is
// called every time a question is posed, so a plain write would reset the position of a
// resolver that is already reading — retiring its progress on the arrival of the very question
// it was reading towards.
func Watch(store entity.Store, binding, name string, origin entity.Ref) {
	if binding == "" || name == "" {
		return
	}
	ref := entity.Ref{System: resolverSystem, Kind: resolverInstance, ID: name + "@" + origin.ID}
	if origin.System != System {
		// A subject's id may not be unique across systems the way a task id is, so its
		// address carries the whole ref.
		ref.ID = name + "@" + origin.String()
	}
	_, _, _ = store.Write(entity.Write{
		At: ref.At(),
		Value: entity.Obj{
			"binding": binding,
			"origin":  origin.String(),
			// position is where this instance has read to, and starts before the first
			// frame. It is on the record rather than only in the reader because that is
			// what tells "has not decided" from "has not caught up".
			"position": int64(0),
		}.Payload(),
		When: []entity.Require{entity.Created()},
	})
}

// The resolver system, spelled here rather than imported: mirror is what every runtime writes
// through, and a dependency from it onto the resolver package would put the binding's own
// vocabulary underneath the index that merely records it.
//
// Which is the polite reading. The blunt one is that the import is a cycle, and this is one
// fact with two spellings until that is cut — see Meta.Owner.
const (
	resolverSystem   = "resolvers"
	resolverInstance = "instance"
)

// write projects the accumulated state onto the task's entity. A failed write is
// DROPPED, deliberately and here rather than invisibly at each call: Reflect runs on the
// task's own event goroutine with nobody to return to, and a task must not stall
// because its index could not be updated. What it costs is that the entity can lag the
// task it mirrors — the index is a projection, and the read plane remains the record.
// nowStamp is the one spelling of an instant on a task record. UTC, because a reader
// comparing two tasks should not have to know where each runtime was standing.
func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

func write(store entity.Store, id string, st entity.Obj, at ...entity.Ref) {
	ref := Ref(id)
	if len(at) == 1 {
		ref = at[0]
	}
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: st.Payload()}); err != nil {
		return
	}
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
func ParseResult(text string) entity.Payload {
	n := answer.Extract(text)
	if n == nil {
		return nil
	}
	return n
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
func ClaimRef(id string) entity.Ref { return entity.Ref{System: System, Kind: ClaimKind, ID: id} }
