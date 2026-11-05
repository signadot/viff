package versed

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/task"
)

// sayTo is words said to a running task, as an ask to the task (9ce3bva0, stage 6): recorded at
// verse.ask.<id>, its asker's, about the task and read as the task is, with no clock — a
// conversation turn has none. Where the task waits on the speaker's turn, the words are the
// answer to it; otherwise the task takes them at its next boundary and the ask settles as heard
// (mirror.HearSay). It answers the ask's id.
//
// THE RECORD BEFORE THE WORDS, so the task's say frame, echoing the ask, finds it standing.
func (s *Server) sayTo(r *http.Request, taskID string, h task.Handle, words, by string) (string, error) {
	askID, err := spawnID(api.Spawn{Name: "say"})
	if err != nil {
		return "", err
	}
	ref, about := mirror.AskRef(askID), mirror.Ref(taskID)
	rec := entity.Obj{
		"name": "say", mirror.FromField: mirror.FromPrincipal(by), mirror.ToField: entity.Obj{"task": taskID},
		"question": words, "pose": words, "reason": words, "edit": 1, "subject": about.String(),
		"status": mirror.QuestionPending, "posedAt": s.now().UTC().Format(time.RFC3339),
	}
	answered := false
	if turn := s.turnOf(taskID); turn != "" {
		rw := httptest.NewRecorder()
		s.answerQuestion(rw, r, api.Resolve{ID: turn, Choice: task.Reply, Say: []string{words}, By: by},
			taskID, s.scopedStore(r))
		if rw.Code < 300 {
			answered = true
			rec["status"], rec["verdict"], rec["answer"] = mirror.QuestionAnswered, mirror.Heard, mirror.Heard
			rec["by"], rec["turn"] = about.String(), turn
			rec["answeredAt"] = s.now().UTC().Format(time.RFC3339)
		}
	}
	grants := []entity.Grant{{At: ref.Segs(), To: map[entity.Level][]string{entity.Read: {entity.Audience(about)}}}}
	if err := entity.Establish(s.Store, ref, by, entity.Ref{}, grants); err != nil {
		return "", err
	}
	if _, _, err := s.scopedStore(r).Write(entity.Write{
		At: ref.At(), Value: rec.Payload(), When: []entity.Require{entity.Created()},
	}); err != nil {
		return "", fmt.Errorf("say: %w", err)
	}
	if !answered {
		if err := h.Steer(words, by, askID); err != nil {
			mirror.DropQuestion(entity.As(s.Store, entity.SystemDocket), askID,
				"the task could not be told: "+strings.TrimSpace(err.Error()))
			return "", err
		}
	}
	return askID, nil
}

// turnOf is the conversation turn a task stands waiting at, "" where it is not at one.
func (s *Server) turnOf(taskID string) string {
	ents, err := pendingQuestionsErr(entity.As(s.Store, entity.SystemDocket))
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if mirror.TaskFrom(e.Payload) == taskID && entity.Str(e.Payload, "gate") == "turn" {
			return e.Ref.Name()
		}
	}
	return ""
}
