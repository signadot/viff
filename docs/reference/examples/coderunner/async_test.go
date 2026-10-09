package main

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// shEnv is the smallest environment a task can run in: one repository, a shell agent that
// says what it was told to.
func shEnv(origin, say string) *Config {
	return &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Repos:  []Repo{{Name: "svc", Repo: origin, Base: "main"}},
			Driver: "sh",
			Env:    []string{"PATH"},
		}},
		Agents: map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "echo " + say}}},
	}
}

// A spawn is answered before the task exists. The caller's deadline bounds the answer and not
// the work, so a preparation that takes longer than the deadline — a first clone of anything
// real — is a task that is running, not a spawn that failed.
func TestASpawnIsAnsweredBeforeTheTaskIsPrepared(t *testing.T) {
	fastLifecycle(t)
	r := testRunner(t, shEnv(initRepo(t), "ok"))
	gate := make(chan struct{})
	r.prepareHook = func(string) { <-gate }
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	rt := runner.HTTP(srv.URL)

	start := time.Now()
	h := rt.Spawn(task.Launch{ID: "t1", Prompt: "say ok", Vector: &task.Vector{Default: task.Auto}})
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the spawn waited %s for a preparation that has not even begun", took)
	}
	// What the caller sees meanwhile: the task, running, on the list and by name.
	snap, err := rt.(task.Lister).TaskSnapshot("t1")
	if err != nil {
		t.Fatalf("a task that was accepted cannot be read back: %v", err)
	}
	if snap.Status != task.Running {
		t.Errorf("status while preparing = %q, want running", snap.Status)
	}
	list, err := rt.(task.Lister).ListTasks()
	if err != nil || len(list) != 1 || list[0].ID != "t1" {
		t.Errorf("the list while preparing = %v (%v), want t1 alone", list, err)
	}
	// Asked again while preparing: the same answer, not a second task and not a refusal.
	if again := rt.Spawn(task.Launch{ID: "t1", Prompt: "say ok"}); again.Err() != "" {
		t.Errorf("a second spawn of a task being prepared was refused: %s", again.Err())
	}

	close(gate)
	if st := awaitTerminal(t, h); st != task.Done {
		t.Fatalf("task ended %s (%s)", st, h.Err())
	}
	if got := h.Result(); got != "ok" {
		t.Errorf("result = %q, want ok", got)
	}
}

// A preparation that fails is a task that failed, with the reason, on the plane the caller is
// already reading — the same shape as a child that exits without a terminal status.
func TestAPreparationThatFailsIsAFailedTask(t *testing.T) {
	fastLifecycle(t)
	nowhere := filepath.Join(t.TempDir(), "no-such-repository")
	r := testRunner(t, shEnv(nowhere, "never"))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	rt := runner.HTTP(srv.URL)

	h := rt.Spawn(task.Launch{ID: "t2", Prompt: "say never"})
	if h.Err() != "" {
		t.Fatalf("the spawn itself was refused, so the caller never got a task to read: %s", h.Err())
	}
	if st := awaitTerminal(t, h); st != task.Failed {
		t.Fatalf("task ended %s, want failed", st)
	}
	why := h.Err()
	if !strings.Contains(why, "environment") || !strings.Contains(why, "svc") {
		t.Errorf("the failure does not say what could not be prepared: %q", why)
	}
	// And it stays said: the list and the snapshot both remember it.
	snap, err := rt.(task.Lister).TaskSnapshot("t2")
	if err != nil || snap.Status != task.Failed || snap.Err != why {
		t.Errorf("snapshot after the failure = %+v (%v)", snap, err)
	}
	list, _ := rt.(task.Lister).ListTasks()
	if len(list) != 1 || list[0].Status != task.Failed {
		t.Errorf("list after the failure = %v", list)
	}
}

// A cancel while preparing ends the preparation, and no process is ever started for it.
func TestACancelWhilePreparingIsCancelled(t *testing.T) {
	fastLifecycle(t)
	r := testRunner(t, shEnv(initRepo(t), "late"))
	gate := make(chan struct{})
	r.prepareHook = func(string) { <-gate }
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	rt := runner.HTTP(srv.URL)

	h := rt.Spawn(task.Launch{ID: "t3", Prompt: "say late"})
	if err := h.Cancel("changed my mind"); err != nil {
		t.Fatalf("cancel while preparing: %v", err)
	}
	close(gate)
	if st := awaitTerminal(t, h); st != task.Cancelled {
		t.Fatalf("task ended %s, want cancelled (%s)", st, h.Err())
	}
	if c := r.childOf("t3"); c == nil || c.cmd != nil {
		t.Error("a task process was started for a preparation that had been cancelled")
	}
	if _, err := filepath.Glob(filepath.Join(r.workspaces, "*t3*")); err != nil {
		t.Fatal(err)
	} else if m, _ := filepath.Glob(filepath.Join(r.workspaces, "*")); len(m) != 0 {
		t.Errorf("a workspace was left behind: %v", m)
	}
}
