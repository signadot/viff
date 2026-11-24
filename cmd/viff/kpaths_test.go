package main

import (
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/entity"
)

// `--kpaths` IS THE SAME LISTING AS ONE TOKEN PER LINE, and it exists for the one thing steps
// cannot do: survive a shell variable.
//
// A step is a word, so a line of them only reaches a read if the shell splits it — `xargs -L1`,
// or unquoted. A script that captures a line and passes it as ONE argument (`ref=$(… | head -1);
// viff data --at "$ref"`) needs a spelling that is one argument, and that is the kinded path.
func TestKPathsIsTheListingAsOneToken(t *testing.T) {
	store := serve(t)
	for _, segs := range [][]string{
		{"demo", "pr", "1"},
		{"demo", "pr", "a b"}, // the segment that makes steps need quoting
		{"demo", "pr", "a.b"}, // …and the one that makes a kpath need it
	} {
		if _, _, err := store.Write(entity.Write{
			At: entity.RefAt(segs...).At(), Value: entity.Obj{"stage": "open"}.Payload(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runCLI(t, "data", "--kpaths", "--depth", "1", "demo", "pr")
	if err != nil {
		t.Fatalf("data --kpaths: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("--kpaths listed %d addresses, want 3:\n%s", len(lines), out)
	}
	// EVERY LINE READS BACK AS ONE ARGUMENT, through --at, which is the whole point.
	for _, line := range lines {
		got, err := runCLI(t, "data", "--at", line)
		if err != nil || !strings.Contains(got, "stage: open") {
			t.Errorf("--at %q = (%q, %v), want the entity", line, strings.TrimSpace(got), err)
		}
	}
}

// The two spellings list the SAME addresses; only the rendering differs. `--kpaths` implies the
// listing, so it need not be passed beside `--paths`.
func TestPathsAndKPathsListTheSameThings(t *testing.T) {
	store := serve(t)
	for _, id := range []string{"1", "2"} {
		if _, _, err := store.Write(entity.Write{
			At: entity.RefAt("demo", "pr", id).At(), Value: entity.Obj{"stage": "open"}.Payload(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	steps, err := runCLI(t, "data", "--paths", "--depth", "1", "demo", "pr")
	if err != nil {
		t.Fatal(err)
	}
	kp, err := runCLI(t, "data", "--kpaths", "--depth", "1", "demo", "pr")
	if err != nil {
		t.Fatal(err)
	}
	if a, b := strings.Count(steps, "\n"), strings.Count(kp, "\n"); a != b {
		t.Errorf("--paths listed %d and --kpaths %d:\n%s\n%s", a, b, steps, kp)
	}
	if strings.TrimSpace(steps) != "demo pr 1\ndemo pr 2" {
		t.Errorf("--paths = %q", steps)
	}
	if strings.TrimSpace(kp) != `demo.pr."1"`+"\n"+`demo.pr."2"` {
		t.Errorf("--kpaths = %q", kp)
	}
}

// And the charter listing takes both, for the same reason and to the same spellings.
func TestCharterTakesBothSpellings(t *testing.T) {
	serve(t)
	if err := installPackageRuleAt(t, "notes"); err != nil {
		t.Fatalf("installing a package: %v", err)
	}
	for _, c := range []struct{ flag, want string }{
		{"--paths", "notes rules note"},
		{"--kpaths", "notes.rules.note"},
	} {
		out, err := runCLI(t, "charter", c.flag)
		if err != nil || strings.TrimSpace(out) != c.want {
			t.Errorf("charter %s = (%q, %v), want %q", c.flag, strings.TrimSpace(out), err, c.want)
		}
	}
}
