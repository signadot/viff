package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

// slowDriver takes `steps` actions, one every tick, and — when talks — holds a conversation
// into which words said to it are folded.
type slowDriver struct {
	steps int
	mu    sync.Mutex
	heard []string
}

func (d *slowDriver) Next(ctx context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	if d.steps <= 0 {
		return task.ToolUse{}, false, nil
	}
	d.steps--
	select {
	case <-time.After(40 * time.Millisecond):
	case <-ctx.Done():
		return task.ToolUse{}, false, ctx.Err()
	}
	return task.ToolUse{Name: "noop"}, true, nil
}

// talkingDriver is a slowDriver that holds a conversation.
type talkingDriver struct{ *slowDriver }

func (d talkingDriver) Fold(msgs []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.heard = append(d.heard, msgs...)
}

type noopExec struct{}

func (noopExec) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{Use: tu, Output: "ok"}, nil, nil
}

// sayServer hosts tasks driven as the prompt says: `talk` a conversation of many slow steps,
// `mute` many slow steps and no conversation, `turn` a conversation that goes quiet at once.
func sayServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	build := func(l task.Launch, reg *task.Registry) *task.Task {
		var d task.Driver
		switch l.Prompt {
		case "talk":
			d = talkingDriver{&slowDriver{steps: 200}}
		case "turn":
			d = talkingDriver{&slowDriver{}}
		default:
			d = &slowDriver{steps: 10}
		}
		tk := task.NewTask(l.ID, task.NewSupervisor(&task.Vector{Default: task.Auto}, reg), d, noopExec{})
		if l.Converse {
			tk.SetConverse(reg)
		}
		return tk
	}
	srv := NewWithStore(runnerd.Builder(build), entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func spawnSaying(t *testing.T, url, as, id, prompt string, converse bool) {
	t.Helper()
	b, _ := codec.Marshal(map[string]any{"id": id, "prompt": prompt, "converse": converse}, codec.Tony)
	resp := postAs(t, as, url+"/tasks", "application/tony", string(b))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spawn %s: %s: %s", id, resp.Status, bodyOf(t, resp))
	}
}

func sayTo(t *testing.T, url, as, id, words string) string {
	t.Helper()
	b, _ := codec.Marshal(api.Say{Instruction: words, By: as}, codec.Tony)
	resp := postAs(t, as, url+"/tasks/"+id+"/say", "application/tony", string(b))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("say to %s: %s: %s", id, resp.Status, bodyOf(t, resp))
	}
	var out api.Asked
	if err := client.Decode(resp, &out); err != nil {
		t.Fatal(err)
	}
	return out.ID
}

func waitAsk(t *testing.T, srv *Server, id, status string) entity.Payload {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok, _ := srv.Store.Get(mirror.AskRef(id)); ok && entity.Str(e.Payload, "status") == status {
			return e.Payload
		}
		time.Sleep(20 * time.Millisecond)
	}
	e, _, _ := srv.Store.Get(mirror.AskRef(id))
	t.Fatalf("ask %s never became %s: %v", id, status, fmtPayload(e.Payload))
	return nil
}

// WORDS TO A RUNNING TASK ARE AN ASK TO IT (9ce3bva0, stage 6): recorded as the speaker's, about the
// task, with no clock, and heard when the task takes them at its next boundary.
func TestWordsToARunningTaskAreAnAskItHears(t *testing.T) {
	srv, ts := sayServer(t)
	const alice = "user:alice@example.com"
	spawnSaying(t, ts.URL, alice, "run-talk", "talk", false)
	id := sayTo(t, ts.URL, alice, "run-talk", "use the staging bucket")
	rec := waitAsk(t, srv, id, mirror.QuestionAnswered)
	if entity.Str(rec, "answer") != mirror.Heard || entity.Str(rec, "asker") != alice ||
		entity.Str(rec, "subject") != mirror.Ref("run-talk").String() || entity.Get(rec, "deadline") != nil {
		t.Fatalf("the say settled as %s", fmtPayload(rec))
	}
	if docket := bodyOf(t, getAs(t, alice, ts.URL+"/asks?all=true")); strings.Contains(docket, "use the staging bucket") {
		t.Fatalf("words to a task are on the docket: %s", docket)
	}
}

