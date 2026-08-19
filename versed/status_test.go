package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/trigger"
)

// /status summarizes the running verse: it reports the store revision and live
// counts, which move as the verse is driven.
func TestStatus(t *testing.T) {
	srv := testServer(t) // no daemons needed
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	get := func() map[string]any {
		t.Helper()
		resp, err := http.Get(ts.URL + "/status")
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		var m map[string]any
		decodeResp(t, resp, &m)
		return m
	}

	m := get()
	for _, k := range []string{"storeRev", "flows", "gates", "tasks", "fired"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("status missing %q: %v", k, m)
		}
	}
	// THE CHARTER IS COUNTED WHERE IT IS INSTALLED, not once at the top. A top-level `rules`
	// counted BASELINE's alone while every number beside it counts the whole verse, so a verse
	// whose program is packaged reported none.
	if n := charterCount(t, m, "rules"); n != 0 {
		t.Fatalf("charter rules = %v, want 0 on a fresh verse", n)
	}

	// Install a trigger and write an entity; the counts move.
	if err := srv.Eng.AddTrigger(trigger.Trigger{
		Name:      "t",
		Condition: trigger.Condition{Subject: trigger.Match{"system": "demo"}},
	}); err != nil {
		t.Fatal(err)
	}
	entitytest.Set(t, srv.Store, entity.RefAt("demo", "x", "1"), entity.Obj{"k": "v"}.Payload())

	m = get()
	if n := charterCount(t, m, "rules"); n != 1 {
		t.Fatalf("charter rules = %v, want 1 after install", n)
	}
	if rev, _ := codec.AsInt64(m["storeRev"]); rev < 1 {
		t.Fatalf("storeRev = %v, want >= 1 after a Put", rev)
	}
}

// Status names the installed sources. This is the one line that says a source is REGISTERED:
// every counter above it moves on what verse was already given, so a verse whose source was
// dropped reads exactly like a healthy quiet one until this list is empty. Reaching the far
// side is a separate question, not asserted by this list — see the inline note below and the
// type doc on api.Status.
func TestStatusSources(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sources := func() []string {
		t.Helper()
		resp, err := http.Get(ts.URL + "/status")
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		defer resp.Body.Close()
		var out api.Status
		decodeResp(t, resp, &out)
		return out.Sources
	}

	if got := sources(); len(got) != 0 {
		t.Fatalf("sources = %v, want none on a fresh verse", got)
	}

	// Installed through the real route, so status reports what an operator actually put here.
	// No probe: what is asserted is that the REGISTRATION shows up, and reaching the far side is
	// `verse sources`' question — see the type doc on api.Status.
	resp := postTony(t, ts.URL+"/sources?probe=false", map[string]any{
		"system": "git", "name": "laptop", "url": "http://127.0.0.1:1/",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, bodyOf(t, resp))
	}
	resp.Body.Close()

	if got := sources(); len(got) != 1 || got[0] != "verse:sources:git:laptop" {
		t.Fatalf("sources = %v, want [sources:git:laptop]", got)
	}
}

// charterCount reads one of the root node's counts out of the charter census — baseline's own,
// which is what the root of that tree is.
func charterCount(t *testing.T, m map[string]any, key string) int64 {
	t.Helper()
	c, ok := m["charter"].(map[string]any)
	if !ok {
		t.Fatalf("status has no charter census: %v", m)
	}
	n, _ := codec.AsInt64(c[key])
	return n
}
