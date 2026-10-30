package versed

import (
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/schedule"
)

// A schedule declared in a charter's head document beside the strata is a record at
// verse.schedule.<id>, stamped with `next` once, owned by its installer, and gone with the
// charter that declared it.

func scheduleRecord(t *testing.T, srv *Server, id string) (entity.Payload, bool) {
	t.Helper()
	ent, ok, err := srv.Store.Get(schedule.Ref(id))
	if err != nil {
		t.Fatalf("reading schedule %s: %v", id, err)
	}
	return ent.Payload, ok
}

func del(t *testing.T, as, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(api.PrincipalHeader, as)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAScheduleDeclaredInACharterIsARecordWithANext(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Zone = time.UTC
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	file := `[
		{name: mine, every: 1h},
		{name: note-ticks,
		 condition: {subject: {path: "verse.schedule.mine.tick.*"}},
		 action: {spawn: {agent: {name: verse}, prompt: "echo $subject"}}},
	]`
	if resp := installAs(t, "alice@example.com", ts.URL, file); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s %s", resp.Status, readBody(t, resp))
	}
	rec, ok := scheduleRecord(t, srv, "mine")
	if !ok {
		t.Fatal("no record at verse.schedule.mine")
	}
	if entity.Str(rec, schedule.EveryField) != "1h" {
		t.Fatalf("record = %v", rec)
	}
	if who, err := entity.ActsAs(srv.Store, schedule.Ref("mine")); err != nil || who != "user:alice@example.com" {
		t.Fatalf("the schedule is %q's (%v), want alice's", who, err)
	}
	next, err := schedule.ParseKey(entity.Str(rec, schedule.NextField))
	if err != nil || !next.After(time.Now()) || next.Minute() != 0 {
		t.Fatalf("next = %q: want the next top of the hour", entity.Str(rec, schedule.NextField))
	}

	// Applied again: the same grid, so the standing `next` is honoured.
	if resp := installAs(t, "alice@example.com", ts.URL, file); resp.StatusCode != http.StatusCreated {
		t.Fatalf("re-install: %s %s", resp.Status, readBody(t, resp))
	}
	if again, _ := scheduleRecord(t, srv, "mine"); entity.Str(again, schedule.NextField) != schedule.Key(next) {
		t.Fatalf("a re-install moved next from %s to %s", schedule.Key(next), entity.Str(again, schedule.NextField))
	}
	// A new grid: `next` is stamped afresh, on it.
	if resp := installAs(t, "alice@example.com", ts.URL, `{name: mine, every: 2h}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("re-grid: %s %s", resp.Status, readBody(t, resp))
	}
	moved, _ := scheduleRecord(t, srv, "mine")
	next2, err := schedule.ParseKey(entity.Str(moved, schedule.NextField))
	if err != nil || next2.Hour()%2 != 0 || next2.Minute() != 0 || !next2.After(time.Now()) {
		t.Fatalf("next after the grid changed = %s, want an even hour ahead", next2)
	}

	// The calendar's names are not a schedule's to take.
	if resp := installAs(t, "alice@example.com", ts.URL, `{name: date, every: 1h}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a schedule named date: %s, want 400", resp.Status)
	}

	// Removed by name, as a rule or a level is.
	if resp := del(t, "alice@example.com", ts.URL+"/charter/mine"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove: %s %s", resp.Status, readBody(t, resp))
	}
	if _, ok := scheduleRecord(t, srv, "mine"); ok {
		t.Fatal("the record survived its removal")
	}
	// And the owner record went with it, so the name is free for the next declarer.
	if _, ok, err := srv.Store.Get(entity.PermsRef(schedule.Ref("mine"))); err != nil || ok {
		t.Fatalf("the perms record survived the removal (ok=%v err=%v)", ok, err)
	}
	if resp := installAs(t, "bob@example.com", ts.URL, `{name: mine, every: 1h}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("bob declaring the freed name: %s %s", resp.Status, readBody(t, resp))
	}
}

// scheduleListings counts the listings of the schedule records a server makes through it.
type scheduleListings struct {
	entity.Store
	n int
}

func (c *scheduleListings) count(at []string) {
	if slices.Equal(at, []string{entity.VerseRoot, schedule.System}) {
		c.n++
	}
}
func (c *scheduleListings) ListAt(at []string, depth int, pred func(entity.Ref) bool) []entity.Entity {
	c.count(at)
	return c.Store.ListAt(at, depth, pred)
}
func (c *scheduleListings) ListAtErr(at []string, depth int, pred func(entity.Ref) bool) ([]entity.Entity, error) {
	c.count(at)
	return entity.ListAtErr(c.Store, at, depth, pred)
}

// THE CHARTER TREE READS THE SCHEDULES A FIXED NUMBER OF TIMES, however many charters it lists:
// each is handed the ones declared at its address. Read per charter, `verse c` on a verse of
// fifteen charters read every schedule record fifteen times, one after another.
func TestTheCharterTreeReadsTheSchedulesOnce(t *testing.T) {
	cs := &scheduleListings{Store: entitytest.Store(t)}
	srv := serverWith(t, runnerdtest.Scripted, cs)
	srv.Zone = time.UTC
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	listings := func(ats ...string) (int, string) {
		t.Helper()
		for _, at := range ats {
			if code, body := installAt(t, ts.URL, at, `[{name: nightly, every: 24h, from: "2026-01-01T02:00:00Z"}]`); code != http.StatusCreated {
				t.Fatalf("install at %s: %d %s", at, code, body)
			}
		}
		cs.n = 0
		resp, err := http.Get(ts.URL + "/charter")
		if err != nil {
			t.Fatal(err)
		}
		return cs.n, readBody(t, resp)
	}
	three, _ := listings("drift", "lint", "docs")
	six, tree := listings("audit", "deps", "fmt")
	if six != three {
		t.Errorf("the tree listed the schedules %d times over three charters and %d over six, want the same", three, six)
	}
	for _, at := range []string{"drift", "lint", "docs", "audit", "deps", "fmt"} {
		if !strings.Contains(tree, at+":nightly") {
			t.Errorf("the tree does not list %s:nightly under its charter:\n%s", at, tree)
		}
	}
}

func TestAPackagesScheduleCarriesItsAddressAndLeavesWithIt(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Zone = time.UTC
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	code, body := installAt(t, ts.URL, "drift", `[
		{name: nightly, every: 24h, from: "2026-01-01T02:00:00Z"},
	]`)
	if code != http.StatusCreated {
		t.Fatalf("install at drift: %d %s", code, body)
	}
	rec, ok := scheduleRecord(t, srv, "drift:nightly")
	if !ok {
		t.Fatal("no record at verse.schedule.drift:nightly")
	}
	if entity.Str(rec, schedule.AtField) != "drift" || entity.Str(rec, schedule.FromField) != "2026-01-01T02:00:00Z" {
		t.Fatalf("record = %v", rec)
	}
	if next, err := schedule.ParseKey(entity.Str(rec, schedule.NextField)); err != nil || next.Hour() != 2 {
		t.Fatalf("next = %q, want 02:00 on some day", entity.Str(rec, schedule.NextField))
	}
	// The charter tree lists it under its package, and a path reaches it for removal.
	resp, err := http.Get(ts.URL + "/charter?path=drift")
	if err != nil {
		t.Fatal(err)
	}
	tree := readBody(t, resp)
	if !strings.Contains(tree, "schedules") || !strings.Contains(tree, "drift:nightly") {
		t.Fatalf("the charter tree at drift does not list the schedule:\n%s", tree)
	}
	if code, body := deletePath(t, ts.URL, "drift.schedules.nightly", false); code != http.StatusNoContent {
		t.Fatalf("removing by path: %d %s", code, body)
	}
	if _, ok := scheduleRecord(t, srv, "drift:nightly"); ok {
		t.Fatal("the schedule survived its removal by path")
	}
	// Declared again, it goes with the package.
	if code, body := installAt(t, ts.URL, "drift", `{name: nightly, every: 24h}`); code != http.StatusCreated {
		t.Fatalf("re-install at drift: %d %s", code, body)
	}
	if code, body := deletePath(t, ts.URL, "drift", false); code != http.StatusNoContent {
		t.Fatalf("uninstall: %d %s", code, body)
	}
	if _, ok := scheduleRecord(t, srv, "drift:nightly"); ok {
		t.Fatal("the package's schedule survived the uninstall")
	}
}

func TestAScheduleIsNotAScopes(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Start(t.Context())
	docs, err := codec.ParseNodes([]byte(`{name: mine, every: 1h}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if _, code, err := srv.installCharterAs(srv.Store, "me", "", docs); err == nil || code != http.StatusBadRequest {
		t.Fatalf("a scoped schedule: code %d err %v, want a refusal", code, err)
	}
}
