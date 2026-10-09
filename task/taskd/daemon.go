// Package taskd hosts Tasks behind an HTTP control + read plane: spawn
// tasks, stream their events (SSE), resolve their gates by id, steer them,
// rebind their vector, and pause/cancel their subtrees — all over the wire, so
// autonomy is observed and adjusted live and remotely. Control ops are task-
// or subtree-scoped (never per-child); the read plane is drop-with-gap.
//
// It is the cleaned, renamed core of agentgated, with the LLM harness dependency
// severed: how a Task is built is injected as a Builder, so the daemon carries
// no coupling to any particular driver/runtime.
package taskd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
)

// Builder constructs a hosted task from a task.Launch — the id, the prompt, the
// requested driver (e.g. "scripted"/"llm"; "" = the builder's default), and the frame and
// tool set that shape the agent — plus the Registry that serves as its gate resolver and
// an optional ContentReader (nil = read from the local filesystem; non-nil = read
// federated files through the verse). It is injected so the daemon stays decoupled from
// any particular driver/runtime, and picks the driver per spawn.
type Builder func(l task.Launch, reg *task.Registry, content task.ContentReader) *task.Task

// Daemon hosts a set of running tasks.
type Daemon struct {
	mu    sync.Mutex
	tasks map[string]*hosted
	seq   atomic.Uint64
	build Builder
	log   io.Writer
	// onQuestion, when set, is called as each hosted task poses a gate question. The
	// daemon holds no store and should not: publishing a question is the job of whoever
	// owns one, and this is the seam that hands it over with the derived effects still
	// attached — the read plane's own EvPending frame carries only what an attached client
	// needs to see, which is less than a record needs to be matchable.
	//
	// THE LAUNCH is passed with it, because what a record needs beyond the question — where it
	// goes, and what it is about — is the launch's to say, and the launch is the one thing that
	// exists before the task does. The SCOPE because this daemon is one runtime shared by
	// baseline and every scoped engine, and a publisher that closed over a single store sent
	// every scope's questions to that one (git-issue kwxxm8cth12ksn99fhn0). The SUBJECT because
	// the only other place it is written is the task's own record, which lands after the task
	// is started, and a task can ask before it does (git-issue nkptp9b3h12ksx3kg9n0). The
	// daemon still holds no store and makes no decision about where anything goes — it
	// repeats what the launch said, and the publisher decides what that means.
	onQuestion func(l task.Launch, p task.Pending)
	onCheck    func(l task.Launch, q task.GateQuestion) error
	// onDrop is the counterpart, and is not optional in the way it looks. A question is
	// WITHDRAWN when its last waiter stops waiting — an intervention that redirects the run,
	// a pause, a deadline — and without a publisher for that half the record posted by
	// onQuestion above stays `pending` on the docket, offering a decision nothing is left to
	// receive. It was announced and never retracted, so `verse question` listed a withdrawn
	// question beside a live one with nothing to tell them apart, and answering it was a 404
	// (git-issue zdaqh9vkh12ksxneg9n0).
	//
	// It carries the scope for the reason onQuestion does: the retraction has to reach the
	// store the post went to, or a scope's question is published in one place and retired in
	// another.
	onDrop func(taskID, scope, questionID, why string)
}

// OnQuestion registers the publisher. Optional: taskd runs standalone in tests and under
// `verse task run` with nothing behind it.
func (d *Daemon) OnQuestion(f func(l task.Launch, p task.Pending)) { d.onQuestion = f }

// OnCheck registers what may refuse a hosted task's question before it is posed (Registry.OnCheck),
// told the task's launch as onQuestion is.
func (d *Daemon) OnCheck(f func(l task.Launch, q task.GateQuestion) error) { d.onCheck = f }

// OnDrop registers the publisher's other half — a question leaving the docket unanswered. A
// caller that registers one without the other publishes questions it never retires.
func (d *Daemon) OnDrop(f func(taskID, scope, questionID, why string)) { d.onDrop = f }

type hosted struct {
	task   *task.Task
	reg    *task.Registry
	prompt string
	// driver is retained, not just passed to build, because it is the only durable record
	// of WHO was picked when a spawn's doer came from a `provider` selection. The broker's
	// snapshot has always reported it; a locally-run task did not, so an auto-selected
	// doer was observable nowhere but a trace line.
	driver string
	// occurrence is which run this is (see task.Launch.Occurrence), retained so the snapshot
	// can say so: the process, and not only the entity, knows which run it is.
	occurrence int64
	// scope is the federation the task was launched in (task.Launch.Scope), retained so the
	// runtime can say whose a task is: one runtime hosts baseline's and every scope's, and a
	// control write from the wrong scope is refused by asking, not by reaching the task.
	scope string

	mu     sync.Mutex
	status task.Status
	reason string
	err    string
	result string // the task's final output (driver's last message), once completed
}

