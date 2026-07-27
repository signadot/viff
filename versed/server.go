// Package versed is the reactive engine as a service: it assembles the entity
// store, the trigger engine, and a gate registry, and serves an HTTP surface that
// the `verse` CLI drives. The surface keeps the two planes distinct — observation
// (watch, entity reads) separate from control (gate resolve, and later flow
// steering) — per docs/deployment-and-steering.md.
//
// Scaffold status: the process runtime (proc) is not yet mounted; spawning actions
// no-op until it is wired.
package versed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
	"github.com/signadot/verse/process/processd"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/source"
	"github.com/signadot/verse/trigger"
)

// Server is the monolith's in-process assembly: one store, one engine, one gate
// registry, and the process runtime, behind one HTTP surface.
type Server struct {
	Store  entity.Store
	Reg    *process.Registry
	Eng    *trigger.Engine
	Procd  *processd.Daemon
	runner process.Runner // how processes are run/governed; local (processd) today

	// Addr/BrokerAddr are this verse's OWN listen addresses — the operator API and (if
	// served) the broker face — published on /status so a client or worker can discover
	// where verse listens, as the docd addresses are published for a controller. Set by the
	// process that binds them (cmd/verse up); empty until then.
	Addr       string
	BrokerAddr string

	// LLM is a secrets-safe summary of the process runtime's LLM config, published on
	// /status so you can see whether agent creds are configured on THIS server (the one
	// that hosts the runtime) — the common gotcha. Set by `up`; nil when unconfigured.
	LLM *api.LLM

	// DocdClient/DocdMount are the addresses of the docd this verse is backed by,
	// when store-kind is docd. They are published on GET /status so a separately-run
	// controller process (e.g. `verse connect`) can find docd's mount face without
	// guessing the ephemeral ports `up` chose.
	DocdClient string
	DocdMount  string

	// ctx is the lifetime captured at Start; scoped engines start under a child of it.
	ctx context.Context
	// Per-scope engines. A trigger installed under ?scope=<s> runs on a dedicated
	// engine observing store.Scope(s) — the reactivity for one dev's federation
	// (verse connect) lives in its own copy-on-write scope, isolated from baseline and
	// from other scopes. Created lazily on the first scoped trigger, stopped when the
	// scope is dropped (disconnect).
	scopeMu   sync.Mutex
	scopeEngs map[string]*scopeEngine

	// brokers are worker-facing handlers hosted under /broker/<nonce> (see BrokerHandler).
	// The nonce is a capability: knowing the URL is the grant to dial in as that runner's
	// worker. The well-known "local" nonce is handled specially (the in-process supervisor).
	brokerMu sync.Mutex
	brokers  map[string]http.Handler

	// routes is the published route table, filled in by Handler as it registers each
	// route — never maintained separately. See routes.go.
	routes []api.Route
}

// scopeEngine is a per-scope engine plus the cancel that stops it (on scope delete).
type scopeEngine struct {
	eng    *trigger.Engine
	cancel context.CancelFunc
}

// NewWithStore assembles the server with the given process driver builder (scripted,
// LLM, …) over a store. The engine resolves trigger gates through the shared registry,
// so a human answers them out-of-band via POST /gate/resolve; a trigger's spawn action
// goes through the process runtime (hosted, mirrored into the verse).
func NewWithStore(build processd.Builder, store entity.Store) *Server {
	reg := process.NewRegistry()
	eng := trigger.New(store, reg)
	procd := processd.New(build)
	s := &Server{Store: store, Reg: reg, Eng: eng, Procd: procd, scopeEngs: map[string]*scopeEngine{}}
	s.runner = localRunner{procd} // the in-process runner ("/broker/local")

	// The Spawner owns the full lifecycle: host it via the runner, then mirror
	// its state into the verse so triggers can react to its completion.
	eng.SetSpawner(s.spawnerFor(store, nil))
	return s
}

// localRunner is the in-process runner: it hosts processes on the embedded
// processd runtime — the "/broker/local" handler. Remote runners (a base
// URL: k8s, lambda, a human broker) implement the same process.Runner behind the
// wire contract; the engine drives them through the identical seam. See docs/process-control.md.
type localRunner struct{ d *processd.Daemon }

func (l localRunner) Spawn(id, task, driver string, content process.ContentReader) process.Handle {
	return l.d.Spawn(id, task, driver, content)
}

// Attach returns the still-live in-process handle for id, or nil — after a restart the
// in-memory runtime is gone, so the caller orphans (a local process cannot outlive verse).
func (l localRunner) Attach(id string) process.Handle {
	if p := l.d.Lookup(id); p != nil {
		return p
	}
	return nil
}

func (localRunner) Ref() string { return "local" }

// SetRunner rebinds how this server runs processes — e.g. to a remote runner base
// URL (runner.HTTP) instead of the in-process local supervisor. The spawnerFor
// closures read s.runner at spawn time, so this takes effect for subsequent spawns
// (baseline and scoped). content federation is not forwarded to a remote runner yet.
func (s *Server) SetRunner(p process.Runner) { s.runner = p }

// UseBroker installs an in-process dial-in broker as the runner, hosted under a
// minted nonce, and returns it (e.g. for the operator to Revoke a claim). face is this
// verse's externally-reachable broker base (scheme://host:broker-addr — what a worker can
// reach); the broker publishes <face>/broker/<nonce> as each process's `dialURL`, closing
// the mint→publish→dial loop so a worker is told where to dial in rather than pre-configured.
func (s *Server) UseBroker(face string) *broker.Broker {
	b := broker.New()
	nonce := s.HostBroker("", b.WorkerHandler())
	b.SetBase(strings.TrimRight(face, "/") + "/broker/" + nonce)
	s.SetRunner(b)
	return b
}

// BrokerHandler serves this verse's broker face — the runner contract hosted under
// /broker/<nonce>, so a remote verse or a worker reaches a runner through verse's own URL
// rather than a separate service (see docs/process-control.md "Hosting"). The well-known
// "local" nonce is the built-in supervisor (processd); any other nonce is a handler
// registered via HostBroker (e.g. a dial-in broker's WorkerHandler). It is intended for a
// SIBLING port, kept distinct from the trusted operator API.
func (s *Server) BrokerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/broker/{nonce}/", func(w http.ResponseWriter, r *http.Request) {
		nonce := r.PathValue("nonce")
		h := s.brokerFor(nonce)
		if h == nil {
			http.NotFound(w, r)
			return
		}
		// Strip /broker/<nonce> so the mounted handler sees its own /processes/... paths,
		// exactly as it would if hosted at a root.
		http.StripPrefix("/broker/"+nonce, h).ServeHTTP(w, r)
	})
	return mux
}

// brokerFor resolves a nonce to its worker-facing handler. "local" is the built-in
// supervisor; every other nonce must have been registered via HostBroker.
func (s *Server) brokerFor(nonce string) http.Handler {
	if nonce == "local" {
		return s.Procd.Handler()
	}
	s.brokerMu.Lock()
	defer s.brokerMu.Unlock()
	return s.brokers[nonce]
}

