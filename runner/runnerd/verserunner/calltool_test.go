package verserunner

import (
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

var (
	callToolOnce sync.Once
	callToolDir  string // where main's verse-call-tool is built, once for the package
	callToolErr  error
)

// withCallTool builds verse-call-tool and puts it on PATH, where a runner finds it and its tasks'
// processes do too, as an image's runner finds the one installed beside it.
func withCallTool(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH to build verse-call-tool with")
	}
	callToolOnce.Do(func() {
		callToolDir, callToolErr = os.MkdirTemp("", "vct")
		if callToolErr != nil {
			return
		}
		out, err := exec.Command("go", "build", "-o", filepath.Join(callToolDir, CallToolBinary),
			"github.com/signadot/verse/cmd/verse-call-tool").CombinedOutput()
		if err != nil {
			callToolErr = &buildError{err, out}
		}
	})
	if callToolErr != nil {
		t.Fatal(callToolErr)
	}
	t.Setenv("PATH", callToolDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type buildError struct {
	err error
	out []byte
}

func (e *buildError) Error() string {
	return "building verse-call-tool: " + e.err.Error() + "\n" + string(e.out)
}

// commitScript commits work on the task's checkout of svc, as a script's own git does.
const commitScript = "cd svc\necho work > f\ngit add f\n" +
	"git -c user.name=a -c user.email=a@x commit -qm work -m 'Verse-Task: t1'\n"

// launchScript launches a script on acme/svc, allowed to create the task's own branches, under
// the rows given and `allow` for the rest.
func launchScript(rt client.Launcher, id, script string, rows ...task.Row) {
	policy := append(rows, allowing("git", "git-issue", "question")...)
	rt.Launch(runner.Launch{ID: id, Policy: policy, Doer: runner.Doer{
		Script: script,
		With:   runner.With{Network: netOpen, Git: []runner.Repo{{Repo: "acme/svc"}}, GitIssue: []runner.Tracker{{Repo: "acme/svc"}}},
	}})
}

// A SCRIPT'S PUSH IS ASKED ABOUT AS THE AGENT'S IS, AND LANDS: the gate on git.push asks, the
// answer lets it go, and the branch is at the forge.
func TestAScriptsPushIsAskedAboutAndLands(t *testing.T) {
	withCallTool(t)
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveRepos(t, forge, Secrets{})
	launchScript(rt, "t1", "#!/bin/sh\nset -e\n"+commitScript+"verse-call-tool git-push", pushGate)
	q := pending(t, rt, "t1")
	if q.Step != task.StepGitPush {
		t.Fatalf("asked about %+v, want git.push", q)
	}
	if err := rt.Attach("t1").(resolver).Resolve(q.ID, task.Proceed, "", "user:ana"); err != nil {
		t.Fatal(err)
	}
	if snap := ended(t, rt, "t1"); snap.Status != task.Done || !strings.Contains(snap.Result, "verse/auto/t1") {
		t.Fatalf("task: %+v", snap)
	}
	if !at(t, forge, "refs/heads/verse/auto/t1") {
		t.Fatal("the approved push didn't reach the forge")
	}
}

// A LAUNCH IS HELD TO THE RUNNER'S CEILING, at the launch and at each act: a policy that would
// allow what the operator's ceiling refuses is refused when it is launched, naming the ceiling's
// row, and a policy that allows everything still has each push the ceiling refuses refused where
// the runner performs it, while the rest lands.
func TestAScriptIsHeldToTheRunnersCeiling(t *testing.T) {
	withCallTool(t)
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	noMain := task.Row{Name: "no-main", Match: &task.Match{Verb: "push", Params: map[string]string{"ref": "refs/heads/main"}}, Then: task.Then{Mode: task.ThenDeny}}
	rt := serveConfigured(t, Config{Repos: &Repos{Mirrors: t.TempDir(), Forge: forge},
		Systems: map[string]System{"git": {Ceiling: task.Policy{noMain}}}}, Secrets{}, Tokens{})

	toMain := task.Row{Match: &task.Match{System: "git", Verb: "push", Params: map[string]string{"ref": "refs/heads/main"}}, Then: task.Then{Mode: task.ThenAllow}}
	l := rt.Launch(runner.Launch{ID: "t0", Policy: task.Policy{toMain, {Then: task.Then{Mode: task.ThenAllow}}}, Doer: runner.Doer{
		Script: "true", With: runner.With{Network: netOpen, Git: []runner.Repo{{Repo: "acme/svc"}}}}})
	if !strings.Contains(l.Err(), "no-main") {
		t.Fatalf("a launch allowing a push to main past the ceiling: %q, want it refused naming the ceiling's row", l.Err())
	}

	launchScript(rt, "t1", "#!/bin/sh\n"+commitScript+
		"verse-call-tool git-push -branch main\necho \"main=$?\"\nverse-call-tool git-push\necho \"own=$?\"")
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "main=3") || !strings.Contains(snap.Result, "own=0") ||
		!strings.Contains(snap.Result, "refused by the runner's ceiling, its row no-main") {
		t.Fatalf("task: %+v", snap)
	}
	if !at(t, forge, "refs/heads/verse/auto/t1") {
		t.Fatal("the push the ceiling allows didn't reach the forge")
	}
	if strings.Contains(message(t, forge), "work") {
		t.Fatal("the push to main the ceiling refuses reached the forge")
	}
}

// NOBODY IS ASKED ABOUT WHAT THE CEILING REFUSES: a push the launch asks about and the runner's
// ceiling denies is refused at the step, in the ceiling's words, before it is put to anyone.
func TestAnAskTheCeilingRefusesIsNeverPut(t *testing.T) {
	withCallTool(t)
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	ceiling := task.Row{Name: "no-main", Match: &task.Match{Verb: "push", Params: map[string]string{"ref": "refs/heads/main"}}, Then: task.Then{Mode: task.ThenDeny}}
	rt := serveConfigured(t, Config{Repos: &Repos{Mirrors: t.TempDir(), Forge: forge},
		Systems: map[string]System{"git": {Ceiling: task.Policy{ceiling}}}}, Secrets{}, Tokens{})
	launchScript(rt, "t1", "#!/bin/sh\n"+commitScript+"verse-call-tool git-push -branch main\necho \"main=$?\"", pushGate)
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "main=3") ||
		!strings.Contains(snap.Result, "refused by the runner's ceiling, its row no-main") {
		t.Fatalf("task: %+v", snap)
	}
	events, stop := rt.Attach("t1").SubscribeSince(0)
	defer stop()
	for e := range events {
		if e.Kind == task.EventPending {
			t.Fatalf("someone was asked about a push the ceiling refuses: %+v", e)
		}
	}
}

