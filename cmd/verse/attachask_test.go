package main

import (
	"bytes"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/harness"
	"github.com/signadot/verse/versed"
)

// serveCalls is serve with a runtime that runs a call list, the way `verse up` dispatches one:
// the scripted builder the other tests share runs a prompt as a shell command and has no list
// to run.
func serveCalls(t *testing.T) entity.Store {
	t.Helper()
	root := t.TempDir()
	build := func(l task.Launch, reg *task.Registry, content task.ContentReader) *task.Task {
		if l.Driver != task.DriverCalls {
			return versed.NewScriptedTask(l, root, task.DefaultShellEnv, reg)
		}
		return harness.Calls(harness.Config{
			ID: l.ID, Root: root, Resolver: reg, Content: content,
			Tools: l.Tools, Answer: l.Answer, Vector: l.Vector, Env: task.DefaultShellEnv,
		}, l.Calls)
	}
	store := entitytest.Store(t)
	srv := versed.NewWithStore(build, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	// Before Close, which waits for every open request: an attach still streaming a task that
	// is blocked on a question nobody answered — a failing test's shape — would hold it open.
	t.Cleanup(ts.CloseClientConnections)
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	return store
}

// syncBuf is an output a test can read while the command is still writing it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) Close() error                { return nil }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// runCLIIn is runCLI with something on stdin, started in the background: it returns the output
// as it grows and the run's end.
func runCLIIn(t *testing.T, in io.Reader, args ...string) (*syncBuf, <-chan error) {
	t.Helper()
	out := &syncBuf{}
	cc := &cli.Context{Out: out, Err: out, In: io.NopCloser(in), Go: t.Context()}
	cmd := rootCmd()
	g.Addr = testAddr
	for len(cmd.Children) > 0 && len(args) > 0 {
		if strings.HasPrefix(args[0], "-") {
			break
		}
		sub := cmd.FindSub(cc, args[0])
		if sub == nil {
			break
		}
		cmd, args = sub, args[1:]
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Run(cc, args) }()
	return out, done
}

// waitFor polls the output for a line, and says what it saw if it never comes.
func waitFor(t *testing.T, out *syncBuf, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never saw %q:\n%s", want, out.String())
}

func waitEnd(t *testing.T, out *syncBuf, done <-chan error) string {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v\n%s", err, out.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("the attach never ended:\n%s", out.String())
	}
	return out.String()
}

