package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/versed"
)

// parrot is a driver that holds a conversation and proposes nothing: it goes quiet at once,
// and what it is told is what it says. It is the model-free stand-in for an agent in a
// conversation, so the turn can be exercised end to end from the CLI.
type parrot struct {
	heard []string
	emit  func(task.Frame)
}

func (d *parrot) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	if d.emit != nil {
		if n := len(d.heard); n > 0 {
			d.emit(task.Frame{Kind: task.EvSay, Text: "you said: " + d.heard[n-1]})
		} else {
			d.emit(task.Frame{Kind: task.EvSay, Text: "hello"})
		}
	}
	return task.ToolUse{}, false, nil
}
func (d *parrot) Fold(msgs []string) { d.heard = append(d.heard, msgs...) }
func (d *parrot) Result() string     { return strings.Join(d.heard, "|") }

type nopExec struct{}

func (nopExec) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{Use: tu}, nil, nil
}

// serveParrot is serve with a runtime whose `parrot` driver holds a conversation.
func serveParrot(t *testing.T) entity.Store {
	t.Helper()
	build := func(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
		if l.Driver != "parrot" {
			return versed.NewScriptedTask(l, t.TempDir(), task.DefaultShellEnv, reg)
		}
		vec := l.Vector
		if vec == nil {
			vec = &task.Vector{Default: task.Auto}
		}
		d := &parrot{}
		tk := task.NewTask(l.ID, task.NewSupervisor(vec, reg), d, nopExec{})
		d.emit = tk.Emit
		if l.Converse {
			tk.SetConverse(reg)
		}
		return tk
	}
	store := entitytest.Store(t)
	srv := versed.NewWithStore(build, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(ts.CloseClientConnections)
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	return store
}

// A CONVERSATION FROM THE TERMINAL: `task run -attach` hands the person the turn when the model
// goes quiet, a typed line is the next message, and Ctrl-D ends it — the run finishes `done`
// with the last message as its result.
func TestTaskRunAttachIsAConversationEndedByEOF(t *testing.T) {
	serveParrot(t)
	out, done := runCLIIn(t, strings.NewReader("first thing\n"),
		"task", "run", "--driver", "parrot", "--attach", "--as", "user:t", "chat")
	got := waitEnd(t, out, done)
	for _, want := range []string{"gate: turn", "→ sent", "you said: first thing", "→ ended", "text: done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "first thing\n") && strings.Count(got, "gate: turn") != 2 {
		t.Fatalf("want two turns (one answered, one ended):\n%s", got)
	}
}

// -no-converse: the run ends when the model stops, as a run with nobody attached does.
func TestNoConverseEndsWhenTheModelStops(t *testing.T) {
	serveParrot(t)
	out, done := runCLIIn(t, strings.NewReader("never read\n"),
		"task", "run", "--driver", "parrot", "--attach", "--no-converse", "--as", "user:t", "chat")
	got := waitEnd(t, out, done)
	if strings.Contains(got, "gate: turn") {
		t.Fatalf("a turn was handed over under -no-converse:\n%s", got)
	}
	if !strings.Contains(got, "text: done") {
		t.Fatalf("the run did not end:\n%s", got)
	}
}

// Without -attach nobody is there to take a turn, so no turn is posed whatever the flag says.
func TestConverseMeansNothingWithoutAttach(t *testing.T) {
	serveParrot(t)
	out, err := runCLI(t, "task", "run", "--driver", "parrot", "--wait", "--as", "user:t", "chat")
	if err != nil {
		t.Fatalf("task run -wait: %v\n%s", err, out)
	}
	if strings.Contains(out, "turn") {
		t.Fatalf("a turn was posed with nobody attached:\n%s", out)
	}
}
