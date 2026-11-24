package stock

import (
	"io"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"os"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/client"
	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/runner/runnerd/viff/modeltest"
	"go.viff.dev/viff/pkg/runner/runnerd/workspace"
	"go.viff.dev/viff/pkg/task"
)

// The viff agent's way out, end to end: a task in its own process, a model that drives it, a
// forge on this filesystem, and the runner between them.

// commitWork is the command that commits a change in svc with the task's trailer.
func commitWork(id string) map[string]any {
	return map[string]any{"command": "cd svc && echo work > f && git add f && " +
		"git commit -qm work -m 'Viff-Task: " + id + "'"}
}

// launchPusher launches an agent on acme/svc, allowed to push its own branches, under rows and
// then git, its shell and its writes, allowed.
func launchPusher(rt client.Launcher, id string, rows ...task.Row) {
	rt.Launch(runner.Launch{ID: id, Prompt: "work", Autonomy: append(append(task.Autonomy(nil), rows...), allowing("git", "commands", "files")...),
		Agent: &runner.Agent{Name: AgentName}, Environment: giving(envOf(netOpen, []runner.Repo{{Repo: "acme/svc"}}, nil), "commands", "files")})
}

// pushGate asks before any push, and noMain refuses a push to main.
var (
	pushGate = task.Row{Name: "push", Match: &task.Match{System: "git", Verb: "push"}, Then: task.Then{Mode: task.ThenAsk}}
	noMain   = task.Row{Name: "no-main", Match: &task.Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"ref": ir.FromString("refs/heads/main")}}, Then: task.Then{Mode: task.ThenDeny}}
)

// A PUSH DENIED SENDS NOTHING, AND THE WORK IS HELD: the agent is told no, the forge never sees the
// branch, and once the task has ended its commit waits in the runner's mirror, held.
func TestADeniedPushSendsNothingAndTheWorkIsHeld(t *testing.T) {
	forge, mirrors := t.TempDir(), t.TempDir()
	seed(t, forge, "acme/svc")
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		switch res := r.Results(); len(res) {
		case 0:
			return modeltest.Use("run_bash", commitWork("t1"))
		case 1:
			return modeltest.Use("git-push", map[string]any{})
		default:
			return modeltest.Say(res[len(res)-1])
		}
	})
	rt := serveReposAt(t, forge, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL}, mirrors)
	launchPusher(rt, "t1", pushGate)
	q := pending(t, rt, "t1")
	if err := rt.Attach("t1").(resolver).Resolve(q.ID, task.Deny, "", "user:ana"); err != nil {
		t.Fatal(err)
	}
	if snap := ended(t, rt, "t1"); snap.Status != task.Done || !strings.Contains(snap.Result, "DENIED") {
		t.Fatalf("task: %+v", snap)
	}
	if at(t, forge, "refs/heads/viff/auto/t1") {
		t.Fatal("a denied push reached the forge")
	}
	m := workspace.NewMirrors(mirrors, workspace.TokenAuth(""))
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		held, err := m.Staged(filepath.Join(forge, "acme/svc"))
		if err != nil {
			t.Fatal(err)
		}
		if len(held) == 1 && held[0].Task == "t1" && held[0].To == "" {
			return
		}
	}
	t.Fatal("the denied work is not held in the runner's mirror")
}

// AN AGENT'S COMMIT AS ANOTHER IDENTITY IS REFUSED AT PUSH: signadot/viff#1 as it happened, an
// agent committing its own work with a test command's GIT_AUTHOR_* in front of it. The launch has
// no person behind it, so the identity it must carry is the runner's; the agent is told so, and
// the forge never sees the branch.
func TestAnAgentsCommitAsAnotherIdentityIsRefusedAtPush(t *testing.T) {
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		switch res := r.Results(); len(res) {
		case 0:
			return modeltest.Use("run_bash", map[string]any{"command": "cd svc && echo work > f && git add f && " +
				"GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com git commit -qm work -m 'Viff-Task: t1'"})
		case 1:
			return modeltest.Use("git-push", map[string]any{})
		default:
			return modeltest.Say(res[len(res)-1])
		}
	})
	rt := serveRepos(t, forge, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL})
	launchPusher(rt, "t1", task.Row{Name: "push", Match: &task.Match{System: "git", Verb: "push"}, Then: task.Then{Mode: task.ThenAllow}})
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "author test <test@example.com>") ||
		!strings.Contains(snap.Result, "viff runner <runner@invalid>") {
		t.Fatalf("task: %+v, want the push refused naming the author and the identity to carry", snap)
	}
	if at(t, forge, "refs/heads/viff/auto/t1") {
		t.Fatal("a commit as another identity reached the forge")
	}
}

