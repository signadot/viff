package versed

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// THE STALE-REVIEW SUGGESTION, as shipped in deploy/charters, for the two rules that are the
// charter's own logic: a weekday tick gathers the open pull requests waiting on the person, and a
// day the task found something stale puts one message in the outbox for the Slack io. The task
// between them is the runner's, held here by a driver that never finishes; its answer is written
// onto the day as the completion hook would write it.
func TestTheStaleReviewGathersAWeekdayAndSendsOnlyWhatIsStale(t *testing.T) {
	srv := testServer(t)
	srv.Zone = time.UTC
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withSlackIO(t, srv)
	registerDeployed(t, ts, "claude", "../deploy/claude-runner/claude-runner.yaml")
	b, err := os.ReadFile("../deploy/charters/example/repo/app/stale-review.tony")
	if err != nil {
		t.Fatal(err)
	}
	installCharter(t, ts, "", string(b))

	pr := func(n string) entity.Ref { return entity.RefAt("github", "signadot", "signadot", "pr", n) }
	day := func(date string) entity.Ref { return entity.RefAt("signadot", "stale-review", "day", date) }
	box := func(date string) entity.Ref { return entity.RefAt("signadot", "stale-review", "out", "slack", date) }
	// Waiting on foxish among others; waiting on someone else; closed while still requesting.
	entitytest.Set(t, srv.Store, pr("7"), entity.Obj{"number": 7, "state": "open", "requestedReviewers": []any{"ana", "foxish"}}.Payload())
	entitytest.Set(t, srv.Store, pr("8"), entity.Obj{"number": 8, "state": "open", "requestedReviewers": []any{"ana"}}.Payload())
	entitytest.Set(t, srv.Store, pr("9"), entity.Obj{"number": 9, "state": "closed", "requestedReviewers": []any{"foxish"}}.Payload())

	// A Saturday's tick, then a Monday's, as `verse schedule tick` backfills them.
	tick := func(due string) {
		t.Helper()
		body, _ := codec.Marshal(api.ScheduleTick{Due: due}, codec.Tony)
		resp := postAs(t, tester, ts.URL+"/schedules/signadot.stale-review:morning/tick", "application/tony", string(body))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("ticking %s: %s %s", due, resp.Status, readBody(t, resp))
		}
	}
	tick("2026-10-24T09:00:00Z")
	tick("2026-10-19T09:00:00Z")

	waitFor := func(what string, ref entity.Ref, ok func(entity.Payload) bool) entity.Payload {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			e, found, _ := srv.Store.Get(ref)
			if found && ok(e.Payload) {
				return e.Payload
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %v", what, e.Payload)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	got := waitFor("Monday was not gathered", day("2026-10-19"), func(p entity.Payload) bool {
		return entity.Get(p, "prs") != nil
	})
	if prs := fmt.Sprint(entity.ToObj(got)["prs"]); prs != "[7]" {
		t.Errorf("Monday's prs = %s, want [7]: %v", prs, entity.ToObj(got))
	}
	if _, found, _ := srv.Store.Get(day("2026-10-24")); found {
		t.Errorf("a Saturday was gathered")
	}

	// The task's answer, as onDone patches it: a stale one sends, an empty one does not.
	answer := func(date string, stale []any, digest string) {
		e, _, _ := srv.Store.Get(day(date))
		o := entity.ToObj(e.Payload)
		if o == nil {
			o = entity.Obj{"prs": []any{7}}
		}
		o["stage"], o["stale"], o["digest"] = "done", stale, digest
		entitytest.Set(t, srv.Store, day(date), o.Payload())
	}
	answer("2026-10-19", []any{entity.Obj{"number": 7}}, "waiting: #7")
	answer("2026-10-20", []any{}, "")
	sent := waitFor("Monday's digest was not sent", box("2026-10-19"), func(p entity.Payload) bool {
		return entity.Str(p, "to") != ""
	})
	if entity.Str(sent, "to") != "user:heidi@example.com" || entity.Str(sent, "text") != "waiting: #7" {
		t.Errorf("Monday's message = %v", entity.ToObj(sent))
	}
	if e, found, _ := srv.Store.Get(box("2026-10-20")); found && entity.Str(e.Payload, "to") != "" {
		t.Errorf("a day with nothing stale sent: %v", entity.ToObj(e.Payload))
	}
}
