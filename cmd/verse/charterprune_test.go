package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keepFile is a package with a rule, a level and a schedule a later directory leaves out.
const keepFile = `at: sync.keep
reads: [demo]
strata:
- name: src
  level: 0
  match: {path: "demo"}
- name: rows
  level: 10
  match: {path: "sync.keep.row"}
- name: spare
  level: 20
  match: {path: "sync.keep.spare"}
schedules:
- name: often
  every: 1h
rules:
- name: copy
  stratum: src
  condition:
    subject: {path: "demo.item.*"}
  action:
    derive:
    - path: "sync.keep.row"
      id: .[subject.value.id]
      value: {seen: true}
- name: extra
  stratum: src
  condition:
    subject: {path: "demo.other.*"}
  action:
    derive:
    - path: "sync.keep.row"
      id: .[subject.value.id]
      value: {other: true}
`

// keptFile is keepFile less the rule `extra`, the level `spare` and the schedule.
const keptFile = `at: sync.keep
reads: [demo]
strata:
- name: src
  level: 0
  match: {path: "demo"}
- name: rows
  level: 10
  match: {path: "sync.keep.row"}
rules:
- name: copy
  stratum: src
  condition:
    subject: {path: "demo.item.*"}
  action:
    derive:
    - path: "sync.keep.row"
      id: .[subject.value.id]
      value: {seen: true}
`

