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

// A worker that claims a brokered task is granted `read-write` and `control` on the task's
// record, as the owner's act carried out by versed, and every report it makes from then on is
// written as the worker — which is what the grant is checked against. The worker is listed as
// a runner principal, and the grant reaches nothing but the task.
func TestAClaimGrantsTheWorkerItsTaskAndItsReportsAreWrittenAsIt(t *testing.T) {
	srv := testServer(t)
	b := srv.UseBroker("http://verse.test:9090")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	worker := httptest.NewServer(b.WorkerHandler())
	defer worker.Close()

	resp := postAs(t, "alice@example.com", ts.URL+"/tasks", "application/json",
		`{"prompt":"ad-hoc run","driver":"human","runner":"broker"}`)
	var sp struct {
		ID string `tony:"field=id"`
	}
	decodeResp(t, resp, &sp)
	resp.Body.Close()
	if sp.ID == "" {
		t.Fatal("no id returned from task run")
	}
	taskRef := mirror.Ref(sp.ID)
	if got := owner(t, srv, taskRef); got != "user:alice@example.com" {
		t.Fatalf("the task is owned by %q, want the requester", got)
	}

	// Enforced from here: the worker's reports have to be admitted by its grant and by
	// nothing else.

	resp, err := worker.Client().Post(worker.URL+"/tasks/"+sp.ID+"/claim", "application/json",
		strings.NewReader(`{"identity":"runner:bot"}`))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, resp, &claim)
	resp.Body.Close()

	// The grant lands with the claim's running frame, which the mirror projects asynchronously,
	// beside the one the task's record carries from the root's default (read for everyone).
	deadline := time.Now().Add(3 * time.Second)
	var bots *entity.Grant
	for bots == nil && time.Now().Before(deadline) {
		rec, ok, err := srv.Store.Get(entity.PermsRef(taskRef))
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			grants, _ := entity.ParseGrants(taskRef.Segs(), rec.Payload)
			for i, g := range grants {
				if len(g.To[entity.ReadWrite]) > 0 && g.To[entity.ReadWrite][0] == "runner:bot" {
					bots = &grants[i]
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if bots == nil || bots.Below || strings.Join(bots.At, ".") != strings.Join(taskRef.Segs(), ".") ||
		len(bots.To[entity.Control]) != 1 || bots.To[entity.Control][0] != "runner:bot" {
		t.Fatalf("the task's grant to bot = %+v; want read-write and control for bot at the task", bots)
	}
	if ok, err := entity.Holds(srv.Store, "runner:bot", entity.ReadWrite, taskRef); err != nil || !ok {
		t.Fatalf("bot holds read-write on its task: %v %v", ok, err)
	}
	if ok, _ := entity.Holds(srv.Store, "runner:bot", entity.OwnerLevel, taskRef); ok {
		t.Fatal("bot owns the task")
	}
	if ok, _ := entity.Holds(srv.Store, "runner:bot", entity.ReadWrite, mirror.QuestionRef(sp.ID+"~d1")); ok {
		t.Fatal("bot's grant reaches a question its task would raise")
	}
	if _, ok, err := srv.Store.Get(entity.PrincipalRef(entity.RunnerKind, "bot")); err != nil || !ok {
		t.Fatalf("bot is not listed as a runner principal (ok=%v err=%v)", ok, err)
	}

	// A report is written as the worker: the commit that projects it carries the worker as
	// its author, and lands under enforcement because the grant admits it.
	from := srv.Store.Head()
	resp, err = worker.Client().Post(worker.URL+"/tasks/"+sp.ID+"/report", "application/json",
		strings.NewReader(fmt.Sprintf(`{"epoch":%d,"events":[{"kind":"say","text":"working on it"}]}`, claim.Epoch)))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report: %s", resp.Status)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e, ok, err := srv.Store.Get(taskRef)
		if err != nil {
			t.Fatal(err)
		}
		if ok && entity.Str(e.Payload, "say") == "working on it" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	e, _, _ := srv.Store.Get(taskRef)
	if entity.Str(e.Payload, "say") != "working on it" {
		t.Fatalf("the worker's report did not land: %v", e.Payload)
	}
	commits, err := srv.Store.Since(from)
	if err != nil {
		t.Fatal(err)
	}
	by := ""
	for _, c := range commits {
		if c.ChangedAt(len(taskRef.Segs())) != nil {
			for _, r := range c.ChangedAt(len(taskRef.Segs())) {
				if r == taskRef {
					by = c.Author
				}
			}
		}
	}
	if by != "runner:bot" {
		t.Fatalf("the report's commit is written by %q, want the worker", by)
	}
	if refused, _ := entity.PolicyOf(srv.Store).Refusals(); len(refused) != 0 {
		t.Fatalf("something was refused along the way: %v", refused)
	}
}
