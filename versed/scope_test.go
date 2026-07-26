package versed

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"

	logdserver "github.com/signadot/tony-format/go-tony/system/logd/server"
	"github.com/signadot/tony-format/go-tony/system/logd/storage"
)

func startLogd(t *testing.T) string {
	t.Helper()
	store, err := storage.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	srv := logdserver.New(&logdserver.Spec{Storage: store})
	if err := srv.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start logd: %v", err)
	}
	t.Cleanup(func() { srv.StopTCP() })
	return srv.TCPAddr()
}

// The scope overlay is honored end-to-end through the HTTP surface, and
// POST /scope/delete tears it down — the federation teardown path the CLI drives.
func TestScopeOverHTTP(t *testing.T) {
	store, err := entity.OpenLogdStore(entity.LogdConfig{Addr: startLogd(t), ClientID: "versed-scope-test"})
	if err != nil {
		t.Fatalf("open logd store: %v", err)
	}
	defer store.Close()

	srv := NewWithStore(ScriptedBuilder, store)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	put := func(scope, ref, stage string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"ref": ref, "payload": map[string]any{"stage": stage}})
		u := ts.URL + "/entity"
		if scope != "" {
			u += "?scope=" + scope
		}
		resp, err := http.Post(u, "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		resp.Body.Close()
	}
	stageOf := func(scope, ref string) (string, bool) {
		t.Helper()
		q := url.Values{"ref": {ref}}
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
			Payload map[string]any `json:"payload" tony:"field=payload"`
		}
		decodeResp(t, resp, &out)
		s, _ := out.Payload["stage"].(string)
		return s, true
	}

	put("", "demo:pr:1", "open") // baseline

	// Scope reads baseline through, then its write is isolated.
	if s, _ := stageOf("me", "demo:pr:1"); s != "open" {
		t.Fatalf("scope did not read baseline through: %q", s)
	}
	put("me", "demo:pr:1", "scoped")
	if s, _ := stageOf("me", "demo:pr:1"); s != "scoped" {
		t.Fatalf("scope write not visible in scope: %q", s)
	}
	if s, _ := stageOf("", "demo:pr:1"); s != "open" {
		t.Fatalf("scope write leaked into baseline: %q", s)
	}

	// Teardown drops the overlay.
	resp, err := http.Post(ts.URL+"/scope/delete?scope=me", "application/json", nil)
	if err != nil {
		t.Fatalf("scope delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("scope delete status = %d, want 204", resp.StatusCode)
	}
	if s, _ := stageOf("me", "demo:pr:1"); s != "open" {
		t.Fatalf("after delete, overlay survived: %q", s)
	}
}

// The in-memory store has no real scopes: scope delete answers 501 so the client
// falls back to per-entity cleanup.
func TestScopeDeleteUnsupportedOnMemStore(t *testing.T) {
	srv := New(ScriptedBuilder) // in-memory
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/scope/delete?scope=me", "application/json", nil)
	if err != nil {
		t.Fatalf("scope delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("mem-store scope delete status = %d, want 501", resp.StatusCode)
	}
}
