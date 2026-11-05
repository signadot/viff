package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/task"
)

// installAll installs the two strata these tests share, then every rule in src.
func installAll(t *testing.T, eng *Engine, src string) {
	t.Helper()
	withStrata(t, eng,
		Stratum{Name: "low", Level: 0, Match: []Match{mm("git")}},
		Stratum{Name: "high", Level: 1, Match: []Match{mm("notes")}})
	trigs, _, _, _, ok := parseBlock(src)
	if !ok || len(trigs) == 0 {
		t.Fatalf("did not parse as a charter:\n%s", src)
	}
	for _, tr := range trigs {
		if err := eng.AddTrigger(tr); err != nil {
			t.Fatalf("trigger %s: %v", tr.Name, err)
		}
	}
}

// A DERIVE OVER A FIELD ANOTHER RULE OWNS IS NAMED AT INSTALL.
//
// Installing a charter RE-FIRES every rule over everything it selects — that is what makes an
// install act on the world, and is correct. A derive is a patch merged at depth, so one naming a
// field another rule writes rewrites that field on every install, over whatever the flow had
// reached. Measured on 131 records: `reviewed: baseline` came back as 85 pending and 46 gathered
// after re-applying an unchanged rule (git-issue 2c6z0ahwh12kraxbgnn0).
func TestADeriveOverAnotherRulesFieldIsAdvised(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	installAll(t, eng, `
- name: record
  stratum: low
  condition: {subject: {path: "git.commit.*"}}
  action:
    derive:
    - path: "notes.row"
      value:
        reviewed: pending
- name: gather
  stratum: high
  condition: {subject: {path: "notes.row.*", value: {reviewed: pending}}}
  action:
    patch: {reviewed: gathered}
`)
	notes := eng.adviseDeriveClobber(mustParseCharter(t, `
- name: record
  stratum: low
  condition: {subject: {path: "git.commit.*"}}
  action:
    derive:
    - path: "notes.row"
      value:
        reviewed: pending
`))
	var found string
	for _, n := range notes {
		if strings.Contains(n, "reviewed") {
			found = n
		}
	}
	if found == "" {
		t.Fatalf("no note about the clobbered field:\n%s", strings.Join(notes, "\n"))
	}
	for _, want := range []string{"record", "gather", "RE-FIRES", "!not.has-path reviewed"} {
		if !strings.Contains(found, want) {
			t.Errorf("the note does not carry %q:\n%s", want, found)
		}
	}
}

// A derive that states only the facts it owns earns no note — which is the shape the note asks
// for. `gather` starts from an ABSENCE, so there is nothing for the derive to overwrite.
func TestADeriveThatOwnsItsFieldsIsNotAdvised(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	installAll(t, eng, `
- name: record
  stratum: low
  condition: {subject: {path: "git.commit.*"}}
  action:
    derive:
    - path: "notes.row"
      value:
        sha: .[subject.name]
- name: gather
  stratum: high
  condition: {subject: {path: "notes.row.*", value: !not.has-path reviewed}}
  action:
    patch: {reviewed: gathered}
`)
	for _, tr := range eng.Triggers() {
		if n := eng.adviseDeriveClobber(tr); len(n) > 0 {
			t.Errorf("%s earned: %s", tr.Name, strings.Join(n, "\n"))
		}
	}
}

// A patch on an address the derive does not cover is a different entity and is not a clobber.
func TestADeriveElsewhereIsNotAdvised(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	installAll(t, eng, `
- name: record
  stratum: low
  condition: {subject: {path: "git.commit.*"}}
  action:
    derive:
    - path: "notes.row"
      value:
        reviewed: pending
- name: elsewhere
  condition: {subject: {path: "demo.pr.*"}}
  action:
    patch: {reviewed: gathered}
`)
	for _, tr := range eng.Triggers() {
		if n := eng.adviseDeriveClobber(tr); len(n) > 0 {
			t.Errorf("%s earned a note about an unrelated address: %s", tr.Name, strings.Join(n, "\n"))
		}
	}
}