// pendingQuestionFor reads the id of the task's standing question off the docket record.
func pendingQuestionFor(t *testing.T, store entity.Store, taskID string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, ent := range store.List(mirror.IsQuestion) {
			o := entity.ToObj(ent.Payload)
			if o["task"] == taskID && o["status"] == mirror.QuestionPending {
				return entity.Str(ent.Payload, "id")
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pending question for %s", taskID)
	return ""
}

const askDoc = `{calls: [{tool: ask, input: {question: "which one?"}}]}`

// AN ANSWER IN WORDS REACHES THE TASK as the ask's result, from the attach prompt.
func TestAttachAnswersAnAskInWords(t *testing.T) {
	serveCalls(t)
	out, done := runCLIIn(t, strings.NewReader("the second\n"),
		"task", "run", "--driver", "calls", "--attach", "--as", "user:t", askDoc)
	got := waitEnd(t, out, done)
	if !strings.Contains(got, "user:t: the second") {
		t.Fatalf("the answer did not reach the task as its result:\n%s", got)
	}
	if !strings.Contains(got, "→ sent") {
		t.Fatalf("the prompt did not report sending:\n%s", got)
	}
}

// LINES TYPED BEFORE THE QUESTION GO AS ONE MESSAGE: what a person wrote while the task was
// working is one message with paragraphs, not a message per line.
func TestLinesTypedBeforeAnAskGoAsOneReply(t *testing.T) {
	serveCalls(t)
	out, done := runCLIIn(t, strings.NewReader("first\nsecond\n"),
		"task", "run", "--driver", "calls", "--attach", "--as", "user:t", askDoc)
	got := waitEnd(t, out, done)
	if n := strings.Count(got, "→ sent"); n != 1 {
		t.Fatalf("sent %d replies, want one:\n%s", n, got)
	}
	if !strings.Contains(got, "user:t: first") || !strings.Contains(got, "second") {
		t.Fatalf("the reply lost a line:\n%s", got)
	}
}

// A LINE TYPED BEFORE A GATE IS NOT ITS ANSWER. It waits for the next question that wants words;
// the gate is answered by somebody who saw it.
func TestALineTypedBeforeAGateIsNotItsAnswer(t *testing.T) {
	store := serveCalls(t)
	doc := `{calls: [{tool: run_bash, input: {command: "echo ran"}}, {tool: ask, input: {question: "and?"}}]}`
	out, done := runCLIIn(t, strings.NewReader("hello\n"),
		"task", "run", "--driver", "calls", "--vector", "ask", "--attach", "--as", "user:t", doc)
	waitFor(t, out, "answer it: verse gate approve")
	id := strings.TrimSpace(strings.SplitN(out.String(), "\n", 2)[0])
	qid := pendingQuestionFor(t, store, id)
	if _, err := runCLI(t, "gate", "approve", qid, "--as", "user:t"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	got := waitEnd(t, out, done)
	if !strings.Contains(got, "user:t: hello") {
		t.Fatalf("the line typed before the gate did not reach the ask:\n%s", got)
	}
	if strings.Contains(got, "denied") {
		t.Fatalf("the gate was answered by a line typed before it:\n%s", got)
	}
}

// NOTHING TYPED LEAVES THE QUESTION STANDING: the stream says how to answer it, keeps going,
// and an answer from elsewhere reaches the task.
func TestEOFLeavesTheQuestionPending(t *testing.T) {
	store := serveCalls(t)
	out, done := runCLIIn(t, strings.NewReader(""),
		"task", "run", "--driver", "calls", "--attach", "--as", "user:t", askDoc)
	waitFor(t, out, "answer it: verse gate")
	id := strings.TrimSpace(strings.SplitN(out.String(), "\n", 2)[0])
	qid := pendingQuestionFor(t, store, id)
	if _, err := runCLI(t, "gate", qid, "--answer", "reply", "--say", "from afar", "--as", "user:t"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	got := waitEnd(t, out, done)
	if !strings.Contains(got, "user:t: from afar") {
		t.Fatalf("the answer from elsewhere did not reach the task:\n%s", got)
	}
}

// A PROMPT TAKES A LINE TYPED AFTER IT, and asks again for anything that is not an answer.
func TestAPromptTakesALineTypedAfterIt(t *testing.T) {
	serveCalls(t)
	doc := `{calls: [{tool: run_bash, input: {command: "echo ran"}}]}`
	pr, pw := io.Pipe()
	defer pw.Close()
	out, done := runCLIIn(t, pr,
		"task", "run", "--driver", "calls", "--vector", "ask", "--attach", "--as", "user:t", doc)
	waitFor(t, out, "approve? [y/n]")
	io.WriteString(pw, "maybe\n")
	deadline := time.Now().Add(10 * time.Second)
	for strings.Count(out.String(), "approve? [y/n]") < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Count(out.String(), "approve? [y/n]") < 2 {
		t.Fatalf("a non-answer was not asked again:\n%s", out.String())
	}
	io.WriteString(pw, "y\n")
	got := waitEnd(t, out, done)
	if !strings.Contains(got, "→ approved") || !strings.Contains(got, "ran") {
		t.Fatalf("the approval did not run the command:\n%s", got)
	}
}

// The unit of it: what a prompt makes of a line.
func TestPromptTake(t *testing.T) {
	yn := newPrompt(task.Frame{ID: "t~d1", Options: nil})
	if c, _, ok := yn.take("Yes"); !ok || c != task.Proceed {
		t.Errorf("yes → %q %v", c, ok)
	}
	if _, _, ok := yn.take("hello"); ok {
		t.Error("a yes-or-no question took a word as an answer")
	}
	menu := newPrompt(task.Frame{ID: "t~d2", Options: []task.Option{{ID: "a"}, {ID: "b"}}})
	if c, _, ok := menu.take("2"); !ok || c != "b" {
		t.Errorf("2 → %q %v", c, ok)
	}
	if _, _, ok := menu.take("c"); ok {
		t.Error("a menu took an option it does not offer")
	}
	reply := newPrompt(task.Frame{ID: "t~3", Options: []task.Option{{ID: replyOption}}})
	if c, say, ok := reply.take("  some words "); !ok || c != replyOption || say[0] != "some words" {
		t.Errorf("words → %q %v %v", c, say, ok)
	}
}
