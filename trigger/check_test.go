package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// A PACKAGE IS CHECKED AS IT WOULD INSTALL, AND NOTHING IS WRITTEN. Its strata are the file's,
// not what stood; a rule outside its stratum, or deriving sideways, is refused with an install's
// words; a rule naming a stratum the file does not declare is held; and the store has nothing
// of it afterwards.
func TestCheckIsAnInstallThatWritesNothing(t *testing.T) {
	store := entitytest.Store(t)
	confine, err := ParseAt("repos.r")
	if err != nil {
		t.Fatal(err)
	}
	src := `
strata:
- {name: low, level: 0, match: {path: repos.r.low}}
- {name: high, level: 1, match: {path: repos.r.high}}
rules:
- name: lift
  stratum: low
  condition: {subject: {path: "repos.r.low.*"}}
  action:
    derive:
    - {path: repos.r.high, id: seen, value: {seen: true}}
- name: waits
  stratum: later
  condition: {subject: {path: "repos.r.low.*"}}
  action: {patch: {waited: true}}
`
	trigs, _, strata, _, ok := parseBlock(src)
	if !ok {
		t.Fatal("did not parse")
	}
	held, err := Check(store, confine, nil, "user:ana@example.com", strata, trigs, nil)
	if err != nil {
		t.Fatalf("a package that installs was refused: %v", err)
	}
	if len(held) != 1 || held[0] != "waits" {
		t.Errorf("held = %v, want the rule whose stratum the file does not declare", held)
	}
	if ents := entity.Below(store, func(entity.Ref) bool { return true }, 12); len(ents) != 0 {
		t.Fatalf("the check wrote %d entities, the first %s", len(ents), ents[0].Ref)
	}

	// Sideways: a derive into its own level is refused, as an install refuses it.
	bad := strings.Replace(src, "path: repos.r.high, id: seen", "path: repos.r.low, id: seen", 1)
	trigs, _, strata, _, _ = parseBlock(bad)
	if _, err := Check(store, confine, nil, "user:ana@example.com", strata, trigs, nil); err == nil || !strings.Contains(err.Error(), "lift") {
		t.Fatalf("a sideways derive: %v, want a refusal naming the rule", err)
	}
	// Outside the address: confined to repos.r, a rule selecting elsewhere is refused.
	far := strings.Replace(src, `subject: {path: "repos.r.low.*"}}
  action:
    derive`, `subject: {path: "elsewhere.*"}}
  action:
    derive`, 1)
	trigs, _, strata, _, _ = parseBlock(far)
	if _, err := Check(store, confine, nil, "user:ana@example.com", strata, trigs, nil); err == nil {
		t.Fatal("a rule outside its stratum's pattern was admitted")
	}
	// The same rule twice in one package.
	trigs, _, strata, _, _ = parseBlock(src)
	if _, err := Check(store, confine, nil, "user:ana@example.com", strata, append(trigs, trigs[0]), nil); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("a rule named twice: %v", err)
	}
}
