package viffd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/entity"
)

// privateWithTasks is a viff instance that holds nothing out, with a task of alice's hosted here.
func privateWithTasks(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv := testServer(t)
	entity.PolicyOf(srv.Store).SetRootChildGrants(nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	for _, body := range []map[string]any{
		{"id": "hosted", "spawn": map[string]any{"script": "sleep 30"}},
	} {
		resp := postAsTony(t, "alice@example.com", ts.URL+"/tasks", body)
		if msg := readBody(t, resp); resp.StatusCode >= http.StatusBadRequest {
			t.Fatalf("spawning %v: %s %s", body["id"], resp.Status, msg)
		}
	}
	return srv, ts
}

// A task is read as its record is: whoever may not read the record reads neither the snapshot
// nor the events of a task hosted here. Somebody who names nobody is among them.
func TestATaskIsReadAsItsRecordIs(t *testing.T) {
	_, ts := privateWithTasks(t)
	for _, id := range []string{"hosted"} {
		for _, route := range []string{"", "/events"} {
			url := ts.URL + "/tasks/" + id + route
			for _, who := range []string{"bob@example.com", "runner:w", ""} {
				resp := getAs(t, who, url)
				body := readBody(t, resp)
				// A runner's name with no token is refused at the door, before the record is read.
				if who == "runner:w" && resp.StatusCode == http.StatusUnauthorized {
					continue
				}
				if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "refused") {
					t.Errorf("%q reads /tasks/%s%s: %s %q, want it refused", who, id, route, resp.Status, body)
				}
			}
		}
		for _, route := range []string{""} {
			resp := getAs(t, "alice@example.com", ts.URL+"/tasks/"+id+route)
			if body := readBody(t, resp); resp.StatusCode != http.StatusOK {
				t.Errorf("alice reads her own /tasks/%s%s: %s %q", id, route, resp.Status, body)
			}
		}
	}
}

// In the open viff a task is anybody's to read, as its record is.
func TestATaskInTheOpenViffIsAnybodysToRead(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp := postAsTony(t, "alice@example.com", ts.URL+"/tasks",
		map[string]any{"id": "open", "spawn": map[string]any{"script": "sleep 30"}})
	resp.Body.Close()
	for _, who := range []string{"bob@example.com", ""} {
		resp := getAs(t, who, ts.URL+"/tasks/open")
		if body := readBody(t, resp); resp.StatusCode != http.StatusOK {
			t.Errorf("%q reads a task in the open viff: %s %q", who, resp.Status, body)
		}
	}
}
