package source_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/source"
)

// A hint carries the refs and NOTHING ELSE — the same shape the `/bin/sh` git hook posts, so
// the two senders cannot drift.
func TestPostHintCarriesTheRefsAndNoBody(t *testing.T) {
	var got *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	url := source.HintURL(srv.URL, "git", "laptop")
	if !strings.HasSuffix(url, "/sources/git/laptop/hint") {
		t.Fatalf("hint url %q", url)
	}
	err := source.PostHint(context.Background(), url,
		entity.RefAt("git", "head", "verse"),
		entity.RefAt("git", "head", "tony"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost {
		t.Fatalf("method %s, want POST", got.Method)
	}
	if len(body) != 0 {
		t.Fatalf("a hint sent a body: %q — there is nothing to send but which slice", body)
	}
	refs := got.URL.Query()["at"]
	if len(refs) != 2 || refs[0] != "git.head.verse" || refs[1] != "git.head.tony" {
		t.Fatalf("refs %v", refs)
	}
}

// No refs is a full resync, spelled by sending no parameter at all.
func TestPostHintWithNoRefsAsksForEverything(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := source.PostHint(context.Background(), srv.URL+"/hint"); err != nil {
		t.Fatal(err)
	}
	if query != "" {
		t.Fatalf("query %q, want none", query)
	}
}

// The two refusals an operator will actually hit are a registration name that does not match
// the url the source was configured with (404) and a registration whose reflector is not
// running (503). Both have to come back as errors, or a source goes on believing its
// deliveries are landing.
func TestPostHintReportsARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such source", http.StatusNotFound)
	}))
	defer srv.Close()

	err := source.PostHint(context.Background(), srv.URL+"/hint")
	if err == nil {
		t.Fatal("a 404 was not an error")
	}
	if !strings.Contains(err.Error(), "no such source") {
		t.Fatalf("error %v does not carry what the server said", err)
	}
}

// Hints is the Hinter a deployment wires in, and a failure is logged rather than returned:
// the caller is holding a webhook request it must answer in seconds.
func TestHintsLogsWhatItCouldNotDeliver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not running", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	var logged []string
	h := source.Hints(srv.URL+"/hint", func(f string, a ...any) {
		logged = append(logged, f)
	})
	h(context.Background(), entity.RefAt("git", "head", "verse"))
	if len(logged) != 1 {
		t.Fatalf("logged %v, want one line", logged)
	}
}
