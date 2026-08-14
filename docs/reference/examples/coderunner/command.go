package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/signadot/verse/task"
)

// The command driver runs an external program in the workspace and takes what it says as the
// task's result. A coding agent binary is one such program; so is a script.
//
// It replaced a driver called `claude`, and the reason is worth keeping because it is a
// judgement about where a distinction belongs rather than a tidy-up.
//
// From verse's side, invoking a foreign agent and running a script are the SAME SHAPE: one
// opaque action, a subprocess, an exit status, some output. Two drivers meant two subprocess
// spawners — two places to apply containment, build an environment, choose a working directory
// — and the duplication had already cost something: confinement was applied on the agent path
// and not on the harness's `run_bash`, so the one tool that can leave a workspace was the one
// not covered. One path is one place to get that right.
//
// What is NOT folded away is the ability to RE-INVOKE. A driver that runs once and completes
// cannot honour a steer (it has nowhere to put it) and cannot repair an answer that missed its
// contract (it cannot ask again). Both are the same capability, and it is generic rather than
// anything to do with agents — so it lives here, and a program that can resume a session gets
// steering and contract repair for free.
//
// What verse gates here is the INVOCATION, not what the program does inside it. Nothing can gate
// inside a subprocess, and one action per turn says so honestly rather than implying a
// supervision that is not there. The read plane gets what the program reports about itself,
// which is a self-report and labelled as one.

// Command is how to invoke one program, from the runner's config. `claude` is an entry in that
// config rather than a type in this file, which is the whole point: nothing here knows what a
// coding agent is.
type Command struct {
	// Argv is the program and its arguments. `$prompt` is replaced with the text this turn is
	// asking for. Required.
	Argv []string `tony:"field=argv"`
	// Resume is appended instead of nothing on turns after the first, with `$session`
	// replaced by whatever the last turn reported. A program with no notion of a session
	// leaves this empty and gets one turn — which is a correct program, not a broken one, and
	// simply cannot be steered or asked to repair an answer.
	Resume []string `tony:"field=resume,omitzero"`
	// Output says how to read what the program said: `text` (stdout IS the result) or `json`
	// (stdout is one object carrying `result`, `session_id` and `is_error`). Empty = text.
	//
	// Two values rather than a path language on purpose. A field path would be a small
	// query dialect nobody validates, for a shape that is either "the program printed the
	// answer" or "the program printed a wrapper around it".
	Output string `tony:"field=output,omitzero"`
}

func (c Command) argv(prompt, session string) []string {
	out := make([]string, 0, len(c.Argv)+len(c.Resume))
	for _, a := range c.Argv {
		out = append(out, strings.ReplaceAll(a, "$prompt", prompt))
	}
	if session != "" {
		for _, a := range c.Resume {
			out = append(out, strings.ReplaceAll(a, "$session", session))
		}
	}
	return out
}

// commandResult is the slice of a json-output program's document this depends on. Anything else
// it reports is ignored rather than decoded, so a new field upstream is not a breaking change.
type commandResult struct {
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
	Subtype   string `json:"subtype"`
}

type commandDriver struct {
	name   string // the driver name a spawn asked for, for messages
	cmd    Command
	prompt string
	frame  string
	env    Environment
	point  Point
	conf   Confiner
	dir    string

	// contractSrc is the shape the answer must have, for the OPENING turn only — the check
	// itself is the loop's (task.AnswerCheck). Stated up front for the reason harness.Build
	// states it: meeting the check for the first time as a rejection wastes a repair attempt
	// learning what it could have been told at the start.
	contractSrc string

	session string // what the program called this conversation, for Resume
	last    string // its last final message: this task's result
	started bool

	mu     sync.Mutex
	steers []string

	emit func(task.Frame)
}

func (d *commandDriver) Next(_ context.Context, prev task.StepResult) (task.ToolUse, bool, error) {
	if !d.started {
		d.started = true
		return d.turn(d.opening()), true, nil
	}
	if prev.Err != nil {
		return task.ToolUse{}, false, prev.Err
	}
	if prev.Blocked {
		// The gate refused this invocation. There is no other way to do this work — the
		// program IS the doer — so the honest end is to stop and say so rather than loop.
		return task.ToolUse{}, false, fmt.Errorf("the invocation was refused: %s", prev.Note)
	}
	// Everything below needs another turn, and a program with no Resume cannot have one. Saying
	// so is better than looping on a first turn forever or dropping the request in silence.
	again := func(text, why string) (task.ToolUse, bool, error) {
		if len(d.cmd.Resume) == 0 {
			return task.ToolUse{}, false, fmt.Errorf(
				"%s: %s, but this command declares no way to resume a session", d.name, why)
		}
		return d.turn(text), true, nil
	}
	// A steer that arrived during the turn is the next turn. A contract repair is one too —
	// it arrives here the same way, because the loop hands both in through Steer — and the
	// precedence between them is the loop's (machine.go: an instruction from outside is new
	// authorization and outranks a shape complaint the program has not seen yet).
	if s := d.drain(); s != "" {
		return again(s, "there is more to say to the program")
	}
	return task.ToolUse{}, false, nil
}

// opening is the first turn: the frame (standing instructions) and the prompt (this job),
// concatenated because a program takes one text. That is a real difference from the harness,
// where a frame is a system message the model cannot confuse with the task.
func (d *commandDriver) opening() string {
	var b strings.Builder
	if d.frame != "" {
		b.WriteString(d.frame)
		b.WriteString("\n\n")
	}
	if d.contractSrc != "" {
		fmt.Fprintf(&b, "ANSWER CONTRACT. Your final message must satisfy:\n%s\n\n", d.contractSrc)
	}
	b.WriteString(d.prompt)
	return b.String()
}

