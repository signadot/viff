package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packageFile is a charter with every part a file can hold, comments included.
const packageFile = `at: roundtrip
reads: [demo]
schedules:
- name: often
  every: 1h
strata:
- name: src
  level: 0
  match: {path: "demo"}
- name: rows
  level: 10
  match: {path: "roundtrip.row"}
rules:
# why the copy exists
- name: copy
  stratum: src
  condition:
    subject: {path: "demo.item.*"}
  action:
    derive:
    - path: "roundtrip.row"
      id: .[subject.value.id]
      value: {seen: true} # the flag it sets
`

// WHAT `viff c` PRINTS IS WHAT `viff c -a -f` INSTALLS: the charter at an address comes out as
// its file, `at:` and comments and all, and that file puts back the same charter.
func TestACharterPrintsAsTheFileThatInstallsIt(t *testing.T) {
	serve(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "in.tony")
	if err := os.WriteFile(f, []byte(packageFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "charter", "-a", "-f", f); err != nil {
		t.Fatalf("install: %v (%s)", err, out)
	}
	first, err := runCLI(t, "charter", "roundtrip")
	if err != nil {
		t.Fatalf("charter roundtrip: %v", err)
	}
	for _, want := range []string{"at: roundtrip", "reads:", "- demo", "# why the copy exists",
		"# the flag it sets", "name: rows", "name: often"} {
		if !strings.Contains(first, want) {
			t.Errorf("the printed charter lacks %q:\n%s", want, first)
		}
	}
	for _, not := range []string{"writes:", "children:"} {
		if strings.Contains(first, not) {
			t.Errorf("the printed charter carries %q, which no install takes:\n%s", not, first)
		}
	}

	// Taken down and put back from what was printed, it prints the same.
	if out, err := runCLI(t, "charter", "-r", "roundtrip"); err != nil {
		t.Fatalf("remove: %v (%s)", err, out)
	}
	printed := filepath.Join(dir, "printed.tony")
	if err := os.WriteFile(printed, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "charter", "-a", "-f", printed); err != nil {
		t.Fatalf("installing what was printed: %v (%s)", err, out)
	}
	again, err := runCLI(t, "charter", "roundtrip")
	if err != nil {
		t.Fatalf("charter roundtrip: %v", err)
	}
	if again != first {
		t.Errorf("the charter did not survive the round trip:\n--- printed\n%s\n--- after\n%s", first, again)
	}
}

// A TREE IS A DIRECTORY: `-d` writes each charter at or under the path to a file of its own,
// and `-a -d` installs a directory's charters, each at its own `at:`, whatever the layout.
func TestADirectoryOfChartersRoundTrips(t *testing.T) {
	serve(t)
	src := t.TempDir()
	for name, body := range map[string]string{
		"one.tony":        packageFile,
		"nested/two.tony": strings.ReplaceAll(packageFile, "roundtrip", "two.deep"),
		"README.md":       "not a charter, and not read as one",
	} {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := runCLI(t, "charter", "-a", "-d", src); err != nil {
		t.Fatalf("install a directory: %v (%s)", err, out)
	}
	before := map[string]string{}
	for _, at := range []string{"roundtrip", "two.deep"} {
		out, err := runCLI(t, append([]string{"charter"}, strings.Split(at, ".")...)...)
		if err != nil {
			t.Fatalf("charter %s: %v", at, err)
		}
		before[at] = out
	}

	// Written out from the root: a file per charter at its address, and baseline — empty
	// here — not at all.
	out := t.TempDir()
	if o, err := runCLI(t, "charter", "-d", out); err != nil {
		t.Fatalf("charter -d: %v (%s)", err, o)
	}
	for _, p := range []string{"roundtrip/charter.tony", "two/deep/charter.tony"} {
		if _, err := os.Stat(filepath.Join(out, p)); err != nil {
			t.Errorf("-d did not write %s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "charter.tony")); err == nil {
		t.Error("-d wrote an empty baseline")
	}

	// Taken down and put back from what was written, each prints the same.
	for _, at := range []string{"roundtrip", "two"} {
		if o, err := runCLI(t, "charter", "-R", at); err != nil {
			t.Fatalf("remove %s: %v (%s)", at, err, o)
		}
	}
	if o, err := runCLI(t, "charter", "-a", "-d", out); err != nil {
		t.Fatalf("installing what -d wrote: %v (%s)", err, o)
	}
	for at, want := range before {
		got, err := runCLI(t, append([]string{"charter"}, strings.Split(at, ".")...)...)
		if err != nil {
			t.Fatalf("charter %s: %v", at, err)
		}
		if got != want {
			t.Errorf("%s did not survive the directory:\n--- before\n%s\n--- after\n%s", at, want, got)
		}
	}
}

// ONE FILE, ONE CHARTER, and a directory's files each say where they go.
func TestACharterFileIsOneCharter(t *testing.T) {
	serve(t)
	dir := t.TempDir()
	two := filepath.Join(dir, "two.tony")
	if err := os.WriteFile(two, []byte("{at: a}\n---\n{at: b}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "charter", "-a", "-f", two); err == nil || !strings.Contains(err.Error(), "-d") {
		t.Errorf("a file of two charters = %v, want a refusal pointing at -d", err)
	}

	same := t.TempDir()
	for _, name := range []string{"x.tony", "y.tony"} {
		if err := os.WriteFile(filepath.Join(same, name), []byte("{at: a}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := runCLI(t, "charter", "-a", "-d", same)
	if err == nil || !strings.Contains(err.Error(), "x.tony") || !strings.Contains(err.Error(), "y.tony") {
		t.Errorf("two files at one address = %v, want a refusal naming both", err)
	}
	if _, err := runCLI(t, "charter", "-a", "a", "-d", same); err == nil {
		t.Error("a path beside -d was accepted: each file says where it goes")
	}

	bare := t.TempDir()
	if err := os.WriteFile(filepath.Join(bare, "rules.tony"),
		[]byte(`[{name: r, condition: {subject: {path: "demo.x.*"}}, action: {patch: {seen: true}}}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "charter", "-a", "-d", bare); err == nil {
		t.Error("a bare list in a directory was installed: there is no path for it to go at")
	}
}
