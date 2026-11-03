package stock

import (
	"cmp"
	"context"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/verse/modeltest"
	"github.com/signadot/verse/systems"
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

// opened is c with each system each doer holds and its section writes no ceiling for under an
// open one (systems.Open), its `git` ceiling within its forge: what a test that is not about
// ceilings configures, a ceiling written nowhere being closed. A c offering nothing is scripts
// and the verse agent (every), as a test runner's default.
func opened(c Config) Config {
	if len(c.Agents) == 0 && c.Script == nil {
		c = every(c, nil)
	}
	c.Agents = maps.Clone(c.Agents)
	if c.Script != nil {
		s := *c.Script
		s.Systems = openedFor(c, ScriptDoer, s.Systems)
		c.Script = &s
	}
	for name, a := range c.Agents {
		a.Systems = openedFor(c, name, a.Systems)
		c.Agents[name] = a
	}
	return c
}

// openedFor is one doer's ceilings, opened as opened says.
func openedFor(c Config, doer string, sys map[string]System) map[string]System {
	held := maps.Clone(sys)
	if held == nil {
		held = map[string]System{}
	}
	for _, name := range append(c.performs(), innerFor(doer)...) {
		s := held[name]
		if len(s.Ceiling) == 0 {
			s.Ceiling = systems.Open()
			if name == "git" {
				host, _ := c.Repos.forge()
				s.Ceiling[0].Attributes = map[string]*ir.Node{"repo": systems.Regexp("^" + regexp.QuoteMeta(strings.ToLower(host)) + "/")}
			}
		}
		held[name] = s
	}
	return held
}

// every is c running scripts and the verse agent, each under the ceilings sys, as far as each is
// stopped on them: a test's runner whose doers share their ceilings, as every runner's did before
// each doer had its own.
func every(c Config, sys map[string]System) Config {
	c.Script = &Script{Systems: forDoer(ScriptDoer, sys)}
	c.Agents = map[string]Agent{AgentName: {Systems: forDoer(AgentName, sys)}}
	return c
}

// forDoer is sys without the inner systems doer is not stopped on.
func forDoer(doer string, sys map[string]System) map[string]System {
	out := map[string]System{}
	for name, s := range sys {
		if doers, isInner := inner[name]; isInner && !slices.Contains(doers, doer) {
			continue
		}
		out[name] = s
	}
	return out
}

// serveConfigured serves the verse runner configured by c, with the secrets and tokens given:
// each system it holds and writes no ceiling for under an open one (opened).
func serveConfigured(t *testing.T, c Config, sec Secrets, tokens Tokens) client.Launcher {
	t.Helper()
	return serveRaising(t, c, sec, tokens, nil)
}

// serveRaising is serveConfigured for a runner that carries its tasks' requests to a verse with
// toVerse.
func serveRaising(t *testing.T, c Config, sec Secrets, tokens Tokens, toVerse delivery.Verse) client.Launcher {
	t.Helper()
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control, err := os.MkdirTemp("", "vr")
	if err != nil {
		t.Fatal(err)
	}
	h, err := Standalone(opened(c), sec, tokens, runnerd.Unconfined{},
		[]string{os.Args[0], "-test.run=^$", taskArg}, work, control, toVerse, io.Discard)
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
	rt.Launch(runner.Launch{ID: "t1", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, Script: "#!/bin/sh\nset -e\nls\ncd svc\ngit branch --show-current\ngit remote\ncat README\nfor f in /proc/$$/fd/*; do readlink $f; done | grep -c socket || true\nfor fd in 3 4 5; do [ ! -e /proc/$$/fd/$fd ] || echo fd$fd; done", Environment: envOf(netOpen, []runner.Repo{{Repo: "acme/svc"}}, nil)})
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
	rt.Launch(runner.Launch{ID: "t1", Prompt: "do the work", Autonomy: append(task.Autonomy{{Name: "push", Match: &task.Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"ref": systems.Glob("refs/heads/verse/auto/*")}}, Then: task.Then{Mode: task.ThenAsk}}},
		allowing("git", "commands", "files")...),
		Agent: &runner.Agent{Name: AgentName}, Environment: giving(envOf(netOpen, []runner.Repo{{Repo: "acme/svc"}}, nil), "commands", "files")})

	var q task.PendingGate
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline) && q.ID == ""; time.Sleep(20 * time.Millisecond) {
		if snap, err := rt.(task.Lister).TaskSnapshot("t1"); err == nil && len(snap.Pending) > 0 {
			q = snap.Pending[0]
		}
	}
	// The runner's two facts are on the question too: the branch is not at the forge yet.
	want := map[string]string{"repo": "github.com/acme/svc", "from": "main", "ref": "refs/heads/verse/auto/t1", "delete": "false", "force": "false",
		"exists": "false", "own": "false"}
	if q.Step != task.StepGitPush || q.Gate != "push" || !q.Fields.DeepEqual(systems.FieldsDoc(q.Step, want)) {
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
	if !strings.Contains(brief, "svc/ — github.com/acme/svc at main") || !strings.Contains(brief, "Verse-Task: t1") ||
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
	if _, ok := (Config{Script: &Script{}}).Describe(nil).Offers[0].Systems["git"]; ok {
		t.Fatal("a runner with no mirrors offers repos")
	}
	rt, _ := serve(t, Secrets{}, runnerd.Unconfined{})
	h := rt.Launch(runner.Launch{ID: "t1", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAsk}}}, Script: "#!/bin/sh\ntrue", Environment: envOf(netOpen, []runner.Repo{{Repo: "acme/svc"}}, nil)})
	frames, stop := h.Subscribe()
	defer stop()
	f := <-frames
	if f.Kind != task.EventStatus || task.Status(f.Text) != task.Failed || !strings.Contains(f.Err, "holds no `git`") {
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

	h := rt.Launch(runner.Launch{ID: "t1", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, Script: "#!/bin/sh\necho ran", Environment: envOf(netOpen, []runner.Repo{{Repo: "acme/svc"}}, nil)})
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
	for _, l := range []runner.Launch{{Agent: &runner.Agent{Name: AgentName}}, {Script: "true"}} {
		// Attach closes what it is given, as a task's process owns fd 5: so it is given a copy.
		fd, err := syscall.Dup(int(r.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		d := New(opened(Config{Repos: &Repos{Mirrors: "unused"}}), Secrets{}, t.TempDir(), io.Discard)
		d.deliveryFD = uintptr(fd)
		l.ID, l.Environment = "t1", envOf(nil, []runner.Repo{{Repo: "acme/svc"}}, nil)
		l.Autonomy = task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}
		p := d.Build(l, task.NewRegistry())
		if _, err := p.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "delivery channel") {
			t.Fatalf("%s ran without its channel: %v", cmp.Or(l.Who(), "agent"), err)
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

// envOf is the environment a test's launch works in: the network n (nil: no network block, which
// is none), the repositories repos, each fetched and pushed anywhere (what may leave is the
// autonomy's to decide), and the trackers of trackers.
func envOf(n *runner.Network, repos []runner.Repo, trackers []runner.Tracker) systems.Environment {
	e := systems.Environment{Systems: systems.Systems{}}
	if n != nil {
		b := systems.Block{Attributes: map[string]string{"mode": n.Mode}}
		for _, entry := range n.Allow {
			host, port, err := net.SplitHostPort(entry)
			if err != nil {
				host, port = entry, ""
			}
			if b.Verbs == nil {
				b.Verbs = map[string][]systems.Shape{}
			}
			shape := systems.Shape{"host": ir.FromString(host)}
			if port != "" {
				// A port is a number on the step, so a shape writes it as one.
				n, err := strconv.ParseInt(port, 10, 64)
				if err != nil {
					panic(err)
				}
				shape["port"] = ir.FromInt(n)
			}
			b.Verbs["tcp-client"] = append(b.Verbs["tcp-client"], shape)
		}
		e.Systems["network"] = []systems.Block{b}
	}
	for _, r := range repos {
		attrs := map[string]string{"repo": r.Repo}
		if r.From != "" {
			attrs["from"] = r.From
		}
		e.Systems["git"] = append(e.Systems["git"], systems.Block{Attributes: attrs,
			Verbs: map[string][]systems.Shape{"fetch": {{}}, "push": {{}}}})
	}
	for _, tr := range trackers {
		e.Systems["git-issue"] = append(e.Systems["git-issue"], systems.Block{Attributes: map[string]string{"repo": tr.Repo},
			Verbs: everyVerb("git-issue")})
	}
	return e
}

// giving is e with a block for each of names giving every verb of it: the systems of one block
// (files, commands, agent, question, slack, …), for a task whose rows decide them.
func giving(e systems.Environment, names ...string) systems.Environment {
	out := systems.Environment{Vars: e.Vars, Systems: systems.Systems{}}
	for k, v := range e.Systems {
		out.Systems[k] = v
	}
	for _, name := range names {
		out.Systems[name] = []systems.Block{{Verbs: everyVerb(name)}}
	}
	return out
}

// everyVerb is every verb of a system, each with one shape that names nothing.
func everyVerb(system string) map[string][]systems.Shape {
	d, _ := systems.Get(system)
	out := map[string][]systems.Shape{}
	for _, v := range d.Verbs {
		out[v.Name] = []systems.Shape{{}}
	}
	return out
}

// withVars is e with the variables vars.
func withVars(e systems.Environment, vars map[string]string) systems.Environment {
	e.Vars = vars
	return e
}
