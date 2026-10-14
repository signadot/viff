package runnerd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/task"
)

// SubprocessHandler is a task handler that runs each task in a process of its own, a child of the
// handler's, inside its Sandbox, and serves the task contract by passing each request on to that
// process. The process serves the contract with an InProcessHandler of its own (ServeTask), so
// nothing is translated on the way.
//
// What a SubprocessHandler adds is the lifecycle a process has and an in-process task doesn't: a
// process can die without its task ending. So the handler remembers each task's last state,
// keeps answering for it after its process is gone, and reports a process that exits without
// finishing as a failed task, with its exit status.
//
// The runner's own secrets don't reach the process by its environment: Command's process gets
// Env and nothing else, and Config on its stdin.
type SubprocessHandler struct {
	SubprocessConfig

	describe runner.Description
	seq      atomic.Uint64
	mu       sync.Mutex
	tasks    map[string]*proc
}

// SubprocessConfig is how a standalone runner starts a task's process.
type SubprocessConfig struct {
	// Command is the task process: a program, and its arguments, that calls ServeTask.
	Command []string
	// Env is the task process's whole environment, but HOME, which is the task's directory, and
	// TMPDIR, which is /tmp (the sandbox's own).
	Env []string
	// Config is written to the task process's stdin.
	Config []byte
	// Sandbox confines the task process.
	Sandbox Sandbox
	// Work is where the tasks' directories are made, and Control where their sockets are: a
	// directory no task's sandbox binds.
	Work, Control string
	Log           io.Writer
}

// Timings, variables so the tests can shorten them.
var (
	// How long a finished task's process is kept, so a reader attached at its last frame gets it.
	lingerAfterEnd = 3 * time.Second
	// How long a process has to exit after its lifeline closes, before it is killed.
	stopGrace = 5 * time.Second
	// How long a starting process has to answer.
	startTimeout = 30 * time.Second
	startPoll    = 20 * time.Millisecond
	// How long a task's snapshot has to catch up with the end its event stream reported.
	settleTimeout = 2 * time.Second
)

// The descriptors a task process inherits, in the order the runner puts them in cmd.ExtraFiles.
//
// fd 3 is a listening socket: the process serves on it, and its path is in the runner's Control
// directory, outside the sandbox, so the task can't reach its own control plane. fd 4 is a
// lifeline, a pipe the runner never writes: it reads EOF when the runner closes it or dies.
const (
	controlFD  = 3
	lifelineFD = 4
)

// NewSubprocessHandler is a standalone runner that starts its tasks' processes as c says.
func NewSubprocessHandler(c SubprocessConfig) *SubprocessHandler {
	if c.Log == nil {
		c.Log = os.Stderr
	}
	return &SubprocessHandler{SubprocessConfig: c, tasks: map[string]*proc{}}
}

// Describe sets what the runner offers. Set it before the runner is served.
func (h *SubprocessHandler) Describe(desc runner.Description) { h.describe = desc }

// HTTPHandler serves the task contract and GET /describe.
func (h *SubprocessHandler) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /describe", func(w http.ResponseWriter, r *http.Request) {
		writeOut(w, r, http.StatusOK, h.describe)
	})
	mux.HandleFunc("POST /tasks", h.spawn)
	mux.HandleFunc("GET /tasks", h.list)
	mux.HandleFunc("/tasks/{id}", h.forward)
	mux.HandleFunc("/tasks/{id}/{op}", h.forward)
	return mux
}

// proc is one task's process, from the runner's side.
type proc struct {
	id   string
	dir  string
	sock string
	cmd  *exec.Cmd
	life *os.File // the lifeline's write end
	rt   task.Runner
	prox *httputil.ReverseProxy
	// ready is closed once the process hosts the task, or has failed to; err says which.
	ready chan struct{}
	err   error
	// gone is closed when the process has exited, by the one goroutine that waits for it.
	gone chan struct{}

	mu    sync.Mutex
	exit  error
	final *task.Snapshot // the last word on the task, once there is one
	dead  bool
}

func (p *proc) running() bool {
	select {
	case <-p.gone:
		return false
	default:
		return true
	}
}

func (p *proc) snapshot() (task.Snapshot, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.final == nil {
		return task.Snapshot{}, false
	}
	return *p.final, true
}