// done reports whether this run has finished — the question the spawn dedupe is actually
// asking. A live task of the same id is the SAME work and joins; a finished one is a previous
// run, and the next spawn is a new one.
func (h *hosted) done() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status.Terminal()
}

func (h *hosted) snapshot() task.Instance {
	h.mu.Lock()
	defer h.mu.Unlock()
	pend := h.reg.Pending()
	pending := make([]task.PendingGate, 0, len(pend))
	for _, p := range pend {
		pending = append(pending, task.PendingGate{
			ID: p.Question.ID, Origin: p.Question.Origin, Tool: p.Tool, Action: p.Action,
			Gate: p.Question.Gate, Mode: p.Question.Mode.String(), Reason: p.Question.Reason,
		})
	}
	return task.Instance{
		ID: h.task.ID, Prompt: h.prompt, Driver: h.driver, Status: h.status,
		Err: h.err, Result: h.result, Pending: pending, Occurrence: h.occurrence,
		Cursor: h.task.Cursor(), // resume the event stream with ?since=<cursor>
	}
}

// New returns a Daemon that builds hosted tasks with the given Builder.
func New(build Builder) *Daemon {
	return &Daemon{tasks: map[string]*hosted{}, build: build, log: os.Stderr}
}

// Handler returns the HTTP routes (Go 1.22 method+wildcard patterns).
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /tasks", d.spawn)
	mux.HandleFunc("GET /tasks", d.list)
	mux.HandleFunc("GET /tasks/{id}", d.get)
	mux.HandleFunc("GET /tasks/{id}/events", d.events)
	mux.HandleFunc("POST /tasks/{id}/resolve", d.resolve)
	mux.HandleFunc("POST /tasks/{id}/say", d.say)
	mux.HandleFunc("POST /tasks/{id}/cancel", d.control("cancel"))
	mux.HandleFunc("POST /tasks/{id}/pause", d.control("pause"))
	mux.HandleFunc("POST /tasks/{id}/resume", d.control("resume"))
	mux.HandleFunc("PUT /tasks/{id}/vector", d.setVector)
	return mux
}

func (d *Daemon) lookup(r *http.Request) *hosted {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tasks[r.PathValue("id")]
}

// Lookup returns the live hosted task for id, or nil — the local re-attach path.
func (d *Daemon) Lookup(id string) *task.Task {
	d.mu.Lock()
	defer d.mu.Unlock()
	if h := d.tasks[id]; h != nil {
		return h.task
	}
	return nil
}

// Spawn builds, hosts, and runs a task for the given prompt — the programmatic
// seam the engine's Spawner uses, so a trigger-spawned task is hosted and
// controllable (list/get/attach/cancel) exactly like a `verse task run` one.
// Returns the running task (already Run in the background).
func (d *Daemon) Spawn(l task.Launch, content task.ContentReader) *task.Task {
	h, _ := d.spawnHosted(l, content)
	return h.task
}

