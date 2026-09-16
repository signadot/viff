package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/task"
)

// TWO HALVES OF ONE FILE MEANT TWO THINGS. `InstallTrigger` replaces a rule of the same name,
// saying so in as many words — the file is what the operator means and re-applying one is the
// ordinary way to change a rule. `InstallStratum` refused a level that differed and told the
// operator to uninstall the whole package, which takes the rules down with it. So editing a level
// in a charter file and re-applying it failed, while editing a rule beside it in the same file
// succeeded.
//
// They agree now: a re-applied file replaces a stratum the way it replaces a rule.
func TestReApplyingAFileReplacesALevel(t *testing.T) {
	eng, store := newEng(t)
	if err := eng.InstallStratum(Stratum{Name: "drift", Level: 10, Match: []Match{mm("gh")}}); err != nil {
		t.Fatalf("installing a level: %v", err)
	}
	// The same level, declared again: the file has been applied twice, and nothing happens.
	if err := eng.InstallStratum(Stratum{Name: "drift", Level: 10, Match: []Match{mm("gh")}}); err != nil {
		t.Fatalf("re-applying an unchanged level: %v", err)
	}
	// And now edited — a level moved and a union narrowed, which is what an operator iterating on
	// a charter does.
	if err := eng.InstallStratum(Stratum{Name: "drift", Level: 20, Match: []Match{mm("gh"), mm("git")}}); err != nil {
		t.Fatalf("re-applying an edited level: %v", err)
	}
	got := eng.Strata()
	if len(got) != 1 || got[0].Level != 20 || len(got[0].Match) != 2 {
		t.Fatalf("the engine is not running the edited level: %+v", got)
	}

	// THE STORE HAS THE EDITED ONE TOO, whole. A write MERGES at its path, so a level written over
	// the one it replaces would keep whatever the edit dropped — and the charter a restart brings
	// back would not be the charter that is running. Read back the way a restart does.
	back := New(store, task.AutoResolver{})
	if err := back.LoadStrata(); err != nil {
		t.Fatalf("loading the strata back: %v", err)
	}
	if s := back.Strata(); len(s) != 1 || s[0].Level != 20 || len(s[0].Match) != 2 {
		t.Fatalf("the durable level is not the one that is running: %+v", s)
	}

	// Narrowing the union again: the element that went is gone from the record, rather than
	// merged back in under it.
	if err := eng.InstallStratum(Stratum{Name: "drift", Level: 20, Match: []Match{mm("gh")}}); err != nil {
		t.Fatalf("narrowing the union: %v", err)
	}
	back = New(store, task.AutoResolver{})
	if err := back.LoadStrata(); err != nil {
		t.Fatalf("loading the strata back: %v", err)
	}
	if s := back.Strata(); len(s) != 1 || len(s[0].Match) != 1 {
		t.Fatalf("the narrowed level kept what it dropped: %+v", s)
	}
}

