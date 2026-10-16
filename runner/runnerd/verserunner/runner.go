package verserunner

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// Secrets is what the runner reads from its environment. A task's process doesn't have the
// runner's environment, so they are sent to it on its stdin.
type Secrets struct {
	PortkeyAPIKey  string `tony:"field=portkeyApiKey,omitzero"`
	PortkeyBaseURL string `tony:"field=portkeyBaseUrl,omitzero"`
	// PortkeyVirtualKey is the virtual key a spawn that picks none runs under, when the
	// configuration names none: a routing label, read from the environment where a deployment
	// keeps it beside the account key.
	PortkeyVirtualKey string `tony:"field=portkeyVirtualKey,omitzero"`
}

// SecretsFromEnv reads the runner's secrets from its environment.
func SecretsFromEnv() Secrets {
	return Secrets{
		PortkeyAPIKey:     os.Getenv(EnvPortkeyAPIKey),
		PortkeyBaseURL:    os.Getenv(EnvPortkeyBaseURL),
		PortkeyVirtualKey: os.Getenv(EnvPortkeyVirtualKey),
	}
}

// Standalone is the verse runner as a standalone runner, with a runnerd.SubprocessHandler: each
// task in a process of its own, started with Command (which calls ServeTask), confined by
// Sandbox. The process gets the runner's configuration and secrets on its stdin.
//
// With environments configured, the runner is the handler's Preparer: each task gets a workspace
// of the spawn's repositories under Data, the network the environment declares, and the
// delivery channel its pushes are asked through; its process gets an environment with
// only what the environment's allowlist names. Without, each task gets a fresh directory under
// Work, and an environment with only what task.DefaultShellEnv names.
type Standalone struct {
	Config  Config
	Secrets Secrets
	Sandbox runnerd.Sandbox
	// Command is the task process: a program and its arguments that call ServeTask.
	Command []string
	// Work is where the tasks' directories are made, Control where their sockets are, and Data
	// where the mirrors and workspaces of the environments live.
	Work, Control, Data string
	// Keep keeps a task's workspace when it ends, for debugging.
	Keep bool
	// GitToken is the runner's own git credential: what it fetches its mirrors with and pushes
	// from them with. It stays on this side of the process boundary. Empty is right for a local
	// path.
	GitToken string
	// CallTool is the `verse-call-tool` binary, copied into each task's workspace so a program
	// there can call the task's tools (calltool.go). Empty: programs have no such command, and
	// the tools are still served for anything that speaks the socket.
	CallTool string
	Log      io.Writer
}

// Start builds the runner. Nothing is served yet: Handler is what to serve.
func (s Standalone) Start() (*Runner, error) {
	if s.Log == nil {
		s.Log = os.Stderr
	}
	blob, err := codec.Marshal(taskConfig{Config: s.Config, Secrets: s.Secrets}, codec.Tony)
	if err != nil {
		return nil, err
	}
	r := &Runner{cfg: s.Config, secrets: s.Secrets, log: s.Log, keep: s.Keep, token: s.GitToken,
		callTool: s.CallTool, control: s.Control}
	hc := runnerd.SubprocessConfig{
		Command: s.Command, Env: task.ShellEnv(task.DefaultShellEnv), Config: blob,
		Sandbox: s.Sandbox, Work: s.Work, Control: s.Control, Log: s.Log,
	}
	// The mirrors exist whatever the configuration says, because the descriptor reads them: a
	// runner with no environments answers GET /runner with nothing held, not with a panic.
	data := s.Data
	if data == "" {
		data = s.Work
	}
	r.mirrors = workspace.NewMirrors(filepath.Join(data, "mirrors"), workspace.TokenAuth(s.GitToken))
	r.workspaces = filepath.Join(data, "workspaces")
	if len(s.Config.Environments) > 0 {
		hc.Preparer = r
	}
	r.h = runnerd.NewSubprocessHandler(hc)
	r.h.Describe(s.Config.Describe(s.Secrets))
	return r, nil
}

// Runner is the verse runner: what every task needs and nothing a task owns. Standalone, it holds
// the handler that runs each task's process, the mirrors, the credentials and the control
// sockets, all on this side of a process boundary the task cannot cross. Embedded (New), it
// builds its tasks in the process that hosts it.
//
// No git credential reaches a task. A task asks to push, over the delivery channel, and the
// runner checks the request against the spawn's allowance, stages the work and pushes it from
// its own mirror with its own token (runner/runnerd/delivery). What a task gets back is what happened.
type Runner struct {
	cfg     Config
	secrets Secrets
	log     io.Writer
	drivers Drivers // the embedded runner's builder

	mirrors    *workspace.Mirrors
	workspaces string
	keep       bool
	// token is this runner's own git credential: every fetch and every push is made with it, on
	// this side.
	token string
	// callTool is the `verse-call-tool` binary each workspace gets a copy of; "" for none.
	callTool string
	control  string // the directory holding one control socket per task, and the egress sockets
	// h runs each task in a process of its own and serves the contract for it; this runner is
	// its Preparer (prepare.go) when it has environments.
	h *runnerd.SubprocessHandler

	// prepareHook, when set, runs at the start of every preparation. Tests use it to hold a
	// spawn in the state the design is about — answered, and not yet hosted.
	prepareHook func(id string)
}

// Handler is the verse-facing surface: the task contract and `GET /describe`, served by the
// handler, and the descriptor beside them.
func (r *Runner) Handler() http.Handler {
	mux := http.NewServeMux()
	// The descriptor comes first: it is what an operator reads before pointing a charter here,
	// and what `verse runner add` should ask. A STUB — it says what this runner is and what it
	// can contain, and nothing yet consumes it.
	mux.HandleFunc("GET /runner", r.Descriptor)
	mux.Handle("/", r.h.HTTPHandler())
	return mux
}