// A BRANCH A DENY ROW REFUSES IS REFUSED WITHOUT ASKING: no one is asked to approve a push the
// policy refuses, and the agent is told which row did.
func TestABranchADenyRowRefusesIsRefusedWithoutAsking(t *testing.T) {
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		switch res := r.Results(); len(res) {
		case 0:
			return modeltest.Use("run_bash", commitWork("t1"))
		case 1:
			return modeltest.Use("git-push", map[string]any{"branch": "main"})
		default:
			return modeltest.Say(res[len(res)-1])
		}
	})
	rt := serveRepos(t, forge, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL})
	launchPusher(rt, "t1", noMain, pushGate)
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "no-main") {
		t.Fatalf("task: %+v", snap)
	}
	events, stop := rt.Attach("t1").SubscribeSince(0)
	defer stop()
	for e := range events {
		if e.Kind == task.EventPending {
			t.Fatalf("someone was asked about a push that couldn't happen: %+v", e)
		}
	}
	if got := message(t, forge); got != "seed" {
		t.Fatalf("main moved at the forge, to %q", got)
	}
}

// THE AGENT FETCHES WHAT MOVED UPSTREAM: after the forge's main moves, a fetch says so, and
// mirror/main in the checkout is where it is now.
func TestTheAgentFetchesWhatMovedUpstream(t *testing.T) {
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		switch res := r.Results(); len(res) {
		case 0:
			advance(t, filepath.Join(forge, "acme/svc"))
			return modeltest.Use("git-fetch", map[string]any{})
		case 1:
			return modeltest.Use("run_bash", map[string]any{"command": "git -C svc log -1 --format=%s mirror/main"})
		default:
			return modeltest.Say(strings.Join(res, "\n"))
		}
	})
	rt := serveRepos(t, forge, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL})
	launchPusher(rt, "t1")
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "fetched svc: mirror/main ") ||
		!strings.Contains(snap.Result, "upstream moved") {
		t.Fatalf("task: %+v", snap)
	}
}

// A task that committed nothing and pushes is told so, and nothing moves at the forge.
func TestAPushOfNothingSaysSo(t *testing.T) {
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		if res := r.Results(); len(res) > 0 {
			return modeltest.Say(res[0])
		}
		return modeltest.Use("git-push", map[string]any{})
	})
	rt := serveRepos(t, forge, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL})
	launchPusher(rt, "t1")
	if snap := ended(t, rt, "t1"); snap.Status != task.Done || !strings.Contains(snap.Result, "nothing to push") {
		t.Fatalf("task: %+v", snap)
	}
	if at(t, forge, "refs/heads/viff/auto/t1") {
		t.Fatal("a push of nothing made a branch at the forge")
	}
}

// resolver is a task's handle, answering its gates.
type resolver interface {
	Resolve(decision, choice, say, by string) error
}

// pending waits for a task's first question.
func pending(t *testing.T, rt client.Launcher, id string) task.PendingGate {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if snap, err := rt.(task.Lister).TaskSnapshot(id); err == nil && len(snap.Pending) > 0 {
			return snap.Pending[0]
		}
	}
	t.Fatalf("%s asked nothing", id)
	return task.PendingGate{}
}

