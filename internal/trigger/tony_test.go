package trigger

import (
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/launch"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/task"
)

// A charter must survive a round trip through every dialect. It asserts the round trip
// rather than the spelling on purpose — the spelling is pinned by
// TestCharterGrammarSpellings, and keeping the two apart is what let this test guard the
// move to native tags instead of having to be rewritten by it.
func TestCharterRoundTrip(t *testing.T) {
	charters := []Trigger{
		{
			Name: "advance-on-review",
			Condition: Condition{
				When:    MatchWhen(Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}),
				Subject: mm("demo.pr.*"),
			},
			Action: Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		},
		{
			Name: "spawn-a-review",
			Condition: Condition{
				Subject: mm("demo.pr.*"),
			},
			Gates: []Gate{{Name: "human", Mode: "ask"}},
			Action: Action{Spawn: &launch.Spec{
				Agent: &runner.Agent{Name: "viff"}, Prompt: "review $[subject.ref]", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}},
			}},
		},
		{
			Name: "quorum-gated",
			Condition: Condition{
				Subject: mm("demo.pr.*"),
			},
			Gates: []Gate{{
				Name: "two-approvals",
				Need: 2,
			}},
			Action: Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
		},
	}

	for _, want := range charters {
		for _, f := range []codec.Format{codec.Tony, codec.YAML} {
			t.Run(want.Name+"/"+f.String(), func(t *testing.T) {
				b, err := codec.Marshal(want, f)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				var got Trigger
				if err := codec.Unmarshal(b, &got, f); err != nil {
					t.Fatalf("unmarshal %q: %v", b, err)
				}
				if got.Name != want.Name {
					t.Fatalf("name: got %q want %q\n%s", got.Name, want.Name, b)
				}
				if (got.Condition.When == nil) != (want.Condition.When == nil) {
					t.Errorf("when survived differently\n%s", b)
				}
				// The subject has to survive — it is the one field a charter cannot be installed
				// without. Compare what it MATCHES.
				probe := entity.RefAt("demo", "pr", "1")
				if len(got.Condition.Subject) == 0 {
					t.Errorf("the subject did not survive\n%s", b)
				} else if got.Condition.Subject.MatchRef(probe) != want.Condition.Subject.MatchRef(probe) {
					t.Errorf("subject: got %v want %v\n%s", got.Condition.Subject, want.Condition.Subject, b)
				}
				if len(got.Gates) != len(want.Gates) {
					t.Errorf("gates: got %d want %d\n%s", len(got.Gates), len(want.Gates), b)
				}
				if (got.Action.Spawn == nil) != (want.Action.Spawn == nil) {
					t.Errorf("spawn survived differently\n%s", b)
				}
				if got.Action.Spawn != nil && got.Action.Spawn.Who() != want.Action.Spawn.Who() {
					t.Errorf("spawn.agent: got %q want %q\n%s",
						got.Action.Spawn.Who(), want.Action.Spawn.Who(), b)
				}
			})
		}
	}
}

// Optional charter fields must not appear when unset — a charter is written and read by
// hand, so this is the difference between a readable document and a wall of nulls. `when` is
// one of those now, and `subject` is not: the minimal charter is the one that carries only the
// subject, and a charter missing it is not a terse document but an uninstallable one.
func TestCharterOmitsUnsetFields(t *testing.T) {
	b, err := codec.Marshal(Trigger{
		Name:      "minimal",
		Condition: Condition{Subject: mm("demo.pr.*")},
	}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, gone := range []string{"gates:", "when:", "spawn:", "patch:"} {
		if strings.Contains(got, gone) {
			t.Errorf("unset field %q leaked into the charter:\n%s", gone, got)
		}
	}
	if !strings.Contains(got, "subject:") {
		t.Errorf("the required field did not survive rendering:\n%s", got)
	}
}

// Every spelling the charter grammar accepts, decoded to the same AST. The tony forms are
// native tags; the JSON forms are the tag-as-key emulation that predates them. Both are one
// grammar with one decoder, which is the point of Stage 4 — not two implementations that
// have to be kept in step.
func TestCharterGrammarSpellings(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		fmt  codec.Format
		want func(*Formula) bool
	}{
		{
			"tony/address-sugar", `demo.pr.*`, codec.Tony,
			func(f *Formula) bool {
				return f.IsMatchAtom() && isPath(f.Req.Facts, "demo.pr.*")
			},
		},
		{
			"tony/bare-match", `{path: "demo.pr.*"}`, codec.Tony,
			func(f *Formula) bool { return f.IsMatchAtom() && isPath(f.Req.Facts, "demo.pr.*") },
		},
		{
			"tony/empty-match", `{}`, codec.Tony,
			func(f *Formula) bool { return f.IsMatchAtom() && f.Req.Facts.IsEmpty() },
		},
		{
			"tony/explicit-match-tag", `!match {path: "demo.*.*"}`, codec.Tony,
			func(f *Formula) bool { return f.IsMatchAtom() && isPath(f.Req.Facts, "demo.*.*") },
		},

		// The JSON spelling, decoded by the same code path.
		{
			"json/address-sugar", `"demo.pr.*"`, codec.JSON,
			func(f *Formula) bool { return f.IsMatchAtom() && isPath(f.Req.Facts, "demo.pr.*") },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got Formula
			if err := codec.Unmarshal([]byte(c.doc), &got, c.fmt); err != nil {
				t.Fatalf("decoding %s: %v", c.doc, err)
			}
			if !c.want(&got) {
				t.Errorf("%s decoded to %+v (req %+v)", c.doc, got, got.Req)
			}
		})
	}
}

func TestUnknownFormulaTagIsRejected(t *testing.T) {
	var f Formula
	err := codec.Unmarshal([]byte(`!untl [!quorum 2]`), &f, codec.Tony)
	if err == nil {
		t.Fatal("a misspelled operator was accepted")
	}
	if !strings.Contains(err.Error(), "untl") {
		t.Errorf("the error does not name the tag: %v", err)
	}
}
