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
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/trigger"
)

// waitUntil polls until cond holds, or fails the test.
func waitUntil(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

// selectionOn finds the selection question standing on a server's docket, by its chain.
func selectionOn(t *testing.T, srv *Server, chain string) string {
	t.Helper()
	for _, g := range docket(t, srv) {
		if g["chain"] == chain {
			id, _ := g["id"].(string)
			return id
		}
	}
	return ""
}

func spawnedDoer(t *testing.T, srv *Server, doer string) bool {
	t.Helper()
	for _, e := range srv.Store.List(func(r entity.Ref) bool {
		return mirror.IsTask(r)
	}) {
		if entity.Str(e.Payload, "doer") == doer {
			return true
		}
	}
	return false
}

func reviewRule(p *launch.Provider) trigger.Trigger {
	return trigger.Trigger{
		Name:      "review",
		Condition: trigger.Condition{Subject: matchAt("demo.pr.*", map[string]any{"stage": "open"})},
		Action: trigger.Action{
			Patch: entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{Prompt: "review $subject", Grant: "read-only", Provider: p},
		},
	}
}

// A selection outlives the verse that asked it, and the answer cast after the restart still
// decides who does the work.
//
// This is the case the docket could not survive. A selection opens from inside an action,
// AFTER that action has written its latch — so the subject no longer matches the rule,
// nothing re-fires it, and the only thing that ever awaited the answer was a goroutine in the
// verse that died. The entry stayed on the docket looking answerable, an operator picked a
// doer, and nothing whatsoever happened.
func TestARestartResumesASelection(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	if err := first.Eng.AddTrigger(reviewRule(&launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "ask"})); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	testerOwns(t, first, entity.RefAt("demo", "pr", "1")) // the tester's pr, so the tester's pick
	entitytest.Set(t, first.Store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	var qid string
	waitUntil(t, "the selection to reach the docket", func() bool {
		qid = selectionOn(t, first, "review")
		return qid != ""
	})
	stop() // the verse goes down with the selection still open

	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())
	waitDocket(t, second, qid)

	ts := httptest.NewServer(second.Handler())
	defer ts.Close()
	// The pick, cast from the docket exactly as `verse gate <id> --answer claude --as scott` does.
	resp := postTony(t, ts.URL+answerPath(qid), api.Resolve{Choice: "claude", By: tester})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("answering the resumed selection = %s, want 204", resp.Status)
	}
	if q := settledQuestion(t, second, qid); q["verdict"] != "claude" {
		t.Fatalf("the record does not carry the pick: %+v", q)
	}
	// And the work it was holding up actually starts, under the doer the answer named — the
	// claim, since a selection that settles and spawns nothing has resumed only on paper.
	waitUntil(t, "the picked doer to be spawned", func() bool { return spawnedDoer(t, second, "claude") })
}

// A resumed selection honours the clock it was posed under: `within` is stamped as an
// absolute instant, so a window that expired while the verse was down settles the question
// on the way back up, on nothing, and nothing spawns.
func TestARestartHonoursASelectionsDeadline(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	if err := first.Eng.AddTrigger(reviewRule(&launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "vote", Need: 2, Within: "10m"})); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, first.Store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	var qid string
	waitUntil(t, "the selection to reach the docket", func() bool {
		qid = selectionOn(t, first, "review")
		return qid != ""
	})
	waitUntil(t, "the deadline to be stamped", func() bool {
		_, _, at := mirror.DeadlineOf(entity.Obj(questionDoc(t, first, qid)).Payload())
		return at != ""
	})
	backdate(t, first.Store, qid) // down for longer than the window
	stop()

	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())
	waitUntil(t, "the expired selection to settle on nothing", func() bool {
		return questionDoc(t, second, qid)["verdict"] == "none"
	})
	time.Sleep(200 * time.Millisecond)
	if spawnedDoer(t, second, "gpt") || spawnedDoer(t, second, "claude") {
		t.Fatal("a no-pick spawned a doer anyway")
	}
}
