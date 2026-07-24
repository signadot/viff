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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/example/infra/verse/process"
)

// Builder constructs a hosted process from an id, a task, the requested driver
// (e.g. "scripted"/"llm"; "" = the builder's default), the Registry that serves as its
// gate resolver, and an optional ContentReader (nil = read from the local filesystem;
// non-nil = read federated files through the verse). It is injected so the daemon stays
// decoupled from any particular driver/runtime, and picks the driver per spawn.
type Builder func(id, task, driver string, reg *process.Registry, content process.ContentReader) *process.Process

// Daemon hosts a set of running processes.
type Daemon struct {
	mu    sync.Mutex
	proc  map[string]*hosted
	seq   atomic.Uint64
	build Builder
	log   io.Writer
}

type hosted struct {
	proc *process.Process
	reg  *process.Registry
	task string

	mu     sync.Mutex
	status string // running | completed | cancelled | error
	reason string
	err    string
	result string // the process's final output (driver's last message), once completed
}

func (h *hosted) snapshot() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	pend := h.reg.Pending()
	pending := make([]map[string]any, 0, len(pend))
	for _, p := range pend {
		pending = append(pending, map[string]any{
			"id": p.Question.ID, "origin": p.Question.Origin, "tool": p.Tool,
			"gate": p.Question.Gate, "mode": p.Question.Mode.String(), "reason": p.Question.Reason,
		})
	}
	return map[string]any{
		"id": h.proc.ID, "task": h.task, "status": h.status,
		"reason": h.reason, "err": h.err, "result": h.result, "pending": pending,
		"cursor": h.proc.Cursor(), // resume the event stream with ?since=<cursor>
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
func (d *Daemon) Spawn(id, task, driver string, content process.ContentReader) *process.Process {
	return d.spawnHosted(id, task, driver, content).proc
}

// spawnHosted builds, registers, wires the read plane, and runs a process. Shared
// by Spawn and the HTTP spawn handler.
func (d *Daemon) spawnHosted(id, task, driver string, content process.ContentReader) *hosted {
	reg := process.NewRegistry()
	proc := d.build(id, task, driver, reg, content)
	h := &hosted{proc: proc, reg: reg, task: task, status: "running"}

	// Surface pending decisions on the same read plane as the loop's events, so an
	// attached client sees what a gate is blocked on and can resolve it by id.
	reg.OnPost(func(p process.Pending) {
		proc.Emit(process.Frame{
			Kind: process.EvPending, ID: p.Question.ID, Tool: p.Tool,
			Gate: p.Question.Gate, Mode: p.Question.Mode.String(), Text: p.Question.Reason,
		})
	})

	d.mu.Lock()
	d.proc[id] = h
	d.mu.Unlock()

	go func() {
		// A hosted process outlives its spawn call, so it must NOT derive from a
		// request context. Its own subtree cancel (proc.Cancel, via POST /cancel) is
		// the stop path.
		out, err := proc.Run(context.Background())
		h.mu.Lock()
		h.reason, h.status = out.Reason, out.Reason
		h.result = proc.Result() // the driver's final answer, if any
		if err != nil {
			h.status, h.err = "error", err.Error()
		}
		h.mu.Unlock()
	}()
	return h
}

func (d *Daemon) spawn(w http.ResponseWriter, r *http.Request) {
	var body struct{ Id, Task, Driver string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := body.Id // a controlPolicy client (the engine) passes its own id so it matches
	if id == "" { // the process:instance entity; an ad-hoc `process run` gets a fresh one
		id = fmt.Sprintf("p%d", d.seq.Add(1))
	}
	h := d.spawnHosted(id, body.Task, body.Driver, nil) // ad-hoc `process run`: local filesystem
	writeJSON(w, http.StatusCreated, map[string]any{"id": h.proc.ID})
}

// Count returns the number of hosted processes (live and completed-but-unreaped).
func (d *Daemon) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.proc)
}

func (d *Daemon) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Snapshots())
}

// Snapshots returns every hosted process in the shape the process API returns — for versed
// to union the local runtime with a policy's own processes (e.g. a broker's) in one list.
func (d *Daemon) Snapshots() []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]map[string]any, 0, len(d.proc))
	for _, h := range d.proc {
		out = append(out, h.snapshot())
	}
	return out
}

// SnapshotOf returns one hosted process's snapshot, or false if this daemon doesn't host it.
func (d *Daemon) SnapshotOf(id string) (map[string]any, bool) {
	d.mu.Lock()
	h := d.proc[id]
	d.mu.Unlock()
	if h == nil {
		return nil, false
	}
	return h.snapshot(), true
}

func (d *Daemon) get(w http.ResponseWriter, r *http.Request) {
	h := d.lookup(r)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, h.snapshot())
}

func (d *Daemon) events(w http.ResponseWriter, r *http.Request) {
	h := d.lookup(r)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	var ch <-chan process.Frame
	var unsub func()
	if s := r.URL.Query().Get("since"); s != "" {
		// Resumable: replay the retained window after the client's cursor, then live.
		// The ring already carries any EvPending, so no separate gate replay is needed.
		since, _ := strconv.ParseInt(s, 10, 64)
		ch, unsub = h.proc.SubscribeSince(since)
	} else {
		// Live attach from now: replay currently-blocked gates so it knows what to resolve,
		// and — if the process already FINISHED — its terminal status, so an attach after
		// completion exits instead of blocking on a stream that never emits again. (The
		// ?since path doesn't need this: its ring replay already carries the real terminal.)
		ch, unsub = h.proc.Subscribe()
		for _, p := range h.reg.Pending() {
			writeSSE(w, process.Frame{
				Kind: process.EvPending, ID: p.Question.ID, Tool: p.Tool,
				Gate: p.Question.Gate, Mode: p.Question.Mode.String(), Text: p.Question.Reason,
			})
		}
		if st, _ := h.snapshot()["status"].(string); st != "" && st != "running" {
			writeSSE(w, process.Frame{Kind: process.EvStatus, Text: st, Process: h.proc.ID})
		}
	}
	defer unsub()
	fl.Flush()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, ev)
			fl.Flush()
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
	var body struct {
		DecisionID string   `json:"decisionId"`
		Proceed    bool     `json:"proceed"`
		ChildMode  string   `json:"childMode"`
		Say        []string `json:"say"` // words to say on resolution (a gate that informs)
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res := process.Resolution{
		Proceed: body.Proceed, By: "human:daemon",
		ChildMode: parseChildMode(body.ChildMode), Steer: body.Say,
	}
	if !h.reg.Answer(body.DecisionID, res) {
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
		Instruction string `json:"instruction"`
		By          string `json:"by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Instruction == "" {
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
	var v process.Vector
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.proc.SetVector(&v)
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeSSE(w io.Writer, ev process.Frame) {
	b, _ := json.Marshal(ev)
	fmt.Fprintf(w, "data: %s\n\n", b)
}
