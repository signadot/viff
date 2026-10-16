package verserunner

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd/calltool"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/verserunner/agent"
	"github.com/signadot/verse/task"
)

// The binary a program calls, built once for the package's tests: what the runner copies into
// each workspace is the real thing, so a program's `verse-call-tool ask` here is the one a
// deployment runs.
var (
	callToolOnce sync.Once
	callToolPath string
	callToolErr  error
)

func callToolBinary(t *testing.T) string {
	t.Helper()
	callToolOnce.Do(func() {
		dir, err := os.MkdirTemp("", "vct")
		if err != nil {
			callToolErr = err
			return
		}
		callToolPath = filepath.Join(dir, CallToolBinary)
		cmd := exec.Command("go", "build", "-o", callToolPath, "github.com/signadot/verse/cmd/verse-call-tool")
		if out, err := cmd.CombinedOutput(); err != nil {
			callToolErr = err
			callToolPath = string(out)
		}
	})
	if callToolErr != nil {
		t.Fatalf("building %s: %v\n%s", CallToolBinary, callToolErr, callToolPath)
	}
	return callToolPath
}

// shProgram is an environment whose agent is a shell script, with the floor named.
func shProgram(script string, floor ...string) *Config {
	return &Config{
		Default:      "e",
		Environments: []Environment{{Name: "e", Driver: "sh", Env: []string{"PATH"}, Tools: floor}},
		Agents:       map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", script}}},
	}
}

// drive runs a launch to its end, answering each question it poses with answer, and returns the
// terminal status and the tools the questions were about.
func drive(t *testing.T, r *Runner, l task.Launch, answer func(f task.Event) (choice, say string)) (task.Status, []string, task.Handle) {
	t.Helper()
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)
	h := client.HTTP(srv.URL).Spawn(l)
	frames, cancel := h.Subscribe()
	defer cancel()
	res := h.(interface {
		Resolve(decision, choice, say, by string) error
	})
	var asked []string
	for f := range frames {
		if f.Kind == task.EventPending && answer != nil {
			asked = append(asked, f.Tool)
			choice, say := answer(f)
			if err := res.Resolve(f.ID, choice, say, "human:test"); err != nil {
				t.Fatalf("answering %s: %v", f.Tool, err)
			}
		}
		if f.Kind == task.EventStatus && task.Status(f.Text).Terminal() {
			return task.Status(f.Text), asked, h
		}
	}
	t.Fatal("the read plane closed without a terminal status")
	return "", nil, h
}

// A program in the workspace asks the person through its shell, and reads what they said.
//
// The whole arrangement, end to end: the runner copies the binary into the workspace, the task
// process serves the socket, the program's environment names both, the call becomes an action
// of the loop, the question reaches the read plane and is answered over the control plane, and
// the answer is what the program prints.
func TestAProgramAsksThroughItsShell(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, shProgram(`ans=$(verse-call-tool ask -question "ship it?" -options yes -options no) || exit 1; echo "they said: $ans"`))
	status, asked, h := drive(t, r, task.Launch{
		ID: "c1", Repos: ro(origin), Prompt: "ask them", Vector: &task.Vector{Default: task.Auto},
	}, func(f task.Event) (string, string) { return "yes", "" })
	if status != task.Done {
		t.Fatalf("task ended %s (%s)", status, h.Err())
	}
	if len(asked) != 1 || asked[0] != "ask" {
		t.Fatalf("questions posed: %v, want one ask", asked)
	}
	if got := h.Result(); got != "they said: human:test chose yes" {
		t.Fatalf("result = %q", got)
	}
}

// A gate's refusal of a call is an answer the program reads — exit 3, the reason on stdout — and
// not the end of its run. Nothing the refused call would have done happens.
func TestARefusedCallIsExit3AndTheRunGoesOn(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, shProgram(`echo x > svc/x.txt; out=$(verse-call-tool push); echo "exit=$? $out"`))
	status, asked, h := drive(t, r, task.Launch{
		ID: "c2", Repos: rw(origin), Prompt: "push it",
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "delivery", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.Push}},
		}}},
	}, func(f task.Event) (string, string) { return "deny", "not now" })
	if status != task.Done {
		t.Fatalf("task ended %s (%s)", status, h.Err())
	}
	if len(asked) != 1 || asked[0] != delivery.PushToolName {
		t.Fatalf("questions posed: %v, want one push", asked)
	}
	got := h.Result()
	if !strings.HasPrefix(got, "exit=3 refused:") || strings.Count(got, "not now") != 1 {
		t.Fatalf("result = %q", got)
	}
	if commitOn(origin, "refs/heads/verse/auto/c2") != "" {
		t.Fatal("the refused push happened")
	}
}

