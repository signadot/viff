package harness

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
)

const testContract = `class: !or [bug, nit]
severity: !or [low, high]`

func contract(t *testing.T) *answer.Contract {
	t.Helper()
	c, err := answer.ParseSource(testContract)
	if err != nil {
		t.Fatalf("parse contract: %v", err)
	}
	return c
}

func stop(content string) scriptedTurn {
	return scriptedTurn{msg: chatMessage{Role: "assistant", Content: content}, finish: "stop"}
}

// An answer that satisfies the contract ends the run where it always did: one model turn,
// no repair, nothing said back.
func TestContractSatisfiedEndsTheRun(t *testing.T) {
	fake := &fakeCompleter{turns: []scriptedTurn{stop(`{class: bug, severity: high}`)}}
	d := &llmDriver{client: fake, prompt: "review it", log: io.Discard, contract: contract(t)}

	_, more, err := d.Next(context.Background(), task.StepResult{})
	if err != nil || more {
		t.Fatalf("more=%v err=%v, want done with no error", more, err)
	}
	if fake.i != 1 {
		t.Fatalf("%d model calls, want 1", fake.i)
	}
}

// The failure this exists for: an agent that narrates instead of answering. It is told what
// is wrong, it fixes it, and the run completes — the charter downstream never sees the prose.
func TestNarrationIsRepairedIntoAnAnswer(t *testing.T) {
	fake := &fakeCompleter{turns: []scriptedTurn{
		stop("I read the handler and both callers. It's a nil deref, and I'd call it high severity."),
		stop("{class: bug, severity: high}"),
	}}
	d := &llmDriver{client: fake, prompt: "review it", log: io.Discard, contract: contract(t)}

	_, more, err := d.Next(context.Background(), task.StepResult{})
	if err != nil || more {
		t.Fatalf("more=%v err=%v, want done with no error", more, err)
	}
	if fake.i != 2 {
		t.Fatalf("%d model calls, want 2 (one repair)", fake.i)
	}
	// What went back has to be a DIAGNOSIS, not a re-ask: the contract itself, and the
	// honest exit for an agent that cannot answer truthfully.
	sent := fake.seen[1]
	last := sent[len(sent)-1]
	if last.Role != "user" {
		t.Fatalf("repair arrived as role %q, want user", last.Role)
	}
	for _, want := range []string{"answer contract", "class", "!or", failTool} {
		if !strings.Contains(last.Content, want) {
			t.Errorf("repair message missing %q:\n%s", want, last.Content)
		}
	}
	if got := d.Result(); got != "{class: bug, severity: high}" {
		t.Errorf("Result() = %q, want the repaired answer", got)
	}
}

// A wrong answer is repaired against the specific field that was wrong — the thing a bare
// "does not match" cannot say.
func TestWrongValueIsNamedInTheRepair(t *testing.T) {
	fake := &fakeCompleter{turns: []scriptedTurn{
		stop("{class: catastrophe, severity: high}"),
		stop("{class: bug, severity: high}"),
	}}
	d := &llmDriver{client: fake, prompt: "review it", log: io.Discard, contract: contract(t)}
	if _, _, err := d.Next(context.Background(), task.StepResult{}); err != nil {
		t.Fatalf("Next: %v", err)
	}
	sent := fake.seen[1]
	repair := sent[len(sent)-1].Content
	if !strings.Contains(repair, "class") || !strings.Contains(repair, "catastrophe") {
		t.Errorf("repair does not name the field and the value found:\n%s", repair)
	}
}

// An unconvincible model fails the RUN, loudly, which is the honest end — and the branch
// every rule already declares. The budget is bounded: two repairs, three model calls, then
// the error. (A fourth call would run off the end of the script and panic the test, which is
// the assertion that the loop does not spin.)
func TestUnrepairableAnswerFailsTheRun(t *testing.T) {
	fake := &fakeCompleter{turns: []scriptedTurn{
		stop("still thinking out loud"),
		stop("nope, prose again"),
		stop("and once more"),
	}}
	d := &llmDriver{client: fake, prompt: "review it", log: io.Discard, contract: contract(t)}

	_, more, err := d.Next(context.Background(), task.StepResult{})
	if err == nil {
		t.Fatal("run completed with an answer that never satisfied its contract")
	}
	if more {
		t.Error("more=true after a terminal contract failure")
	}
	if fake.i != 3 {
		t.Fatalf("%d model calls, want 3 (initial + %d repairs)", fake.i, answer.DefaultRepairs)
	}
	if !strings.Contains(err.Error(), "answer contract") {
		t.Errorf("error does not say what failed: %v", err)
	}
}

// No contract, no check: a spawn that declares nothing keeps the old behaviour exactly,
// including the freedom to answer in prose.
func TestNoContractLeavesTheLoopAlone(t *testing.T) {
	fake := &fakeCompleter{turns: []scriptedTurn{stop("I had a look and it seems fine.")}}
	d := &llmDriver{client: fake, prompt: "review it", log: io.Discard}
	if _, more, err := d.Next(context.Background(), task.StepResult{}); err != nil || more {
		t.Fatalf("more=%v err=%v", more, err)
	}
	if fake.i != 1 {
		t.Fatalf("%d model calls, want 1", fake.i)
	}
}

// The contract is stated in the frame, so an agent meets it before it stops rather than as
// a rejection — and it comes back OFF for a child, which runs no such check.
func TestContractClauseGoesOnAndComesOff(t *testing.T) {
	base := defaultFrame(toolDefs(), Vector())
	c := contract(t)
	framed := strings.TrimRight(base, "\n") + "\n\n" + contractClause(c)
	for _, line := range strings.Split(c.Source(), "\n") { // indented into the clause
		if !strings.Contains(framed, line) {
			t.Fatalf("the frame does not state the contract line %q", line)
		}
	}
	if got := withoutContract(framed); got != strings.TrimRight(base, "\n") {
		t.Errorf("withoutContract left residue:\n%q", got[len(strings.TrimRight(base, "\n")):])
	}
	if got := withoutContract(base); got != base {
		t.Error("withoutContract altered a frame that never carried a clause")
	}
}

// A contract the driver cannot USE fails the task at BUILD, before a model call — the
// same treatment a tool name no driver knows gets, and for the same reason. A document
// always parses; what does not survive is a schema with a defect in it.
func TestUnusableContractFailsTheBuild(t *testing.T) {
	bad, err := codec.ParseNode([]byte("signature: {name: c}\ndefine:\n  node: {next: .[node]}\naccept: .[node]\n"), codec.Tony)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, _ := Build(Config{ID: "p1", Prompt: "go", Client: &fakeCompleter{}, Answer: bad})
	out, runErr := p.Run(context.Background())
	if runErr == nil {
		t.Fatal("a task was built on a contract that cannot be used")
	}
	if out.Reason != string(task.Failed) {
		t.Errorf("reason %q, want %q", out.Reason, task.Failed)
	}
}
