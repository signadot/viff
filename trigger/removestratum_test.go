package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// A LEVEL A RULE DERIVES INTO IS IN USE, even when no rule LIVES in it.
//
// `RemoveStratum` refuses a level that still holds rules, for a reason it states itself: they
// would be left "installed and unchecked". It counted only the rules whose `stratum:` names it,
// and a derive TARGET is the other way a level is depended on — `ledger` owns
// `verse-dev.drift.finding`, nothing declares `stratum: ledger`, and removing it left
// `comment-drift` deriving into a level that no longer existed.
//
// That is exactly the state install refuses ("derive into X matches no stratum: every derived
// entity has to belong to a level"), reached through the back door. Driven by hand on a local
// verse: `verse c -r strata ledger` answered "removed ledger".
func TestAStratumADeriveTargetsIsInUse(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	eng.Confine(SubtreeAt("verse-dev", "drift"))
	eng.SetReads([]Subtree{SubtreeAt("git")})

	for _, st := range []Stratum{
		{Name: "reflected", Level: 0, Match: []Match{{"system": "git"}}},
		{Name: "tip", Level: 10, Match: []Match{mustPathMatch(t, `{path: [verse-dev, drift, head]}`)}},
		{Name: "ledger", Level: 20, Match: []Match{mustPathMatch(t, `{path: [verse-dev, drift, finding]}`)}},
	} {
		if err := eng.AddStratum(st); err != nil {
			t.Fatalf("stratum %q: %v", st.Name, err)
		}
	}
	// Lives in `tip`, derives into `ledger`. Nothing anywhere says `stratum: ledger`.
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: gather
  stratum: tip
  condition:
    subject: !verse-path [verse-dev, drift, head, "*"]
  action:
    derive:
    - path: [verse-dev, drift, finding]
      value: {noted: true}
`)); err != nil {
		t.Fatalf("install: %v", err)
	}

	err := eng.RemoveStratum("ledger")
	if err == nil {
		t.Fatal("removed a level a rule derives into: the rule is now installed and unchecked")
	}
	if !strings.Contains(err.Error(), "gather") {
		t.Errorf("the refusal does not name the rule that depends on it: %v", err)
	}

	// And the level nothing depends on still goes, so this did not turn into "no stratum may
	// ever be removed".
	if err := eng.AddStratum(Stratum{Name: "spare", Level: 30,
		Match: []Match{mustPathMatch(t, `{path: [verse-dev, drift, spare]}`)}}); err != nil {
		t.Fatal(err)
	}
	if err := eng.RemoveStratum("spare"); err != nil {
		t.Errorf("a level nothing uses could not be removed: %v", err)
	}
}