// HostBroker mounts a worker-facing handler under /broker/<nonce> and returns the nonce a
// worker dials. Pass a well-known nonce to pin the URL, or "" to mint a fresh unguessable
// one — the nonce is the capability, so an unguessable mint is the private default. The
// handler is expected to serve the worker-facing dial-in protocol (claim/inbox/report),
// e.g. broker.Broker.WorkerHandler().
func (s *Server) HostBroker(nonce string, h http.Handler) string {
	if nonce == "" {
		nonce = mintNonce()
	}
	s.brokerMu.Lock()
	if s.brokers == nil {
		s.brokers = map[string]http.Handler{}
	}
	s.brokers[nonce] = h
	s.brokerMu.Unlock()
	return nonce
}

// mintNonce returns an unguessable path segment used as a broker capability.
func mintNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// spawnerFor builds a Spawner that hosts a process via the runner and mirrors its
// state into st — the baseline store for the baseline engine, a scoped store for a
// per-scope engine, so a scoped flow's process state stays in that scope. content (nil
// for baseline) routes a spawned agent's read_file through the verse.
func (s *Server) spawnerFor(st entity.Store, content process.ContentReader) trigger.Spawner {
	return func(id string, spec launch.Spec) process.Handle {
		h := s.runner.Spawn(id, spec.Task, spec.Driver, content)
		mirror.Reflect(st, id, h, mirror.Meta{Subject: spec.Subject, Runner: s.runner.Ref(), Chain: spec.Chain, Task: spec.Task})
		return h
	}
}

// reconcileProcesses runs on Start: for each non-terminal process:instance, either RESUME
// (a remote runner still running the work — re-attach and re-mirror from the entity's
// runner + cursor) or ORPHAN (a local in-process runtime that died with the previous
// run). This refines the old orphan-only reconcile now that a process can outlive verse
// behind a remote runner. Control re-tracking (cancel/say a resumed process) is a
// follow-on; this keeps the entity truthful.
func (s *Server) reconcileProcesses() {
	var resumed []resumedRun
	for _, ent := range s.Store.List(func(r entity.Ref) bool { return r.System == mirror.System }) {
		if ent.Ref.Kind == mirror.ClaimKind {
			// A run claim, not a process. It is cleared below rather than here, and it is
			// named explicitly so it is not merely skipped by accident (a claim carries no
			// `status`, so it would fall through the check that follows).
			continue
		}
		status := entity.Str(ent.Payload, "status")
		if status == "" || process.TerminalStatus(status) {
			continue
		}
		runnerRef := entity.Str(ent.Payload, "runner")
		subject := entity.Str(ent.Payload, "flow")
		// Reflect REPLACES the entity's payload, so provenance it does not carry is lost on
		// re-attach. Read the chain back off the entity and hand it straight in: it is the
		// address of the action that spawned this process, and a resumed process should not
		// forget where it came from. AdoptChain re-derives its hooks from that address —
		// see docs/trigger-model.md.
		chain := entity.Str(ent.Payload, "chain")
		if runnerRef != "" && runnerRef != "local" { // remote: the work outlived verse
			if h := runner.HTTP(runnerRef).Attach(ent.Ref.ID); h != nil {
				// re-attach: observe
				mirror.Reflect(s.Store, ent.Ref.ID, h, mirror.Meta{Subject: subject, Runner: runnerRef, Chain: chain})
				// re-track: steerable, AND its hooks rehydrated from the chain, so the
				// chain or sequence it belongs to carries on rather than ending silently.
				s.Eng.AdoptChain(subject, ent.Ref.ID, chain, h)
				resumed = append(resumed, resumedRun{subject: subject, chain: chain})
				continue
			}
		}
		// local (or unresolvable): the in-process runtime is gone → orphan. Only the status
		// changes, so only the status is written — the rest of what the process recorded is
		// none of this reconcile's business.
		s.write(ent.Ref.At("status"), entity.Val(string(process.Orphaned)))
	}
	s.reconcileClaims(resumed)
}

// write applies a value at a path during startup reconciliation, reporting a failure on
// stderr. Reconcile runs before the server serves, with no request to fail: a write that did
// not land leaves the verse claiming something untrue about a process, and the operator
// deserves to see that at the point it happens rather than to deduce it later.
func (s *Server) write(at entity.Path, v entity.Payload) {
	if _, err := s.Store.Write(entity.Write{At: at, Value: v}); err != nil {
		fmt.Fprintf(os.Stderr, "verse: reconcile: write %s: %v\n", at, err)
	}
}

// resumedRun is a process that survived the restart, and the chain it came from — enough to
// tell whether the sequence a claim guards is still alive.
type resumedRun struct{ subject, chain string }

// reconcileClaims drops run claims whose run did not survive the restart.
//
// A claim is an exclusive lease on a sequence, released when the sequence ends. Nothing
// releases one across a restart, so the question is whether the run it guards survived.
//
// It survives when its process was re-adopted WITH its hooks — that process will complete,
// and its completion advances the sequence. Then the claim must stay: dropping it would let a
// concurrent firing start a second run alongside the one still going.
//
// It does not survive when the process was orphaned (a local runtime cannot outlive verse) or
// when nothing was resumed at all. Then no completion is coming, and a surviving claim would
// block that sequence forever with no live process to explain why.
//
// So a claim is exactly as durable as the run it guards — which is what makes a durable lease
// safe rather than a liability, and is only decidable now that resume rehydrates hooks.
//
// Scoped engines hold their claims in their own scope stores; those are created lazily and
// are not walked here. A scoped sequence therefore keeps a stale claim across a restart —
// worth fixing when scoped resume is, since both are the same gap.
func (s *Server) reconcileClaims(resumed []resumedRun) {
	for _, ent := range s.Store.List(func(r entity.Ref) bool {
		return r.System == mirror.System && r.Kind == mirror.ClaimKind
	}) {
		if claimIsLive(ent, resumed) {
			continue
		}
		s.write(ent.Ref.At(), nil) // release: nothing stands there any more
	}
}

// claimIsLive reports whether any resumed process belongs to the sequence this claim holds:
// same subject, and a chain at or below the claim's own.
func claimIsLive(claim entity.Entity, resumed []resumedRun) bool {
	subject := entity.Str(claim.Payload, "flow")
	chain := entity.Str(claim.Payload, "chain")
	if subject == "" || chain == "" {
		return false
	}
	for _, r := range resumed {
		if r.subject == subject && trigger.ChainUnder(chain, r.chain) {
			return true
		}
	}
	return false
}

// storeContentReader resolves a federated file's content by fetching its
// local:dir-content-<scope>:<path> entity — served on demand by the connect content
// controller through docd, never stored. It reads via the baseline store, since the
// content mount is baseline (mounts are not scope-specific).
type storeContentReader struct {
	store entity.Store
	kind  string // dir-content-<scope>
}

func (c storeContentReader) ReadContent(path string) (string, bool, error) {
	e, ok, err := c.store.Get(entity.Ref{System: source.System, Kind: c.kind, ID: path})
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	body := entity.Get(e.Payload, "content")
	if body == nil {
		return "", false, nil
	}
	return body.String, true, nil
}

