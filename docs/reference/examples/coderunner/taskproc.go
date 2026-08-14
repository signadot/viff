package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/harness"
	"github.com/signadot/verse/task/taskd"
)

// `coderunner -task` is the other half of this program: ONE task, in ITS OWN PROCESS, serving
// the same task-control contract the parent serves to verse.
//
// The reason is that containment has a shape, and the shape is a process. Everything this runner
// can build a point at — namespaces, a private filesystem, no network — is applied by exec'ing
// something, so a task the runner runs IN-PROCESS is a task none of it reaches. That produced the
// backwards arrangement worth stating plainly: the `llm` driver, the one whose every action is
// gated and narrated, was the one running uncontained, while the opaque agent binary got the
// sandbox. Supervision and containment were mutually exclusive, and the trade was invisible from
// the outside.
//
// Wrapping each tool instead cannot close it. `run_bash` is the tool that leaves, but a confiner
// applied per-tool has to be complete to be a floor, and completeness is a claim about every tool
// anyone adds later. A boundary drawn once, around the whole task, is a claim about one thing.
//
// What crosses the boundary is the CONTRACT, not a private channel:
//
//   - control (cancel, pause, steer, resolve a gate, rebind a vector) and the read plane (SSE)
//     are the same routes verse calls on the parent, so gating and narration survive the hop
//     unchanged — they were never in-process, they were always a protocol.
//   - the credential this runner pushes with does NOT cross it. Delivery stays in the parent,
//     which is what makes "the task cannot reach the token" a property of the process boundary
//     rather than of an env-var allowlist.
//   - what the TASK needs — the model key for the `llm` driver — does cross, on stdin below,
//     because a real coding agent needs secrets plumbed to it and pretending otherwise just
//     moves the problem. What bounds a secret once it is inside is `network.egress: restricted`,
//     which no confiner here builds yet: see docs/working/coding-runner-residual.md.
//
// The config arrives on STDIN rather than as a file or an environment. A file would have to be
// bind-mounted into the sandbox, where the task could read it; an environment variable is
// inherited by every subprocess the task starts. Stdin is read once, before anything is served,
// and is then gone.

// The two inherited descriptors, in the order the parent puts them in cmd.ExtraFiles.
//
// fd 3 is a LISTENING socket, not a path. That is the point: a unix socket the parent could dial
// by name would have to exist somewhere, and anywhere inside the sandbox is somewhere the task
// can connect to its own control plane and answer its own gates. An inherited listener has no
// name in the child's filesystem at all — the parent holds the only path, outside the mount
// namespace.
//
// fd 4 is a lifeline: a pipe whose write end the parent holds and never writes to. Reading it
// returns EOF exactly when the parent closes it or dies, which is the one signal that works both
// as a graceful stop and as an orphan check. bwrap's `--die-with-parent` covers the second on
// Linux and nothing covers it on macOS, so this is here rather than there.
const (
	controlFD  = 3
	lifelineFD = 4
)

// childConfig is everything the task process is told, and it is deliberately less than the
// runner's own config: one environment rather than the catalogue, resolved credentials rather
// than the names of environment variables to look them up in, and no git token.
type childConfig struct {
	ID        string `tony:"field=id"`
	Workspace string `tony:"field=workspace"`
	Branch    string `tony:"field=branch"`
	// Bases is repository name → the sha its checkout started from. It travels rather than
	// being read here so that a `$sha` in a push target expands identically on both sides of
	// the process boundary: this side names the ref its delivery gate matches on, the parent
	// writes it, and a gate matching a ref nobody pushed is the divergence worth spending a
	// field to prevent.
	Bases       map[string]string  `tony:"field=bases,omitzero"`
	Brief       string             `tony:"field=brief,omitzero"`
	Environment Environment        `tony:"field=environment"`
	Agents      map[string]Command `tony:"field=agents,omitzero"`
	LLM         LLMCreds           `tony:"field=llm,omitzero"`
	// Egress is the unix socket, AS THIS PROCESS SEES IT, that reaches the allowlisting proxy
	// the runner built for this task. Empty means egress is whatever the containment left it —
	// either everything or nothing, both of which need no channel.
	Egress string `tony:"field=egress,omitzero"`
}

// LLMCreds is the resolved form of Config.LLM: values, not the names of variables to read them
// from. The parent does the lookup because the parent is where the environment is, and sending
// values means the child needs no inherited environment at all — which is what lets
// buildEnv's allowlist be the whole of what a task's subprocess sees.
type LLMCreds struct {
	BaseURL    string `tony:"field=baseUrl,omitzero"`
	Model      string `tony:"field=model,omitzero"`
	Key        string `tony:"field=key,omitzero"`
	VirtualKey string `tony:"field=virtualKey,omitzero"`
}

