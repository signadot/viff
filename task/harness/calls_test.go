package harness

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/task"
)

// recordTool is a runtime's own tool — what `push` is to the coding runner — that records its
// calls and derives a push effect, so a vector has something typed to match on.
type recordTool struct {
	mu    sync.Mutex
	calls []string
	block bool
}

func (r *recordTool) tool() Tool {
	return Tool{
		Name:        "push",
		Description: "send work to a declared destination",
		Params:      map[string]any{"destination": map[string]any{"type": "string"}},
		Derive: func(tu *task.ToolUse) {
			dest, _ := tu.Input["destination"].(string)
			tu.Effects = []task.Effect{{Verb: task.Push, Resource: task.GitRef(dest)}}
		},
		Do: func(_ context.Context, tu task.ToolUse) (string, bool, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			dest, _ := tu.Input["destination"].(string)
			if r.block {
				return "the remote declined " + dest, true, nil
			}
			r.calls = append(r.calls, dest)
			return "pushed to " + dest, false, nil
		},
	}
}

func (r *recordTool) pushed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// A call list reaches the runtime's own tools. This is the thing step 3 is for: the merge task
// is `git merge --ff-only` followed by a push, and until now `push` was offered to the llm
// driver alone — so the one task with a single right answer was the one that had to be an agent.
func TestACallListReachesTheRuntimesOwnTools(t *testing.T) {
	rt := &recordTool{}
	p := Calls(Config{
		ID: "t", Root: t.TempDir(), Resolver: task.AutoResolver{}, Log: io.Discard,
		Vector: &task.Vector{Default: task.Auto}, Extra: []Tool{rt.tool()},
	}, []task.Call{
		{Tool: "run_bash", Input: map[string]any{"command": "true"}},
		{Tool: "push", Input: map[string]any{"destination": "main"}},
	})

	out, err := p.Run(context.Background())
	if err != nil || out.Reason != "done" {
		t.Fatalf("run: reason=%q err=%v", out.Reason, err)
	}
	if got := rt.pushed(); len(got) != 1 || got[0] != "main" {
		t.Fatalf("pushed %v, want [main]", got)
	}
}

