package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `verse c -a` IS ATOMIC ACROSS THE WHOLE FILE, end to end.
//
// It used to POST each document of the file on its own, so a file whose levels installed and whose
// rule was refused left the levels standing — and told the operator the command failed. That is a
// package nobody wrote down, and the one state `verse c -r` was needed to get out of. Then it
// posted one PACKAGE per request, which left the same hole for a file spanning two addresses
// (versed's TestAMultiPackageFileIsAppliedAsOneStep is that half).
//
// The file below is the shape the defect was found in: a declaration, a level, and a rule that
// writes outside the address they are all installed at.
func TestCharterAddInstallsAFileOrNoneOfIt(t *testing.T) {
	serve(t)
	f := filepath.Join(t.TempDir(), "charter.tony")
	if err := os.WriteFile(f, []byte(`
at: probe
reads: [git]
---
- name: lvl
  level: 0
  match: {path: "git"}
---
- name: good
  condition:
    subject: {path: "elsewhere.thing.x"}
  action:
    patch: {saw: "yes"}
`), 0o600); err != nil {
		t.Fatalf("write charter: %v", err)
	}
	out, err := runCLI(t, "charter", "-a", "-f", f)
	if err == nil {
		t.Fatalf("a rule writing outside its package installed: %s", out)
	}
	if !strings.Contains(err.Error(), "nothing was installed") {
		t.Errorf("the refusal does not say the file was not applied: %v", err)
	}
	// NOTHING AT THE ADDRESS: no level, no declaration, no package to list. The listing 404s,
	// which the CLI reports as a charter installed nowhere.
	if out, err := runCLI(t, "charter"); err != nil {
		t.Fatalf("charter: %v", err)
	} else if strings.Contains(out, "probe") || strings.Contains(out, "lvl") {
		t.Errorf("the refused file left a package standing: %s", out)
	}

	// And the corrected file installs whole, which is what an operator does next — where before
	// they met the levels their own failed attempt had left behind.
	if err := os.WriteFile(f, []byte(`
at: probe
reads: [git]
---
- name: lvl
  level: 0
  match: {path: "git"}
---
- name: good
  condition:
    when: {path: "git.head.*"}
    subject: {path: "probe.seen.head"}
  action:
    patch: {saw: "yes"}
`), 0o600); err != nil {
		t.Fatalf("write charter: %v", err)
	}
	if out, err := runCLI(t, "charter", "-a", "-f", f); err != nil {
		t.Fatalf("the corrected file: %v (%s)", err, out)
	}
	out, err = runCLI(t, "charter", "probe")
	if err != nil {
		t.Fatalf("charter probe: %v", err)
	}
	if !strings.Contains(out, "lvl") || !strings.Contains(out, "good") {
		t.Errorf("the corrected file did not install whole: %s", out)
	}
}

// A COMMENT IS PART OF THE RULE, and a file's worth of definitions now travels in ONE request
// rather than one at a time — so the author's reasons have to survive the new trip. They are sent
// `---` separated rather than as an array for exactly this: a definition stays a document, which is
// what the file wrote, where an element of a list is re-rendered one indent in and a comment beside
// a flow map is lost on the way back through the parser.
func TestCharterAddKeepsCommentsInOneRequest(t *testing.T) {
	serve(t)
	f := filepath.Join(t.TempDir(), "charter.tony")
	if err := os.WriteFile(f, []byte(`
# why this rule is here
name: noted
condition:
  subject: {path: "demo.pr.*"}
action:
  patch:
    saw: true # the flag it sets
---
name: other
condition:
  subject: {path: "demo.issue.*"}
action:
  patch:
    saw: true
`), 0o600); err != nil {
		t.Fatalf("write charter: %v", err)
	}
	if out, err := runCLI(t, "charter", "-a", "-f", f); err != nil {
		t.Fatalf("install: %v (%s)", err, out)
	}
	out, err := runCLI(t, "charter", "rules", "noted")
	if err != nil {
		t.Fatalf("charter path: %v", err)
	}
	for _, want := range []string{"# why this rule is here", "# the flag it sets"} {
		if !strings.Contains(out, want) {
			t.Errorf("the installed rule came back without %q:\n%s", want, out)
		}
	}
}

// A RE-APPLIED FILE IS THE ORDINARY WAY TO CHANGE A CHARTER, both halves of it: a rule is
// replaced, and so is a level. Editing a level and re-applying used to be refused, with the
// remedy being to uninstall the package — which takes the rules beside it down too.
func TestCharterAddReAppliesAnEditedFile(t *testing.T) {
	serve(t)
	f := filepath.Join(t.TempDir(), "charter.tony")
	write := func(level, saw string) {
		t.Helper()
		if err := os.WriteFile(f, []byte(`
- name: lvl
  level: `+level+`
  match: {path: "demo"}
---
- name: note
  stratum: lvl
  condition:
    subject: {path: "demo.pr.*"}
  action:
    patch: {saw: "`+saw+`"}
`), 0o600); err != nil {
			t.Fatalf("write charter: %v", err)
		}
	}
	write("0", "first")
	if out, err := runCLI(t, "charter", "-a", "-f", f); err != nil {
		t.Fatalf("install: %v (%s)", err, out)
	}
	write("50", "second")
	if out, err := runCLI(t, "charter", "-a", "-f", f); err != nil {
		t.Fatalf("re-applying an edited file: %v (%s)", err, out)
	}
	out, err := runCLI(t, "charter")
	if err != nil {
		t.Fatalf("charter: %v", err)
	}
	if !strings.Contains(out, "level: 50") {
		t.Errorf("the edited level did not replace the one that stood: %s", out)
	}
	if !strings.Contains(out, "second") || strings.Contains(out, "first") {
		t.Errorf("the edited rule did not replace the one that stood: %s", out)
	}
}
