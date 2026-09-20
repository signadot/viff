package versed

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// A runner reached by url acts as itself: the identity on its entity is listed as a runner
// principal, granted the task at spawn, and every report the mirror projects is written as it —
// not as the charter's owner whose work it runs.
func TestARunnerReachedByUrlActsAsItself(t *testing.T) {
	srv := testServer(t)
	face := runnerFace(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp := postAs(t, "ops@example.com", ts.URL+"/runners", "application/tony",
		fmt.Sprintf(`{name: gpu, url: %q, identity: gpu-bot}`, face.URL))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("runner add: %s", resp.Status)
	}

	from := srv.Store.Head()
	resp = postAs(t, "alice@example.com", ts.URL+"/tasks", "application/tony",
		`{prompt: "go", driver: scripted, runner: verse.runners.gpu}`)
	var sp struct {
		ID string `tony:"field=id"`
	}
	decodeResp(t, resp, &sp)
	resp.Body.Close()
	if sp.ID == "" {
		t.Fatal("no id from the spawn")
	}
	taskRef := mirror.Ref(sp.ID)
	if got := owner(t, srv, taskRef); got != "user:alice@example.com" {
		t.Fatalf("the task is owned by %q, want the requester", got)
	}

	// The grant, the listing, and a report written as the runner.
	deadline := time.Now().Add(3 * time.Second)
	granted := false
	for time.Now().Before(deadline) && !granted {
		granted, _ = entity.Holds(srv.Store, "runner:gpu-bot", entity.Control, taskRef)
		time.Sleep(10 * time.Millisecond)
	}
	if !granted {
		t.Fatal("gpu-bot was not granted control of the task it runs")
	}
	if ok, _ := entity.MayCreate(srv.Store, "runner:gpu-bot", taskRef); ok {
		t.Fatal("gpu-bot owns the task")
	}
	if _, ok, _ := srv.Store.Get(entity.PrincipalRef(entity.RunnerKind, "gpu-bot")); !ok {
		t.Fatal("gpu-bot is not listed as a runner")
	}
	by := ""
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && by != "runner:gpu-bot" {
		commits, err := srv.Store.Since(from, entity.PrefixPattern(taskRef.Segs(), len(taskRef.Segs())))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range commits {
			if c.Changed(taskRef) && c.Author == "runner:gpu-bot" {
				by = c.Author
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if by != "runner:gpu-bot" {
		t.Fatal("no report on the task was written as the runner")
	}
	if refused, _ := entity.PolicyOf(srv.Store).Refusals(); len(refused) != 0 {
		t.Fatalf("something was refused along the way: %v", refused)
	}
}

// A worker raising a gate says who it is for, and the question is theirs to answer; where the
// spawn's own gate of that name names answerers, the spawn's stand and the frame's are ignored.
func TestAWorkerRaisedGateIsHeldOutToWhomItNames(t *testing.T) {
	srv := testServer(t)
	b := srv.UseBroker("http://verse.test:9090")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	worker := httptest.NewServer(b.WorkerHandler())
	defer worker.Close()
	withoutTheDocketDefault(t, srv)

	spawn := func(body string) (string, int64) {
		t.Helper()
		resp := postAs(t, "alice@example.com", ts.URL+"/tasks", "application/tony", body)
		var sp struct {
			ID string `tony:"field=id"`
		}
		decodeResp(t, resp, &sp)
		resp.Body.Close()
		resp, err := worker.Client().Post(worker.URL+"/tasks/"+sp.ID+"/claim", "application/json", strings.NewReader(`{"identity":"runner:bot"}`))
		if err != nil {
			t.Fatal(err)
		}
		var claim struct {
			Epoch int64 `tony:"field=epoch"`
		}
		decodeResp(t, resp, &claim)
		resp.Body.Close()
		return sp.ID, claim.Epoch
	}
	raise := func(id string, epoch int64, gate, qid string, to ...string) {
		t.Helper()
		ev := fmt.Sprintf(`{kind: pending, id: %q, gate: %q, text: prod, answerers: [%s]}`, qid, gate, strings.Join(to, ", "))
		resp, err := worker.Client().Post(worker.URL+"/tasks/"+id+"/report", "application/tony",
			strings.NewReader(fmt.Sprintf(`{epoch: %d, events: [%s]}`, epoch, ev)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		waitDocket(t, srv, qid)
	}
	holders := func(qid string) []string {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if rec, ok, _ := srv.Store.Get(entity.PermsRef(mirror.QuestionRef(qid))); ok {
				if gs, _ := entity.ParseGrants(mirror.QuestionRef(qid).Segs(), rec.Payload); len(gs) > 0 && len(gs[0].To[entity.ReadWrite]) > 0 {
					return gs[0].To[entity.ReadWrite]
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s was never held out to anybody", qid)
		return nil
	}

	// The worker's own question, to ani.
	id, epoch := spawn(`{prompt: "deploy", driver: human, runner: broker}`)
	qid := id + "~d1"
	raise(id, epoch, "unsure", qid, "ani@example.com")
	if got := holders(qid); len(got) != 1 || got[0] != "user:ani@example.com" {
		t.Fatalf("held out to %v, want ani", got)
	}
	resp := answerAs(t, "bob@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a stranger answered: %s", resp.Status)
	}
	resp = answerAs(t, "ani@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ani could not answer what was raised to him: %s", resp.Status)
	}

	// The spawn's policy gate stands over the frame's opinion.
	id, epoch = spawn(`{prompt: "release", driver: human, runner: broker,
		gates: [{name: push, mode: ask, answerers: [oscar@example.com]}]}`)
	qid = id + "~d1"
	raise(id, epoch, "push", qid, "bot")
	if got := holders(qid); len(got) != 1 || got[0] != "user:oscar@example.com" {
		t.Fatalf("held out to %v, want davido by the spawn's gate", got)
	}
	resp = answerAs(t, "runner:bot", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the worker answered the policy gate it raised: %s", resp.Status)
	}
	resp = answerAs(t, "oscar@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("davido could not answer: %s", resp.Status)
	}
}

// A TASK'S GATE IS NOT ANSWERABLE BEFORE IT IS POSED EITHER. A worker's pending frame reaches the
// broker at once and the docket a moment later, through the task's mirror, and an answer sent in
// between is addressed to a question with no record of who may answer it. No registry knows a
// task's gates, so nothing but the record can say this one is davido's: a stranger is refused at
// the first instant as they would be a minute later.
func TestATasksGateIsNotAnswerableBeforeItIsPosed(t *testing.T) {
	srv := testServer(t)
	b := srv.UseBroker("http://verse.test:9090")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	worker := httptest.NewServer(b.WorkerHandler())
	defer worker.Close()

	for i := 0; i < 5; i++ { // the window is narrow, so it is tried more than once
		resp := postAs(t, "alice@example.com", ts.URL+"/tasks", "application/tony", `{prompt: "release", driver: human,
			runner: broker, gates: [{name: push, mode: ask, answerers: [oscar@example.com]}]}`)
		var sp struct {
			ID string `tony:"field=id"`
		}
		decodeResp(t, resp, &sp)
		resp.Body.Close()
		resp, err := worker.Client().Post(worker.URL+"/tasks/"+sp.ID+"/claim", "application/json", strings.NewReader(`{"identity":"runner:bot"}`))
		if err != nil {
			t.Fatal(err)
		}
		var claim struct {
			Epoch int64 `tony:"field=epoch"`
		}
		decodeResp(t, resp, &claim)
		resp.Body.Close()
		qid := sp.ID + "~d1"
		resp, err = worker.Client().Post(worker.URL+"/tasks/"+sp.ID+"/report", "application/tony",
			strings.NewReader(fmt.Sprintf(`{epoch: %d, events: [{kind: pending, id: %q, gate: push, text: prod}]}`, claim.Epoch, qid)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		// With no wait for the docket: the earliest an answer can be sent.
		resp = answerAs(t, "mallory@example.com", ts.URL, qid)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("round %d: a stranger answered a task's gate held out to davido: %s", i, resp.Status)
		}
		resp = answerAs(t, "oscar@example.com", ts.URL, qid)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("round %d: davido could not answer: %s", i, resp.Status)
		}
	}
}

// WHO A TASK IS WORKED BY IS WHO CLAIMED IT. A running status names who the task's record is
// written as from then on, so taken from the frame, a claim made with no identity and reported
// `by: system:default` would put the system's name on alice's task, with read-write and control.
func TestAReportedStatusDoesNotNameItsOwnClaimant(t *testing.T) {
	srv := testServer(t)
	b := srv.UseBroker("http://verse.test:9090")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	worker := httptest.NewServer(b.WorkerHandler())
	defer worker.Close()

	resp := postAs(t, "alice@example.com", ts.URL+"/tasks", "application/tony", `{prompt: "deploy", driver: human, runner: broker}`)
	var sp struct {
		ID string `tony:"field=id"`
	}
	decodeResp(t, resp, &sp)
	resp.Body.Close()
	resp, err := worker.Client().Post(worker.URL+"/tasks/"+sp.ID+"/claim", "application/tony", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, resp, &claim)
	resp.Body.Close()
	for _, by := range []string{"system:default", "via:verse.trigger.rule.calendar"} {
		resp, err = worker.Client().Post(worker.URL+"/tasks/"+sp.ID+"/report", "application/tony",
			strings.NewReader(fmt.Sprintf(`{epoch: %d, events: [{kind: status, text: running, by: %q}, {kind: exec, tool: sh, text: hi}]}`, claim.Epoch, by)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	// Once the exec frame has been projected, the status before it has been too.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok, _ := srv.Store.Get(mirror.Ref(sp.ID)); ok && entity.Str(e.Payload, "lastAction") == "sh" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, who := range []string{"system:default", "via:verse.trigger.rule.calendar"} {
		rec, ok, _ := srv.Store.Get(entity.PermsRef(mirror.Ref(sp.ID)))
		if !ok {
			t.Fatal("the task has no record")
		}
		gs, _ := entity.ParseGrants(mirror.Ref(sp.ID).Segs(), rec.Payload)
		for _, g := range gs {
			for l, names := range g.To {
				for _, n := range names {
					if n == who {
						t.Errorf("the task's record holds %s out to %s, which a frame named", l, who)
					}
				}
			}
		}
	}
}
