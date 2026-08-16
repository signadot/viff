package versed

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/trigger"
)

// declareImports posts a package's `imports:` declaration, which a rule reading outside its own
// address needs installed first.
func declareImports(t *testing.T, url, at string, imports ...string) (int, string) {
	t.Helper()
	var q []string
	for _, im := range imports {
		q = append(q, im)
	}
	node, err := codec.ParseNode([]byte("{imports: ["+strings.Join(q, ", ")+"]}"), codec.Tony)
	if err != nil {
		t.Fatalf("parse imports: %v", err)
	}
	resp := postTony(t, url+"/packages?at="+at, node)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

// installAt posts a charter into the package at an address, answering with the status so a test
// can assert a refusal as readily as an install.
func installAt(t *testing.T, url, at, src string) (int, string) {
	t.Helper()
	node, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		t.Fatalf("parse charter: %v", err)
	}
	resp := postTony(t, url+"/triggers?at="+at, node)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

// A charter installed at an address runs in an engine of its own and writes inside that
// address. This is the whole shape end to end: install, fire, and the entity lands.
func TestACharterPackageRunsAtItsAddress(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := declareImports(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring imports = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {system: git, kind: head}, subject: {system: notes, kind: seen, id: head}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing a package = %d: %s", code, msg)
	}

	entitytest.Set(t, srv.Store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "abc"}.Payload())
	waitUntil(t, "the package's rule to fire", func() bool {
		e, ok, _ := srv.Store.Get(entity.RefAt("notes", "seen", "head"))
		return ok && entity.Str(e.Payload, "saw") == "yes"
	})

	// The record says which package it belongs to, so a reader can tell whose rule it is
	// without inferring it from which listing it turned up in.
	rule, ok, _ := srv.Store.Get(trigger.CharterRefAt("notes", "note-the-head"))
	if !ok {
		t.Fatal("the package's rule was not written down")
	}
	if got := entity.Str(rule.Payload, trigger.AtField); got != "notes" {
		t.Fatalf("the record says at=%q, want notes", got)
	}
}

// A package may not install a rule that writes outside its address — the isolation, refused at
// the moment somebody writes it rather than discovered when it fires.
func TestACharterPackageCannotReachOutsideItsAddress(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	code, msg := installAt(t, ts.URL, "notes", `{
		name: reach,
		condition: {subject: {system: demo, kind: pr}},
		action: {patch: {tampered: true}},
	}`)
	if code == 201 {
		t.Fatal("a package installed a rule writing outside its address")
	}
	if !strings.Contains(msg, "demo:pr") || !strings.Contains(msg, "notes") {
		t.Errorf("the refusal names neither the address nor the package: %s", msg)
	}
}

// Verse's own half is nobody's to install a charter into: a package there could write the
// records verse keeps about its own running, which is the boundary P1b drew.
func TestACharterPackageMayNotBeInstalledInVersesHalf(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	code, msg := installAt(t, ts.URL, "verse.task", `{
		name: sneaky,
		condition: {subject: {root: verse, system: task, kind: instance}},
		action: {patch: {status: done}},
	}`)
	if code == 201 {
		t.Fatal("a charter installed itself into verse's own half")
	}
	if !strings.Contains(msg, "verse") {
		t.Errorf("the refusal does not say why: %s", msg)
	}
}

// A package comes back at a restart, and comes back as ITS OWN charter rather than as part of
// baseline's — which is what the address stamped on the record is for.
//
// It is the half a scope cannot have: a scope's layer is invisible from baseline, so a
// federation waits for its dev to reconnect, while a package is a stamp anyone can list.
func TestACharterPackageIsRestoredAtItsAddress(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	ts := httptest.NewServer(first.Handler())
	if code, msg := declareImports(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring imports = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {system: git, kind: head}, subject: {system: notes, kind: seen, id: head}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing = %d: %s", code, msg)
	}
	ts.Close()
	stop()
	time.Sleep(50 * time.Millisecond)

	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())

	// The baseline charter did NOT take it: a package's rules are not baseline's, and a
	// restart that folded them in would hand one group's rules the whole verse to write in.
	for _, tr := range second.Eng.Triggers() {
		if tr.Name == "note-the-head" {
			t.Fatal("the package's rule was restored into the baseline charter")
		}
	}

	// And it runs: the package's own engine came back with it.
	entitytest.Set(t, second.Store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "abc"}.Payload())
	waitUntil(t, "the restored package to fire", func() bool {
		e, ok, _ := second.Store.Get(entity.RefAt("notes", "seen", "head"))
		return ok && entity.Str(e.Payload, "saw") == "yes"
	})
}

