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
	answer := func(code int, said string) Verse {
		return func(_ context.Context, taskID, method, path string, ask []byte) (int, string, error) {
			if method != http.MethodPost || path != "/asks" {
				return http.StatusNotFound, method + " " + path, nil
			}
			saw = append(saw, call{taskID, string(ask)})
			return code, said, nil
		}
	}
	open := map[string]systems.Ceiling{"verse": systems.Open()}
	l := within(nil, "verse")
	l.Autonomy = task.Autonomy{{Match: &task.Match{System: "verse", Verb: "ask-create"}, Then: task.Then{Mode: task.ThenAllow}}, {Then: task.Then{Mode: task.ThenAllow}}}
	tk := Task{ID: "t7", Workspace: &workspace.Workspace{}, Launch: l}
	const ask = "{name: land, question: \"Land it?\"}"

	s := &Server{Log: io.Discard, Services: Services{Verse: answer(http.StatusCreated, "id: land-1a2b")}, Ceilings: open}
	if rep := s.answer(tk, frame(t, Request{Op: OpVerseAskCreate, Ask: ask})); !rep.OK || !strings.Contains(rep.Out, "raised ask land-1a2b") {
		t.Errorf("a raise: %+v", rep)
	}
	if len(saw) != 1 || saw[0] != (call{"t7", ask}) {
		t.Errorf("the raiser was handed %+v, want the task and the ask as written", saw)
	}

	s.Services.Verse = answer(http.StatusBadRequest, "then: spawn.environment.systems.network: beyond the task")
	if rep := s.answer(tk, frame(t, Request{Op: OpVerseAskCreate, Ask: ask})); !strings.Contains(rep.Refused, "the verse refused the ask: then: spawn.environment") {
		t.Errorf("a raise verse refuses: %+v", rep)
	}

	denied := tk
	dl := *l
	dl.Autonomy = task.Autonomy{{Name: "no-raise", Match: &task.Match{System: "verse", Verb: "ask-create"}, Then: task.Then{Mode: task.ThenDeny}}, {Then: task.Then{Mode: task.ThenAllow}}}
	denied.Launch = &dl
	saw = nil
	if rep := s.answer(denied, frame(t, Request{Op: OpVerseAskCreate, Ask: ask})); !strings.Contains(rep.Refused, "the autonomy's row no-raise") || len(saw) != 0 {
		t.Errorf("a raise the autonomy denies: %+v (carried %v)", rep, saw)
	}

	s.Ceilings = map[string]systems.Ceiling{}
	if rep := s.answer(tk, frame(t, Request{Op: OpVerseAskCreate, Ask: ask})); !strings.Contains(rep.Refused, "ceiling") || len(saw) != 0 {
		t.Errorf("a raise beyond the ceiling: %+v (carried %v)", rep, saw)
	}

	s.Services.Verse = nil
	if rep := s.answer(tk, frame(t, Request{Op: OpVerseAskCreate, Ask: ask})); !strings.Contains(rep.Error, "reaches no verse") {
		t.Errorf("a runner with no raiser: %+v", rep)
	}
}