// engFor returns the engine serving a scope for read/manage operations: the baseline
// engine for the empty scope, the scope's engine if one exists, or nil if that scope
// has no engine yet (reads treat nil as empty — never create an engine on a GET).
func (s *Server) engFor(scope string) *trigger.Engine {
	if scope == "" {
		return s.Eng
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	if se, ok := s.scopeEngs[scope]; ok {
		return se.eng
	}
	return nil
}

// scopeEngine returns the engine for a scope, creating and starting it on first use.
// It observes store.Scope(scope) — one dev's copy-on-write federation — and spawns
// into that same scope. Requires Start to have captured the lifetime ctx.
func (s *Server) scopeEngine(scope string) *trigger.Engine {
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	if se, ok := s.scopeEngs[scope]; ok {
		return se.eng
	}
	scoped := s.Store.Scope(scope)
	eng := trigger.New(scoped, s.Reg)
	// Scoped agents read file content through the verse (the connect content
	// controller), so a validator isn't tied to the machine holding the files.
	content := storeContentReader{store: s.Store, kind: source.ContentPrefix + scope}
	eng.SetSpawner(s.spawnerFor(scoped, content))
	sctx, cancel := context.WithCancel(s.ctx)
	// Per-trigger observation: no root watch. Each scoped trigger watches exactly the
	// (system,kind) subtree it matches, scoped to this federation. cancel stops them.
	eng.ObserveScoped(sctx)
	s.scopeEngs[scope] = &scopeEngine{eng: eng, cancel: cancel}
	return eng
}

// Start begins the engine's reactive loop (observing the store). It returns
// immediately; the loop runs until ctx is cancelled. It first reconciles orphans —
// process:instance entities a previous run left running — since this is the monolith
// (the process runtime restarts with the engine, so those processes are gone).
func (s *Server) Start(ctx context.Context) {
	s.ctx = ctx            // scoped engines (created later, per federation) start under this
	s.reconcileProcesses() // resume remote-hosted processes; orphan local-dead ones
	// Per-trigger subtree observation — no root watch. The baseline engine watches
	// exactly the (system[,kind]) subtree each trigger matches, the same docd-aligned
	// model the scoped engines use; a mount under verse no longer collides with (and
	// force-ends) a whole-verse root watch. The old root-watch dispatch loop (Start)
	// and its cursor/steering control plane remain in the engine, dormant, for
	// reintroduction when engine-wide steering is wired.
	s.Eng.ObserveScoped(ctx)
}

// Handler returns the HTTP surface. Observation (read plane) and control are
// deliberately distinct routes.
func (s *Server) Handler() http.Handler {
	rt := &router{mux: http.NewServeMux()}
	// observation (read plane)
	rt.handle("GET /status", s.status, "", "verse-status")
	rt.handle("GET /watch", s.watch, "", "verse-event")
	rt.handle("GET /entities", s.listEntities, "", "")
	rt.handle("GET /entity", s.getEntity, "", "verse-entity")
	rt.handle("GET /gates", s.listGates, "", "")
	rt.handle("GET /flows", s.listFlows, "", "")
	rt.handle("GET /flow/tree", s.flowTree, "", "verse-flow-tree")
	rt.handle("GET /triggers", s.listTriggers, "", "verse-trigger")
	rt.handle("GET /triggers/{name}", s.getTrigger, "", "verse-trigger")
	rt.handle("GET /goals", s.listGoals, "", "verse-goal")
	// the API describing itself: the route table, and the signatures it points at
	rt.handle("GET /api", s.serveAPI, "", "verse-routes")
	rt.handle("GET /schema", s.serveSchema, "", "")
	rt.handle("GET /schema/{name}", s.serveSignature, "", "")
	// mutation + control (out-of-band from observation; priority-serviced)
	rt.handle("POST /entity", s.putEntity, "verse-put-entity", "verse-entity")
	rt.handle("DELETE /entity", s.deleteEntity, "", "")
	rt.handle("POST /triggers", s.addTrigger, "verse-trigger", "")
	rt.handle("DELETE /triggers/{name}", s.removeTrigger, "", "")
	rt.handle("POST /goals", s.addGoal, "verse-goal", "")
	rt.handle("DELETE /goals/{name}", s.removeGoal, "", "")
	rt.handle("POST /goal/pause", s.goalPause, "verse-goal-steer", "verse-steered")
	rt.handle("POST /goal/resume", s.goalResume, "verse-goal-steer", "verse-steered")
	rt.handle("POST /goal/cancel", s.goalCancel, "verse-goal-steer", "verse-steered")
	rt.handle("POST /goal/say", s.goalSay, "verse-goal-steer", "verse-steered")
	rt.handle("POST /scope/delete", s.deleteScope, "", "")
	rt.handle("POST /gate/resolve", s.resolveGate, "verse-resolve", "")
	rt.handle("POST /flow/pause", s.flowPause, "verse-flow-steer", "verse-steered")
	rt.handle("POST /flow/resume", s.flowResume, "verse-flow-steer", "verse-steered")
	rt.handle("POST /flow/cancel", s.flowCancel, "verse-flow-steer", "verse-steered")
	rt.handle("POST /flow/say", s.flowSay, "verse-flow-steer", "verse-steered")
	// process runtime (proc) — mounted; serves /processes[/{id}[/…]]. versed intercepts the
	// two READ routes to union the local runtime with the active runner's own processes
	// (e.g. a broker's), stamping each with its runner; everything else (spawn, events,
	// control) falls through to the local runtime.
	ph := s.Procd.Handler()
	rt.handle("GET /processes", s.listProcesses, "", "verse-instance")
	rt.handle("POST /processes", s.spawnProcess, "verse-spawn", "verse-spawned")
	rt.handle("GET /processes/{id}", s.getProcess, "", "verse-instance")
	rt.handle("GET /processes/{id}/inbox", s.processInbox, "", "verse-inbox-item")
	rt.handle("GET /processes/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		s.processEvents(w, r, ph)
	}, "", "verse-frame")
	// cancel/say are runner-agnostic: local processes go to the runtime, brokered/remote
	// ones to the runner's handle — so `process cancel`/`say` work wherever a process runs.
	rt.handle("POST /processes/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		s.processControl(w, r, ph, "cancel")
	}, "", "")
	rt.handle("POST /processes/{id}/say", func(w http.ResponseWriter, r *http.Request) {
		s.processControl(w, r, ph, "say")
	}, "verse-say", "")
	rt.handle("POST /processes/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		s.processControl(w, r, ph, "resolve")
	}, "verse-resolve", "")
	// re-vector (slide autonomy on a running process) — runner-agnostic like cancel/say: a
	// local process is served by the runtime mount (processd's setVector), a brokered/remote
	// one goes to the runner handle's SetVector.
	rt.handle("PUT /processes/{id}/vector", func(w http.ResponseWriter, r *http.Request) {
		s.processControl(w, r, ph, "vector")
	}, "verse-set-vector", "")
	rt.mount("/processes", ph)
	rt.mount("/processes/", ph)
	s.routes = rt.routes
	return rt.mux
}