// A DENIED CALL IS AN ANSWER, NOT THE END: the script reads exit 3 and the denial, goes on, and
// nothing reaches the forge.
func TestAScriptReadsADenialAndGoesOn(t *testing.T) {
	withCallTool(t)
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveRepos(t, forge, Secrets{})
	launchScript(rt, "t1", "#!/bin/sh\n"+commitScript+"verse-call-tool git-push\necho \"exit=$?\"", pushGate)
	q := pending(t, rt, "t1")
	if err := rt.Attach("t1").(resolver).Resolve(q.ID, task.Deny, "", "user:ana"); err != nil {
		t.Fatal(err)
	}
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "exit=3") ||
		!strings.Contains(snap.Result, "denied by user:ana") {
		t.Fatalf("task: %+v", snap)
	}
	if at(t, forge, "refs/heads/verse/auto/t1") {
		t.Fatal("a denied push reached the forge")
	}
}

// A BRANCH THE LAUNCH DOESN'T ALLOW IS REFUSED WITHOUT ASKING, in the launch's words, and a call
// that fails is exit 1: in neither case does the script's run end.
func TestAScriptsRefusedAndFailedCallsAreAnswers(t *testing.T) {
	withCallTool(t)
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveRepos(t, forge, Secrets{})
	launchScript(rt, "t1", "#!/bin/sh\n"+commitScript+
		"verse-call-tool git-push -branch main\necho \"push=$?\"\n"+
		"verse-call-tool question-ask -question q -within never 2>&1\necho \"ask=$?\"", noMain, pushGate)
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "push=3") ||
		!strings.Contains(snap.Result, "no-main") || !strings.Contains(snap.Result, "ask=1") {
		t.Fatalf("task: %+v", snap)
	}
	events, stop := rt.Attach("t1").SubscribeSince(0)
	defer stop()
	for e := range events {
		if e.Kind == task.EventPending {
			t.Fatalf("someone was asked about a push that couldn't happen: %+v", e)
		}
	}
}

// A SCRIPT ASKS, and the answer is what the call prints.
func TestAScriptAsksAndReadsTheAnswer(t *testing.T) {
	withCallTool(t)
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveRepos(t, forge, Secrets{})
	launchScript(rt, "t1", "#!/bin/sh\nverse-call-tool question-ask -question 'ship it?'")
	q := pending(t, rt, "t1")
	if err := rt.Attach("t1").(resolver).Resolve(q.ID, task.Reply, "ship it", "user:ana"); err != nil {
		t.Fatal(err)
	}
	if snap := ended(t, rt, "t1"); snap.Status != task.Done || !strings.Contains(snap.Result, "ship it") {
		t.Fatalf("task: %+v", snap)
	}
}

