package harness

import (
	"context"
	"io"
	"testing"

	"github.com/signadot/verse/task"
)

type scriptedTurn struct {
	msg    chatMessage
	finish string
}

// fakeCompleter returns scripted model turns and records the messages passed to
// each call, so a test can inspect where an injected steer lands.
type fakeCompleter struct {
	turns []scriptedTurn
	i     int
	seen  [][]chatMessage
}

func (f *fakeCompleter) complete(_ context.Context, msgs []chatMessage, _ []toolDef) (chatMessage, string, error) {
	cp := make([]chatMessage, len(msgs))
	copy(cp, msgs)
	f.seen = append(f.seen, cp)
	t := f.turns[f.i]
	f.i++
	return t.msg, t.finish, nil
}

func mkTC(id, name string) toolCall {
	var tc toolCall
	tc.ID = id
	tc.Type = "function"
	tc.Function.Name = name
	return tc
}

// A steer injected mid-multi-tool-call turn must NOT flush while tool calls are
// still being dispatched (it would interleave a user message between an assistant
// tool_calls and its required tool results). It folds in only once the pending
// buffer drains — as the last user message before the next model turn.
func TestSteerFoldsInAfterPendingDrains(t *testing.T) {
	fake := &fakeCompleter{turns: []scriptedTurn{
		{msg: chatMessage{Role: "assistant", ToolCalls: []toolCall{mkTC("c1", "t1"), mkTC("c2", "t2")}}, finish: "tool_calls"},
		{msg: chatMessage{Role: "assistant", Content: "done"}, finish: "stop"},
	}}
	d := &llmDriver{client: fake, prompt: "do it", log: io.Discard}
	ctx := context.Background()

	tu1, more, err := d.Next(ctx, task.StepResult{})
	if err != nil || !more || tu1.Name != "t1" {
		t.Fatalf("next1: name=%q more=%v err=%v", tu1.Name, more, err)
	}
	// inject a steer BETWEEN the two buffered tool calls
	d.Steer([]string{"please also X"})

	tu2, more, err := d.Next(ctx, task.StepResult{Use: tu1, Output: "r1"})
	if err != nil || !more || tu2.Name != "t2" {
		t.Fatalf("next2: name=%q more=%v err=%v", tu2.Name, more, err)
	}
	_, more, err = d.Next(ctx, task.StepResult{Use: tu2, Output: "r2"})
	if err != nil || more {
		t.Fatalf("next3: more=%v err=%v", more, err)
	}

	if len(fake.seen) != 2 {
		t.Fatalf("want 2 model calls, got %d", len(fake.seen))
	}
	// first model call: the steer must NOT be present yet
	for _, m := range fake.seen[0] {
		if m.Content == "please also X" {
			t.Fatalf("steer leaked into the first model call")
		}
	}
	// second model call: steer is the LAST message, after both tool results
	msgs := fake.seen[1]
	if last := msgs[len(msgs)-1]; last.Role != "user" || last.Content != "please also X" {
		t.Fatalf("steer not folded in as last user message: %+v", last)
	}
	tools, steerIdx := 0, -1
	for i, m := range msgs {
		if m.Role == "tool" {
			tools++
		}
		if m.Content == "please also X" {
			steerIdx = i
		}
	}
	if tools != 2 {
		t.Fatalf("want 2 tool results before the steer, got %d", tools)
	}
	if steerIdx != len(msgs)-1 {
		t.Fatalf("steer at index %d, want last (%d)", steerIdx, len(msgs)-1)
	}
}