// at reports whether the forge's acme/svc has ref.
func at(t *testing.T, forge, ref string) bool {
	t.Helper()
	r, err := git.PlainOpen(filepath.Join(forge, "acme/svc"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Reference(plumbing.ReferenceName(ref), true)
	return err == nil
}

// message is the subject of the forge's main.
func message(t *testing.T, forge string) string {
	t.Helper()
	r, err := git.PlainOpen(filepath.Join(forge, "acme/svc"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := r.Reference(plumbing.ReferenceName("refs/heads/main"), true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.CommitObject(h.Hash())
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(c.Message)
}

// advance puts one more commit, "upstream moved", on the origin's main.
func advance(t *testing.T, origin string) {
	t.Helper()
	work := t.TempDir()
	r, err := git.PlainClone(work, false, &git.CloneOptions{URL: origin, ReferenceName: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "upstream"), []byte("moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@invalid", When: time.Now()}
	if _, err := wt.Commit("upstream moved", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
}

// A RUNNER SHUTTING DOWN KEEPS WHAT ITS TASKS COMMITTED: a task stopped mid-run by a rollout has
// its unpushed commit held in the mirror before its directory goes, not deleted with it.
func TestShuttingDownHoldsWhatATaskCommitted(t *testing.T) {
	forge, mirrors := t.TempDir(), t.TempDir()
	seed(t, forge, "acme/svc")
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control, err := os.MkdirTemp("", "vr")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(control)
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		work := commitWork("t1")
		work["command"] = work["command"].(string) + " && touch ../committed && sleep 60"
		return modeltest.Use("run_bash", work)
	})
	h, err := Standalone(opened(Config{Repos: &Repos{Mirrors: mirrors, Forge: forge}}),
		Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL}, Tokens{},
		runnerd.Unconfined{}, []string{os.Args[0], "-test.run=^$", taskArg}, work, control, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.HTTPHandler())
	defer srv.Close()
	launchPusher(client.HTTP(srv.URL), "t1")
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if m, _ := filepath.Glob(filepath.Join(work, "task-*", "t1", "committed")); len(m) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the task never committed")
		}
	}
	h.Shutdown()
	held, err := workspace.NewMirrors(mirrors, workspace.TokenAuth("")).Staged(filepath.Join(forge, "acme/svc"))
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].Task != "t1" || !strings.Contains(held[0].Ref, "/held/") {
		t.Fatalf("after the shutdown the mirror holds %+v, want t1's commit held", held)
	}
}

// THE AGENT'S WORK, REBASED ONTO WHAT UPSTREAM DID MEANWHILE, IS PUSHED: it commits, upstream
// moves, it fetches and rebases onto mirror/main as its brief says, and the push carries its
// commit on top of upstream's, which it was never asked to sign.
func TestAPushAfterARebaseOntoUpstreamLands(t *testing.T) {
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	model := modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		switch res := r.Results(); len(res) {
		case 0:
			return modeltest.Use("run_bash", commitWork("t1"))
		case 1:
			advance(t, filepath.Join(forge, "acme/svc"))
			return modeltest.Use("git-fetch", map[string]any{})
		case 2:
			return modeltest.Use("run_bash", map[string]any{"command": "cd svc && git rebase -q mirror/main && git log --format=%s -3"})
		case 3:
			return modeltest.Use("git-push", map[string]any{})
		default:
			return modeltest.Say(res[len(res)-1])
		}
	})
	rt := serveRepos(t, forge, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL})
	launchPusher(rt, "t1")
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "pushed svc -> refs/heads/viff/auto/t1") {
		t.Fatalf("task: %+v", snap)
	}
	r, err := git.PlainOpen(filepath.Join(forge, "acme/svc"))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.Reference("refs/heads/viff/auto/t1", true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := c.Parent(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Message, "work") || strings.TrimSpace(parent.Message) != "upstream moved" {
		t.Fatalf("the branch is %q on %q, want the task's work on upstream's", c.Message, parent.Message)
	}
}

// launchWith launches an agent on acme/svc with repo's permissions, under rows and then git, its
// shell and its writes, allowed.
func launchWith(rt client.Launcher, id string, repo runner.Repo, rows ...task.Row) {
	repo.Repo = "acme/svc"
	rt.Launch(runner.Launch{ID: id, Prompt: "work", Autonomy: append(append(task.Autonomy(nil), rows...), allowing("git", "commands", "files")...),
		Agent: &runner.Agent{Name: AgentName}, Environment: giving(envOf(netOpen, []runner.Repo{repo}, nil), "commands", "files")})
}

