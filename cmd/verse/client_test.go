package main

import (
	"net/url"
	"strings"
	"testing"

	"github.com/signadot/verse/process"
)

// The delimiter every gate id carries must reach the route as ITSELF. That is the whole
// reason process.GateSep is what it is: RFC 3986 unreserved, so url.URL.String() emits it
// verbatim and the server matches /processes/{id} on the id the user typed. If it ever
// became a character Go percent-encodes, every `gate resolve` would 404 — so pin the
// round trip, not the spelling.
func TestURLForPassesAGateIDThrough(t *testing.T) {
	defer func(addr string) { g.Addr = addr }(g.Addr)
	g.Addr = "localhost:9797"

	id := "run-33c719683411" + process.GateSep + "d1"
	got := urlFor("/processes/"+id+"/events", nil)
	if strings.Contains(got, "%") {
		t.Errorf("a gate id was percent-encoded in %q — GateSep %q is not URL-unreserved",
			got, process.GateSep)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("urlFor produced an unparseable URL %q: %v", got, err)
	}
	if want := "/processes/" + id + "/events"; u.Path != want {
		t.Errorf("path = %q, want %q", u.Path, want)
	}
	if u.Fragment != "" {
		t.Errorf("the id became a URL fragment %q — the request would hit %q", u.Fragment, u.Path)
	}
}

// An id reaches urlFor straight from the command line, so it may still contain anything a
// shell will pass along — including a "#", which no longer delimits a gate id but is exactly
// what punishes a concatenated URL. It starts a FRAGMENT rather than landing in the path, so
// the request goes somewhere real but wrong — `/processes/run-x#d1/events` fetches
// `/processes/run-x` — and, because the "?" is appended after the "#", it goes there with no
// scope and no format either. A 200 from the wrong route is the worst answer available, so
// this pins the escaping rather than the symptom.
func TestURLForEscapesThePath(t *testing.T) {
	defer func(addr string) { g.Addr = addr }(g.Addr)
	g.Addr = "localhost:9797"

	got := urlFor("/processes/run-33c719683411#d1/events", nil)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("urlFor produced an unparseable URL %q: %v", got, err)
	}
	if u.Fragment != "" {
		t.Errorf("the id became a URL fragment %q — the request would hit %q, not the events route",
			u.Fragment, u.Path)
	}
	if want := "/processes/run-33c719683411#d1/events"; u.Path != want {
		t.Errorf("path = %q, want %q", u.Path, want)
	}
	if !strings.Contains(got, "%23") {
		t.Errorf("the %q is not escaped in %q", "#", got)
	}
}

// The query string has to survive an id like that too: it carries --scope and the output
// format, and losing them silently is how a scoped command answers from baseline.
func TestURLForKeepsTheQueryAfterAnEscapedID(t *testing.T) {
	defer func(addr, scope string) { g.Addr, g.Scope = addr, scope }(g.Addr, g.Scope)
	g.Addr, g.Scope = "localhost:9797", "me"

	u, err := url.Parse(urlFor("/processes/run-x#d1/events", nil))
	if err != nil {
		t.Fatalf("unparseable: %v", err)
	}
	if got := u.Query().Get("scope"); got != "me" {
		t.Errorf("scope = %q, want %q — the query was swallowed by the fragment", got, "me")
	}
}
