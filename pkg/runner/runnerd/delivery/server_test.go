package delivery

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd/workspace"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/systems/systemstest"
	"go.viff.dev/viff/pkg/task"
)

// A task asks over its channel and the runner acts: a push of committed work carrying the
// trailers lands at the remote, a branch outside the launch's allowance is refused before
// anything moves, a commit without the trailers is refused with what is missing, and a fetch
// says what moved upstream.
func TestTheRunnerActsOnWhatTheTaskAsks(t *testing.T) {
	forge := t.TempDir()
	origin := initRepo(t, forge, "acme/svc")
	l := gitLaunch("t1", runner.Repo{Repo: "acme/svc"}, task.Autonomy{
		{Name: "no-main", Match: &task.Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"ref": ir.FromString("refs/heads/main")}}, Then: task.Then{Mode: task.ThenDeny}},
		{Match: &task.Match{System: "git"}, Then: task.Then{Mode: task.ThenAllow}},
		{Then: task.Then{Mode: task.ThenAllow}},
	})
	repos, err := workspace.FromLaunch(l.Repos(), forge)
	if err != nil {
		t.Fatal(err)
	}
	mirrors := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
	ws, err := mirrors.Open(context.Background(), l.ID, repos, t.TempDir(), workspace.Author{Name: "t", Email: "t@invalid"})
	if err != nil {
		t.Fatal(err)
	}
	// The runner's ceiling gives the task's own branches and main, and no release branch.
	ceilings := map[string]systems.Ceiling{"git": {{
		Attributes: map[string]*ir.Node{"repo": systems.Regexp("^github\\.com/acme/")},
		Verbs:      map[string][]systems.Shape{"fetch": {{}}, "push": {{"ref": ir.FromString("refs/heads/main")}, {"ref": systems.Regexp("^refs/heads/viff/")}}},
	}}}
	ch, err := Open(l.ID, ws, mirrors, func(workspace.Repo) (string, error) { return "", nil }, Services{}, ceilings, l, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() { ch.Serve(); close(served) }()
	conn, err := net.FileConn(ch.Child()) // what Attach does with the descriptor a child inherits
	if err != nil {
		t.Fatal(err)
	}
	_ = ch.Child().Close()
	cl := NewClient(conn)

	dir := filepath.Join(ws.Root, "svc")
	commit(t, dir, "work\n\nViff-Task: t1\n")

	// A branch a row of the policy denies: refused here too, whatever the task decided, and
	// nothing reaches the remote.
	rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": "main"}})
	if err != nil || rep.Refused == "" || !strings.Contains(rep.Refused, "refused by the autonomy's row no-main") {
		t.Fatalf("a push to main: %+v %v", rep, err)
	}
	// And one beyond the runner's ceiling, which the environment and the policy allowed: the
	// operator's line holds at the act, as a check that missed.
	rep, err = cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": "release/1.0"}})
	if err != nil || rep.Refused == "" || !strings.Contains(rep.Refused, "beyond the runner's ceiling for git") ||
		!strings.Contains(rep.Refused, "{ref: refs/heads/main}, {ref: !regexp ^refs/heads/viff/}") {
		t.Fatalf("a push to release/1.0: %+v %v", rep, err)
	}
	// Its own branch: pushed.
	rep, err = cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": ws.Branch}})
	if err != nil || !rep.OK {
		t.Fatalf("a push to its own branch: %+v %v", rep, err)
	}
	if msg := message(t, origin, "refs/heads/"+ws.Branch); !strings.HasPrefix(msg, "work") {
		t.Fatalf("the remote's %s holds %q", ws.Branch, msg)
	}
	// A commit without the trailers: refused, with what is missing.
	commit(t, dir, "more work\n")
	rep, err = cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": ws.Branch}})
	if err != nil || !strings.Contains(rep.Refused, "Viff-Task") {
		t.Fatalf("a commit without trailers: %+v %v", rep, err)
	}
	// Upstream moves; a fetch says so, its own pushed branch included.
	advance(t, origin)
	rep, err = cl.Ask(Request{Op: "fetch", Fields: map[string]string{"repo": "svc"}})
	if err != nil || !rep.OK || len(rep.Updated) != 2 || !strings.HasPrefix(rep.Updated[0], "mirror/main ") ||
		!strings.HasPrefix(rep.Updated[1], "mirror/"+ws.Branch+" new..") {
		t.Fatalf("a fetch after upstream moved: %+v %v", rep, err)
	}
	// A repository the workspace does not have.
	if rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "other"}}); err != nil || rep.Error == "" {
		t.Fatalf("a push to a repository not here: %+v %v", rep, err)
	}

	_ = cl.Close()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not end with the task's end of the channel")
	}
}

