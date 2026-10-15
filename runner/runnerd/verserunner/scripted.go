package verserunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/signadot/verse/task"
)

// ── scripted driver: the default task runtime driver ─────────────────────
//
// A dependency-free driver that performs a few steps of "work" then completes, so
// the runtime is exercisable without model credentials. `verse up --llm` swaps in
// the LLM harness.

// ScriptedBuilder builds the credential-free task runtime driver: it runs the prompt as a real
// script (by its #! line, or bash; see scriptCommand), confined to root and cancellable via the task context — a governed shell
// command as a task. It is the dependency-free stand-in for an LLM agent, so the runtime is
// exercisable and steerable (and can run real work like `sleep`) without model credentials.
// ScriptedBuilder is the runnerd.Builder form (root = the task's cwd). `verse up` uses
// NewScriptedTask directly to confine scripted commands to the agent root.
func ScriptedBuilder(l task.Launch, reg *task.Registry) *task.Task {
	return NewScriptedTask(l, "", task.DefaultShellEnv, reg)
}

// NewScriptedTask builds a scripted task rooted at root — a call list of exactly one call,
// which is what `scripted` always was. env names the variables its shell passes through from
// this process; see task.ShellEnv.
//
// The driver is task's, because one shell command is the degenerate case of a list of calls and
// a second implementation of "propose one action, then stop" would buy nothing; the autonomy is
// the SPAWN's, so a charter asking to be asked about its one command is asked.
//
// The consequence is deliberate and is the reason this is a contract change: a `driver:
// scripted` spawn that names no vector now ASKS, where it used to run unattended. The
// exemption that justified auto — "you authorized it by typing it" — is true of `verse task
// run --driver scripted` and of nothing else, so it lives on that path now (cmd/verse/task.go)
// rather than travelling to charters that typed nothing.
func NewScriptedTask(l task.Launch, root string, env []string, reg *task.Registry) *task.Task {
	// A LIST THE SPAWN WROTE IS THE LIST, and the prompt is read only where there is none: a
	// charter's `calls:` reaches this builder as l.Calls, and a builder that read the prompt
	// alone ran such a task to `done` having done nothing. An empty prompt and no list is an
	// empty list: the task completes having done nothing, which is what the old driver did
	// with a blank command.
	var calls []task.Call
	switch {
	case len(l.Calls) > 0:
		if err := task.CheckCalls(l.Calls); err != nil {
			return task.NewFailed(l.ID, reg, err.Error())
		}
		calls = l.Calls
	case strings.TrimSpace(l.Prompt) != "":
		calls = []task.Call{{Tool: "run_bash", Input: map[string]any{"command": l.Prompt}}}
	}
	set, ended, err := handCredential(l, root)
	if err != nil {
		return task.NewFailed(l.ID, reg, err.Error())
	}
	return ended(task.NewCallsTaskFrom(l, calls, reg, &shellExec{root: root, env: env, set: set}))
}

// shellExec runs run_bash for real: cancellable via ctx (so `cancel` kills a running command),
// confined to root, with only the variables env names. It is the scripted driver's counterpart
// of the harness's fsExec.
type shellExec struct {
	root string
	env  []string
	// set is the variables the shell is given outright, `NAME=value`: where the task's
	// credential file is, for a task that has one.
	set []string
}

func (e *shellExec) Execute(ctx context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	// Its environment is the declared passthrough rather than the server's, which carries the
	// server's credentials.
	c, done, err := scriptCommand(ctx, str(tu.Input, "command"))
	if err != nil {
		return task.StepResult{Use: tu, Output: "exit: " + err.Error(), Err: err}, nil, nil
	}
	defer done()
	c.Dir = e.root
	c.Env = append(task.ShellEnv(e.env), e.set...)
	out, err := c.CombinedOutput()
	res := strings.TrimSpace(string(out))
	// The code, alongside the Err this executor folds it into. Both, because the fold is this
	// executor's reading and a driver may want the fact underneath it — see
	// task.StepResult.Exit. A spawn failure (no such binary) has no exit code and gets none.
	var code *int
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		n := ee.ExitCode()
		code = &n
	}
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
	return task.StepResult{Use: tu, Output: res, Err: err, Exit: code}, nil, nil
}

// scriptCommand is how a script runs. A script that starts with a #! line is run by the program
// that line names, which can be anything that reads a file: sh, python3, node,
// `signadot sandbox apply -f`. The line is read as the kernel reads it: the program, and at most
// one argument, the rest of the line after a space or a tab (`#!/usr/bin/env -S` splits more).
// The script is written to a file the program is given, and done removes it. The file has no
// extension, so a program that needs one (`go run` wants .go) can't read it.
//
// A script with no #! line is run by bash -c (non-login, which avoids the login shell's
// job-control setpgid, "failed to change group ID", that fails in restricted environments).
//
// The file is read, never executed: executing a file this process has just written can fail
// with "text file busy" when another goroutine forks while it is open.
func scriptCommand(ctx context.Context, script string) (*exec.Cmd, func(), error) {
	line, ok := strings.CutPrefix(script, "#!")
	if !ok {
		return exec.CommandContext(ctx, "bash", "-c", script), func() {}, nil
	}
	line, _, _ = strings.Cut(line, "\n")
	line = strings.TrimSpace(line)
	prog, arg := line, ""
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		prog, arg = line[:i], line[i+1:]
	}
	if prog == "" {
		return nil, nil, errors.New("the script's #! line names no program")
	}
	f, err := os.CreateTemp("", "verse-script-*")
	if err != nil {
		return nil, nil, fmt.Errorf("the script could not be written: %w", err)
	}
	done := func() { os.Remove(f.Name()) }
	_, err = f.WriteString(script)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		done()
		return nil, nil, fmt.Errorf("the script could not be written: %w", err)
	}
	args := []string{f.Name()}
	if arg = strings.TrimSpace(arg); arg != "" {
		args = []string{arg, f.Name()}
	}
	return exec.CommandContext(ctx, prog, args...), done, nil
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
