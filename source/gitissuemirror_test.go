package source_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/cmd/git-issue/issuelib"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/source"
)

// copyRef makes a SECOND ref at the same commit — a push that added without deleting, which
// is the shape every mirror of a git-issue tracker is in.
func copyRef(t *testing.T, dir, from, to string) {
	t.Helper()
	sha, err := exec.Command("git", "-C", dir, "rev-parse", from).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", from, err)
	}
	if out, err := exec.Command("git", "-C", dir, "update-ref", to, trimSHA(sha)).CombinedOutput(); err != nil {
		t.Fatalf("update-ref %s: %v: %s", to, err, out)
	}
}

func trimSHA(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// A mirror carries BOTH refs for an issue whose status has changed, and the status it reflects
// is the one the later ref says.
//
// `git issue` moves a ref to change status, so the repository somebody types in has exactly one
// ref per issue and the namespace IS the status. `git issue push` writes the new ref and does
// not delete the old, so a mirror holds both — on the verse tracker, 12 of 17 closed issues.
// Read naively that is one issue reflected twice, and nine closed issues were reading
// `status: open` on staging with nothing in the code deciding which won.
//
// Both directions, because the ambiguity is symmetric and that is the whole difficulty: a
// stale close and a live reopen leave an id in both namespaces, so "closed wins" would fix
// the first and pin every reopened issue closed forever. The refs settle it themselves —
// create, close and reopen are commits on one chain, so the descendant is current.
func TestGitIssueMirrorReadsTheCurrentRef(t *testing.T) {
	needGit(t)

	t.Run("a stale open ref beside a closure reads closed", func(t *testing.T) {
		dir := t.TempDir()
		initRepo(t, dir)
		xidr := fileIssue(t, dir, "shipped", "body", nil)
		// The mirror's shape: close moves the ref, and the push leaves the old one behind.
		copyRef(t, dir, issuelib.RefForXIDR(xidr), "refs/orig/"+xidr)
		setStatus(t, dir, xidr, "closed")
		copyRef(t, dir, "refs/orig/"+xidr, issuelib.RefForXIDR(xidr))

		if got := statusOf(t, dir, xidr); got != "closed" {
			t.Fatalf("status = %q, want closed — the stale open ref won", got)
		}
	})

	t.Run("a stale closed ref beside a reopen reads open", func(t *testing.T) {
		dir := t.TempDir()
		initRepo(t, dir)
		xidr := fileIssue(t, dir, "back again", "body", nil)
		setStatus(t, dir, xidr, "closed")
		copyRef(t, dir, issuelib.ClosedRefForXIDR(xidr), "refs/orig/"+xidr)
		setStatus(t, dir, xidr, "open")
		copyRef(t, dir, "refs/orig/"+xidr, issuelib.ClosedRefForXIDR(xidr))

		if got := statusOf(t, dir, xidr); got != "open" {
			t.Fatalf("status = %q, want open — a reopened issue was pinned closed", got)
		}
	})
}

// setStatus closes or reopens the way the `git issue` commands do, which is the part that
// matters here: the status and closedBy are written to meta.tony and COMMITTED, and only then
// does the ref move. That commit is why a real close leaves the two refs at different
// commits — the fact this whole fix rests on. A helper that only moved the ref (as
// closeIssue does, and as an earlier version of this test did) leaves both refs on ONE
// commit, where no ancestry exists to read and the test passes or fails by accident.
func setStatus(t *testing.T, dir, xidr, status string) {
	t.Helper()
	t.Chdir(dir)
	s := issuelib.NewGitStoreWithOutput(testLog{t})
	from, to := issuelib.RefForXIDR(xidr), issuelib.ClosedRefForXIDR(xidr)
	if status == "open" {
		from, to = to, from
	}
	issue, _, err := s.GetByRef(from)
	if err != nil {
		t.Fatalf("read %s: %v", from, err)
	}
	issue.Status = status
	if status == "closed" {
		sha := "0000000000000000000000000000000000000000"
		issue.ClosedBy = &sha
	} else {
		issue.ClosedBy = nil
	}
	if err := s.Update(issue, status, nil); err != nil {
		t.Fatalf("update %s: %v", xidr, err)
	}
	if err := s.MoveRef(from, to); err != nil {
		t.Fatalf("move %s → %s: %v", from, to, err)
	}
}

// statusOf reflects the repository and answers the issue entity's status — and fails if the
// issue was reflected more than once, since one issue is one entity however many refs a
// mirror is carrying for it.
func statusOf(t *testing.T, dir, xidr string) string {
	t.Helper()
	gi := source.NewGitIssue()
	gi.AddRepo("A", dir)
	rn, err := gi.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	var found []entity.Obj
	for _, e := range rn.Entities {
		if e.Ref.Kind() == "issue" && strings.Contains(e.Ref.ID(), xidr) {
			found = append(found, entity.ToObj(e.Payload))
		}
	}
	if len(found) != 1 {
		t.Fatalf("the issue was reflected %d times, want once", len(found))
	}
	st, _ := found[0]["status"].(string)
	if _, hasClosedBy := found[0]["closedBy"]; hasClosedBy && st != "closed" {
		t.Fatalf("an open issue carries closedBy: %v", found[0])
	}
	return st
}
