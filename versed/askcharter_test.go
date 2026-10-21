package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

// asking is a started verse, which asks as every verse does: the verb writes an ask and versed
// poses it, and the seed holds out creating an ask to everyone.
func asking(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	return askingOn(t, testServer(t))
}

// askingOn is asking over a server built elsewhere: one whose tasks' actions are steps
// (stepBuilder), for a test about a then-task's own gate.
func askingOn(t *testing.T, srv *Server) (*Server, *httptest.Server) {
	t.Helper()
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

// waitField waits for a record to carry a field.
func waitField(t *testing.T, srv *Server, ref entity.Ref, field string) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(ref); ok {
			if v := entity.Str(e.Payload, field); v != "" {
				return v
			}
		}
		select {
		case <-deadline:
			t.Fatalf("%s never carried %s", ref, field)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// posed waits for an ask to be pending on the docket: the ask is the question.
func posed(t *testing.T, srv *Server, ref entity.Ref) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(ref); ok && entity.Str(e.Payload, "status") == mirror.QuestionPending {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s was never posed", ref)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// thenTaskOf waits for the task an ask's `then` ran, by the id written on the ask.
func thenTaskOf(t *testing.T, srv *Server, ref entity.Ref) entity.Entity {
	t.Helper()
	id := waitField(t, srv, ref, "then-task")
	deadline := time.After(5 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(mirror.Ref(id)); ok {
			return e
		}
		select {
		case <-deadline:
			t.Fatalf("the then of %s never ran as %s", ref, id)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// AN ASK, END TO END: daniel asks davido, with what follows a yes; the ask is posed to davido
// alone, davido's yes is recorded on it, and its then runs as a task that is daniel's.
func TestAnAskIsAnsweredAndItsThenRunsForTheAsker(t *testing.T) {
	srv, ts := asking(t)
	const daniel, davido = "user:daniel@example.com", "user:oscar@example.com"
	id := askID(t, ask(t, daniel, ts.URL, `{name: land, question: "Review verse/ask?", answerers: [oscar@example.com], then: {spawn: {prompt: "merge verse/ask to main", environment: {systems: {network: {mode: open}}}}}}`))
	land := mirror.AskRef(id)
	posed(t, srv, land)
	if got := heldOutTo(t, srv, id); len(got) != 1 || got[0] != davido {
		t.Fatalf("held out to %v, want davido alone", got)
	}
	if resp := answerAs(t, davido, ts.URL, id); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("davido's answer: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if got := waitField(t, srv, land, "answer"); got != "yes" {
		t.Fatalf("the ask records %q, want yes", got)
	}
	task := thenTaskOf(t, srv, land)
	// The owner record follows the write it records, so it is waited for, not read at once.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		o, _ := entity.OwnerOf(srv.Store, task.Ref)
		if o == daniel {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("its then is %q's, want the asker's", o)
		}
	}
	if got := entity.Str(task.Payload, "prompt"); got != "merge verse/ask to main" {
		t.Fatalf("its then runs %q, want the then's prompt", got)
	}
	if got := entity.Str(task.Payload, "flow"); got != land.String() {
		t.Fatalf("its then is about %q, want the ask", got)
	}
}

// A THEN'S RECORD SAYS WHAT IT RUNS UNDER: the policy its asker wrote beside the script, as a
// spawn's is.
func TestAThenRunsRecordCarriesItsPolicy(t *testing.T) {
	srv, ts := asking(t)
	const daniel, davido = "user:daniel@example.com", "user:oscar@example.com"
	id := askID(t, ask(t, daniel, ts.URL, `{name: tag, question: "Tag v1?", answerers: [oscar@example.com],
		then: {spawn: {script: "true", environment: {systems: {network: {mode: open}, question: {ask: {}}}}, autonomy: [{match: {system: question}, then: allow}, {then: deny}]}}}`))
	tag := mirror.AskRef(id)
	posed(t, srv, tag)
	if resp := answerAs(t, davido, ts.URL, id); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("davido's answer: %s: %s", resp.Status, bodyOf(t, resp))
	}
	then := thenTaskOf(t, srv, tag)
	if p := mirror.AutonomyOf(then.Payload); len(p) != 2 || p[0].Match.System != "question" || p[0].Then.Mode != "allow" {
		t.Errorf("the then-task's record holds the policy %v, want the one written beside the script", p)
	}
	if _, script := mirror.AgentOf(then.Payload); !script {
		t.Errorf("the then-task's record is no script's: %v", entity.ToObj(then.Payload))
	}
}

// A THEN'S AUTONOMY IS CHECKED WHEN IT IS ASKED, as a run's is when it is started: a row that is no
// row, or an autonomy beside an answer, which runs nothing, is refused and no ask stands.
func TestAThensAutonomyIsCheckedWhenAsked(t *testing.T) {
	_, ts := asking(t)
	for _, then := range []string{
		`{spawn: {prompt: "go", environment: {systems: {network: {mode: open}}}, autonomy: [{then: sometimes}]}}`,
		`{spawn: {prompt: "go", environment: {systems: {network: {mode: open}}}, autonomy: [{match: {verb: push}, then: allow}, {then: deny}]}}`,
		`{answer: {ask: other, choice: proceed}, autonomy: [{then: deny}]}`,
	} {
		resp := ask(t, "user:daniel@example.com", ts.URL, `{name: bad, question: "Go?", then: `+then+`}`)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("then %s: %s %s, want 400", then, resp.Status, bodyOf(t, resp))
		}
		resp.Body.Close()
	}
}

// AN ASK WITHOUT A THEN IS A VOTE: open to anyone where it names nobody, and the answer is all it
// leaves.
func TestAnAskWithoutAThenIsAVote(t *testing.T) {
	srv, ts := asking(t)
	id := askID(t, ask(t, "user:daniel@example.com", ts.URL, `{name: name, question: "Call it runverse?"}`))
	name := mirror.AskRef(id)
	posed(t, srv, name)
	if resp := answerAs(t, "user:bob@example.com", ts.URL, id); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("an open vote refused bob: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if got := waitField(t, srv, name, "answer"); got != "yes" {
		t.Fatalf("the vote records %q, want yes", got)
	}
}

// A NO IS RECORDED AND RUNS NOTHING: the ask records `answer: no`, and its then is never a task.
func TestANoToAnAskIsRecordedAndRunsNothing(t *testing.T) {
	srv, ts := asking(t)
	const daniel, davido = "user:daniel@example.com", "user:oscar@example.com"
	id := askID(t, ask(t, daniel, ts.URL, `{name: land, question: "Merge verse/ask?", answerers: [oscar@example.com], then: {spawn: {prompt: "merge verse/ask to main", environment: {systems: {network: {mode: open}}}}}}`))
	land := mirror.AskRef(id)
	posed(t, srv, land)
	resp := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "deny", By: davido})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("davido's no: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if got := waitField(t, srv, land, "answer"); got != "no" {
		t.Fatalf("the ask records %q, want no", got)
	}
	time.Sleep(200 * time.Millisecond)
	if e, _, _ := srv.Store.Get(land); entity.Get(e.Payload, "then-task") != nil {
		t.Fatal("a no ran the then")
	}
}

// AN ASK IS NOT ANSWERED BY ITS ASKER: the asker owns the record, and answering it is still whom
// it asks'.
func TestAnAskIsNotAnsweredByItsAsker(t *testing.T) {
	srv, ts := asking(t)
	const daniel = "user:daniel@example.com"
	id := askID(t, ask(t, daniel, ts.URL, `{name: land, question: "Merge?", answerers: [oscar@example.com]}`))
	posed(t, srv, mirror.AskRef(id))
	if resp := answerAs(t, daniel, ts.URL, id); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("daniel answering his own ask: %s, want 403", resp.Status)
	}
}

// A WAITING ASK IS WAITED ON AGAIN AFTER A RESTART: the wait was the last process's, and the
// answer given after it still settles the ask and runs its then.
func TestAnAskOutlivesARestart(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	ts := httptest.NewServer(first.Handler())
	const daniel, davido = "user:daniel@example.com", "user:oscar@example.com"
	id := askID(t, ask(t, daniel, ts.URL, `{name: land, question: "Merge?", answerers: [oscar@example.com], then: {spawn: {prompt: "merge it", environment: {systems: {network: {mode: open}}}}}}`))
	land := mirror.AskRef(id)
	posed(t, first, land)
	ts.Close()
	stop()

	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())
	ts2 := httptest.NewServer(second.Handler())
	defer ts2.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := answerAs(t, davido, ts2.URL, id)
		if resp.StatusCode == http.StatusNoContent {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("davido's answer after the restart: %s: %s", resp.Status, bodyOf(t, resp))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := waitField(t, second, land, "answer"); got != "yes" {
		t.Fatalf("the ask records %q, want yes", got)
	}
	thenTaskOf(t, second, land)
}
