package trigger

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/launch"
)

// A charter must survive a round trip through every dialect. Today the tony spelling goes
// through gomap's reflection path, so a Formula renders as the verbose {op, req, args}
// parse node rather than the tagged form — structurally correct, but not yet how a charter
// is meant to read. Stage 4 gives Formula and Match hand-written codecs that emit
// `!until [ !quorum 2 ]`; this test is what guards that change, so it asserts the
// round trip rather than the spelling.
func TestCharterRoundTrip(t *testing.T) {
	charters := []Trigger{
		{
			Name: "advance-on-review",
			Condition: Condition{
				When:    MatchWhen(Match{"system": "demo", "kind": "pr", "fields": map[string]any{"stage": "open"}}),
				Subject: Match{"system": "demo", "kind": "pr", "id": "*"},
			},
			Action: Action{Set: map[string]any{"stage": "reviewed"}},
		},
		{
			Name: "spawn-a-review",
			Condition: Condition{
				When: MatchWhen(Match{"system": "demo", "kind": "pr"}),
			},
			Gates: []Gate{{Name: "human", Mode: "ask"}},
			Action: Action{Spawn: &launch.Spec{
				Driver: "llm", Task: "review $subject", Policy: "local",
			}},
		},
		{
			Name: "quorum-gated",
			Condition: Condition{
				When: MatchWhen(Match{"system": "demo", "kind": "pr"}),
			},
			Gates: []Gate{{
				Name:    "two-approvals",
				Formula: &Formula{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, N: 2}},
			}},
			Action: Action{Set: map[string]any{"stage": "shipped"}},
		},
	}

	for _, want := range charters {
		for _, f := range []codec.Format{codec.Tony, codec.JSON, codec.YAML} {
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
				if len(got.Gates) != len(want.Gates) {
					t.Errorf("gates: got %d want %d\n%s", len(got.Gates), len(want.Gates), b)
				}
				if (got.Action.Spawn == nil) != (want.Action.Spawn == nil) {
					t.Errorf("spawn survived differently\n%s", b)
				}
				if got.Action.Spawn != nil && got.Action.Spawn.Driver != want.Action.Spawn.Driver {
					t.Errorf("spawn.driver: got %q want %q\n%s",
						got.Action.Spawn.Driver, want.Action.Spawn.Driver, b)
				}
			})
		}
	}
}

// The JSON path is the oracle: it is what verse serves today, so codec's JSON rendering
// must name the same fields encoding/json does. If these diverge, the migration would
// silently rename fields on the wire.
func TestCharterJSONFieldNamesMatchOracle(t *testing.T) {
	tr := Trigger{
		Name: "x",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "demo"}),
			Subject: Match{"system": "demo", "kind": "pr"},
		},
		Gates:  []Gate{{Name: "g", Mode: "ask"}},
		Action: Action{Set: map[string]any{"stage": "s"}},
	}

	viaCodec, err := codec.Marshal(tr, codec.JSON)
	if err != nil {
		t.Fatal(err)
	}
	viaOracle, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}

	var a, b map[string]any
	if err := json.Unmarshal(viaCodec, &a); err != nil {
		t.Fatalf("codec JSON is not valid JSON: %v (%s)", err, viaCodec)
	}
	if err := json.Unmarshal(viaOracle, &b); err != nil {
		t.Fatal(err)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			t.Errorf("codec JSON is missing top-level field %q\n codec:  %s\n oracle: %s",
				k, viaCodec, viaOracle)
		}
	}
}

// Optional charter fields must not appear when unset — a charter is written and read by
// hand, so this is the difference between a readable document and a wall of nulls.
func TestCharterOmitsUnsetFields(t *testing.T) {
	b, err := codec.Marshal(Trigger{
		Name:      "minimal",
		Condition: Condition{When: MatchWhen(Match{})},
	}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, gone := range []string{"gates:", "subject:", "spawn:", "set:"} {
		if strings.Contains(got, gone) {
			t.Errorf("unset field %q leaked into the charter:\n%s", gone, got)
		}
	}
}