// initRepo makes a bare repository at forge/name with one commit on `main`.
func initRepo(t *testing.T, forge, name string) string {
	t.Helper()
	dir := filepath.Join(forge, name)
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	wr, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wr.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	commit(t, work, "seed")
	head, err := wr.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err := wr.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{
		config.RefSpec("+" + head.Name().String() + ":refs/heads/main"),
	}}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// advance puts one more commit on the origin's main.
func advance(t *testing.T, origin string) {
	t.Helper()
	work := t.TempDir()
	wr, err := git.PlainClone(work, false, &git.CloneOptions{URL: origin, ReferenceName: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	commit(t, work, "upstream moved")
	if err := wr.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
}

// commit writes a file in a checkout and commits everything with msg, as the identity of the
// launches here (gitLaunch's, which has no principal: the runner's).
func commit(t *testing.T, dir, msg string) {
	t.Helper()
	commitAs(t, dir, msg, workspace.RunnerAuthor)
}

// commitAs is commit as another identity, author and committer both.
func commitAs(t *testing.T, dir, msg string, who workspace.Author) plumbing.Hash {
	t.Helper()
	r, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "f-"+time.Now().Format("150405.000000000"))
	if err := os.WriteFile(name, []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: who.Name, Email: who.Email, When: time.Now()}
	h, err := wt.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// A PUSH AS ANOTHER IDENTITY IS REFUSED AND NOTHING LEAVES: a task's commit authored as someone
// other than its checkout's identity, as signadot/viff#1's three `test <test@example.com>`
// commits were, is refused at the push with the identity it must carry, and neither the forge nor
// the mirror's pending refs get it. Re-committed as the checkout's identity, it goes as before.
// For a person's launch and for a charter's.
func TestAPushAsAnotherIdentityIsRefusedAndNothingLeaves(t *testing.T) {
	// The third is a rule's task acting for a person, which is held to the person's identity: its
	// runner's identity, which it carried before, is refused too.
	forAna := entity.Pair("user:ana@example.com", "via:mirror.trigger.rule.run-request")
	for _, principal := range []string{"user:ana@example.com", "agent:comment-drift", forAna} {
		t.Run(principal, func(t *testing.T) {
			forge := t.TempDir()
			origin := initRepo(t, forge, "acme/svc")
			l := gitLaunch("t1", runner.Repo{Repo: "acme/svc"}, task.Autonomy{
				{Match: &task.Match{System: "git"}, Then: task.Then{Mode: task.ThenAllow}},
				{Then: task.Then{Mode: task.ThenAllow}},
			})
			l.Provenance.Principal = principal
			id := workspace.IdentityFor(l)
			repos, err := workspace.FromLaunch(l.Repos(), forge)
			if err != nil {
				t.Fatal(err)
			}
			mirrors := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
			ws, err := mirrors.Open(context.Background(), l.ID, repos, t.TempDir(), id)
			if err != nil {
				t.Fatal(err)
			}
			ch, err := Open(l.ID, ws, mirrors, func(workspace.Repo) (string, error) { return "", nil }, Services{}, systemstest.Open(l.Environment), l, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			go ch.Serve()
			conn, err := net.FileConn(ch.Child())
			if err != nil {
				t.Fatal(err)
			}
			_ = ch.Child().Close()
			cl := NewClient(conn)
			defer cl.Close()
			dir := filepath.Join(ws.Root, "svc")
			msg := "work\n\n" + strings.Join(workspace.RequiredTrailers(l), "\n") + "\n"
			if principal == forAna {
				if want := (workspace.Author{Name: "ana@example.com (viff viff)", Email: "ana+viff-viff-runner@example.com"}); id != want {
					t.Fatalf("a rule's task for ana is given %v, want %v", id, want)
				}
			}
			forgeRepo, err := git.PlainOpen(origin)
			if err != nil {
				t.Fatal(err)
			}
			r, err := git.PlainOpen(dir)
			if err != nil {
				t.Fatal(err)
			}
			wt, err := r.Worktree()
			if err != nil {
				t.Fatal(err)
			}
			// toBase drops what was committed, back to the checkout's base.
			toBase := func() {
				t.Helper()
				if err := wt.Reset(&git.ResetOptions{Commit: plumbing.NewHash(ws.Trees[0].Head), Mode: git.HardReset}); err != nil {
					t.Fatal(err)
				}
			}

			others := []workspace.Author{{Name: "test", Email: "test@example.com"}}
			if id != workspace.RunnerAuthor {
				others = append(others, workspace.RunnerAuthor)
			}
			for _, other := range others {
				toBase()
				commitAs(t, dir, msg, other)
				rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": ws.Branch}})
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range []string{"author " + other.Name + " <" + other.Email + ">", id.Name + " <" + id.Email + ">", "Amend or re-commit"} {
					if rep.OK || !strings.Contains(rep.Refused, s) {
						t.Errorf("a push as %v: %+v, want it refused naming %q", other, rep, s)
					}
				}
				if _, err := forgeRepo.Reference(plumbing.ReferenceName("refs/heads/"+ws.Branch), true); err == nil {
					t.Errorf("the forge has %s after a refused push as %v", ws.Branch, other)
				}
				staged, err := mirrors.Staged(origin)
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range staged {
					if s.Task == l.ID {
						t.Errorf("a refused push as %v was staged: %+v", other, s)
					}
				}
			}

			// Back to the base and committed again as the checkout's identity: delivered.
			toBase()
			commitAs(t, dir, msg, id)
			if rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": ws.Branch}}); err != nil || !rep.OK {
				t.Fatalf("a push as the checkout's identity: %+v %v", rep, err)
			}
			tip, err := forgeRepo.Reference(plumbing.ReferenceName("refs/heads/"+ws.Branch), true)
			if err != nil {
				t.Fatal(err)
			}
			c, err := forgeRepo.CommitObject(tip.Hash())
			if err != nil {
				t.Fatal(err)
			}
			if c.Author.Name != id.Name || c.Author.Email != id.Email || c.Committer.Name != id.Name || c.Committer.Email != id.Email {
				t.Errorf("the forge's tip is authored %s <%s>, committed %s <%s>; want %v", c.Author.Name, c.Author.Email, c.Committer.Name, c.Committer.Email, id)
			}
		})
	}
}

// message is the message of the commit a ref points at in a repository.
func message(t *testing.T, repo, ref string) string {
	t.Helper()
	r, err := git.PlainOpen(repo)
	if err != nil {
		t.Fatal(err)
	}
	h, err := r.Reference(plumbing.ReferenceName(ref), true)
	if err != nil {
		t.Fatalf("%s at %s: %v", ref, repo, err)
	}
	c, err := r.CommitObject(h.Hash())
	if err != nil {
		t.Fatal(err)
	}
	return c.Message
}

// neither is a branch of another run's: `create` reaches neither.
func TestABranchIsTheTasksOwnWhenItMadeIt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the forge refuses the first push by permissions, which don't stop root")
	}
	forge := t.TempDir()
	origin := initRepo(t, forge, "acme/svc")
	l := gitLaunch("t1", runner.Repo{Repo: "acme/svc"}, task.Autonomy{
		{Name: "no-rewrite", Match: &task.Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"force": ir.FromBool(true)}}, Then: task.Then{Mode: task.ThenDeny}},
		{Then: task.Then{Mode: task.ThenAllow}},
	})
	repos, err := workspace.FromLaunch(l.Repos(), forge)
	if err != nil {
		t.Fatal(err)
	}
	mirrors := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
	ws, err := mirrors.Open(context.Background(), l.ID, repos, t.TempDir(), workspace.Author{})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := Open(l.ID, ws, mirrors, func(workspace.Repo) (string, error) { return "", nil }, Services{}, systemstest.Open(l.Environment), l, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	go ch.Serve()
	conn, err := net.FileConn(ch.Child())
	if err != nil {
		t.Fatal(err)
	}
	_ = ch.Child().Close()
	cl := NewClient(conn)
	defer cl.Close()
	dir := filepath.Join(ws.Root, "svc")

	// The first push fails at the forge, and its work waits in the mirror.
	commit(t, dir, "work\n\nViff-Task: t1\n")
	objects := filepath.Join(origin, "objects")
	chmodDirs := func(mode os.FileMode) {
		_ = filepath.WalkDir(objects, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, mode)
			}
			return nil
		})
	}
	chmodDirs(0o555)
	rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": ws.Branch}})
	chmodDirs(0o755)
	if err != nil || rep.Error == "" {
		t.Fatalf("the first push should have failed at the forge: %+v %v", rep, err)
	}
	// The sweep lands it.
	if n, err := mirrors.Reflect(context.Background(), origin, nil); err != nil || n != 1 {
		t.Fatalf("the sweep landed %d: %v", n, err)
	}
	// Pushed again after more work: the task's own.
	commit(t, dir, "more\n\nViff-Task: t1\n")
	if rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": ws.Branch}}); err != nil || !rep.OK {
		t.Fatalf("a push to the branch the task made, which the sweep landed: %+v %v", rep, err)
	}
	if msg := message(t, origin, "refs/heads/"+ws.Branch); !strings.HasPrefix(msg, "more") {
		t.Fatalf("the forge's %s is %q", ws.Branch, msg)
	}

	// The task's own branch is rewritten without saying so: a push of it after a rewrite of
	// the work lands. Another run's branch is not the task's own: a push that would rewrite it
	// is what `force` says, and the autonomy's row refuses that.
	r, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := r.Reference("refs/heads/main", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Storer.SetReference(plumbing.NewHashReference("refs/heads/viff/auto/t0", seed.Hash())); err != nil {
		t.Fatal(err)
	}
	if rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": "viff/auto/t0", "force": "true"}}); err != nil || !strings.Contains(rep.Refused, "no-rewrite") {
		t.Errorf("a forced push to another run's branch: %+v %v, want the autonomy's row to refuse it", rep, err)
	}
	if rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": ws.Branch}}); err != nil || !rep.OK {
		t.Errorf("the task's own branch, pushed again: %+v %v", rep, err)
	}
}

