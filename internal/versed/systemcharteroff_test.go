package versed

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/runner/runnerd/runnerdtest"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

// A CHARTER THE CONFIG STOPS NAMING IS OFF AFTER A RESTART: the system installs at start what the
// config names, so a package it installed that the config no longer names is taken down at start,
// with what it kept at its address. What it put elsewhere stays, and so does a package somebody
// else installed: a reconciler turned off leaves the charters it installed running.
func TestASystemCharterTheConfigDropsIsOffAfterARestart(t *testing.T) {
	const reconciler = `at: sys.thing
rules:
- name: note
  condition: {when: {path: "sys.thing.in"}, subject: {path: "sys.thing.out"}}
  action: {patch: {saw: "yes"}}
`
	addr := entitytest.Addr(t)
	start := func(texts []SystemCharter) (*Server, *httptest.Server, func()) {
		srv := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
		srv.SystemCharters = texts
		ctx, stop := context.WithCancel(t.Context())
		srv.Start(ctx)
		ts := httptest.NewServer(srv.Handler())
		return srv, ts, func() {
			ts.Close()
			stop()
			time.Sleep(50 * time.Millisecond)
		}
	}
	stands := func(srv *Server, at string) bool { return srv.packagesSeenAt(at) }

	srv, ts, stop := start([]SystemCharter{{Name: "thing", Text: []byte(reconciler)}})
	if !stands(srv, "sys.thing") {
		t.Fatal("the configured system charter did not install")
	}
	if code, msg := installAt(t, ts.URL, "repos.thing", `{
		name: keep,
		condition: {when: {path: "repos.thing.in"}, subject: {path: "repos.thing.out"}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing the charter it reconciles = %d: %s", code, msg)
	}
	entitytest.Set(t, srv.Store, entity.RefAt("sys", "thing", "in"), entity.Obj{"n": 1}.Payload())
	waitUntil(t, "the system charter to keep its state", func() bool {
		_, ok, _ := srv.Store.Get(entity.RefAt("sys", "thing", "out"))
		return ok
	})
	stop()

	// Still named: a restart keeps it, and what it keeps.
	srv, _, stop = start([]SystemCharter{{Name: "thing", Text: []byte(reconciler)}})
	if !stands(srv, "sys.thing") {
		t.Fatal("a restart with the config unchanged lost the system charter")
	}
	stop()

	// No longer named: off.
	srv, _, stop = start(nil)
	defer stop()
	waitUntil(t, "the system charter the config dropped to be uninstalled", func() bool { return !stands(srv, "sys.thing") })
	waitUntil(t, "what the dropped system charter kept at its address to go", func() bool {
		_, ok, _ := srv.Store.Get(entity.RefAt("sys", "thing", "out"))
		return !ok
	})
	if !stands(srv, "repos.thing") {
		t.Error("the charter installed beside it was taken down with it")
	}
}

// WHAT A DROPPED SYSTEM CHARTER KEPT AT ITS ADDRESS STAYS WHERE THE CHARTERS CANNOT BE LISTED: a
// listing that was not read says nothing about what is installed there, and a package's data
// may be under the address.
func TestADroppedSystemAddressStaysWhereTheChartersCannotBeListed(t *testing.T) {
	store := &failCharterListing{Store: entitytest.Store(t)}
	srv := serverWith(t, runnerdtest.Scripted, store)
	srv.Start(t.Context())
	kept := entity.RefAt("sys", "thing", "pkg", "data")
	entitytest.Set(t, srv.Store, kept, entity.Obj{"n": 1}.Payload())
	store.on.Store(true)
	err := srv.dropSystemAddress(t.Context(), entity.As(srv.Store, entity.SystemCharter), "sys.thing")
	store.on.Store(false)
	if err == nil {
		t.Fatal("the address was dropped on a listing that was not read")
	}
	if _, ok, _ := srv.Store.Get(kept); !ok {
		t.Fatal("what stood under the address was removed on a listing that was not read")
	}
}
