package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
)

// A PLACE STAYS A PLACE WHEN IT IS WRITTEN DOWN, which is the property a restart rests on: the
// store's copy of the charter is the charter, and a spelling that means something else on the way
// back is a charter nobody edited quietly changing.
//
// Two spellings, and the difference is exactly three segments long. `{path: [a, b, c]}` is a
// PLACE at every length. `!verse-path [a b c]` FOLDS into the three dimensions, deliberately, so
// that the vector spelling names the entity `a:b:c` the way every charter written before paths
// does. A stratum rendered with the second came back constraining `id` — which a stratum may not
// do, since a derived id is the engine's — so restore refused it, and then refused every rule
// whose derive needed the level it named.
//
// The charter shrank across a restart: `verse-dev.drift` lost `ledger` and `comment-drift`,
// `verse-dev.merge` lost `train`, `train-load` and `train-run`. Nothing failed; there were simply
// fewer rules than the store held.
func TestAPathStratumSurvivesBeingWrittenDown(t *testing.T) {
	head := Stratum{Name: "drift", Level: 10,
		Match: []Match{mustPathMatch(t, `{path: [verse-dev, drift, head]}`)}}
	ledger := Stratum{Name: "ledger", Level: 20,
		Match: []Match{mustPathMatch(t, `{path: [verse-dev, drift, finding]}`)}}

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
		if !got.Match[0].Deep() {
			t.Errorf("%s came back as dimensions rather than a path: %v", st.Name, got.Match[0])
		}
		back[st.Name] = got
	}

	if Matches(back["drift"].Match).overlaps(back["ledger"].Match) {
		t.Error("two disjoint places overlap after a round trip, which is what restore refused")
	}
}

// The union form and the path form are both lists, so the TAG is what tells them apart — and a
// stratum's `match` takes a union, which is why reading a tagged list as one was not optional.
func TestATaggedListIsOneMatchNotAUnion(t *testing.T) {
	node, err := codec.ParseNode([]byte(`!verse-path [verse-dev, drift, finding, "*"]`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	var ms Matches
	if err := ms.FromTonyIR(node); err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("an address read as %d matches: each step became a match on its own, and a bare "+
			"name matches a whole system", len(ms))
	}
	// The union form still reads as a union.
	node, err = codec.ParseNode([]byte(`[demo:pr:*, {system: gh, kind: issue}]`), codec.Tony)
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

// A DEEP SUBJECT TOO: the rendering a rule is stored with has to name the same entities the rule
// named, and `!verse-path` with exactly three steps does not.
func TestADeepSubjectRendersAsAPath(t *testing.T) {
	m := mustPathMatch(t, `{path: [verse-dev, drift, head]}`)
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
	if got.str(mKeyID) != "" || got.dim(mKeyID) != nil {
		t.Errorf("the place came back constraining an id: %v", got)
	}
}
