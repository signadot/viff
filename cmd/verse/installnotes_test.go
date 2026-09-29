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
	// The listing says the rule is held — on stderr, since the file on stdout is what installs,
	// and a mark in it would be refused, or kept as the rule's comment.
	out, errOut, err := runCLIStreams(t, "charter")
	if err != nil || !strings.Contains(errOut, "rule waits is held: it waits for stratum sdcl") {
		t.Errorf("the charter does not say the rule is held (%v):\n%s", err, errOut)
	}
	if !strings.Contains(out, "name: waits") || strings.Contains(out, "held") {
		t.Errorf("the file lists the held rule with its mark, or not at all:\n%s", out)
	}
}
