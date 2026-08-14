package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/signadot/verse/task"
)

// A fetch must not prune staged work.
//
// This is the property the whole ref layout is chosen for. `ensure` fetches `+refs/heads/*` and
// `+refs/tags/*` with Prune set, and go-git's pruneRemotes removes local refs matching the REVERSE
// of a configured refspec — so a namespace outside those two is untouchable. Put staged work under
// refs/heads and the next spawn deletes it, silently, which is the exact failure this runner
// already had once with task branches in a shared mirror.
func TestFetchDoesNotPruneStagedWork(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	env := Environment{Name: "e", Repos: []Repo{
		{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/verse/auto/$task"},
	}}
	ws, err := m.Open(context.Background(), "p1", env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "work.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	held := ws.Salvage(context.Background(), task.Launch{ID: "p1"})
	if held.Err != nil || len(held.Staged) != 1 {
		t.Fatalf("salvage: %+v", held)
	}
	ref := held.Staged[0]

	// A later spawn on the same repository fetches the mirror again, with prune.
	if _, err := m.Open(context.Background(), "p2", env, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	mirror, err := git.PlainOpen(m.dirFor(origin))
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
	repo := Repo{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/verse/auto/$task"}
	env := Environment{Name: "e", Repos: []Repo{repo}}

	// One task whose delivery was approved and failed, one whose delivery was refused.
	approved, err := m.Open(context.Background(), "ok1", env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	refused, err := m.Open(context.Background(), "no1", env, t.TempDir())
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
	if _, err := approved.Trees[0].commitWork(task.Launch{ID: "ok1"}, "the approved work"); err != nil {
		t.Fatal(err)
	}
	if err := m.stage(context.Background(), approved.Trees[0], approved.Branch,
		stagedRef(stagePending, "ok1", "svc", "refs/heads/verse/auto/ok1")); err != nil {
		t.Fatal(err)
	}
	held := refused.Salvage(context.Background(), task.Launch{ID: "no1"})
	if len(held.Staged) != 1 || !strings.Contains(held.Staged[0], "refs/held/") {
		t.Fatalf("a refused delivery did not go to held: %+v", held)
	}

	n, err := m.Reflect(context.Background(), repo, nil)
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
	mirror, _ := git.PlainOpen(m.dirFor(origin))
	if _, err := mirror.Reference(plumbing.ReferenceName(stagedRef(stagePending, "ok1", "svc", "refs/heads/verse/auto/ok1")), true); err == nil {
		t.Error("a delivered ref is still pending, so it will be pushed again forever")
	}
	if _, err := mirror.Reference(plumbing.ReferenceName(held.Staged[0]), true); err != nil {
		t.Errorf("held work vanished: %v", err)
	}
	// And a second sweep has nothing to do.
	if n, err := m.Reflect(context.Background(), repo, nil); err != nil || n != 0 {
		t.Errorf("a second sweep moved %d refs (%v) — refs/pending is not self-limiting", n, err)
	}
}

// Salvage is what makes a refused or crashed task's work findable.
func TestSalvageHoldsWorkNobodyAuthorizedToLeave(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	env := Environment{Name: "e", Repos: []Repo{
		{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/verse/auto/$task"},
		{Name: "ref", Repo: initRepo(t), Base: "main"}, // reference tree: nothing to hold
	}}
	ws, err := m.Open(context.Background(), "s1", env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"svc", "ref"} {
		if err := os.WriteFile(filepath.Join(ws.Root, name, "work.txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := ws.Salvage(context.Background(), task.Launch{ID: "s1"})
	if d.Err != nil {
		t.Fatalf("salvage: %v", d.Err)
	}
	// Only the repository with somewhere to go is held: a reference tree's edits were never
	// going to leave, and staging them would imply they might.
	want := stagedRef(stageHeld, "s1", "svc", "")
	if len(d.Staged) != 1 || d.Staged[0] != want {
		t.Fatalf("Staged = %v, want [%s]", d.Staged, want)
	}
	if len(d.Pushed) != 0 {
		t.Fatal("salvage pushed something to a remote")
	}
	if len(d.Loose) != 0 {
		t.Fatalf("Loose = %v, so the workspace would be kept anyway", d.Loose)
	}
	if msg := commitAt(t, m.dirFor(origin), want); !strings.Contains(msg, "Verse-Task: s1") {
		t.Fatalf("held work carries no provenance: %q", msg)
	}
}

// Work that did not leave is FINDABLE, and the descriptor is where you find it.
//
// Staging made workspaces disposable, which removed the thing an operator used to `cd` into.
// Replacing it with a log line would have traded a lost result for an unfindable one — the mirror
// is a directory named by a hash of a remote url, so "it is in the mirror" is not an answer. The
// record carries the command.
func TestUndeliveredWorkIsListedWithHowToGetItBack(t *testing.T) {
	origin := initRepo(t)
	repo := Repo{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/verse/auto/$task"}
	cfg := &Config{Default: "e", Environments: []Environment{{Name: "e", Repos: []Repo{repo}}}}
	r := testRunner(t, cfg)

	// Nothing held yet.
	if got := r.staged(); len(got) != 0 {
		t.Fatalf("a fresh runner claims to hold %v", got)
	}
	ws, err := r.mirrors.Open(context.Background(), "s9", cfg.Environments[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "work.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := ws.Salvage(context.Background(), task.Launch{ID: "s9"}); d.Err != nil {
		t.Fatal(d.Err)
	}

	got := r.staged()
	if len(got) != 1 {
		t.Fatalf("staged = %+v, want one entry", got)
	}
	w := got[0]
	if w.Task != "s9" || w.Repo != "svc" || w.Ref != stagedRef(stageHeld, "s9", "svc", "") {
		t.Errorf("entry does not identify the work: %+v", w)
	}
	if !strings.Contains(w.Why, "authorized") {
		t.Errorf("held work does not say why it is held: %q", w.Why)
	}
	// The recovery command names the mirror by its real path, because a hash of a url is not
	// something anybody can be expected to reconstruct.
	if !strings.Contains(w.Fetch, r.mirrors.dirFor(origin)) || !strings.Contains(w.Fetch, w.Ref) {
		t.Errorf("the fetch command cannot be pasted: %q", w.Fetch)
	}
	// And the descriptor carries it, since that is the document an operator reads before
	// removing a runner — which would throw this away.
	rec := httptest.NewRecorder()
	r.Descriptor(rec, httptest.NewRequest("GET", "/runner", nil))
	if body := rec.Body.String(); !strings.Contains(body, "s9") || !strings.Contains(body, "undelivered") {
		t.Errorf("GET /runner does not report undelivered work:\n%s", body)
	}
}

// The descriptor publishes the containment THIS deployment can build, not the weakest one.
func TestTheDescriptorReportsTheConfinerItWasStartedWith(t *testing.T) {
	origin := initRepo(t)
	cfg := &Config{Default: "e", Environments: []Environment{{
		Name: "e", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}},
	}}}
	r := testRunner(t, cfg)
	r.conf = bwrapConfiner{bin: "/usr/bin/bwrap"}

	rec := httptest.NewRecorder()
	r.Descriptor(rec, httptest.NewRequest("GET", "/runner", nil))
	body := rec.Body.String()
	// A bwrap runner reaches a private filesystem; reporting the host confiner's floor here
	// understated it in the one document an operator reads before pointing a charter at it.
	if !strings.Contains(body, "private") {
		t.Errorf("a bwrap runner publishes the host floor:\n%s", body)
	}
	// And the field says per-leaf, because Provides refuses combinations no ceiling can express.
	if !strings.Contains(body, "perLeaf") {
		t.Errorf("the ceiling is not labelled as per-leaf, so it reads as a catalogue:\n%s", body)
	}
}