// A TASK READS ONE OF ITS OWNER'S ASKS BACK, by its id or the name it was asked under, as `verse
// ask <name|id>` shows it: whether it was answered. The read is carried to verse for the task
// (GET /asks), after the task's autonomy and the runner's ceiling; a name two asks share is
// refused with their ids, rather than guessed between.
func TestATasksReadOfAnAskIsCarriedToVerse(t *testing.T) {
	var saw []string
	asks := `- {id: land-1a2b, name: land, question: "Land it?", answer: yes, status: answered}
- {id: tea-1, name: tea, question: "Tea?", status: pending, waitingOn: ["user:dana@example.com"]}
- {id: tea-2, name: tea, question: "Tea again?", status: pending}
`
	verse := func(_ context.Context, taskID, method, path string, body []byte) (int, string, error) {
		saw = append(saw, taskID+" "+method+" "+path)
		return http.StatusOK, asks, nil
	}
	l := within(nil, "verse")
	l.Autonomy = task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}
	tk := Task{ID: "t7", Workspace: &workspace.Workspace{}, Launch: l}
	s := &Server{Log: io.Discard, Services: Services{Verse: verse}, Ceilings: map[string]systems.Ceiling{"verse": systems.Open()}}

	for _, arg := range []string{"land-1a2b", "land"} {
		rep := s.answer(tk, frame(t, Request{Op: OpVerseAskGet, Ask: arg}))
		if !rep.OK || !strings.Contains(rep.Out, "answer: yes") || !strings.Contains(rep.Out, "id: land-1a2b") {
			t.Errorf("the ask read by %q: %+v", arg, rep)
		}
	}
	if len(saw) == 0 || saw[0] != "t7 GET /asks" {
		t.Errorf("the read was carried as %v, want the task's GET /asks", saw)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpVerseAskGet, Ask: "tea-1"})); !strings.Contains(rep.Out, "answer: pending") || !strings.Contains(rep.Out, "dana") {
		t.Errorf("a pending ask: %+v, want it pending and whom it waits on", rep)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpVerseAskGet, Ask: "tea"})); !strings.Contains(rep.Refused, "tea-1, tea-2") {
		t.Errorf("a name two asks share: %+v, want it refused with their ids", rep)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpVerseAskGet, Ask: "nothing"})); !strings.Contains(rep.Refused, "no ask named or numbered") {
		t.Errorf("an ask that is not there: %+v", rep)
	}

	denied := tk
	dl := *l
	dl.Autonomy = task.Autonomy{{Name: "no-read", Match: &task.Match{System: "verse", Verb: "ask-get"}, Then: task.Then{Mode: task.ThenDeny}}, {Then: task.Then{Mode: task.ThenAllow}}}
	denied.Launch = &dl
	saw = nil
	if rep := s.answer(denied, frame(t, Request{Op: OpVerseAskGet, Ask: "land"})); !strings.Contains(rep.Refused, "the autonomy's row no-read") || len(saw) != 0 {
		t.Errorf("a read the autonomy denies: %+v (carried %v)", rep, saw)
	}
	s.Services.Verse = nil
	if rep := s.answer(tk, frame(t, Request{Op: OpVerseAskGet, Ask: "land"})); !strings.Contains(rep.Error, "reaches no verse") {
		t.Errorf("a runner that reaches no verse: %+v", rep)
	}
}

// A TASK LISTS ITS OWNER'S ASKS, as `verse ask` lists them: each row with its id and its answer,
// `pending` until it settles, carried to verse for the task (GET /asks) after the task's autonomy;
// none is said in a sentence.
func TestATasksListingOfItsOwnersAsksIsCarriedToVerse(t *testing.T) {
	var saw []string
	asks := `- {id: land-1a2b, name: land, question: "Land it?", answer: yes, status: answered}
- {id: tea-1, name: tea, question: "Tea?", status: pending}
`
	verse := func(_ context.Context, taskID, method, path string, body []byte) (int, string, error) {
		saw = append(saw, taskID+" "+method+" "+path)
		return http.StatusOK, asks, nil
	}
	l := within(nil, "verse")
	l.Autonomy = task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}
	tk := Task{ID: "t7", Workspace: &workspace.Workspace{}, Launch: l}
	s := &Server{Log: io.Discard, Services: Services{Verse: verse}, Ceilings: map[string]systems.Ceiling{"verse": systems.Open()}}

	rep := s.answer(tk, frame(t, Request{Op: OpVerseAskList}))
	if !rep.OK || !strings.Contains(rep.Out, "id: land-1a2b") || !strings.Contains(rep.Out, "id: tea-1") || !strings.Contains(rep.Out, "answer: pending") {
		t.Errorf("the owner's asks listed: %+v", rep)
	}
	if len(saw) != 1 || saw[0] != "t7 GET /asks" {
		t.Errorf("the listing was carried as %v, want the task's GET /asks", saw)
	}
	asks = "[]"
	if rep := s.answer(tk, frame(t, Request{Op: OpVerseAskList})); !rep.OK || !strings.Contains(rep.Out, "no asks standing") {
		t.Errorf("no asks: %+v, want a sentence saying so", rep)
	}

	denied := tk
	dl := *l
	dl.Autonomy = task.Autonomy{{Name: "no-list", Match: &task.Match{System: "verse", Verb: "ask-list"}, Then: task.Then{Mode: task.ThenDeny}}, {Then: task.Then{Mode: task.ThenAllow}}}
	denied.Launch = &dl
	saw = nil
	if rep := s.answer(denied, frame(t, Request{Op: OpVerseAskList})); !strings.Contains(rep.Refused, "the autonomy's row no-list") || len(saw) != 0 {
		t.Errorf("a listing the autonomy denies: %+v (carried %v)", rep, saw)
	}
	s.Services.Verse = nil
	if rep := s.answer(tk, frame(t, Request{Op: OpVerseAskList})); !strings.Contains(rep.Error, "reaches no verse") {
		t.Errorf("a runner that reaches no verse: %+v", rep)
	}
}
