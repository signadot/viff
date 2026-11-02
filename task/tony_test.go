package task

import (
	"reflect"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/systems"
)

// The wire types must survive a round trip through every dialect. The JSON path is the
// oracle: it is what verse serves today, so a tony round trip that disagrees with it is a
// regression, not a new spelling.
func TestTonyRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want func() any
	}{
		{"instance", Snapshot{
			ID: "run-1", Prompt: "review", Agent: "llm", Status: Running,
			Cursor:  7,
			Pending: []PendingGate{{ID: "d1", Gate: "shell", Step: StepCommandsRun, Fields: systems.FieldsDoc(StepCommandsRun, map[string]string{"command": "ls"}), Reason: "why"}},
		}, func() any { return &Snapshot{} }},

		{
			"instance-minimal",
			Snapshot{ID: "run-2", Status: Done},
			func() any { return &Snapshot{} },
		},

		{"frame", Event{
			Kind: EventDecision, Task: "run-1", Cursor: 3, ID: "d1",
			Gate: "shell", Step: StepCommandsRun, Fields: systems.FieldsDoc(StepCommandsRun, map[string]string{"command": "ls"}), By: "user:walter@example.com", Proceed: true, Text: "ok",
		}, func() any { return &Event{} }},

		{"autonomy", Autonomy{
			{Name: "release", Match: &Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"repo": ir.FromString("signadot/verse"), "ref": systems.Glob("refs/heads/release/*")}},
				Then: Then{Mode: ThenAsk, Ask: &Ask{Need: 2, Within: "24h", Answerers: []string{"group:release"}, Default: "deny", Labels: map[string]string{"team": "release"}}}},
			{Match: &Match{Verb: "push"}, Then: Then{Mode: ThenDeny}},
			{Then: Then{Mode: ThenAsk}},
		}, func() any { return &Autonomy{} }},

		{"policy-minimal", Autonomy{{Then: Then{Mode: ThenAllow}}}, func() any { return &Autonomy{} }},
	}

	for _, c := range cases {
		for _, f := range []codec.Format{codec.Tony, codec.YAML} {
			t.Run(c.name+"/"+f.String(), func(t *testing.T) {
				b, err := codec.Marshal(c.v, f)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				got := c.want()
				if err := codec.Unmarshal(b, got, f); err != nil {
					t.Fatalf("unmarshal %q: %v", b, err)
				}
				// Read back and written again, it is the wire it was read from: a pattern is a
				// tony node, compared as what it says rather than as the struct holding it.
				again, err := codec.Marshal(reflect.ValueOf(got).Elem().Interface(), f)
				if err != nil {
					t.Fatalf("marshal again: %v", err)
				}
				if string(again) != string(b) {
					t.Errorf("round trip differs\n wire: %s\n  got: %s", b, again)
				}
			})
		}
	}
}

// Optional fields must not appear when unset — the readability this whole migration is
// for. Without codec's omitzero pass, gomap's reflection path emits all ten.
func TestTonyOmitsUnsetFields(t *testing.T) {
	b, err := codec.Marshal(Snapshot{ID: "run-1", Status: Running}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(b))
	if got != "id: run-1\nstatus: running" {
		t.Errorf("unset fields leaked into the document:\n%s", got)
	}
}
