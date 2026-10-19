package verserunner

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/verserunner/agent/modeltest"
	"github.com/signadot/verse/task"
)

// scriptRun is what a script's task came to: its output, and its failure.
type scriptRun struct {
	Output string
	Err    error
}

// runScript runs script as a task of its own, as a launch would.
func runScript(t *testing.T, script string) scriptRun {
	t.Helper()
	p := New(Config{}, Secrets{}, t.TempDir(), io.Discard).Build(runner.Launch{ID: "s",
		Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Doer: runner.Doer{Script: script, With: runner.With{Network: netOpen}}}, task.NewRegistry())
	_, _ = p.Run(context.Background()) // a failed script is a failed task, which Err says
	var err error
	if msg := p.Err(); msg != "" {
		err = errors.New(msg)
	}
	return scriptRun{Output: p.Result(), Err: err}
}

// A script's shell sees the declared passthrough and not the runner's environment,
// which carries the runner's secrets.
func TestAScriptSeesOnlyThePassthrough(t *testing.T) {
	t.Setenv("VERSE_TEST_SECRET", "hunter2")
	res := runScript(t, "env")
	if res.Err != nil {
		t.Fatalf("env failed: %v\n%s", res.Err, res.Output)
	}
	if strings.Contains(res.Output, "hunter2") {
		t.Fatalf("an undeclared variable reached the shell:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "PATH=") {
		t.Fatalf("the default passthrough lost PATH:\n%s", res.Output)
	}
}

// A script runs by its #! line, which can name any program that reads a file, with the one
// argument the line may carry; a script with none runs with bash.
func TestAScriptRunsByItsShebang(t *testing.T) {
	for _, tc := range []struct {
		name, script, want string
		needs              string
	}{
		{"no shebang", `echo "$BASH_VERSION" | cut -c1`, "", ""},
		{"sh", "#!/bin/sh\necho from sh", "from sh", ""},
		{"an argument", "#!/bin/sh -e\nfalse\necho not reached", "", ""},
		{"a tab", "#!/bin/sh\t-e\nfalse\necho not reached", "", ""},
		{"python", "#!/usr/bin/env python3\nprint(6 * 7)", "42", "python3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needs != "" {
				if _, err := exec.LookPath(tc.needs); err != nil {
					t.Skipf("%s is not installed", tc.needs)
				}
			}
			res := runScript(t, tc.script)
			switch tc.name {
			case "no shebang":
				// $BASH_VERSION's first character is bash's major version: a digit only bash says.
				if res.Err != nil || len(res.Output) != 1 || res.Output[0] < '1' || res.Output[0] > '9' {
					t.Fatalf("not run by bash: %v %q", res.Err, res.Output)
				}
			case "an argument", "a tab":
				if res.Err == nil || strings.Contains(res.Output, "not reached") {
					t.Fatalf("-e was not passed: %v %q", res.Err, res.Output)
				}
			default:
				if res.Err != nil || res.Output != tc.want {
					t.Fatalf("got %v %q, want %q", res.Err, res.Output, tc.want)
				}
			}
		})
	}
}

func TestAScriptFileIsRemovedWhenDone(t *testing.T) {
	c, done, err := scriptCommand(context.Background(), "#!/bin/sh\ntrue")
	if err != nil {
		t.Fatal(err)
	}
	file := c.Args[len(c.Args)-1]
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the script file isn't there: %v", err)
	}
	done()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the script file is still there: %v", err)
	}
}

func TestAShebangWithNoProgramFails(t *testing.T) {
	res := runScript(t, "#!\necho hi")
	if res.Err == nil || !strings.Contains(res.Err.Error(), "names no program") {
		t.Fatalf("got %v %q", res.Err, res.Output)
	}
}

