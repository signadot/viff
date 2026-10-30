package versed

import (
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/schedule"
)

const issueNextSchedule = "repos.verse.git-issue:sweep"

// ISSUE-NEXT SWEEPS ON ITS TICK AND ONLY THEN. Installed as the file spells it, an issue that
// moves between ticks wakes nothing; a tick gives every open issue a proposal record carrying
// its facts and the stub proposal, and a closed issue none. At the next tick an issue that moved
// gets its new facts and keeps its proposal: the derive owns the facts, the stub owns `next`.
func TestIssueNextProposesOnTheTick(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Zone = time.UTC
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	issue := func(id string) entity.Ref { return entity.RefAt("git-issue", "verse", "issue", id) }
	proposal := func(id string) entity.Ref {
		return entity.RefAt("repos", "verse", "git-issue", "next-proposal", id)
	}
	put := func(id, status, updatedAt string) {
		entitytest.Set(t, srv.Store, issue(id), entity.Obj{
			"id": id, "repo": "verse", "status": status, "title": "issue " + id,
			"updatedAt": updatedAt, "labels": []any{},
		}.Payload())
	}
	put("aaa", "open", "t1")
	put("bbb", "open", "t1")
	put("ccc", "closed", "t1")

	src, err := os.ReadFile(repoCharters + "/issue-next.tony")
	if err != nil {
		t.Fatal(err)
	}
	if resp := postAs(t, tester, ts.URL+"/charter", "application/tony", string(src)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}

	// Between ticks an issue moves and nothing is proposed.
	put("aaa", "open", "t2")
	time.Sleep(200 * time.Millisecond)
	if _, ok, _ := srv.Store.Get(proposal("aaa")); ok {
		t.Fatal("an issue that moved between ticks was proposed before the tick")
	}

	tick := func(at time.Time) {
		t.Helper()
		body, _ := codec.Marshal(api.ScheduleTick{Due: schedule.Key(at)}, codec.Tony)
		resp := postAs(t, tester, ts.URL+"/schedules/"+issueNextSchedule+"/tick", "application/tony", string(body))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("tick: %s: %s", resp.Status, bodyOf(t, resp))
		}
	}
	due := time.Now().UTC().Truncate(3 * time.Hour)
	tick(due.Add(-6 * time.Hour))

	for _, id := range []string{"aaa", "bbb"} {
		p := waitProposal(t, srv.Store, proposal(id), func(p entity.Payload) bool { return entity.Str(p, "label") != "" })
		if entity.Str(p, "issue") != id || entity.Str(p, "title") != "issue "+id ||
			entity.Str(p, "next") != "verse:analyze" || entity.Str(p, "label") != "verse-next=verse:analyze" {
			t.Fatalf("proposal %s = %v", id, entity.ToObj(p))
		}
	}
	if got := entity.Str(waitProposal(t, srv.Store, proposal("aaa"), nil), "updatedAt"); got != "t2" {
		t.Fatalf("aaa's proposal carries updatedAt %q, want the issue's t2", got)
	}
	if _, ok, _ := srv.Store.Get(proposal("ccc")); ok {
		t.Fatal("a closed issue was proposed")
	}

	// What moves the proposal is somebody else's to write; the next tick leaves it standing.
	entitytest.Set(t, srv.Store, proposal("bbb"), entity.Obj{
		"issue": "bbb", "title": "issue bbb", "updatedAt": "t1",
		"next": "world:design", "label": "verse-next=world:design",
	}.Payload())
	put("bbb", "open", "t3")
	tick(due.Add(-3 * time.Hour))
	p := waitProposal(t, srv.Store, proposal("bbb"), func(p entity.Payload) bool { return entity.Str(p, "updatedAt") == "t3" })
	if entity.Str(p, "next") != "world:design" || entity.Str(p, "label") != "verse-next=world:design" {
		t.Fatalf("the sweep wrote over a proposal: %v", entity.ToObj(p))
	}
}

// waitProposal waits for a record that ok accepts (nil: any), and returns it.
func waitProposal(t *testing.T, store entity.Reader, ref entity.Ref, ok func(entity.Payload) bool) entity.Payload {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last entity.Payload
	for time.Now().Before(deadline) {
		if e, found, _ := store.Get(ref); found {
			last = e.Payload
			if ok == nil || ok(e.Payload) {
				return e.Payload
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last %v", ref, entity.ToObj(last))
	return nil
}
