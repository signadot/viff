package verse

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/task"
)

// run_bash sees the declared passthrough and nothing else of the process hosting it.
//
// The process running the harness is frequently the one holding the model credentials the agent
// runs on, and a shell that inherited its environment handed them to the agent — reachable with
// `env`, needing no gate to be crossed and no cleverness from the model.
func TestRunBashSeesOnlyThePassthrough(t *testing.T) {
	t.Setenv("VERSE_TEST_SECRET", "hunter2")
	t.Setenv("VERSE_TEST_PASSED", "yes")
	e := &fsExec{root: t.TempDir(), log: io.Discard, env: []string{"VERSE_TEST_PASSED"}}
	res, _, err := e.Execute(context.Background(), task.ToolUse{Name: "run_bash",
		Input: map[string]any{"command": "env"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "hunter2") {
		t.Fatalf("an undeclared variable reached the shell:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "VERSE_TEST_PASSED=yes") {
		t.Fatalf("the declared variable did not reach the shell:\n%s", res.Output)
	}
}

// An executor that declares nothing passes nothing, rather than everything: a nil exec.Cmd.Env
// means "inherit", which is the opposite of an empty passthrough.
func TestRunBashWithNoPassthroughInheritsNothing(t *testing.T) {
	t.Setenv("VERSE_TEST_SECRET", "hunter2")
	e := &fsExec{root: t.TempDir(), log: io.Discard}
	res, _, err := e.Execute(context.Background(), task.ToolUse{Name: "run_bash",
		Input: map[string]any{"command": "env"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "hunter2") {
		t.Fatalf("an empty passthrough inherited the host environment:\n%s", res.Output)
	}
}
