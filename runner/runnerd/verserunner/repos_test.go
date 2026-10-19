package verserunner

import (
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/verserunner/agent/modeltest"
	"github.com/signadot/verse/task"
)

// serveRepos serves the verse runner with its repos fetched from forge.
func serveRepos(t *testing.T, forge string, sec Secrets) client.Launcher {
	t.Helper()
	return serveReposAt(t, forge, sec, t.TempDir())
}

// serveReposAt is serveRepos keeping its mirrors in mirrors.
func serveReposAt(t *testing.T, forge string, sec Secrets, mirrors string) client.Launcher {
	t.Helper()
	return serveConfigured(t, Config{Repos: &Repos{Mirrors: mirrors, Forge: forge}}, sec, Tokens{})
}

// serveConfigured serves the verse runner configured by c, with the secrets and tokens given.
func serveConfigured(t *testing.T, c Config, sec Secrets, tokens Tokens) client.Launcher {
	t.Helper()
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control, err := os.MkdirTemp("", "vr")
	if err != nil {
		t.Fatal(err)
	}
	h, err := Standalone(c, sec, tokens, runnerd.Unconfined{},
		[]string{os.Args[0], "-test.run=^$", taskArg}, work, control, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.HTTPHandler())
	t.Cleanup(func() { srv.Close(); h.Shutdown(); os.RemoveAll(control) })
	return client.HTTP(srv.URL)
}

// A launch's repos are checked out for its task: each under its name in the task's working
// directory, on the task's own branch, with the runner's mirror the only remote.
func TestALaunchsReposAreCheckedOutForIt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads the script's descriptors from /proc")
	}
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveRepos(t, forge, Secrets{})
	rt.Launch(runner.Launch{ID: "t1", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Doer: runner.Doer{
		Script: "#!/bin/sh\nset -e\nls\ncd svc\ngit branch --show-current\ngit remote\ncat README\nfor f in /proc/$$/fd/*; do readlink $f; done | grep -c socket || true\nfor fd in 3 4 5; do [ ! -e /proc/$$/fd/$fd ] || echo fd$fd; done",
		With:   runner.With{Network: netOpen, Git: []runner.Repo{{Repo: "acme/svc"}}},
	}})
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done {
		t.Fatalf("task: %+v", snap)
	}
	// And it holds none of the descriptors its process was handed: no socket (the channel, the
	// control listener), nothing at fd 3, 4 or 5.
	if got, want := strings.Fields(snap.Result), []string{"svc", "verse/auto/t1", "mirror", "seed", "0"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("the task saw %q, want %q", got, want)
	}
}

// AN AGENT'S PUSH IS A STEP ITS GATES ASK ABOUT, AND THE RUNNER MAKES IT: the agent commits with
// the trailers its brief names and calls push; a git.push gate asks, naming the repo, the ref
// and that it is no delete; once approved the runner pushes, and the branch is at the forge.
func TestAnAgentsPushIsGatedAndTheRunnerMakesIt(t *testing.T) {
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	var brief string
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		switch res := r.Results(); len(res) {
		case 0:
			brief = r.Prompt()
			return modeltest.Use("run_bash", map[string]any{"command": "cd svc && echo work > f && git add f && " +
				"git -c user.name=a -c user.email=a@x commit -qm 'work' -m 'Verse-Task: t1'"})
		case 1:
			return modeltest.Use("git-push", map[string]any{})
		default:
			return modeltest.Say(res[len(res)-1])
		}
	})
	rt := serveRepos(t, forge, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL})
	rt.Launch(runner.Launch{ID: "t1", Prompt: "do the work", Policy: append(task.Policy{{Name: "push", Match: &task.Match{System: "git", Verb: "push", Params: map[string]string{"ref": "refs/heads/verse/auto/*"}}, Then: task.Then{Mode: task.ThenAsk}}},
		allowing("git", "commands", "files")...),
		Doer: runner.Doer{Agent: AgentName, With: runner.With{Network: netOpen,
			Git: []runner.Repo{{Repo: "acme/svc"}}}}})

	var q task.PendingGate
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline) && q.ID == ""; time.Sleep(20 * time.Millisecond) {
		if snap, err := rt.(task.Lister).TaskSnapshot("t1"); err == nil && len(snap.Pending) > 0 {
			q = snap.Pending[0]
		}
	}
	// The runner's two facts are on the question too: the branch is not at the forge yet.
	want := map[string]string{"repo": "github.com/acme/svc", "ref": "refs/heads/verse/auto/t1", "delete": "false", "force": "false",
		"exists": "false", "own": "false"}
	if q.Step != task.StepGitPush || q.Gate != "push" || !maps.Equal(q.Fields, want) {
		t.Fatalf("the question was %+v, want the push gate on %v", q, want)
	}
	resolver := rt.Attach("t1").(interface {
		Resolve(decision, choice, say, by string) error
	})
	if err := resolver.Resolve(q.ID, task.Proceed, "", "user:ana"); err != nil {
		t.Fatal(err)
	}
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "pushed svc -> refs/heads/verse/auto/t1") {
		t.Fatalf("task: %+v", snap)
	}
	if !strings.Contains(brief, "svc/ — acme/svc at main") || !strings.Contains(brief, "Verse-Task: t1") ||
		!strings.HasSuffix(brief, "do the work") {
		t.Errorf("the agent was briefed:\n%s", brief)
	}
	origin, err := git.PlainOpen(filepath.Join(forge, "acme/svc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := origin.Reference("refs/heads/verse/auto/t1", true); err != nil {
		t.Fatalf("the branch is not at the forge: %v", err)
	}
}