// Where the task waits on the speaker's turn, the words are the answer to it.
func TestWordsToATaskWaitingOnYourTurnAnswerIt(t *testing.T) {
	srv, ts := sayServer(t)
	const alice = "user:alice@example.com"
	spawnSaying(t, ts.URL, alice, "run-turn", "turn", true)
	deadline := time.Now().Add(10 * time.Second)
	for srv.turnOf("run-turn") == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	turn := srv.turnOf("run-turn")
	if turn == "" {
		t.Fatal("the conversation never handed alice the turn")
	}
	id := sayTo(t, ts.URL, alice, "run-turn", "what did you find?")
	rec := waitAsk(t, srv, id, mirror.QuestionAnswered)
	if entity.Str(rec, "turn") != turn {
		t.Fatalf("the say did not answer the turn %s: %s", turn, fmtPayload(rec))
	}
	if q := settledQuestion(t, srv, turn); q["verdict"] != task.Reply || q["by"] != alice {
		t.Fatalf("the turn settled %v by %v, want alice's reply: %v", q["verdict"], q["by"], q)
	}
}

// Words a task never takes — it holds no conversation — are withdrawn when it ends, saying so.
func TestWordsATaskEndsWithoutHearingAreWithdrawn(t *testing.T) {
	srv, ts := sayServer(t)
	const alice = "user:alice@example.com"
	spawnSaying(t, ts.URL, alice, "run-mute", "mute", false)
	id := sayTo(t, ts.URL, alice, "run-mute", "anyone there?")
	// While it waits, it is no person's to answer, and not on the docket.
	if docket := bodyOf(t, getAs(t, alice, ts.URL+"/asks?all=true")); strings.Contains(docket, "anyone there?") {
		t.Fatalf("words to a task are on the docket: %s", docket)
	}
	rec := waitAsk(t, srv, id, mirror.QuestionOrphaned)
	if !strings.Contains(entity.Str(rec, "why"), "before it heard this") {
		t.Fatalf("the unheard say does not say why: %s", fmtPayload(rec))
	}
}

// Words to a task are the task's to settle: a restart does not wait on one as it waits on a
// person's ask, which would put it on the docket for people.
func TestARestartDoesNotAskPeopleWhatWasSaidToATask(t *testing.T) {
	srv, _ := sayServer(t)
	ref := mirror.AskRef("say-orphan")
	if err := entity.Establish(srv.Store, ref, "user:alice@example.com", entity.Ref{}, []entity.Grant{}); err != nil {
		t.Fatal(err)
	}
	entitytest.Set(t, srv.Store, ref, entity.Obj{
		"asker": "user:alice@example.com", "question": "hello", "status": mirror.QuestionPending,
		mirror.SaidToField: "run-gone", "subject": mirror.Ref("run-gone").String(),
	}.Payload())
	if err := srv.resumeAsks(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	for _, p := range srv.Reg.Pending() {
		if p.Question.ID == "say-orphan" {
			t.Fatal("a restart waited on words said to a task, as on a person's ask")
		}
	}
}

// Words to a flow are words to each task it reaches: an ask to each.
func TestWordsToAFlowAreAnAskToEachTask(t *testing.T) {
	srv, ts := sayServer(t)
	const alice = "user:alice@example.com"
	spawnSaying(t, ts.URL, alice, "run-flow", "talk", false)
	resp := postAs(t, alice, ts.URL+"/flow/say", "application/tony", `{task: run-flow, text: "wrap it up", by: "`+alice+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("flow say: %s: %s", resp.Status, bodyOf(t, resp))
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range entity.Below(srv.Store, mirror.IsAsk, 1, entity.VerseRoot, mirror.AskSystem) {
			if entity.Str(e.Payload, mirror.SaidToField) == "run-flow" && entity.Str(e.Payload, "answer") == mirror.Heard {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the flow's words were never an ask the task heard")
}
