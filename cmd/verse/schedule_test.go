package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/codec"
)

// `verse schedule tick <name> --due <instant>` posts the backfill to the schedule's record, the
// package's address folded into the id when --at names one.
func TestScheduleTickPostsToTheRecord(t *testing.T) {
	var path string
	var body api.ScheduleTick
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := codec.Unmarshal(raw, &body, codec.Tony); err != nil {
			t.Errorf("decoding the body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{schedule: x, tick: y}`))
	}))
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	if _, err := runCLI(t, "schedule", "tick", "hourly", "--due", "2026-09-18T03:00:00Z"); err != nil {
		t.Fatalf("schedule tick: %v", err)
	}
	if path != "/schedules/hourly/tick" || body.Due != "2026-09-18T03:00:00Z" {
		t.Fatalf("posted %s %+v", path, body)
	}
	if _, err := runCLI(t, "schedule", "tick", "nightly", "--at", "drift", "--due", "2026-09-18T02:00:00Z"); err != nil {
		t.Fatalf("schedule tick --at: %v", err)
	}
	if path != "/schedules/drift:nightly/tick" {
		t.Fatalf("posted to %s, want the package's record", path)
	}
	if _, err := runCLI(t, "schedule", "tick", "hourly"); err == nil {
		t.Fatal("a tick with no --due was accepted")
	}
}