// A script's task ends with its output: done when it exits 0, failed with the reason otherwise,
// and the output it wrote is the result either way.
func TestAScriptsTaskEndsWithItsOutput(t *testing.T) {
	r := New(Config{}, Secrets{}, t.TempDir(), io.Discard)
	for _, tc := range []struct {
		script, status, result, err string
	}{
		{"echo hello", "done", "hello", ""},
		{"echo partial; exit 3", "failed", "partial", "the script failed: exit status 3"},
	} {
		p := r.Build(runner.Launch{ID: "s", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Doer: runner.Doer{Script: tc.script, With: runner.With{Network: netOpen}}}, task.NewRegistry())
		oc, _ := p.Run(context.Background())
		if oc.Reason != tc.status || p.Result() != tc.result || p.Err() != tc.err {
			t.Errorf("%q: %s %q %q, want %s %q %q", tc.script, oc.Reason, p.Result(), p.Err(), tc.status, tc.result, tc.err)
		}
	}
}

// A launch's timeout ends a task that runs too long, cancelled with the reason; the time it spends
// waiting for an answer doesn't count.
func TestATimeoutEndsATaskButNotItsWaiting(t *testing.T) {
	h := runnerd.NewInProcessHandler(New(Config{}, Secrets{}, t.TempDir(), io.Discard).Build)
	_, err := h.Launch(runner.Launch{ID: "slow", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Timeout: "300ms",
		Doer: runner.Doer{Script: "sleep 5", With: runner.With{Network: netOpen}}})
	if err != nil {
		t.Fatal(err)
	}
	// An agent's command asks, and is answered after longer than its timeout: the wait is free.
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		if res := r.Results(); len(res) > 0 {
			return modeltest.Say(strings.TrimSpace(res[0][strings.LastIndex(res[0], "\n")+1:]))
		}
		return modeltest.Use("run_bash", map[string]any{"command": "echo ran"})
	})
	agents := runnerd.NewInProcessHandler(New(Config{}, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL},
		t.TempDir(), io.Discard).Build)
	_, err = agents.Launch(runner.Launch{ID: "waits", Prompt: "go", Timeout: "300ms", Policy: task.Policy{{Match: &task.Match{System: "commands"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAsk}}}, Doer: runner.Doer{Agent: AgentName, With: runner.With{Network: netOpen}}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	gate := ""
	if s, ok := agents.SnapshotOf("waits"); ok && len(s.Pending) == 1 {
		gate = s.Pending[0].ID
	}
	if !agents.Resolve("waits", gate, task.Choose(task.Proceed, "user:ana")) {
		t.Fatalf("the command wasn't asked, or wasn't answered")
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		a, _ := h.SnapshotOf("slow")
		b, _ := agents.SnapshotOf("waits")
		if a.Status.Terminal() && b.Status.Terminal() {
			break
		}
	}
	if s, _ := h.SnapshotOf("slow"); s.Status != task.Cancelled || !strings.Contains(s.Err, "timed out") {
		t.Errorf("slow: %s %q, want cancelled for its timeout", s.Status, s.Err)
	}
	if s, _ := agents.SnapshotOf("waits"); s.Status != task.Done || s.Result != "ran" {
		t.Errorf("waits: %s %q %q, want done: its wait didn't count", s.Status, s.Result, s.Err)
	}
}

// A script that exits leaving a child running ends: the child holds its output open, and is cut
// off once the script has been gone for scriptWaitDelay.
func TestAScriptThatLeavesAChildEnds(t *testing.T) {
	start := time.Now()
	res := runScript(t, "sleep 30 &\necho started")
	if res.Err != nil || res.Output != "started" {
		t.Fatalf("got %v %q", res.Err, res.Output)
	}
	if took := time.Since(start); took > scriptWaitDelay+5*time.Second {
		t.Fatalf("it took %v: the child held the script's end", took)
	}
}

// A TMPDIR too long for a unix socket's path fails the script's task with that said, rather than
// with bind's words.
func TestATMPDIRTooLongForTheSocketIsSaid(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 100))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	res := runScript(t, "true")
	if res.Err == nil || !strings.Contains(res.Err.Error(), "too long for a unix socket") ||
		!strings.Contains(res.Err.Error(), "TMPDIR") {
		t.Fatalf("got %v", res.Err)
	}
}
