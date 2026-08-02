// Package processd hosts Processes behind an HTTP control + read plane: spawn
// processes, stream their events (SSE), resolve their gates by id, steer them,
// rebind their vector, and pause/cancel their subtrees — all over the wire, so
// autonomy is observed and adjusted live and remotely. Control ops are process-
// or subtree-scoped (never per-child); the read plane is drop-with-gap.
//
// It is the cleaned, renamed core of agentgated, with the LLM harness dependency
// severed: how a Process is built is injected as a Builder, so the daemon carries
// no coupling to any particular driver/runtime.
package processd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/process"
)

// Builder constructs a hosted process from a process.Launch — the id, the task, the
// requested driver (e.g. "scripted"/"llm"; "" = the builder's default), and the frame and
// tool set that shape the agent — plus the Registry that serves as its gate resolver and
// an optional ContentReader (nil = read from the local filesystem; non-nil = read
// federated files through the verse). It is injected so the daemon stays decoupled from
// any particular driver/runtime, and picks the driver per spawn.
type Builder func(l process.Launch, reg *process.Registry, content process.ContentReader) *process.Process

// Daemon hosts a set of running processes.
type Daemon struct {
	mu    sync.Mutex
	proc  map[string]*hosted
	seq   atomic.Uint64
	build Builder
	log   io.Writer
	// onQuestion, when set, is called as each hosted process poses a gate question. The
	// daemon holds no store and should not: publishing a question is the job of whoever
	// owns one, and this is the seam that hands it over with the derived effects still
	// attached — the read plane's own EvPending frame carries only what an attached client
	// needs to see, which is less than a record needs to be matchable.
	onQuestion func(processID string, p process.Pending)
}

// OnQuestion registers the publisher. Optional: processd runs standalone in tests and under
// `verse process run` with nothing behind it.
func (d *Daemon) OnQuestion(f func(processID string, p process.Pending)) { d.onQuestion = f }

type hosted struct {
	proc *process.Process
	reg  *process.Registry
	task string
	// driver is retained, not just passed to build, because it is the only durable record
	// of WHO was picked when a spawn's doer came from a `provider` selection. The broker's
	// snapshot has always reported it; a locally-run process did not, so an auto-selected
	// doer was observable nowhere but a trace line.
	driver string

	mu     sync.Mutex
	status process.Status
	reason string
	err    string
	result string // the process's final output (driver's last message), once completed
}

func (h *hosted) snapshot() process.Instance {
	h.mu.Lock()
	defer h.mu.Unlock()
	pend := h.reg.Pending()
	pending := make([]process.PendingGate, 0, len(pend))
	for _, p := range pend {
		pending = append(pending, process.PendingGate{
			ID: p.Question.ID, Origin: p.Question.Origin, Tool: p.Tool,
			Gate: p.Question.Gate, Mode: p.Question.Mode.String(), Reason: p.Question.Reason,
		})
	}
	return process.Instance{
		ID: h.proc.ID, Task: h.task, Driver: h.driver, Status: h.status,
		Err: h.err, Result: h.result, Pending: pending,
		Cursor: h.proc.Cursor(), // resume the event stream with ?since=<cursor>
	}
}

// New returns a Daemon that builds hosted processes with the given Builder.
func New(build Builder) *Daemon {
	return &Daemon{proc: map[string]*hosted{}, build: build, log: os.Stderr}
}

// Handler returns the HTTP routes (Go 1.22 method+wildcard patterns).
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /processes", d.spawn)
	mux.HandleFunc("GET /processes", d.list)
	mux.HandleFunc("GET /processes/{id}", d.get)
	mux.HandleFunc("GET /processes/{id}/events", d.events)
	mux.HandleFunc("POST /processes/{id}/resolve", d.resolve)
	mux.HandleFunc("POST /processes/{id}/say", d.say)
	mux.HandleFunc("POST /processes/{id}/cancel", d.control("cancel"))
	mux.HandleFunc("POST /processes/{id}/pause", d.control("pause"))
	mux.HandleFunc("POST /processes/{id}/resume", d.control("resume"))
	mux.HandleFunc("PUT /processes/{id}/vector", d.setVector)
	return mux
}

func (d *Daemon) lookup(r *http.Request) *hosted {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.proc[r.PathValue("id")]
}

// Lookup returns the live hosted process for id, or nil — the local re-attach path.
func (d *Daemon) Lookup(id string) *process.Process {
	d.mu.Lock()
	defer d.mu.Unlock()
	if h := d.proc[id]; h != nil {
		return h.proc
	}
	return nil
}