// ANOTHER TASK'S BRANCH IS NOT THIS TASK'S TO PUSH, under the rows a charter writes for a task's
// own work: a create is a push to a branch that is not at the forge, and an update a push to one
// whose tip this task made, both facts the runner fills. t0's branch, at main's tip, is neither
// for t1, so t1's fast-forward onto it, its rewrite of it and its deletion are refused by the row
// after; t1's own branch is created, and then updated. (Victor's finding on #24.)
func TestAnotherTasksBranchIsNotThisTasksToPush(t *testing.T) {
	forge := t.TempDir()
	origin := initRepo(t, forge, "acme/svc")
	allow, deny := task.Then{Mode: task.ThenAllow}, task.Then{Mode: task.ThenDeny}
	push := func(name string, params map[string]*ir.Node) task.Row {
		return task.Row{Name: name, Match: &task.Match{System: "git", Verb: "push", Params: params}, Then: allow}
	}
	own := task.Autonomy{
		push("create", map[string]*ir.Node{"ref": systems.Glob("refs/heads/viff/auto/*"), "exists": ir.FromBool(false)}),
		push("update", map[string]*ir.Node{"ref": systems.Glob("refs/heads/viff/auto/*"), "own": ir.FromBool(true)}),
		{Name: "rest", Match: &task.Match{System: "git", Verb: "push"}, Then: deny},
		{Match: &task.Match{System: "git"}, Then: allow},
		{Then: allow},
	}
	r, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := r.Reference("refs/heads/main", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Storer.SetReference(plumbing.NewHashReference("refs/heads/viff/auto/t0", seed.Hash())); err != nil {
		t.Fatal(err)
	}
	ws, _, cl := openTaskUnder(t, forge, runner.Repo{}, own)
	commit(t, filepath.Join(ws.Root, "svc"), "work\n\nViff-Task: t1\n")

	for _, req := range []Request{
		{Op: "push", Fields: map[string]string{"repo": "svc", "branch": "viff/auto/t0"}},
		{Op: "push", Fields: map[string]string{"repo": "svc", "branch": "viff/auto/t0", "force": "true"}},
		{Op: "push", Fields: map[string]string{"repo": "svc", "branch": "viff/auto/t0", "delete": "true"}},
	} {
		if rep, err := cl.Ask(req); err != nil || !strings.Contains(rep.Refused, "rest") {
			t.Errorf("%+v onto another task's branch: %+v %v, want the row after the task's own to refuse it", req, rep, err)
		}
	}
	if got, err := r.Reference("refs/heads/viff/auto/t0", true); err != nil || got.Hash() != seed.Hash() {
		t.Fatalf("t0's branch moved: %v %v", got, err)
	}
	if rep, err := cl.Ask(Request{Op: OpState, Fields: map[string]string{"repo": "svc", "branch": "viff/auto/t0"}}); err != nil || !rep.OK || !rep.Exists || rep.Own {
		t.Errorf("state of another task's branch = %+v %v, want exists and not own", rep, err)
	}

	// The task's own: made, then made again.
	if rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": ws.Branch}}); err != nil || !rep.OK {
		t.Fatalf("the task's own branch, created: %+v %v", rep, err)
	}
	commit(t, filepath.Join(ws.Root, "svc"), "more\n\nViff-Task: t1\n")
	if rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": ws.Branch}}); err != nil || !rep.OK {
		t.Fatalf("the task's own branch, updated: %+v %v", rep, err)
	}
	if msg := message(t, origin, "refs/heads/"+ws.Branch); !strings.HasPrefix(msg, "more") {
		t.Errorf("the forge's %s is %q", ws.Branch, msg)
	}
	// …and it is the task's own on a channel that remembers nothing, as a resumed task's is:
	// `own` is read off the tip's trailers, not off this channel.
	if _, _, again := openTaskUnder(t, forge, runner.Repo{}, own); true {
		if rep, err := again.Ask(Request{Op: OpState, Fields: map[string]string{"repo": "svc", "branch": ws.Branch}}); err != nil || !rep.OK || !rep.Exists || !rep.Own {
			t.Errorf("state of the task's own branch, from a fresh channel = %+v %v, want exists and own", rep, err)
		}
	}
}

