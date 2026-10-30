package versed

import (
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/schedule"
)

// GET /schedules lists every record with its grid and its standing tick; POST /schedules/{id}/tick
// backfills one, for a caller the record admits.
func TestSchedulesOnTheWire(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Zone = time.UTC
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if resp := installAs(t, "alice@example.com", ts.URL, `{name: mine, every: 1h}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice's schedule: %s %s", resp.Status, readBody(t, resp))
	}
	resp, err := http.Get(ts.URL + "/schedules")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var rows []api.ScheduleRow
	if err := codec.Unmarshal(raw, &rows, codec.Tony); err != nil {
		t.Fatalf("decoding the listing: %v\n%s", err, raw)
	}
	byID := map[string]api.ScheduleRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	for _, id := range []string{"hourly", "daily", "weekly", "mine"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("the listing lacks %s: %v", id, rows)
		}
	}
	if byID["mine"].By != "user:alice@example.com" || byID["mine"].Every != "1h" || byID["mine"].Next == "" {
		t.Fatalf("mine = %+v", byID["mine"])
	}

	// A backfill of her own schedule lands; one of the system's is refused.
	past := schedule.Key(time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour))
	tick := func(as, id string) *http.Response {
		body, _ := codec.Marshal(api.ScheduleTick{Due: past}, codec.Tony)
		return postAs(t, as, ts.URL+"/schedules/"+id+"/tick", "application/tony", string(body))
	}
	if resp := tick("alice@example.com", "mine"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice backfilling mine: %s %s", resp.Status, readBody(t, resp))
	}
	standingTicks(t, srv.Store, "mine", past)
	if resp := tick("alice@example.com", "hourly"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("alice backfilling the system's hourly: %s, want 403", resp.Status)
	}
	if resp := tick("alice@example.com", "nope"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a schedule that is not there: %s, want 404", resp.Status)
	}
	// The listing shows the backfilled tick as the standing one until the timer replaces it.
	resp, _ = http.Get(ts.URL + "/schedules")
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	rows = nil
	if err := codec.Unmarshal(raw, &rows, codec.Tony); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == "mine" && row.Tick != past {
			t.Fatalf("mine's standing tick = %q, want %s", row.Tick, past)
		}
	}
}