// A CALLER THAT GIVES UP WITHDRAWS ITS QUESTION: the docket is cleared, the script goes on, and
// nothing is pushed.
func TestAScriptThatStopsWaitingWithdrawsItsQuestion(t *testing.T) {
	withCallTool(t)
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("no timeout(1) to give up with")
	}
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveRepos(t, forge, Secrets{})
	launchScript(rt, "t1", "#!/bin/sh\n"+commitScript+"timeout 2 verse-call-tool git-push\necho gave up", pushGate)
	pending(t, rt, "t1")
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "gave up") || len(snap.Pending) != 0 {
		t.Fatalf("task: %+v", snap)
	}
	if at(t, forge, "refs/heads/verse/auto/t1") {
		t.Fatal("a withdrawn push reached the forge")
	}
}

// A SCRIPT'S TOOLS ARE ITS REPOS' AND ITS TRACKERS', AS FAR AS ITS POLICY PROVIDES THEM: without
// repos it has `question-ask` alone, with a repo `git-fetch` and `git-push` besides, and the
// repository's own tracker where the launch names it under `with.git-issue`. A system no row
// names, or one a row withholds, has no tool; with no policy there is none.
func TestAScriptsToolsAreItsReposAndAsk(t *testing.T) {
	withCallTool(t)
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveRepos(t, forge, Secrets{})
	list := "#!/bin/sh\nverse-call-tool | grep -x '[a-z-]*' | sort | tr '\\n' ' '"
	tracked := runner.With{Network: netOpen, Git: []runner.Repo{{Repo: "acme/svc"}}, GitIssue: []runner.Tracker{{Repo: "acme/svc"}}}
	rt.Launch(runner.Launch{ID: "bare", Policy: allowing("question"), Doer: runner.Doer{Script: list, With: runner.With{Network: netOpen}}})
	rt.Launch(runner.Launch{ID: "ro", Policy: allowing("git", "question"), Doer: runner.Doer{Script: list,
		With: runner.With{Network: netOpen, Git: []runner.Repo{{Repo: "acme/svc"}}}}})
	rt.Launch(runner.Launch{ID: "tracked", Policy: allowing("git", "git-issue", "question"), Doer: runner.Doer{Script: list, With: tracked}})
	rt.Launch(runner.Launch{ID: "withheld", Policy: append(task.Policy{
		{Match: &task.Match{System: "git", Verb: "push"}, Then: task.Then{Mode: task.ThenWithhold}},
		{Match: &task.Match{System: "git-issue"}, Then: task.Then{Mode: task.ThenWithhold}}},
		allowing("git", "question")...), Doer: runner.Doer{Script: list, With: tracked}})
	// A system no row names is not the task's, whatever it works with: no `git` row, no git-push.
	rt.Launch(runner.Launch{ID: "unnamed", Policy: allowing("question"), Doer: runner.Doer{Script: list, With: tracked}})
	rt.Launch(runner.Launch{ID: "none", Doer: runner.Doer{Script: list, With: tracked}})
	issues := "git-issue-close git-issue-comment git-issue-create git-issue-pull git-issue-push "
	for id, want := range map[string]string{"bare": "question-ask", "ro": "git-fetch git-push question-ask", "tracked": "git-fetch " + issues + "git-push question-ask",
		"withheld": "git-fetch question-ask", "unnamed": "question-ask", "none": ""} {
		if snap := ended(t, rt, id); snap.Status != task.Done || strings.TrimSpace(snap.Result) != want {
			t.Errorf("%s: %+v, want the tools %q", id, snap, want)
		}
	}
}

// UNDER BWRAP, with no network, a script still reaches its tools: the binary is bound read only
// where it is, and the socket is in the sandbox's own /tmp.
func TestAConfinedScriptReachesItsTools(t *testing.T) {
	withCallTool(t)
	sb, err := runnerd.Bwrap()
	if err != nil {
		t.Skipf("bwrap can't run here: %v", err)
	}
	rt, _ := serveNetwork(t, sb)
	rt.Launch(runner.Launch{ID: "t1", Policy: allowing("question"), Doer: runner.Doer{
		Script: "#!/bin/sh\nverse-call-tool question-ask -question hi; echo \"$VERSE_TOOLS\"", With: runner.With{Network: netOpen}}})
	q := pending(t, rt, "t1")
	if err := rt.Attach("t1").(resolver).Resolve(q.ID, task.Reply, "hello back", "user:ana"); err != nil {
		t.Fatal(err)
	}
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "hello back") || !strings.Contains(snap.Result, "/tmp/") {
		t.Fatalf("task: %+v", snap)
	}
}

