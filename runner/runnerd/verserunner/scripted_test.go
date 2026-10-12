package verserunner

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/signadot/verse/task"
)

// The scripted driver's shell sees the declared passthrough and not the server's environment,
// which carries the server's credentials.
func TestScriptedShellSeesOnlyThePassthrough(t *testing.T) {
	t.Setenv("VERSE_TEST_SECRET", "hunter2")
	e := &shellExec{root: t.TempDir(), env: task.DefaultShellEnv}
	res, _, _ := e.Execute(context.Background(), task.ToolUse{Name: "run_bash",
		Input: map[string]any{"command": "env"}})
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
			e := &shellExec{root: t.TempDir(), env: task.DefaultShellEnv}
			res, _, _ := e.Execute(context.Background(), task.ToolUse{Name: "run_bash",
				Input: map[string]any{"command": tc.script}})
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
	e := &shellExec{root: t.TempDir(), env: task.DefaultShellEnv}
	res, _, _ := e.Execute(context.Background(), task.ToolUse{Name: "run_bash",
		Input: map[string]any{"command": "#!\necho hi"}})
	if res.Err == nil || !strings.Contains(res.Output, "names no program") {
		t.Fatalf("got %v %q", res.Err, res.Output)
	}
}
