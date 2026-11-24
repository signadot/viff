package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `viff c -a` IS ATOMIC ACROSS THE WHOLE FILE, end to end: a file whose levels install and
// whose rule is refused leaves nothing standing, and says so. Levels left behind would be a
// package nobody wrote down (viffd's TestAMultiPackageFileIsAppliedAsOneStep is the same
// promise across several charters in one request).
//
// The file below is the shape the defect was found in: a declaration, a level, and a rule that
// writes outside the address they are all installed at.
func TestCharterAddInstallsAFileOrNoneOfIt(t *testing.T) {
	serve(t)
	f := filepath.Join(t.TempDir(), "charter.tony")
	if err := os.WriteFile(f, []byte(`
at: probe
reads: [git]
strata:
- name: lvl
  level: 0
  match: {path: "git"}
rules:
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
strata:
- name: lvl
  level: 0
  match: {path: "git"}
rules:
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

// A COMMENT IS PART OF THE RULE, and it survives the trip a charter file makes: parsed here,
// sent in one request, split there. The first rule's head comment is the one at risk — the
// parser hangs the lines above a block list's first entry on the list — and a comment beside a
// flow map is the other.
func TestCharterAddKeepsCommentsInOneRequest(t *testing.T) {
	serve(t)
	f := filepath.Join(t.TempDir(), "charter.tony")
	if err := os.WriteFile(f, []byte(`
rules:
# why this rule is here
- name: noted
  condition:
    subject: {path: "demo.pr.*"}
  action:
    patch:
      saw: true # the flag it sets
# and why this one
- name: other
  condition:
    subject: {path: "demo.issue.*"}
  action:
    patch: {saw: true} # beside a flow map
`), 0o600); err != nil {
		t.Fatalf("write charter: %v", err)
	}
	if out, err := runCLI(t, "charter", "-a", "-f", f); err != nil {
		t.Fatalf("install: %v (%s)", err, out)
	}
	for rule, wants := range map[string][]string{
		"noted": {"# why this rule is here", "# the flag it sets"},
		"other": {"# and why this one", "# beside a flow map"},
	} {
		out, err := runCLI(t, "charter", "rules", rule)
		if err != nil {
			t.Fatalf("charter path: %v", err)
		}
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("rule %s came back without %q:\n%s", rule, want, out)
			}
		}
	}
}

// A RE-APPLIED FILE IS THE ORDINARY WAY TO CHANGE A CHARTER, both halves of it: a rule is
// replaced, and so is a level — without uninstalling the package, which would take the rules
// beside it down too.
func TestCharterAddReAppliesAnEditedFile(t *testing.T) {
	serve(t)
	f := filepath.Join(t.TempDir(), "charter.tony")
	write := func(level, saw string) {
		t.Helper()
		if err := os.WriteFile(f, []byte(`
strata:
- name: lvl
  level: `+level+`
  match: {path: "demo"}
rules:
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