// spawnHosted builds, registers, wires the read plane, and runs a task. Shared
// by Spawn and the HTTP spawn handler. The bool reports whether it BUILT one; false means
// the id was already hosted and that task is what came back.
//
// An id is spawned ONCE. A second spawn of one that is already hosted returns the existing
// task rather than building beside it — because the alternative is not two tasks but
// one lost one: registration is a map assignment, so the newcomer took the id and the
// original kept running with nothing able to reach it, address it, or stop it.
//
// That makes the spawn safe to RETRY, which is the point. A caller whose spawn request
// timed out cannot know whether the far side acted, and until now its only options were to
// abandon real work or to destroy it.
func (d *Daemon) spawnHosted(l task.Launch, content task.ContentReader) (*hosted, bool) {
	d.mu.Lock()
	if h, ok := d.tasks[l.ID]; ok && !h.done() {
		d.mu.Unlock()
		return h, false
	}
	d.mu.Unlock()

	reg := task.NewRegistry()
	tk := d.build(l, reg, content)

	// Install the spawn's authority axes here rather than in the Builder, because they are
	// properties of the RUNTIME's contract with verse, not of any driver: a scripted
	// task and an LLM one are bounded the same way, and a Builder that forgot to apply
	// them would silently hand back an unbounded task. Both are conditional — a spawn
	// that named neither gets exactly what it got before (wildcard scope, the builder's
	// own vector), so this narrows only what a caller asked to have narrowed.
	if l.Grant != nil {
		tk.SetScope(*l.Grant)
	}
	if l.Vector != nil {
		tk.SetVector(l.Vector, "launch")
	}
	tk.SetRetain(l.Retain) // how much of its read plane a resuming reader can still see
	if l.Converse {
		tk.SetConverse(reg) // the person's turn is a question on this task's own docket
	}
	h := &hosted{task: tk, reg: reg, prompt: l.Prompt, driver: l.Driver,
		occurrence: l.Occurrence, scope: l.Scope, status: task.Running}

	// Surface pending decisions on the same read plane as the loop's events, so an
	// attached client sees what a gate is blocked on and can resolve it by id.
	reg.OnCheck(func(q task.GateQuestion) error {
		if d.onCheck != nil {
			return d.onCheck(l, q)
		}
		return nil
	})
	reg.OnPost(func(p task.Pending) {
		if d.onQuestion != nil {
			d.onQuestion(l, p)
		}
		// Whom it asks crosses too: verse poses a remote task's question from this frame, and a
		// turn or an ask naming $principal is the task's person's, not the docket's.
		tk.Emit(task.Frame{
			Kind: task.EvPending, ID: p.Question.ID, Tool: p.Tool, Action: p.Action,
			Gate: p.Question.Gate, Mode: p.Question.Mode.String(), Text: p.Question.Reason,
			Options: p.Question.Options, Deadline: p.Question.Deadline, Answerers: p.Question.Answerers,
		})
	})
	// And the counterpart, on the SAME registry. This is the one that was missing: the
	// registry's own drop wiring works and is tested, and a hosted task's questions simply had
	// nobody registered to hear it.
	reg.OnDrop(func(q task.GateQuestion, why string) {
		if d.onDrop != nil {
			d.onDrop(l.ID, l.Scope, q.ID, why)
		}
	})

	// Re-checked under the lock: two concurrent spawns of one id race the check above, and
	// the loser must not start a second loop. Whoever registered first is the task.
	//
	// A TERMINATED ONE IS NOT A WINNER OF THAT RACE. Nothing reaps this map, so a task that
	// finished stayed in it forever and every later spawn of the same id was answered with it:
	// the call returned instantly, carrying the FIRST run's answer, with the second run's
	// prompt on the record beside it. The model was never called, and a completion hook then
	// ran on the replayed answer and advanced the flow as though the work had been done
	// (git-issue 6j911yhzh12kswgjgsn0).
	//
	// The id is stable by design — it is what makes a task addressable and what stops a re-fire
	// spawning twice — so any rule whose subject OUTLIVES one task (a cursor, a queue head,
	// anything that walks) got one real run and then unbounded silent no-ops that each looked
	// like success. The dedupe is meant to be against a task that is still running, which is
	// what "the engine dedups a spawn against a live task" says everywhere else; this is the
	// one place that read it as "a task that ever existed".
	d.mu.Lock()
	if existing, ok := d.tasks[l.ID]; ok && !existing.done() {
		d.mu.Unlock()
		return existing, false
	}
	d.tasks[l.ID] = h
	d.mu.Unlock()

	go func() {
		// A hosted task outlives its spawn call, so it must NOT derive from a
		// request context. Its own subtree cancel (task.Cancel, via POST /cancel) is
		// the stop path.
		out, err := tk.Run(context.Background())
		h.mu.Lock()
		h.reason, h.status = out.Reason, task.Status(out.Reason)
		h.result = tk.Result() // the driver's final answer, if any
		// AN ERROR IS A CAUSE, NOT A STATUS, and conflating the two made every cancel read as a
		// failure. Run returns the run's cause alongside its outcome — that is how a cancel's
		// reason reaches a caller at all — so `err != nil` is true for a task somebody stopped
		// on purpose, and overwriting the status with Failed threw away what the machine had
		// just decided (exit(Cancelled), emitted as its terminal frame).
		//
		// It is not cosmetic: verse distinguishes the two everywhere it matters. A charter's
		// onCancelled and onFailed are different actions, and verse-dev's merge train says
		// why in its own words — a cancel decided nothing, so the gate is asked again,
		// where a failed run must not re-ask anybody anything. With this, a cancelled run fired
		// onFailed and the question was never re-posed.
		//
		// So the error is recorded and the terminal status stands. Failed remains the fallback
		// for a run that ended with an error and NO terminal status of its own — a loop that
		// broke rather than one that ended.
		if err != nil {
			h.err = err.Error()
			if !h.status.Terminal() {
				h.status = task.Failed
			}
		}
		h.mu.Unlock()
	}()
	return h, true
}

