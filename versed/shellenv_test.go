package versed

import (
	"context"
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
