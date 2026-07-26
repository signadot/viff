package trigger

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/launch"
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

// The `json:` struct tags are no longer a wire format — nothing in verse serves through
// encoding/json — but they are still THERE, and two tag sets that disagree about a field
// name is the bug class hardest to see. This checks they agree, which is the only reason
// the json: tags are worth keeping until they are swept.
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
		{"tony/quorum-threshold", `!quorum 2`, codec.Tony,
			func(f *Formula) bool { return f.Req != nil && f.Req.Kind == ReqQuorum && f.Req.N == 2 }},
		{"tony/quorum-object", `!quorum {n: 2, choice: reject}`, codec.Tony,
			func(f *Formula) bool { return f.Req.N == 2 && f.Req.Choice == "reject" }},
		{"tony/fact", `!fact motion:deadline`, codec.Tony,
			func(f *Formula) bool {
				return f.Req.Kind == ReqFact && f.Req.Facts.System() == "motion" && f.Req.Facts.Kind() == "deadline"
			}},
		{"tony/deadline", `!deadline "4h"`, codec.Tony,
			func(f *Formula) bool { return f.Req.Kind == ReqDeadline && f.Req.Within == "4h" }},
		{"tony/until", `!until [!quorum 2, !deadline "4h"]`, codec.Tony,
			func(f *Formula) bool {
				return f.Op == OpUntil && len(f.Args) == 2 &&
					f.Args[0].Req.Kind == ReqQuorum && f.Args[1].Req.Kind == ReqDeadline
			}},
		{"tony/address-sugar", `demo:pr:*`, codec.Tony,
			func(f *Formula) bool {
				return f.IsMatchAtom() && f.Req.Facts.System() == "demo" && f.Req.Facts.Kind() == "pr"
			}},
		{"tony/bare-match", `{system: demo, kind: pr}`, codec.Tony,
			func(f *Formula) bool { return f.IsMatchAtom() && f.Req.Facts.Kind() == "pr" }},
		{"tony/empty-match", `{}`, codec.Tony,
			func(f *Formula) bool { return f.IsMatchAtom() && f.Req.Facts.IsEmpty() }},
		{"tony/explicit-match-tag", `!match {system: demo}`, codec.Tony,
			func(f *Formula) bool { return f.IsMatchAtom() && f.Req.Facts.System() == "demo" }},

		// The JSON spelling, decoded by the same code path.
		{"json/tag-keyed-quorum", `{"quorum": 2}`, codec.JSON,
			func(f *Formula) bool { return f.Req.Kind == ReqQuorum && f.Req.N == 2 }},
		{"json/tag-keyed-until", `{"until": [{"quorum": 2}, {"fact": "motion:deadline"}]}`, codec.JSON,
			func(f *Formula) bool { return f.Op == OpUntil && len(f.Args) == 2 }},
		{"json/verbose-node", `{"op": "atom", "req": {"kind": "quorum", "n": 2}}`, codec.JSON,
			func(f *Formula) bool { return f.Op == OpAtom && f.Req.Kind == ReqQuorum && f.Req.N == 2 }},
		{"json/address-sugar", `"demo:pr:*"`, codec.JSON,
			func(f *Formula) bool { return f.IsMatchAtom() && f.Req.Facts.System() == "demo" }},
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

// A formula written in tony must survive a trip through JSON and back. This is what makes
// the two dialects one grammar rather than two: codec.Detag renders the tags as the key
// form, and the decoder reads the key form back into tags.
func TestFormulaSurvivesDialectRoundTrip(t *testing.T) {
	orig := &Formula{Op: OpUntil, Args: []*Formula{
		{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, N: 2, Choice: "reject"}},
		{Op: OpAtom, Req: &Requirement{Kind: ReqDeadline, Within: "4h"}},
	}}
	asJSON, err := codec.Marshal(orig, codec.JSON)
	if err != nil {
		t.Fatalf("to json: %v", err)
	}
	if !strings.Contains(string(asJSON), `"until"`) {
		t.Fatalf("the tag did not become a key: %s", asJSON)
	}
	var back Formula
	if err := codec.Unmarshal(asJSON, &back, codec.JSON); err != nil {
		t.Fatalf("from json: %v", err)
	}
	asTony, err := codec.Marshal(&back, codec.Tony)
	if err != nil {
		t.Fatalf("to tony: %v", err)
	}
	for _, want := range []string{"!until", "!quorum", "!deadline"} {
		if !strings.Contains(string(asTony), want) {
			t.Errorf("%s did not come back as a tag:\n%s", want, asTony)
		}
	}
	if back.Args[0].Req.Choice != "reject" || back.Args[1].Req.Within != "4h" {
		t.Errorf("detail lost through the round trip: %+v", back.Args)
	}
}

// An unknown tag is an error, not a silently-ignored formula. A typo'd charter must fail at
// install rather than match nothing — the same discipline Match.Validate enforces.
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
