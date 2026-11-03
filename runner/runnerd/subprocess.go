package runnerd

import (
	"bytes"
	"cmp"
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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/systems"
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
	owned    EnvOf         // the variables a launch's task shell is given by the runner (CheckLaunch)
	linger   time.Duration // lingerAfterEnd, as it was when the handler was made
	seq      atomic.Uint64
	mu       sync.Mutex
	tasks    map[string]*proc
}

// SubprocessConfig is how a standalone runner starts a task's process.
type SubprocessConfig struct {
	// Command is the task process: a program, and its arguments, that calls ServeTask.
	Command []string
	// Env is a launch's task process's whole environment, but HOME, which is the task's
	// directory, and TMPDIR, which is /tmp (the sandbox's own): what its doer's shell is given,
	// so a process holds no variable its doer's shell is not given. Nil is an empty one.
	Env func(*runner.Launch) []string
	// Config is written to the task process's stdin.
	Config []byte
	// Sandbox confines the task process.
	Sandbox Sandbox
	// Programs are the other programs a task runs, found inside the sandbox where they are
	// outside it: a script's verse-call-tool.
	Programs []string
	// Repos checks out the repos a launch names, and performs what a task asks over its channel:
	// a push, a proposal, a tracker's act. Nil refuses a launch that names repos, and opens no
	// channel.
	Repos *Repos
	// AgentHosts are the hosts an agent's task reaches whatever its network: its model. Under
	// network none it gets a proxy that allows them alone.
	AgentHosts []string
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
// lifeline, a pipe the runner never writes: it reads EOF when the runner closes it or dies. fd 5,
// for a task that names repos, is its delivery channel (package delivery).
const (
	controlFD  = 3
	lifelineFD = 4
	// DeliveryFD is the task's end of its delivery channel, when its launch names repos.
	DeliveryFD = 5
)

// NewSubprocessHandler is a standalone runner that starts its tasks' processes as c says.
func NewSubprocessHandler(c SubprocessConfig) *SubprocessHandler {
	if c.Log == nil {
		c.Log = os.Stderr
	}
	return &SubprocessHandler{SubprocessConfig: c, linger: lingerAfterEnd, tasks: map[string]*proc{}}
}

// Describe sets what the runner offers. Set it before the runner is served.
func (h *SubprocessHandler) Describe(desc runner.Description) { h.describe = desc }

// OwnEnv names the variables the runner gives a launch's task shell itself, which the launch's
// `with.env` may not name: its doer's. Unset is task.DefaultShellEnv.
func (h *SubprocessHandler) OwnEnv(of EnvOf) { h.owned = of }

// HTTPHandler serves the task contract and GET /describe.
func (h *SubprocessHandler) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /describe", func(w http.ResponseWriter, r *http.Request) {
		writeOut(w, r, http.StatusOK, h.describe)
	})
	mux.HandleFunc("POST /tasks", h.launch)
	mux.HandleFunc("GET /tasks", h.list)
	// Each route by its method, as an InProcessHandler serves them (runner/doc.go), so a method
	// the contract does not name is refused 405 rather than taken as the one it does.
	mux.HandleFunc("GET /tasks/{id}", h.forward(""))
	mux.HandleFunc("GET /tasks/{id}/events", h.forward("events"))
	mux.HandleFunc("POST /tasks/{id}/resolve", h.forward("resolve"))
	mux.HandleFunc("POST /tasks/{id}/say", h.forward("say"))
	mux.HandleFunc("POST /tasks/{id}/cancel", h.forward("cancel"))
	mux.HandleFunc("POST /tasks/{id}/pause", h.forward("pause"))
	mux.HandleFunc("POST /tasks/{id}/resume", h.forward("resume"))
	mux.HandleFunc("PUT /tasks/{id}/autonomy", h.forward("autonomy"))
	return mux
}

