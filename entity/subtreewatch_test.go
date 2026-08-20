package entity

import (
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
)

// watch reads a subtree watch one CHANGE at a time, over a stream that delivers one COMMIT
// at a time. The two differ wherever a commit carries several entities — the level-triggered
// seed is exactly that, one commit replaying the whole slice — so these tests, which are
// about what each change says, buffer a commit's changes and take them in turn.
type watch struct {
	ch  <-chan Commit
	buf []Event
}

func reader(ch <-chan Commit) *watch { return &watch{ch: ch} }

// next takes the next change, pulling a fresh commit when the current one is spent.
func (w *watch) next(t *testing.T, why string) Event {
	t.Helper()
	for len(w.buf) == 0 {
		select {
		case c, ok := <-w.ch:
			if !ok {
				t.Fatalf("subtree watch closed waiting for %s", why)
			}
			w.buf = c.Changes
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %s", why)
		}
	}
	ev := w.buf[0]
	w.buf = w.buf[1:]
	return ev
}

// quiet asserts the watch stays silent for a grace period — nothing buffered, nothing
// arriving.
func (w *watch) quiet(t *testing.T, why string) {
	t.Helper()
	if len(w.buf) > 0 {
		t.Fatalf("unexpected change %s (%s): %+v", w.buf[0].Ref, why, w.buf[0].To)
	}
	select {
	case c := <-w.ch:
		t.Fatalf("unexpected commit %d (%s): %+v", c.Rev, why, c.Changes)
	case <-time.After(300 * time.Millisecond):
	}
}

// A subtree watch's payload is the ENTITY as of the commit, not the writer's delta —
// including fields written BEFORE the watch opened. The watch maintains the slice as a
// document, and the initial State event is what seeds it; a document seeded with null
// holds only what has been patched since, so a deep write of one field surfaces as an
// entity of one field. That is the level-state contract broken at its root: the trigger
// engine matches on To and binds $vars from it, so a task's `note` vanishes from both
// the match and the agent's prompt.
func TestWatchSubtreeLevelsFieldsWrittenBeforeTheWatch(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"stage": "open", "note": "ship it"}.Payload())

	ch, unsub, err := s.WatchSubtree(Depth, "demo", "pr")
	if err != nil {
		t.Fatalf("watch subtree: %v", err)
	}
	defer unsub()
	r := reader(ch)

	// The open replays what already stands there — level-triggered, so a watcher does not
	// have to have been listening when the entity was written.
	ev := r.next(t, "the replay of what already stood there")
	if ev.Ref != ref || ev.Op != Set {
		t.Fatalf("replayed event = %s %v, want %s set", ev.Ref, ev.Op, ref)
	}
	if got := ToObj(ev.To); got["stage"] != "open" || got["note"] != "ship it" {
		t.Fatalf("replayed To = %v, want the whole entity", got)
	}
	// Once. The slice is replayed at the open, not re-announced afterwards.
	r.quiet(t, "the slice replayed twice")

	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("reviewed")})
	ev = r.next(t, "the deep write of stage")
	if ev.Ref != ref || ev.Op != Set {
		t.Fatalf("event = %s %v, want %s set", ev.Ref, ev.Op, ref)
	}
	got := ToObj(ev.To)
	if got["stage"] != "reviewed" {
		t.Errorf("stage = %v, want reviewed", got["stage"])
	}
	if got["note"] != "ship it" {
		t.Errorf("note = %v, want %q — the payload is the delta, not the entity", got["note"], "ship it")
	}
}

// A deep write reaches a field the initial state did not carry: the entity must still
// read whole afterwards, so the seeded document and the applied deltas are one document
// and not two views of one.
func TestWatchSubtreeLevelsAcrossSeveralDeepWrites(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "task", "t1")
	set(t, s, ref, Obj{"note": "write the doc"}.Payload())

	ch, unsub, err := s.WatchSubtree(Depth, "demo", "task")
	if err != nil {
		t.Fatalf("watch subtree: %v", err)
	}
	defer unsub()
	r := reader(ch)

	if ev := r.next(t, "the replay"); ToObj(ev.To)["note"] != "write the doc" {
		t.Fatalf("replayed To = %v, want the note", ToObj(ev.To))
	}

	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("queued")})
	ev := r.next(t, "stage=queued")
	if got := ToObj(ev.To); got["note"] != "write the doc" || got["stage"] != "queued" {
		t.Fatalf("after first deep write, To = %v, want note+stage", got)
	}

	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("ready")})
	ev = r.next(t, "stage=ready")
	if got := ToObj(ev.To); got["note"] != "write the doc" || got["stage"] != "ready" {
		t.Fatalf("after second deep write, To = %v, want note+stage=ready", got)
	}
}

