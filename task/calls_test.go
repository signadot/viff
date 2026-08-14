package task

import (
	"context"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// listExec records what ran and can hand back either kind of bad news: an Err, or a non-zero
// exit with no error at all — which is the harness's reading and the one a call list cannot
// see without StepResult.Exit.
type listExec struct {
	mu    sync.Mutex
	order []string
	fail  map[string]string // tool → error text
	exit  map[string]int    // tool → exit code
}

func (e *listExec) Execute(_ context.Context, tu ToolUse) (StepResult, []ToolUse, error) {
	e.mu.Lock()
	e.order = append(e.order, tu.Name)
	e.mu.Unlock()
	res := StepResult{Use: tu, Output: "ok"}
	if msg, ok := e.fail[tu.Name]; ok {
		res.Err = errString(msg)
	}
	if code, ok := e.exit[tu.Name]; ok {
		res.Exit = &code
	}
	return res, nil, nil
}

func (e *listExec) ran() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.order...)
}

type errString string

func (e errString) Error() string { return string(e) }

// callsTask builds a task over a call list with the given vector, so each test says only what
// it is about. The list is the launch's own field: there is no other way in.
func callsTask(t *testing.T, calls []Call, vec *Vector, reg Resolver, e Executor) *Task {
	t.Helper()
	p := NewCallsTask(Launch{ID: "t", Calls: calls, Vector: vec}, reg, e)
	p.SetLog(io.Discard)
	return p
}

// call is a one-line entry for the tests that only care about order.
func call(tool, fatal string) Call { return Call{Tool: tool, Fatal: fatal} }

// The list runs in the order it was written, and the task completes.
func TestACallListRunsInOrder(t *testing.T) {
	e := &listExec{}
	p := callsTask(t, []Call{
		{Tool: "first", Input: map[string]any{"command": "echo one"}},
		{Tool: "second"},
		{Tool: "third"},
	}, autoVec(), AutoResolver{}, e)

	out, err := p.Run(context.Background())
	if err != nil || out.Reason != "done" {
		t.Fatalf("run: reason=%q err=%v", out.Reason, err)
	}
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(e.ran(), want) {
		t.Fatalf("ran %v, want %v", e.ran(), want)
	}
}

// An action list is supervised PER ACTION: the vector sees each call on its own, the question
// names the call it is about, and denying one leaves the calls after it unrun.
//
// This is the property the whole driver rests on. A shell script handed to `run_bash` is one
// action, so a gate can only say yes or no to the whole of it; a list of three is three
// decisions, and nothing downstream had to change for that to be true.
func TestACallListIsSupervisedPerAction(t *testing.T) {
	e := &listExec{}
	reg := NewRegistry()
	var asked []string
	var mu sync.Mutex
	reg.OnPost(func(pd Pending) {
		mu.Lock()
		asked = append(asked, pd.Question.ID)
		mu.Unlock()
		reg.Answer(pd.Question.ID, Resolution{Proceed: false, By: "human:test"})
	})
	p := callsTask(t, []Call{call("first", ""), call("second", ""), call("third", "")}, askOn("second"), reg, e)

	_, err := p.Run(context.Background())
	if err == nil {
		t.Fatal("a denied call ended the list as a success")
	}
	if !strings.Contains(err.Error(), "call 2 of 3 (second)") {
		t.Fatalf("error does not name the call that stopped the list: %v", err)
	}
	if want := []string{"first"}; !reflect.DeepEqual(e.ran(), want) {
		t.Fatalf("ran %v, want %v — the denial stopped exactly one call short", e.ran(), want)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 {
		t.Fatalf("%d questions posed, want exactly 1 — one gate per call, not per list", len(asked))
	}
}

// `fatal:` is what each call gets to say about its own bad outcomes. Same list, same refusal,
// two different answers — which is the point: a sweep declining one ref should delete the rest,
// and a push after a merge that did not merge should not happen.
func TestFatalNarrowsWhatEndsTheList(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fatal string
		want  []string
	}{
		{"absent means anything stops it", "", nil},
		{"blocked is not fatal here", "err", []string{"second"}},
		{"and it can be named explicitly", "err,blocked", nil},
		{"and spacing is not a trap", "err, blocked", nil},
		{"or waived entirely", "none", []string{"second"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &listExec{}
			reg := NewRegistry()
			reg.OnPost(func(pd Pending) {
				reg.Answer(pd.Question.ID, Resolution{Proceed: false, By: "human:test"})
			})
			p := callsTask(t, []Call{call("first", tc.fatal), call("second", "")}, askOn("first"), reg, e)

			_, err := p.Run(context.Background())
			if !reflect.DeepEqual(e.ran(), tc.want) {
				t.Fatalf("ran %v, want %v", e.ran(), tc.want)
			}
			if tc.want == nil && err == nil {
				t.Fatal("the list stopped and the run reported success")
			}
			if tc.want != nil && err != nil {
				t.Fatalf("a refusal this call declared survivable failed the run: %v", err)
			}
		})
	}
}

