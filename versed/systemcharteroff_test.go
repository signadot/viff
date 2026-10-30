package versed

import (
	"context"
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
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
	if stands(srv, "sys.thing") {
		t.Error("the system charter the config dropped is still installed after a restart")
	}
	if _, ok, _ := srv.Store.Get(entity.RefAt("sys", "thing", "out")); ok {
		t.Error("what the dropped system charter kept at its address outlived it")
	}
	if !stands(srv, "repos.thing") {
		t.Error("the charter installed beside it was taken down with it")
	}
}
