package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/task"
)

func askCall(id, question string, options ...string) toolCall {
	tc := mkTC(id, askTool)
	args := map[string]any{"question": question}
	if len(options) > 0 {
		args["options"] = options
	}
	b, _ := json.Marshal(args)
	tc.Function.Arguments = string(b)
	return tc
}

// askRun builds a task whose model asks once and then stops, and runs it against reg. It
// returns the fake (to read what the model was told) and the run's end.
func askRun(t *testing.T, ctx context.Context, reg *task.Registry, vec *task.Vector, call toolCall) (*fakeCompleter, <-chan error) {
	t.Helper()
	fake := &fakeCompleter{turns: []scriptedTurn{
		{msg: chatMessage{Role: "assistant", ToolCalls: []toolCall{call}}, finish: "tool_calls"},
		{msg: chatMessage{Role: "assistant", Content: "thanks"}, finish: "stop"},
	}}
	p, _ := Build(Config{ID: "t1", Prompt: "chat", Root: t.TempDir(), Resolver: reg, Client: fake, Vector: vec})
	done := make(chan error, 1)
	go func() {
		_, err := p.Run(ctx)
		done <- err
	}()
	return fake, done
}

// pendingAsk waits for the task's question to reach the docket.
func pendingAsk(t *testing.T, reg *task.Registry) task.Pending {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ps := reg.Pending(); len(ps) > 0 {
			return ps[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the ask never reached the docket")
	return task.Pending{}
}

// toldAfterAsk is what the model read as the ask's result: the tool message of the second
// model call.
func toldAfterAsk(t *testing.T, fake *fakeCompleter) string {
	t.Helper()
	if len(fake.seen) < 2 {
		t.Fatalf("the model was called %d time(s), want 2", len(fake.seen))
	}
	msgs := fake.seen[1]
	last := msgs[len(msgs)-1]
	if last.Role != "tool" || last.Name != askTool {
		t.Fatalf("the last message before the second call is %s/%s, want the ask's result", last.Role, last.Name)
	}
	return last.Content
}

// AN ASK IS A QUESTION ON THE DOCKET, and its answer is what the model reads next.
func TestAskBlocksOnTheDocketAndReturnsTheAnswer(t *testing.T) {
	reg := task.NewRegistry()
	fake, done := askRun(t, context.Background(), reg, nil, askCall("c1", "which one?"))

	p := pendingAsk(t, reg)
	if p.Tool != askTool || p.Question.Reason != "which one?" || p.Question.Origin != "t1" {
		t.Fatalf("the docket holds %+v", p)
	}
	if !strings.HasPrefix(p.Question.ID, "t1"+task.GateSep) {
		t.Fatalf("question id %q does not carry the task's id, so no answer could find it", p.Question.ID)
	}
	if len(p.Question.Options) != 1 || p.Question.Options[0].ID != replyOption {
		t.Fatalf("an unnamed question offers %+v, want the one option %q", p.Question.Options, replyOption)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: replyOption, By: "user:t", Steer: []string{"the second"}})
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := toldAfterAsk(t, fake); got != "user:t: the second" {
		t.Fatalf("the model read %q", got)
	}
}

// A NAMED OUTCOME COMES BACK BY ITS ID.
func TestAskWithOptionsReturnsTheChoice(t *testing.T) {
	reg := task.NewRegistry()
	fake, done := askRun(t, context.Background(), reg, nil, askCall("c1", "a or b?", "a", "b"))
	p := pendingAsk(t, reg)
	if len(p.Question.Options) != 2 || p.Question.Options[1].ID != "b" {
		t.Fatalf("options %+v", p.Question.Options)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: "b", By: "user:t"})
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := toldAfterAsk(t, fake); got != "user:t chose b" {
		t.Fatalf("the model read %q", got)
	}
}

// ASKING IS NOT ASKED ABOUT. Under a vector whose default is `ask`, every action is a question
// for the supervisor — and the ask itself must not be one, or the person is asked whether the
// task may ask them, and only then what it wanted to know.
func TestAskIsNotGatedByTheVector(t *testing.T) {
	reg := task.NewRegistry()
	var posted int
	reg.OnPost(func(task.Pending) { posted++ })
	fake, done := askRun(t, context.Background(), reg, &task.Vector{Default: task.Ask}, askCall("c1", "go on?"))
	p := pendingAsk(t, reg)
	if p.Tool != askTool || p.Question.Gate != askTool {
		t.Fatalf("the first question on the docket is the supervisor's, not the task's: %+v", p)
	}
	reg.Answer(p.Question.ID, task.Resolution{Choice: replyOption, By: "user:t", Steer: []string{"yes"}})
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if posted != 1 {
		t.Fatalf("%d questions were posed, want exactly the ask", posted)
	}
	if got := toldAfterAsk(t, fake); got != "user:t: yes" {
		t.Fatalf("the model read %q", got)
	}
}

// A CANCELLED ASK LEAVES THE DOCKET: the question is withdrawn with its task, not left for
// somebody to answer into nothing.
func TestACancelledAskLeavesTheDocket(t *testing.T) {
	reg := task.NewRegistry()
	dropped := make(chan string, 1)
	reg.OnDrop(func(q task.GateQuestion, _ string) { dropped <- q.ID })
	ctx, cancel := context.WithCancel(context.Background())
	_, done := askRun(t, ctx, reg, nil, askCall("c1", "still there?"))
	p := pendingAsk(t, reg)
	cancel()
	<-done
	select {
	case id := <-dropped:
		if id != p.Question.ID {
			t.Fatalf("dropped %q, want %q", id, p.Question.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question was not withdrawn")
	}
	if ps := reg.Pending(); len(ps) != 0 {
		t.Fatalf("the docket still holds %+v", ps)
	}
}
