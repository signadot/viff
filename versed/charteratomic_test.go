package versed

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/trigger"
)

// A CHARTER FILE IS APPLIED AS ONE STEP.
//
// It used to go document by document with no unwind, so a file whose first document installed and
// whose second was refused left the first standing:
//
//	verse c -a -f <file>
//	charter add: 400 Bad Request: trigger "good": … is outside probe:pkg:*
//	verse charter probe pkg          # what stands afterwards
//	name: lvl                        # the stratum from the file that just failed
//
// A refusal anywhere left the package in a state nobody wrote down — some levels, none of the
// rules, and an operator who had been told the command failed. It is also how a package ended up
// with a level an operator could not remove by re-applying a corrected file: the levels were
// already there.
func TestACharterFileIsAppliedAsOneStep(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// A level that is fine, and a rule that writes outside the address it is installed at.
	code, msg := installAt(t, ts.URL, "probe.pkg", `[
		{name: lvl, level: 0, match: {system: git}},
		{
			name: good,
			condition: {subject: {system: elsewhere, kind: thing, id: x}},
			action: {patch: {saw: "yes"}},
		},
	]`)
	if code == 201 {
		t.Fatal("a rule writing outside its package installed")
	}
	if !strings.Contains(msg, "outside") {
		t.Errorf("the refusal is not the one the rule earned: %s", msg)
	}
	if !strings.Contains(msg, "nothing was installed") {
		t.Errorf("the refusal does not say the file was not applied: %s", msg)
	}

	// NOTHING STANDS. Not the level the first document would have installed, not a package for it
	// to belong to, and not an engine watching on behalf of a charter that is not there.
	if _, ok, _ := srv.Store.Get(trigger.StrataRefAt("probe.pkg", "lvl")); ok {
		t.Error("the level from the refused file is standing")
	}
	if got := srv.packagesSeen(); len(got) != 0 {
		t.Errorf("a refused file left a package behind: %v", got)
	}
	srv.scopeMu.Lock()
	_, running := srv.scopeEngs["at:probe.pkg"]
	srv.scopeMu.Unlock()
	if running {
		t.Error("an engine is running at an address nothing is installed at")
	}
}

// AND WHAT STOOD BEFORE IT IS PUT BACK. The unwind is not "remove what this request installed" but
// "restore what it replaced": a re-applied file REPLACES a rule and a level, so a file refused
// halfway must not leave the earlier half edited either.
func TestARefusedFilePutsBackWhatItReplaced(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := installAt(t, ts.URL, "notes", `[
		{name: lvl, level: 0, match: {system: notes}},
		{
			name: note,
			stratum: lvl,
			condition: {subject: {system: notes, kind: seen, id: head}},
			action: {patch: {saw: "first"}},
		},
	]`); code != 201 {
		t.Fatalf("installing a charter = %d: %s", code, msg)
	}

	// The same file, edited in both halves, with a third document that is refused.
	code, msg := installAt(t, ts.URL, "notes", `[
		{name: lvl, level: 50, match: {system: notes}},
		{
			name: note,
			stratum: lvl,
			condition: {subject: {system: notes, kind: seen, id: head}},
			action: {patch: {saw: "second"}},
		},
		{
			name: reaching,
			condition: {subject: {system: elsewhere, kind: thing, id: x}},
			action: {patch: {saw: "yes"}},
		},
	]`)
	if code == 201 {
		t.Fatal("a rule writing outside its package installed")
	}

	eng, err := srv.packageEngine("notes")
	if err != nil {
		t.Fatalf("the package's engine: %v", err)
	}
	if st, ok := eng.Stratum("lvl"); !ok || st.Level != 0 {
		t.Errorf("the edited level stands after the file was refused: %+v (%s)", st, msg)
	}
	rule, ok := eng.Rule("note")
	if !ok {
		t.Fatal("the rule that stood before the refused file is gone")
	}
	if saw := entity.Str(rule.Action.Patch, "saw"); saw != "first" {
		t.Errorf("the edited rule stands after the file was refused: saw = %q", saw)
	}
	if _, ok := eng.Rule("reaching"); ok {
		t.Error("the refused rule installed")
	}

	// The store agrees, which is what a restart would read.
	ent, ok, _ := srv.Store.Get(trigger.CharterRefAt("notes", "note"))
	if !ok {
		t.Fatal("the rule's record is gone")
	}
	rec, err := codec.Marshal(ent.Payload, codec.Tony)
	if err != nil {
		t.Fatalf("reading the rule's record: %v", err)
	}
	if !strings.Contains(string(rec), "first") {
		t.Errorf("the durable rule is not the one that is running: %s", rec)
	}
}

// A PACKAGE THAT ALREADY STOOD IS NOT SWEPT UP by a refused install. The engine is dropped only
// when the request is what brought the address into being; an existing package keeps its engine,
// its levels and its rules.
func TestARefusedFileLeavesAnExistingPackageRunning(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := declareReads(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring imports = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {system: git, kind: head}, subject: {system: notes, kind: seen, id: head}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing a package = %d: %s", code, msg)
	}

	if code, _ := installAt(t, ts.URL, "notes", `[
		{
			name: reaching,
			condition: {subject: {system: elsewhere, kind: thing, id: x}},
			action: {patch: {saw: "yes"}},
		},
	]`); code == 201 {
		t.Fatal("a rule writing outside its package installed")
	}

	if got := srv.packagesSeen(); len(got) != 1 || got[0] != "notes" {
		t.Fatalf("packagesSeen = %v, want the package that stood before the refusal", got)
	}
	eng, err := srv.packageEngine("notes")
	if err != nil {
		t.Fatalf("the package's engine: %v", err)
	}
	if _, ok := eng.Rule("note-the-head"); !ok {
		t.Error("a refused install took the package's rule down with it")
	}
	if len(eng.Reads()) != 1 {
		t.Errorf("a refused install dropped what the package reads: %v", eng.Reads())
	}
}