// WHAT THE OLD REFUSAL PROTECTED IS STILL PROTECTED, and by the check rather than by the refusal.
// The rules in a stratum were checked against it when they installed, so moving it under them
// would leave a charter whose ordering nobody has verified — so every rule faces the level
// discipline again, and one that no longer fits refuses the CHANGE, naming itself. It is
// RecheckReads one axis over.
func TestAnEditedLevelARuleNoLongerFitsIsRefused(t *testing.T) {
	eng, store := newEng(t)
	withStrata(t, eng, Stratum{Name: "observe", Level: 10, Match: []Match{mm("gh")}})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: seen
  stratum: observe
  condition:
    subject: {path: "gh.pr.*"}
  action:
    patch: {saw: true}
`)); err != nil {
		t.Fatalf("installing a rule: %v", err)
	}

	// The level moves off the place its rule selects in. The rule is installed, so the EDIT is
	// what is refused.
	err := eng.InstallStratum(Stratum{Name: "observe", Level: 10, Match: []Match{mm("signadot")}})
	if err == nil {
		t.Fatal("changed a level out from under the rule standing in it")
	}
	if !strings.Contains(err.Error(), "seen") {
		t.Errorf("the refusal does not name the rule the edit would break: %v", err)
	}

	// AND THE LEVEL THAT STOOD IS STILL STANDING, in the engine and in the store: a refused change
	// is not a half-made one.
	if s := eng.Strata(); len(s) != 1 || !isPath(s[0].Match[0], "gh") {
		t.Fatalf("the refused edit was made anyway: %+v", s)
	}
	back := New(store, task.AutoResolver{})
	if err := back.LoadStrata(); err != nil {
		t.Fatalf("loading the strata back: %v", err)
	}
	if s := back.Strata(); len(s) != 1 || !isPath(s[0].Match[0], "gh") {
		t.Fatalf("the durable level is not the one that is running: %+v", s)
	}
}

// The set-properties still hold across an edit — an edited level may not land on a level another
// holds, or overlap one — and the level being edited is not its own neighbour, which is the case a
// naive check reports as a duplicate of itself.
func TestAnEditedLevelStillFacesTheOrdering(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng,
		Stratum{Name: "low", Level: 10, Match: []Match{mm("gh")}},
		Stratum{Name: "high", Level: 20, Match: []Match{mm("signadot")}},
	)
	// Onto a level `high` holds.
	if err := eng.InstallStratum(Stratum{Name: "low", Level: 20, Match: []Match{mm("gh")}}); err == nil {
		t.Fatal("two strata at level 20")
	}
	// Onto what `high` owns.
	if err := eng.InstallStratum(Stratum{Name: "low", Level: 10, Match: []Match{mm("signadot")}}); err == nil {
		t.Fatal("two strata own signadot")
	}
	// And an ordinary edit — same name, same level, a wider union — is not a collision with
	// itself.
	if err := eng.InstallStratum(Stratum{Name: "low", Level: 10, Match: []Match{mm("gh"), mm("git")}}); err != nil {
		t.Fatalf("an edited level collided with the one it replaces: %v", err)
	}
}

// A HELD RULE OCCUPIES ITS NAME. `InstallTrigger` asked `Trigger`, which answers only what is
// running, so a rule waiting for its stratum was left standing and the install behind it met
// nameTaken's "is already held" — re-applying an edited file refused, which is the one thing
// InstallTrigger exists to allow.
func TestReApplyingAFileReplacesAHeldRule(t *testing.T) {
	eng, _ := newEng(t)
	rule := func(patch string) Trigger {
		return mustParseCharter(t, `
- name: waiting
  stratum: later
  condition:
    subject: {path: "gh.pr.*"}
  action:
    patch: {saw: "`+patch+`"}
`)
	}
	// No stratum `later`, so this is HELD rather than refused.
	if err := eng.InstallTrigger(rule("first")); err != nil {
		t.Fatalf("installing a rule whose stratum has not arrived: %v", err)
	}
	if _, ok := eng.Trigger("waiting"); ok {
		t.Fatal("a rule naming an absent stratum is running")
	}
	if err := eng.InstallTrigger(rule("second")); err != nil {
		t.Fatalf("re-applying a file that edits a held rule: %v", err)
	}
	// The stratum arrives, and what installs is the SECOND version — one rule, the edited one.
	if err := eng.AddStratum(Stratum{Name: "later", Level: 10, Match: []Match{mm("gh")}}); err != nil {
		t.Fatalf("installing the stratum: %v", err)
	}
	got := eng.Triggers()
	if len(got) != 1 {
		t.Fatalf("want one rule, got %d: %+v", len(got), got)
	}
	if saw := entity.Str(got[0].Action.Patch, "saw"); saw != "second" {
		t.Fatalf("the held rule was not replaced by the edited one: saw = %q", saw)
	}
}