// A non-zero exit with no error is the harness's reading of a failed command, and a call list
// running on it must be able to say that command failing ends the list. Nothing else in a
// StepResult distinguishes `git merge --ff-only` refusing to fast-forward from it succeeding.
func TestANonZeroExitCanEndTheList(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fatal string
		want  []string
	}{
		{"absent means it stops", "", []string{"first"}},
		{"named explicitly", "nonzero", []string{"first"}},
		{"and a list can carry on regardless", "err", []string{"first", "second"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &listExec{exit: map[string]int{"first": 1}}
			p := callsTask(t, []Call{call("first", tc.fatal), call("second", "")}, autoVec(), AutoResolver{}, e)

			_, err := p.Run(context.Background())
			if !reflect.DeepEqual(e.ran(), tc.want) {
				t.Fatalf("ran %v, want %v", e.ran(), tc.want)
			}
			if len(tc.want) == 1 {
				if err == nil {
					t.Fatal("the command failed, the list stopped, and the run said done")
				}
				if !strings.Contains(err.Error(), "exited 1") {
					t.Fatalf("error does not say what happened: %v", err)
				}
			} else if err != nil {
				t.Fatalf("an exit this call declared survivable failed the run: %v", err)
			}
		})
	}
}

// An Err ends the list under the default, and says whose words those were.
func TestAFailedCallEndsTheList(t *testing.T) {
	e := &listExec{fail: map[string]string{"first": "no such remote"}}
	p := callsTask(t, []Call{call("first", ""), call("second", "")}, autoVec(), AutoResolver{}, e)

	_, err := p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no such remote") {
		t.Fatalf("run: %v — want the executor's own words", err)
	}
	if want := []string{"first"}; !reflect.DeepEqual(e.ran(), want) {
		t.Fatalf("ran %v, want %v", e.ran(), want)
	}
}

// A contract this driver cannot enforce fails the SPAWN. There is no model to repair an
// answer, so running the list and silently returning nothing would leave the charter
// downstream written against a promise nobody kept.
func TestAnAnswerContractFailsTheSpawn(t *testing.T) {
	e := &listExec{}
	l := Launch{ID: "t", Calls: []Call{call("first", "")}, Vector: autoVec(), Answer: ir.FromString("verdict")}
	p := NewCallsTask(l, AutoResolver{}, e)
	p.SetLog(io.Discard)

	_, err := p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "answer contract") {
		t.Fatalf("run: %v — want the spawn refused for the contract", err)
	}
	if len(e.ran()) != 0 {
		t.Fatalf("ran %v — a refused spawn ran part of its list", e.ran())
	}
}

// A list the engine would have refused fails the spawn here too. The engine checks at fire time
// so a charter's author sees it, and this checks again because a task can be posted straight at
// a runtime without passing through a charter at all.
func TestAnUncheckableListFailsTheSpawn(t *testing.T) {
	e := &listExec{}
	p := callsTask(t, []Call{call("first", "sideways")}, autoVec(), AutoResolver{}, e)

	_, err := p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sideways") {
		t.Fatalf("run: %v — want the spawn refused, naming what it could not read", err)
	}
	if len(e.ran()) != 0 {
		t.Fatalf("ran %v — a spawn that could not be read ran anyway", e.ran())
	}
}

// `none` must stand alone: a list of what ends this call cannot also say nothing does.
func TestFatalNoneMustStandAlone(t *testing.T) {
	err := CheckCalls([]Call{call("first", "none,err")})
	if err == nil || !strings.Contains(err.Error(), "stand alone") {
		t.Fatalf("CheckCalls: %v", err)
	}
}

func TestParseCalls(t *testing.T) {
	calls, err := ParseCalls(`
calls:
- tool: run_bash
  input: {command: "git merge --ff-only origin/topic"}
- tool: push
  input: {destination: main}
  fatal: err
- tool: note
  fatal: none
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("%d calls, want 3", len(calls))
	}
	if calls[0].Tool != "run_bash" || calls[0].Input["command"] != "git merge --ff-only origin/topic" {
		t.Fatalf("first call = %+v", calls[0])
	}
	// Absent, narrowed, and emptied are three different statements, and the zero value has to
	// keep them apart: nil means anything stops the list, and `fatal: []` means nothing does.
	if calls[0].Fatal != "" {
		t.Fatalf("an absent fatal parsed as %q", calls[0].Fatal)
	}
	if calls[1].Fatal != "err" {
		t.Fatalf("fatal = %q", calls[1].Fatal)
	}
	if calls[2].Fatal != "none" {
		t.Fatalf("`fatal: none` parsed as %q", calls[2].Fatal)
	}
}

func TestParseCallsRefusals(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"no list", "prompt: \"do the thing\"\n", `no "calls:" list`},
		{"prose around it", "do the thing:\ncalls:\n- tool: t\n", "not a tony"},
		{"empty list", "calls: []\n", `"calls:" is empty`},
		{"no tool", "calls:\n- input: {a: b}\n", `no "tool:"`},
		{"input is not a map", "calls:\n- tool: t\n  input: hello\n", "input must be a map"},
		{"unknown outcome", "calls:\n- tool: t\n  fatal: sideways\n", "not one of err, blocked, nonzero, none"},
		{"none alongside something", "calls:\n- tool: t\n  fatal: \"none,err\"\n", "stand alone"},
		{"an unquoted comma, which tony refuses", "calls:\n- tool: t\n  fatal: err,blocked\n", "not a tony document"},
		{"fatal as a tony list", "calls:\n- tool: t\n  fatal: [err]\n", "not a tony list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCalls(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
