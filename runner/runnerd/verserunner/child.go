package verserunner

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/signadot/verse/answer"
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
// anything the config names under `agents:`. A program has no tools, so nothing it does leaves
// the workspace: delivery is a tool, and a driver that offers the runner's tools to its program
// (as the codex driver does) is how a program comes to deliver.
func buildCommandTask(l task.Launch, reg *task.Registry, cmd Command, env Environment, dir string) (*task.Task, error) {
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
		dir: dir,
	}
	if contract != nil {
		d.contractSrc = contract.Source()
	}
	t := task.NewTask(l.ID, task.NewSupervisor(vec, reg), d, &commandExec{d: d})
	t.SetLog(os.Stderr)
	// The answer contract: the harness's give-up is a tool call (`yield`) and this driver has
	// no tools, so the repair names the thing this program can actually do.
	if contract != nil {
		t.SetAnswerCheck(&answer.Checker{C: contract, FailHint: "saying you cannot"})
	}
	d.emit = t.Emit
	return t, nil
}

// offeredTools is the runner's tools that pass the environment's floor: all of them when the
// floor names none, else those it names. The same rule the harness applies to its own tools
// (agent.selectTools), stated once more here because the delivery brief names them before
// the harness is built.
func offeredTools(extra []agent.Tool, floor []string) []agent.Tool {
	if len(floor) == 0 {
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