// Spawn builds, hosts, and runs a process for the given task — the programmatic
// seam the engine's Spawner uses, so a trigger-spawned process is hosted and
// controllable (list/get/attach/cancel) exactly like a `verse process run` one.
// Returns the running process (already Run in the background).
func (d *Daemon) Spawn(l process.Launch, content process.ContentReader) *process.Process {
	h, _ := d.spawnHosted(l, content)
	return h.proc
}

// spawnHosted builds, registers, wires the read plane, and runs a process. Shared
// by Spawn and the HTTP spawn handler. The bool reports whether it BUILT one; false means
// the id was already hosted and that process is what came back.
//
// An id is spawned ONCE. A second spawn of one that is already hosted returns the existing
// process rather than building beside it — because the alternative is not two processes but
// one lost one: registration is a map assignment, so the newcomer took the id and the
// original kept running with nothing able to reach it, address it, or stop it.
//
// That makes the spawn safe to RETRY, which is the point. A caller whose spawn request
// timed out cannot know whether the far side acted, and until now its only options were to
// abandon real work or to destroy it.
func (d *Daemon) spawnHosted(l process.Launch, content process.ContentReader) (*hosted, bool) {
	d.mu.Lock()
	if h, ok := d.proc[l.ID]; ok {
		d.mu.Unlock()
		return h, false
	}
	d.mu.Unlock()

	reg := process.NewRegistry()
	proc := d.build(l, reg, content)

	// Install the spawn's authority axes here rather than in the Builder, because they are
	// properties of the RUNTIME's contract with verse, not of any driver: a scripted
	// process and an LLM one are bounded the same way, and a Builder that forgot to apply
	// them would silently hand back an unbounded process. Both are conditional — a spawn
	// that named neither gets exactly what it got before (wildcard scope, the builder's
	// own vector), so this narrows only what a caller asked to have narrowed.
	if l.Grant != nil {
		proc.SetScope(*l.Grant)
	}
	if l.Vector != nil {
		proc.SetVector(l.Vector, "launch")
	}
	h := &hosted{proc: proc, reg: reg, task: l.Task, driver: l.Driver, status: process.Running}

	// Surface pending decisions on the same read plane as the loop's events, so an
	// attached client sees what a gate is blocked on and can resolve it by id.
	reg.OnPost(func(p process.Pending) {
		if d.onQuestion != nil {
			d.onQuestion(l.ID, p)
		}
		proc.Emit(process.Frame{
			Kind: process.EvPending, ID: p.Question.ID, Tool: p.Tool,
			Gate: p.Question.Gate, Mode: p.Question.Mode.String(), Text: p.Question.Reason,
		})
	})

	// Re-checked under the lock: two concurrent spawns of one id race the check above, and
	// the loser must not start a second loop. Whoever registered first is the process.
	d.mu.Lock()
	if existing, ok := d.proc[l.ID]; ok {
		d.mu.Unlock()
		return existing, false
	}
	d.proc[l.ID] = h
	d.mu.Unlock()

	go func() {
		// A hosted process outlives its spawn call, so it must NOT derive from a
		// request context. Its own subtree cancel (proc.Cancel, via POST /cancel) is
		// the stop path.
		out, err := proc.Run(context.Background())
		h.mu.Lock()
		h.reason, h.status = out.Reason, process.Status(out.Reason)
		h.result = proc.Result() // the driver's final answer, if any
		if err != nil {
			h.status, h.err = process.Failed, err.Error()
		}
		h.mu.Unlock()
	}()
	return h, true
}

func (d *Daemon) spawn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Id     string          `tony:"field=id"`
		Task   string          `tony:"field=task"`
		Driver string          `tony:"field=driver"`
		Frame  string          `tony:"field=frame"`
		Tools  []string        `tony:"field=tools"`
		Answer *ir.Node        `tony:"field=answer,omitzero"`
		Vector *process.Vector `tony:"field=vector,omitzero"`
		Grant  *process.Grant  `tony:"field=grant,omitzero"`
	}
	_ = codec.Read(r, &body)
	id := body.Id // a runner client (the engine) passes its own id so it matches
	if id == "" { // the process:instance entity; an ad-hoc `process run` gets a fresh one
		id = fmt.Sprintf("p%d", d.seq.Add(1))
	}
	// ad-hoc `process run`: local filesystem. Everything that shapes or bounds the agent
	// rides the wire, so a process hosted on a REMOTE runner is framed and bounded exactly
	// as a local one — the seam is complete or the guarantee is local-only. The vector and
	// grant arrive as resolved predicates rather than names for the reason process.Launch
	// gives: the vocabulary lives at one end, so the two cannot drift apart.
	h, created := d.spawnHosted(process.Launch{
		ID: id, Task: body.Task, Driver: body.Driver, Frame: body.Frame, Tools: body.Tools,
		Answer: body.Answer, Vector: body.Vector, Grant: body.Grant,
	}, nil)
	// 201 built it, 200 found it already running. A retrying client needs no more than
	// that, and a client that does not care can ignore it: both mean "this id is running".
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeOut(w, r, status, map[string]any{"id": h.proc.ID})
}

