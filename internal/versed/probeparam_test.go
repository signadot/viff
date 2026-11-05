package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `?probe` IS TRUE OR FALSE. A value that is neither used to read as the route's default, so a
// typo on a listing quietly skipped the probe it asked for; it is refused now, on the listings
// and the installs alike.
func TestProbeIsTrueOrFalse(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, path := range []string{"/runners?probe=yes", "/iods?probe=1"} {
		resp, err := ts.Client().Do(mustReq(t, http.MethodGet, ts.URL+path))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s = %s, want 400", path, resp.Status)
		}
	}
	// Installs that would otherwise be taken, refused for the probe alone.
	for path, body := range map[string]map[string]any{
		"/runners?probe=no": {"name": "gpu", "url": "http://127.0.0.1:1", "subject": someSubject(t)},
		"/iods?probe=0":     {"name": "laptop", "root": "git", "dir": t.TempDir()},
	} {
		resp := postTony(t, ts.URL+path, body)
		msg := readBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, "probe") {
			t.Errorf("POST %s = %s %q, want 400 about the probe", path, resp.Status, msg)
		}
	}
	for _, path := range []string{"/runners", "/runners?probe=false", "/iods?probe=true"} {
		resp, err := ts.Client().Do(mustReq(t, http.MethodGet, ts.URL+path))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %s, want 200", path, resp.Status)
		}
	}
}
