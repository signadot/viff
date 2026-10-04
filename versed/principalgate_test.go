package versed

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/taskd"
)

// runnerID is a principal a request may present that is no person: a runner acting as itself.
const runnerID = "runner:ci-1"

// waitingOn is who a question waits on, as its record says.
func waitingOn(t *testing.T, srv *Server, id string) []string {
	t.Helper()
	e, _, _ := srv.Store.Get(mirror.QuestionRef(id))
	return entity.Strs(e.Payload, mirror.WaitingOnField)
}

// A GATE NAMING $principal ASKS WHOEVER RAN THE TASK, and the task's record says whom it acts for.
// A gate naming nobody is the docket's (TestARunsGateTakesAThreshold).
func TestAGateNamingPrincipalAsksWhoeverRanTheTask(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	gates := shellGate(0, nil)
	gates[0].Answerers = []string{task.PrincipalAnswerer}
	resp := postTony(t, ts.URL+"/tasks", api.Spawn{ID: "mine", Prompt: "true", Gates: gates})
	if resp.StatusCode >= 300 {
		t.Fatalf("spawn = %s: %s", resp.Status, bodyOf(t, resp))
	}
	resp.Body.Close()
	me := "user:" + tester
	if got := waitingOn(t, srv, waitQuestion(t, srv, "mine")); !slices.Equal(got, []string{me}) {
		t.Fatalf("the gate waits on %v, want %s alone", got, me)
	}
	if e, _, _ := srv.Store.Get(mirror.Ref("mine")); entity.Str(e.Payload, "asker") != me {
		t.Errorf("the task's record says it acts for %q, want %s", entity.Str(e.Payload, "asker"), me)
	}
}

// AN ASK'S `then` ASKS ITS ASKER: the task acts for daniel, so its own question, naming nobody,
// waits on him rather than on the docket, and its record keeps that across a restart.
func TestAThenTasksOwnQuestionAsksTheAsker(t *testing.T) {
	srv, ts := asking(t)
	const daniel, davido = "user:daniel@example.com", "user:oscar@example.com"
	id := askID(t, ask(t, daniel, ts.URL, `{name: land, question: "Land it?", answerers: [oscar@example.com], then: {prompt: "true"}}`))
	land := mirror.AskRef(id)
	posed(t, srv, land)
	if resp := answerAs(t, davido, ts.URL, id); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("davido's answer: %s: %s", resp.Status, bodyOf(t, resp))
	}
	then := thenTaskOf(t, srv, land)
	if got := waitingOn(t, srv, waitQuestion(t, srv, then.Ref.Name())); !slices.Equal(got, []string{daniel}) {
		t.Fatalf("the then-task's question waits on %v, want daniel alone", got)
	}
	if got := entity.Strs(then.Payload, "answerers"); !slices.Equal(got, []string{task.PrincipalAnswerer}) {
		t.Errorf("the then-task's record holds answerers %v, want [%s]", got, task.PrincipalAnswerer)
	}
}

// A RUN BY NOBODY CANNOT NAME $principal: a runner acting as itself is no person to ask, so the
// spawn is refused rather than its gate left to the docket.
func TestAGateNamingPrincipalIsRefusedForNobody(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	gates := shellGate(0, nil)
	gates[0].Answerers = []string{task.PrincipalAnswerer}
	b, err := codec.Marshal(api.Spawn{ID: "nobodys", Prompt: "true", Gates: gates}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	resp := postAs(t, runnerID, ts.URL+"/tasks", "application/tony", string(b))
	if msg := bodyOf(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, "acts for nobody") {
		t.Fatalf("a runner's run naming $principal: %s %q, want 400 saying it acts for nobody", resp.Status, msg)
	}
}

// SLIDING A GATE NAMING $principal ONTO A RUNNING TASK is refused as its spawn would be: a run a
// runner started acts for nobody, and one a person started acts for them.
func TestAVectorNamingPrincipalNeedsSomebodyToAsk(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	slide := func(as, id string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/tasks/"+id+"/vector", strings.NewReader(
			`{vector: {default: auto, gates: [{name: shell, mode: ask, answerers: [$principal]}]}}`))
		req.Header.Set("Content-Type", "application/tony")
		req.Header.Set(api.PrincipalHeader, as)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	for _, c := range []struct{ as, id string }{{runnerID, "runners"}, {tester, "testers"}} {
		b, _ := codec.Marshal(api.Spawn{ID: c.id, Prompt: "sleep 5", Vector: "auto"}, codec.Tony)
		if resp := postAs(t, c.as, ts.URL+"/tasks", "application/tony", string(b)); resp.StatusCode >= 300 {
			t.Fatalf("spawn %s: %s: %s", c.id, resp.Status, bodyOf(t, resp))
		}
		waitField(t, srv, mirror.Ref(c.id), "status")
	}
	if resp := slide(runnerID, "runners"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("$principal onto a runner's run: %s %s, want 400", resp.Status, bodyOf(t, resp))
	} else if msg := bodyOf(t, resp); !strings.Contains(msg, "acts for nobody") {
		t.Errorf("$principal onto a runner's run: refused for another reason: %s", msg)
	}
	if resp := slide(tester, "testers"); resp.StatusCode != http.StatusNoContent {
		t.Errorf("$principal onto the tester's run: %s %s, want 204", resp.Status, bodyOf(t, resp))
	}
}

// A CONVERSATION ON A REMOTE RUNNER HANDS THE TURN TO WHOEVER RAN IT, as a local one does: the
// runner's frame says the turn names $principal, verse puts the launcher there, and the record
// says it waits on them.
func TestARemoteConversationsTurnIsItsLaunchers(t *testing.T) {
	build := func(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
		tk := task.NewTask(l.ID, task.NewSupervisor(&task.Vector{Default: task.Auto}, reg),
			talkingDriver{&slowDriver{}}, noopExec{})
		if l.Converse {
			tk.SetConverse(reg)
		}
		return tk
	}
	face := httptest.NewServer(taskd.New(build).Handler())
	t.Cleanup(face.Close)
	var spawned string
	t.Cleanup(func() { // end the conversation at the runner, so its stream closes
		if q := pendingOn(t, face.URL, spawned); spawned != "" && q != "" {
			postTony(t, face.URL+"/tasks/"+spawned+"/resolve", map[string]any{
				"id": q, "choice": task.End, "by": "cleanup",
			}).Body.Close()
		}
	})

	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	postTony(t, ts.URL+"/runners", map[string]any{"name": "face", "url": face.URL}).Body.Close()
	resp := postTony(t, ts.URL+"/tasks", map[string]any{
		"prompt": "turn", "runner": "verse.runners.face", "name": "talk", "converse": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spawn = %s: %s", resp.Status, bodyOf(t, resp))
	}
	var sp map[string]any
	decodeResp(t, resp, &sp)
	spawned, _ = sp["id"].(string)
	me := "user:" + tester
	if got := waitingOn(t, srv, waitQuestion(t, srv, spawned)); !slices.Equal(got, []string{me}) {
		t.Fatalf("the remote turn waits on %v, want %s alone", got, me)
	}
}
