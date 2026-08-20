package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// The parent half of the hop. This process is not a runtime any more: it is a PROXY that
// prepares a workspace, starts one contained process per task, and forwards the task-control
// contract to it.
//
// Everything verse asks — spawn, list, get, attach, resolve a gate, steer, cancel, rebind a
// vector — is the same request against the child, over a socket the task cannot dial. Nothing is
// translated, so nothing can drift: `local ≡ remote` holds one level deeper than it did.
//
// What stays here is exactly what must not cross: the git mirrors, the push credential, and
// delivery. And what this file has to add for the boundary to be honest is lifecycle. An
// in-process task cannot die without its runtime; a child can, and a runner that noticed only by
// its stream going quiet would leave verse waiting on a task nobody is running. So each child is
// supervised, its terminal status is REMEMBERED here, and a process that exits without one
// becomes a `failed` this side reports with the exit status in it.

// Timings. Variables rather than constants only so the tests can run the whole lifecycle without
// waiting out a linger designed for a human reader.
var (
	// How long a finished task's process is left alive, so a reader attached at the moment of
	// the terminal frame gets it rather than an EOF.
	lingerAfterTerminal = 3 * time.Second
	// How long a child gets to exit on its own after its lifeline closes, before it is killed.
	stopGrace = 5 * time.Second
	// How long a starting child gets to answer on its control socket. Generous: it opens a git
	// repository per repo in the workspace before it serves anything.
	startTimeout = 30 * time.Second
	// How often the parent asks a starting child whether it is up yet.
	startPoll = 20 * time.Millisecond
	// How long the snapshot gets to catch up with a terminal the read plane already reported.
	settleTimeout = 2 * time.Second
)

// egressServer is one task's way out: an allowlisting proxy on a unix socket this side owns.
// nil when the point did not ask for `network.egress: restricted`.
type egressServer struct {
	sock string
	ln   net.Listener
	srv  *http.Server
	eg   *Egress
}

// startEgress builds the proxy for one task and starts serving it.
func (r *Runner) startEgress(id string, env Environment) (*egressServer, error) {
	eg, err := NewEgress(id, env.Egress, r.log)
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(r.control, fmt.Sprintf("e%d.sock", r.seq.Add(1)))
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("egress socket: %w", err)
	}
	// 0600 on the socket is not the boundary — the boundary is that the only thing that can
	// reach this path is whatever the confiner bound it into. It is here because a runner that
	// shares a host with anything else should not widen by default.
	_ = os.Chmod(sock, 0o600)
	srv := &http.Server{Handler: eg}
	go func() { _ = srv.Serve(ln) }()
	return &egressServer{sock: sock, ln: ln, srv: srv, eg: eg}, nil
}

// insideAs is where this socket appears to the task, which only the confiner knows: one that
// remaps the filesystem binds it somewhere fixed, one that does not hands back what it was given.
// A nil server has no path, which is how a task with no restricted egress is told there is none.
func (e *egressServer) insideAs(c Confiner) string {
	if e == nil {
		return ""
	}
	return c.EgressAt(e.sock)
}

func (e *egressServer) stop() {
	if e == nil {
		return
	}
	_ = e.srv.Close()
	_ = os.Remove(e.sock)
}

// child is one task's process, from this side.
type child struct {
	id     string
	sock   string
	ws     *Workspace
	cmd    *exec.Cmd
	life   *os.File // write end of the lifeline; closing it asks the child to exit
	egress *egressServer
	svc    net.Conn // the delivery channel's parent end (deliver.go)
	rt     task.Runner
	prox   *httputil.ReverseProxy
	// gone is closed by the one goroutine that calls cmd.Wait, so any number of readers can
	// wait for the process and exactly one reaps it.
	gone chan struct{}

	mu    sync.Mutex
	exit  error
	final *task.Instance // the last word on this task, once there is one
	dead  bool
	// What delivery did, recorded when the task asked for it. Delivery is an action inside the
	// loop now (deliver.go), so this side finds out by being asked rather than by doing it —
	// and a task that was never asked is one whose work did not leave, which is what the
	// workspace decision below turns on.
	delivered Delivery
	asked     bool
}

func (c *child) recordDelivery(d Delivery) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = true
	c.delivered = d
}

func (c *child) delivery() (Delivery, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.delivered, c.asked
}

