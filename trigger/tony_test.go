package trigger

import (
	"reflect"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
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
			Action: Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		},
		{
			Name: "spawn-a-review",
			Condition: Condition{
				When: MatchWhen(Match{"system": "demo", "kind": "pr"}),
			},
			Gates: []Gate{{Name: "human", Mode: "ask"}},
			Action: Action{Spawn: &launch.Spec{
				Driver: "llm", Task: "review $subject", Vector: "local",
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
	for _, gone := range []string{"gates:", "subject:", "spawn:", "patch:"} {
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

// A formula WRITTEN IN JSON is the same formula written in tony. That is the direction
// that still matters: verse no longer renders JSON, but people still write it, and the
// tag-keyed spelling (`{"until": [...]}`) is the only way a JSON-shaped charter can reach
// an operator — JSON has no tag syntax.
//
// The keyspace discipline this rests on is asserted separately by
// TestMatchKeysDisjointFromFormulaTags: a single-key object is read as an operator only
// because no match dimension shares a name with one.
func TestFormulaReadsTheJSONKeyForm(t *testing.T) {
	const asJSON = `{"until": [{"quorum": {"n": 2, "choice": "reject"}}, {"deadline": "4h"}]}`

	var got Formula
	if err := codec.Unmarshal([]byte(asJSON), &got, codec.JSON); err != nil {
		t.Fatalf("from json: %v", err)
	}
	if got.Op != OpUntil || len(got.Args) != 2 {
		t.Fatalf("the key form did not read as an operator: %+v", got)
	}
	if got.Args[0].Req.Choice != "reject" || got.Args[0].Req.N != 2 {
		t.Errorf("quorum detail lost: %+v", got.Args[0].Req)
	}
	if got.Args[1].Req.Within != "4h" {
		t.Errorf("deadline detail lost: %+v", got.Args[1].Req)
	}

	// And it is the same value the tony spelling gives, which is what "one grammar in two
	// syntaxes" has to mean.
	var fromTony Formula
	if err := codec.Unmarshal([]byte(`!until [!quorum {n: 2, choice: reject}, !deadline "4h"]`),
		&fromTony, codec.Tony); err != nil {
		t.Fatalf("from tony: %v", err)
	}
	if !reflect.DeepEqual(got, fromTony) {
		t.Errorf("the two spellings differ:\n json: %+v\n tony: %+v", got, fromTony)
	}

	// Rendering goes out as tags, in both formats verse writes.
	asTony, err := codec.Marshal(&got, codec.Tony)
	if err != nil {
		t.Fatalf("to tony: %v", err)
	}
	for _, want := range []string{"!until", "!quorum", "!deadline"} {
		if !strings.Contains(string(asTony), want) {
			t.Errorf("%s did not come back as a tag:\n%s", want, asTony)
		}
	}
}

// An unknown tag is an error, not a silently-ignored formula. A typo'd charter must fail at
// install rather than match nothing — the same discipline Match.Validate enforces.
// An unquoted duration is the easy mistake to make, and it is a quiet one: a tony literal
// may not start with a digit and commas are optional, so `!deadline 4h` reads as the
// number 4 beside a separate "h" rather than as the string "4h". That is the correct
// reading, which is the problem — the document is well-formed, so the parser has nothing
// to say and we are the last line. Taking the 4 as a threshold would leave the deadline
// empty, and a gate whose deadline silently isn't one waits forever.
func TestNumericDeadlineIsRejected(t *testing.T) {
	for _, src := range []string{`!deadline 4`, `!until [!quorum 2, !deadline 4]`} {
		var f Formula
		err := codec.Unmarshal([]byte(src), &f, codec.Tony)
		if err == nil {
			t.Fatalf("%s: a numeric deadline was accepted", src)
		}
		if !strings.Contains(err.Error(), `"4h"`) {
			t.Errorf("%s: the error does not show the fix: %v", src, err)
		}
	}
	// The quoted spelling is the one that works, and it must keep working.
	var f Formula
	if err := codec.Unmarshal([]byte(`!deadline "4h"`), &f, codec.Tony); err != nil {
		t.Fatalf(`!deadline "4h": %v`, err)
	}
	if got := f.Req.Within; got != "4h" {
		t.Errorf("Within = %q, want %q", got, "4h")
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
