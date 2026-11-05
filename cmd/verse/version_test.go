package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/verse/pkg/api"
)

// stub is a server that answers with the version it is told to claim, and records what the
// client said about itself.
func stub(t *testing.T, claims string) (*httptest.Server, *string) {
	t.Helper()
	var heard string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		heard = r.Header.Get(api.VersionHeader)
		if claims != "" {
			w.Header().Set(api.VersionHeader, claims)
		}
		w.Header().Set("Content-Type", "application/tony")
		_, _ = w.Write([]byte("ok: true\n"))
	}))
	t.Cleanup(ts.Close)
	return ts, &heard
}

// warned runs fn with a fresh once and a buffer to warn into, and returns what was said. The
// once is what makes the warning appear at most once per invocation, so a test that did not
// reset it would pass on the strength of a warning some earlier test emitted.
func warned(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevOnce := warnTo, skewWarned
	warnTo, skewWarned = &buf, new(sync.Once)
	defer func() { warnTo, skewWarned = prevOut, prevOnce }()
	fn()
	return buf.String()
}

// Every request says which verse is asking. The server cannot refuse a client it cannot
// identify, so a client that does not introduce itself is a check that never fires.
func TestTheClientStampsItsVersion(t *testing.T) {
	ts, heard := stub(t, "(devel)")

	resp, err := verse.Get(ts.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if *heard != api.Version() {
		t.Errorf("the server heard %s: %q, want %q", api.VersionHeader, *heard, api.Version())
	}
}

// Drift warns and the answer still arrives — the request is fine, the operator just needs to
// know that the two ends were built at different times before they read anything into what
// comes back.
func TestDriftWarnsOnceAndServes(t *testing.T) {
	ts, _ := stub(t, "v1.9.0")
	client := &http.Client{Transport: skewCheck{mine: "v1.2.0"}}

	var status int
	out := warned(t, func() {
		for range 3 { // one server, three requests, one line
			resp, err := client.Get(ts.URL)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			status = resp.StatusCode
			resp.Body.Close()
		}
	})
	if status != http.StatusOK {
		t.Errorf("a drifting server was not served: %d", status)
	}
	if n := strings.Count(out, "\n"); n != 1 {
		t.Errorf("three requests warned %d times:\n%s", n, out)
	}
	for _, v := range []string{"v1.2.0", "v1.9.0"} {
		if !strings.Contains(out, v) {
			t.Errorf("the warning %q does not name %q", out, v)
		}
	}
}

// An incompatible server fails the request in the client too, not only at the server. The
// server's refusal is what stops a write; this is what still says something true when the
// server is old enough not to look at the header — which is the server you meet on the day
// this check first matters.
func TestIncompatibleFailsTheRequest(t *testing.T) {
	ts, _ := stub(t, "v2.0.0") // says nothing about the request; a pre-check server
	client := &http.Client{Transport: skewCheck{mine: "v1.2.0"}}

	_, err := client.Get(ts.URL)
	if err == nil {
		t.Fatal("a v1 client got an answer from a v2 server")
	}
	for _, v := range []string{"v1.2.0", "v2.0.0"} {
		if !strings.Contains(err.Error(), v) {
			t.Errorf("the error %q does not name %q", err, v)
		}
	}
}

// A server that says nothing is not a server that disagrees: an older server does not stamp,
// nor does anything older than this check, and a warning on every `verse task` call would be
// noise about a comparison that was never made.
func TestNoVersionHeaderIsSilent(t *testing.T) {
	ts, _ := stub(t, "")
	client := &http.Client{Transport: skewCheck{mine: "v1.2.0"}}

	out := warned(t, func() {
		resp, err := client.Get(ts.URL)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		resp.Body.Close()
	})
	if out != "" {
		t.Errorf("an unstamped server warned: %q", out)
	}
}

// The warning goes to STDERR. A `verse entity | jq` whose input grew a line about versions
// is a worse bug than the skew it is reporting.
func TestTheWarningIsNotOnStdout(t *testing.T) {
	ts, _ := stub(t, "v1.9.0")
	prev := verse
	verse = &http.Client{Transport: skewCheck{mine: "v1.2.0"}}
	prevAddr := testAddr
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	defer func() { verse, testAddr = prev, prevAddr }()

	var out string
	warning := warned(t, func() {
		var err error
		if out, err = runCLI(t, "status"); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if warning == "" {
		t.Fatal("no warning at all")
	}
	if strings.Contains(out, "v1.9.0") || strings.Contains(out, "verse:") {
		t.Errorf("the skew warning landed on stdout:\n%s", out)
	}
}