// remember records the last word on the task. The first one stands: the end the task reported
// is a better account than its process exiting, which always comes after.
func (p *proc) remember(in task.Snapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.final == nil {
		in.ID = p.id
		p.final = &in
	}
}

func (p *proc) exitStatus() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exit == nil {
		return "status 0"
	}
	return p.exit.Error()
}

// spawn is POST /tasks: start a process for the task, and pass the spawn on to it.
func (h *SubprocessHandler) spawn(w http.ResponseWriter, r *http.Request) {
	var body task.SpawnRequest
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, fmt.Sprintf("the spawn does not parse: %v", err), http.StatusBadRequest)
		return
	}
	if body.ID == "" {
		body.ID = fmt.Sprintf("p%d", h.seq.Add(1))
	}
	raw, err := codec.Marshal(&body, codec.Tony)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// A task is spawned once while it runs: a second spawn of a live id is answered by the task
	// that has it, which is what makes a spawn safe to retry. A finished id runs again.
	h.mu.Lock()
	p := h.tasks[body.ID]
	if p != nil {
		if _, done := p.snapshot(); done || !p.running() {
			p = nil
		}
	}
	created := p == nil
	if created {
		p = &proc{id: body.ID, ready: make(chan struct{}), gone: make(chan struct{})}
		h.tasks[body.ID] = p
	}
	h.mu.Unlock()

	if created {
		p.err = h.start(p, raw)
		close(p.ready)
		if p.err != nil {
			fmt.Fprintf(h.Log, "runnerd: %s: %v\n", p.id, p.err)
			h.mu.Lock()
			if h.tasks[p.id] == p {
				delete(h.tasks, p.id)
			}
			h.mu.Unlock()
			http.Error(w, p.err.Error(), http.StatusBadGateway)
			return
		}
		go h.supervise(p)
		writeOut(w, r, http.StatusCreated, runner.SpawnResponse{ID: p.id})
		return
	}
	select {
	case <-p.ready:
	case <-r.Context().Done():
		return
	}
	if p.err != nil {
		http.Error(w, p.err.Error(), http.StatusBadGateway)
		return
	}
	writeOut(w, r, http.StatusOK, runner.SpawnResponse{ID: p.id})
}

// start makes the task's directory, starts its process in the sandbox, and hands it the spawn.
func (h *SubprocessHandler) start(p *proc, spawn []byte) error {
	dir, err := os.MkdirTemp(h.Work, "task-")
	if err != nil {
		return fmt.Errorf("the task's directory could not be made: %w", err)
	}
	p.dir = dir
	fail := func(err error) error {
		p.stop(h.Log)
		os.RemoveAll(dir)
		return err
	}

	sock := fmt.Sprintf("%s/t%d.sock", h.Control, h.seq.Add(1))
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fail(fmt.Errorf("the task's control socket: %w", err))
	}
	p.sock = sock
	ul := ln.(*net.UnixListener)
	// Closing a unix listener removes its path, and the path is the runner's half: the process
	// accepts on a duplicate of the listener, and the runner dials the path.
	ul.SetUnlinkOnClose(false)
	lf, err := ul.File()
	ln.Close()
	if err != nil {
		return fail(fmt.Errorf("the task's control socket: %w", err))
	}
	defer lf.Close()
	lifeR, lifeW, err := os.Pipe()
	if err != nil {
		return fail(err)
	}
	defer lifeR.Close()
	p.life = lifeW

	cmd := exec.Command(h.Command[0], h.Command[1:]...)
	cmd.Stdin = bytes.NewReader(h.Config)
	cmd.Env = append(append([]string{}, h.Env...), "HOME="+dir, "TMPDIR=/tmp")
	out := tagged(h.Log, p.id)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.ExtraFiles = []*os.File{lf, lifeR} // fd 3 and fd 4
	if err := h.Sandbox.Wrap(cmd, dir); err != nil {
		return fail(err)
	}
	if err := cmd.Start(); err != nil {
		return fail(fmt.Errorf("starting the task's process: %w", err))
	}
	p.cmd = cmd
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.exit = err
		p.mu.Unlock()
		close(p.gone)
	}()

	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}
	base := "http://task"
	p.rt = client.HTTPOver(base, tr)
	target, _ := url.Parse(base)
	p.prox = httputil.NewSingleHostReverseProxy(target)
	p.prox.Transport = tr
	p.prox.FlushInterval = -1 // the event stream is passed on as it comes
	p.prox.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, fmt.Sprintf("the process running task %s did not answer: %v", p.id, err),
			http.StatusBadGateway)
	}

	if err := p.awaitReady(); err != nil {
		return fail(err)
	}
	if err := post(tr, base+"/tasks", spawn); err != nil {
		return fail(fmt.Errorf("the task's process refused the spawn: %w", err))
	}
	return nil
}

