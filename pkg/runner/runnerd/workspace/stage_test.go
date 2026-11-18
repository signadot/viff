package workspace

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/systems"
)

// pushing is a launch's environment that gives the task a push to each repo: its work may
// leave, so work that did not is held.
func pushing(repos ...string) systems.Environment {
	e := systems.Environment{Systems: systems.Systems{}}
	for _, r := range repos {
		e.Systems["git"] = append(e.Systems["git"], systems.Block{Attributes: map[string]string{"repo": r},
			Verbs: map[string][]systems.Shape{"push": {{"ref": systems.Regexp("^refs/heads/")}}}})
	}
	return e
}

// A fetch must not prune staged work.
//
// This is the property the whole ref layout is chosen for. `ensure` fetches `refs/heads/*` and
// `refs/tags/*` with Prune set, and go-git's pruneRemotes removes local refs matching the REVERSE
// of a refspec it was given — so a namespace outside those two is untouchable. Put staged work under
// refs/heads and the next launch deletes it, silently, which is the exact failure this runner
// already had once with task branches in a shared mirror.
func TestFetchDoesNotPruneStagedWork(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{
		{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin},
	}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "work.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	held := ws.Salvage(context.Background(), &runner.Launch{ID: "p1", Environment: pushing("acme/svc")})
	if held.Err != nil || len(held.Staged) != 1 {
		t.Fatalf("salvage: %+v", held)
	}
	ref := held.Staged[0]

	// A later launch on the same repository fetches the mirror again, with prune.
	if _, err := m.Open(context.Background(), "p2", repos, t.TempDir(), Author{}); err != nil {
		t.Fatal(err)
	}
	mirror, err := git.PlainOpen(m.Dir(origin))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mirror.Reference(plumbing.ReferenceName(ref), true); err != nil {
		t.Fatalf("a fetch pruned staged work at %s: %v", ref, err)
	}
}