// A whole-system watch (kind "") seeds the same way — one level shallower.
func TestWatchSubtreeSystemWideLevelsFromInitialState(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "7")
	set(t, s, ref, Obj{"stage": "open", "note": "keep me"}.Payload())

	ch, unsub, err := s.WatchSubtree(Depth, "demo", "")
	if err != nil {
		t.Fatalf("watch subtree: %v", err)
	}
	defer unsub()
	r := reader(ch)

	if ev := r.next(t, "the replay"); ev.Ref != ref {
		t.Fatalf("replayed ref = %s, want %s", ev.Ref, ref)
	}

	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("reviewed")})
	ev := r.next(t, "the deep write of stage")
	if ev.Ref != ref {
		t.Fatalf("event ref = %s, want %s", ev.Ref, ref)
	}
	if got := ToObj(ev.To); got["note"] != "keep me" || got["stage"] != "reviewed" {
		t.Fatalf("To = %v, want note+stage=reviewed", got)
	}
}

// An entity that appears after the watch opens is a Set, and its removal a Delete —
// the replayed seed must not disturb either edge. It also means a REMOVAL of something
// that was only ever in the seed is reported: prev holds the replayed slice, so the
// tombstone diffs against it (before the seed landed, such a delete was invisible, and a
// teardown-on-removal trigger never ran).
func TestWatchSubtreeAppearAndDeleteAfterSeeding(t *testing.T) {
	s := openStore(t)
	seeded := RefAt("demo", "pr", "1")
	set(t, s, seeded, Obj{"stage": "open"}.Payload())

	ch, unsub, err := s.WatchSubtree(Depth, "demo", "pr")
	if err != nil {
		t.Fatalf("watch subtree: %v", err)
	}
	defer unsub()
	r := reader(ch)

	if ev := r.next(t, "the replay"); ev.Ref != seeded || ev.Op != Set {
		t.Fatalf("replayed event = %s %v, want %s set", ev.Ref, ev.Op, seeded)
	}

	fresh := RefAt("demo", "pr", "2")
	set(t, s, fresh, Obj{"stage": "open"}.Payload())
	if ev := r.next(t, "the new entity"); ev.Ref != fresh || ev.Op != Set {
		t.Fatalf("event = %s %v, want %s set", ev.Ref, ev.Op, fresh)
	}
	remove(t, s, seeded)
	if ev := r.next(t, "the removal"); ev.Ref != seeded || ev.Op != Delete {
		t.Fatalf("event = %s %v, want %s delete", ev.Ref, ev.Op, seeded)
	}
}

// A subtree event says WHEN it happened. It carried no revision at all until the model
// needed to ask questions at a commit: the root watch stamped one and the subtree watch did
// not, so the production observation path — which is the subtree one — could report a change
// without being able to say which commit it belonged to.
//
// The load-bearing part is the last assertion: the revision an event carries is one AsOf
// accepts, and reading the entity back there yields what the event said. An event and a
// snapshot taken at its revision are then two views of one moment rather than two moments.
func TestSubtreeWatchStampsTheCommit(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"stage": "open"}.Payload())

	ch, unsub, err := s.WatchSubtree(Depth, "demo", "pr")
	if err != nil {
		t.Fatalf("WatchSubtree: %v", err)
	}
	defer unsub()
	r := reader(ch)

	// The replayed seed is a change like any other and carries the commit it stands at.
	seed := r.next(t, "the replayed seed")
	if seed.Rev == 0 {
		t.Fatal("the replayed seed carries no revision")
	}

	set(t, s, ref, Obj{"stage": "reviewed"}.Payload())
	ev := r.next(t, "the update")
	if ev.Rev == 0 {
		t.Fatal("a subtree event carries no revision")
	}
	if ev.Rev < seed.Rev {
		t.Fatalf("revision went backwards: seed %d, then %d", seed.Rev, ev.Rev)
	}

	// The revision is a real commit: a snapshot there reads what the event reported.
	snap, err := s.AsOf(ev.Rev)
	if err != nil {
		t.Fatalf("AsOf the event's rev %d: %v", ev.Rev, err)
	}
	e, ok, err := snap.Get(ref)
	if err != nil || !ok {
		t.Fatalf("snapshot at the event's rev: ok=%v err=%v", ok, err)
	}
	if got, want := ToObj(e.Payload)["stage"], ToObj(ev.To)["stage"]; got != want {
		t.Fatalf("at rev %d the entity is %v, but the event said %v", ev.Rev, got, want)
	}

	// A delete carries one too — it is the same stream, and a disappearance happens at a
	// commit as much as an appearance does.
	remove(t, s, ref)
	del := r.next(t, "the delete")
	if del.Op != Delete {
		t.Fatalf("event = %v, want delete", del.Op)
	}
	if del.Rev < ev.Rev {
		t.Fatalf("delete revision %d is behind the update's %d", del.Rev, ev.Rev)
	}
}