// THE RUNNER'S BINARY IS THE TASK'S, whatever the task's PATH: a runner whose shell passthrough
// leaves PATH out still gives its scripts the binary it found.
func TestAScriptGetsTheRunnersBinaryWithoutPATH(t *testing.T) {
	withCallTool(t)
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control, err := os.MkdirTemp("", "vr")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(control)
	h, err := Standalone(Config{ShellEnv: []string{"HOME"}}, Secrets{}, Tokens{}, runnerd.Unconfined{},
		[]string{os.Args[0], "-test.run=^$", taskArg}, work, control, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.HTTPHandler())
	defer func() { srv.Close(); h.Shutdown() }()
	rt := client.HTTP(srv.URL)
	rt.Launch(runner.Launch{ID: "t1", Policy: allowing("question"), Doer: runner.Doer{
		Script: "#!/bin/sh\nverse-call-tool question-ask -question hi", With: runner.With{Network: netOpen}}})
	q := pending(t, rt, "t1")
	if err := rt.Attach("t1").(resolver).Resolve(q.ID, task.Reply, "found", "user:ana"); err != nil {
		t.Fatal(err)
	}
	if snap := ended(t, rt, "t1"); snap.Status != task.Done || !strings.Contains(snap.Result, "found") {
		t.Fatalf("task: %+v", snap)
	}
}

// WHAT THE APPROVER SAID GOES WITH THE CALL'S ANSWER: the script reads it after what the push
// did.
func TestAScriptReadsWhatItsApproverSaid(t *testing.T) {
	withCallTool(t)
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveRepos(t, forge, Secrets{})
	launchScript(rt, "t1", "#!/bin/sh\nset -e\n"+commitScript+"verse-call-tool git-push", pushGate)
	q := pending(t, rt, "t1")
	if err := rt.Attach("t1").(resolver).Resolve(q.ID, task.Proceed, "next time, a draft", "user:ana"); err != nil {
		t.Fatal(err)
	}
	snap := ended(t, rt, "t1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "verse/auto/t1") || !strings.Contains(snap.Result, "next time, a draft") {
		t.Fatalf("task: %+v", snap)
	}
}

// AN OPERATOR'S WORDS TO A RUNNING SCRIPT ARE REFUSED WHEN SAID: it has no turn for them, and
// the call it is waiting on stands.
func TestASayToARunningScriptIsRefused(t *testing.T) {
	withCallTool(t)
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveRepos(t, forge, Secrets{})
	launchScript(rt, "t1", "#!/bin/sh\nverse-call-tool question-ask -question hi")
	q := pending(t, rt, "t1")
	if err := rt.Attach("t1").Steer("do something else", "user:bo", ""); !errors.Is(err, task.ErrUnsteerable) {
		t.Fatalf("the say = %v, want ErrUnsteerable", err)
	}
	if err := rt.Attach("t1").(resolver).Resolve(q.ID, task.Reply, "hello", "user:ana"); err != nil {
		t.Fatalf("the question didn't stand: %v", err)
	}
	if snap := ended(t, rt, "t1"); snap.Status != task.Done || !strings.Contains(snap.Result, "hello") {
		t.Fatalf("task: %+v", snap)
	}
}

// Scott's probe on #23: an operator's say while a script's push is asked about is refused, the
// question stands, and once approved the push lands.
func TestASayDuringAScriptsPushLeavesItsQuestionStanding(t *testing.T) {
	withCallTool(t)
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	rt := serveRepos(t, forge, Secrets{})
	launchScript(rt, "t1", "#!/bin/sh\nset -e\n"+commitScript+"verse-call-tool git-push", pushGate)
	q := pending(t, rt, "t1")
	if err := rt.Attach("t1").Steer("also bump the version", "user:ana", ""); !errors.Is(err, task.ErrUnsteerable) {
		t.Fatalf("the say = %v, want ErrUnsteerable", err)
	}
	if again := pending(t, rt, "t1"); again.ID != q.ID {
		t.Fatalf("the question is %+v, want %s still standing", again, q.ID)
	}
	if err := rt.Attach("t1").(resolver).Resolve(q.ID, task.Proceed, "", "user:ana"); err != nil {
		t.Fatal(err)
	}
	if snap := ended(t, rt, "t1"); snap.Status != task.Done || !at(t, forge, "refs/heads/verse/auto/t1") {
		t.Fatalf("task: %+v; the approved push didn't land", snap)
	}
}

// allowing is a policy that gives a task every verb of the systems named and allows each call
// of them: what a test's task is given when what it is asked is not the test's point.
func allowing(systems ...string) task.Policy {
	var p task.Policy
	for _, s := range systems {
		p = append(p, task.Row{Match: &task.Match{System: s}, Then: task.Then{Mode: task.ThenAllow}})
	}
	return append(p, task.Row{Then: task.Then{Mode: task.ThenAllow}})
}
