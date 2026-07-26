package main

import (
	"net/url"
	"strings"
	"testing"
)

// An id reaches urlFor straight from the command line, so it may contain anything a shell
// will pass along. "#" is the one that matters: every process gate id has one, and
// concatenated into a URL it starts a FRAGMENT rather than landing in the path. The
// request then goes somewhere real but wrong — `/processes/run-x#d1/events` fetches
// `/processes/run-x` — and, because the "?" is appended after the "#", it goes there with
// no scope and no format either. A 200 from the wrong route is the worst answer available,
// so this pins the escaping rather than the symptom.
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
