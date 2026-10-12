package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/task"
)

// THE LOCAL DRIVE: a real model, a real fan-out, and no repository, runner or cluster anywhere.
//
// It exists because the alternative was a fifteen-minute round trip through `make ship` and a
// shared outage, and the cost of a feedback loop selects what gets validated. The advice this
// replaces was written, shipped, and never once watched, because watching it cost fifteen
// minutes — and when a drive finally did watch it, it did not work.
//
// What it needs from the world is a model. The staging drive's subject was a git commit, its
// fan-out was one agent per changed Go file, and its delivery was a push; none of that is
// load-bearing for the question, which is whether a model with sub-agents running reaches for
// `wait` or invents a way to poll.
//
// IT RUNS BOTH ARMS, and that is not thoroughness — it is the correction to a first version
// that ran only the fixed code, passed, and proved nothing. Run against the PREVIOUS code it
// passed identically: three files, three agents, no polling at all. A test the bug passes is
// not a test of the fix. So the control is built in, and it is the one that has to fail before
// the other one means anything.
//
// OPT-IN: real tokens.
//
//	source ~/secrets/portkey.sh && VERSE_DRIVE=1 go test ./runner/runnerd/verserunner/agent/ -run Drive -v
type driveResult struct {
	boundaries int
	spawned    []string
	waits      int
	execs      []string
	// polling is what the parent did AFTER it had delegated everything and before it answered:
	// extra sub-agents, and commands run to pass the time. Measured by position rather than by
	// count, because a parent that surveys its directory first runs legitimate commands and an
	// earlier version of this counted those as polling and called the control a success.
	polling []string
	answer  string
	outcome string
	err     error
}

// oldCallAgentAdvice is what call_agent said before `wait` existed: the instruction the staging
// drive watched a model follow the letter of and miss entirely. The control arm needs it
// verbatim, or it is testing a description that names a tool it was not given.
const oldCallAgentAdvice = "Delegate a self-contained subtask to a sub-agent that runs " +
	"concurrently. Give a short agent name and a full prompt describing the task. Use for " +
	"independent subtasks you can run in parallel. TO WAIT FOR RESULTS, END YOUR TURN: say what " +
	"you are waiting for and stop, without calling a tool. You will be called again as soon as a " +
	"sub-agent finishes, with its result. Never sleep or poll for a sub-agent — sleeping delays " +
	"the result rather than waiting for it, because a turn that is still running is a turn whose " +
	"children cannot be folded in."

var driveFiles = map[string]string{
	"alpha.txt": "The alpha service reads orders from the queue and writes them to the ledger.",
	"beta.txt":  "The beta service reconciles the ledger against the bank feed every hour.",
	"gamma.txt": "The gamma service emails a summary to the finance team each morning.",
}

// The parent's tools. run_bash is offered because the staging drive had it and used it to poll:
// withholding it would let a pass hide behind a missing instrument rather than a model that did
// not want one.
var driveTools = []string{"read_file", "list_dir", "run_bash", "call_agent"}

// The parent's job. It INVESTIGATES FIRST, and that is not padding: on staging the model had
// run four tool calls (a git show, a grep, a read) before it fanned out, so by the time it had
// nothing to do it was several turns into being a thing that calls tools. Locally, a parent that
// delegates on its second turn has no such momentum — and that difference alone was enough to
// make the first version of this control fail to reproduce anything.
const drivePrompt = "Your working directory holds a few small text files, each describing one " +
	"service. First survey it yourself: list the directory, and read each file to check it is " +
	"not empty. THEN, for EACH file, call_agent once — one agent per file, running concurrently " +
	"— giving each the filename. When all of them have reported, answer for all of them " +
	"together as a JSON document in the shape below, with nothing before it and nothing after."

// The answer contract, and it is the piece that turned out to matter. Staging had one; the
// first three versions of this drive did not, and did not reproduce anything.
//
// The mechanism it exposes is sharper than "the model has no verb for waiting". defaultFrame
// tells the agent "Your final message IS this task's result", and a contract adds "it is
// checked the moment you stop". Against that, "END YOUR TURN: say what you are waiting for and
// stop, without calling a tool" reads as INSTRUCTIONS TO ANSWER NOW — and the agent cannot
// answer, because its sub-agents have not reported. So it does the only other thing available
// and calls a tool, any tool, to avoid terminating on an answer it does not have yet.
//
// That is why `wait` works where the wording did not: it is a way to end a turn WITHOUT
// producing the final message.
const driveContract = `files: !irtype 0
summaries: !all
  file: !irtype "alpha.txt"
  says: !irtype "what the service described in it does"`

// The child's job, deliberately slow. This is the variable that matters and the one the first
// version of this test got wrong: children that finish in seconds never leave the parent with
// nothing to do, so the parent never has the problem the fix is for, so both arms passed.
const driveChildJob = "Read the named file. Then run `sleep 40` with run_bash — this stands in " +
	"for the slow analysis a real sub-agent does, and the point of this exercise is what your " +
	"PARENT does while you take your time. Then answer in one sentence saying which service the " +
	"file describes and what that service does."