// onlyRule is a one-rule package at an address, for the packages a prune leaves alone or takes.
func onlyRule(at string) string {
	return `at: ` + at + `
reads: [demo]
strata:
- name: src
  level: 0
  match: {path: "demo"}
- name: rows
  level: 10
  match: {path: "` + at + `.row"}
rules:
- name: mark
  stratum: src
  condition:
    subject: {path: "demo.item.*"}
  action:
    derive:
    - path: "` + at + `.row"
      id: .[subject.value.id]
      value: {seen: true}
`
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A PRUNE IS `-R` THEN `-a -d`, AS ONE STEP: under the path, what stands afterwards is what the
// directory holds — a rule, level or schedule its file leaves out goes, and so does a package it
// has no file for — and what is outside the path is not touched.
func TestAPruneLeavesWhatTheDirectoryHolds(t *testing.T) {
	serve(t)
	first := writeTree(t, map[string]string{
		"keep.tony": keepFile,
		"gone.tony": onlyRule("sync.gone"),
	})
	if out, err := runCLI(t, "charter", "-a", "-d", first); err != nil {
		t.Fatalf("install: %v (%s)", err, out)
	}
	outside := writeTree(t, map[string]string{"x.tony": onlyRule("elsewhere")})
	if out, err := runCLI(t, "charter", "-a", "-d", outside); err != nil {
		t.Fatalf("install elsewhere: %v (%s)", err, out)
	}

	// Without --prune the directory only adds: `extra` survives a file that leaves it out.
	second := writeTree(t, map[string]string{"keep.tony": keptFile})
	if out, err := runCLI(t, "charter", "-a", "-d", second); err != nil {
		t.Fatalf("install without prune: %v (%s)", err, out)
	}
	if got, _ := runCLI(t, "charter", "sync", "keep"); !strings.Contains(got, "name: extra") {
		t.Fatalf("an install without --prune removed a rule:\n%s", got)
	}

	out, err := runCLI(t, "charter", "-a", "sync", "-d", second, "--prune")
	if err != nil {
		t.Fatalf("prune: %v (%s)", err, out)
	}
	for _, want := range []string{"rule extra", "level spare", "schedule often", "sync gone: uninstalled"} {
		if !strings.Contains(out, want) {
			t.Errorf("the prune's report lacks %q:\n%s", want, out)
		}
	}
	got, err := runCLI(t, "charter", "sync", "keep")
	if err != nil {
		t.Fatalf("charter sync keep: %v", err)
	}
	for _, not := range []string{"name: extra", "name: spare", "every: 1h"} {
		if strings.Contains(got, not) {
			t.Errorf("the pruned package still holds %q:\n%s", not, got)
		}
	}
	for _, want := range []string{"name: copy", "name: rows"} {
		if !strings.Contains(got, want) {
			t.Errorf("the pruned package lost %q, which its file holds:\n%s", want, got)
		}
	}
	if got, err := runCLI(t, "charter", "sync", "gone"); err == nil && strings.Contains(got, "name: mark") {
		t.Errorf("a package with no file under the path survived the prune:\n%s", got)
	}
	if got, err := runCLI(t, "charter", "elsewhere"); err != nil || !strings.Contains(got, "name: mark") {
		t.Errorf("a package outside the path was touched: %v\n%s", err, got)
	}
}

// A REFUSED PRUNE PUTS BACK WHAT IT TOOK OUT OF THE PACKAGES IT KEEPS, as well as the installs. The
// file keeps a rule that derives into a level it leaves out, so the level refuses to go — after
// the rule the file also left out has gone — and the request is refused: that rule still stands.
// The package with no file was uninstalled ahead of the install, which is not undone, and the
// refusal names it.
func TestARefusedPrunePutsBackWhatItRemoved(t *testing.T) {
	serve(t)
	first := writeTree(t, map[string]string{
		"keep.tony": keepFile,
		"gone.tony": onlyRule("sync.gone"),
	})
	if out, err := runCLI(t, "charter", "-a", "-d", first); err != nil {
		t.Fatalf("install: %v (%s)", err, out)
	}
	noRows := strings.Replace(keptFile, `- name: rows
  level: 10
  match: {path: "sync.keep.row"}
`, "", 1)
	bad := writeTree(t, map[string]string{"keep.tony": noRows})
	_, err := runCLI(t, "charter", "-a", "sync", "-d", bad, "--prune")
	if err == nil {
		t.Fatal("a prune taking out a level a kept rule derives into was accepted")
	}
	if !strings.Contains(err.Error(), "sync.gone") || !strings.Contains(err.Error(), "uninstalled") {
		t.Errorf("the refusal does not say the package the prune leaves out was uninstalled: %v", err)
	}
	got, err := runCLI(t, "charter", "sync", "keep")
	if err != nil {
		t.Fatalf("charter sync keep: %v", err)
	}
	for _, want := range []string{"name: extra", "name: spare", "name: rows", "every: 1h"} {
		if !strings.Contains(got, want) {
			t.Errorf("a refused prune left %q out:\n%s", want, got)
		}
	}
	if got, _ := runCLI(t, "charter", "sync", "gone"); strings.Contains(got, "name: mark") {
		t.Errorf("the package the prune leaves out stands after the prune uninstalled it:\n%s", got)
	}
}

// A PRUNE NAMES WHAT IT CLEARS, and clears only that: no path is refused, and so is a file whose
// charter is outside the path.
func TestAPruneNamesItsAddress(t *testing.T) {
	serve(t)
	dir := writeTree(t, map[string]string{"x.tony": onlyRule("elsewhere")})
	if _, err := runCLI(t, "charter", "-a", "-d", dir, "--prune"); err == nil {
		t.Error("--prune with no path was accepted")
	}
	_, err := runCLI(t, "charter", "-a", "sync", "-d", dir, "--prune")
	if err == nil || !strings.Contains(err.Error(), "not at or under") {
		t.Errorf("a file outside the pruned path = %v, want a refusal naming it", err)
	}
	if got, _ := runCLI(t, "charter", "elsewhere"); strings.Contains(got, "name: mark") {
		t.Errorf("the refused prune installed the file outside it:\n%s", got)
	}
	if _, err := runCLI(t, "charter", "-a", "-f", filepath.Join(dir, "x.tony"), "--prune"); err == nil {
		t.Error("--prune with -f was accepted: it syncs a directory")
	}
}