func (d *Daemon) spawn(w http.ResponseWriter, r *http.Request) {
	// The launch as the contract declares it (task.SpawnRequest), which is what the caller
	// rendered: a field this end had its own name for is a field the two could disagree on.
	var body task.SpawnRequest
	// A BODY THAT DOES NOT PARSE IS REFUSED. Spawned anyway it is a task with no prompt under
	// the id of nothing, answered `201`, and the caller is told its work is running. No body
	// at all is a launch that says nothing, which is a spawn of the runner's defaults.
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, fmt.Sprintf("the spawn does not parse: %v", err), http.StatusBadRequest)
		return
	}
	l := body.Launch()
	// a runner client (the engine) passes its own id so it matches the task:instance entity;
	// an ad-hoc spawn gets a fresh one
	if l.ID == "" {
		l.ID = fmt.Sprintf("p%d", d.seq.Add(1))
	}
	// THE WHOLE LAUNCH reaches the builder. Everything that shapes or bounds the agent rides
	// the wire, so a task hosted on a REMOTE runner is framed and bounded exactly as a local
	// one — the seam is complete or the guarantee is local-only. The vector and grant arrive
	// as resolved predicates rather than names for the reason task.Launch gives: the
	// vocabulary lives at one end, so the two cannot drift apart.
	h, created := d.spawnHosted(l, nil)
	// 201 built it, 200 found it already running. A retrying client needs no more than
	// that, and a client that does not care can ignore it: both mean "this id is running".
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeOut(w, r, status, task.Spawned{ID: h.task.ID})
}

// Count returns the number of hosted tasks (live and completed-but-unreaped).
func (d *Daemon) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.tasks)
}

func (d *Daemon) list(w http.ResponseWriter, r *http.Request) {
	writeOut(w, r, http.StatusOK, d.Snapshots())
}

// Snapshots returns every hosted task in the shape the task API returns — for versed
// to union the local runtime with a runner's own tasks (e.g. a broker's) in one list.
func (d *Daemon) Snapshots() []task.Instance {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]task.Instance, 0, len(d.tasks))
	for _, h := range d.tasks {
		out = append(out, h.snapshot())
	}
	return out
}

// ScopeOf is the scope a hosted task was launched in — "" for baseline — and whether id is
// hosted here at all.
func (d *Daemon) ScopeOf(id string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := d.tasks[id]
	if h == nil {
		return "", false
	}
	return h.scope, true
}

// SnapshotOf returns one hosted task's snapshot, or false if this daemon doesn't host it.
func (d *Daemon) SnapshotOf(id string) (task.Instance, bool) {
	d.mu.Lock()
	h := d.tasks[id]
	d.mu.Unlock()
	if h == nil {
		return task.Instance{}, false
	}
	return h.snapshot(), true
}

func (d *Daemon) get(w http.ResponseWriter, r *http.Request) {
	h := d.lookup(r)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	writeOut(w, r, http.StatusOK, h.snapshot())
}

func (d *Daemon) events(w http.ResponseWriter, r *http.Request) {
	h := d.lookup(r)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	sse, err := codec.NewSSE(w, r)
	if err != nil {
		codec.HTTPError(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	var ch <-chan task.Frame
	var unsub func()
	if s := r.URL.Query().Get("since"); s != "" {
		// Resumable: replay the retained window after the client's cursor, then live.
		// The ring already carries any EvPending, so no separate gate replay is needed.
		since, _ := strconv.ParseInt(s, 10, 64)
		ch, unsub = h.task.SubscribeSince(since)
	} else {
		// Live attach from now: replay currently-blocked gates so it knows what to resolve.
		// A finished task needs no special case here — the bus hands a late subscriber
		// the terminal and closes, so an attach after completion exits rather than blocking
		// on a stream that will never emit again.
		ch, unsub = h.task.Subscribe()
		for _, p := range h.reg.Pending() {
			_ = sse.Send(task.Frame{
				Kind: task.EvPending, ID: p.Question.ID, Tool: p.Tool, Action: p.Action,
				Gate: p.Question.Gate, Mode: p.Question.Mode.String(), Text: p.Question.Reason,
				Options: p.Question.Options, Deadline: p.Question.Deadline, Answerers: p.Question.Answerers,
			})
		}
	}
	defer unsub()
	sse.Flush()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			_ = sse.Send(ev)
		case <-r.Context().Done():
			return
		}
	}
}