// proc is one task's process, from the runner's side.
type proc struct {
	id string
	// known is the task as the runner knows it from its launch, for when its process can't say.
	known task.Snapshot
	// l is the launch: what a replacement of the task's autonomy is held to (replacePolicy).
	l *runner.Launch
	// autonomy is the task's CURRENT autonomy, the launch's until a replacement lands
	// (replacePolicy): what its proxy decides each connection by (connections).
	autonomy atomic.Pointer[task.Autonomy]
	dir      string
	sock     string
	cmd      *exec.Cmd
	life     *os.File // the lifeline's write end
	rt       task.Runner
	prox     *httputil.ReverseProxy
	co       *checkout     // the task's repos, when its launch names some
	eg       *EgressServer // the task's way out, when its network allows hosts
	// egress is eg's proxy, for what it refused, read without p.mu by whatever reports the task.
	egress atomic.Pointer[Egress]
	// abort ends the task before its process hosts it: a cancel, or the runner shutting down.
	abort context.CancelCauseFunc
	// ready is closed once the process hosts the task, or the task has ended without it.
	ready chan struct{}
	// gone is closed when the process has exited, by the one goroutine that waits for it.
	gone chan struct{}
	// done is closed once the runner is through with the task: its process stopped, its repos
	// settled (what didn't leave salvaged), its directory removed or kept. Shutdown waits on it.
	done chan struct{}

	mu sync.Mutex
	// hosted is set, under mu, once the process hosts the task: from then a cancel is the
	// task's to carry out, and before it the runner's.
	hosted bool
	exit   error
	final  *task.Snapshot // the last word on the task, once there is one
	dead   bool
	// events is the task's read plane as the runner followed it, so a reader can still replay
	// it once the process has exited: the last task.RingCap of it, as the task itself keeps.
	events []task.Event
}

// record keeps an event the runner followed, once.
func (p *proc) record(e task.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.Kind == task.EventGap {
		return
	}
	if n := len(p.events); n > 0 && e.Cursor <= p.events[n-1].Cursor {
		return
	}
	p.events = append(p.events, e)
	if len(p.events) > task.RingCap {
		p.events = p.events[len(p.events)-task.RingCap:]
	}
}

// eventsSince is the followed events after the cursor since. A reader behind what is kept, or
// reading across a hole the runner's own following left (a reconnect that fell behind), is told
// so first with a gap marker, and resyncs from the snapshot: what the task's own stream does.
func (p *proc) eventsSince(since int64) []task.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []task.Event
	next := since + 1
	missed := 0
	for _, e := range p.events {
		if e.Cursor <= since {
			continue
		}
		missed += int(e.Cursor - next)
		next = e.Cursor + 1
		out = append(out, e)
	}
	if missed > 0 {
		gap := task.Event{Kind: task.EventGap, Task: p.id, Cursor: next - 1, Dropped: missed}
		out = append([]task.Event{gap}, out...)
	}
	return out
}

// current is the task's snapshot as its process reports it, or, when that can't be had, the
// task as the runner knows it: its id, running.
func (p *proc) current() task.Snapshot {
	if final, done := p.snapshot(); done {
		return final
	}
	if !p.isReady() {
		return p.as(task.Running, "")
	}
	if lister, ok := p.rt.(task.Lister); ok {
		if in, err := lister.TaskSnapshot(p.id); err == nil {
			return p.withRefused(in)
		}
	}
	return p.as(task.Running, "")
}

// as is the task as the runner knows it, with a status and an error.
func (p *proc) as(status task.Status, err string) task.Snapshot {
	snap := p.known
	snap.Status, snap.Err = status, err
	return p.withRefused(snap)
}

// withRefused is snap with the connections the task's proxy refused, which only the runner
// knows: the task's process sees a 403, never the decision.
func (p *proc) withRefused(snap task.Snapshot) task.Snapshot {
	if eg := p.egress.Load(); eg != nil {
		if r := eg.Refusals(); len(r) > 0 {
			snap.Refused = r
		}
	}
	return snap
}

