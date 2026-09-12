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
		fmt.Sprintf(`{name: gpu, kind: pool, url: %q, identity: gpu-bot}`, face.URL))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("runner add: %s", resp.Status)
	}

	from := srv.Store.Head()
	resp = postAs(t, "alice@example.com", ts.URL+"/tasks", "application/tony",
		`{prompt: "go", driver: scripted, runner: verse.runners.pool.gpu}`)
	var sp struct {
		ID string `tony:"field=id"`
	}
	decodeResp(t, resp, &sp)
	resp.Body.Close()
	if sp.ID == "" {
		t.Fatal("no id from the spawn")
	}
	taskRef := mirror.Ref(sp.ID)
	if got := owner(t, srv, taskRef); got != "alice@example.com" {
		t.Fatalf("the task is owned by %q, want the requester", got)
	}

	// The grant, the listing, and a report written as the runner.
	deadline := time.Now().Add(3 * time.Second)
	granted := false
	for time.Now().Before(deadline) && !granted {
		granted, _ = entity.Holds(srv.Store, "gpu-bot", entity.Control, taskRef)
		time.Sleep(10 * time.Millisecond)
	}
	if !granted {
		t.Fatal("gpu-bot was not granted control of the task it runs")
	}
	if ok, _ := entity.Holds(srv.Store, "gpu-bot", entity.OwnerLevel, taskRef); ok {
		t.Fatal("gpu-bot owns the task")
	}
	if _, ok, _ := srv.Store.Get(entity.PrincipalRef(entity.RunnerKind, "gpu-bot")); !ok {
		t.Fatal("gpu-bot is not listed as a runner")
	}
	by := ""
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && by != "gpu-bot" {
		commits, err := srv.Store.Since(from)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range commits {
			for _, r := range c.ChangedAt(len(taskRef.Segs())) {
				if r == taskRef && c.Author == "gpu-bot" {
					by = c.Author
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if by != "gpu-bot" {
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
		resp, err := worker.Client().Post(worker.URL+"/tasks/"+sp.ID+"/claim", "application/json", strings.NewReader(`{"identity":"bot"}`))
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
				if gs, _ := entity.ParseGrants(rec.Payload); len(gs) > 0 && len(gs[0].To[entity.ReadWrite]) > 0 {
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
	if got := holders(qid); len(got) != 1 || got[0] != "ani@example.com" {
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
	if got := holders(qid); len(got) != 1 || got[0] != "oscar@example.com" {
		t.Fatalf("held out to %v, want davido by the spawn's gate", got)
	}
	resp = answerAs(t, "bot", ts.URL, qid)
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