// exitStatus is what cmd.Wait said, readable after gone closes.
func (c *child) exitStatus() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exit == nil {
		return "status 0"
	}
	return c.exit.Error()
}

// running reports whether this task's process is still there. The record outlives it — that is
// what answers a `task get` about a task that has ended — so "this runner knows the id" and
// "the id is live" are different questions, and the spawn path needs the second one.
func (c *child) running() bool {
	select {
	case <-c.gone:
		return false
	default:
		return true
	}
}

func (c *child) snapshot() (task.Instance, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.final == nil {
		return task.Instance{}, false
	}
	return *c.final, true
}

// remember records the last word about a task. First writer wins: a terminal status observed on
// the read plane is a better account than "the process exited", and the process always exits
// afterwards.
func (c *child) remember(in task.Instance) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.final == nil {
		in.ID = c.id
		c.final = &in
	}
}

// settled is the snapshot to remember a finished task by.
//
// The read plane reaches terminal a BEAT BEFORE the snapshot does, and the gap is not a
// formality: the loop emits its last status during teardown, while the daemon records status and
// result when Run returns. Asking once, at the instant of the frame, therefore catches `running`
// with no result — and this side would then keep answering that forever, which is worse than not
// having remembered anything. Cost half an hour: what it looks like is a completed task whose
// event stream carries one `running` frame and then ends.
//
// So ask until it agrees with the plane that said so. If it never does, the frame is still the
// better authority for the STATUS, since the task did reach it.
func (c *child) settled(terminal task.Status) task.Instance {
	lister, ok := c.rt.(task.Lister)
	var last task.Instance
	for deadline := time.Now().Add(settleTimeout); ok && time.Now().Before(deadline); {
		snap, err := lister.TaskSnapshot(c.id)
		if err == nil {
			last = snap
			if snap.Status.Terminal() {
				return snap
			}
		}
		time.Sleep(startPoll)
	}
	last.ID, last.Status = c.id, terminal
	return last
}

// stop asks the child to exit, then makes it. Idempotent.
func (c *child) stop() {
	c.mu.Lock()
	if c.dead {
		c.mu.Unlock()
		return
	}
	c.dead = true
	c.mu.Unlock()

	if c.life != nil {
		_ = c.life.Close() // EOF on fd 4: the graceful ask
	}
	select {
	case <-c.gone:
	case <-time.After(stopGrace):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.gone
	}
	_ = os.Remove(c.sock)
	if c.svc != nil {
		_ = c.svc.Close()
	}
	// The way out closes with the task that had it. A proxy outliving its task is a route into
	// this runner's network with nobody's name on it.
	c.egress.stop()
}