func (p *proc) isReady() bool {
	select {
	case <-p.ready:
		return true
	default:
		return false
	}
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
		in = p.withRefused(in)
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

// launch is POST /tasks: take the task, answer with its snapshot, and then start it — check out
// its repos, start its process, pass the launch on to it. A task that can't be started fails,
// with the reason; one cancelled before its process hosts it ends cancelled.
func (h *SubprocessHandler) launch(w http.ResponseWriter, r *http.Request) {
	// Checked here, before anything is started for it: a launch this runner refuses costs no
	// sandbox. The task's process checks it again as it arrives.
	body, ok := readLaunch(w, r, func(l *runner.Launch) error {
		if err := CheckLaunch(l, h.describe, h.owned.of(l)); err != nil {
			return err
		}
		if len(l.Repos()) > 0 && h.Repos == nil {
			return errNoRepos
		}
		// What the launch asked for first, so a refusal names it, and then what the task gets:
		// an agent's none is an allow, for its model.
		if err := h.Sandbox.Provides(Network{Mode: l.Network().Mode}); err != nil {
			return fmt.Errorf("environment.systems.network: %w", err)
		}
		network, own := h.network(l)
		if err := h.Sandbox.Provides(network); err != nil {
			return err
		}
		if network.Mode == NetworkAllow {
			if _, err := NewEgress(l.ID, own, func(string, string) error { return nil }, nil); err != nil {
				return fmt.Errorf("environment.systems.network: %w", err)
			}
		}
		return nil
	})
	if !ok {
		return
	}
	raw, err := codec.Marshal(&body, codec.Tony)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// A task is launched once while it runs: a second launch of a live id is answered by the task
	// that has it, which is what makes a launch safe to retry. A finished id runs again.
	h.mu.Lock()
	p := h.tasks[body.ID]
	if p != nil {
		if _, done := p.snapshot(); done {
			p = nil
		}
	}
	created := p == nil
	if created {
		ctx, abort := context.WithCancelCause(context.Background())
		p = &proc{id: body.ID, abort: abort, ready: make(chan struct{}), gone: make(chan struct{}),
			done:  make(chan struct{}),
			known: task.Snapshot{ID: body.ID, Prompt: body.Prompt, Agent: doerName(&body, h.describe), Occurrence: body.Occurrence},
			l:     &body}
		h.tasks[body.ID] = p
		go h.run(ctx, p, &body, raw)
	}
	h.mu.Unlock()

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeOut(w, r, status, p.current())
}

// run starts a task and follows it to its end. The task's process hosts it once start returns;
// a cancel that came first is carried out here, and the process, if it had started, is stopped.
func (h *SubprocessHandler) run(ctx context.Context, p *proc, l *runner.Launch, raw []byte) {
	defer close(p.done)
	err := h.start(ctx, p, l, raw)
	p.mu.Lock()
	if err == nil && ctx.Err() != nil {
		err = context.Cause(ctx)
	}
	p.hosted = err == nil
	p.mu.Unlock()
	if err == nil {
		close(p.ready)
		h.supervise(p)
		return
	}
	p.stop(h.Log)
	p.eg.Stop()
	if p.co != nil {
		p.co.abandon()
	}
	os.RemoveAll(p.dir)
	if ctx.Err() != nil {
		p.remember(p.as(task.Cancelled, context.Cause(ctx).Error()))
	} else {
		fmt.Fprintf(h.Log, "runnerd: %s: %v\n", p.id, err)
		p.remember(p.as(task.Failed, err.Error()))
	}
	close(p.ready)
}

// start makes the task's directory, checks out its repos in it, starts its process in the
// sandbox, and hands it the launch. What it leaves behind on an error, run cleans up.
func (h *SubprocessHandler) start(ctx context.Context, p *proc, l *runner.Launch, launch []byte) error {
	dir, err := os.MkdirTemp(h.Work, "task-")
	if err != nil {
		return fmt.Errorf("the task's directory could not be made: %w", err)
	}
	p.dir = dir
	root := dir
	var extra []*os.File
	// The checkout and the channel, wherever the runner performs anything for its tasks: a
	// task with no repos still asks it for a tracker's acts over the same channel.
	if h.Repos != nil {
		co, err := h.Repos.prepare(ctx, l, dir, h.Log)
		if err != nil {
			return fmt.Errorf("checking out the task's repos: %w", err)
		}
		p.co, root = co, co.ws.Root
		extra = append(extra, co.ch.Child()) // fd 5
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	network, own := h.network(l)
	if network.Mode == NetworkAllow {
		eg, err := ServeEgress(h.Control, p.id, own, p.connections(l, h.describe), h.Log)
		if err != nil {
			return err
		}
		p.eg, network.Egress = eg, eg.Sock()
		p.egress.Store(eg.Egress)
	}

	sock := fmt.Sprintf("%s/t%d.sock", h.Control, h.seq.Add(1))
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("the task's control socket: %w", err)
	}
	p.sock = sock
	ul := ln.(*net.UnixListener)
	// Closing a unix listener removes its path, and the path is the runner's half: the process
	// accepts on a duplicate of the listener, and the runner dials the path.
	ul.SetUnlinkOnClose(false)
	lf, err := ul.File()
	ln.Close()
	if err != nil {
		return fmt.Errorf("the task's control socket: %w", err)
	}
	defer lf.Close()
	lifeR, lifeW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer lifeR.Close()
	p.life = lifeW

	cmd := exec.Command(h.Command[0], h.Command[1:]...)
	cmd.Stdin = bytes.NewReader(h.Config)
	var env []string
	if h.Env != nil {
		env = h.Env(l)
	}
	cmd.Env = append(append([]string{}, env...), "HOME="+root, "TMPDIR=/tmp")
	out := tagged(h.Log, p.id)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.ExtraFiles = append([]*os.File{lf, lifeR}, extra...) // fd 3, fd 4, and fd 5
	if err := h.Sandbox.Wrap(cmd, root, network, h.Programs); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the task's process: %w", err)
	}
	p.cmd = cmd
	if p.co != nil {
		// The process has its own copy of its end now, and the runner answers on the other.
		_ = p.co.ch.Child().Close()
		go p.co.ch.Serve()
	}
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
		return err
	}
	if err := post(tr, base+"/tasks", launch); err != nil {
		return fmt.Errorf("the task's process refused the launch: %w", err)
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

// supervise follows a task to its end, remembers how it ended, and then stops its process,
// settles its repos and removes its directory. A process that exits without the task ending is
// a failed task.
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
			time.Sleep(h.linger)
			break
		}
	}
	p.stop(h.Log)
	p.remember(p.as(task.Failed, fmt.Sprintf(
		"the task's process exited (%s) before the task ended", p.exitStatus())))
	p.eg.Stop() // a way out outliving its task would be a route into the runner's network
	if p.co != nil && h.Repos.settle(p.co, h.Log) {
		return // the workspace is the only copy of work that could not be staged: kept
	}
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
			p.record(f)
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
func (h *SubprocessHandler) forward(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h.forwardOp(w, r, op) }
}