// THE RUNNER DECIDES BY THE POLICY THE TASK HAS NOW, not the one it was launched with: a push
// the launch's rows refused goes once a replacement allows it, and a push a replacement refuses
// is refused by the replacement's row. Without that, `viff task policy` would lift a deny in the
// task's process and nowhere else.
func TestTheRunnerDecidesByTheTasksCurrentPolicy(t *testing.T) {
	forge := t.TempDir()
	initRepo(t, forge, "acme/svc")
	allow, deny := task.Then{Mode: task.ThenAllow}, task.Then{Mode: task.ThenDeny}
	row := func(name string, then task.Then) task.Row {
		return task.Row{Name: name, Match: &task.Match{System: "git", Verb: "push"}, Then: then}
	}
	rest := task.Autonomy{{Match: &task.Match{System: "git"}, Then: allow}, {Then: allow}}
	ws, _, ch, cl := openChannel(t, forge, runner.Repo{}, append(task.Autonomy{row("launched", deny)}, rest...))
	commit(t, filepath.Join(ws.Root, "svc"), "work\n\nViff-Task: t1\n")
	push := Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": ws.Branch}}
	if rep, err := cl.Ask(push); err != nil || !strings.Contains(rep.Refused, "launched") {
		t.Fatalf("under the launch's rows: %+v %v, want its deny row to refuse", rep, err)
	}
	ch.SetAutonomy(append(task.Autonomy{row("opened", allow)}, rest...))
	if rep, err := cl.Ask(push); err != nil || !rep.OK {
		t.Fatalf("once a replacement allows it: %+v %v", rep, err)
	}
	commit(t, filepath.Join(ws.Root, "svc"), "more\n\nViff-Task: t1\n")
	ch.SetAutonomy(append(task.Autonomy{row("closed", deny)}, rest...))
	if rep, err := cl.Ask(push); err != nil || !strings.Contains(rep.Refused, "closed") {
		t.Fatalf("once a replacement refuses it: %+v %v, want the replacement's row to refuse", rep, err)
	}
}