// agentDoing is a fake model that makes the agent use each of steps in turn, then report what
// the last of them said, and records which tools it was offered.
func agentDoing(t *testing.T, offered *[]string, steps ...modeltest.Reply) *modeltest.Model {
	return modeltest.New(t, func(r modeltest.Request) modeltest.Reply {
		if offered != nil && *offered == nil {
			for _, tool := range r.Tools {
				*offered = append(*offered, tool.Function.Name)
			}
		}
		res := r.Results()
		if len(res) < len(steps) {
			return steps[len(res)]
		}
		return modeltest.Say(strings.Join(res, "\n---\n"))
	})
}

// A DENY ROW HOLDS WHEREVER THE PUSH IS MADE: the agent's push to main is refused by the row
// before anything is asked, and the branch the run creates is its own, pushed again after more
// work.
func TestADenyRowHoldsAndARunsOwnBranchIsPushedAgain(t *testing.T) {
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	model := agentDoing(t, nil,
		modeltest.Use("run_bash", commitWork("t1")),
		modeltest.Use("git-push", map[string]any{"branch": "main"}),
		modeltest.Use("git-push", map[string]any{}),
		modeltest.Use("run_bash", map[string]any{"command": "cd svc && echo more >> f && git add f && " +
			"git commit -qm more -m 'Viff-Task: t1'"}),
		modeltest.Use("git-push", map[string]any{}),
	)
	rt := serveRepos(t, forge, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL})
	launchWith(rt, "t1", runner.Repo{}, noMain)
	snap := ended(t, rt, "t1")
	parts := strings.Split(snap.Result, "\n---\n")
	if snap.Status != task.Done || len(parts) != 5 {
		t.Fatalf("task: %+v", snap)
	}
	if !strings.Contains(parts[1], "no-main") {
		t.Errorf("the push to main said %q, want it refused by the row", parts[1])
	}
	for _, i := range []int{2, 4} {
		if !strings.Contains(parts[i], "pushed svc -> refs/heads/viff/auto/t1") {
			t.Errorf("push %d said %q, want it pushed", i, parts[i])
		}
	}
	if got := message(t, forge); got != "seed" {
		t.Errorf("main at the forge is %q, want it untouched", got)
	}
}

// A PUSH TO AN ABSENT BRANCH CREATES IT, A DELETE REMOVES ONE, each as the policy allows; and on
// a forge with no pull requests the run is offered no pull request tools.
func TestAPushCreatesAndADeleteRemoves(t *testing.T) {
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	old := filepath.Join(forge, "acme/svc")
	r, err := git.PlainOpen(old)
	if err != nil {
		t.Fatal(err)
	}
	main, err := r.Reference("refs/heads/main", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Storer.SetReference(plumbing.NewHashReference("refs/heads/viff/auto/t0", main.Hash())); err != nil {
		t.Fatal(err)
	}
	var offered []string
	model := agentDoing(t, &offered,
		modeltest.Use("run_bash", commitWork("t1")),
		modeltest.Use("git-push", map[string]any{"branch": "viff/train"}),
		modeltest.Use("git-push", map[string]any{"branch": "viff/auto/t0", "delete": true}),
	)
	rt := serveRepos(t, forge, Secrets{PortkeyAPIKey: "k", PortkeyBaseURL: model.URL})
	launchWith(rt, "t1", runner.Repo{})
	snap := ended(t, rt, "t1")
	parts := strings.Split(snap.Result, "\n---\n")
	if snap.Status != task.Done || len(parts) != 3 {
		t.Fatalf("task: %+v", snap)
	}
	if !strings.Contains(parts[1], "pushed svc -> refs/heads/viff/train") {
		t.Errorf("the update said %q, want viff/train created", parts[1])
	}
	if !strings.Contains(parts[2], "deleted svc refs/heads/viff/auto/t0") {
		t.Errorf("the delete said %q", parts[2])
	}
	if at(t, forge, "refs/heads/viff/auto/t0") || !at(t, forge, "refs/heads/viff/train") {
		t.Error("the forge's branches aren't what the run did")
	}
	if slices.Contains(offered, createPRTool) || slices.Contains(offered, updatePRTool) || !slices.Contains(offered, pushTool) {
		t.Errorf("offered %v: want push, and no pull request tools on a forge without them", offered)
	}
}
