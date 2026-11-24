package viffd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// The scope overlay is honored end-to-end through the HTTP surface, and
// POST /scope/delete tears it down — the teardown path a client drives.
func TestScopeOverHTTP(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	put := func(scope, ref, stage string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"at": ref, "payload": map[string]any{"stage": stage}})
		u := ts.URL + "/entity"
		if scope != "" {
			u += "?scope=" + scope
		}
		resp, err := httpPost(u, "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		resp.Body.Close()
	}
	stageOf := func(scope, ref string) (string, bool) {
		t.Helper()
		q := url.Values{"at": {ref}}
		if scope != "" {
			q.Set("scope", scope)
		}
		resp, err := http.Get(ts.URL + "/entity?" + q.Encode())
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return "", false
		}
		var out struct {
			Payload map[string]any `tony:"field=payload"`
		}
		decodeResp(t, resp, &out)
		s, _ := out.Payload["stage"].(string)
		return s, true
	}

	put("", "demo.pr.1", "open") // baseline

	// Scope reads baseline through, then its write is isolated.
	if s, _ := stageOf("me", "demo.pr.1"); s != "open" {
		t.Fatalf("scope did not read baseline through: %q", s)
	}
	put("me", "demo.pr.1", "scoped")
	if s, _ := stageOf("me", "demo.pr.1"); s != "scoped" {
		t.Fatalf("scope write not visible in scope: %q", s)
	}
	if s, _ := stageOf("", "demo.pr.1"); s != "open" {
		t.Fatalf("scope write leaked into baseline: %q", s)
	}

	// Teardown drops the overlay.
	resp, err := httpPost(ts.URL+"/scope/delete?scope=me", "application/json", nil)
	if err != nil {
		t.Fatalf("scope delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("scope delete status = %d, want 204", resp.StatusCode)
	}
	if s, _ := stageOf("me", "demo.pr.1"); s != "open" {
		t.Fatalf("after delete, overlay survived: %q", s)
	}
}

// DELETING A SCOPE CANCELS WHAT IT OWNS (git-issue y51f9ce5h12kr2zvmxn0). A scope's tasks run in
// the runtime every scope shares with baseline, so dropping the scope's engine and its overlay
// left them running, answering to a scope that no longer exists with nowhere to record what they
// did. What the scope's engine tracks goes, every spawn in the scope among it. Baseline's are not
// the scope's, and keep running.
func TestDeletingAScopeCancelsItsLiveTasks(t *testing.T) {
	srv, ts := startedServer(t)
	// A scope is its overlay, and the store refuses to delete one holding nothing.
	entitytest.Set(t, srv.Store.Scope("me"), entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	eng := srv.scopeEngine("me")
	tracked := newIdleHandle(t)
	eng.Adopt("demo.pr.1", "tracked", tracked)
	baseline := newIdleHandle(t)
	srv.Eng.Adopt("demo.pr.1", "baseline", baseline)

	resp, err := httpPost(ts.URL+"/scope/delete?scope=me", "application/json", nil)
	if err != nil {
		t.Fatalf("scope delete: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("scope delete status = %d %s, want 204", resp.StatusCode, body)
	}

	if tracked.count("cancel") != 1 {
		t.Errorf("the scope engine's task was cancelled %d times, want once", tracked.count("cancel"))
	}
	if baseline.count("cancel") != 0 {
		t.Errorf("baseline's work was touched: its task cancelled %d times", baseline.count("cancel"))
	}
}
