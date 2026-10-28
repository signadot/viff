package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
)

// A TASK'S RAISE IS A REQUEST TO VERSE, carried by the runner for the task (`question.raise`): the
// ask a person makes with `verse ask`, made as the task's owner. The runner presents its own
// token and names the task (api.TaskHeader), and verse admits it through the task's record, so
// the task holds no credential and names nobody. What verse refuses, the task reads in verse's
// words.
const OpQuestionRaise = "question.raise"

// Raiser is how the runner raises an ask at its verse for a task it hosts: POST /asks with the
// runner's own token and the task's id, the body the ask as a tony document (api.Ask). It answers
// the verse's status and what it said; an error is a request that got no answer, or one verse
// refused the runner itself on (its token, or the task it carried the request for).
type Raiser func(ctx context.Context, taskID string, ask []byte) (status int, said string, err error)

// questionRaise answers a `question.raise`: decided by the task's autonomy and the runner's
// ceiling like any act, then carried to verse.
func (s *Server) questionRaise(ctx context.Context, tk Task, req Request) Reply {
	if s.Services.Raise == nil {
		return Reply{Error: "this runner reaches no verse with a token of its own (its `verse:` block and " +
			"VERSE_TOKEN_FILE), so it cannot raise an ask for a task"}
	}
	if strings.TrimSpace(req.Ask) == "" {
		return Reply{Refused: "a raise carries the ask"}
	}
	if why := s.refused(tk, req.Decision, task.Step{Name: task.StepQuestionRaise, Fields: map[string]string{}}); why != "" {
		return Reply{Refused: why}
	}
	code, said, err := s.Services.Raise(ctx, tk.ID, []byte(req.Ask))
	switch {
	case err != nil:
		return Reply{Error: "the ask was not carried to verse: " + err.Error()}
	case code/100 == 4:
		return Reply{Refused: "the verse refused the ask: " + said}
	case code/100 != 2:
		return Reply{Error: fmt.Sprintf("the verse answered %d: %s", code, said)}
	}
	var asked api.Asked
	if err := codec.Unmarshal([]byte(said), &asked, codec.Tony); err != nil || asked.ID == "" {
		return Reply{Error: "the verse raised the ask and its answer does not say the id: " + said}
	}
	out := "raised ask " + asked.ID + ": it stands at verse.ask." + asked.ID + " until it is answered; `verse ask " + asked.ID + "` reads it"
	fmt.Fprintf(s.Log, "delivery: %s: %s\n", tk.ID, out)
	return Reply{OK: true, Out: out}
}