// Shutdown stops every task process. Without it a restart leaves orphans holding workspaces —
// bwrap's --die-with-parent covers the crash case on Linux and nothing covers it on macOS, which
// is what the lifeline is for.
func (r *Runner) Shutdown() { r.h.Shutdown() }

// running is how many tasks this runner has, live or remembered.
func (r *Runner) running() int { return len(r.h.Snapshots()) }

// taskConfig is what a task's process reads on its stdin: the runner's configuration and secrets,
// and, for a task with a workspace, what the runner prepared for it.
type taskConfig struct {
	Config  Config    `tony:"field=config"`
	Secrets Secrets   `tony:"field=secrets"`
	Task    *taskPrep `tony:"field=task,omitzero"`
}

// ServeTask is a task's process: it reads the runner's configuration and secrets from stdin,
// and serves the one task a SubprocessHandler hands it, in the working directory — the task's
// own, or the workspace the runner prepared, reopened here with no credential in it.
func ServeTask(stdin io.Reader, log io.Writer) error {
	blob, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading the task's configuration: %w", err)
	}
	var tc taskConfig
	if err := codec.Unmarshal(blob, &tc, codec.Tony); err != nil {
		return fmt.Errorf("the task's configuration does not parse: %w", err)
	}
	if tc.Task == nil {
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		return runnerd.ServeTask(New(tc.Config, tc.Secrets, dir, log).Build)
	}
	tp := tc.Task
	// The checkout is the runner's work — it holds the mirrors. This side opens what is already
	// there, and its Trees carry NO auth: a push is a request over the channel, and the runner
	// makes it.
	ws, err := workspace.Reopen(tp.Workspace, tp.Repos, tp.Branch, tp.Bases)
	if err != nil {
		return err
	}
	env := tp.Environment
	if tp.Egress != "" {
		// The runner's allowlisting proxy, made usable as a proxy by this process and declared
		// as passthrough, so the environment allowlist carries it into every subprocess: what a
		// task's process gets is what the environment names, and the runner has just named four
		// more.
		names, err := runnerd.UseEgress(tp.Egress)
		if err != nil {
			return err
		}
		env.Env = append(env.Env, names...)
	}
	r := New(tc.Config, tc.Secrets, ws.Root, log)
	d := &r.drivers
	d.Env, d.Workspace, d.Environment, d.Brief, d.Agents, d.Tools = env.Env, ws, &env, tp.Brief, tc.Config.Agents, tc.Config.Tools
	d.Default = cmp.Or(env.Driver, d.Default)
	// The agent's push and fetch ask over the channel on fd 5, which is the whole reason a push
	// is a request rather than a push: the credential is on the other side of it. The launch is
	// what a commit's trailers carry, and the tools are built before it arrives: one process is
	// one task, so this is a handover rather than sharing.
	var launched task.Launch
	cl, err := delivery.Attach(pushFD, ws, func() task.Launch { return launched })
	if err != nil {
		// Without it the task would run with a workspace it was told it may push from and no way
		// to: a task that cannot deliver is not run, and the reason is the process's exit.
		return err
	}
	d.Extra = append(d.Extra, cl.Tools()...)
	return runnerd.ServeTask(func(l task.Launch, reg *task.Registry) *task.Task {
		launched = l
		return d.Build(l, reg)
	})
}

// New is the verse runner under c and s, running its tasks in dir. A task's shell sees only the
// variables task.DefaultShellEnv names, never the runner's secrets.
func New(c Config, s Secrets, dir string, log io.Writer) *Runner {
	r := &Runner{cfg: c, secrets: s, log: log}
	r.drivers = Drivers{
		Root:          dir,
		Env:           task.DefaultShellEnv,
		Log:           log,
		Default:       "scripted",
		LLMBaseURL:    cmp.Or(c.Models.BaseURL, s.PortkeyBaseURL),
		LLMKey:        s.PortkeyAPIKey,
		LLMModel:      c.Models.Model,
		LLMVirtualKey: cmp.Or(c.Models.Portkey.VirtualKey, s.PortkeyVirtualKey),
		VirtualKeyOr:  r.virtualKey,
		NoKey:         "set " + EnvPortkeyAPIKey,
		Agents:        c.Agents,
		Tools:         c.Tools,
	}
	if s.PortkeyAPIKey != "" {
		r.drivers.Default = "llm"
	}
	return r
}

// Build is a runnerd builder.
func (r *Runner) Build(l task.Launch, reg *task.Registry) *task.Task {
	return r.drivers.Build(l, reg)
}

// virtualKey is the Portkey virtual key a run uses: the one its spawn picked, when the runner
// accepts the annotation, else the configured one.
//
// A key picked under a runner that doesn't accept the annotation is said in the log, since
// nothing refuses such a spawn yet. The key itself isn't logged.
func (r *Runner) virtualKey(picked string) string {
	own := cmp.Or(r.cfg.Models.Portkey.VirtualKey, r.secrets.PortkeyVirtualKey)
	if picked == "" {
		return own
	}
	if r.cfg.accepts(AnnotationVirtualKey) {
		return picked
	}
	fmt.Fprintf(r.log, "a spawn picked a Portkey virtual key, but the runner doesn't accept %s: "+
		"the configured one is used\n", AnnotationVirtualKey)
	return own
}

// ReflectAll retries, once, every push a gate approved that did not land: for a runner that
// starts holding approved work, before it serves anything.
func (r *Runner) ReflectAll(ctx context.Context) {
	if r.mirrors != nil {
		r.reflectAll(ctx)
	}
}
