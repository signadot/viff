package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
)

// privateWithTasks is a verse that holds nothing out, serving a broker, with two tasks of
// alice's: one hosted here, and one on the broker for a worker to claim.
func privateWithTasks(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv := testServer(t)
	srv.UseBroker("http://verse.test:9090")
	entity.PolicyOf(srv.Store).SetRootChildGrants(nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	for _, body := range []map[string]any{
		{"id": "hosted", "prompt": "sleep 30", "driver": "scripted"},
		{"id": "brokered", "prompt": "t", "driver": "human", "runner": "broker"},
	} {
		resp := postAsTony(t, "alice@example.com", ts.URL+"/tasks", body)
		if msg := readBody(t, resp); resp.StatusCode >= http.StatusBadRequest {
			t.Fatalf("spawning %v: %s %s", body["id"], resp.Status, msg)
		}
	}
	return srv, ts
}

// A task is read as its record is: whoever may not read the record reads neither the snapshot,
// nor the events, nor the inbox, of a task hosted here or one on the broker. Somebody who names
// nobody is among them.
func TestATaskIsReadAsItsRecordIs(t *testing.T) {
	_, ts := privateWithTasks(t)
	for _, id := range []string{"hosted", "brokered"} {
		for _, route := range []string{"", "/events", "/inbox"} {
			url := ts.URL + "/tasks/" + id + route
			for _, who := range []string{"bob@example.com", "runner:w", ""} {
				resp := getAs(t, who, url)
				body := readBody(t, resp)
				if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "refused") {
					t.Errorf("%q reads /tasks/%s%s: %s %q, want it refused", who, id, route, resp.Status, body)
				}
			}
		}
		for _, route := range []string{"", "/inbox"} {
			resp := getAs(t, "alice@example.com", ts.URL+"/tasks/"+id+route)
			if body := readBody(t, resp); resp.StatusCode != http.StatusOK {
				t.Errorf("alice reads her own /tasks/%s%s: %s %q", id, route, resp.Status, body)
			}
		}
	}
}

// In the open verse a task is anybody's to read, as its record is.
func TestATaskInTheOpenVerseIsAnybodysToRead(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp := postAsTony(t, "alice@example.com", ts.URL+"/tasks",
		map[string]any{"id": "open", "prompt": "sleep 30", "driver": "scripted"})
	resp.Body.Close()
	for _, who := range []string{"bob@example.com", ""} {
		resp := getAs(t, who, ts.URL+"/tasks/open")
		if body := readBody(t, resp); resp.StatusCode != http.StatusOK {
			t.Errorf("%q reads a task in the open verse: %s %q", who, resp.Status, body)
		}
	}
}

// Where to dial is answered to a worker that holds nothing yet, and is all it is told: the
// address, for a brokered task, and one answer for a task that is hosted here and for one that
// is not there.
func TestWhereToDialIsAnsweredToAWorkerThatHoldsNothing(t *testing.T) {
	_, ts := privateWithTasks(t)

	resp := getAs(t, "runner:w", ts.URL+"/tasks/brokered/dial")
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "http://verse.test:9090") {
		t.Fatalf("where to dial for a brokered task: %s %q", resp.Status, body)
	}
	for _, said := range []string{"prompt", "status", "alice", "runner"} {
		if strings.Contains(body, said) {
			t.Errorf("where to dial says more than the address (%q): %q", said, body)
		}
	}

	answers := map[string]string{}
	for _, id := range []string{"hosted", "nowhere"} {
		resp := getAs(t, "runner:w", ts.URL+"/tasks/"+id+"/dial")
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("where to dial for %s: %s %q, want 404", id, resp.Status, body)
		}
		answers[id] = strings.ReplaceAll(body, id, "<id>")
	}
	if answers["hosted"] != answers["nowhere"] {
		t.Errorf("where to dial says whether a task exists:\n  %s\n  %s", answers["hosted"], answers["nowhere"])
	}
}