// awaitReady waits until the process answers GET /tasks, or exits.
func (p *proc) awaitReady() error {
	lister, _ := p.rt.(task.Lister)
	deadline := time.Now().Add(startTimeout)
	var last error
	for time.Now().Before(deadline) {
		select {
		case <-p.gone:
			return fmt.Errorf("the task's process exited before it served anything (%s); its "+
				"output is on the runner's log, tagged [%s]", p.exitStatus(), p.id)
		default:
		}
		if _, err := lister.ListTasks(); err == nil {
			return nil
		} else {
			last = err
		}
		time.Sleep(startPoll)
	}
	return fmt.Errorf("the task's process did not answer within %s: %v", startTimeout, last)
}

func post(tr http.RoundTripper, url string, body []byte) error {
	resp, err := (&http.Client{Transport: tr, Timeout: client.ControlTimeout}).Post(url,
		codec.MediaType(codec.Tony), bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if detail := strings.TrimSpace(string(msg)); detail != "" {
			return errors.New(detail)
		}
		return errors.New(resp.Status)
	}
	return nil
}

// supervise follows a task to its end, remembers how it ended, and then stops its process and
// removes its directory. A process that exits without the task ending is a failed task.
func (h *SubprocessHandler) supervise(p *proc) {
	var since int64
	for {
		var end task.Status
		if end, since = p.follow(since); end == "" {
			break // the process exited first
		}
		// The end the stream reported stands once the snapshot agrees, or the process is gone.
		// The client reports a subscribe the process refused as a failed frame on the stream,
		// which is not the task ending: then the snapshot says it still runs, and it is
		// followed again.
		snap, agreed := p.settled()
		if agreed || !p.running() {
			if !snap.Status.Terminal() {
				snap.Status = end
			}
			p.remember(snap)
			time.Sleep(lingerAfterEnd)
			break
		}
	}
	p.stop(h.Log)
	p.remember(task.Snapshot{Status: task.Failed, Err: fmt.Sprintf(
		"the task's process exited (%s) before the task ended", p.exitStatus())})
	os.RemoveAll(p.dir)
}

// follow reads the task's events from since until a terminal status, and returns it with the
// cursor it reached; "" when its process exited first. A stream that closes while the process
// runs is not the task ending: it is resumed from the last frame read.
func (p *proc) follow(since int64) (task.Status, int64) {
	h := p.rt.Attach(p.id)
	for {
		frames, cancel := h.SubscribeSince(since)
		for f := range frames {
			if f.Cursor > since {
				since = f.Cursor
			}
			if f.Kind == task.EventStatus && task.Status(f.Text).Terminal() {
				cancel()
				return task.Status(f.Text), since
			}
		}
		cancel()
		select {
		case <-p.gone:
			return "", since
		case <-time.After(startPoll):
		}
	}
}

// settled is the snapshot to remember a finished task by, and whether it says the task has
// ended. The event stream reports the end a moment before the snapshot does, so it is asked
// again until it agrees, for up to settleTimeout.
func (p *proc) settled() (task.Snapshot, bool) {
	lister, _ := p.rt.(task.Lister)
	var last task.Snapshot
	for deadline := time.Now().Add(settleTimeout); time.Now().Before(deadline); {
		if snap, err := lister.TaskSnapshot(p.id); err == nil {
			last = snap
			if snap.Status.Terminal() {
				return snap, true
			}
		}
		time.Sleep(startPoll)
	}
	return last, false
}

// stop closes the lifeline, then kills the process if it hasn't exited. Idempotent.
func (p *proc) stop(log io.Writer) {
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		return
	}
	p.dead = true
	p.mu.Unlock()
	if p.life != nil {
		p.life.Close()
	}
	if p.cmd != nil {
		select {
		case <-p.gone:
		case <-time.After(stopGrace):
			fmt.Fprintf(log, "runnerd: %s: the task's process didn't exit; killing it\n", p.id)
			p.cmd.Process.Kill()
			<-p.gone
		}
	} else {
		close(p.gone)
	}
	if p.sock != "" {
		os.Remove(p.sock)
	}
}