// ── scripted driver: the default process runtime driver ─────────────────────
//
// A dependency-free driver that performs a few steps of "work" then completes, so
// the runtime is exercisable without model credentials. `verse up --llm` swaps in
// the LLM harness.

// ScriptedBuilder builds the credential-free process runtime driver: it runs the task as a real
// shell command (`bash -lc`), confined to root and cancellable via the process context — a
// governed shell command as a process. It is the dependency-free stand-in for an LLM agent, so
// the runtime is exercisable and steerable (and can run real work like `sleep`) without model
// credentials. The gate vector is Auto — a scripted command runs freely; the LLM harness is what
// wires the effect-gating vector.
// ScriptedBuilder is the processd.Builder form (root = the process's cwd). `verse up` uses
// NewScriptedProcess directly to confine scripted commands to the agent root.
func ScriptedBuilder(id, task, _ string, reg *process.Registry, _ process.ContentReader) *process.Process {
	return NewScriptedProcess(id, task, "", reg)
}

// NewScriptedProcess builds a scripted process rooted at root.
func NewScriptedProcess(id, task, root string, reg *process.Registry) *process.Process {
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, reg)
	return process.NewProcess(id, sup, &scriptDriver{command: task}, &shellExec{root: root})
}

// scriptDriver runs its task as a single shell command, then completes.
type scriptDriver struct {
	command string
	done    bool
}

func (d *scriptDriver) Next(_ context.Context, prev process.StepResult) (process.ToolUse, bool, error) {
	// A non-zero exit ends the process as FAILED. The llm driver can afford to read a
	// failure as just another step result and re-plan around it; a scripted process has
	// no one to do the re-planning, so a failure nothing reports is a failure that reads
	// as success — and a trigger's onDone fires on a command that never worked.
	if prev.Err != nil {
		return process.ToolUse{}, false, prev.Err
	}
	if d.done || strings.TrimSpace(d.command) == "" {
		return process.ToolUse{}, false, nil
	}
	d.done = true
	return process.ToolUse{Name: "run_bash", Input: map[string]any{"command": d.command}}, true, nil
}

// shellExec runs run_bash for real: cancellable via ctx (so `cancel` kills a running command),
// confined to root. It is the scripted driver's counterpart of the harness's fsExec.
type shellExec struct{ root string }

