package main

import (
	"context"
	"github.com/signadot/verse/internal/versed/versedtest"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/stock"
	"github.com/signadot/verse/pkg/systems/systemstest"
	"github.com/signadot/verse/pkg/task"
)

// parrot is a driver that holds a conversation and proposes nothing: it goes quiet at once,
// and what it is told is what it says. It is the model-free stand-in for an agent in a
// conversation, so the turn can be exercised end to end from the CLI.
type parrot struct {
	heard []string
	emit  func(task.Event)
}

func (d *parrot) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	if d.emit != nil {
		if n := len(d.heard); n > 0 {
			d.emit(task.Event{Kind: task.EventSay, Text: "you said: " + d.heard[n-1]})
		} else {
			d.emit(task.Event{Kind: task.EventSay, Text: "hello"})
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
	build := func(l runner.Launch, reg *task.Registry) *task.Task {
		if l.Who() != "parrot" {
			return stock.New(stock.Open(), stock.Secrets{}, t.TempDir(), io.Discard).Build(l, reg)
		}
		d := &parrot{}
		tk := task.NewTask(l.ID, systemstest.Opened(task.NewSupervisor(l.Environment, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, reg), l.Environment), d, nopExec{})
		d.emit = tk.Emit
		if l.Converse {
			tk.SetConverse(reg)
		}
		return tk
	}
	store := entitytest.Store(t)
	srv := versedtest.Server(t, build, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(ts.CloseClientConnections)
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	return store
}

// parrotSpawn is the spawn of a conversation with the parrot agent.
const parrotSpawn = `{prompt: chat, agent: {name: parrot}, environment: {systems: {network: {mode: open}}}}`

// A CONVERSATION FROM THE TERMINAL: `task run -attach` hands the person the turn when the model
// goes quiet, a typed line is the next message, and Ctrl-D ends it — the run finishes `done`
// with the last message as its result.
func TestTaskRunAttachIsAConversationEndedByEOF(t *testing.T) {
	serveParrot(t)
	out, done := runCLIIn(t, strings.NewReader("first thing\n"),
		"task", "run", "-f", spawnFile(t, parrotSpawn), "--attach", "--as", "user:t")
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
		"task", "run", "-f", spawnFile(t, parrotSpawn), "--attach", "--no-converse", "--as", "user:t")
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
	out, err := runCLI(t, "task", "run", "-f", spawnFile(t, parrotSpawn), "--wait", "--as", "user:t")
	if err != nil {
		t.Fatalf("task run -wait: %v\n%s", err, out)
	}
	if strings.Contains(out, "turn") {
		t.Fatalf("a turn was posed with nobody attached:\n%s", out)
	}
}

// A SAY WHILE THE TURN STANDS, THEN A REPLY AT THE NEXT TURN — the sequence a person produces
// with two terminals: the say withdraws the standing turn and is folded, the model speaks, a new
// turn is posed, and the reply typed at its prompt answers THAT turn.
func TestASayWhileTheTurnStandsDoesNotBreakTheNextTurn(t *testing.T) {
	serveParrot(t)
	pr, pw := io.Pipe()
	defer pw.Close()
	// As the test config's own user, so the out-of-band say (which takes no --as) holds control.
	out, done := runCLIIn(t, pr, "task", "run", "-f", spawnFile(t, parrotSpawn), "--attach", "--as", "user:tester@example.com")
	waitFor(t, out, "id: ")
	// The first line that is not task run's word to stderr (the parrot is given no system, and
	// is told so) is the id.
	var id string
	for _, line := range strings.Split(out.String(), "\n") {
		if !strings.HasPrefix(line, "task run:") {
			id = strings.TrimSpace(line)
			break
		}
	}
	waitFor(t, out, "~t1 kind: pending")
	if _, err := runCLI(t, "task", "say", id, "hello oob"); err != nil {
		t.Fatalf("say: %v", err)
	}
	waitFor(t, out, "~t2 kind: pending")
	io.WriteString(pw, "anything\n")
	waitFor(t, out, "→ sent")
	pw.Close()
	got := waitEnd(t, out, done)
	if strings.Contains(got, "404") || strings.Contains(got, "no such question") {
		t.Fatalf("the reply at the second turn was refused:\n%s", got)
	}
	if !strings.Contains(got, "~t2 kind: decision") {
		t.Fatalf("the second turn was not answered by the typed reply:\n%s", got)
	}
	if !strings.Contains(got, "you said: anything") {
		t.Fatalf("the reply did not reach the driver as the turn's answer:\n%s", got)
	}
}
