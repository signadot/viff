package entity

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	docdserver "github.com/signadot/tony-format/go-tony/system/docd/server"
	"github.com/signadot/tony-format/go-tony/system/libctl"
	logdapi "github.com/signadot/tony-format/go-tony/system/logd/api"
)

// These tests pin the reason MountRoot exists: a mount is process-driven, so it has no
// history, and a history-bearing watch must never span one.
//
// LogdStore watches its root kpath and serves Since with a native FromCommit replay watch
// on that same path. docd turns a watch whose path is a strict ANCESTOR of a mount into a
// COMPOSED watch, and a composed watch (go-tony v0.0.87, docd/server/watch.go:106,126)
// silently drops FromCommit and never emits ReplayComplete — so such a Since returns
// ok=true with zero events after running to its internal timeout, and the engine's catchUp
// under-delivers without noticing.
//
//   - TestLogdStore_SinceAcrossContentMount — the production layout (MountKPath →
//     MountRoot) keeps Since working. The regression guard.
//   - TestLogdStore_SinceUnderMountBelowRoot — mounts below the root by hand and confirms
//     the fault is real. This is what MountRoot buys, kept as evidence.
//   - TestDocdComposedWatchDropsFromCommitBelowRoot — pins the cause in docd, not in Since.

// contentMountKind mirrors the shape `verse connect` uses (source.ContentPrefix + a nonce
// scope), so the mount kpath quoting is exactly what MountKPath produces in production.
const contentMountKind = "dir-content-a1b2c3"

// contentLikeController mimics source.Content: file bytes served on demand, nothing
// stored. Watch must NOT decline (returning ErrUnsupported would make an overlapping
// ancestor watch fail to establish). Duplicated here rather than imported because package
// source imports package entity.
type contentLikeController struct{ mount string }

// Match serves a payload for any path below the mount, so a test can prove a ref routes
// all the way through refPath → docd → this controller.
func (c contentLikeController) Match(_ context.Context, path string, _ *ir.Node, _ libctl.MatchParams) (*ir.Node, error) {
	rest, ok := strings.CutPrefix(path, c.mount+".")
	if !ok || rest == "" {
		return ir.Null(), nil // the mount root itself, or an unrelated path
	}
	return ir.FromMap(map[string]*ir.Node{"content": ir.FromString("hi")}), nil
}

func (contentLikeController) Patch(context.Context, string, *ir.Node, libctl.PatchParams) (*ir.Node, error) {
	return nil, libctl.ErrUnsupported
}

func (contentLikeController) Watch(ctx context.Context, _ string, _ libctl.WatchParams, _ func(*logdapi.WatchEvent) error) error {
	<-ctx.Done()
	return ctx.Err()
}

// startDocd brings up an in-process docd in front of logd with both faces listening: the
// client face (which speaks the logd session protocol, and is what a LogdStore connects
// to) and the mount face (where controllers register).
func startDocd(t *testing.T, logdAddr string) *docdserver.Server {
	t.Helper()
	srv := docdserver.New(&docdserver.Spec{LogdAddr: logdAddr})
	if err := srv.StartClientTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start docd client face: %v", err)
	}
	t.Cleanup(func() { srv.StopClientTCP() })
	if err := srv.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start docd mount face: %v", err)
	}
	t.Cleanup(func() { srv.StopTCP() })
	return srv
}

// mountController runs a controller owning kpath and blocks until docd reports the mount
// live — the same wait `verse connect` does before installing its trigger. It returns how
// long the mount took, which is itself evidence: mount/unmount are coordinator WRITERS, so
// a mount overlapped by an active ancestor watch blocks for docd's MountForceAfter window
// (default 5s) before force-ending that reader. The timeout must exceed that window, or
// the harness mistakes writer priority for a failure to mount.
func mountController(t *testing.T, docd *docdserver.Server, kpath string) time.Duration {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	start := time.Now()
	errc := make(chan error, 1)
	go func() {
		errc <- libctl.RunController(ctx, &libctl.ControllerConfig{
			DocdAddr:   docd.TCPAddr(),
			Controller: "content-test",
			Path:       kpath,
			Handler:    contentLikeController{mount: kpath},
		})
	}()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errc:
			t.Fatalf("controller exited early: %v", err)
		default:
		}
		if docd.Mounts.Lookup(kpath).Live() {
			return time.Since(start)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("controller did not mount %q within 20s", kpath)
	return 0
}

