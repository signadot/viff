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

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/systems/systemstest"
	"github.com/signadot/verse/task"
)

// pushing is a launch's policy that gives the task git and lets every call of it go.
var pushing = task.Autonomy{{Match: &task.Match{System: "git"}, Then: task.Then{Mode: task.ThenAllow}}, {Then: task.Then{Mode: task.ThenAllow}}}

// gitLaunch is a launch of the verse agent that checks out repo, with a fetch and a push to any
// branch of it, under policy.
func gitLaunch(id string, repo runner.Repo, policy task.Autonomy) *runner.Launch {
	attrs := map[string]string{"repo": repo.Repo}
	if repo.From != "" {
		attrs["from"] = repo.From
	}
	return &runner.Launch{ID: id, Prompt: "p", Agent: &runner.Agent{Name: "verse"}, Autonomy: policy,
		Environment: systems.Environment{Systems: systems.Systems{"git": {{Attributes: attrs,
			Verbs: map[string][]systems.Shape{"fetch": {{}}, "push": {{"ref": "refs/heads/**"}}}}}}}}
}

// within is a launch whose environment gives every verb of each named system: in a block per repo
// for a system a repo names its blocks by, one block otherwise, under an autonomy that allows
// them all. What a call may do is then the ceiling's, or a test's own autonomy's.
func within(repos []string, names ...string) *runner.Launch {
	env := systems.Environment{Systems: systems.Systems{}}
	for _, name := range names {
		d, _ := systems.Get(name)
		verbs := map[string][]systems.Shape{}
		for _, v := range d.Verbs {
			verbs[v.Name] = []systems.Shape{{}}
		}
		if u, ok := d.Unique(); ok {
			for _, r := range repos {
				env.Systems[name] = append(env.Systems[name], systems.Block{Attributes: map[string]string{u.Name: r}, Verbs: verbs})
			}
			continue
		}
		env.Systems[name] = []systems.Block{{Verbs: verbs}}
	}
	return &runner.Launch{ID: "t", Prompt: "p", Environment: env, Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}}
}

// A push the task asked for before its process died is finished before anything decides what
// did not leave. Settle closes the channel and then waits for the request in flight; without
// that, salvage would read a tree an approved push was staging from, hold the work under `held`
// as if nobody had authorized it, and the next sweep would send it twice. (Scott's, from #16.)
func TestSettleWaitsForThePushInFlight(t *testing.T) {
	ctx := context.Background()
	forge := t.TempDir()
	origin := initRepo(t, forge, "acme/svc")
	l := gitLaunch("s1", runner.Repo{Repo: "acme/svc"}, pushing)
	repos, err := workspace.FromLaunch(l.Repos(), forge)
	if err != nil {
		t.Fatal(err)
	}
	m := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
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
	ch, err := Open(l.ID, ws, m, cred, Services{}, systemstest.Open(l.Environment), l, io.Discard)
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
