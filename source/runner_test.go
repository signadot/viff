package source_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/infra/verse/entity"
	"github.com/example/infra/verse/source"
)

// Webhook is a hint that targets a repo and writes nothing.
func TestGitWebhookTargets(t *testing.T) {
	g := source.NewGit()
	g.AddRepo("A", "/tmp/x")

	refs, _ := g.Webhook(httptest.NewRequest("POST", "/hooks/git?repo=A", nil))
	if len(refs) != 1 || refs[0] != (entity.Ref{System: source.GitSystem, Kind: "head", ID: "A"}) {
		t.Fatalf("Webhook(?repo=A) = %+v, want a git:head:A target", refs)
	}
	if refs, _ := g.Webhook(httptest.NewRequest("POST", "/hooks/git", nil)); len(refs) != 0 {
		t.Fatalf("Webhook with no repo should request a full resync, got %+v", refs)
	}
}

// InstallHook writes an executable reference-transaction hook that POSTs the runner, refuses
// to clobber a foreign hook, and RemoveHook takes only ours back out.
func TestGitInstallHook(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	g := source.NewGit()
	id := g.AddRepo("", dir)

	if err := g.InstallHook(id, "http://127.0.0.1:9999/hooks/git"); err != nil {
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
	if s := string(b); !contains(s, "http://127.0.0.1:9999/hooks/git") || !contains(s, "repo="+id) {
		t.Fatalf("hook does not POST the runner: %s", s)
	}

	// a foreign hook is not clobbered.
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := g.InstallHook(id, "http://x/hooks/git"); err == nil {
		t.Fatal("install should refuse to overwrite a foreign hook")
	}
	// RemoveHook leaves the foreign hook alone.
	_ = g.RemoveHook(id)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("RemoveHook should not remove a foreign hook")
	}
}

// End-to-end informer: a hint POST to Hooks() drives a targeted reconcile that reflects a
// change the (disabled) periodic net wouldn't have caught yet.
func TestGitRunnerHookReconciles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	commit(t, dir, "a.txt", "hi", "first")

	store := entity.NewMemStore()
	g := source.NewGit()
	g.AddRepo("A", dir)
	rn := source.NewRunner(g, store, 0) // periodic net off: only initial + hints reconcile

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rn.Run(ctx)

	waitFor(t, store, entity.Ref{System: source.GitSystem, Kind: "head", ID: "A"}) // initial sync

	// a new branch the periodic net won't see (it's off) — only a hint reconcile surfaces it.
	gitRun(t, dir, "branch", "shiny")
	ts := httptest.NewServer(rn.Hooks())
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/hooks/git?repo=A", "application/json", nil)
	if err != nil {
		t.Fatalf("hook POST: %v", err)
	}
	resp.Body.Close()

	waitFor(t, store, entity.Ref{System: source.GitSystem, Kind: "ref", ID: "A/shiny"})
}

func waitFor(t *testing.T, store entity.Store, ref entity.Ref) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := store.Get(ref); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", ref)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