// AN ACT A ROW ASKS ABOUT IS PERFORMED ON THE ANSWER THE RUNNER RELAYED, and on nothing else: the
// task's process holds the model, so its word that the question was answered is not taken. With
// no answer relayed the push is refused saying so; with one, about this call, it goes, once;
// the next push to the same branch is asked about again; a relayed deny is no answer.
func TestAnAskedPushIsPerformedOnTheAnswerTheRunnerRelayed(t *testing.T) {
	forge := t.TempDir()
	initRepo(t, forge, "acme/svc")
	allow, ask := task.Then{Mode: task.ThenAllow}, task.Then{Mode: task.ThenAsk}
	asked := task.Autonomy{
		{Name: "gate", Match: &task.Match{System: "git", Verb: "push"}, Then: ask},
		{Match: &task.Match{System: "git"}, Then: allow},
		{Then: allow},
	}
	ws, _, ch, cl := openChannel(t, forge, runner.Repo{}, asked)
	commit(t, filepath.Join(ws.Root, "svc"), "work\n\nViff-Task: t1\n")
	push := Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": ws.Branch}}
	about := task.NewStep(task.StepGitPush, map[string]string{
		"repo": "github.com/acme/svc", "from": "main", "ref": "refs/heads/" + ws.Branch, "delete": "false", "force": "false",
		"exists": "false", "own": "false"})

	if rep, err := cl.Ask(push); err != nil || !strings.Contains(rep.Refused, "relayed no answer") {
		t.Fatalf("with no answer relayed: %+v %v, want it refused saying so", rep, err)
	}
	ch.Answer("t1~d1", about, task.Deny, "user:ana")
	if rep, err := cl.Ask(push); err != nil || !strings.Contains(rep.Refused, "relayed no answer") {
		t.Fatalf("on a relayed deny: %+v %v, want it refused", rep, err)
	}
	ch.Answer("t1~d2", about, task.Proceed, "user:ana")
	if rep, err := cl.Ask(push); err != nil || !rep.OK {
		t.Fatalf("on the relayed answer: %+v %v", rep, err)
	}
	if n := ch.Answers(); n != 1 {
		t.Errorf("%d answer(s) left, want the deny alone: the act took the proceed", n)
	}
	// The branch is there now and the task's own, which is a different call: an answer about
	// the create does not cover it, and a second push is asked about again.
	commit(t, filepath.Join(ws.Root, "svc"), "more\n\nViff-Task: t1\n")
	if rep, err := cl.Ask(push); err != nil || !strings.Contains(rep.Refused, "relayed no answer") {
		t.Fatalf("a second push on the one answer: %+v %v, want it refused", rep, err)
	}
	about = about.With("exists", ir.FromBool(true)).With("own", ir.FromBool(true))
	ch.Answer("t1~d3", about, task.Proceed, "user:bo")
	if rep, err := cl.Ask(push); err != nil || !rep.OK {
		t.Fatalf("on the second answer, about the update: %+v %v", rep, err)
	}
	// An act that names its decision is matched by it: the answer need not say what call it
	// was about, and an act naming a decision nobody answered is refused naming it.
	commit(t, filepath.Join(ws.Root, "svc"), "again\n\nViff-Task: t1\n")
	ch.Answer("t1~d4", task.Step{}, task.Proceed, "user:cy")
	named := push
	named.Decision = "t1~d5"
	if rep, err := cl.Ask(named); err != nil || !strings.Contains(rep.Refused, "t1~d5") {
		t.Fatalf("an act naming an unanswered decision: %+v %v, want it refused naming the decision", rep, err)
	}
	named.Decision = "t1~d4"
	if rep, err := cl.Ask(named); err != nil || !rep.OK {
		t.Fatalf("an act naming the answered decision: %+v %v", rep, err)
	}
	if n := ch.Answers(); n != 1 {
		t.Errorf("%d answer(s) left, want the deny alone", n)
	}
	// An answer is to the call it was about: one about a push to main does not push the task's
	// branch, whatever decision the act names, and is kept.
	commit(t, filepath.Join(ws.Root, "svc"), "last\n\nViff-Task: t1\n")
	ch.Answer("t1~d6", about.With("ref", ir.FromString("refs/heads/main")), task.Proceed, "user:di")
	named.Decision = "t1~d6"
	if rep, err := cl.Ask(named); err != nil || !strings.Contains(rep.Refused, "about another call: this one differs in ref") {
		t.Fatalf("a push on the answer about another branch: %+v %v, want it refused naming the ref", rep, err)
	}
	if n := ch.Answers(); n != 2 {
		t.Errorf("%d answer(s) left, want the deny and the unspent answer", n)
	}
}