// EDGE-FAITHFUL: one commit in, one commit out. The watch used to coalesce a burst — apply
// every queued delta, then diff once — which is exact for a level and lossy for an edge: a
// value written at one commit and overwritten at the next left only the later one in a merged
// diff, and the transition between them could not be recovered by anyone reading the stream.
//
// Written as a burst on purpose: the writes go in back to back so the watcher has several
// deltas queued when it wakes, which is precisely the case the coalescing existed for.
func TestWatchSubtreeDoesNotCoalesceCommits(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")

	ch, unsub, err := s.WatchSubtree(Depth, "demo", "pr")
	if err != nil {
		t.Fatalf("WatchSubtree: %v", err)
	}
	defer unsub()

	stages := []string{"open", "reviewing", "reviewed", "merged"}
	for _, st := range stages {
		set(t, s, ref, Obj{"stage": st}.Payload())
	}

	// Every transition is reported, in order, one commit each — none merged away.
	var got []string
	var revs []int64
	deadline := time.After(5 * time.Second)
	for len(got) < len(stages) {
		select {
		case c, ok := <-ch:
			if !ok {
				t.Fatal("watch closed early")
			}
			if len(c.Changes) != 1 {
				t.Fatalf("commit %d carries %d changes, want 1 — these writes were separate "+
					"transactions: %+v", c.Rev, len(c.Changes), c.Changes)
			}
			got = append(got, Str(c.Changes[0].To, "stage"))
			revs = append(revs, c.Rev)
		case <-deadline:
			t.Fatalf("saw %v, want %v — a transition was coalesced away", got, stages)
		}
	}
	for i, want := range stages {
		if got[i] != want {
			t.Fatalf("transition %d = %q, want %q (saw %v)", i, got[i], want, got)
		}
	}
	// Distinct commits, ascending: each write is its own revision, and the stream says so.
	for i := 1; i < len(revs); i++ {
		if revs[i] <= revs[i-1] {
			t.Fatalf("revisions not ascending: %v", revs)
		}
	}
}

// A commit that changes nothing in the watched slice emits nothing. The watch reports one
// subtree; a consumer of it has no use for the revisions where its slice stood still.
func TestWatchSubtreeIgnoresOtherSlices(t *testing.T) {
	s := openStore(t)
	ch, unsub, err := s.WatchSubtree(Depth, "demo", "pr")
	if err != nil {
		t.Fatalf("WatchSubtree: %v", err)
	}
	defer unsub()
	r := reader(ch)

	set(t, s, RefAt("demo", "task", "a"), Obj{"n": 1}.Payload())
	set(t, s, RefAt("other", "pr", "b"), Obj{"n": 2}.Payload())
	r.quiet(t, "writes to slices this watch does not cover")

	set(t, s, RefAt("demo", "pr", "1"), Obj{"stage": "open"}.Payload())
	if ev := r.next(t, "a write to the watched slice"); ev.Ref.ID() != "1" {
		t.Fatalf("event = %s, want demo.pr.1", ev.Ref)
	}
}

// A scoped subtree watch — what a per-scope engine runs (Engine.ObserveScoped) — seeds the
// entities the scope inherits from baseline, and reports a scoped delete of one of them as
// a Delete. So hiding an inherited entity is not merely a read-time fact: it is an edge the
// scope's triggers fire on, the same one they would see for an entity the scope wrote itself.
//
// This is the event half of TestLogdStore_ScopeHidesAnInheritedEntity, and it is what a
// scoped reflection needs: a converge inside a scope that tombstones a branch the checkout
// no longer has must reach that scope's rules. See docs/sources.md.
func TestScopedWatchSeesADeleteOfAnInheritedEntity(t *testing.T) {
	base := openStore(t)
	ref := RefAt("demo", "pr", "inherited")
	set(t, base, ref, Obj{"stage": "open"}.Payload())

	scoped, ok := base.Scope("me").(*LogdStore)
	if !ok {
		t.Fatal("Scope did not return a *LogdStore")
	}
	ch, unsub, err := scoped.WatchSubtree(Depth, "demo", "pr")
	if err != nil {
		t.Fatalf("scoped WatchSubtree: %v", err)
	}
	defer unsub()
	r := reader(ch)

	// The seed: a scoped watch replays what the scope reads, inherited entities included.
	if ev := r.next(t, "the seed of the inherited entity"); ev.Ref != ref || ev.Op != Set {
		t.Fatalf("seed = %s %s, want %s set", ev.Ref, ev.Op, ref)
	}

	remove(t, scoped, ref)
	if ev := r.next(t, "the scoped tombstone"); ev.Ref != ref || ev.Op != Delete {
		t.Fatalf("after a scoped delete of an inherited entity: %s %s, want %s delete",
			ev.Ref, ev.Op, ref)
	}
}

// A watch is addressed by a PREFIX of an address, and the (system, kind) pair is one way of
// spelling one. This pins the equivalence rather than leaving it to the reader: the same slice
// arrives whether the whole-system watch is asked for as one segment or as two with an empty
// second, which is the compatibility the variadic signature is carrying.
func TestWatchSubtreeTakesAPrefix(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "9")
	set(t, s, ref, Obj{"stage": "open"}.Payload())

	for _, prefix := range [][]string{{"demo"}, {"demo", ""}} {
		ch, unsub, err := s.WatchSubtree(Depth, prefix...)
		if err != nil {
			t.Fatalf("watch %q: %v", prefix, err)
		}
		if ev := reader(ch).next(t, "the replay"); ev.Ref != ref {
			t.Errorf("watch %q replayed %s, want %s", prefix, ev.Ref, ref)
		}
		unsub()
	}
}
