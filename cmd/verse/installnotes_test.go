package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What an install has to say is printed to whoever ran it, under the line that says what was
// installed: a rule held for a stratum that is not there, and a rule that is legal and asks its
// question again.
func TestCharterAddPrintsWhatTheInstallHasToSay(t *testing.T) {
	serve(t)
	f := filepath.Join(t.TempDir(), "charter.tony")
	if err := os.WriteFile(f, []byte(`- name: waits
  stratum: sdcl
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    patch: {stage: reviewed}
- name: asks-again
  condition:
    subject: {path: "demo.task.*", value: {stage: open}}
  gates:
  - {name: approval, mode: ask}
  action:
    patch: {stage: reviewed}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "charter", "-a", "-f", f)
	if err != nil {
		t.Fatalf("charter -a: %v\n%s", err, out)
	}
	for _, want := range []string{
		"baseline: installed 1 trigger(s)",
		"held: rule waits waits for stratum sdcl, which is not installed",
		"note: rule asks-again: gate \"approval\" blocks",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the install did not say %q:\n%s", want, out)
		}
	}
	// The listing shows the held rule, marked.
	if out, err = runCLI(t, "charter"); err != nil || !strings.Contains(out, "held: sdcl") {
		t.Errorf("the charter does not list the held rule as held (%v):\n%s", err, out)
	}
}
