package delivery

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// A TASK'S RAISE IS CARRIED TO VERSE FOR THE TASK, after the task's autonomy and the runner's
// ceiling, each read again here: the raiser is handed the task's id and the ask as written, and
// what verse refuses the task reads in verse's words. A runner with no raiser says it reaches no
// verse.
func TestATasksRaiseIsCarriedToVerseForTheTask(t *testing.T) {
	type call struct{ task, ask string }
	var saw []call
	answer := func(code int, said string) Raiser {
		return func(_ context.Context, taskID string, ask []byte) (int, string, error) {
			saw = append(saw, call{taskID, string(ask)})
			return code, said, nil
		}
	}
	open := map[string]systems.Ceiling{"question": systems.Open()}
	l := within(nil, "question")
	l.Autonomy = task.Autonomy{{Match: &task.Match{System: "question", Verb: "raise"}, Then: task.Then{Mode: task.ThenAllow}}, {Then: task.Then{Mode: task.ThenAllow}}}
	tk := Task{ID: "t7", Workspace: &workspace.Workspace{}, Launch: l}
	const ask = "{name: land, question: \"Land it?\"}"

	s := &Server{Log: io.Discard, Services: Services{Raise: answer(http.StatusCreated, "id: land-1a2b")}, Ceilings: open}
	if rep := s.answer(tk, frame(t, Request{Op: OpQuestionRaise, Ask: ask})); !rep.OK || !strings.Contains(rep.Out, "raised ask land-1a2b") {
		t.Errorf("a raise: %+v", rep)
	}
	if len(saw) != 1 || saw[0] != (call{"t7", ask}) {
		t.Errorf("the raiser was handed %+v, want the task and the ask as written", saw)
	}

	s.Services.Raise = answer(http.StatusBadRequest, "then: spawn.environment.systems.network: beyond the task")
	if rep := s.answer(tk, frame(t, Request{Op: OpQuestionRaise, Ask: ask})); !strings.Contains(rep.Refused, "the verse refused the ask: then: spawn.environment") {
		t.Errorf("a raise verse refuses: %+v", rep)
	}

	denied := tk
	dl := *l
	dl.Autonomy = task.Autonomy{{Name: "no-raise", Match: &task.Match{System: "question", Verb: "raise"}, Then: task.Then{Mode: task.ThenDeny}}, {Then: task.Then{Mode: task.ThenAllow}}}
	denied.Launch = &dl
	saw = nil
	if rep := s.answer(denied, frame(t, Request{Op: OpQuestionRaise, Ask: ask})); !strings.Contains(rep.Refused, "the autonomy's row no-raise") || len(saw) != 0 {
		t.Errorf("a raise the autonomy denies: %+v (carried %v)", rep, saw)
	}

	s.Ceilings = map[string]systems.Ceiling{}
	if rep := s.answer(tk, frame(t, Request{Op: OpQuestionRaise, Ask: ask})); !strings.Contains(rep.Refused, "ceiling") || len(saw) != 0 {
		t.Errorf("a raise beyond the ceiling: %+v (carried %v)", rep, saw)
	}

	s.Services.Raise = nil
	if rep := s.answer(tk, frame(t, Request{Op: OpQuestionRaise, Ask: ask})); !strings.Contains(rep.Error, "reaches no verse") {
		t.Errorf("a runner with no raiser: %+v", rep)
	}
}
