package workspace

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/signadot/tony-format/git-issue/issuelib"
	"github.com/signadot/tony-format/git-issue/ops"

	"github.com/signadot/verse/pkg/runner"
)

// AN ISSUE IS FILED IN THE CHECKOUT AND LEAVES WHEN PUSHED, as a commit does: CreateIssue writes
// the ref here and the remote has nothing; PushIssues sends the tracker and the remote's own
// store lists the issue with the title given; a second push has nothing new to send.
func TestAnIssueIsFiledInTheCheckoutAndLeavesWhenPushed(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	ws, err := m.Open(context.Background(), "i1", []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.Trees[0]
	issue, err := tree.CreateIssue("the roof leaks", "when it rains\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issue.Ref, "refs/git-issues/v1/open/") {
		t.Fatalf("filed as %s", issue.Ref)
	}
	if got, _, err := issuelib.NewGitStoreAt(tree.Dir, io.Discard).Get(issue.ID); err != nil || got.Title != "the roof leaks" {
		t.Fatalf("the checkout's tracker: %+v, %v", got, err)
	}
	if remote, _ := issuelib.NewGitStoreAt(origin, io.Discard).List(false); len(remote) != 0 {
		t.Fatalf("a filing reached the remote before any push: %+v", remote)
	}
	if _, err := tree.CreateIssue(" ", "x"); err == nil || !strings.Contains(err.Error(), "title") {
		t.Errorf("no title: %v", err)
	}

	n, refusal, err := m.PushIssues(context.Background(), tree.Dir, origin, nil)
	if err != nil || refusal != "" || n != 1 {
		t.Fatalf("push: n=%d refusal=%q err=%v", n, refusal, err)
	}
	remote, err := issuelib.NewGitStoreAt(origin, io.Discard).List(false)
	if err != nil || len(remote) != 1 || remote[0].ID != issue.ID || remote[0].Title != "the roof leaks" {
		t.Fatalf("the remote's tracker lists %+v, %v", remote, err)
	}
	if n, refusal, err := m.PushIssues(context.Background(), tree.Dir, origin, nil); err != nil || refusal != "" || n != 0 {
		t.Errorf("a second push: n=%d refusal=%q err=%v, want nothing sent and no complaint", n, refusal, err)
	}
	// A checkout with no tracker has nothing to push, and says so rather than failing.
	ws2, err := m.Open(context.Background(), "i2", []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	if n, _, err := m.PushIssues(context.Background(), ws2.Trees[0].Dir, origin, nil); err != nil || n != 0 {
		t.Errorf("an empty tracker: n=%d, %v", n, err)
	}
}

// AN ISSUE EDITED HERE AND AT THE REMOTE BOTH IS A PUSH THE REMOTE REFUSES, answered as a
// refusal that says to pull first, and a pull leaves the task's copy as the task has it.
func TestAnIssueEditedOnBothSidesIsRefusedNotForced(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	ws, err := m.Open(context.Background(), "i3", []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.Trees[0]
	issue, err := tree.CreateIssue("the roof leaks", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, refusal, err := m.PushIssues(context.Background(), tree.Dir, origin, nil); err != nil || refusal != "" {
		t.Fatal(refusal, err)
	}
	// Both sides edit the one issue.
	for _, dir := range []string{origin, tree.Dir} {
		st := issuelib.NewGitStoreAt(dir, io.Discard)
		got, _, err := st.Get(issue.ID)
		if err != nil {
			t.Fatal(err)
		}
		got.Labels = append(got.Labels, "from-"+dir[len(dir)-3:])
		if err := st.Update(got, "label", nil); err != nil {
			t.Fatal(err)
		}
	}
	n, refusal, err := m.PushIssues(context.Background(), tree.Dir, origin, nil)
	if err != nil || n != 1 || !strings.Contains(refusal, "pull the tracker first") {
		t.Fatalf("push of a diverged issue: n=%d refusal=%q err=%v, want a refusal saying to pull", n, refusal, err)
	}
	mirror, err := m.FetchIssues(context.Background(), origin)
	if err != nil {
		t.Fatal(err)
	}
	moved, _, err := tree.RefreshIssues(mirror)
	if err != nil || len(moved) != 0 {
		t.Errorf("a pull over a diverged issue moved %v, %v; want the task's copy left alone", moved, err)
	}
}

// A CLOSE LEAVES THE REMOTE CLOSED: closing moves an issue's ref from `open/` to `closed/`, and
// the push deletes the remote's `open/` ref with the `closed/` one written, so the remote lists
// the issue closed and not also open. A remote that moved its `open/` ref since is the both-sides
// edit, refused, and nothing of the push is written.
func TestAClosePushedLeavesTheRemoteClosed(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	ws, err := m.Open(context.Background(), "i5", []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.Trees[0]
	refs := func(dir string) []string {
		r, err := git.PlainOpen(dir)
		if err != nil {
			t.Fatal(err)
		}
		iter, _ := r.References()
		var out []string
		_ = iter.ForEach(func(ref *plumbing.Reference) error {
			if strings.HasPrefix(ref.Name().String(), "refs/git-issues/v1/") {
				out = append(out, strings.TrimPrefix(ref.Name().String(), "refs/git-issues/v1/"))
			}
			return nil
		})
		return out
	}
	file := func(title string) *issuelib.Issue {
		issue, err := tree.CreateIssue(title, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, refusal, err := m.PushIssues(context.Background(), tree.Dir, origin, nil); err != nil || refusal != "" {
			t.Fatal(refusal, err)
		}
		return issue
	}

	closed := file("the roof leaks")
	if _, err := tree.CloseIssue(closed.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, refusal, err := m.PushIssues(context.Background(), tree.Dir, origin, nil); err != nil || refusal != "" || n == 0 {
		t.Fatalf("push of a close: n=%d refusal=%q err=%v", n, refusal, err)
	}
	xidr := strings.TrimPrefix(closed.Ref, "refs/git-issues/v1/open/")
	if got := strings.Join(refs(origin), " "); got != "closed/"+xidr {
		t.Errorf("the remote holds %q after the close, want only closed/%s", got, xidr)
	}

	// The remote's copy moves after this checkout last saw it, and the checkout closes it.
	moved := file("the door sticks")
	st := issuelib.NewGitStoreAt(origin, io.Discard)
	there, _, err := st.Get(moved.ID)
	if err != nil {
		t.Fatal(err)
	}
	there.Labels = append(there.Labels, "remote")
	if err := st.Update(there, "label", nil); err != nil {
		t.Fatal(err)
	}
	before := strings.Join(refs(origin), " ")
	if _, err := tree.CloseIssue(moved.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, refusal, err := m.PushIssues(context.Background(), tree.Dir, origin, nil); err != nil || !strings.Contains(refusal, "pull the tracker (git-issue-pull)") {
		t.Fatalf("a close over a remote edit: refusal=%q err=%v, want a refusal saying to pull", refusal, err)
	}
	if after := strings.Join(refs(origin), " "); after != before {
		t.Errorf("a refused push wrote the remote: %q, was %q", after, before)
	}
}

// A CLOSE AT THE REMOTE IS TAKEN BY THE PULL, not added beside the open ref: the push that
// finds it refuses, the pull moves the issue to `closed/` here and drops `open/`, though the
// mirror saw it open before, and the next push leaves the remote listing it closed alone.
func TestACloseAtTheRemoteIsPulledAsAMove(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	ws, err := m.Open(context.Background(), "i6", []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.Trees[0]
	refs := func(dir string) string {
		r, err := git.PlainOpen(dir)
		if err != nil {
			t.Fatal(err)
		}
		iter, _ := r.References()
		var out []string
		_ = iter.ForEach(func(ref *plumbing.Reference) error {
			if strings.HasPrefix(ref.Name().String(), "refs/git-issues/v1/") {
				out = append(out, strings.TrimPrefix(ref.Name().String(), "refs/git-issues/v1/"))
			}
			return nil
		})
		sort.Strings(out)
		return strings.Join(out, " ")
	}
	push := func() string {
		_, refusal, err := m.PushIssues(context.Background(), tree.Dir, origin, nil)
		if err != nil {
			t.Fatal(err)
		}
		return refusal
	}
	roof, err := tree.CreateIssue("the roof leaks", "")
	if err != nil {
		t.Fatal(err)
	}
	if refusal := push(); refusal != "" {
		t.Fatal(refusal)
	}
	// The mirror has seen the issue open, as it has once any task pulled it.
	pull := func() {
		mirror, err := m.FetchIssues(context.Background(), origin)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := tree.RefreshIssues(mirror); err != nil {
			t.Fatal(err)
		}
	}
	pull()
	if _, err := ops.Close(issuelib.NewGitStoreAt(origin, io.Discard), roof.ID, ""); err != nil {
		t.Fatal(err)
	}
	door, err := tree.CreateIssue("the door sticks", "")
	if err != nil {
		t.Fatal(err)
	}
	if refusal := push(); !strings.Contains(refusal, "pull the tracker (git-issue-pull)") {
		t.Fatalf("a push behind the remote's close: %q, want it refused", refusal)
	}
	pull()
	roofX := strings.TrimPrefix(roof.Ref, "refs/git-issues/v1/open/")
	doorX := strings.TrimPrefix(door.Ref, "refs/git-issues/v1/open/")
	want := []string{"closed/" + roofX, "open/" + doorX}
	sort.Strings(want)
	if got := refs(tree.Dir); got != strings.Join(want, " ") {
		t.Errorf("the checkout holds %q after the pull, want %q", got, strings.Join(want, " "))
	}
	if refusal := push(); refusal != "" {
		t.Fatalf("the push after the pull: %q", refusal)
	}
	if got := refs(origin); got != strings.Join(want, " ") {
		t.Errorf("the remote holds %q, want %q: the closed issue listed open again", got, strings.Join(want, " "))
	}
}

// A REPOSITORY'S TRACKER IS PULLED INTO THE CHECKOUT: the issue refs the ordinary fetch leaves
// out come into the mirror and then the checkout under their own names, so the checkout lists
// what the remote holds; a second pull with nothing new moves nothing; a remote with no tracker
// is no error; the task's own branch is untouched.
func TestIssuesArePulledIntoTheCheckout(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	ws, err := m.Open(context.Background(), "f1", []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.Trees[0]
	mirror, err := m.FetchIssues(context.Background(), origin)
	if err != nil {
		t.Fatalf("a remote with no tracker: %v", err)
	}
	if moved, open, err := tree.RefreshIssues(mirror); err != nil || len(moved) != 0 || len(open) != 0 {
		t.Fatalf("an empty tracker: moved %v, open %v, %v", moved, open, err)
	}
	filed, err := issuelib.NewGitStoreAt(origin, io.Discard).Create("the roof leaks", "# the roof leaks\n\nwhen it rains\n")
	if err != nil {
		t.Fatal(err)
	}
	if mirror, err = m.FetchIssues(context.Background(), origin); err != nil {
		t.Fatal(err)
	}
	moved, open, err := tree.RefreshIssues(mirror)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || !strings.HasPrefix(moved[0], "v1/open/"+filed.ID+" new..") {
		t.Errorf("moved %v, want the one issue ref, new", moved)
	}
	if len(open) != 1 || open[0].ID != filed.ID || open[0].Title != "the roof leaks" {
		t.Fatalf("the checkout lists %+v", open)
	}
	if got, _, err := issuelib.NewGitStoreAt(tree.Dir, io.Discard).Get(filed.ID); err != nil || got.Title != "the roof leaks" {
		t.Errorf("the checkout's tracker: %+v, %v", got, err)
	}
	if moved, open, err := tree.RefreshIssues(mirror); err != nil || len(moved) != 0 || len(open) != 1 {
		t.Errorf("a second refresh: moved %v, open %d, %v", moved, len(open), err)
	}
	r, _ := git.PlainOpen(tree.Dir)
	if _, err := r.Reference(plumbing.ReferenceName("refs/heads/"+ws.Branch), true); err != nil {
		t.Errorf("the task's branch: %v", err)
	}
}

// A COMMENT AND A CLOSE ARE WRITTEN HERE AND LEAVE WITH THE PUSH: the issue's ref moves to the
// closed namespace in the checkout, the push sends both refs' worth of history, and the remote's
// own store reads the issue closed, with the comment in its tree.
func TestACommentAndACloseLeaveWithThePush(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	ws, err := m.Open(context.Background(), "c1", []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.Trees[0]
	issue, err := tree.CreateIssue("the roof leaks", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.CommentIssue(issue.ID, "a tarp is on it"); err != nil {
		t.Fatal(err)
	}
	if _, err := tree.CommentIssue(issue.ID, " "); err == nil {
		t.Error("an empty comment was written")
	}
	closed, err := tree.CloseIssue(issue.ID, "")
	if err != nil || closed.Status != "closed" {
		t.Fatalf("close: %+v, %v", closed, err)
	}
	if _, _, err := m.PushIssues(context.Background(), tree.Dir, origin, nil); err != nil {
		t.Fatal(err)
	}
	remote := issuelib.NewGitStoreAt(origin, io.Discard)
	open, _ := remote.List(false)
	all, _ := remote.List(true)
	if len(open) != 0 || len(all) != 1 || all[0].ID != issue.ID || all[0].Status != "closed" {
		t.Fatalf("the remote lists open %+v, all %+v; want the one issue, closed", open, all)
	}
	shown, err := ops.Show(remote, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(shown), "a tarp is on it") {
		t.Errorf("the remote's copy lacks the comment: %+v", shown)
	}
}