// startChild prepares and starts one task process, and returns once it is answering.
func (r *Runner) startChild(id string, env Environment, point Point, ws *Workspace, driver string) (*child, error) {
	// The socket lives in the runner's own control directory, which is not under the workspace
	// and is not bind-mounted into any sandbox. The child never sees this path — it inherits the
	// listener as a descriptor — so this is the parent's handle and nobody else's.
	sock := filepath.Join(r.control, fmt.Sprintf("c%d.sock", r.seq.Add(1)))
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	ul := ln.(*net.UnixListener)
	// Closing a unix listener UNLINKS its path by default, and the path is the half the parent
	// keeps — so closing this one would take the socket out from under the child that is about
	// to serve on it. Costs half an hour to find, because what it looks like is a child that
	// starts and never answers.
	ul.SetUnlinkOnClose(false)
	lf, err := ul.File() // a dup: the child accepts on this, the parent dials the path
	ln.Close()
	if err != nil {
		_ = os.Remove(sock)
		return nil, fmt.Errorf("control socket: %w", err)
	}
	defer lf.Close()

	lifeR, lifeW, err := os.Pipe()
	if err != nil {
		_ = os.Remove(sock)
		return nil, err
	}
	defer lifeR.Close()

	// The delivery channel: a connected pair, one end inherited by the task process. A CONNECTED
	// socket rather than a path, because nothing but that process should be able to ask — a path
	// inside the sandbox is one the agent's shell could write to, and a task that can call
	// `deliver` itself is a task that delivers without passing its gate.
	svcParent, svcChild, err := socketPair()
	if err != nil {
		_ = os.Remove(sock)
		lifeW.Close()
		return nil, err
	}
	defer svcChild.Close()

	// The way out, when the point says there is a restricted one. It is a socket on this side of
	// the boundary, bound into the sandbox by the confiner and dialled by the task process —
	// which is what makes an allowlist a route rather than a setting.
	box := Sandbox{Dir: ws.Root}
	var eg *egressServer
	if point["network.egress"] == "restricted" {
		eg, err = r.startEgress(id, env)
		if err != nil {
			_ = os.Remove(sock)
			lifeW.Close()
			return nil, err
		}
		box.Egress = eg.sock
	}

	bases := map[string]string{}
	for _, t := range ws.Trees {
		bases[t.Repo.Name] = t.Head
	}
	cfg := childConfig{
		ID: id, Workspace: ws.Root, Branch: ws.Branch, Brief: ws.Brief(), Bases: bases,
		Environment: env, Agents: r.cfg.Agents, Egress: eg.insideAs(r.conf),
		LLM: LLMCreds{
			BaseURL: r.cfg.LLM.BaseURL, Model: r.cfg.LLM.Model,
			Key:        os.Getenv(r.cfg.LLM.APIKeyEnv),
			VirtualKey: os.Getenv(r.cfg.LLM.VirtualKeyEnv),
		},
	}
	blob, err := codec.Marshal(cfg, codec.Tony)
	if err != nil {
		_ = os.Remove(sock)
		lifeW.Close()
		eg.stop()
		return nil, err
	}

	cmd := exec.Command(r.self, "-task")
	cmd.Stdin = bytes.NewReader(blob)
	// The environment the whole task tree gets, built from the allowlist rather than inherited.
	//
	// This is where the credential split is actually enforced, and leaving it out was a hole
	// rather than an omission: the runner reads its git token out of its own environment, so a
	// child started with the default environment inherits it — and the harness's `run_bash` sets
	// no env of its own, so the task's SHELL had the push credential. An allowlist applied only
	// to the agent binary was never a floor; applied to the process that hosts everything, it is.
	cmd.Env = buildEnv(env.Env, env.Commands)
	// One writer for both streams, so a line on stderr cannot land inside a line on stdout.
	out := tagged(r.log, id)
	cmd.Stdout, cmd.Stderr = out, out
	// Order matters: fd 3 is the listener, fd 4 the lifeline, fd 5 the delivery channel (see
	// taskproc.go).
	cmd.ExtraFiles = []*os.File{lf, lifeR, svcChild}
	// The containment, built around the whole process — which is the point of the hop. Confine
	// rewrites Path and Args (bwrap becomes the program and this becomes its argument); ExtraFiles
	// ride along, since they are descriptors rather than arguments.
	if err := r.conf.Confine(cmd, point, box); err != nil {
		_ = os.Remove(sock)
		lifeW.Close()
		svcParent.Close()
		eg.stop()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(sock)
		lifeW.Close()
		svcParent.Close()
		eg.stop()
		return nil, fmt.Errorf("starting the task process: %w", err)
	}

	// One transport, so the supervisor and the proxy share a connection pool to this child.
	tr := unixTransport(sock)
	c := &child{
		id: id, sock: sock, ws: ws, cmd: cmd, life: lifeW, egress: eg, svc: svcParent,
		gone: make(chan struct{}),
		// The contract, over a transport that ignores the host and dials the socket. The URL is
		// what Ref reports and what the request line carries, so it names the peer rather than an
		// address anything could reach.
		//
		// Through hostSafe, because a task id is not a hostname. Every id a TRIGGER spawns is
		// `<chain>@<subject>` — `comment-drift@drift.head.head-moved@git.head.verse` — and an @
		// makes the rest of it userinfo, after which the colons read as a port and the url does
		// not parse. So no charter-spawned task could start here at all, while every
		// hand-launched `run-<hex>` one could.
		rt:   runner.HTTPOver("http://task."+hostSafe(id), tr),
		prox: unixProxy(tr, id),
	}
	go func() {
		err := cmd.Wait()
		c.mu.Lock()
		c.exit = err
		c.mu.Unlock()
		close(c.gone)
	}()

	if err := c.awaitReady(); err != nil {
		c.stop()
		return nil, err
	}
	return c, nil
}