// forwardOp answers op on a task: from what the runner knows while the task is not hosted yet or
// has ended, and otherwise from the task's own process.
func (h *SubprocessHandler) forwardOp(w http.ResponseWriter, r *http.Request, op string) {
	id := r.PathValue("id")
	h.mu.Lock()
	p := h.tasks[id]
	h.mu.Unlock()
	if p == nil {
		http.NotFound(w, r)
		return
	}
	if _, done := p.snapshot(); !done && !p.isReady() {
		// Not hosted yet: its repos are being checked out, or its process started. A cancel is
		// the runner's to carry out, and a look is answered from what the runner knows; the
		// rest wait for the process.
		switch {
		case op == "cancel" && cancelEarly(w, r, p):
			return
		case op == "":
			writeOut(w, r, http.StatusOK, p.current())
			return
		}
		select {
		case <-p.ready:
		case <-r.Context().Done():
			return
		}
	}
	final, done := p.snapshot()
	if !done {
		switch op {
		case "":
			writeOut(w, r, http.StatusOK, p.current()) // the process's, with what its proxy refused
			return
		case "autonomy":
			h.replaceAutonomy(w, r, p)
			return
		case "resolve":
			h.relayResolve(w, r, p)
			return
		}
		p.prox.ServeHTTP(w, r)
		return
	}
	switch op {
	case "":
		writeOut(w, r, http.StatusOK, final)
	case "events":
		// A finished task's stream is what the runner followed of it, from the reader's cursor,
		// and then it closes: what a late reader gets from an InProcessHandler too. A task whose
		// process died before it said how it ended gets that end said for it.
		sse, err := codec.NewSSE(w, r)
		if err != nil {
			codec.HTTPError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		events := p.eventsSince(since)
		if n := len(events); n == 0 || !(events[n-1].Kind == task.EventStatus && task.Status(events[n-1].Text).Terminal()) {
			events = append(events, task.Event{Kind: task.EventStatus, Task: id, Text: string(final.Status), Err: final.Err})
		}
		if r.URL.Query().Get("since") == "" {
			events = events[len(events)-1:] // a live attach to an ended task gets its end
		}
		for _, e := range events {
			_ = sse.Send(e)
		}
		sse.Flush()
	// The rest as an InProcessHandler answers them for an ended task: a cancel, pause or resume
	// does nothing and says so with 204, a say or a new autonomy is 410, and no question is left
	// to resolve.
	case "cancel", "pause", "resume":
		w.WriteHeader(http.StatusNoContent)
	case "say", "autonomy":
		http.Error(w, fmt.Sprintf("task %s has ended (%s)", id, final.Status), http.StatusGone)
	case "resolve":
		http.Error(w, "unknown or already-answered decision", http.StatusNotFound)
	}
}

// connections is how the task's proxy decides each connection to a host that is none of the
// runner's own (connectionRefused): by the launch's environment, the task's current autonomy (the
// launch's until a replacement lands), and the network ceiling of its doer's offer in desc.
func (p *proc) connections(l *runner.Launch, desc runner.Description) func(host, port string) error {
	a := l.Autonomy
	p.autonomy.Store(&a)
	var ceiling systems.Ceiling
	if o, err := offerFor(l.Who(), desc); err == nil {
		ceiling = o.Systems["network"].Ceiling
	}
	return func(host, port string) error {
		return connectionRefused(l.Environment, *p.autonomy.Load(), ceiling, host, port)
	}
}

// replaceAutonomy is PUT /tasks/{id}/autonomy for a task its process hosts. The replacement is
// checked HERE, in the runner's process, against what the runner keeps fixed (CheckAutonomy: each
// row reaching the launch's environment, which lies within the ceilings) before the task is told
// of it, and
// once the task has taken it, it is set on the task's channel, so what the runner decides at
// each act follows the task from the next one. The task's process checks it again, and that is
// not what holds the line: it is the process the autonomy constrains.
func (h *SubprocessHandler) replaceAutonomy(w http.ResponseWriter, r *http.Request, p *proc) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	again := func() {
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.ContentLength = int64(len(data))
	}
	again()
	var body runner.AutonomyRequest
	if err := codec.ReadStrict(r, &body); err != nil {
		http.Error(w, fmt.Sprintf("the autonomy does not parse: %v", err), http.StatusBadRequest)
		return
	}
	if l := p.l; l != nil {
		if err := CheckAutonomy(body.Autonomy, l); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	again()
	rec := &statusRecorder{ResponseWriter: w}
	p.prox.ServeHTTP(rec, r)
	if rec.code == http.StatusNoContent {
		if p.co != nil {
			p.co.ch.SetAutonomy(body.Autonomy)
		}
		a := body.Autonomy
		p.autonomy.Store(&a)
	}
}

// relayResolve is POST /tasks/{id}/resolve for a task its process hosts: the answer to one of
// its questions, relayed to the process, and KEPT HERE too, with the call the question was about
// as the task's pending gate names it. An act a row asks about is performed by this process on
// such an answer and on nothing else (delivery.Task.Answered): the task's process holds the
// model and reports whatever it likes, and this side is the one that saw the answer given, and
// by whom. A question about no call (a bare ask) leaves nothing to keep.
func (h *SubprocessHandler) relayResolve(w http.ResponseWriter, r *http.Request, p *proc) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	again := func() {
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.ContentLength = int64(len(data))
	}
	again()
	var body runner.ResolveRequest
	if err := codec.ReadStrict(r, &body); err != nil {
		http.Error(w, fmt.Sprintf("the resolve does not parse: %v", err), http.StatusBadRequest)
		return
	}
	step, about := p.gateAbout(body.ID)
	again()
	if p.co != nil {
		// Marked in flight before the process hears it: the act it allows can reach the
		// channel before the process's word that it was taken comes back (Channel.Relaying).
		defer p.co.ch.Relaying()()
	}
	rec := &statusRecorder{ResponseWriter: w}
	p.prox.ServeHTTP(rec, r)
	switch {
	case rec.code != http.StatusNoContent:
		fmt.Fprintf(h.Log, "runnerd: %s: the answer %s to %s was not taken (%d); nothing kept\n", p.id, body.Choice, body.ID, rec.code)
	case p.co == nil:
		// A task with no channel performs nothing this side: nothing to keep an answer for.
	default:
		p.co.ch.Answer(body.ID, step, body.Choice, body.By)
		what := "about no call this side read"
		if about {
			what = "about " + step.String()
		}
		fmt.Fprintf(h.Log, "runnerd: %s: kept the answer %s to %s by %s, %s\n", p.id, body.Choice, body.ID, body.By, what)
	}
}

