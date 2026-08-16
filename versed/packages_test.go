package versed

import (
	"context"
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
	rule, ok, _ := srv.Store.Get(trigger.CharterRef("note-the-head"))
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