// A package reads its own address and what it DECLARES, and nothing else. The refusal names the
// line to add, because a rule reaching past its imports is a dependency somebody has not written
// down rather than a mistake in the rule.
func TestAPackageReadsOnlyWhatItImports(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	rule := `{
		name: note-the-head,
		condition: {when: {system: git, kind: head}, subject: {system: notes, kind: seen, id: head}},
		action: {patch: {saw: "yes"}},
	}`
	code, msg := installAt(t, ts.URL, "notes", rule)
	if code == 201 {
		t.Fatal("a package read a system it never declared")
	}
	if !strings.Contains(msg, "does not import") || !strings.Contains(msg, "imports: [git.head]") {
		t.Errorf("the refusal does not say what to add: %s", msg)
	}

	// Declared, it installs — the same rule, one line of declaration apart.
	if code, msg := declareImports(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring imports = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", rule); code != 201 {
		t.Fatalf("installing after the declaration = %d: %s", code, msg)
	}
}

// THE POINT OF THE WHOLE MECHANISM: two packages that feed each other are refused at install,
// rather than throttled later in a chain that crosses two engines and belongs to neither
// charter's author.
func TestAnImportCycleBetweenPackagesIsRefused(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := declareImports(t, ts.URL, "a", "b"); code != 201 {
		t.Fatalf("a importing b = %d: %s", code, msg)
	}
	code, msg := declareImports(t, ts.URL, "b", "a")
	if code == 201 {
		t.Fatal("two packages reading each other installed")
	}
	if !strings.Contains(msg, "cycle") || !strings.Contains(msg, "a → b → a") {
		t.Errorf("the refusal does not show the loop: %s", msg)
	}

	// And the shape that is NOT a loop still installs: both reading one source is the ordinary
	// case, and a chain a → b → c is a dependency order rather than a cycle.
	if code, msg := declareImports(t, ts.URL, "b", "git"); code != 201 {
		t.Fatalf("b importing a source = %d: %s", code, msg)
	}
}

// A declaration is durable and comes back BEFORE the rules it allows: a restore that loaded the
// charter first would refuse the very rules it is bringing back.
func TestAPackagesImportsAreRestored(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	ts := httptest.NewServer(first.Handler())
	if code, msg := declareImports(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {system: git, kind: head}, subject: {system: notes, kind: seen, id: head}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing = %d: %s", code, msg)
	}
	ts.Close()
	stop()
	time.Sleep(50 * time.Millisecond)

	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())

	// It runs, which is the whole assertion: the rule reads `git`, and it could only be
	// restored into an engine that had the declaration back first.
	entitytest.Set(t, second.Store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "abc"}.Payload())
	waitUntil(t, "the restored package to fire", func() bool {
		e, ok, _ := second.Store.Get(entity.RefAt("notes", "seen", "head"))
		return ok && entity.Str(e.Payload, "saw") == "yes"
	})
}

// Narrowing must not leave a rule standing outside what its package declares — re-installing a
// charter with a line deleted is exactly how that would happen.
func TestNarrowingImportsIsRefusedWhileARuleNeedsThem(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := declareImports(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {system: git, kind: head}, subject: {system: notes, kind: seen, id: head}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing = %d: %s", code, msg)
	}
	code, msg := declareImports(t, ts.URL, "notes")
	if code == 201 {
		t.Fatal("the declaration narrowed out from under an installed rule")
	}
	if !strings.Contains(msg, "note-the-head") {
		t.Errorf("the refusal does not name the rule that still needs it: %s", msg)
	}
}

// A store that cannot be READ answers the empty slice, which is what a verse holding nothing
// answers — so a listing built on `Store.List` reports "there is nothing here" for a store that
// is simply unreadable. That is how an unreplayable patch in the log presents: `verse state`
// empty, the charter tree showing no rules, every write still reporting success.
//
// The surfaces ask the store instead, and answer 503.
func TestAnUnreadableStoreIsNotAnEmptyVerse(t *testing.T) {
	if got := storeUnreadable(entitytest.Store(t)); got != nil {
		t.Fatalf("a healthy store reports %v", got)
	}
	if got := storeUnreadable(unreadableStore{}); got == nil {
		t.Fatal("a store that cannot be read reported no error")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/state", nil)
	if !unreadable(rec, req, unreadableStore{}) {
		t.Fatal("the read failure did not answer")
	}
	if rec.Code != 503 {
		t.Errorf("answered %d, want 503 — the request is fine and the store is not", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "empty verse") {
		t.Errorf("the body does not say what would otherwise be reported: %s", rec.Body.String())
	}
}

// unreadableStore is a reader whose reads fail, which is the only way to have one: the guard in
// entity/raw.go means no write can create the real thing any more.
type unreadableStore struct{ entity.Reader }

func (unreadableStore) ReadError() error { return errors.New("replay failed at commit 2277") }
