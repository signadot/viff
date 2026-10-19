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

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// A task asks over its channel and the runner acts: a push of committed work carrying the
// trailers lands at the remote, a branch outside the launch's allowance is refused before
// anything moves, a commit without the trailers is refused with what is missing, and a fetch
// says what moved upstream.
func TestTheRunnerActsOnWhatTheTaskAsks(t *testing.T) {
	forge := t.TempDir()
	origin := initRepo(t, forge, "acme/svc")
	l := &runner.Launch{ID: "t1", Doer: runner.Doer{Agent: "verse", With: runner.With{Git: []runner.Repo{
		{Repo: "acme/svc"},
	}}}, Policy: task.Policy{
		{Name: "no-main", Match: &task.Match{System: "git", Verb: "push", Params: map[string]string{"ref": "refs/heads/main"}}, Then: task.Then{Mode: task.ThenDeny}},
		{Match: &task.Match{System: "git"}, Then: task.Then{Mode: task.ThenAllow}},
		{Then: task.Then{Mode: task.ThenAllow}},
	}}
	repos, err := workspace.FromLaunch(l.Doer.With.Git, forge)
	if err != nil {
		t.Fatal(err)
	}
	mirrors := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
	ws, err := mirrors.Open(context.Background(), l.ID, repos, t.TempDir(), workspace.Author{Name: "t", Email: "t@invalid"})
	if err != nil {
		t.Fatal(err)
	}
	ceilings := map[string]task.Policy{"git": {
		{Name: "no-release", Match: &task.Match{System: "git", Verb: "push", Params: map[string]string{"ref": "refs/heads/release/**"}}, Then: task.Then{Mode: task.ThenDeny}},
		{Then: task.Then{Mode: task.ThenAllow}},
	}}
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
	commit(t, dir, "work\n\nVerse-Task: t1\n")

	// A branch a row of the policy denies: refused here too, whatever the task decided, and
	// nothing reaches the remote.
	rep, err := cl.Ask(Request{Op: "push", Repo: "svc", Branch: "main"})
	if err != nil || rep.Refused == "" || !strings.Contains(rep.Refused, "refused by the policy's row no-main") {
		t.Fatalf("a push to main: %+v %v", rep, err)
	}
	// And one the runner's ceiling denies, which the policy allowed: the operator's line holds.
	rep, err = cl.Ask(Request{Op: "push", Repo: "svc", Branch: "release/1.0"})
	if err != nil || rep.Refused == "" || !strings.Contains(rep.Refused, "refused by the runner's ceiling, its row no-release for git") {
		t.Fatalf("a push to release/1.0: %+v %v", rep, err)
	}
	// Its own branch: pushed.
	rep, err = cl.Ask(Request{Op: "push", Repo: "svc", Branch: ws.Branch})
	if err != nil || !rep.OK {
		t.Fatalf("a push to its own branch: %+v %v", rep, err)
	}
	if msg := message(t, origin, "refs/heads/"+ws.Branch); !strings.HasPrefix(msg, "work") {
		t.Fatalf("the remote's %s holds %q", ws.Branch, msg)
	}
	// A commit without the trailers: refused, with what is missing.
	commit(t, dir, "more work\n")
	rep, err = cl.Ask(Request{Op: "push", Repo: "svc", Branch: ws.Branch})
	if err != nil || !strings.Contains(rep.Refused, "Verse-Task") {
		t.Fatalf("a commit without trailers: %+v %v", rep, err)
	}
	// Upstream moves; a fetch says so, its own pushed branch included.
	advance(t, origin)
	rep, err = cl.Ask(Request{Op: "fetch", Repo: "svc"})
	if err != nil || !rep.OK || len(rep.Updated) != 2 || !strings.HasPrefix(rep.Updated[0], "mirror/main ") ||
		!strings.HasPrefix(rep.Updated[1], "mirror/"+ws.Branch+" new..") {
		t.Fatalf("a fetch after upstream moved: %+v %v", rep, err)
	}
	// A repository the workspace does not have.
	if rep, err := cl.Ask(Request{Op: "push", Repo: "other"}); err != nil || rep.Error == "" {
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

// commit writes a file in a checkout and commits everything with msg.
func commit(t *testing.T, dir, msg string) {
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
	sig := &object.Signature{Name: "t", Email: "t@invalid", When: time.Now()}
	if _, err := wt.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
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
	l := &runner.Launch{ID: "t1", Doer: runner.Doer{Agent: "verse", With: runner.With{Git: []runner.Repo{
		{Repo: "acme/svc"},
	}}}, Policy: task.Policy{
		{Name: "no-rewrite", Match: &task.Match{System: "git", Verb: "push", Params: map[string]string{"force": "true"}}, Then: task.Then{Mode: task.ThenDeny}},
		{Then: task.Then{Mode: task.ThenAllow}},
	}}
	repos, err := workspace.FromLaunch(l.Doer.With.Git, forge)
	if err != nil {
		t.Fatal(err)
	}
	mirrors := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
	ws, err := mirrors.Open(context.Background(), l.ID, repos, t.TempDir(), workspace.Author{})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := Open(l.ID, ws, mirrors, func(workspace.Repo) (string, error) { return "", nil }, Services{}, nil, l, io.Discard)
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
	commit(t, dir, "work\n\nVerse-Task: t1\n")
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
	rep, err := cl.Ask(Request{Op: "push", Repo: "svc", Branch: ws.Branch})
	chmodDirs(0o755)
	if err != nil || rep.Error == "" {
		t.Fatalf("the first push should have failed at the forge: %+v %v", rep, err)
	}
	// The sweep lands it.
	if n, err := mirrors.Reflect(context.Background(), origin, nil); err != nil || n != 1 {
		t.Fatalf("the sweep landed %d: %v", n, err)
	}
	// Pushed again after more work: the task's own.
	commit(t, dir, "more\n\nVerse-Task: t1\n")
	if rep, err := cl.Ask(Request{Op: "push", Repo: "svc", Branch: ws.Branch}); err != nil || !rep.OK {
		t.Fatalf("a push to the branch the task made, which the sweep landed: %+v %v", rep, err)
	}
	if msg := message(t, origin, "refs/heads/"+ws.Branch); !strings.HasPrefix(msg, "more") {
		t.Fatalf("the forge's %s is %q", ws.Branch, msg)
	}

	// The task's own branch is rewritten without saying so: a push of it after a rewrite of
	// the work lands. Another run's branch is not the task's own: a push that would rewrite it
	// is what `force` says, and the policy's row refuses that.
	r, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := r.Reference("refs/heads/main", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Storer.SetReference(plumbing.NewHashReference("refs/heads/verse/auto/t0", seed.Hash())); err != nil {
		t.Fatal(err)
	}
	if rep, err := cl.Ask(Request{Op: "push", Repo: "svc", Branch: "verse/auto/t0", Force: true}); err != nil || !strings.Contains(rep.Refused, "no-rewrite") {
		t.Errorf("a forced push to another run's branch: %+v %v, want the policy's row to refuse it", rep, err)
	}
	if rep, err := cl.Ask(Request{Op: "push", Repo: "svc", Branch: ws.Branch}); err != nil || !rep.OK {
		t.Errorf("the task's own branch, pushed again: %+v %v", rep, err)
	}
}

// ANOTHER TASK'S BRANCH IS NOT THIS TASK'S TO PUSH, under the rows a charter writes for a task's
// own work: a create is a push to a branch that is not at the forge, and an update a push to one
// whose tip this task made, both facts the runner fills. t0's branch, at main's tip, is neither
// for t1, so t1's fast-forward onto it, its rewrite of it and its deletion are refused by the row
// after; t1's own branch is created, and then updated. (Daniel's finding on #24.)
func TestAnotherTasksBranchIsNotThisTasksToPush(t *testing.T) {
	forge := t.TempDir()
	origin := initRepo(t, forge, "acme/svc")
	allow, deny := task.Then{Mode: task.ThenAllow}, task.Then{Mode: task.ThenDeny}
	push := func(name string, params map[string]string) task.Row {
		return task.Row{Name: name, Match: &task.Match{System: "git", Verb: "push", Params: params}, Then: allow}
	}
	own := task.Policy{
		push("create", map[string]string{"ref": "refs/heads/verse/auto/*", "exists": "false"}),
		push("update", map[string]string{"ref": "refs/heads/verse/auto/*", "own": "true"}),
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
	if err := r.Storer.SetReference(plumbing.NewHashReference("refs/heads/verse/auto/t0", seed.Hash())); err != nil {
		t.Fatal(err)
	}
	ws, _, cl := openTaskUnder(t, forge, runner.Repo{}, own)
	commit(t, filepath.Join(ws.Root, "svc"), "work\n\nVerse-Task: t1\n")

	for _, req := range []Request{
		{Op: "push", Repo: "svc", Branch: "verse/auto/t0"},
		{Op: "push", Repo: "svc", Branch: "verse/auto/t0", Force: true},
		{Op: "push", Repo: "svc", Branch: "verse/auto/t0", Delete: true},
	} {
		if rep, err := cl.Ask(req); err != nil || !strings.Contains(rep.Refused, "rest") {
			t.Errorf("%+v onto another task's branch: %+v %v, want the row after the task's own to refuse it", req, rep, err)
		}
	}
	if got, err := r.Reference("refs/heads/verse/auto/t0", true); err != nil || got.Hash() != seed.Hash() {
		t.Fatalf("t0's branch moved: %v %v", got, err)
	}
	if rep, err := cl.Ask(Request{Op: OpState, Repo: "svc", Branch: "verse/auto/t0"}); err != nil || !rep.OK || !rep.Exists || rep.Own {
		t.Errorf("state of another task's branch = %+v %v, want exists and not own", rep, err)
	}

	// The task's own: made, then made again.
	if rep, err := cl.Ask(Request{Op: "push", Repo: "svc", Branch: ws.Branch}); err != nil || !rep.OK {
		t.Fatalf("the task's own branch, created: %+v %v", rep, err)
	}
	commit(t, filepath.Join(ws.Root, "svc"), "more\n\nVerse-Task: t1\n")
	if rep, err := cl.Ask(Request{Op: "push", Repo: "svc", Branch: ws.Branch}); err != nil || !rep.OK {
		t.Fatalf("the task's own branch, updated: %+v %v", rep, err)
	}
	if msg := message(t, origin, "refs/heads/"+ws.Branch); !strings.HasPrefix(msg, "more") {
		t.Errorf("the forge's %s is %q", ws.Branch, msg)
	}
	// …and it is the task's own on a channel that remembers nothing, as a resumed task's is:
	// `own` is read off the tip's trailers, not off this channel.
	if _, _, again := openTaskUnder(t, forge, runner.Repo{}, own); true {
		if rep, err := again.Ask(Request{Op: OpState, Repo: "svc", Branch: ws.Branch}); err != nil || !rep.OK || !rep.Exists || !rep.Own {
			t.Errorf("state of the task's own branch, from a fresh channel = %+v %v, want exists and own", rep, err)
		}
	}
}

// THE RUNNER DECIDES BY THE POLICY THE TASK HAS NOW, not the one it was launched with: a push
// the launch's rows refused goes once a replacement allows it, and a push a replacement refuses
// is refused by the replacement's row. Without that, `verse task policy` would lift a deny in the
// task's process and nowhere else.
func TestTheRunnerDecidesByTheTasksCurrentPolicy(t *testing.T) {
	forge := t.TempDir()
	initRepo(t, forge, "acme/svc")
	allow, deny := task.Then{Mode: task.ThenAllow}, task.Then{Mode: task.ThenDeny}
	row := func(name string, then task.Then) task.Row {
		return task.Row{Name: name, Match: &task.Match{System: "git", Verb: "push"}, Then: then}
	}
	rest := task.Policy{{Match: &task.Match{System: "git"}, Then: allow}, {Then: allow}}
	ws, _, ch, cl := openChannel(t, forge, runner.Repo{}, append(task.Policy{row("launched", deny)}, rest...))
	commit(t, filepath.Join(ws.Root, "svc"), "work\n\nVerse-Task: t1\n")
	push := Request{Op: "push", Repo: "svc", Branch: ws.Branch}
	if rep, err := cl.Ask(push); err != nil || !strings.Contains(rep.Refused, "launched") {
		t.Fatalf("under the launch's rows: %+v %v, want its deny row to refuse", rep, err)
	}
	ch.SetPolicy(append(task.Policy{row("opened", allow)}, rest...))
	if rep, err := cl.Ask(push); err != nil || !rep.OK {
		t.Fatalf("once a replacement allows it: %+v %v", rep, err)
	}
	commit(t, filepath.Join(ws.Root, "svc"), "more\n\nVerse-Task: t1\n")
	ch.SetPolicy(append(task.Policy{row("closed", deny)}, rest...))
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
	asked := task.Policy{
		{Name: "gate", Match: &task.Match{System: "git", Verb: "push"}, Then: ask},
		{Match: &task.Match{System: "git"}, Then: allow},
		{Then: allow},
	}
	ws, _, ch, cl := openChannel(t, forge, runner.Repo{}, asked)
	commit(t, filepath.Join(ws.Root, "svc"), "work\n\nVerse-Task: t1\n")
	push := Request{Op: "push", Repo: "svc", Branch: ws.Branch}
	about := task.Step{Name: task.StepGitPush, Fields: map[string]string{
		"repo": "github.com/acme/svc", "ref": "refs/heads/" + ws.Branch, "delete": "false", "force": "false",
		"exists": "false", "own": "false"}}

	if rep, err := cl.Ask(push); err != nil || !strings.Contains(rep.Refused, "relayed no answer") {
		t.Fatalf("with no answer relayed: %+v %v, want it refused saying so", rep, err)
	}
	ch.Answer(about, task.Deny, "user:ana")
	if rep, err := cl.Ask(push); err != nil || !strings.Contains(rep.Refused, "relayed no answer") {
		t.Fatalf("on a relayed deny: %+v %v, want it refused", rep, err)
	}
	ch.Answer(about, task.Proceed, "user:ana")
	if rep, err := cl.Ask(push); err != nil || !rep.OK {
		t.Fatalf("on the relayed answer: %+v %v", rep, err)
	}
	if n := ch.Answers(); n != 1 {
		t.Errorf("%d answer(s) left, want the deny alone: the act took the proceed", n)
	}
	// The branch is there now and the task's own, which is a different call: an answer about
	// the create does not cover it, and a second push is asked about again.
	commit(t, filepath.Join(ws.Root, "svc"), "more\n\nVerse-Task: t1\n")
	if rep, err := cl.Ask(push); err != nil || !strings.Contains(rep.Refused, "relayed no answer") {
		t.Fatalf("a second push on the one answer: %+v %v, want it refused", rep, err)
	}
	about.Fields["exists"], about.Fields["own"] = "true", "true"
	ch.Answer(about, task.Proceed, "user:bo")
	if rep, err := cl.Ask(push); err != nil || !rep.OK {
		t.Fatalf("on the second answer, about the update: %+v %v", rep, err)
	}
}

// openTask is a task's workspace on forge's acme/svc with repo's permissions, and its end of the
// channel, served.
func openTask(t *testing.T, forge string, repo runner.Repo) (*workspace.Workspace, *workspace.Mirrors, *Client) {
	t.Helper()
	return openTaskUnder(t, forge, repo, pushing)
}

// openTaskUnder is openTask with the task's policy given.
func openTaskUnder(t *testing.T, forge string, repo runner.Repo, policy task.Policy) (*workspace.Workspace, *workspace.Mirrors, *Client) {
	t.Helper()
	ws, mirrors, _, cl := openChannel(t, forge, repo, policy)
	return ws, mirrors, cl
}

// openChannel is openTaskUnder with the runner's side of the channel too.
func openChannel(t *testing.T, forge string, repo runner.Repo, policy task.Policy) (*workspace.Workspace, *workspace.Mirrors, *Channel, *Client) {
	t.Helper()
	repo.Repo = "acme/svc"
	l := &runner.Launch{ID: "t1", Doer: runner.Doer{Agent: "verse", With: runner.With{Git: []runner.Repo{repo}}}, Policy: policy}
	repos, err := workspace.FromLaunch(l.Doer.With.Git, forge)
	if err != nil {
		t.Fatal(err)
	}
	mirrors := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
	ws, err := mirrors.Open(context.Background(), l.ID, repos, t.TempDir(), workspace.Author{})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := Open(l.ID, ws, mirrors, func(workspace.Repo) (string, error) { return "", nil }, Services{}, nil, l, io.Discard)
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
	for _, msg := range []string{"first\n\nVerse-Task: t1\n", "second\n\nVerse-Task: t1\n"} {
		commit(t, dir, msg)
		if rep, err := cl.Ask(Request{Op: "push", Repo: "svc", Branch: "main"}); err != nil || rep.Refused == "" {
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
	commit(t, dir, "on my-work\n\nVerse-Task: t1\n")
	if rep, err := cl.Ask(Request{Op: "push", Repo: "svc", Branch: ws.Branch}); err != nil || !rep.OK || strings.Contains(rep.Out, "nothing to push") {
		t.Fatalf("a push from my-work: %+v %v", rep, err)
	}
	if msg := message(t, origin, "refs/heads/"+ws.Branch); !strings.HasPrefix(msg, "on my-work") {
		t.Fatalf("the forge's %s is %q, want the work on my-work", ws.Branch, msg)
	}
}

// A VERB THE POLICY DOES NOT NAME IS NOT THE TASK'S: the channel refuses a call of it, whatever the
// policy's last row says, and says which verb; a verb a row names is decided by the rows.
func TestAVerbThePolicyDoesNotNameIsRefused(t *testing.T) {
	s := &Server{Log: io.Discard}
	allow := task.Then{Mode: task.ThenAllow}
	repo := map[string]string{"repo": "github.com/acme/svc"}
	for _, p := range []task.Policy{
		{{Match: &task.Match{System: "git-issue"}, Then: allow}, {Then: allow}},
		{{Then: allow}},
		nil,
	} {
		tk := Task{ID: "t1", Workspace: &workspace.Workspace{}, Launch: &runner.Launch{Policy: p}}
		if why := s.refused(tk, task.Step{Name: task.StepGitPush, Fields: repo}); !strings.Contains(why, "names no git.push") {
			t.Errorf("a push under %v: %q, want it refused as no verb of the policy's", p, why)
		}
	}
	tk := Task{ID: "t1", Workspace: &workspace.Workspace{}, Launch: &runner.Launch{Policy: task.Policy{
		{Match: &task.Match{System: "git-issue"}, Then: allow}, {Then: allow}}}}
	if why := s.refused(tk, task.Step{Name: task.StepGitIssuePush, Fields: repo}); why != "" {
		t.Errorf("a tracker push under a policy naming git-issue: refused, %q", why)
	}
}
