package verserunner

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/runner/runnerd/calltool"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/verserunner/agent"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// A TASK'S PROCESS, when the runner gave it a workspace: one task, in its own process, serving
// the same task-control contract the runner serves to verse (runnerd.ServeTask).
//
// The reason is that containment has a shape, and the shape is a process. Everything the
// sandbox builds — namespaces, a private filesystem, no network — is applied by exec'ing
// something, so a task the runner ran in-process would be a task none of it reaches. A boundary
// drawn once, around the whole task, is a claim about one thing; a sandbox per tool would have to
// be complete to be a floor, and completeness is a claim about every tool anyone adds later.
//
// What crosses the boundary is the CONTRACT, not a private channel:
//
//   - control (cancel, pause, steer, resolve a gate, rebind a vector) and the read plane (SSE)
//     are the same routes verse calls on the runner, so gating and narration survive the hop
//     unchanged — they were never in-process, they were always a protocol.
//   - what the TASK needs — the model key for the `llm` driver — does cross, on stdin, because a
//     real coding agent needs secrets plumbed to it and pretending otherwise just moves the
//     problem. What bounds a secret once it is inside is a restricted network: no route out but
//     an allow-listing proxy (runnerd's egress).
//   - a PUSH never crosses it as a credential. The task asks over the delivery channel (fd 5),
//     and the runner checks the allowance, stages the work and pushes from its mirror with its
//     own token (runner/runnerd/delivery). The line that matters for the model key is the allowlist
//     that decides what a task's SUBPROCESSES inherit (buildEnv): the key reaches trusted Go,
//     and never the shell the model drives. The invariant is "no secret reaches the MODEL",
//     which is the thing whose output is published to a read plane, an entity store and a
//     commit body.
//
// The configuration arrives on STDIN rather than as a file or an environment. A file would have to
// be bind-mounted into the sandbox, where the task could read it; an environment variable is
// inherited by every subprocess the task starts. Stdin is read once, before anything is served,
// and is then gone.

// pushFD is the connected socket the runner hands the task process for the delivery channel,
// after the control listener (3) and the lifeline (4): the first descriptor the Preparer adds.
const pushFD = 5

// taskPrep is what a task's process is told about the workspace the runner prepared for it, and
// it is deliberately less than the runner's own configuration: one environment rather than the
// catalogue, and no git token.
type taskPrep struct {
	ID        string `tony:"field=id"`
	Workspace string `tony:"field=workspace"`
	Branch    string `tony:"field=branch"`
	// Bases is repository name → the sha its checkout started from: what a delivery is measured
	// against (workspace.Tree.AuthoredWork). It travels rather than being read here, because
	// reading it from the checkout would agree today and stop agreeing the moment a task
	// committed.
	Bases map[string]string `tony:"field=bases,omitzero"`
	Brief string            `tony:"field=brief,omitzero"`
	// Repos are the repositories in the workspace as the runner resolved them from the spawn: a
	// base this side read from the mirror's HEAD is one the other side must not have to guess.
	Repos       []workspace.Repo `tony:"field=repos,omitzero"`
	Environment Environment      `tony:"field=environment"`
	// Egress is the unix socket, AS THIS PROCESS SEES IT, that reaches the allowlisting proxy
	// the runner built for this task. Empty means the network is whatever the sandbox left it —
	// everything or nothing, both of which need no channel.
	Egress string `tony:"field=egress,omitzero"`
}

// buildCommandTask builds a task that runs an external program — an agent binary, a script,
// anything the config names under `agents:` — in dir, offering it tools: the runner's own and
// `ask`, passed the floor, which the program calls from its shell as `verse-call-tool` over a
// socket this task process serves. Nothing the program does leaves the workspace but through
// those tools: delivery is a tool, and a call of one is an action of the loop.
func buildCommandTask(l task.Launch, reg *task.Registry, cmd Command, env Environment, dir string, tools []agent.Tool) (*task.Task, error) {
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
		// The invocation hands the work to another program, and ASK is the honest default for
		// it — verse cannot see inside a subprocess, so the invocation is the one thing there is
		// to decide about before it runs. What the program then asks for through its tools is
		// each an action of its own, and auto lets those through unless a gate names them.
		vec = &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "agent", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.CallAgent}},
		}}}
	}
	d := &commandDriver{
		name: l.Driver, cmd: cmd,
		prompt: l.Prompt, frame: l.Frame, env: env,
		dir:   dir,
		calls: make(chan *toolCall, 8),
	}
	if contract != nil {
		d.contractSrc = contract.Source()
	}
	if len(tools) > 0 {
		if err := os.MkdirAll(binDir(dir), 0o755); err != nil {
			return nil, fmt.Errorf("the workspace's own directory: %w", err)
		}
		sock, err := socketFor(dir)
		if err != nil {
			return nil, err
		}
		srv := &calltool.Server{Tools: tools, Call: d.call}
		if err := srv.Start(sock); err != nil {
			return nil, fmt.Errorf("serving the task's tools: %w", err)
		}
		d.tools, d.set = srv, toolsEnv(dir, sock)
	}
	t := task.NewTask(l.ID, task.NewSupervisor(vec, reg), d, &commandExec{d: d})
	t.SetLog(os.Stderr)
	// The answer contract: the harness's give-up is a tool call (`yield`) and this driver has
	// no such tool, so the repair names the thing this program can actually do.
	if contract != nil {
		t.SetAnswerCheck(&answer.Checker{C: contract, FailHint: "saying you cannot"})
	}
	d.emit = t.Emit
	// The socket closes with the task: a program the task left running finds its tools gone,
	// which is what being gone should look like.
	frames, stop := t.Subscribe()
	go func() {
		for range frames { // the stream ends when the task does
		}
		stop()
		d.close()
	}()
	return t, nil
}

// offeredTools is the runner's tools that pass the floor: all of them when there is no floor
// (nil), else those it names — none, for `[]`. The same rule the harness applies to its own tools
// (agent.selectTools), stated once more here because the delivery brief names them before
// the harness is built.
func offeredTools(extra []agent.Tool, floor []string) []agent.Tool {
	if floor == nil {
		return extra
	}
	var out []agent.Tool
	for _, t := range extra {
		if slices.Contains(floor, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

// deliveryBrief tells an agent that has the `push` tool how its work leaves: commit it, with the
// trailers, then push. It rides the prompt beside the workspace brief, for the same reason: the
// arrangement is the runner's, and an agent that was not told it leaves edits uncommitted and
// believes it delivered. "" when nothing here can deliver.
func deliveryBrief(l task.Launch, ws *workspace.Workspace, tools []agent.Tool) string {
	if ws == nil || !ws.Deliverable() ||
		!slices.ContainsFunc(tools, func(t agent.Tool) bool { return t.Name == delivery.PushToolName }) {
		return ""
	}
	var b strings.Builder
	b.WriteString("To deliver work, commit it in the repository it belongs to, then call the `push` " +
		"tool, naming the branch it goes to; with no branch it pushes your own branch, `" + ws.Branch +
		"`, under that name. What each repository permits is listed above. Only commits are " +
		"delivered; uncommitted changes stay here. End every commit message with these trailers, " +
		"as its last paragraph, exactly:\n")
	for _, line := range workspace.RequiredTrailers(l) {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("A push of a commit without them is refused and tells you what is missing.")
	if slices.ContainsFunc(tools, func(t agent.Tool) bool { return t.Name == delivery.FetchToolName }) {
		b.WriteString(" If upstream may have moved since you started, `fetch` updates the " +
			"`mirror/*` refs here and you rebase onto them yourself.")
	}
	b.WriteString("\n\n")
	return b.String()
}
