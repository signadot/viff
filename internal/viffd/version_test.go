package viffd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/entity"
)

// Every answer says which viff gave it — including the ones nobody renders a document for. A
// header that rides along with the successful replies and not with the 404s is absent from
// exactly the replies someone is trying to explain.
func TestEveryResponseCarriesTheVersion(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, path := range []string{
		"/status",                 // a document
		"/schema",                 // a big one
		"/entity",                 // a 400: no ref
		"/schema/viff-nonesuch",   // a 404 from a handler
		"/nothing-is-here-at-all", // a 404 from the mux
		"/watch?since=0&limit=0",  // a stream
	} {
		resp, err := ts.Client().Get(ts.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		got := resp.Header.Get(api.VersionHeader)
		resp.Body.Close()
		if got != api.Version() {
			t.Errorf("GET %s answered %s with %s: %q, want %q",
				path, resp.Status, api.VersionHeader, got, api.Version())
		}
	}
}

// An incompatible client is turned away BEFORE the handler runs, and the point of that is the
// write: the client sees the version skew only in the answer, by which time a put it should
// never have made has already happened. So the test is not that the status is 409 — it is that
// the store did not change.
func TestAnIncompatibleClientIsRefusedBeforeItWrites(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(versionedAs(srv.Handler(), "v1.2.0"))
	defer ts.Close()

	body := strings.NewReader(`{at: demo.pr."1", payload: {stage: open}}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/entity", body)
	req.Header.Set("Content-Type", "application/tony")
	req.Header.Set(api.VersionHeader, "v2.0.0")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("POST /entity from a v2 client to a v1 server = %s, want 409", resp.Status)
	}
	if _, ok, err := srv.Store.Get(entity.RefAt("demo", "pr", "1")); ok || err != nil {
		t.Errorf("the refused write happened anyway (ok=%v err=%v)", ok, err)
	}
}

// Drift is not grounds for refusing anything: a minor apart means one side has features the
// other does not, which the operator should hear about and the request should survive.
func TestDriftIsServedAnyway(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(versionedAs(srv.Handler(), "v1.2.0"))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req.Header.Set(api.VersionHeader, "v1.9.9")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a client one minor away got %s, want 200", resp.Status)
	}
}

// A caller that says nothing about its version is served. It may be a viff older than this
// check, a script, or curl — and an endpoint meant to be usable by hand must stay usable by
// hand. Unknown is not incompatible.
func TestAnUnversionedClientIsServed(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(versionedAs(srv.Handler(), "v1.2.0"))
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a caller with no version header got %s, want 200", resp.Status)
	}
}