// storeOverDocd opens a LogdStore through a fresh logd+docd pair and starts the lazy live
// root watch (what Engine.Start does, and what makes Rev track commits).
func storeOverDocd(t *testing.T) (*LogdStore, *docdserver.Server) {
	t.Helper()
	docd := startDocd(t, startLogd(t))
	s, err := OpenLogdStore(LogdConfig{Addr: docd.ClientTCPAddr(), ClientID: "verse-test"})
	if err != nil {
		t.Fatalf("open store through docd: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	_, unsub := s.Observe()
	t.Cleanup(unsub)
	return s, docd
}

// sinceReplays reports the events Since(from) yields and how long the call took. Since
// swallows its own failure (it returns ok=true with a partial replay when its internal
// timeout fires), so the elapsed time is part of the evidence.
func sinceReplays(t *testing.T, s *LogdStore, from int64) ([]Event, bool, time.Duration) {
	t.Helper()
	start := time.Now()
	evs, ok := s.Since(from)
	return evs, ok, time.Since(start)
}

func hasRef(evs []Event, ref Ref) bool {
	for _, ev := range evs {
		if ev.Ref == ref {
			return true
		}
	}
	return false
}

// awaitSince polls Since(from) until it replays ref, returning the last attempt's timing.
// Bounded attempts rather than a deadline loop, because the failure mode is a 15s stall per
// attempt — a deadline loop would hide it.
func awaitSince(t *testing.T, s *LogdStore, from int64, ref Ref) ([]Event, bool, time.Duration, bool) {
	t.Helper()
	for attempt := range 3 {
		evs, ok, elapsed := sinceReplays(t, s, from)
		if ok && hasRef(evs, ref) {
			return evs, ok, elapsed, true
		}
		if attempt < 2 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	evs, ok, elapsed := sinceReplays(t, s, from)
	return evs, ok, elapsed, false
}

// TestLogdStore_SinceAcrossContentMount is the regression guard for the production layout:
// with the content controller mounted where MountKPath puts it (under MountRoot, disjoint
// from the entity root), Since keeps replaying, the mount does not stall on the root watch,
// and a mounted slice is still reachable by its ordinary entity ref.
func TestLogdStore_SinceAcrossContentMount(t *testing.T) {
	s, docd := storeOverDocd(t)

	// Control: no mounts yet, so the root watch is single-route.
	before := s.Rev()
	refA := Ref{System: "demo", Kind: "pr", ID: "a"}
	s.Put(refA, Obj{"stage": "open"}.Payload())
	eventually(t, "Since to replay a write with no mount present", func() bool {
		evs, ok, _ := sinceReplays(t, s, before)
		return ok && hasRef(evs, refA)
	})

	// Mount exactly where verse connect does.
	mount := MountKPath("local", contentMountKind)
	if strings.HasPrefix(mount, DefaultRoot+".") {
		t.Fatalf("MountKPath returned a path under the entity root: %q — a mount below a "+
			"watched root composes the watch and breaks FromCommit replay", mount)
	}
	t.Logf("mounting at %q (entity root %q — disjoint)", mount, DefaultRoot)
	mountTook := mountController(t, docd, mount)
	t.Logf("mount became live in %s", mountTook)
	if mountTook > 2*time.Second {
		t.Errorf("mount took %s — it should not overlap the root watch at all; an overlapping "+
			"mount waits docd's MountForceAfter window (~5s) before force-ending the watch, "+
			"which `verse connect` would pay on every connect", mountTook)
	}

	time.Sleep(250 * time.Millisecond)

	// The claim: Since still replays a write made after the mount.
	mid := s.Rev()
	refB := Ref{System: "demo", Kind: "pr", ID: "b"}
	s.Put(refB, Obj{"stage": "open"}.Payload())

	evs, ok, elapsed, found := awaitSince(t, s, mid, refB)
	t.Logf("with the mount on its own root: Since(%d) → ok=%v events=%d elapsed=%s", mid, ok, len(evs), elapsed)
	if !found {
		t.Fatalf("Since did not replay %s (ok=%v events=%d elapsed=%s) — the root watch is "+
			"being composed, so the mount is not disjoint from it after all", refB, ok, len(evs), elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Since replayed but took %s — a composed watch starves ReplayComplete, so the "+
			"call is running to its internal timeout and catchUp would stall the dispatch loop", elapsed)
	}

	// End-to-end: a mount-owned ref still resolves through refPath → MountRoot → docd →
	// the controller. This is what makes the split path-rendering only.
	got, ok, _ := s.Get(Ref{System: "local", Kind: contentMountKind, ID: "README.md"})
	if !ok || ToObj(got.Payload)["content"] != "hi" {
		t.Fatalf("mount-owned ref did not resolve through MountRoot: ok=%v payload=%#v", ok, got.Payload)
	}
}

// TestLogdStore_SinceUnderMountBelowRoot is the evidence for MountRoot: it mounts a
// controller BELOW the store root by hand — the layout verse used before this split — and
// confirms Since silently under-delivers. It asserts the broken behaviour on purpose, so
// the rationale stays checked rather than becoming folklore.
//
// If this test starts failing, docd has likely grown FromCommit support for composed
// watches; MountRoot would then no longer be required for THIS reason (the writer-priority
// stall and "a process has no history" would still argue for it).
func TestLogdStore_SinceUnderMountBelowRoot(t *testing.T) {
	// NOT RUN BY DEFAULT — set GOTEST_LONG=1 to run it.
	//
	// It takes ~65s, which was ~90% of this package's entire test time and most of the
	// whole suite's. The cost is intrinsic: the mount waits out MountForceAfter (~5s) and
	// each Since attempt runs to its 15s internal timeout, and those delays ARE the finding
	// — shortening them would remove what is being demonstrated.
	//
	// So it is skipped rather than trimmed, and the honest consequence is that this
	// evidence is NOT exercised on an ordinary run: a change that fixed (or worsened) the
	// composed-watch behaviour would not be noticed until someone runs it deliberately.
	// It is kept because the rationale for MountRoot is worth being able to re-check on
	// demand rather than trusting a comment.
	requireLong(t)
	s, docd := storeOverDocd(t)

	before := s.Rev()
	refA := Ref{System: "demo", Kind: "pr", ID: "a"}
	s.Put(refA, Obj{"stage": "open"}.Payload())
	eventually(t, "Since to replay a write with no mount present", func() bool {
		evs, ok, _ := sinceReplays(t, s, before)
		return ok && hasRef(evs, refA)
	})

	// The OLD layout: strictly below the watched root, so docd composes the root watch.
	belowRoot := seg(DefaultRoot) + ".local." + seg(contentMountKind)
	t.Logf("mounting BELOW the entity root at %q (the pre-MountRoot layout)", belowRoot)
	mountTook := mountController(t, docd, belowRoot)
	t.Logf("mount became live in %s (writer priority: it waited on the overlapping root watch)", mountTook)

	time.Sleep(250 * time.Millisecond)

	mid := s.Rev()
	refB := Ref{System: "demo", Kind: "pr", ID: "b"}
	s.Put(refB, Obj{"stage": "open"}.Payload())

	evs, ok, elapsed, found := awaitSince(t, s, mid, refB)
	t.Logf("with a mount below the root: Since(%d) → ok=%v events=%d elapsed=%s", mid, ok, len(evs), elapsed)

	if found {
		t.Fatalf(`Since replayed %s even with a mount below the root (ok=%v events=%d elapsed=%s).

docd may have grown FromCommit support for composed watches. Re-read
docd/server/watch.go:106,126 — if composition now honors FromCommit, MountRoot is no
longer needed to keep Since working (though a mount below a watched root still costs
the MountForceAfter stall, and a process-driven mount still has no history to replay).`,
			refB, ok, len(evs), elapsed)
	}
	t.Logf("confirmed: a mount below the watched root makes Since under-deliver silently "+
		"(ok=%v with %d events) — this is what MountRoot avoids", ok, len(evs))
}

// TestDocdComposedWatchDropsFromCommitBelowRoot pins the cause below verse: it asks docd
// directly for a FromCommit replay on an ancestor path with a mount below, with no
// LogdStore involved. Also asserted as broken on purpose (see the test above).
func TestDocdComposedWatchDropsFromCommitBelowRoot(t *testing.T) {
	docd := startDocd(t, startLogd(t))

	writer := libctl.NewLogdSession(&libctl.LogdSessionConfig{Addr: docd.ClientTCPAddr(), ClientID: "w"})
	t.Cleanup(func() { writer.Close() })
	ctx := context.Background()

	root := DefaultRoot
	if err := writer.Patch(ctx, root+".demo.pr.seed", ir.FromMap(map[string]*ir.Node{
		"stage": ir.FromString("open"),
	})); err != nil {
		t.Fatalf("seed patch: %v", err)
	}

	// Learn the current commit from a plain watch's initial state.
	probe := libctl.NewLogdSession(&libctl.LogdSessionConfig{Addr: docd.ClientTCPAddr(), ClientID: "probe"})
	t.Cleanup(func() { probe.Close() })
	pw, err := probe.Watch(ctx, root, &libctl.WatchOptions{})
	if err != nil {
		t.Fatalf("probe watch: %v", err)
	}
	var from int64
	select {
	case ev, ok := <-pw.Events():
		if !ok {
			t.Fatal("probe watch closed before initial state")
		}
		from = ev.Commit
	case <-time.After(5 * time.Second):
		t.Fatal("no initial state from probe watch")
	}
	pw.Close()

	// Mount below the watched root, then write past `from`.
	mountController(t, docd, seg(root)+".local."+seg(contentMountKind))
	if err := writer.Patch(ctx, root+".demo.pr.after", ir.FromMap(map[string]*ir.Node{
		"stage": ir.FromString("open"),
	})); err != nil {
		t.Fatalf("post-mount patch: %v", err)
	}

	// Ask for the replay the way LogdStore.Since does.
	reader := libctl.NewLogdSession(&libctl.LogdSessionConfig{Addr: docd.ClientTCPAddr(), ClientID: "r"})
	t.Cleanup(func() { reader.Close() })
	fc := from
	w, err := reader.Watch(ctx, root, &libctl.WatchOptions{FromCommit: &fc, NoInit: true})
	if err != nil {
		t.Fatalf("FromCommit watch on %q with a mount below: %v", root, err)
	}
	defer w.Close()

	timeout := time.After(6 * time.Second)
	patches := 0
	for {
		select {
		case <-timeout:
			t.Logf("confirmed: no ReplayComplete and %d patch event(s) — docd composes the "+
				"ancestor watch and builds its sub-watches {Path, NoInit:true} with no FromCommit "+
				"(docd/server/watch.go:126), so the replay request is dropped", patches)
			return
		case ev, ok := <-w.Events():
			if !ok {
				t.Fatalf("watch closed before the timeout (err=%v, %d patches)", w.Err(), patches)
			}
			if ev.ReplayComplete {
				t.Fatalf(`docd signalled ReplayComplete for a composed FromCommit watch on %q (after %d patches).

Composed watches appear to honor FromCommit now; see the note on
TestLogdStore_SinceUnderMountBelowRoot.`, root, patches)
			}
			if ev.Patch != nil {
				patches++
			}
		}
	}
}

// requireLong skips a test unless GOTEST_LONG is set to something non-empty.
//
// For tests whose runtime is the point rather than an accident — waiting out a real
// timeout to demonstrate what happens when it expires. Those cannot be made fast without
// destroying what they show, and leaving them in the default run costs more than they earn
// on an ordinary edit-test loop.
//
//	GOTEST_LONG=1 go test ./entity/
func requireLong(t *testing.T) {
	t.Helper()
	if os.Getenv("GOTEST_LONG") == "" {
		t.Skip("long: set GOTEST_LONG=1 to run (skipped by default; see the test's comment)")
	}
}