// And it is gated the same way, which is the property that makes this cheap: the vector matches
// on the DERIVED effect, and the deriver is handed the runtime's tools whichever driver proposed
// the action. A gate written for the agent's push holds a list's push, with nobody having to
// know a second driver exists.
func TestACallListsPushIsGatedLikeTheAgents(t *testing.T) {
	rt := &recordTool{}
	reg := task.NewRegistry()
	var asked []task.GateQuestion
	var mu sync.Mutex
	reg.OnPost(func(pd task.Pending) {
		mu.Lock()
		asked = append(asked, pd.Question)
		mu.Unlock()
		reg.Answer(pd.Question.ID, task.Resolution{Proceed: false, By: "human:test"})
	})
	// The gate a charter would write: ask about anything that pushes. It names no tool and no
	// driver — only the effect.
	vec := &task.Vector{Default: task.Auto, Gates: []task.Gate{
		{Name: "delivery", Mode: task.Ask, Match: task.GateMatch{Verbs: []task.Verb{task.Push}}},
	}}
	p := Calls(Config{
		ID: "t", Root: t.TempDir(), Resolver: reg, Log: io.Discard,
		Vector: vec, Extra: []Tool{rt.tool()},
	}, []task.Call{
		{Tool: "run_bash", Input: map[string]any{"command": "true"}},
		{Tool: "push", Input: map[string]any{"destination": "main"}},
	})

	_, err := p.Run(context.Background())
	if err == nil {
		t.Fatal("a denied push ended the list as a success")
	}
	if got := rt.pushed(); len(got) != 0 {
		t.Fatalf("pushed %v despite the gate — the vector is decoration", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 {
		t.Fatalf("%d questions, want 1 — the shell command is ungated here and the push is not", len(asked))
	}
	if asked[0].Gate != "delivery" {
		t.Fatalf("decided by gate %q, want the one matching the push effect", asked[0].Gate)
	}
}

// A refusal the list said it could survive is survived, tool-side. `push` reports a remote
// declining as BLOCKED rather than as an error precisely because it is an ordinary outcome, and
// a sweep deleting ten refs wants the other nine deleted.
func TestARefusedToolCallCanBeSurvivable(t *testing.T) {
	rt := &recordTool{block: true}
	p := Calls(Config{
		ID: "t", Root: t.TempDir(), Resolver: task.AutoResolver{}, Log: io.Discard,
		Vector: &task.Vector{Default: task.Auto}, Extra: []Tool{rt.tool()},
	}, []task.Call{
		{Tool: "push", Input: map[string]any{"destination": "one"}, Fatal: task.Fatals(task.FatalErr)},
		{Tool: "push", Input: map[string]any{"destination": "two"}, Fatal: task.Fatals(task.FatalErr)},
	})

	if out, err := p.Run(context.Background()); err != nil || out.Reason != "done" {
		t.Fatalf("run: reason=%q err=%v — a refusal the calls declared survivable ended the run", out.Reason, err)
	}
}

// The tool floor applies to a list, and it applies BEFORE anything runs. The llm driver enforces
// it by not offering what a spawn narrowed away; a list has nobody to withhold from, so the
// equivalent is refusing the spawn rather than discovering it three calls in.
func TestACallListCannotNameAToolTheSpawnDoesNotHave(t *testing.T) {
	rt := &recordTool{}
	p := Calls(Config{
		ID: "t", Root: t.TempDir(), Resolver: task.AutoResolver{}, Log: io.Discard,
		Vector: &task.Vector{Default: task.Auto},
		Tools:  []string{"read_file"}, // narrowed: no run_bash, no push
		Extra:  []Tool{rt.tool()},
	}, []task.Call{
		{Tool: "read_file", Input: map[string]any{"path": "x"}},
		{Tool: "push", Input: map[string]any{"destination": "main"}},
	})

	_, err := p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), `no tool "push"`) {
		t.Fatalf("run: %v — want the spawn refused, naming the tool it does not have", err)
	}
	if got := rt.pushed(); len(got) != 0 {
		t.Fatalf("pushed %v — a refused spawn ran part of its list", got)
	}
}

// The runtime's closing act is installed for a list too. It is the runner's own last action —
// what makes a push happen at all in the coding runner — and a driver that skipped it would be
// a task whose work never leaves.
func TestACallListGetsTheClosingAct(t *testing.T) {
	fin := &recordFinisher{}
	p := Calls(Config{
		ID: "t", Root: t.TempDir(), Resolver: task.AutoResolver{}, Log: io.Discard,
		Vector: &task.Vector{Default: task.Auto}, Finish: fin,
	}, []task.Call{{Tool: "run_bash", Input: map[string]any{"command": "true"}}})

	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fin.n != 1 {
		t.Fatalf("the finisher ran %d times, want 1", fin.n)
	}
}

// recordFinisher is a runtime's closing act: it proposes one action, and records that it ran.
type recordFinisher struct{ n int }

func (f *recordFinisher) Finish(string) (task.ToolUse, bool) {
	return task.ToolUse{Name: "deliver"}, true
}

func (f *recordFinisher) Do(context.Context, task.ToolUse) (string, error) {
	f.n++
	return "delivered", nil
}

// A contract fails the spawn here as it does anywhere: there is no model to repair an answer.
func TestACallListStillRefusesAnAnswerContract(t *testing.T) {
	p := Calls(Config{
		ID: "t", Root: t.TempDir(), Resolver: task.AutoResolver{}, Log: io.Discard,
		Vector: &task.Vector{Default: task.Auto}, Answer: ir.FromString("verdict"),
	}, []task.Call{{Tool: "run_bash", Input: map[string]any{"command": "true"}}})

	if _, err := p.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "answer contract") {
		t.Fatalf("run: %v — want the spawn refused for the contract", err)
	}
}