// A runner that keeps no mirrors doesn't offer repos, and refuses a launch that names them.
func TestARunnerWithoutReposRefusesThem(t *testing.T) {
	if _, ok := (Config{}).Describe(Secrets{}, nil).Offers[0].Systems["git"]; ok {
		t.Fatal("a runner with no mirrors offers repos")
	}
	rt, _ := serve(t, Secrets{}, runnerd.Unconfined{})
	h := rt.Launch(runner.Launch{ID: "t1", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAsk}}}, Doer: runner.Doer{Script: "#!/bin/sh\ntrue",
		With: runner.With{Network: netOpen, Git: []runner.Repo{{Repo: "acme/svc"}}}}})
	frames, stop := h.Subscribe()
	defer stop()
	f := <-frames
	if f.Kind != task.EventStatus || task.Status(f.Text) != task.Failed || !strings.Contains(f.Err, "intercepts no `git`") {
		t.Fatalf("the launch came back %+v", f)
	}
}

// A task cancelled while its repos are being checked out ends cancelled, with who cancelled it,
// though a first clone runs on to its end: the next launch finds the mirror there.
func TestATaskCancelledWhileItsReposAreCheckedOutIsCancelled(t *testing.T) {
	asked := make(chan struct{}, 1)
	release := make(chan struct{})
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case asked <- struct{}{}:
		default:
		}
		<-release // the forge takes its time
		http.Error(w, "gone", http.StatusServiceUnavailable)
	}))
	rt := serveRepos(t, forge.URL, Secrets{})
	t.Cleanup(func() { close(release); forge.Close() }) // first: the runner's own cleanup waits on its clone

	h := rt.Launch(runner.Launch{ID: "t1", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Doer: runner.Doer{
		Script: "#!/bin/sh\necho ran", With: runner.With{Network: netOpen, Git: []runner.Repo{{Repo: "acme/svc"}}},
	}})
	select {
	case <-asked:
	case <-time.After(10 * time.Second):
		t.Fatal("the runner never asked the forge for the repo")
	}
	if err := h.Cancel("not needed", "user:ana@example.com"); err != nil {
		t.Fatal(err)
	}
	snap := ended(t, rt, "t1")
	if snap.Status != task.Cancelled || snap.Err != "cancelled by user:ana@example.com: not needed" {
		t.Fatalf("the task ended %+v", snap)
	}
}

// A task that names repos and can't reach its delivery channel doesn't run: its work would have
// no way out.
func TestATaskWithoutItsChannelFails(t *testing.T) {
	r, w, err := os.Pipe() // a descriptor, and not a socket
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	// An agent and a script alike: both call their repos' tools over it.
	for _, doer := range []runner.Doer{{Agent: AgentName}, {Script: "true"}} {
		// Attach closes what it is given, as a task's process owns fd 5: so it is given a copy.
		fd, err := syscall.Dup(int(r.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		d := New(Config{Repos: &Repos{Mirrors: "unused"}}, Secrets{}, t.TempDir(), io.Discard)
		d.deliveryFD = uintptr(fd)
		doer.With = runner.With{Git: []runner.Repo{{Repo: "acme/svc"}}}
		p := d.Build(runner.Launch{ID: "t1", Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, Doer: doer}, task.NewRegistry())
		if _, err := p.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "delivery channel") {
			t.Fatalf("%s ran without its channel: %v", doer.Name(), err)
		}
	}
}

// ended waits for a task to end, and is its snapshot then.
func ended(t *testing.T, rt client.Launcher, id string) task.Snapshot {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if snap, err := rt.(task.Lister).TaskSnapshot(id); err == nil && snap.Status.Terminal() {
			return snap
		}
	}
	t.Fatalf("%s didn't end", id)
	return task.Snapshot{}
}

// seed makes a bare repository at forge/name with one commit on main: a README saying "seed".
func seed(t *testing.T, forge, name string) {
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
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, err := wr.Worktree()
	if err != nil {
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
}

// netOpen is the network every launch on a test runner names: an unconfined runner builds no
// other, and absent is none.
var netOpen = &runner.Network{Mode: runner.NetworkOpen}