// openTask is a task's workspace on forge's acme/svc with repo's permissions, and its end of the
// channel, served.
func openTask(t *testing.T, forge string, repo runner.Repo) (*workspace.Workspace, *workspace.Mirrors, *Client) {
	t.Helper()
	return openTaskUnder(t, forge, repo, pushing)
}

// openTaskUnder is openTask with the task's policy given.
func openTaskUnder(t *testing.T, forge string, repo runner.Repo, policy task.Autonomy) (*workspace.Workspace, *workspace.Mirrors, *Client) {
	t.Helper()
	ws, mirrors, _, cl := openChannel(t, forge, repo, policy)
	return ws, mirrors, cl
}

// openChannel is openTaskUnder with the runner's side of the channel too.
func openChannel(t *testing.T, forge string, repo runner.Repo, policy task.Autonomy) (*workspace.Workspace, *workspace.Mirrors, *Channel, *Client) {
	t.Helper()
	repo.Repo = "acme/svc"
	l := gitLaunch("t1", repo, policy)
	repos, err := workspace.FromLaunch(l.Repos(), forge)
	if err != nil {
		t.Fatal(err)
	}
	mirrors := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
	ws, err := mirrors.Open(context.Background(), l.ID, repos, t.TempDir(), workspace.Author{})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := Open(l.ID, ws, mirrors, func(workspace.Repo) (string, error) { return "", nil }, Services{}, systemstest.Open(l.Environment), l, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	go ch.Serve()
	conn, err := net.FileConn(ch.Child())
	if err != nil {
		t.Fatal(err)
	}
	_ = ch.Child().Close()
	cl := NewClient(conn)
	t.Cleanup(func() { cl.Close() })
	return ws, mirrors, ch, cl
}

// TWO PIECES OF HELD WORK FROM ONE TASK ARE BOTH KEPT: each refused push is held under the commit
// it holds, so the second doesn't overwrite the first and leave its commit unreferenced.
func TestTwoRefusedPushesAreBothHeld(t *testing.T) {
	forge := t.TempDir()
	origin := initRepo(t, forge, "acme/svc")
	ws, mirrors, cl := openTask(t, forge, runner.Repo{})
	advance(t, origin) // main moves, so a push to it can't fast-forward and the forge refuses it
	dir := filepath.Join(ws.Root, "svc")
	for _, msg := range []string{"first\n\nViff-Task: t1\n", "second\n\nViff-Task: t1\n"} {
		commit(t, dir, msg)
		if rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": "main"}}); err != nil || rep.Refused == "" {
			t.Fatalf("a push to main that can't fast-forward: %+v %v", rep, err)
		}
	}
	staged, err := mirrors.Staged(origin)
	if err != nil {
		t.Fatal(err)
	}
	var held, fetch []string
	for _, s := range staged {
		if strings.Contains(s.Ref, "/held/") {
			held, fetch = append(held, s.Ref), append(fetch, s.Fetch)
		}
	}
	if len(held) != 2 || held[0] == held[1] {
		t.Fatalf("held %v, want both refused pushes kept", held)
	}
	// Each with its own way back: two held commits fetched into one branch would collide.
	into := func(cmd string) string { _, dst, _ := strings.Cut(strings.Fields(cmd)[3], ":"); return dst }
	if into(fetch[0]) == into(fetch[1]) {
		t.Errorf("the recovery commands fetch into one branch: %q", fetch)
	}
}