// Count returns the number of hosted processes (live and completed-but-unreaped).
func (d *Daemon) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.proc)
}

func (d *Daemon) list(w http.ResponseWriter, r *http.Request) {
	writeOut(w, r, http.StatusOK, d.Snapshots())
}

// Snapshots returns every hosted process in the shape the process API returns — for versed
// to union the local runtime with a runner's own processes (e.g. a broker's) in one list.
func (d *Daemon) Snapshots() []process.Instance {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]process.Instance, 0, len(d.proc))
	for _, h := range d.proc {
		out = append(out, h.snapshot())
	}
	return out
}

// SnapshotOf returns one hosted process's snapshot, or false if this daemon doesn't host it.
func (d *Daemon) SnapshotOf(id string) (process.Instance, bool) {
	d.mu.Lock()
	h := d.proc[id]
	d.mu.Unlock()
	if h == nil {
		return process.Instance{}, false
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

	var ch <-chan process.Frame
	var unsub func()
	if s := r.URL.Query().Get("since"); s != "" {
		// Resumable: replay the retained window after the client's cursor, then live.
		// The ring already carries any EvPending, so no separate gate replay is needed.
		since, _ := strconv.ParseInt(s, 10, 64)
		ch, unsub = h.proc.SubscribeSince(since)
	} else {
		// Live attach from now: replay currently-blocked gates so it knows what to resolve.
		// A finished process needs no special case here — the bus hands a late subscriber
		// the terminal and closes, so an attach after completion exits rather than blocking
		// on a stream that will never emit again.
		ch, unsub = h.proc.Subscribe()
		for _, p := range h.reg.Pending() {
			_ = sse.Send(process.Frame{
				Kind: process.EvPending, ID: p.Question.ID, Tool: p.Tool,
				Gate: p.Question.Gate, Mode: p.Question.Mode.String(), Text: p.Question.Reason,
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

// Resolve answers a hosted process's pending decision by id — the Go entry versed's unified
// gate surface uses to resolve a process gate (write/shell/delegate) the same way it resolves
// an engine gate. Reports whether the decision matched.
func (d *Daemon) Resolve(procID, decisionID string, res process.Resolution) bool {
	d.mu.Lock()
	h := d.proc[procID]
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
	// process-control.md mixes them — `{id, proceed}` — so both halves are read independently
	// rather than as two fixed vocabularies.
	//
	// The verdict is the OR, and it is not lossy: a yes is `true` in whichever field the
	// client used, and a no is `false` in both AND absent from both. There is no answer the
	// two fields can express that this reads wrongly, short of a client setting them against
	// each other on purpose.
	var body struct {
		ID      string `tony:"field=id"`
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
	proceed := body.Approve || body.Proceed
	by := body.By
	if by == "" {
		by = "human:daemon"
	}
	res := process.Resolution{
		Proceed: proceed, By: by,
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
	var body struct {
		Instruction string `tony:"field=instruction"`
		By          string `tony:"field=by"`
	}
	if err := codec.Read(r, &body); err != nil || body.Instruction == "" {
		http.Error(w, "instruction required", http.StatusBadRequest)
		return
	}
	by := body.By
	if by == "" {
		by = "human:cli"
	}
	h.proc.Steer(body.Instruction, by)
	w.WriteHeader(http.StatusNoContent)
}

// control returns a handler for the subtree ops that take no body.
func (d *Daemon) control(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := d.lookup(r)
		if h == nil {
			http.NotFound(w, r)
			return
		}
		switch op {
		case "cancel":
			h.proc.Cancel("cancelled via daemon")
		case "pause":
			h.proc.Pause()
		case "resume":
			h.proc.Resume()
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
	var body struct {
		Vector process.Vector `tony:"field=vector"`
		By     string         `tony:"field=by"`
	}
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.proc.SetVector(&body.Vector, body.By)
	w.WriteHeader(http.StatusNoContent)
}

func parseChildMode(s string) process.ChildMode {
	switch s {
	case "autonomous":
		return process.ChildAutonomous
	case "careful":
		return process.ChildCareful
	default:
		return process.ChildInheritGates
	}
}

func writeOut(w http.ResponseWriter, r *http.Request, code int, v any) {
	codec.Write(w, r, code, v)
}