func (d *commandDriver) turn(text string) task.ToolUse {
	return task.ToolUse{
		Name:  "run",
		Input: map[string]any{"text": text, "command": d.name},
		// Derived here rather than left to a deriver's guess: this action hands the work to
		// another program, which is the verb the vector already knows how to gate as
		// delegation.
		//
		// `CallAgent` is deliberately nil. That field is not a label — it is the instruction
		// that makes the loop route an action to spawnChild and build a sub-task through a
		// SpawnFunc this runner does not have. With it set, every task failed with "no spawn
		// factory configured" the moment its gate was approved.
		Effects: []task.Effect{{Verb: task.CallAgent, Resource: task.AgentRes(d.name)}},
	}
}

// Steer implements task.Steerable: the loop drains instructions at the boundary before Next, so
// they land cooperatively — honoured at the next turn, never interrupting one in flight.
func (d *commandDriver) Steer(instructions []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.steers = append(d.steers, instructions...)
}

func (d *commandDriver) drain() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.steers) == 0 {
		return ""
	}
	s := strings.Join(d.steers, "\n")
	d.steers = nil
	return s
}

// Result implements task.Resulter: the program's last output is this task's result, which is
// what a completed task's `result` carries and what a charter's `$result` derefs.
func (d *commandDriver) Result() string { return d.last }

// commandExec runs one turn. It is THE subprocess path of this runner: where containment is
// applied and where the environment is BUILT rather than inherited.
type commandExec struct{ d *commandDriver }

func (e *commandExec) Execute(ctx context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	d := e.d
	text, _ := tu.Input["text"].(string)
	argv := d.cmd.argv(text, d.session)
	if len(argv) == 0 {
		return task.StepResult{Use: tu}, nil, fmt.Errorf("%s: empty argv", d.name)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// What makes this Execute honour its context, which task.Executor states as an obligation.
	// CommandContext kills the program when the task is cancelled, but stdout below is a buffer
	// rather than a file, so os/exec copies through a pipe and Wait waits for the COPY. Anything
	// the agent left running — a dev server, a watcher, a `&` in a script it wrote — inherits
	// that pipe and holds it open, and the task becomes uncancellable. An agent binary is far
	// more likely to leave something running than a one-line run_bash, so this seam needs it more
	// than the one it is borrowed from (harness.orphanWaitDelay, which explains the number).
	cmd.WaitDelay = 2 * time.Second
	// The environment is the allowlist and nothing else. os.Environ() is deliberately not
	// consulted: a passthrough list is a floor, and "everything except what we remembered to
	// strip" is not.
	cmd.Env = buildEnv(d.env.Env, d.env.Commands)
	if err := d.conf.Confine(cmd, d.point, Sandbox{Dir: d.dir}); err != nil {
		return task.StepResult{Use: tu}, nil, err
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()

	res, err := d.read(out.Bytes(), errb.String(), runErr)
	if err != nil {
		return task.StepResult{Use: tu}, nil, err
	}
	if res.SessionID != "" {
		d.session = res.SessionID // every later turn resumes this
	}
	if res.Result != "" {
		d.last = res.Result
	}
	if res.IsError {
		return task.StepResult{Use: tu, Output: res.Result}, nil,
			fmt.Errorf("%s failed (%s): %s", d.name, res.Subtype, firstLine(res.Result))
	}
	if d.emit != nil {
		d.emit(task.Frame{Kind: task.EvSay, Text: res.Result})
	}
	return task.StepResult{Use: tu, Output: res.Result}, nil, nil
}

// read turns what the program printed into a result, per its declared Output.
func (d *commandDriver) read(stdout []byte, stderr string, runErr error) (commandResult, error) {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = strings.TrimSpace(string(stdout))
	}
	if d.cmd.Output != "json" {
		if runErr != nil {
			return commandResult{}, fmt.Errorf("%s: %v: %s", d.name, runErr, firstLine(detail))
		}
		return commandResult{Result: strings.TrimRight(string(stdout), "\n")}, nil
	}
	var res commandResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &res); err != nil {
		// No decodable answer is not an empty answer. A program that is missing, or that wrote
		// a usage message, fails the turn with what it actually said.
		if runErr != nil && detail == "" {
			detail = runErr.Error()
		}
		return commandResult{}, fmt.Errorf("%s produced no readable result: %s", d.name, detail)
	}
	return res, nil
}

// buildEnv is the passthrough allowlist plus a PATH narrowed to the commands the environment
// opted into.
//
// The PATH half is honest only as far as the filesystem is: an absolute path walks straight past
// an allowlist, so this is a floor when `filesystem.scope` is `private` and a convention
// otherwise. Stated here rather than discovered, because a list that looks like a permission and
// is not is worse than no list.
// It NEVER returns nil, and that is not a style choice. `exec.Cmd.Env == nil` means "inherit
// os.Environ()", so an environment that declared no passthrough — or whose variables all happened
// to be unset — got the opposite of what it asked for: the whole of the runner's environment,
// including the git token, handed to the task. A list that is a floor when non-empty and an open
// door when empty is worse than no list, and the comment above claimed the opposite.
func buildEnv(passthrough, commands []string) []string {
	env := []string{}
	for _, name := range passthrough {
		if v, ok := lookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	_ = commands // the symlink farm belongs with the mount namespace; see confine.go
	return env
}