// The retry sweeps only what a gate approved.
//
// Two namespaces exist for exactly this. `pending` is work whose delivery was approved and did not
// land; `held` is work that was refused or never got that far. A sweep over one namespace would
// push, later and silently, work an operator declined — which is the whole thing that making the
// push gateable was for.
func TestReflectRetriesApprovedWorkAndNeverHeldWork(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repo := Repo{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}
	repos := []Repo{repo}

	// One task whose delivery was approved and failed, one whose delivery was refused.
	approved, err := m.Open(context.Background(), "ok1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	refused, err := m.Open(context.Background(), "no1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ws := range []*Workspace{approved, refused} {
		if err := os.WriteFile(filepath.Join(ws.Root, "svc", "work.txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The approved one is staged as pending — commit FIRST, then stage, which is the order
	// Deliver uses and the order that matters: staging an uncommitted tree stages the base, and
	// a test that got it backwards would pass on a commit nobody made.
	if _, err := approved.Trees[0].CommitWork(&runner.Launch{ID: "ok1"}, "the approved work"); err != nil {
		t.Fatal(err)
	}
	if err := m.stage(context.Background(), approved.Trees[0],
		StagedRef(StagePending, "ok1", "svc", "refs/heads/verse/auto/ok1"), tipOf(t, approved.Trees[0])); err != nil {
		t.Fatal(err)
	}
	held := refused.Salvage(context.Background(), &runner.Launch{ID: "no1", Environment: pushing("acme/svc")})
	if len(held.Staged) != 1 || !strings.Contains(held.Staged[0], "refs/held/") {
		t.Fatalf("a refused delivery did not go to held: %+v", held)
	}

	n, err := m.Reflect(context.Background(), origin, nil)
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if n != 1 {
		t.Fatalf("reflected %d refs, want exactly the approved one", n)
	}
	if msg := commitAt(t, origin, "refs/heads/verse/auto/ok1"); !strings.Contains(msg, "Verse-Task: ok1") {
		t.Errorf("the retry did not push the WORK (got %q) — an empty check here passes on the "+
			"base commit, which is what a stage-before-commit bug looks like", msg)
	}
	if commitAt(t, origin, "refs/heads/verse/auto/no1") != "" {
		t.Fatal("work a gate REFUSED was pushed by the retry — the sweep overrode the refusal")
	}
	// The pending ref is dropped once it lands; the held one stays where it is.
	mirror, _ := git.PlainOpen(m.Dir(origin))
	if _, err := mirror.Reference(plumbing.ReferenceName(StagedRef(StagePending, "ok1", "svc", "refs/heads/verse/auto/ok1")), true); err == nil {
		t.Error("a delivered ref is still pending, so it will be pushed again forever")
	}
	if _, err := mirror.Reference(plumbing.ReferenceName(held.Staged[0]), true); err != nil {
		t.Errorf("held work vanished: %v", err)
	}
	// And a second sweep has nothing to do.
	if n, err := m.Reflect(context.Background(), origin, nil); err != nil || n != 0 {
		t.Errorf("a second sweep moved %d refs (%v) — refs/pending is not self-limiting", n, err)
	}
}

// Salvage is what makes a refused or crashed task's work findable.
func TestSalvageHoldsWorkNobodyAuthorizedToLeave(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{
		{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin},
		{Repo: runner.Repo{Repo: "acme/ref", From: "main"}, Remote: initRepo(t)}, // reference tree: nothing to hold
	}
	ws, err := m.Open(context.Background(), "s1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"svc", "ref"} {
		if err := os.WriteFile(filepath.Join(ws.Root, name, "work.txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The environment gives no push to the reference repository: nothing of it leaves.
	l := &runner.Launch{ID: "s1", Environment: pushing("acme/svc")}
	l.Environment.Systems["git"] = append(l.Environment.Systems["git"], systems.Block{
		Attributes: map[string]string{"repo": "acme/ref"}, Verbs: map[string][]systems.Shape{"fetch": {{}}}})
	d := ws.Salvage(context.Background(), l)
	if d.Err != nil {
		t.Fatalf("salvage: %v", d.Err)
	}
	// Only the repository with somewhere to go is held: a reference tree's edits were never
	// going to leave, and staging them would imply they might.
	want := StagedRef(StageHeld, "s1", "svc", "") + "/" // and the commit it holds
	if len(d.Staged) != 1 || !strings.HasPrefix(d.Staged[0], want) {
		t.Fatalf("Staged = %v, want [%s<commit>]", d.Staged, want)
	}
	if commitAt(t, origin, "refs/heads/verse/auto/s1") != "" {
		t.Fatal("salvage pushed something to a remote")
	}
	if len(d.Loose) != 0 {
		t.Fatalf("Loose = %v, so the workspace would be kept anyway", d.Loose)
	}
	if msg := commitAt(t, m.Dir(origin), d.Staged[0]); !strings.Contains(msg, "Verse-Task: s1") {
		t.Fatalf("held work carries no provenance: %q", msg)
	}
}

// A retry rewrites its target only when the approval did: the namespace the work waits under
// says so, because the sweep runs without the launch that decided it — at startup, over every
// mirror this runner has, which the mirrors themselves enumerate.
func TestAForcedApprovalRetriesForcedAndTheMirrorsSayWhatThereIs(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
	forced, err := m.Open(context.Background(), "f1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	ff, err := m.Open(context.Background(), "f2", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	// Upstream moves after both checkouts, so neither branch descends from main any more.
	advanceOrigin(t, origin, "moved")
	for _, ws := range []*Workspace{forced, ff} {
		if err := os.WriteFile(filepath.Join(ws.Root, "svc", "work.txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ws.Trees[0].CommitWork(&runner.Launch{ID: ws.Branch}, "work"); err != nil {
			t.Fatal(err)
		}
	}
	// One approved to rewrite a branch of its own, one approved to fast-forward main.
	if d := forced.Stage(context.Background(), "f1", forced.Trees[0], "refs/heads/verse/train", true, tipOf(t, forced.Trees[0])); d.Err != nil {
		t.Fatal(d.Err)
	}
	if d := ff.Stage(context.Background(), "f2", ff.Trees[0], "refs/heads/main", false, tipOf(t, ff.Trees[0])); d.Err != nil {
		t.Fatal(d.Err)
	}
	remotes, err := m.Remotes()
	if err != nil || len(remotes) != 1 || remotes[0] != origin {
		t.Fatalf("Remotes = %v (%v), want the one origin", remotes, err)
	}
	n, err := m.Reflect(context.Background(), origin, nil)
	if n != 1 {
		t.Fatalf("reflected %d, want the forced one alone (%v)", n, err)
	}
	if err == nil || Refusal(err) == "" && !strings.Contains(err.Error(), "refused") {
		t.Errorf("the fast-forward of a moved main was not reported as the remote's refusal: %v", err)
	}
	if commitAt(t, origin, "refs/heads/verse/train") == "" {
		t.Error("the forced retry did not land")
	}
	if !strings.Contains(commitAt(t, origin, "refs/heads/main"), "moved") {
		t.Fatal("main was rewritten by a retry that was approved as a fast-forward")
	}
	// The refused one is held, not retried forever; the landed one is gone.
	held, err := m.Staged(origin)
	if err != nil || len(held) != 1 || !strings.Contains(held[0].Ref, "refs/held/") || held[0].Task != "f2" {
		t.Errorf("staged after the sweep = %+v (%v), want f2 held and nothing else", held, err)
	}
}

// Salvage is decided per tree. A task that delivered one repository and left work in another
// has work that did not leave, and a task that delivered everything has none to hold twice: what
// an approved push staged out of a tree is what that tree's head still is.
func TestSalvageHoldsOnlyTheWorkThatDidNotLeave(t *testing.T) {
	a, b := initRepo(t), initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{
		{Repo: runner.Repo{Repo: "acme/a", From: "main"}, Remote: a},
		{Repo: runner.Repo{Repo: "acme/b", From: "main"}, Remote: b},
	}
	ws, err := m.Open(context.Background(), "s2", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	l := &runner.Launch{ID: "s2", Environment: pushing("acme/a", "acme/b")}
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(ws.Root, name, "work.txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// `a` is committed and staged for an approved push; `b` is edited and never pushed.
	ta := ws.Tree("a")
	if work, err := ta.CommitWork(l, ""); err != nil || !work {
		t.Fatalf("commit a: work=%v err=%v", work, err)
	}
	if d := ws.Stage(context.Background(), "s2", ta, "refs/heads/verse/auto/s2", false, tipOf(t, ta)); d.Err != nil {
		t.Fatal(d.Err)
	}

	d := ws.Salvage(context.Background(), l)
	if d.Err != nil {
		t.Fatalf("salvage: %v", d.Err)
	}
	want := StagedRef(StageHeld, "s2", "b", "") + "/"
	if len(d.Staged) != 1 || !strings.HasPrefix(d.Staged[0], want) {
		t.Fatalf("Staged = %v, want [%s<commit>]: b's work did not leave and a's did", d.Staged, want)
	}

	// Work committed on top of what was delivered has not left either.
	if err := os.WriteFile(filepath.Join(ws.Root, "a", "more.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d = ws.Salvage(context.Background(), l)
	if d.Err != nil {
		t.Fatalf("salvage: %v", d.Err)
	}
	if !slices.ContainsFunc(d.Staged, func(r string) bool { return strings.HasPrefix(r, StagedRef(StageHeld, "s2", "a", "")+"/") }) {
		t.Fatalf("Staged = %v, want a's later work held", d.Staged)
	}
}

// WHAT IS STAGED IS WHAT WAS CHECKED: the commit the delivery check verified, named by its hash,
// though the task commits more in between — an unsigned commit the check would have refused never
// reaches the mirror. And nothing is written into the task's checkout to push it from.
func TestWhatIsStagedIsTheCommitThatWasChecked(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	tree, l := ws.Trees[0], &runner.Launch{ID: "p1"}
	wt, err := tree.repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	id := IdentityFor(l)
	sig := &object.Signature{Name: id.Name, Email: id.Email, When: time.Now()}
	commit := func(file, msg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(tree.Dir, file), []byte(file+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
			t.Fatal(err)
		}
	}
	commit("checked.txt", "checked\n\nVerse-Task: p1\n")
	ok, why, checked, err := tree.AuthoredWork(l)
	if err != nil || !ok || why != "" {
		t.Fatalf("the check: ok=%v why=%q err=%v", ok, why, err)
	}
	commit("after.txt", "unsigned, after the check")

	d := ws.Stage(context.Background(), "p1", tree, "refs/heads/verse/auto/p1", false, checked)
	if d.Err != nil {
		t.Fatal(d.Err)
	}
	mirror, err := git.PlainOpen(m.Dir(origin))
	if err != nil {
		t.Fatal(err)
	}
	staged, err := mirror.Reference(plumbing.ReferenceName(d.Staged[0]), true)
	if err != nil || staged.Hash() != checked {
		t.Fatalf("staged %v (%v), want the checked commit %s", staged, err, checked)
	}
	refs, err := tree.repo.References()
	if err != nil {
		t.Fatal(err)
	}
	_ = refs.ForEach(func(r *plumbing.Reference) error {
		if strings.HasPrefix(r.Name().String(), "refs/verse/") {
			t.Errorf("staging left %s in the task's checkout", r.Name())
		}
		return nil
	})
}