// runTaskProcess is `coderunner -task`: read the config, reopen the workspace the parent
// prepared, and serve one task on the inherited listener until the parent takes the lifeline
// away.
func runTaskProcess() error {
	blob, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("-task: reading the config on stdin: %w", err)
	}
	var cc childConfig
	if err := codec.Unmarshal(blob, &cc, codec.Tony); err != nil {
		return fmt.Errorf("-task: the config on stdin does not parse: %w", err)
	}
	// The checkout is the parent's work — it holds the mirrors and the credential. This side
	// opens what is already there, which is also why its Trees carry no auth: nothing on this
	// side of the boundary can reach a remote.
	ws, err := Reopen(cc.Workspace, cc.Environment, cc.Branch, cc.Bases)
	if err != nil {
		return fmt.Errorf("-task: %w", err)
	}
	ln, err := net.FileListener(os.NewFile(controlFD, "control"))
	if err != nil {
		return fmt.Errorf("-task: fd %d is not the listener this process serves on (it is started "+
			"by the runner, not by hand): %w", controlFD, err)
	}
	go dieWithParent()

	if cc.Egress != "" {
		if err := useEgress(&cc); err != nil {
			return fmt.Errorf("-task: %w", err)
		}
	}
	p := &taskProc{cfg: cc, ws: ws}
	// The runner's closing act, proposed inside this task's loop so the vector can gate it and
	// the read plane can record it. The credential to perform it is on the other side of fd 5,
	// which is the whole reason this is a request rather than a push (deliver.go).
	if conn, err := net.FileConn(os.NewFile(deliverFD, "deliver")); err == nil {
		p.fin = &deliverFinisher{ws: ws, conn: conn}
	}
	return http.Serve(ln, taskd.New(p.build).Handler())
}

// useEgress makes the runner's allowlisting proxy usable as a proxy.
//
// The proxy is on the other side of a unix socket, and nothing speaks "HTTP proxy over a unix
// socket" — not Go's transport, not curl, not go, not npm. So this process listens on its OWN
// loopback and pipes each connection down the socket, which turns the socket into an ordinary
// `http://127.0.0.1:<port>` proxy that everything already knows how to use.
//
// Loopback is safe to expose here for the reason the socket is: with `--unshare-net` this is a
// network namespace of one process tree, so 127.0.0.1 is reachable by the task and by nothing
// else on the machine. Without the unshare it would be the host's loopback and this would be a
// proxy anything could use — which is one more reason a confiner that cannot unshare refuses
// `restricted` rather than approximating it.
//
// The variables are then set two ways because there are two consumers: this process, for the llm
// driver's own model call, and the task's subprocesses, which get them by the environment
// allowlist rather than by inheritance.
func useEgress(cc *childConfig) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("egress: no loopback to put the proxy on (%w) — with the network "+
			"unshared this is the one interface there should be", err)
	}
	sock := cc.Egress
	go func() {
		for {
			down, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer down.Close()
				up, err := net.Dial("unix", sock)
				if err != nil {
					return
				}
				defer up.Close()
				go func() { _, _ = io.Copy(up, down) }()
				_, _ = io.Copy(down, up)
			}()
		}
	}()

	proxy := "http://" + ln.Addr().String()
	names := []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}
	for _, n := range names {
		// Set on this process before anything makes a request: net/http reads the proxy
		// environment ONCE, on the first use, and caches it for the life of the program.
		if err := os.Setenv(n, proxy); err != nil {
			return err
		}
	}
	// And declared as passthrough, so the environment allowlist carries them into every
	// subprocess. Appending to the allowlist rather than special-casing them in buildEnv keeps
	// one mechanism: what a task's process gets is what the environment names, and the runner has
	// just named four more.
	cc.Environment.Env = append(cc.Environment.Env, names...)
	return nil
}

// dieWithParent exits when the lifeline closes. Exit 0 because the ordinary case is the parent
// reaping a finished task; the parent is the one that knows whether that was expected, and it
// has already recorded a terminal status by then either way.
func dieWithParent() {
	f := os.NewFile(lifelineFD, "lifeline")
	if f == nil {
		return
	}
	var b [1]byte
	for {
		if _, err := f.Read(b[:]); err != nil {
			break
		}
	}
	os.Exit(0)
}

type taskProc struct {
	cfg childConfig
	ws  *Workspace
	fin task.Finisher // the runner's closing act: deliver (deliver.go)
}