// awaitReady blocks until the child answers the cheapest question in the contract, or dies
// trying. `GET /tasks` is the same probe `verse runner add` uses — a runner that answers it is a
// runner, and one that does not is not ready to be handed a launch.
func (c *child) awaitReady() error {
	lister, _ := c.rt.(task.Lister)
	deadline := time.Now().Add(startTimeout)
	var last error
	for time.Now().Before(deadline) {
		select {
		case <-c.gone:
			return fmt.Errorf("the task process exited before it served anything (%s) — its "+
				"output is on this runner's log, tagged %s", c.exitStatus(), c.id)
		default:
		}
		if _, err := lister.ListTasks(); err == nil {
			return nil
		} else {
			last = err
		}
		time.Sleep(startPoll)
	}
	return fmt.Errorf("the task process did not answer within %s: %v", startTimeout, last)
}

// spawn is POST /tasks: prepare a workspace, start a contained process, and forward the launch
// to it verbatim.
//
// The body is forwarded rather than re-encoded. This runner reads three things out of it — which
// environment, which driver, and the id — and everything else (frame, tools, answer contract,
// vector, grant, provenance) is the child's business and travels untouched. A proxy that
// re-marshalled would silently drop whatever it had not been taught about yet, which is the one
// failure this seam must not have.
func (r *Runner) spawn(w http.ResponseWriter, req *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	format := codec.Tony
	if f, ok := codec.FormatForMedia(req.Header.Get("Content-Type")); ok {
		format = f
	}
	var body ir.Node
	if err := codec.Unmarshal(raw, &body, format); err != nil {
		http.Error(w, fmt.Sprintf("the spawn body does not parse: %v", err), http.StatusBadRequest)
		return
	}

	id := field(&body, "id")
	if id == "" {
		// Minted here rather than by the child, because the workspace and the branch are named
		// after it and both exist before any child does.
		id = fmt.Sprintf("p%d", r.seq.Add(1))
		setField(&body, "id", id)
	}
	// An id is spawned ONCE WHILE IT RUNS, and a repeated spawn of a live one is answered by it
	// — the same retry-safety taskd has, which a client whose request timed out depends on.
	//
	// A FINISHED one is a different question, and answering it the same way was wrong twice
	// over. The record outlives the process on purpose, so that `forward` can keep answering
	// about a task that has ended — but the socket goes with the process, so this proxied into a
	// path that no longer exists and the spawn came back `502: the process running task … did
	// not answer: dial unix …: no such file or directory`.
	//
	// And a repeat is not an anomaly. The engine mints `<chain>@<subject>`, deterministic ON
	// PURPOSE, and its own dedupe lasts only while the task is live — so a rule firing twice
	// about one subject is a second run of the same id, by design. Treating the id as spent
	// meant a charter could run once per subject per runner lifetime: the first review worked,
	// every later one 502'd, and only a restart cleared it.
	//
	// Falling through re-runs it, and `r.children[id] = c` below replaces the record: the id now
	// names the new run, which is what every reader of it wants. The old workspace is already
	// gone, removed at reap unless it held work nothing could stage.
	if c := r.childOf(id); c != nil && c.running() {
		if _, done := c.snapshot(); !done {
			c.prox.ServeHTTP(w, requestWithBody(req, raw))
			return
		}
	}

	// Every refusal below is an HTTP error rather than a task that fails immediately, and that
	// is the improvement the hop brings here: runner.HTTP reads the status, so a launch this
	// runner will not host comes back as `spawn refused by <url>: <reason>` on the flow that
	// asked for it, instead of a task that exists for one tick.
	env, err := r.cfg.Environment(field(&body, "environment"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The containment this environment declares, checked BEFORE any work is set up. A runner
	// that cannot provide the floor does not run the task with a weaker one: that difference is
	// invisible from the outside, which is the direction that costs.
	point := pointFor(env)
	if err := r.conf.Provides(point); err != nil {
		http.Error(w, fmt.Sprintf("environment %q: %v", env.Name, err), http.StatusBadRequest)
		return
	}
	driver := field(&body, "driver")
	if driver == "" {
		driver = orDefault(env.Driver, driverLLM)
		// Written back so the snapshot reports what actually ran. A launch that named no driver
		// used to be recorded as having none, which is the one thing a reader cannot recover.
		setField(&body, "driver", driver)
	}
	// A launch that cannot possibly finish is refused here rather than at the first model call,
	// where it arrives as somebody else's error message about an invalid key.
	if driver == driverLLM {
		if why := r.cfg.MissingLLMCreds(); why != "" {
			http.Error(w, why, http.StatusBadRequest)
			return
		}
	}
	if _, isAgent := r.cfg.Agents[driver]; !builtinDriver(driver) && !isAgent {
		http.Error(w, fmt.Sprintf(
			"this runner has no driver %q — it has %q, %q, and the agents it declares: %s",
			driver, driverLLM, task.DriverCalls,
			strings.Join(agentNames(r.cfg.Agents), ", ")), http.StatusBadRequest)
		return
	}

	ws, err := r.mirrors.Open(req.Context(), id, env, r.workspaces)
	// The mirrors this spawn just fetched are the mirrors anything undelivered is waiting in, so
	// this is the cheapest place to retry: the fetch has already taken the lock and paid the
	// round trip. In the background, because a backlog against a remote that is down must not
	// hold up a task that has nothing to do with it.
	go func() {
		for _, repo := range env.Repos {
			r.reflect(context.Background(), env, repo)
		}
	}()
	if err != nil {
		http.Error(w, fmt.Sprintf("environment %q: %v", env.Name, err), http.StatusInternalServerError)
		return
	}
	c, err := r.startChild(id, env, point, ws, driver)
	if err != nil {
		_ = ws.Remove(context.Background())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fwd, err := codec.Marshal(&body, codec.Tony)
	if err != nil {
		c.stop()
		_ = ws.Remove(context.Background())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	r.mu.Lock()
	r.children[id] = c
	r.mu.Unlock()

	req2 := requestWithBody(req, fwd)
	req2.Header.Set("Content-Type", codec.MediaType(codec.Tony))
	c.prox.ServeHTTP(w, req2)

	// Supervision starts only once the child HOSTS the task, and the order is not cosmetic.
	// Subscribing first means subscribing to a task that does not exist yet, which the contract
	// answers 404 — and a refused subscribe is reported on the plane the caller is reading, as a
	// FAILED status frame. So every task was declared dead the instant it was started, by the
	// one thing watching it. Cost an afternoon; the symptom was a task killed mid-run with no
	// failure anyone could point at.
	//
	// The launch this side keeps is what delivery writes into the commit trailers: the only
	// externally checkable link back into the verse from a push made under a shared credential.
	// See docs/working/attribution.md.
	prov := ir.Get(&body, "provenance")
	l := task.Launch{
		ID: id, Prompt: field(&body, "prompt"), Driver: driver, Environment: env.Name,
		Provenance: task.Provenance{
			Principal: field(prov, "principal"),
			Subject:   field(prov, "subject"),
			Chain:     field(prov, "chain"),
		},
	}
	// The delivery channel is served for as long as the task process lives: it is how the task's
	// own closing action reaches the credential this side holds.
	go r.servePush(c, c.svc)
	go r.supervise(c, l, env)
}

// supervise watches one child to its end, records the last word about it, delivers what it
// produced, and reaps it.
//
// It watches the READ PLANE rather than waiting on the process, because the two say different
// things: the stream carries the terminal status the task reached, and the process exiting says
// only that it is gone. Both end this loop — a stream that closes because its peer died leaves
// `terminal` false, which is precisely the case that needs a synthesized failure.
func (r *Runner) supervise(c *child, l task.Launch, env Environment) {
	h := c.rt.Attach(c.id)
	frames, cancel := h.Subscribe()
	var terminal task.Status
	for f := range frames {
		if f.Kind == task.EvStatus && task.Status(f.Text).Terminal() {
			terminal = task.Status(f.Text)
			break
		}
	}
	cancel()

	if terminal != "" {
		c.remember(c.settled(terminal))
		// Leave it up a moment: a reader that attached just before the terminal frame is still
		// draining, and cutting the socket now turns a completed task into a broken stream.
		time.Sleep(lingerAfterTerminal)
	}
	c.stop()
	// Whatever we did not learn from the read plane, the exit status says. A process that ended
	// without a terminal status is a FAILED task and must be reported as one: verse is otherwise
	// waiting on a task nobody is running, which is the failure mode the in-process version could
	// not have and this one can.
	c.remember(task.Instance{
		Status: task.Failed,
		Err: fmt.Sprintf("the task process exited (%s) without reaching a terminal status",
			c.exitStatus()),
	})

	ctx := context.Background()
	// Delivery is no longer something this function does. It is the task's own closing action,
	// gated by its vector and recorded on its read plane (deliver.go, task.Finisher) — so what
	// happens here is only the decision about the workspace, from what the task ASKED for.
	//
	// The difference is not bookkeeping. Pushing from out here meant pushing whatever was in the
	// tree regardless of what the vector said about it, after the task was already terminal; a
	// charter could not decline it, and nothing downstream could chain off it. Now a task that
	// was never asked to deliver is one whose work did not leave, and that is a fact about the
	// run rather than about this function's ordering.
	d, asked := c.delivery()
	// Work that did not leave is SALVAGED into the mirror rather than left in a directory
	// somebody has to know to look in — under `held`, because it was never authorized to go
	// anywhere and a retry must not push it (stage.go).
	//
	// This covers three endings that used to be one ugly rule: a gate that refused the push, a
	// task that failed before reaching its delivery step, and a process that died. In all three
	// the runner has work and nobody said it could leave.
	//
	// `asked` is now the whole test, and what it means has moved with the credential. It used to
	// mean "this side pushed something"; it means "this side authorized something and staged it
	// on the way", which is the strongest thing a process that no longer performs the push can
	// truthfully assert. Whether it LANDED is the remote's to say — the pending ref settles
	// itself on the next sweep, below.
	if !asked || (c.ws.Deliverable() && len(d.Staged) == 0) {
		held := c.ws.Salvage(ctx, l)
		for _, ref := range held.Staged {
			fmt.Fprintf(r.log, "coderunner: %s: the work did not leave; it is in this runner's "+
				"mirror at %s — GET /runner lists it with the command to fetch it\n", c.id, ref)
		}
		if held.Err != nil {
			fmt.Fprintf(r.log, "coderunner: %s: salvage: %v\n", c.id, held.Err)
		}
		d.Loose = append(d.Loose, held.Loose...)
	}
	// A workspace is removed only when nothing is LOOSE in it. It used to be removed
	// unconditionally, so a delivery that failed — or that found nothing — destroyed the tree the
	// work was in: not a lost push, a lost result, since the commit lived only in the task's own
	// clone. Observed once, on fourteen verified findings.
	//
	// Staging is what makes the ordinary answer "remove". The work is in the mirror before any
	// push is attempted, so a failed delivery is no longer a reason to keep a checkout — only a
	// failure to stage is, and that one is the disk rather than the network.
	switch {
	case r.keep:
		fmt.Fprintf(r.log, "coderunner: %s: workspace kept at %s\n", c.id, c.ws.Root)
	case len(d.Loose) > 0:
		fmt.Fprintf(r.log, "coderunner: %s: KEEPING %s — %s could not be staged, so this is the "+
			"only copy of the work\n", c.id, c.ws.Root, strings.Join(d.Loose, ", "))
	default:
		if err := c.ws.Remove(ctx); err != nil {
			fmt.Fprintf(r.log, "coderunner: %s: remove workspace: %v\n", c.id, err)
		}
	}
	// And settle whatever this task was authorized to push. The task did the pushing, so this
	// side does not know what became of it — the sweep asks the remote, which is the authority:
	// a ref that landed is already-up-to-date and goes, one that did not is retried, and one the
	// remote refuses moves to `held` so it is not retried forever (stage.go). Here rather than
	// only on the next spawn, so `GET /runner` stops listing work the world already has within
	// seconds of a task ending rather than whenever somebody next uses this repository.
	for _, repo := range env.Repos {
		r.reflect(ctx, env, repo)
	}
}

// forward is every other route: /tasks/{id} and /tasks/{id}/<op>.
//
// A live child is proxied. A dead one is answered from what this side remembered, which is the
// half a proxy alone cannot do — the contract has to keep answering about a task after the
// process that ran it is gone, or a restart-time re-attach reads as "no such task" and a
// completed run looks like a lost one.
func (r *Runner) forward(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	c := r.childOf(id)
	if c == nil {
		http.NotFound(w, req)
		return
	}
	final, done := c.snapshot()
	if !done {
		c.prox.ServeHTTP(w, req)
		return
	}
	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/tasks/"+id:
		codec.Write(w, req, http.StatusOK, final)
	case req.Method == http.MethodGet && req.URL.Path == "/tasks/"+id+"/events":
		// One frame and close. A late subscriber to a finished task gets exactly this from the
		// local bus too, so a reader cannot tell the hop happened.
		sse, err := codec.NewSSE(w, req)
		if err != nil {
			codec.HTTPError(w, req, http.StatusInternalServerError, err.Error())
			return
		}
		_ = sse.Send(task.Frame{Kind: task.EvStatus, Text: string(final.Status), Err: final.Err})
		sse.Flush()
	case req.URL.Path == "/tasks/"+id+"/cancel":
		// Already stopped, which is what the caller asked for. Answering an error here would make
		// a cancel race report a failure for having succeeded early.
		w.WriteHeader(http.StatusNoContent)
	default:
		// Any other control over something already terminal. 409 rather than 404: the task is
		// known, and the difference matters to a caller deciding whether to retry.
		http.Error(w, fmt.Sprintf("task %s has finished (%s)", id, final.Status), http.StatusConflict)
	}
}

// list is GET /tasks: every task this runner is hosting or has hosted, asked of the process that
// holds it or read from what this side remembered.
func (r *Runner) list(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	kids := make([]*child, 0, len(r.children))
	for _, c := range r.children {
		kids = append(kids, c)
	}
	r.mu.Unlock()

	out := make([]task.Instance, 0, len(kids))
	for _, c := range kids {
		if final, done := c.snapshot(); done {
			out = append(out, final)
			continue
		}
		lister, ok := c.rt.(task.Lister)
		if !ok {
			continue
		}
		snap, err := lister.TaskSnapshot(c.id)
		if err != nil {
			// A child that is up but not answering is still a task this runner is responsible
			// for. Reporting nothing would read as a runner with less work than it has.
			snap = task.Instance{ID: c.id, Status: task.Running, Err: err.Error()}
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	codec.Write(w, req, http.StatusOK, out)
}

func (r *Runner) childOf(id string) *child {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.children[id]
}

// Shutdown stops every task process. Without it a restart leaves orphans holding workspaces —
// bwrap's --die-with-parent covers the crash case on Linux and nothing covers it on macOS, which
// is what the lifeline is for.
func (r *Runner) Shutdown() {
	r.mu.Lock()
	kids := make([]*child, 0, len(r.children))
	for _, c := range r.children {
		kids = append(kids, c)
	}
	r.mu.Unlock()
	for _, c := range kids {
		c.stop()
	}
	_ = os.RemoveAll(r.control)
}

// hostSafe renders a task id as something a URL host may hold: everything but a letter, a
// digit and a dash becomes a dash. It is not an encoding and does not have to be reversible —
// nothing decodes it. The host is a NAME here, printed in a request line and in Ref, while the
// transport dials a socket and ignores it entirely.
func hostSafe(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		}
		return '-'
	}, id)
}

// unixTransport dials one child's control socket whatever host the URL names.
func unixTransport(sock string) http.RoundTripper {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}
}

func unixProxy(tr http.RoundTripper, id string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: "task." + id})
			pr.Out.Host = "task." + id
		},
		Transport: tr,
		// Immediately, because the read plane is SSE and a buffered event stream is a stream
		// nobody can watch live. The control bodies here are a few hundred bytes, so there is
		// nothing to trade away.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, fmt.Sprintf("the process running task %s did not answer: %v", id, err),
				http.StatusBadGateway)
		},
	}
}

