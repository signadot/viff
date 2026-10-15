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
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// A push the task asked for before its process died is finished before anything decides what
// did not leave. Settle closes the channel and then waits for the request in flight; without
// that, salvage would read a tree an approved push was staging from, hold the work under `held`
// as if nobody had authorized it, and the next sweep would send it twice.
func TestSettleWaitsForThePushInFlight(t *testing.T) {
	ctx := context.Background()
	origin := initRepo(t)
	m := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
	repos := []workspace.Repo{{Name: "svc", Repo: origin, Base: "main", Access: task.AccessWrite,
		Push: &task.RepoPush{New: []string{"verse/*"}}}}
	l := task.Launch{ID: "s1"}
	ws, err := m.Open(ctx, l.ID, repos, t.TempDir(), workspace.Author{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "work.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Tree("svc").CommitWork(l, "the work"); err != nil {
		t.Fatal(err)
	}

	// The credential is asked for after the allowance and before staging: holding it there is
	// a push in flight at the moment the process dies.
	asked, release := make(chan struct{}), make(chan struct{})
	cred := func(workspace.Repo) (string, error) { close(asked); <-release; return "", nil }
	ch, err := Open(l.ID, ws, m, cred, l, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	go ch.Serve()
	far, err := net.FileConn(ch.Child())
	if err != nil {
		t.Fatal(err)
	}
	ch.Child().Close()
	req, _ := codec.Marshal(&Request{Op: "push", Repo: "svc", Branch: "verse/x"}, codec.Tony)
	if err := writeFrame(far, req); err != nil {
		t.Fatal(err)
	}
	<-asked

	settled := make(chan bool, 1)
	go func() { settled <- ch.Settle(ctx, l, false) }()
	select {
	case <-settled:
		t.Fatal("Settle returned while a push was in flight")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if kept := <-settled; kept {
		t.Error("the workspace was kept, as if work had not left")
	}

	// The push landed, and nothing is held: the work left, and salvage saw that it had.
	if r, err := git.PlainOpen(origin); err != nil {
		t.Fatal(err)
	} else if _, err := r.Reference("refs/heads/verse/x", true); err != nil {
		t.Fatalf("the push in flight did not land: %v", err)
	}
	staged, err := m.Staged(origin)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range staged {
		if strings.Contains(s.Ref, "/held/") {
			t.Errorf("delivered work was held as if it had not left: %s", s.Ref)
		}
	}
}

// initRepo is a bare repository with one commit on main.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "origin.git")
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
	wt, err := wr.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@invalid", When: time.Now()}
	if _, err := wt.Commit("seed", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}
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