// build is the taskd Builder on this side of the hop — the driver dispatch that used to happen
// in the runner's own process, with everything about workspaces, mirrors and delivery gone.
//
// The containment argument is gone too, and its absence is the win. This process IS the
// containment: whatever point the parent built, it built around all of this, so a `run_bash` the
// harness runs is inside it, an agent binary is inside it, and neither needed a confiner of its
// own. That is why hostConfiner appears below — at this depth it means "just set the working
// directory", which is now the honest whole of it.
func (p *taskProc) build(l task.Launch, reg *task.Registry, content task.ContentReader) *task.Task {
	// The workspace layout is the runner's convention, so the runner describes it. It rides the
	// PROMPT rather than the frame because harness.Build derives a default frame from the tools
	// and vector when a spawn writes none, and setting one here would silently replace advice
	// that is more useful than this.
	l.Prompt = p.cfg.Brief + l.Prompt

	var t *task.Task
	switch l.Driver {
	case driverLLM, "":
		// Tools: the spawn narrows, the environment floors. A spawn that names none gets the
		// environment's set, and a tool absent from the environment is never offered whatever a
		// spawn asks for — which is what makes withholding `run_bash` a floor rather than advice.
		tools := l.Tools
		if len(tools) == 0 {
			tools = p.cfg.Environment.Tools
		}
		t, _ = harness.Build(harness.Config{
			ID: l.ID, Prompt: l.Prompt, Root: p.ws.Root, Resolver: reg, Content: content,
			Frame: l.Frame, Tools: tools, Answer: l.Answer, Vector: l.Vector,
			Log:    os.Stderr, // the parent copies this onto its own log, tagged with the task id
			Finish: p.fin,     // the runner's own closing act, inside the loop
			// Resolved by the parent and sent on stdin. Nothing here reads an environment
			// variable for a credential: that is the ambient-credential shape this design
			// refuses everywhere else.
			LLMBaseURL: p.cfg.LLM.BaseURL, LLMModel: p.cfg.LLM.Model,
			LLMKey:        p.cfg.LLM.Key,
			LLMVirtualKey: p.cfg.LLM.VirtualKey,
		})
	default:
		cmd, ok := p.cfg.Agents[l.Driver]
		if !ok {
			// The parent checks this before it ever starts a process, so reaching it means the
			// two ends disagree about the config — worth failing loudly rather than defaulting.
			return task.NewFailed(l.ID, reg, fmt.Sprintf(
				"this task process was not given an agent named %q", l.Driver))
		}
		var err error
		t, err = buildCommandTask(l, reg, cmd, p.cfg.Environment, p.ws.Root, p.fin)
		if err != nil {
			return task.NewFailed(l.ID, reg, err.Error())
		}
	}
	// What a gate can match on: the branch, the base, what has been touched, and where it would
	// go. `GateMatch.Branches` and `GateMatch.OnPush` read this, so a charter can say `ask` on
	// anything touching a release branch and `auto` everywhere else.
	t.SetRepo(p.ws.RepoContext)
	return t
}

// buildCommandTask builds a task that runs an external program — an agent binary, a script,
// anything the config names under `agents:`.
func buildCommandTask(l task.Launch, reg *task.Registry, cmd Command, env Environment, dir string, fin task.Finisher) (*task.Task, error) {
	var contract *answer.Contract
	if l.Answer != nil {
		c, err := answer.Parse(l.Answer)
		if err != nil {
			// A contract that cannot be parsed fails the spawn, for the reason an unknown tool
			// name does: the charter asked for something specific, and running without it while
			// saying nothing is how a field comes to mean nothing.
			return nil, fmt.Errorf("answer contract: %w", err)
		}
		contract = c
	}
	vec := l.Vector
	if vec == nil {
		// One action per turn, and that action hands the work to another program. ASK is the
		// honest default for it — verse cannot see inside a subprocess, so the invocation is the
		// only thing there is to decide about, which is the whole difference from the `llm`
		// driver and the reason both exist.
		vec = &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "agent", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.CallAgent}},
		}}}
	}
	d := &commandDriver{
		name: l.Driver, cmd: cmd,
		prompt: l.Prompt, frame: l.Frame, env: env,
		// The point is already built, around this whole process. hostConfiner here sets the
		// working directory and claims nothing, which is exactly right one level in.
		point: hostProvides, conf: hostConfiner{},
		dir: dir,
	}
	if contract != nil {
		d.contractSrc = contract.Source()
	}
	t := task.NewTask(l.ID, task.NewSupervisor(vec, reg), d, &commandExec{d: d})
	t.SetLog(os.Stderr)
	// Whatever the agent did, the runner's delivery is the last action of the task and is
	// gated like one — installed on the loop, the same way the harness does it for the llm
	// driver. The answer contract rides the same seam: "fail" is the harness's give-up tool
	// and this driver has none, so the repair names the thing this program can actually do.
	if fin != nil {
		t.SetFinisher(fin)
	}
	if contract != nil {
		t.SetAnswerCheck(&answer.Checker{C: contract, FailHint: "saying you cannot"})
	}
	d.emit = t.Emit
	return t, nil
}
