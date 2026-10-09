package task

import (
	"context"
	"strings"
	"testing"
)

// AN ACTION DESCRIBES ITSELF IN ONE LINE, from what it reported about itself: the command, the
// path, where a push goes, or its inputs compactly. It is what an answerer reads on the docket,
// where the tool's name and the derived verb say only who asked and what kind of thing it is.
func TestDescribeSaysWhatTheActionIs(t *testing.T) {
	cases := []struct {
		name string
		tu   ToolUse
		want string
	}{
		{"a shell command", ToolUse{Name: "run_bash", Input: map[string]any{"command": "go test ./..."}}, "go test ./..."},
		{"a file write, by its path and not its content",
			ToolUse{Name: "write_file", Input: map[string]any{"path": "docs/a.md", "content": "a long body\nover lines"}}, "docs/a.md"},
		{"a push, by destination and ref",
			ToolUse{Name: "push", Input: map[string]any{"destination": "auto", "repo": "verse", "to": "refs/heads/verse/auto/1"}},
			"auto → refs/heads/verse/auto/1"},
		{"a push with only a destination", ToolUse{Name: "push", Input: map[string]any{"destination": "main"}}, "main"},
		{"a question, in its words", ToolUse{Name: "ask", Input: map[string]any{"question": "which colour?", "options": []any{"red", "blue"}}}, "which colour?"},
		{"a delegation, by agent and prompt",
			ToolUse{Name: "call_agent", CallAgent: &CallAgentRequest{Agent: "reviewer", Prompt: "check the tests"}}, "reviewer: check the tests"},
		{"a command over several lines keeps its first",
			ToolUse{Name: "run_bash", Input: map[string]any{"command": "go build ./...\ngo test ./..."}}, "go build ./... …"},
		{"anything else: its inputs, in key order, records as their values",
			ToolUse{Name: "apply_patch", Input: map[string]any{
				"reason":  "needs it",
				"changes": []any{map[string]any{"path": "docs/x.md", "kind": "update"}, map[string]any{"path": "y.go", "kind": "add"}},
			}}, "changes: update docs/x.md, add y.go; reason: needs it"},
		{"scalars of every kind", ToolUse{Name: "x", Input: map[string]any{"n": 3, "ok": true, "s": "v"}}, "n: 3; ok: true; s: v"},
		{"nothing said", ToolUse{Name: "x"}, ""},
		{"nested structure is not rendered", ToolUse{Name: "x", Input: map[string]any{"deep": map[string]any{"a": 1}}}, ""},
	}
	for _, c := range cases {
		if got := c.tu.Describe(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// AND IT IS BOUNDED: a record is not the place for a screen of shell.
func TestDescribeIsBounded(t *testing.T) {
	long := strings.Repeat("x", 1000)
	got := ToolUse{Name: "run_bash", Input: map[string]any{"command": long}}.Describe()
	if len(got) > describeMax+3 || !strings.HasSuffix(got, "…") {
		t.Errorf("a %d-byte command rendered to %d bytes", len(long), len(got))
	}
}

// AND THE QUESTION CARRIES IT: what the registry announces about a blocked action says what the
// action is, so every publisher of the docket has it without reading the action itself.
func TestAPendingQuestionCarriesTheAction(t *testing.T) {
	reg := NewRegistry()
	got := make(chan Pending, 1)
	reg.OnPost(func(p Pending) { got <- p })
	q := GateQuestion{ID: "run-x~d1", Origin: "run-x", Gate: "shell", Mode: Ask}
	go func() {
		_, _ = reg.Resolve(context.Background(), q, ToolUse{Name: "run_bash", Input: map[string]any{"command": "go test ./..."}})
	}()
	p := <-got
	if p.Action != "go test ./..." || p.Tool != "run_bash" {
		t.Errorf("the docket is told %q for %s, want the command", p.Action, p.Tool)
	}
	reg.Answer(q.ID, Resolution{Choice: Proceed, Proceed: true, By: "test"})
}
