package task

import (
	"reflect"
	"testing"
)

// The verse's own tools are classified where every driver's supervisor can reach them. A
// scripted run is one `run_bash` call under this package's supervisor, so a command that
// derived nothing here would match no gate over `exec` and be refused by any named grant as
// unclassifiable.
func TestTheVerseToolsAreClassifiedByTheBasicDeriver(t *testing.T) {
	cases := []struct {
		tool  string
		input map[string]any
		want  Effect
	}{
		{"run_bash", map[string]any{"command": "go test ./..."}, Effect{Verb: Exec, Resource: Resource{Kind: KindTask, Ref: "go test ./..."}}},
		{"read_file", map[string]any{"path": "notes.md"}, Effect{Verb: Read, Resource: File("notes.md")}},
		{"list_dir", map[string]any{"path": "docs"}, Effect{Verb: Read, Resource: File("docs")}},
		{"write_file", map[string]any{"path": "notes.md", "content": "x"}, Effect{Verb: Write, Resource: File("notes.md")}},
	}
	for _, c := range cases {
		tu := derive(ToolUse{Name: c.tool, Input: c.input})
		if !reflect.DeepEqual(tu.Effects, []Effect{c.want}) {
			t.Errorf("%s derives %+v, want exactly %+v", c.tool, tu.Effects, c.want)
		}
	}
}

// A shell command is exec to both checks: `no-delegate`, which admits exec, lets it through, and
// a gate over `exec` asks about it.
func TestAShellCommandIsExecToAGrantAndToAGate(t *testing.T) {
	g, _, err := NamedGrant("no-delegate")
	if err != nil {
		t.Fatal(err)
	}
	tu := derive(ToolUse{Name: "run_bash", Input: map[string]any{"command": "date"}})
	if !g.Permits(tu) {
		t.Error("no-delegate refuses run_bash, though it admits exec")
	}
	v := &Vector{Default: Auto, Gates: []Gate{{Name: "shell", Mode: Ask, Match: GateMatch{Verbs: []Verb{Exec}}}}}
	if d := v.Evaluate(tu); d.Mode != Ask || d.Gate != "shell" {
		t.Errorf("a gate over exec gives run_bash %v/%q (%s), want ASK/shell", d.Mode, d.Gate, d.Reason)
	}
}
