package trigger

import (
	"reflect"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/codec"
)

// A PLACE STAYS A PLACE WHEN IT IS WRITTEN DOWN, which is the property a restart rests on: the
// store's copy of the charter is the charter, and a spelling that means something else on the way
// back is a charter nobody edited quietly changing.
//
// `{path: "a.b.c"}` is a PLACE at every length, and it must come back as the same place. A
// stratum that came back as something else overlapped its neighbour, so restore refused it, and
// then refused every rule whose derive needed the level it named.
//
// The charter shrank across a restart: `verse-dev.drift` lost `ledger` and `comment-drift`,
// `verse-dev.merge` lost `train`, `train-load` and `train-run`. Nothing failed; there were simply
// fewer rules than the store held.
func TestAPathStratumSurvivesBeingWrittenDown(t *testing.T) {
	head := Stratum{Name: "drift", Level: 10,
		Match: []Match{mustPathMatch(t, `{path: "verse-dev.drift.head"}`)}}
	ledger := Stratum{Name: "ledger", Level: 20,
		Match: []Match{mustPathMatch(t, `{path: "verse-dev.drift.finding"}`)}}

	back := map[string]Stratum{}
	for _, st := range []Stratum{head, ledger} {
		node, err := (&st).ToTonyIR()
		if err != nil {
			t.Fatalf("%s: %v", st.Name, err)
		}
		var got Stratum
		if err := (&got).FromTonyIR(node); err != nil {
			t.Fatalf("%s: %v", st.Name, err)
		}
		if err := got.validate(); err != nil {
			t.Errorf("%s does not install after a round trip: %v", st.Name, err)
		}
		if len(got.Match) != 1 {
			t.Fatalf("%s came back as %d matches: a path is ONE match, and its segments are not "+
				"a union of names", st.Name, len(got.Match))
		}
		if want := st.Match[0].Segs(); !reflect.DeepEqual(got.Match[0].Segs(), want) {
			t.Errorf("%s came back as %q, want the path %q", st.Name, got.Match[0].Segs(), want)
		}
		back[st.Name] = got
	}

	if Matches(back["drift"].Match).overlaps(back["ledger"].Match) {
		t.Error("two disjoint places overlap after a round trip, which is what restore refused")
	}
}

// A stratum's `match` list is a UNION, and `!verse-path` is not a spelling of one address inside
// it: a tagged list is refused rather than read as a union of its steps, each of which would be a
// one-segment path matching everything under it.
func TestATaggedListIsRefusedNotAUnion(t *testing.T) {
	node, err := codec.ParseNode([]byte(`!verse-path [verse-dev, drift, finding, "*"]`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	var ms Matches
	if err := ms.FromTonyIR(node); err == nil {
		t.Fatalf("`!verse-path` read as %d matches; want a refusal", len(ms))
	}
	// The union form reads as a union.
	node, err = codec.ParseNode([]byte(`[demo.pr.*, {path: "gh.issue.*"}]`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	var union Matches
	if err := union.FromTonyIR(node); err != nil {
		t.Fatal(err)
	}
	if len(union) != 2 {
		t.Errorf("the union form read as %d matches, wanted 2", len(union))
	}
}

// A SUBJECT TOO: the rendering a rule is stored with has to name the same place the rule named.
func TestADeepSubjectRendersAsAPath(t *testing.T) {
	m := mustPathMatch(t, `{path: "verse-dev.drift.head"}`)
	node, err := m.ToTonyIR()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := codec.EncodeNode(&out, node, codec.Tony); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), VersePathTag) {
		t.Errorf("a place is written down with the folding spelling: %s", out.String())
	}
	var got Match
	if err := got.FromTonyIR(node); err != nil {
		t.Fatal(err)
	}
	if !isPath(got, "verse-dev.drift.head") {
		t.Errorf("the place came back as %q", got.Segs())
	}
}