// Resolve answers a hosted task's pending decision by id — the Go entry versed's unified
// gate surface uses to resolve a task gate (write/shell/delegate) the same way it resolves
// an engine gate. Reports whether the decision matched.
func (d *Daemon) Resolve(taskID, decisionID string, res task.Resolution) bool {
	d.mu.Lock()
	h := d.tasks[taskID]
	d.mu.Unlock()
	return h != nil && h.reg.Answer(decisionID, res)
}

func (d *Daemon) resolve(w http.ResponseWriter, r *http.Request) {
	h := d.lookup(r)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	// Spellings of one answer, because this face predates api.Resolve and things dial in
	// against it. `id`/`approve` is what every other answering surface speaks; `decisionId`
	// and `proceed` are the runtime's own older names, still read so a client written against
	// them is not broken by the newer spelling arriving. The contract as documented in
	// task-control.md mixes them — `{id, proceed}` — so both halves are read independently
	// rather than as two fixed vocabularies.
	//
	// The verdict is the OR, and it is not lossy: a yes is `true` in whichever field the
	// client used, and a no is `false` in both AND absent from both. There is no answer the
	// two fields can express that this reads wrongly, short of a client setting them against
	// each other on purpose.
	var body struct {
		ID      string `tony:"field=id"`
		Choice  string `tony:"field=choice"`
		Approve bool   `tony:"field=approve"`

		DecisionID string   `tony:"field=decisionId"`
		Proceed    bool     `tony:"field=proceed"`
		ChildMode  string   `tony:"field=childMode"`
		By         string   `tony:"field=by"`  // who answered; empty = an unattributed caller
		Say        []string `tony:"field=say"` // words to say on resolution (a gate that informs)
	}
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	decision := body.ID
	if decision == "" {
		decision = body.DecisionID
	}
	proceed := body.Approve || body.Proceed || body.Choice == task.Proceed
	res := task.Resolution{
		Choice: body.Choice, Proceed: proceed, By: body.By,
		ChildMode: parseChildMode(body.ChildMode), Steer: body.Say,
	}
	if !h.reg.Answer(decision, res) {
		http.Error(w, "unknown or already-answered decision", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Daemon) say(w http.ResponseWriter, r *http.Request) {
	h := d.lookup(r)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	var body task.SayRequest
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, fmt.Sprintf("the say does not parse: %v", err), http.StatusBadRequest)
		return
	}
	if body.Instruction == "" {
		http.Error(w, "a say carries an `instruction`", http.StatusBadRequest)
		return
	}
	// Empty `by` is an unattributed caller, said as such. An ended task hears nothing, and says so.
	if err := h.task.Steer(body.Instruction, body.By, body.Ask); err != nil {
		http.Error(w, err.Error(), http.StatusGone)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cancelledHere is the reason a cancel gives when its caller gave none.
const cancelledHere = "cancelled via daemon"

// control returns a handler for the subtree ops. A cancel carries why (task.CancelRequest);
// pause and resume carry nothing.
func (d *Daemon) control(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := d.lookup(r)
		if h == nil {
			http.NotFound(w, r)
			return
		}
		switch op {
		case "cancel":
			// The reason is what the task's charters see on `onCancelled`. A cancel with no
			// body is a cancel all the same; one whose body does not parse is refused, since
			// the reason it meant to give is not known and the task would end under another.
			var body task.CancelRequest
			if err := codec.Read(r, &body); err != nil {
				http.Error(w, fmt.Sprintf("the cancel does not parse: %v", err), http.StatusBadRequest)
				return
			}
			if body.Reason == "" {
				body.Reason = cancelledHere
			}
			h.task.Cancel(body.Reason)
		case "pause":
			h.task.Pause()
		case "resume":
			h.task.Resume()
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d *Daemon) setVector(w http.ResponseWriter, r *http.Request) {
	h := d.lookup(r)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	var body task.VectorRequest
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Refused here rather than at the next boundary: a gate whose question cannot be posed
	// as written would hold the task on a question nobody can answer.
	if err := body.Vector.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.task.SetVector(&body.Vector, body.By)
	w.WriteHeader(http.StatusNoContent)
}

func parseChildMode(s string) task.ChildMode {
	mode, _ := task.ChildModeNamed(s)
	return mode
}

func writeOut(w http.ResponseWriter, r *http.Request, code int, v any) {
	codec.Write(w, r, code, v)
}
