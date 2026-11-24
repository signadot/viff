package io

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A commit's TRAILERS reach the viff instance; its body does not.
//
// The trailer block is the one part of a commit message that is structured enough to condition
// on — `Tier:`, `Breaks:`, `Co-Authored-By:`, and the provenance a coding runner writes about
// which task produced a branch. Without it the only thing a charter can match about a push is
// the subject line and the ref name.
func TestReflectCarriesCommitTrailers(t *testing.T) {
	dir := t.TempDir()
	tgit(t, dir, "init", "-b", "main")
	tgit(t, dir, "config", "user.email", "t@example.com")
	tgit(t, dir, "config", "user.name", "Tester")
	tgit(t, dir, "config", "commit.gpgsign", "false")

	// A message with a body AND a trailer block — the shape this exists for. The body is
	// deliberately multi-line, which is what breaks a line-per-commit scan.
	msg := "fix the thing\n\nA body that runs\nover several lines.\n\n" +
		"Viff-Task: p1\nTier: change\nCo-Authored-By: A <a@x>\nCo-Authored-By: B <b@x>\n"
	tcommit(t, dir, "a.txt", "one", msg)
	// A second commit, with no trailers, AFTER the multi-line one: if the scan mis-frames the
	// first message it loses this one, which is the failure mode that matters.
	tcommit(t, dir, "b.txt", "two", "plain subject only")

	cs := gitRecentCommits(t.Context(), dir, 10)
	if len(cs) != 2 {
		t.Fatalf("got %d commits, want 2 — a multi-line message ate the scan", len(cs))
	}
	// Newest first.
	if cs[0].msg != "plain subject only" || cs[0].trailers != nil {
		t.Errorf("commit without trailers: msg=%q trailers=%v", cs[0].msg, cs[0].trailers)
	}
	if cs[1].msg != "fix the thing" {
		t.Errorf("subject = %q — %%s must stay the subject even though %%B is now fetched", cs[1].msg)
	}
	want := map[string]string{
		"Viff-Task":      "p1",
		"Tier":           "change",
		"Co-Authored-By": "A <a@x> B <b@x>", // repeated keys join, as git's own separator does
	}
	if !reflect.DeepEqual(cs[1].trailers, want) {
		t.Errorf("trailers = %#v, want %#v", cs[1].trailers, want)
	}
	if len(cs[1].sha) != 40 {
		t.Errorf("sha = %q — the record separator left something on the front of it", cs[1].sha)
	}
}

// A commit body is arbitrary text, so it will eventually contain the field separator. The
// message is fetched LAST and split with a bound for exactly that reason; without it, one
// commit containing 0x1f shifts every field after it.
func TestBodyContainingTheFieldSeparatorDoesNotShiftFields(t *testing.T) {
	dir := t.TempDir()
	tgit(t, dir, "init", "-b", "main")
	tgit(t, dir, "config", "user.email", "t@example.com")
	tgit(t, dir, "config", "user.name", "Tester")
	tgit(t, dir, "config", "commit.gpgsign", "false")
	tcommit(t, dir, "a.txt", "one", "subject\n\nbody with \x1f in it\n\nTier: fix\n")

	cs := gitRecentCommits(t.Context(), dir, 10)
	if len(cs) != 1 {
		t.Fatalf("got %d commits, want 1", len(cs))
	}
	if cs[0].msg != "subject" {
		t.Errorf("msg = %q", cs[0].msg)
	}
	if got := cs[0].trailers["Tier"]; got != "fix" {
		t.Errorf("trailers = %v, want Tier=fix", cs[0].trailers)
	}
}

func TestParseTrailers(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  string
		want map[string]string
	}{{
		name: "subject only is not a trailer block",
		msg:  "just a subject",
		want: nil,
	}, {
		name: "a prose final paragraph is not a trailer block",
		msg:  "subject\n\nSo the answer is: it depends, and here is why.",
		want: nil,
	}, {
		name: "one non-trailer line disqualifies the paragraph",
		msg:  "subject\n\nTier: change\nand then some prose",
		want: nil,
	}, {
		name: "continuation lines fold into the trailer above",
		msg:  "subject\n\nChecked: make check\n  and make test\nTier: change",
		want: map[string]string{"Checked": "make check and make test", "Tier": "change"},
	}, {
		name: "a value may contain colons",
		msg:  "subject\n\nNotice: git-issue f69agjyeh12ksa25cnn0: still open",
		want: map[string]string{"Notice": "git-issue f69agjyeh12ksa25cnn0: still open"},
	}, {
		// Not a bug to fix here: a trailer block is a heuristic over the last paragraph, and
		// git's own parser reports this one too. It is the argument for the convention living
		// in the key NAMESPACE — `Viff-Task` is safe to match on, bare `Status` never was.
		name: "prose shaped like a trailer is indistinguishable from one",
		msg:  "subject\n\nSome preamble.\n\nStatus: proposal, nothing here runs.",
		want: map[string]string{"Status": "proposal, nothing here runs."},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseTrailers(tc.msg); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseTrailers = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func tgit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func tcommit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "add", file)
	// --cleanup=verbatim so a test's trailer block survives exactly as written.
	tgit(t, dir, "commit", "--cleanup=verbatim", "-m", msg)
}