// What a decider says while approving a call goes to the program with the call's answer, and a
// program that runs once ends done: the words are not kept for a turn it has not got.
func TestAnApproversWordsGoWithTheAnswer(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, shProgram(`out=$(verse-call-tool push); echo "exit=$? $out"`))
	status, asked, h := drive(t, r, task.Launch{
		ID: "c10", Repos: rw(origin), Prompt: "push it",
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "delivery", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.Push}},
		}}},
	}, func(f task.Event) (string, string) { return "proceed", "rebase next time" })
	if status != task.Done {
		t.Fatalf("task ended %s (%s)", status, h.Err())
	}
	if len(asked) != 1 || !strings.Contains(h.Result(), "rebase next time") {
		t.Fatalf("asked %v, result %q", asked, h.Result())
	}
}

// A call whose caller has gone is not proposed: nobody is asked about it.
func TestACallWhoseCallerLeftIsNotProposed(t *testing.T) {
	d := &commandDriver{calls: make(chan *toolCall, 2), running: &program{done: make(chan struct{})}}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	d.calls <- &toolCall{ctx: gone, tool: agent.Tool{Name: "push"}}
	d.calls <- &toolCall{ctx: context.Background(), tool: agent.Tool{Name: "ask"}}
	tu, more, err := d.await(context.Background())
	if err != nil || !more || tu.Name != "ask" {
		t.Fatalf("await = %v %v %v", tu, more, err)
	}
}

// Without the binary in the workspace, the program is told the socket and not a command it
// has not got.
func TestTheBriefSaysWhatIsThere(t *testing.T) {
	tools := []agent.Tool{{Name: "ask"}}
	with := callToolBrief(tools, true)
	without := callToolBrief(tools, false)
	if !strings.Contains(with, CallToolBinary+"`, which is on your PATH") || strings.Contains(with, "GET /tools") {
		t.Errorf("with the binary:\n%s", with)
	}
	if strings.Contains(without, "on your PATH") || !strings.Contains(without, "GET /tools") || !strings.Contains(without, "$VERSE_TOOLS") {
		t.Errorf("without the binary:\n%s", without)
	}
	if callToolBrief(nil, true) != "" {
		t.Error("a brief with nothing offered")
	}
}

// The program is offered what the floor admits, and the binary lists exactly that.
func TestTheListIsTheFloor(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, shProgram(`verse-call-tool`, delivery.FetchToolName, "ask"))
	status, _, h := drive(t, r, task.Launch{
		ID: "c3", Repos: rw(origin), Prompt: "list", Vector: &task.Vector{Default: task.Auto},
	}, nil)
	if status != task.Done {
		t.Fatalf("task ended %s (%s)", status, h.Err())
	}
	got := h.Result()
	for _, want := range []string{"fetch\n", "ask\n", "-question <string>  (required)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the list lacks %q:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if line == delivery.PushToolName {
			t.Errorf("push is listed though the floor withholds it:\n%s", got)
		}
	}
}

// A spawn naming a tool this runner cannot offer for the launch is refused at launch, on the
// wire, with what it does offer: a harness tool for a program, a push with no writable
// repository, a tool outside the runner's floor.
func TestASpawnNamingAnUnavailableToolIsRefusedAtLaunch(t *testing.T) {
	origin := initRepo(t)
	cfg := shProgram(`echo ran`)
	cfg.Tools = []string{"ask", delivery.FetchToolName, delivery.PushToolName}
	r := testRunner(t, cfg)
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	rt := client.HTTP(srv.URL)
	for _, c := range []struct {
		id    string
		repos []task.Repo
		tools []string
		want  string
	}{
		{"c4a", ro(origin), []string{"read_file"}, `tools: "read_file" is not offered for this launch (it offers: ask, fetch)`},
		{"c4b", ro(origin), []string{delivery.PushToolName}, `tools: "push" is not offered for this launch`},
		{"c4c", rw(origin), []string{delivery.CreatePullToolName}, `tools: "create-pr" is not offered for this launch`},
	} {
		h := rt.Spawn(task.Launch{ID: c.id, Repos: c.repos, Prompt: "go", Tools: c.tools, Vector: &task.Vector{Default: task.Auto}})
		if err := h.Err(); !strings.Contains(err, c.want) {
			t.Errorf("%s: spawn reported %q, want %q", c.id, err, c.want)
		}
	}
	if r.running() != 0 {
		t.Error("a refused launch started a process anyway")
	}
	// `yield` passes the floor, as the harness keeps it regardless.
	h := rt.Spawn(task.Launch{ID: "c4y", Repos: ro(origin), Prompt: "go", Driver: driverLLM, Tools: []string{"yield"}, Vector: &task.Vector{Default: task.Auto}})
	if err := h.Err(); strings.Contains(err, "not offered") {
		t.Errorf("yield refused under a floor: %q", err)
	}
	// What the launch makes available is accepted, and `[]` is accepted as no tools.
	h = rt.Spawn(task.Launch{ID: "c4d", Repos: rw(origin), Prompt: "go", Tools: []string{"ask", delivery.PushToolName}, Vector: &task.Vector{Default: task.Auto}})
	if err := h.Err(); err != "" {
		t.Errorf("a spawn naming what is offered: %q", err)
	}
}