// forward is every /tasks/{id} route. A live task's process answers it; a finished task is
// answered from what the runner remembers.
func (h *SubprocessHandler) forward(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.mu.Lock()
	p := h.tasks[id]
	h.mu.Unlock()
	if p == nil {
		http.NotFound(w, r)
		return
	}
	select {
	case <-p.ready:
	case <-r.Context().Done():
		return
	}
	if p.err != nil {
		http.NotFound(w, r)
		return
	}
	final, done := p.snapshot()
	if !done {
		p.prox.ServeHTTP(w, r)
		return
	}
	switch op := r.PathValue("op"); {
	case r.Method == http.MethodGet && op == "":
		writeOut(w, r, http.StatusOK, final)
	case r.Method == http.MethodGet && op == "events":
		// A finished task's stream is its end, and closes: what a late reader gets from an
		// InProcessHandler too.
		sse, err := codec.NewSSE(w, r)
		if err != nil {
			codec.HTTPError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		_ = sse.Send(task.Event{Kind: task.EventStatus, Text: string(final.Status), Err: final.Err})
		sse.Flush()
	// The rest as an InProcessHandler answers them for an ended task: a cancel, pause, resume or
	// vector does nothing and says so with 204, a say is 410, and no gate is left to resolve.
	case op == "cancel" || op == "pause" || op == "resume" || op == "vector":
		w.WriteHeader(http.StatusNoContent)
	case op == "say":
		http.Error(w, fmt.Sprintf("task %s has ended (%s)", id, final.Status), http.StatusGone)
	case op == "resolve":
		http.Error(w, "unknown or already-answered decision", http.StatusNotFound)
	default:
		http.NotFound(w, r)
	}
}

// list is GET /tasks: every task the runner runs or has run.
func (h *SubprocessHandler) list(w http.ResponseWriter, r *http.Request) {
	writeOut(w, r, http.StatusOK, h.Snapshots())
}

// Snapshots is every task the runner runs or has run, asked of its process while it runs.
func (h *SubprocessHandler) Snapshots() []task.Snapshot {
	h.mu.Lock()
	ps := make([]*proc, 0, len(h.tasks))
	for _, p := range h.tasks {
		ps = append(ps, p)
	}
	h.mu.Unlock()
	out := make([]task.Snapshot, 0, len(ps))
	for _, p := range ps {
		select {
		case <-p.ready:
		default:
			out = append(out, task.Snapshot{ID: p.id, Status: task.Running})
			continue
		}
		if p.err != nil {
			continue
		}
		if final, done := p.snapshot(); done {
			out = append(out, final)
			continue
		}
		snap, err := p.rt.(task.Lister).TaskSnapshot(p.id)
		if err != nil {
			// Still the runner's task: leaving it out would read as a runner with less work.
			snap = task.Snapshot{ID: p.id, Status: task.Running, Err: err.Error()}
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Shutdown stops every task's process and removes its directory.
func (h *SubprocessHandler) Shutdown() {
	h.mu.Lock()
	ps := make([]*proc, 0, len(h.tasks))
	for _, p := range h.tasks {
		ps = append(ps, p)
	}
	h.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range ps {
		wg.Go(func() {
			<-p.ready
			p.stop(h.Log)
			if p.dir != "" {
				os.RemoveAll(p.dir)
			}
		})
	}
	wg.Wait()
}

// tagged prefixes each line a task's process writes with its id.
func tagged(w io.Writer, id string) io.Writer { return &tagWriter{w: w, tag: "[" + id + "] "} }

type tagWriter struct {
	mu  sync.Mutex
	w   io.Writer
	tag string
	mid bool // within a line
}

func (t *tagWriter) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var buf bytes.Buffer
	for _, c := range b {
		if !t.mid {
			buf.WriteString(t.tag)
			t.mid = true
		}
		buf.WriteByte(c)
		if c == '\n' {
			t.mid = false
		}
	}
	if _, err := t.w.Write(buf.Bytes()); err != nil {
		return 0, err
	}
	return len(b), nil
}