// WHAT IS PUSHED IS WHAT THE TASK HAS CHECKED OUT: work committed on a branch of its own naming
// is what reaches the forge, the commits the delivery check read, not its branch's old tip.
func TestThePushIsWhatTheTaskHasCheckedOut(t *testing.T) {
	forge := t.TempDir()
	origin := initRepo(t, forge, "acme/svc")
	ws, _, cl := openTask(t, forge, runner.Repo{})
	dir := filepath.Join(ws.Root, "svc")
	r, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{Branch: "refs/heads/my-work", Create: true}); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "on my-work\n\nViff-Task: t1\n")
	if rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": ws.Branch}}); err != nil || !rep.OK || strings.Contains(rep.Out, "nothing to push") {
		t.Fatalf("a push from my-work: %+v %v", rep, err)
	}
	if msg := message(t, origin, "refs/heads/"+ws.Branch); !strings.HasPrefix(msg, "on my-work") {
		t.Fatalf("the forge's %s is %q, want the work on my-work", ws.Branch, msg)
	}
}

// NOTHING OF ITS OWN IS NOT NOTHING TO PUSH: a task that fast-forwards onto another task's branch
// wrote no commit, but its push still moves the target there, as git's would; pushed again, the
// target is up to date and nothing moves.
func TestAPushOfUpstreamCommitsMovesTheTarget(t *testing.T) {
	forge := t.TempDir()
	origin := initRepo(t, forge, "acme/svc")
	// Another task's work, delivered before this one opens: a branch the mirror holds.
	work := t.TempDir()
	wr, err := git.PlainClone(work, false, &git.CloneOptions{URL: origin, ReferenceName: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	commit(t, work, "a car\n\nViff-Task: t0\n")
	if err := wr.Push(&git.PushOptions{RefSpecs: []config.RefSpec{"refs/heads/main:refs/heads/viff/auto/t0"}}); err != nil {
		t.Fatal(err)
	}
	ws, _, cl := openTask(t, forge, runner.Repo{})
	dir := filepath.Join(ws.Root, "svc")
	r, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	car, err := r.Reference("refs/remotes/mirror/viff/auto/t0", true)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.Reset(&git.ResetOptions{Commit: car.Hash(), Mode: git.HardReset}); err != nil {
		t.Fatal(err)
	}
	rep, err := cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": "viff/train"}})
	if err != nil || !rep.OK || strings.Contains(rep.Out, "nothing to push") {
		t.Fatalf("a push of another task's commit to a new branch: %+v %v", rep, err)
	}
	if msg := message(t, origin, "refs/heads/viff/train"); !strings.HasPrefix(msg, "a car") {
		t.Fatalf("the forge's viff/train is %q, want the car", msg)
	}
	rep, err = cl.Ask(Request{Op: "push", Fields: map[string]string{"repo": "svc", "branch": "viff/train"}})
	if err != nil || !rep.OK || !strings.Contains(rep.Out, "already at") {
		t.Fatalf("a push to a target already at the head: %+v %v", rep, err)
	}
}

// A STEP OUTSIDE THE ENVIRONMENT IS NOT THE TASK'S: the channel refuses it, whatever the policy's
// rows say, before any is read, saying what the environment gives; a step it gives is decided by
// the rows.
func TestAStepOutsideTheEnvironmentIsRefused(t *testing.T) {
	s := &Server{Log: io.Discard, Ceilings: performs("git", "git-issue")}
	allow := task.Then{Mode: task.ThenAllow}
	repo := map[string]string{"repo": "github.com/acme/svc"}
	for _, p := range []task.Autonomy{
		{{Match: &task.Match{System: "git"}, Then: allow}, {Then: allow}},
		{{Then: allow}},
		nil,
	} {
		l := within([]string{"acme/svc"}, "git-issue")
		l.Autonomy = p
		tk := Task{ID: "t1", Workspace: &workspace.Workspace{}, Launch: l}
		if why := s.refused(tk, "", task.NewStep(task.StepGitPush, repo)); !strings.Contains(why, "outside the environment: the environment gives no `git` (it gives: git-issue)") {
			t.Errorf("a push under %v: %q, want it refused as outside the environment", p, why)
		}
		if why := s.refused(tk, "", task.NewStep(task.StepGitIssuePush, repo)); p != nil && why != "" {
			t.Errorf("a tracker push the environment gives, under %v: refused, %q", p, why)
		}
	}
	if why := s.refused(Task{ID: "t2"}, "", task.NewStep(task.StepGitIssuePush, repo)); !strings.Contains(why, "outside the environment") {
		t.Errorf("a task nothing is known of has an empty environment: %q", why)
	}
}

// A PUSH WITHIN A SHAPE BY ITS OWNERSHIP ALONE IS DECIDED ON THE RUNNER'S FACTS: a shape naming
// `own: true` lets the push of a branch the task's own commit tops go, on the fields the runner
// fills, and refuses another's, quoting the shapes it gives so the call can be corrected.
func TestAPushWithinAShapeByOwnershipAlone(t *testing.T) {
	s := &Server{Log: io.Discard, Ceilings: performs("git")}
	l := &runner.Launch{ID: "t1", Prompt: "p", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}},
		Environment: systems.Environment{Systems: systems.Systems{"git": {{Attributes: map[string]string{"repo": "github.com/acme/svc"},
			Verbs: map[string][]systems.Shape{"push": {{"ref": systems.Regexp("^refs/heads/viff/"), "own": ir.FromBool(true)}}}}}}}}
	tk := Task{ID: "t1", Workspace: &workspace.Workspace{}, Launch: l}
	push := func(own bool) task.Step {
		return task.NewStep(task.StepGitPush, pushFields(&workspace.Tree{Repo: workspace.Repo{Repo: runner.Repo{Repo: "acme/svc", From: "main"}}},
			"refs/heads/viff/fix", false, false, runner.BranchState{Exists: true, Own: own}))
	}
	if why := s.refused(tk, "", push(true)); why != "" {
		t.Errorf("a push of the task's own branch: refused, %q", why)
	}
	why := s.refused(tk, "", push(false))
	if !strings.Contains(why, "outside the environment") || !strings.Contains(why, "it gives {own: true, ref: !regexp ^refs/heads/viff/}") {
		t.Errorf("a push of another's branch: %q, want it refused quoting the shape", why)
	}
	if !l.Environment.Allows(task.StepGitPush, push(true).Fields) || l.Environment.Allows(task.StepGitPush, push(false).Fields) {
		t.Error("the task's own check, on the same fields, does not agree with the runner's")
	}
}