func (e *shellExec) Execute(ctx context.Context, tu process.ToolUse) (process.StepResult, []process.ToolUse, error) {
	// bash -c (non-login): inherits the server's env/PATH, and avoids the login shell's
	// job-control setpgid ("failed to change group ID") that fails in restricted envs (CI, sandboxes).
	c := exec.CommandContext(ctx, "bash", "-c", str(tu.Input, "command"))
	c.Dir = e.root
	out, err := c.CombinedOutput()
	res := strings.TrimSpace(string(out))
	if err != nil {
		if res != "" {
			res += "\n"
		}
		res += "exit: " + err.Error()
	}
	if res == "" {
		res = "(ok)"
	}
	// The failure rides in the StepResult rather than as an Execute error, because Run
	// discards the result on an error return and the command's output is the whole point
	// of a failure. A cancel is not a failure: the kill that ends `sleep` surfaces here as
	// an error too, and Run's boundary check reaches ctx.Err() first — but say so here, so
	// the driver's cancel and failure paths do not depend on that ordering.
	if ctx.Err() != nil {
		err = nil
	}
	return process.StepResult{Use: tu, Output: res, Err: err}, nil, nil
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

// deleteScope drops the ?scope copy-on-write overlay and all its data — federation
// teardown. It runs against the baseline store (only baseline may delete a scope), so it
// is independent of scopedStore.
func (s *Server) deleteScope(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("scope")
	if name == "" {
		http.Error(w, "scope delete: ?scope required", http.StatusBadRequest)
		return
	}
	// Stop the scope's engine (if any) before its data goes away, so it isn't
	// observing a store whose scope is being deleted.
	s.scopeMu.Lock()
	if se, ok := s.scopeEngs[name]; ok {
		se.cancel()
		delete(s.scopeEngs, name)
	}
	s.scopeMu.Unlock()
	if err := s.Store.DeleteScope(name); err != nil {
		http.Error(w, "scope delete: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// scopedStore returns the store view for the request's ?scope (baseline if absent).
// The seam for local-dev federation — the logd-backed store isolates the scope (see
// docs/deployment-and-steering.md).
func (s *Server) scopedStore(r *http.Request) entity.Store {
	if sc := r.URL.Query().Get("scope"); sc != "" {
		return s.Store.Scope(sc)
	}
	return s.Store
}

// watch streams store changes as SSE, catching up from ?since=<rev> first, then
// live — the cursor model surfaced at the wire.
func (s *Server) watch(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	sse, err := codec.NewSSE(w, r)
	if err != nil {
		codec.HTTPError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	q := r.URL.Query()
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	sys, kind := q.Get("system"), q.Get("kind")
	match := func(ev entity.Event) bool {
		return (sys == "" || ev.Ref.System == sys) && (kind == "" || ev.Ref.Kind == kind)
	}

	// Subscribe BEFORE the catch-up snapshot, so an event landing between the two is
	// captured live rather than lost in the gap. The catch-up and the live stream
	// overlap; dedup by revision (send only strictly-ascending revs).
	ch, unsub := store.Observe()
	defer unsub()

	last := since
	if evs, ok := store.Since(since); ok {
		for _, ev := range evs {
			if match(ev) {
				_ = sse.Send(ev)
			}
			if ev.Rev > last {
				last = ev.Rev
			}
		}
	}
	sse.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Rev != 0 && ev.Rev <= last {
				continue // already delivered by the catch-up (overlap window)
			}
			if ev.Gap > 0 || match(ev) {
				_ = sse.Send(ev)
			}
			if ev.Rev > last {
				last = ev.Rev
			}
		}
	}
}

func (s *Server) listEntities(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sys, kind := q.Get("system"), q.Get("kind")
	ents := s.scopedStore(r).List(func(ref entity.Ref) bool {
		return (sys == "" || ref.System == sys) && (kind == "" || ref.Kind == kind)
	})
	out := make([]map[string]any, 0, len(ents))
	for _, e := range ents {
		out = append(out, map[string]any{"ref": e.Ref.String(), "payload": e.Payload})
	}
	writeOut(w, r, http.StatusOK, out)
}

func (s *Server) getEntity(w http.ResponseWriter, r *http.Request) {
	ref, err := entity.ParseRef(r.URL.Query().Get("ref"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	e, ok, err := s.scopedStore(r).Get(ref)
	if err != nil {
		// A read that could not be performed is NOT a 404. Reporting it as one tells the
		// caller the entity does not exist, which is a different — and actionable —
		// statement from "the store is unreachable", and the caller acts on it: an agent
		// creates the file it was told is missing. 502: verse is up, its store is not.
		http.Error(w, "entity read failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	// The entity itself. Its Ref carries MarshalText, so it still renders as the address
	// string this always sent — and the route table's `verse-entity` now names the type
	// actually written rather than a map that happened to match it.
	writeOut(w, r, http.StatusOK, e)
}

// deleteEntity removes whatever stands at a ref — or, with ?field=a&field=b, at one place
// inside it. Removing what is not there is not an error: the request says what should stand
// there afterwards, and nothing already does.
func (s *Server) deleteEntity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref, err := entity.ParseRef(q.Get("ref"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.scopedStore(r).Write(entity.Write{At: ref.At(q["field"]...)}); err != nil {
		http.Error(w, "entity remove failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// putEntity is the write surface: a value MERGES in at a place — an entity, or a field
// inside one — iff every stated condition holds.
//
// A refused condition is 409, not an error: the request was well-formed and the store was
// reachable, and the answer is "not while that is true". The success body is the entity as
// it now stands, read back, because a write that merges into a place is precisely the case
// where the caller cannot compute the result from what it sent.
func (s *Server) putEntity(w http.ResponseWriter, r *http.Request) {
	var body api.PutEntity
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ref, err := entity.ParseRef(body.Ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	when, err := conditions(body.When)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	store := s.scopedStore(r)
	committed, err := store.Write(entity.Write{At: ref.At(body.Field...), Value: body.Payload, When: when})
	if err != nil {
		http.Error(w, "entity write failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !committed {
		http.Error(w, "write refused: a condition did not hold", http.StatusConflict)
		return
	}
	e, ok, err := store.Get(ref)
	if err != nil || !ok {
		w.WriteHeader(http.StatusNoContent) // it landed; we just cannot show it back
		return
	}
	writeOut(w, r, http.StatusOK, e)
}

// conditions maps the wire's preconditions onto the store's. A condition that asks nothing
// is refused rather than silently dropped: sending one means intending a guard, and a guard
// that is quietly not applied is worse than a rejected request.
func conditions(cs []api.Condition) ([]entity.Require, error) {
	out := make([]entity.Require, 0, len(cs))
	for _, c := range cs {
		var at entity.Path
		if c.Ref != "" {
			ref, err := entity.ParseRef(c.Ref)
			if err != nil {
				return nil, fmt.Errorf("condition ref %q: %w", c.Ref, err)
			}
			at = ref.At(c.Field...)
		}
		switch {
		case c.Pattern != nil:
			out = append(out, entity.Require{At: at, Pattern: c.Pattern})
		case c.Exists != nil && *c.Exists:
			out = append(out, entity.Require{At: at, Pattern: entity.Present()})
		case c.Exists != nil:
			out = append(out, entity.Require{At: at, Pattern: entity.Absent()})
		default:
			return nil, fmt.Errorf("condition asks nothing: set `exists` or `pattern`")
		}
	}
	return out, nil
}

// listGates is the unified pending-gate view: engine (trigger) gates from the registry AND
// per-process gates (an agent blocked on write/shell/delegate), which live in each process's
// own registry and otherwise show only under `process get`. Both are resolvable by id.
func (s *Server) listGates(w http.ResponseWriter, r *http.Request) {
	// A gate asks a person to decide something, so the listing has to say what is at
	// stake. `origin` names who asked and `gate` names which rule; neither answers "about
	// what?", which was only ever recoverable by reading it back out of the `reason` prose.
	// An engine gate carries its subject on the question; a process gate's is the flow the
	// process belongs to, which the engine indexes by process id. Both are omitted when
	// genuinely absent rather than shown empty — a bare `process run` is about nothing.
	//
	// compact extends that rule to EVERY field, and the reason is not tidiness. A brokered
	// gate legitimately has no origin and no mode: an executor raises a gate without
	// deciding who answers it or under which dial. Those printed as `origin: ""` next to a
	// `reason: ""` that was a real drop — the broker kept only id and gate — and the drop
	// was indistinguishable from the structural blanks flanking it. A missing key is
	// visible; an empty one is camouflage. `id` and `where` are structural and always set.
	out := make([]map[string]any, 0)
	compact := func(rec map[string]any) map[string]any {
		for k, v := range rec {
			if s, ok := v.(string); ok && s == "" {
				delete(rec, k)
			}
		}
		return rec
	}
	add := func(rec map[string]any, subject string) {
		if subject != "" {
			rec["subject"] = subject
		}
		out = append(out, compact(rec))
	}
	for _, p := range s.Reg.Pending() {
		rec := map[string]any{
			"id": p.Question.ID, "origin": p.Question.Origin, "gate": p.Question.Gate,
			"mode": p.Question.Mode.String(), "reason": p.Question.Reason, "tool": p.Tool,
			"where": "engine",
		}
		// Deliberately NOT "process". An engine gate is raised before the spawn, so this
		// names a process that does not exist yet and will not exist at all if the gate is
		// denied. Reusing the key a process gate uses for a process that IS running would
		// make one field mean two things, and the reader could not tell which.
		if p.Question.Spawns != "" {
			rec["spawns"] = p.Question.Spawns
		}
		add(rec, p.Question.Subject)
	}
	for _, snap := range s.processSnapshots(s.scopedStore(r)) {
		subject := ""
		if s.Eng != nil {
			subject = s.Eng.SubjectOf(snap.ID)
		}
		for _, pg := range snap.Pending {
			add(map[string]any{
				"id": pg.ID, "origin": pg.Origin, "gate": pg.Gate,
				"mode": pg.Mode, "reason": pg.Reason, "tool": pg.Tool,
				"process": snap.ID, "where": "process",
			}, subject)
		}
	}
	writeOut(w, r, http.StatusOK, out)
}

func (s *Server) resolveGate(w http.ResponseWriter, r *http.Request) {
	var body api.Resolve
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	by := body.By
	if by == "" {
		by = "human:cli"
	}
	res := process.Resolution{Proceed: body.Approve, By: by, Steer: body.Say}
	// A process gate's id is <process-id>~dN; route it to that process. An engine gate id
	// (trigger:…~N) whose owner isn't a process falls through to the registry.
	owner := body.ID
	if i := strings.Index(owner, process.GateSep); i >= 0 {
		owner = owner[:i]
	}
	if !s.isProcess(owner) {
		// The id named no process — either an engine gate, or an executor-raised one whose
		// id carries no owner by construction. Ask the snapshots which process holds it;
		// an engine gate is held by none and still falls through to the registry below.
		owner = s.processHoldingGate(body.ID)
	}
	if s.isProcess(owner) {
		if !s.resolveProcessDecision(owner, body.ID, res) {
			http.Error(w, "unknown or already-answered decision", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.Reg.Answer(body.ID, res) {
		http.Error(w, "unknown or already-answered decision", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── triggers: the charter ────────────────────────────────────────────────────

func (s *Server) listTriggers(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil {
		writeOut(w, r, http.StatusOK, []trigger.Trigger{})
		return
	}
	writeOut(w, r, http.StatusOK, eng.Triggers())
}

// status is a one-shot summary of the running verse: the store's revision and the
// live counts across the planes — the charter size, in-flight flows, pending gates,
// hosted processes, and total firings.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	out := api.Status{
		StoreRev:  s.Store.Rev(),
		Triggers:  len(s.Eng.Triggers()),
		Flows:     len(s.Eng.Flows()),
		Gates:     len(s.Reg.Pending()),
		Processes: s.Procd.Count(),
		Fired:     s.Eng.Fired(),
		Runner:    s.runnerStatus(), // how processes run: kind + resume + dial legs
		LLM:       s.LLM,            // secrets-safe: are agent creds configured on THIS server
	}
	// When docd-backed, publish its addresses so a controller process (verse connect)
	// can reach the mount face; the client face is where store clients connect.
	if s.DocdMount != "" || s.DocdClient != "" {
		out.Docd = &api.Docd{Client: s.DocdClient, Mount: s.DocdMount}
	}
	// This verse's own listen addresses (operator API, and the broker face when served).
	if s.Addr != "" || s.BrokerAddr != "" {
		out.Listen = &api.Listen{API: s.Addr, Broker: s.BrokerAddr}
	}
	writeOut(w, r, http.StatusOK, out)
}

// runnerStatus describes the runner along its two independent legs, so the two are not
// confused: `resume` is the VERSE-facing control/re-attach address (what a restart needs),
// `dial` is the WORKER-facing address (where a dial-in worker connects). They coincide for
// a remote processd (one URL, no separate worker leg) and diverge for a broker (verse drives
// it in-process — ephemeral — while a worker dials a separate face). See docs/process-control.md.
func (s *Server) runnerStatus() *api.RunnerStatus {
	pol := &api.RunnerStatus{Resume: "ephemeral"}
	switch {
	case hasFace(s.runner): // a dial-in broker: in-process control, external worker face
		pol.Kind = "broker"
		if face := s.runner.(interface{ Face() string }).Face(); face != "" {
			pol.Dial = face
		}
	case s.runner.Ref() == "local":
		pol.Kind = "local"
	default: // a remote runner base URL — the Ref IS the re-attach address
		pol.Kind = "remote"
		pol.Resume = s.runner.Ref()
	}
	return pol
}

func hasFace(p process.Runner) bool {
	_, ok := p.(interface{ Face() string })
	return ok
}

// processLister is a runner that keeps its OWN process registry (a broker), distinct
// from the local runtime. localRunner does NOT implement it — it delegates to the same
// processd the union already lists — so brokered processes are added without double-counting
// local ones.
type processLister interface {
	ListProcesses() []process.Instance
	ProcessSnapshot(id string) (process.Instance, bool)
}

// spawnProcess is the ad-hoc `process run` path. Unlike a trigger spawn it has no
// subject/flow, but it goes through the SAME seam: host it via the runner (so a run
// honors --runner/--broker, not just trigger spawns) and mirror it into the store (so a run
// is a process:instance entity — observable, and its completion can drive triggers) — the
// consistency the "processes are entities" model wants.
func (s *Server) spawnProcess(w http.ResponseWriter, r *http.Request) {
	var body api.Spawn
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Task == "" {
		http.Error(w, "task is required", http.StatusBadRequest)
		return
	}
	id := body.ID
	if id == "" {
		id = "run-" + mintNonce()[:12] // ad-hoc id; a trigger spawn supplies its own
	}
	// Honor --scope: mirror the process:instance into that scope's copy-on-write view, not
	// baseline — else a scoped run leaks into baseline (a baseline watch would see it). On the
	// memory store Scope is a passthrough, so isolation only applies to the docd/logd store.
	store := s.Store
	if scope := r.URL.Query().Get("scope"); scope != "" {
		store = s.Store.Scope(scope)
	}
	h := s.runner.Spawn(id, body.Task, body.Driver, nil)
	mirror.Reflect(store, id, h, mirror.Meta{Runner: s.runner.Ref()})
	writeOut(w, r, http.StatusOK, api.Spawned{ID: id})
}

// listProcesses is the unified process list: the local runtime plus, when the runner is a
// broker (or any runner with its own registry), its processes — each stamped with its runner
// so a client sees whether a process is local or brokered (and, for a broker, its dialURL).
func (s *Server) listProcesses(w http.ResponseWriter, r *http.Request) {
	writeOut(w, r, http.StatusOK, s.processSnapshots(s.scopedStore(r)))
}

// processSnapshots is the unified process list for one store view (baseline, or a ?scope
// overlay): the local runtime plus the active runner's own processes (a broker's), each
// stamped with its runner.
//
// The runtime and the broker are scope-blind — they hold every process globally — so a
// bare list would leak a `verse connect` scope's processes into baseline (and across
// scopes). The one record that carries scope is the process:instance:<id> entity that
// mirror.Reflect writes (synchronously, at spawn) into the spawning scope's copy-on-write
// store. So a process belongs to this view iff its instance entity is visible here: the
// overlay's own writes, plus baseline seen through the overlay. On a store without real
// scopes every instance would be visible in every scope, making this a no-op — the
// isolation boundary is the store's (real on docd/logd).
func (s *Server) processSnapshots(store entity.Store) []process.Instance {
	members := s.scopeMembers(store)
	keep := func(inst process.Instance) bool { return members[inst.ID] }
	out := make([]process.Instance, 0)
	for _, inst := range s.Procd.Snapshots() {
		if keep(inst) {
			inst.Runner = "local"
			out = append(out, inst)
		}
	}
	if l, ok := s.runner.(processLister); ok {
		ref := s.runner.Ref()
		for _, inst := range l.ListProcesses() {
			if keep(inst) {
				inst.Runner = ref
				out = append(out, inst)
			}
		}
	}
	return out
}

// scopeMembers returns the set of process ids whose process:instance entity is visible in
// store — the scope's membership.
func (s *Server) scopeMembers(store entity.Store) map[string]bool {
	ids := map[string]bool{}
	for _, e := range store.List(func(ref entity.Ref) bool {
		return ref.System == mirror.System && ref.Kind == "instance"
	}) {
		ids[e.Ref.ID] = true
	}
	return ids
}

// isProcess reports whether id names a process in the runtime or the active runner.
func (s *Server) isProcess(id string) bool {
	if _, ok := s.Procd.SnapshotOf(id); ok {
		return true
	}
	if l, ok := s.runner.(processLister); ok {
		_, ok := l.ProcessSnapshot(id)
		return ok
	}
	return false
}

// processHoldingGate finds which process has decisionID open, for a gate id that does not
// carry its owner.
//
// A gate the RUNTIME raises is <process-id>~dN, so its owner is readable off the id. One an
// EXECUTOR raises (`process do gate`) is <name>-<hex>, deliberately free of GateSep —
// shortID avoids it so the id survives a shell and a URL intact — with the consequence that
// splitting on GateSep recovers nothing and `verse gate approve` answered 404 on a gate
// `verse gates` was listing one line above, `process:` and all. The routing information was
// never missing; it was in the snapshot rather than in the id. This reads it from there.
//
// Unscoped, like isProcess beside it: a resolve is addressed by a unique decision id, not
// discovered by listing, so it does not go through processSnapshots' scope filter.
func (s *Server) processHoldingGate(decisionID string) string {
	holds := func(inst process.Instance) bool {
		for _, pg := range inst.Pending {
			if pg.ID == decisionID {
				return true
			}
		}
		return false
	}
	for _, inst := range s.Procd.Snapshots() {
		if holds(inst) {
			return inst.ID
		}
	}
	if l, ok := s.runner.(processLister); ok {
		for _, inst := range l.ListProcesses() {
			if holds(inst) {
				return inst.ID
			}
		}
	}
	return ""
}

// resolveProcessDecision answers a process's pending gate (write/shell/delegate) — the
// runtime for a local process, the runner handle for a brokered one.
func (s *Server) resolveProcessDecision(procID, decisionID string, res process.Resolution) bool {
	if _, ok := s.Procd.SnapshotOf(procID); ok {
		return s.Procd.Resolve(procID, decisionID, res)
	}
	if h := s.runner.Attach(procID); h != nil {
		// by is carried across this seam, not dropped: the local leg above takes the whole
		// Resolution, so a brokered gate that lost the answerer would be attributed
		// differently from an identical local one purely by where it happened to run.
		if rh, ok := h.(interface {
			Resolve(decision string, proceed bool, say, by string) bool
		}); ok {
			return rh.Resolve(decisionID, res.Proceed, strings.Join(res.Steer, " "), res.By)
		}
	}
	return false
}

// getProcess resolves one process across backends: the local runtime first (live detail),
// then the runner's own registry (a brokered process) — so `process get <id>` works whether
// the process runs locally or behind a broker.
func (s *Server) getProcess(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if inst, ok := s.Procd.SnapshotOf(id); ok {
		inst.Runner = "local"
		writeOut(w, r, http.StatusOK, inst)
		return
	}
	if l, ok := s.runner.(processLister); ok {
		if inst, ok := l.ProcessSnapshot(id); ok {
			inst.Runner = s.runner.Ref()
			writeOut(w, r, http.StatusOK, inst)
			return
		}
	}
	http.NotFound(w, r)
}

// inboxReader is a runner (a broker) that can surface a process's queued worker control for
// operator observability, unfenced. localRunner doesn't implement it (no dial-in worker).
type inboxReader interface {
	ProcessInbox(id string) ([]api.InboxItem, bool)
}

// processInbox is an operator peek at the control queued for a brokered process's worker —
// unfenced, so it doesn't disturb a live worker. A local process has no worker inbox → empty.
func (s *Server) processInbox(w http.ResponseWriter, r *http.Request) {
	items := []api.InboxItem{}
	if ir, ok := s.runner.(inboxReader); ok {
		if got, ok := ir.ProcessInbox(r.PathValue("id")); ok {
			items = got
		}
	}
	// The bare list, not {items: [...]}. Every other collection this server serves is a
	// bare array — triggers, gates, goals, flows, processes, flow trees — and the wrapper
	// here was a JSON habit rather than a decision. Nothing decoded it structurally:
	// `verse process inbox` re-renders the document.
	writeOut(w, r, http.StatusOK, items)
}

// processEvents bridges `attach` across backends: a locally-hosted process streams from the
// runtime (its native SSE); a brokered/remote one is bridged here — Subscribe the runner
// handle (which replays the merged log then goes live) and re-emit each event as SSE.
func (s *Server) processEvents(w http.ResponseWriter, r *http.Request, runtime http.Handler) {
	id := r.PathValue("id")
	if _, ok := s.Procd.SnapshotOf(id); ok {
		runtime.ServeHTTP(w, r) // local runtime owns it
		return
	}
	h := s.runner.Attach(id)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	sse, err := codec.NewSSE(w, r)
	if err != nil {
		codec.HTTPError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	ch, unsub := h.Subscribe()
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

// processControl routes cancel/say: a process the local runtime hosts is served by it (live
// detail); otherwise the request goes to the runner's handle (a brokered/remote process),
// so the same command works wherever a process runs.
func (s *Server) processControl(w http.ResponseWriter, r *http.Request, runtime http.Handler, op string) {
	id := r.PathValue("id")
	if _, ok := s.Procd.SnapshotOf(id); ok {
		runtime.ServeHTTP(w, r) // local runtime owns it
		return
	}
	h := s.runner.Attach(id)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	switch op {
	case "cancel":
		h.Cancel("")
	case "say":
		var body api.Say
		_ = codec.Read(r, &body)
		h.Steer(body.Instruction, body.By)
	case "vector":
		// slide autonomy on a running (brokered/remote) process: {vector, by} is relayed to
		// the worker via the handle, `by` attributing the operator who slid it.
		var body api.SetVector
		if err := codec.Read(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.Vector == nil {
			http.Error(w, "vector required", http.StatusBadRequest)
			return
		}
		h.SetVector(body.Vector, body.By)
	case "resolve":
		// answer a gate the worker raised — relayed to the worker's inbox by the handle.
		rh, ok := h.(interface {
			Resolve(decision string, proceed bool, say, by string) bool
		})
		if !ok {
			http.Error(w, "runner does not support gate resolution", http.StatusNotImplemented)
			return
		}
		// Same body as `POST /gate/resolve` (api.Resolve): one gate, named by id,
		// answered yes or no. This route differs only in WHERE the answer goes — relayed
		// to a remote worker's inbox rather than resolved in-process — which is not a
		// reason for a second vocabulary, and having had one is how `decisionId`/`proceed`
		// drifted away from `id`/`approve` unnoticed.
		var body api.Resolve
		_ = codec.Read(r, &body)
		// The relay interface still carries a single steer string; a multi-part say is
		// joined rather than dropped. Widening it is part of the broker/processd pass.
		by := body.By
		if by == "" {
			by = "human:cli"
		}
		if !rh.Resolve(body.ID, body.Approve, strings.Join(body.Say, "\n"), by) {
			http.Error(w, "unknown or already-answered decision", http.StatusNotFound)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addTrigger(w http.ResponseWriter, r *http.Request) {
	var d trigger.Trigger
	if err := codec.Read(r, &d); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if d.Name == "" {
		http.Error(w, "trigger name required", http.StatusBadRequest)
		return
	}
	// ?scope=<s> installs the trigger on that scope's engine (created on first use),
	// observing and acting within the scope — the reactivity for a `verse connect`
	// federation. No scope → the baseline charter.
	eng := s.Eng
	if scope := r.URL.Query().Get("scope"); scope != "" {
		if s.ctx == nil {
			http.Error(w, "engine not started", http.StatusServiceUnavailable)
			return
		}
		eng = s.scopeEngine(scope)
	}
	if err := eng.AddTrigger(d); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) getTrigger(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil {
		http.NotFound(w, r)
		return
	}
	d, ok := eng.Trigger(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeOut(w, r, http.StatusOK, d)
}

func (s *Server) removeTrigger(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil || !eng.RemoveTrigger(r.PathValue("name")) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── goals: an owning aggregate (owned triggers + done/fail latches + status) ──────

// listGoals returns the live goal roster read from the goal:instance entities — name,
// subject, status, waitingOn, deadline, and (once terminal) outcome — the `verse goals` view.
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	ents := s.Store.List(func(ref entity.Ref) bool { return ref.System == "goal" && ref.Kind == "instance" })
	out := make([]map[string]any, 0, len(ents))
	for _, e := range ents {
		row := map[string]any{"name": e.Ref.ID}
		fields := entity.ToObj(e.Payload)
		for _, k := range []string{"subject", "status", "waitingOn", "deadline", "outcome", "terminal"} {
			if v, ok := fields[k]; ok {
				row[k] = v
			}
		}
		out = append(out, row)
	}
	writeOut(w, r, http.StatusOK, out)
}

func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	var d trigger.GoalDef
	if err := codec.Read(r, &d); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if d.Name == "" {
		http.Error(w, "goal name required", http.StatusBadRequest)
		return
	}
	if err := s.Eng.AddGoal(d); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) removeGoal(w http.ResponseWriter, r *http.Request) {
	if !s.Eng.RemoveGoal(r.PathValue("name")) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// goalPause/Resume/Cancel/Say steer a goal by delegating to its flow (a goal is a flow with a
// destination) — the same control surface, keyed by the goal name.
func (s *Server) goalPause(w http.ResponseWriter, r *http.Request) {
	s.goalSteer(w, r, func(name string) api.Steered {
		return api.Steered{Goal: name, Verb: "pause", Affected: s.Eng.PauseGoal(name)}
	})
}

func (s *Server) goalResume(w http.ResponseWriter, r *http.Request) {
	s.goalSteer(w, r, func(name string) api.Steered {
		return api.Steered{Goal: name, Verb: "resume", Affected: s.Eng.ResumeGoal(name)}
	})
}

func (s *Server) goalCancel(w http.ResponseWriter, r *http.Request) {
	s.goalSteer(w, r, func(name string) api.Steered {
		return api.Steered{Goal: name, Verb: "cancel", Affected: s.Eng.CancelGoal(name)}
	})
}

func (s *Server) goalSay(w http.ResponseWriter, r *http.Request) {
	var body api.GoalSteer
	if err := codec.Read(r, &body); err != nil || body.Name == "" || body.Text == "" {
		http.Error(w, "name and text required", http.StatusBadRequest)
		return
	}
	by := body.By
	if by == "" {
		by = "human:cli"
	}
	writeOut(w, r, http.StatusOK, api.Steered{
		Goal: body.Name, Verb: "say",
		Affected: s.Eng.SteerGoal(body.Name, body.Text, by),
	})
}

func (s *Server) goalSteer(w http.ResponseWriter, r *http.Request, act func(name string) api.Steered) {
	var body api.GoalSteer
	if err := codec.Read(r, &body); err != nil || body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	writeOut(w, r, http.StatusOK, act(body.Name))
}

// ── flow: engine steering (control plane) ────────────────────────────────────

func (s *Server) listFlows(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil {
		writeOut(w, r, http.StatusOK, []string{})
		return
	}
	writeOut(w, r, http.StatusOK, eng.Flows())
}

// flowTree renders a subject's work as a SHAPE — the trigger that acted on it, the process
// that ran, and the completion hooks branching off it, including the ones that have not run
// yet. A read, so the address rides the query string rather than a body (steering is POST).
//
// It is derived per request from the charter, the mirror and the live registry (see
// trigger.FlowTrees). Nothing about the tree is stored: a materialized one would be the
// flows[subject] index that a flow-as-a-query deliberately replaced.
func (s *Server) flowTree(w http.ResponseWriter, r *http.Request) {
	eng := s.engFor(r.URL.Query().Get("scope"))
	if eng == nil {
		writeOut(w, r, http.StatusOK, []trigger.FlowTree{})
		return
	}
	addr := r.URL.Query().Get("subject")
	if addr == "" {
		http.Error(w, "subject required (an entity address, e.g. demo:pr:1 or demo:pr:*)", http.StatusBadRequest)
		return
	}
	m, err := trigger.ParseExpr(addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeOut(w, r, http.StatusOK, eng.FlowTrees(m))
}

// flowAddress decodes the steering body's `subject` — an entity ADDRESS, not just a ref:
// "demo:pr:1" names one subject, "demo:pr:*" the whole kind. A flow is a query over live
// processes' subjects, so one field serves both, and a plain ref stays valid (no client
// breaks). Returns false having already written the error response.
func flowAddress(w http.ResponseWriter, r *http.Request, body *api.FlowSteer) (trigger.Match, bool) {
	if err := codec.Read(r, body); err != nil || body.Subject == "" {
		http.Error(w, "subject required (an entity address, e.g. demo:pr:1 or demo:pr:*)", http.StatusBadRequest)
		return nil, false
	}
	m, err := trigger.ParseExpr(body.Subject)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return m, true
}

// flowPause/flowResume hold the addressed processes at their next boundary — serviced with
// priority over observation by the engine's control plane (see
// docs/deployment-and-steering.md).
func (s *Server) flowPause(w http.ResponseWriter, r *http.Request) {
	var b api.FlowSteer
	m, ok := flowAddress(w, r, &b)
	if !ok {
		return
	}
	writeOut(w, r, http.StatusOK, api.Steered{Subject: b.Subject, Verb: "pause", Affected: s.Eng.PauseFlow(m)})
}

func (s *Server) flowResume(w http.ResponseWriter, r *http.Request) {
	var b api.FlowSteer
	m, ok := flowAddress(w, r, &b)
	if !ok {
		return
	}
	writeOut(w, r, http.StatusOK, api.Steered{Subject: b.Subject, Verb: "resume", Affected: s.Eng.ResumeFlow(m)})
}

func (s *Server) flowCancel(w http.ResponseWriter, r *http.Request) {
	var b api.FlowSteer
	m, ok := flowAddress(w, r, &b)
	if !ok {
		return
	}
	writeOut(w, r, http.StatusOK, api.Steered{Subject: b.Subject, Verb: "cancel", Affected: s.Eng.CancelFlow(m)})
}

func (s *Server) flowSay(w http.ResponseWriter, r *http.Request) {
	var b api.FlowSteer
	m, ok := flowAddress(w, r, &b)
	if !ok {
		return
	}
	if b.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	by := b.By
	if by == "" {
		by = "human:cli"
	}
	writeOut(w, r, http.StatusOK, api.Steered{Subject: b.Subject, Verb: "say", Affected: s.Eng.SteerFlow(m, b.Text, by)})
}

// eventWire is gone: an Event is written as itself. It used to be rebuilt field by field
// into a map — ref stringified, to/from/gap copied conditionally — which is exactly what the
// type's own tony tags already say (omitzero on to, from and gap), and entity.Ref renders as
// its address through MarshalText. The rebuild could only ever agree with the type by
// coincidence.

// writeOut renders a response in the format the request asked for. It takes the request
// because that is where the answer lives — ?format=, then Accept, then the process default.
func writeOut(w http.ResponseWriter, r *http.Request, code int, v any) {
	codec.Write(w, r, code, v)
}