// `tools: []` is no tools: no socket is served, and the command is not on the program's PATH.
func TestNoToolsIsNoSocket(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, shProgram(`verse-call-tool 2>&1; echo "exit=$?"; test -S .verse/tools.sock && echo socket || echo "no socket"`))
	status, _, h := drive(t, r, task.Launch{
		ID: "c11", Repos: ro(origin), Prompt: "go", Tools: []string{}, Vector: &task.Vector{Default: task.Auto},
	}, nil)
	got := h.Result()
	if status != task.Done || !strings.Contains(got, "not found") || !strings.Contains(got, "exit=127") || !strings.HasSuffix(got, "no socket") {
		t.Fatalf("%s %q", status, got)
	}
}

// The runner's own `tools` is a floor under every environment: one naming a tool outside it is
// refused when the runner starts, and a task whose spawn and environment name none gets it.
func TestTheRunnersToolsAreTheFloor(t *testing.T) {
	cfg := &Config{Tools: []string{"ask"}, Default: "e", Environments: []Environment{{Name: "e", Driver: "sh", Tools: []string{delivery.PushToolName}}},
		Agents: map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "true"}}}}
	if err := cfg.Check(); err == nil || !strings.Contains(err.Error(), `which this runner's `+"`tools`"+` does not`) {
		t.Fatalf("an environment outside the runner's floor: %v", err)
	}
	if err := (&Config{Tools: []string{"nope"}}).Check(); err == nil || !strings.Contains(err.Error(), `no tool "nope"`) {
		t.Fatalf("an unknown tool in the runner's floor: %v", err)
	}
	origin := initRepo(t)
	run := shProgram(`verse-call-tool`)
	run.Tools = []string{"ask"}
	r := testRunner(t, run)
	status, _, h := drive(t, r, task.Launch{ID: "c12", Repos: rw(origin), Prompt: "list", Vector: &task.Vector{Default: task.Auto}}, nil)
	got := h.Result()
	if status != task.Done || !strings.HasPrefix(got, "ask\n") || strings.Contains(got, "\npush\n") || strings.Contains(got, "\nfetch\n") {
		t.Fatalf("%s %q", status, got)
	}
}

// A program is told how it reaches the tools, beside the workspace brief.
func TestTheProgramIsBriefedOnItsTools(t *testing.T) {
	origin := initRepo(t)
	// The program prints its prompt: `$prompt` is what the driver tells it.
	r := testRunner(t, &Config{
		Default:      "e",
		Environments: []Environment{{Name: "e", Driver: "sh", Env: []string{"PATH"}}},
		Agents:       map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "echo \"$0\"", "$prompt"}}},
	})
	status, _, h := drive(t, r, task.Launch{
		ID: "c5", Repos: rw(origin), Prompt: "THE JOB", Vector: &task.Vector{Default: task.Auto},
	}, nil)
	if status != task.Done {
		t.Fatalf("task ended %s (%s)", status, h.Err())
	}
	got := h.Result()
	for _, want := range []string{"verse-call-tool", "`ask`", "`push`", "Verse-Task", "THE JOB"} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, got)
		}
	}
}

// A refused invocation ends the task as failed, saying so: the program is the doer, and there is
// no other way to do the work.
func TestARefusedInvocationFailsTheTask(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, shProgram(`echo ran`))
	// No vector: the driver's own default asks before the program runs.
	status, asked, h := drive(t, r, task.Launch{ID: "c7", Repos: ro(origin), Prompt: "go"},
		func(f task.Event) (string, string) { return "deny", "not today" })
	if status != task.Failed {
		t.Fatalf("task ended %s, want failed", status)
	}
	if len(asked) != 1 || asked[0] != runTool {
		t.Fatalf("questions posed: %v, want the invocation", asked)
	}
	if err := h.Err(); !strings.Contains(err, "the invocation was refused") || !strings.Contains(err, "not today") {
		t.Fatalf("err = %q", err)
	}
	if h.Result() != "" {
		t.Fatalf("a refused program produced a result: %q", h.Result())
	}
}

