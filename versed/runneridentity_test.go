package versed

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
	ts := httptest.NewServer(srv.Handler())
	face := runnerFace(t, ts.URL)
	defer ts.Close()
	resp := postAs(t, "ops@example.com", ts.URL+"/runners", "application/tony",
		fmt.Sprintf(`{name: gpu, url: %q, identity: gpu-bot, subject: %q}`, face.URL, face.Subject))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("runner add: %s", resp.Status)
	}

	from := srv.Store.Head()
	resp = postAs(t, "alice@example.com", ts.URL+"/tasks", "application/tony",
		`{spawn: {script: "true", runner: verse.runners.gpu}}`)
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
