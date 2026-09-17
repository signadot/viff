package versed

import (
	"net/http"
	"net/http/httptest"
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
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Zone = time.UTC
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	file := `[
		{name: mine, every: 1h},
		{name: note-ticks,
		 condition: {subject: {path: "verse.schedule.mine.tick.*"}},
		 action: {spawn: {driver: scripted, prompt: "echo $subject"}}},
	]`
	if resp := installAs(t, "alice@example.com", ts.URL, file); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s %s", resp.Status, readBody(t, resp))
	}
	rec, ok := scheduleRecord(t, srv, "mine")
	if !ok {
		t.Fatal("no record at verse.schedule.mine")
	}
	if entity.Str(rec, schedule.EveryField) != "1h" || entity.Str(rec, schedule.ByField) != "user:alice@example.com" {
		t.Fatalf("record = %v", rec)
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
}

func TestAPackagesScheduleCarriesItsAddressAndLeavesWithIt(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
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
	if code, body := deletePath(t, ts.URL, "drift", false); code != http.StatusNoContent {
		t.Fatalf("uninstall: %d %s", code, body)
	}
	if _, ok := scheduleRecord(t, srv, "drift:nightly"); ok {
		t.Fatal("the package's schedule survived the uninstall")
	}
}

func TestAScheduleIsNotAScopes(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	docs, err := codec.ParseNodes([]byte(`{name: mine, every: 1h}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if _, code, err := srv.installCharterAs(srv.Store, "me", "", charterDocs(docs)); err == nil || code != http.StatusBadRequest {
		t.Fatalf("a scoped schedule: code %d err %v, want a refusal", code, err)
	}
}
