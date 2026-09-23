package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
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
		Condition: trigger.Condition{Subject: matchAt("demo.*.*", nil)},
		// A rule must have an action (git-issue b2g9hsnfh12ks5vqg9n0). What it writes is not
		// what this counts — the counts on /status are.
		Action: trigger.Action{Patch: entity.Obj{"seen": true}.Payload()},
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
	// `verse ios`' question — see the type doc on api.Status.
	resp := postTony(t, ts.URL+"/io?probe=false", map[string]any{
		"root": "git", "name": "laptop", "url": "http://127.0.0.1:1/",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, bodyOf(t, resp))
	}
	resp.Body.Close()

	if got := sources(); len(got) != 1 || got[0] != "verse.io.laptop" {
		t.Fatalf("sources = %v, want [verse.io.laptop]", got)
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

// `tasks` is the DOCKET — how many tasks this verse has a record of — and it is the number
// `verse task` lists. This pins it to that list.
//
// The in-process task runtime is NOT that number: it holds the tasks this process hosts, so a
// verse running its work on a remote runner hosts none of them and a count taken from there
// reads 0 over a full docket, disagreeing with the very list an operator reads next
// (git-issue 8hfm5amsh12kr9sah1n0). This server hosts nothing, which is that deployment.
func TestStatusTasksIsTheDocket(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	tasks := func() int64 {
		t.Helper()
		resp, err := http.Get(ts.URL + "/status")
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		defer resp.Body.Close()
		var m map[string]any
		decodeResp(t, resp, &m)
		n, ok := codec.AsInt64(m["tasks"])
		if !ok {
			t.Fatalf("tasks is not a number: %v", m["tasks"])
		}
		return n
	}

	if n := tasks(); n != 0 {
		t.Fatalf("tasks = %d, want 0 on a verse with no record of one", n)
	}

	// The record a spawn writes, for tasks running somewhere this process cannot see.
	ids := []string{"run-a", "run-b", "run-c"}
	for _, id := range ids {
		entitytest.Set(t, srv.Store, mirror.Ref(id), entity.Obj{
			"id":     id,
			"prompt": "do a thing",
			"status": "running",
			"runner": "http://coderunner:9802",
		}.Payload())
	}

	got := tasks()
	if got != int64(len(ids)) {
		t.Fatalf("tasks = %d, want %d — the count is of the records, not of what is hosted here",
			got, len(ids))
	}

	// And it is the LIST's own number, which is the property that makes the two surfaces
	// comparable: whatever `verse task` would answer with, `tasks` counts exactly that.
	insts, err := srv.taskSnapshots(srv.Store)
	if err != nil {
		t.Fatal(err)
	}
	if int(got) != len(insts) {
		t.Fatalf("tasks = %d but the list has %d", got, len(insts))
	}
}

// `fired` is EVERY ENGINE's, because a rule fires in the engine that holds it and a package's
// charter runs in one of its own. A charter installed at an address left baseline's counter at
// 0 while its rules acted — which is what staging reads, where the whole charter is packaged
// under `verse-dev` (git-issue 8hfm5amsh12kr9sah1n0).
func TestStatusFiredCountsEveryEngine(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	fired := func() int64 {
		t.Helper()
		resp, err := http.Get(ts.URL + "/status")
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		defer resp.Body.Close()
		var m map[string]any
		decodeResp(t, resp, &m)
		n, ok := codec.AsInt64(m["fired"])
		if !ok {
			t.Fatalf("fired is not a number: %v", m["fired"])
		}
		return n
	}

	if n := fired(); n != 0 {
		t.Fatalf("fired = %d, want 0 on a verse whose triggers have acted on nothing", n)
	}

	// A package, and nothing in baseline: the deployment this is about.
	if code, msg := declareReads(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring imports = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {path: "git.head.*"}, subject: {path: "notes.seen.head"}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing a package = %d: %s", code, msg)
	}
	entitytest.Set(t, srv.Store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "abc"}.Payload())
	waitUntil(t, "the package's rule to fire", func() bool {
		e, ok, _ := srv.Store.Get(entity.RefAt("notes", "seen", "head"))
		return ok && entity.Str(e.Payload, "saw") == "yes"
	})

	// Baseline holds no rule and has fired at nothing, which is the reading this replaces.
	if n := srv.Eng.Fired(); n != 0 {
		t.Fatalf("baseline fired %d times over a charter it does not hold", n)
	}
	if n := fired(); n < 1 {
		t.Fatalf("fired = %d over a package whose rule has acted", n)
	}
}
