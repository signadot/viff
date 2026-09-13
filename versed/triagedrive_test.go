package versed

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// The triage drive installs as written, over a runner named the way it names it.
func TestTheTriageDriveInstalls(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{
		"kind": "pool", "name": "coderunner", "url": "http://127.0.0.1:1", "identity": "coderunner",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("runner: %s: %s", resp.Status, bodyOf(t, resp))
	}
	src, err := os.ReadFile("../docs/working/issue-triage.tony")
	if err != nil {
		t.Fatal(err)
	}
	resp := postAs(t, tester, ts.URL+"/charter", "application/tony", string(src))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	resp.Body.Close()
	if st := statusOf(t, ts); st.Charter == nil {
		t.Fatal("no charter in the census after the install")
	}
}
