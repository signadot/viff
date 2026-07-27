package versed

import (
	"testing"

	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/process"
)

// The complaint, at the seam it was lost on. `vector:` and `grant:` were expanded by Bind
// and read by nothing: the Spawner took Task and Driver off the spec and dropped the rest.
// A charter could state an envelope, watch `trigger show` render it back, and get an agent
// that ignored it.
func TestLaunchOfResolvesTheNamedAxes(t *testing.T) {
	l, err := launchOf("rev-1", launch.Spec{
		Task: "review it", Driver: "llm", Vector: "auto", Grant: "read-only",
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.Vector == nil || l.Vector.Default != process.Auto {
		t.Errorf("vector = %v, want the named auto vector", l.Vector)
	}
	if l.Grant == nil {
		t.Fatal("grant = nil; `grant: read-only` must reach the spawn")
	}
	read := process.ToolUse{Name: "read", Input: map[string]any{"path": "x"}}
	exec := process.ToolUse{Name: "run_bash", Input: map[string]any{"command": "x"}}
	process.BasicDeriver{}.Derive(&read)
	process.BasicDeriver{}.Derive(&exec)
	if !l.Grant.Permits(read) || l.Grant.Permits(exec) {
		t.Error("the resolved grant does not behave as read-only")
	}
}

// A spec that names neither axis must produce neither, so a process that declared nothing
// keeps exactly the behaviour it had: the runtime's own vector, and an unbounded scope.
// Resolving "unstated" to a value — any value — would change every existing charter.
func TestLaunchOfLeavesUnstatedAxesUnset(t *testing.T) {
	l, err := launchOf("p1", launch.Spec{Task: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Vector != nil || l.Grant != nil {
		t.Fatalf("unstated axes resolved to vector=%v grant=%v, want both unset", l.Vector, l.Grant)
	}
}

// A misspelt envelope must fail the spawn. Dropping the field is how this bug started;
// defaulting it to the wildcard would be the same bug with an error message attached, and
// strictly worse than the original because it reads as though it was honored.
func TestLaunchOfRefusesAnUnknownGrantRatherThanWidening(t *testing.T) {
	if _, err := launchOf("p1", launch.Spec{Task: "go", Grant: "reed-only"}); err == nil {
		t.Fatal("an unknown grant name must fail the spawn, not resolve to unbounded")
	}
	if _, err := launchOf("p1", launch.Spec{Task: "go", Vector: "sometimes"}); err == nil {
		t.Fatal("an unknown vector name must fail the spawn")
	}
}

// The frame and tool set ride along untouched — they are the request, not a reference, so
// there is nothing to resolve and nothing that may be dropped.
func TestLaunchOfCarriesFrameAndTools(t *testing.T) {
	l, err := launchOf("p1", launch.Spec{
		Task: "go", Frame: "answer with a severity", Tools: []string{"read_file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if l.Frame != "answer with a severity" {
		t.Errorf("frame = %q", l.Frame)
	}
	if len(l.Tools) != 1 || l.Tools[0] != "read_file" {
		t.Errorf("tools = %v", l.Tools)
	}
}