// requestWithBody re-bodies a request that has already been read, so it can be forwarded.
func requestWithBody(req *http.Request, body []byte) *http.Request {
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return out
}

// field reads one string off a decoded document. Reading the launch this way rather than into a
// struct is deliberate: a struct here would be a second copy of taskd's wire shape, and the two
// would drift the first time a field was added to one of them.
func field(n *ir.Node, name string) string {
	if n == nil {
		return ""
	}
	v := ir.Get(n, name)
	if v == nil {
		return ""
	}
	return v.String
}

func setField(n *ir.Node, name, value string) {
	n.Fields = append(n.Fields, ir.FromString(name))
	n.Values = append(n.Values, ir.FromString(value))
}

// tagged prefixes a child's output with its task id, so several tasks on one runner produce a log
// somebody can read.
func tagged(w io.Writer, id string) io.Writer { return &tagWriter{w: w, tag: "[" + id + "] "} }

type tagWriter struct {
	w     io.Writer
	tag   string
	mu    sync.Mutex
	fresh bool
}

func (t *tagWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, line := range bytes.SplitAfter(p, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		if !t.fresh {
			_, _ = io.WriteString(t.w, t.tag)
			t.fresh = true
		}
		_, _ = t.w.Write(line)
		if line[len(line)-1] == '\n' {
			t.fresh = false
		}
	}
	return len(p), nil
}