// The binary is put where a program can call it, and only there: the verse agent has the tools
// as its own.
func TestTheBinaryIsInstalledForProgramsOnly(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{
			{Name: "e", Driver: "sh", Env: []string{"PATH"}},
			{Name: "calls", Driver: task.DriverCalls, Env: []string{"PATH"}},
		},
		Agents: map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "test -x .verse/bin/verse-call-tool && echo installed"}}},
	})
	status, _, h := drive(t, r, task.Launch{ID: "c8", Repos: ro(origin), Prompt: "look", Vector: &task.Vector{Default: task.Auto}}, nil)
	if status != task.Done || h.Result() != "installed" {
		t.Fatalf("program: %s %q (%s)", status, h.Result(), h.Err())
	}
	status, _, h = drive(t, r, task.Launch{ID: "c9", Repos: ro(origin), Environment: "calls",
		Calls:  []task.Call{{Tool: "run_bash", Input: map[string]any{"command": "test -e .verse/bin/verse-call-tool && echo installed || echo absent"}}},
		Vector: &task.Vector{Default: task.Auto}}, nil)
	// A call list's result is the shell record: exit, stdout, stderr.
	if status != task.Done || !strings.Contains(h.Result(), "absent") || strings.Contains(h.Result(), "installed") {
		t.Fatalf("call list: %s %q (%s)", status, h.Result(), h.Err())
	}
}

// Words for a program that runs once are refused when they are said, with the reason, and the
// program runs to its end untouched.
func TestWordsToAOneShotProgramAreRefusedWhenSaid(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, shProgram(`sleep 1; echo ran`))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "c6", Repos: ro(origin), Prompt: "go", Vector: &task.Vector{Default: task.Auto},
	})
	frames, cancel := h.Subscribe()
	defer cancel()
	var said error
	var status task.Status
	for f := range frames {
		if f.Kind == task.EventExec && said == nil {
			// The program is running.
			said = h.Steer("hurry", "user:test", "")
			if said == nil {
				t.Fatal("words to a one-shot program were taken")
			}
		}
		if f.Kind == task.EventStatus && task.Status(f.Text).Terminal() {
			status = task.Status(f.Text)
			break
		}
	}
	if !errors.Is(said, task.ErrUnsteerable) || !strings.Contains(said.Error(), "no way to resume") {
		t.Fatalf("steer = %v", said)
	}
	if status != task.Done || h.Result() != "ran" {
		t.Fatalf("task ended %s with %q (%s)", status, h.Result(), h.Err())
	}
}

func TestWithToolsPutsTheBinaryFirstOnPath(t *testing.T) {
	set := toolsEnv("/w", "/w/.verse/tools.sock")
	got := withTools([]string{"PATH=/usr/bin:/bin", "HOME=/h"}, set)
	want := []string{calltool.Env + "=/w/.verse/tools.sock", "PATH=/w/.verse/bin:/usr/bin:/bin", "HOME=/h"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", got, want)
	}
	// No PATH passed through: the binary's directory, then the default search path, so the
	// program's own commands are still found.
	got = withTools([]string{"HOME=/h"}, set)
	if !strings.Contains(strings.Join(got, "|"), "|PATH=/w/.verse/bin:"+defaultPath+"|") {
		t.Fatalf("got %v", got)
	}
	// Nothing offered: the environment as it was.
	if got := withTools([]string{"A=1"}, nil); len(got) != 1 || got[0] != "A=1" {
		t.Fatalf("got %v", got)
	}
}

func TestTheSocketIsInTheWorkspaceWhenItFits(t *testing.T) {
	p, err := socketFor("/w")
	if err != nil || p != "/w/.verse/tools.sock" {
		t.Fatalf("%q, %v", p, err)
	}
	long := "/" + strings.Repeat("w", 120)
	p, err = socketFor(long)
	if err != nil || strings.HasPrefix(p, long) || len(p) >= sunPathMax {
		t.Fatalf("a long workspace path gave %q, %v", p, err)
	}
}

func TestFindCallToolLooksBesideTheRunner(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "runnerd")
	if FindCallTool(self) != "" && !strings.HasSuffix(FindCallTool(self), CallToolBinary) {
		t.Fatal("found something that is not the binary")
	}
	if err := os.WriteFile(filepath.Join(dir, CallToolBinary), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindCallTool(self); got != filepath.Join(dir, CallToolBinary) {
		t.Fatalf("got %q", got)
	}
}