// gateAbout is the call a pending gate of the task is about, by the gate's id: from the task's
// snapshot, where its process answers, else from the events this side followed. False for a
// gate about no call, or one this side does not know.
func (p *proc) gateAbout(id string) (task.Step, bool) {
	if lister, ok := p.rt.(task.Lister); ok && lister != nil {
		if snap, err := lister.TaskSnapshot(p.id); err == nil {
			for _, g := range snap.Pending {
				if g.ID == id {
					return task.Step{Name: g.Step, Fields: g.Fields}, g.Step != ""
				}
			}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.events) - 1; i >= 0; i-- {
		if e := p.events[i]; e.Kind == task.EventPending && e.ID == id {
			return task.Step{Name: e.Step, Fields: e.Fields}, e.Step != ""
		}
	}
	return task.Step{}, false
}

// statusRecorder is a ResponseWriter that remembers the status it wrote.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// network is the network a launch's task gets, as the sandbox builds it, and the runner's own
// hosts its proxy reaches for it: the environment's network block's mode, absent being none, its
// connections decided by the proxy under allow (connections); and an agent reaches its model under
// any network, so under none or allow its proxy reaches AgentHosts too.
func (h *SubprocessHandler) network(l *runner.Launch) (Network, []string) {
	n := Network{Mode: l.Network().Mode}
	var own []string
	if l.Script == "" && n.Mode != NetworkOpen && len(h.AgentHosts) > 0 {
		n.Mode, own = NetworkAllow, slices.Clone(h.AgentHosts)
	}
	return n, own
}

// cancelEarly cancels a task its process doesn't host yet, and answers the request; false, with
// the request as it came, once the process hosts the task, whose cancel it then is.
func cancelEarly(w http.ResponseWriter, r *http.Request, p *proc) bool {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	again := func() { r.Body = io.NopCloser(bytes.NewReader(data)) }
	again()
	var req runner.CancelRequest
	if err := codec.Read(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	again() // for the process, should it host the task first
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hosted {
		return false
	}
	reason := cmp.Or(req.Reason, "no reason given")
	why := fmt.Errorf("cancelled: %s", reason)
	if req.By != "" {
		why = fmt.Errorf("cancelled by %s: %s", req.By, reason)
	}
	p.abort(why)
	// Said now: a first clone runs to its end whatever the task (workspace.Mirrors.Ensure), and
	// the task is over before it is.
	final := p.as(task.Cancelled, why.Error())
	p.final = &final
	w.WriteHeader(http.StatusNoContent)
	return true
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
		if final, done := p.snapshot(); done {
			out = append(out, final)
			continue
		}
		if !p.isReady() {
			out = append(out, p.as(task.Running, ""))
			continue
		}
		if final, done := p.snapshot(); done {
			out = append(out, final)
			continue
		}
		snap, err := p.rt.(task.Lister).TaskSnapshot(p.id)
		if err != nil {
			// Still the runner's task: leaving it out would read as a runner with less work.
			snap = p.as(task.Running, err.Error())
		}
		out = append(out, p.withRefused(snap))
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
			p.abort(errors.New("the runner is shutting down"))
			if !p.isReady() {
				// Not hosted yet, and maybe waiting on a first clone, which runs to its end: run
				// ends it when it returns, and a process it started dies with the runner (its
				// lifeline).
				return
			}
			// The process stops, and the task's own goroutine settles it: what didn't leave is
			// salvaged before its directory goes. Removing the directory here would race that, and
			// a rollout would lose whatever a task committed and didn't push.
			p.stop(h.Log)
			<-p.done
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
