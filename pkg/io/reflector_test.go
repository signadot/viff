package io_test

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	viffio "go.viff.dev/viff/pkg/io"
)

// The ref a git hint names. viff does not parse deliveries — a source owns its own and says
// which refs went stale — so this one constant is the whole of git's side of the contract, and
// the installed hook and the reflector have to agree about it.
func TestGitHintNamesTheRepo(t *testing.T) {
	want := entity.RefAt(viffio.GitRoot, "A")
	if got := viffio.RepoRef("A"); got != want {
		t.Fatalf("RepoRef(A) = %v, want %v", got, want)
	}
}

// InstallHook writes an executable reference-transaction hook that POSTs the reflector, refuses
// to clobber a foreign hook, and RemoveHook takes only ours back out.
func TestGitInstallHook(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	g := viffio.NewGit()
	id := g.AddRepo("", dir)

	if err := g.InstallHook(id, "http://127.0.0.1:9999/iods/git/laptop/hint"); err != nil {
		t.Fatalf("install: %v", err)
	}
	path := filepath.Join(dir, ".git", "hooks", "reference-transaction")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("hook not written: %v", err)
	}
	if info.Mode()&0o100 == 0 {
		t.Fatal("hook is not executable")
	}
	b, _ := os.ReadFile(path)
	// It posts the registration's hint route, naming the slice that went stale and carrying
	// nothing else — there is no data in it for viff to parse or to trust.
	// The address is escaped as a query value, and it is asked for rather than spelled: a
	// segment that kpath quotes (an id that looks like a number, which is exactly what AddRepo
	// hands out) carries its quotes through the escaping.
	want := "at=" + url.QueryEscape(g.RepoRef(id).String())
	if s := string(b); !contains(s, "/iods/git/laptop/hint") || !contains(s, want) {
		t.Fatalf("hook does not POST a hint at the registration: %s", s)
	}

	// a foreign hook is not clobbered.
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := g.InstallHook(id, "http://x/iods/git/laptop/hint"); err == nil {
		t.Fatal("install should refuse to overwrite a foreign hook")
	}
	// RemoveHook leaves the foreign hook alone.
	_ = g.RemoveHook(id)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("RemoveHook should not remove a foreign hook")
	}
}

// End-to-end informer: a hint drives a targeted reconcile that reflects a change the
// (disabled) periodic net wouldn't have caught yet. The route that delivers one is viffd's;
// this is the loop underneath it.
func TestGitReflectorHintReconciles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	commit(t, dir, "a.txt", "hi", "first")

	store := entitytest.Store(t)
	g := viffio.NewGit()
	g.AddRepo("A", dir)
	rf := viffio.NewReflector(g, store, 0) // periodic net off: only initial + hints reconcile

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rf.Run(ctx)

	waitFor(t, store, entity.RefAt(viffio.GitRoot, "A", "head")) // initial sync

	// a new branch the periodic net won't see (it's off) — only a hint reconcile surfaces it.
	gitRun(t, dir, "branch", "shiny")
	rf.Hint(viffio.RepoRef("A"))

	waitFor(t, store, entity.RefAt(viffio.GitRoot, "A", "ref", "shiny"))

	// And a hint with NO refs is a full resync, which is what a delivery that named nothing
	// asks for.
	gitRun(t, dir, "branch", "shinier")
	rf.Hint()
	waitFor(t, store, entity.RefAt(viffio.GitRoot, "A", "ref", "shinier"))
}

func waitFor(t *testing.T, store entity.Store, ref entity.Ref) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok, _ := store.Get(ref); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", ref)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// A source answers with entities, and `System()` does not say which HALF a ref is in: a source
// registered as `task` answering `mirror.task.instance.x` would pass a slice check that asked
// only about `System()`, and converge writes to the store directly — under the refusal that
// guards `POST /entity`. A deployed connector would then be the way into the half the 403
// exists to protect.
func TestAReflectionMayNotNameViffsOwnHalf(t *testing.T) {
	var r viffio.Reflection
	r.Add(entity.ViffRef("task", "instance", "run-1"), entity.Obj{"status": "done"})
	err := r.Check("task")
	if err == nil {
		t.Fatal("a reflection wrote into viff's own half")
	}
	if !strings.Contains(err.Error(), "viff's own half") {
		t.Errorf("the refusal does not say why: %v", err)
	}

	// The same system in the world's half is what a source IS for, and is untouched.
	var ok viffio.Reflection
	ok.Add(entity.RefAt("task", "instance", "run-1"), entity.Obj{"status": "done"})
	if err := ok.Check("task"); err != nil {
		t.Errorf("a world-half reflection was refused: %v", err)
	}
}