// An operator's words while a call is being asked about withdraw the question, as they withdraw
// any gate, and then they are the program's next turn: not lost, and not glued onto the call's
// answer as if a gate's answerer had said them.
func TestAnOperatorsWordsDuringACallAreTheNextTurn(t *testing.T) {
	origin := initRepo(t)
	// The prompt reaches the script as $1 rather than spliced into its source: the opening turn
	// begins with the brief, whose backticks and <tool> -<param> are shell once they are text.
	cfg := shProgram(`case "$1" in *hurry*) echo "turn 2: $1"; exit 0;; esac; echo x > svc/x.txt; out=$(verse-call-tool push); echo "turn 1 exit=$? $out"`)
	sh := cfg.Agents["sh"]
	sh.Argv = append(sh.Argv, "sh", "$prompt")
	sh.Resume = []string{"again"} // a program that can have another turn
	cfg.Agents["sh"] = sh
	r := testRunner(t, cfg)
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "c11", Repos: rw(origin), Prompt: "push it",
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "delivery", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.Push}},
		}}},
	})
	frames, cancel := h.Subscribe()
	defer cancel()
	var status task.Status
	var turns int
	for f := range frames {
		switch {
		case f.Kind == task.EventPending && f.Tool == delivery.PushToolName:
			if err := h.Steer("hurry up", "user:test", ""); err != nil {
				t.Fatalf("steer: %v", err)
			}
		case f.Kind == task.EventExec && f.Tool == runTool:
			turns++
		case f.Kind == task.EventStatus && task.Status(f.Text).Terminal():
			status = task.Status(f.Text)
		}
		if status != "" {
			break
		}
	}
	if status != task.Done {
		t.Fatalf("task ended %s (%s)", status, h.Err())
	}
	got := h.Result()
	if turns != 2 || !strings.HasPrefix(got, "turn 2:") || !strings.Contains(got, "hurry up") {
		t.Fatalf("turns=%d result=%q", turns, got)
	}
	if commitOn(origin, "refs/heads/verse/auto/c11") != "" {
		t.Fatal("the withdrawn push happened")
	}
}

// A question about a call whose caller has gone is withdrawn: the program's shell gave up on
// the call, so nobody is waiting on the answer, and the task is not held for it.
func TestACallersLeavingWithdrawsItsQuestion(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, shProgram(`echo x > svc/x.txt; verse-call-tool push & pid=$!; sleep 1; kill $pid; wait $pid; echo "gave up"`))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "c12", Repos: rw(origin), Prompt: "push it",
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "delivery", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.Push}},
		}}},
	})
	frames, cancel := h.Subscribe()
	defer cancel()
	var asked bool
	var status task.Status
	for f := range frames {
		if f.Kind == task.EventPending && f.Tool == delivery.PushToolName {
			asked = true // and nobody answers it
		}
		if f.Kind == task.EventStatus && task.Status(f.Text).Terminal() {
			status = task.Status(f.Text)
			break
		}
	}
	if !asked {
		t.Fatal("the push was never asked about")
	}
	if status != task.Done || h.Result() != "gave up" {
		t.Fatalf("task ended %s with %q (%s)", status, h.Result(), h.Err())
	}
	if commitOn(origin, "refs/heads/verse/auto/c12") != "" {
		t.Fatal("the withdrawn push happened")
	}
}

// A call that fails is recorded as a failed action — the read plane says so — and is the
// program's to read as exit 1; it does not end the run.
func TestAFailedCallIsRecordedAsOne(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, shProgram(`out=$(verse-call-tool ask -question hi -within soon 2>&1); echo "exit=$? $out"`))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "c13", Repos: ro(origin), Prompt: "ask badly", Vector: &task.Vector{Default: task.Auto},
	})
	frames, cancel := h.Subscribe()
	defer cancel()
	var failed string
	var status task.Status
	for f := range frames {
		if f.Kind == task.EventExec && f.Tool == agent.AskToolName {
			failed = f.Err
		}
		if f.Kind == task.EventStatus && task.Status(f.Text).Terminal() {
			status = task.Status(f.Text)
			break
		}
	}
	if !strings.Contains(failed, `within "soon"`) {
		t.Fatalf("the record of the call says %q, want the failure", failed)
	}
	if status != task.Done || !strings.HasPrefix(h.Result(), "exit=1 ") || !strings.Contains(h.Result(), `within "soon"`) {
		t.Fatalf("task ended %s with %q (%s)", status, h.Result(), h.Err())
	}
}