// drive runs one arm. withVerb=false rebuilds the world as it was: no `wait` on the menu, and
// call_agent carrying the advice that asks the model to stop rather than giving it something to
// call.
func drive(t *testing.T, withVerb bool) driveResult {
	t.Helper()
	root := t.TempDir()
	for name, body := range driveFiles {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tds, err := selectTools(driveTools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !withVerb {
		var keep []toolDef
		for _, td := range tds {
			if td.Function.Name == yieldTool {
				continue
			}
			if td.Function.Name == "call_agent" {
				td.Function.Description = oldCallAgentAdvice
			}
			keep = append(keep, td)
		}
		tds = keep
	}

	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "claude-sonnet-5" // the model staging runs, so this asks staging's question
	}
	// Build's wiring, by hand, because an arm has to control the tool list — which Build
	// resolves for itself, correctly, from selectTools.
	client := clientFrom(os.Getenv("PORTKEY_BASE_URL"), os.Getenv("PORTKEY_API_KEY"),
		os.Getenv("PORTKEY_VIRTUAL_KEY"), model)
	vec := &task.Vector{Default: task.Auto}
	sup := task.NewSupervisor(vec, task.AutoResolver{})
	sup.SetDeriver(demoDeriver{})
	contract, err := answer.ParseSource(driveContract)
	if err != nil {
		t.Fatal(err)
	}
	frame := defaultFrame(tds, vec, nil, false) + "\n\n" + contractClause(contract)
	d := &llmDriver{client: client, tools: tds, frame: frame, prompt: drivePrompt, log: os.Stderr}
	p := task.NewTask("drive", sup, d, &fsExec{root: root, log: os.Stderr})
	p.SetLog(os.Stderr)
	p.SetAnswerCheck(&answer.Checker{C: contract, FailHint: yieldHint})
	d.emit = p.Emit
	var kid int
	p.SetSpawn(func(parent *task.Task, mode task.ChildMode, req *task.CallAgentRequest) (*task.Task, error) {
		kid++
		// The child does NOT inherit the contract clause, exactly as Build does it: the
		// contract describes THIS task's answer, and a child answers a subtask.
		cd := &llmDriver{client: client, tools: tds, frame: withoutContract(frame),
			prompt: req.Prompt + "\n\n" + driveChildJob, log: os.Stderr}
		child, err := parent.NewChild(fmt.Sprintf("%s.%s~%d", parent.ID, req.Agent, kid), mode, req,
			cd, &fsExec{root: root, log: os.Stderr})
		if err == nil {
			cd.emit = child.Emit
		}
		return child, err
	})

	frames, unsub := p.Subscribe()
	defer unsub()
	done := make(chan driveResult, 1)
	go func() {
		var r driveResult
		fannedOut := false // every file has an agent; anything after this is passing the time
		for f := range frames {
			switch f.Kind {
			case task.EvBoundary:
				r.boundaries++
			case task.EvChild:
				if name, ok := strings.CutPrefix(f.Text, "spawned "); ok {
					if fannedOut {
						r.polling = append(r.polling, "spawn:"+name)
					}
					r.spawned = append(r.spawned, name)
					if len(r.spawned) >= len(driveFiles) {
						fannedOut = true
					}
				}
			case task.EvExec:
				r.execs = append(r.execs, f.Tool)
				if fannedOut {
					r.polling = append(r.polling, "exec:"+f.Tool)
				}
			case task.EvSay:
				if strings.Contains(f.Text, "yielded (waiting)") {
					r.waits++
				}
			}
		}
		done <- r
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	out, err := p.Run(ctx)
	r := <-done
	r.answer, r.outcome, r.err = p.Result(), out.Reason, err
	t.Logf("withVerb=%v → boundaries=%d children=%d waits=%d parentExecs=%d POLLING=%d outcome=%s",
		withVerb, r.boundaries, len(r.spawned), r.waits, len(r.execs), len(r.polling), r.outcome)
	if len(r.polling) > 0 {
		t.Logf("    polling %v", r.polling)
	}
	for _, n := range r.spawned {
		t.Logf("    child %s", n)
	}
	if len(r.execs) > 0 {
		t.Logf("    parent execs %v", r.execs)
	}
	return r
}

// AND THE OTHER HALF of the verb, against a real model: a job that cannot be done has to FAIL,
// not complete with an excuse in its result.
//
// This is the floor `fail` was introduced to raise, and folding it into `yield` is exactly the
// kind of change that could lower it again without anything noticing — the failure mode is a
// task reporting `done`, which no assertion about the loop would catch. A charter dereferencing
// $result is the caller that cannot afford it.
func TestDriveAnImpossibleJobYieldsCannot(t *testing.T) {
	requireDrive(t)
	root := t.TempDir()
	tds, err := selectTools([]string{"read_file", "list_dir"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "claude-sonnet-5"
	}
	client := clientFrom(os.Getenv("PORTKEY_BASE_URL"), os.Getenv("PORTKEY_API_KEY"),
		os.Getenv("PORTKEY_VIRTUAL_KEY"), model)
	vec := &task.Vector{Default: task.Auto}
	sup := task.NewSupervisor(vec, task.AutoResolver{})
	sup.SetDeriver(demoDeriver{})
	d := &llmDriver{client: client, tools: tds, frame: defaultFrame(tds, vec, nil, false), log: os.Stderr,
		prompt: "Read the file `quarterly-revenue.csv` in your working directory and report the " +
			"total revenue for Q3. Report the number and nothing else."}
	p := task.NewTask("impossible", sup, d, &fsExec{root: root, log: os.Stderr})
	p.SetLog(os.Stderr)
	d.emit = p.Emit

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := p.Run(ctx)
	t.Logf("outcome=%s err=%v result=%q", out.Reason, err, p.Result())
	if out.Reason != string(task.Failed) {
		t.Errorf("a task asked for a number from a file that does not exist reported %q. "+
			"Stopping with an explanation is read as SUCCESS by everything downstream, which is "+
			"the whole reason this verb exists.", out.Reason)
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "quarterly-revenue") {
		t.Errorf("the failure does not carry what went wrong: %v", err)
	}
}

func requireDrive(t *testing.T) {
	t.Helper()
	if os.Getenv("VERSE_DRIVE") != "1" {
		t.Skip("VERSE_DRIVE=1 to run: this one calls a real model")
	}
	if os.Getenv("PORTKEY_API_KEY") == "" {
		t.Skip("no PORTKEY_API_KEY: source the creds first")
	}
}

// THE CONTROL, which does NOT currently reproduce — and that is written down rather than
// quietly dropped, because it is the honest state of the evidence for the fix below.
//
// Six shapes were tried against the previous code (no `wait`, the "END YOUR TURN" advice):
// three files with fast children; children made slow with extra checks; a parent told to survey
// the directory first, so it arrives at the wait several tool calls deep the way staging's did;
// an answer contract, so "end your turn" collides with "your final message IS the result"; and
// children forced to take forty seconds. In every one the model fanned out, ended its turn, and
// waited correctly. The staging run that spawned `noop`, `noop2` … `noop10` remains a single
// observation.
//
// So what the fix rests on is: the mechanism is right (TestWaitEndsTheTurnAndCostsOneModelCall-
// PerResult), the model uses the verb when it has one (the arm below), and giving it a verb
// removes a class of failure that advice asking for a restraint cannot. What is NOT established
// is that the class is common — one drive saw it, and this cannot make it happen on demand.
//
// It asserts nothing, deliberately. A test that fails for a known reason is noise, and a test
// that passes while measuring nothing is worse. It is a PROBE: run it after changing what the
// agent is told about waiting, and read the numbers.
func TestDriveControlProbesForPollingWithoutTheVerb(t *testing.T) {
	requireDrive(t)
	r := drive(t, false)
	if r.err != nil {
		t.Fatalf("run: %v", r.err)
	}
	t.Logf("CONTROL: %d filler children, %d parent actions after fan-out, %d boundaries",
		len(r.spawned)-len(driveFiles), len(r.polling), r.boundaries)
	if len(r.polling) == 0 {
		t.Log("no polling: the pathology did not reproduce, as it has not in six shapes. " +
			"The fix arm therefore shows the model USES the verb, not that it needed it.")
	}
}

// AND THE FIX. Same prompt, same children, same model — with `wait` on the menu.
func TestDriveTheVerbStopsThePolling(t *testing.T) {
	requireDrive(t)
	r := drive(t, true)
	if r.err != nil {
		t.Fatalf("run: %v", r.err)
	}
	if len(r.spawned) != len(driveFiles) {
		t.Errorf("%d children for %d files — the extras are filler work: %v",
			len(r.spawned), len(driveFiles), r.spawned)
	}
	if len(r.polling) > 0 {
		t.Errorf("the parent did %d thing(s) to pass the time after fanning out: %v",
			len(r.polling), r.polling)
	}
	if r.waits == 0 {
		t.Errorf("%q was never called, so whatever the parent did between fanning out and "+
			"answering, it was not the thing it was given", yieldTool)
	}
	if r.boundaries > 12 {
		t.Errorf("%d boundaries for three files: polling by another name", r.boundaries)
	}
	for name := range driveFiles {
		if !strings.Contains(r.answer, name) {
			t.Errorf("the answer does not mention %s:\n%s", name, r.answer)
		}
	}
	if r.outcome != string(task.Done) {
		t.Errorf("outcome %q, want done", r.outcome)
	}
}
