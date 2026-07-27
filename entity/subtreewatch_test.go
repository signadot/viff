package entity

import (
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
)

// nextEvent takes the next event off a subtree watch, failing if none arrives.
func nextEvent(t *testing.T, ch <-chan Event, why string) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatalf("subtree watch closed waiting for %s", why)
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", why)
	}
	return Event{}
}

// noEvent asserts the watch stays quiet for a grace period.
func noEvent(t *testing.T, ch <-chan Event, why string) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %s (%s): %+v", ev.Ref, why, ev.To)
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	set(t, s, ref, Obj{"stage": "open", "note": "ship it"}.Payload())

	ch, unsub, err := s.WatchSubtree("demo", "pr")
	if err != nil {
		t.Fatalf("watch subtree: %v", err)
	}
	defer unsub()

	// The open replays what already stands there — level-triggered, so a watcher does not
	// have to have been listening when the entity was written.
	ev := nextEvent(t, ch, "the replay of what already stood there")
	if ev.Ref != ref || ev.Op != Set {
		t.Fatalf("replayed event = %s %v, want %s set", ev.Ref, ev.Op, ref)
	}
	if got := ToObj(ev.To); got["stage"] != "open" || got["note"] != "ship it" {
		t.Fatalf("replayed To = %v, want the whole entity", got)
	}
	// Once. The slice is replayed at the open, not re-announced afterwards.
	noEvent(t, ch, "the slice replayed twice")

	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("reviewed")})
	ev = nextEvent(t, ch, "the deep write of stage")
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
	ref := Ref{System: "demo", Kind: "task", ID: "t1"}
	set(t, s, ref, Obj{"note": "write the doc"}.Payload())

	ch, unsub, err := s.WatchSubtree("demo", "task")
	if err != nil {
		t.Fatalf("watch subtree: %v", err)
	}
	defer unsub()

	if ev := nextEvent(t, ch, "the replay"); ToObj(ev.To)["note"] != "write the doc" {
		t.Fatalf("replayed To = %v, want the note", ToObj(ev.To))
	}

	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("queued")})
	ev := nextEvent(t, ch, "stage=queued")
	if got := ToObj(ev.To); got["note"] != "write the doc" || got["stage"] != "queued" {
		t.Fatalf("after first deep write, To = %v, want note+stage", got)
	}

	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("ready")})
	ev = nextEvent(t, ch, "stage=ready")
	if got := ToObj(ev.To); got["note"] != "write the doc" || got["stage"] != "ready" {
		t.Fatalf("after second deep write, To = %v, want note+stage=ready", got)
	}
}

// A whole-system watch (kind "") seeds the same way — one level shallower.
func TestWatchSubtreeSystemWideLevelsFromInitialState(t *testing.T) {
	s := openStore(t)
	ref := Ref{System: "demo", Kind: "pr", ID: "7"}
	set(t, s, ref, Obj{"stage": "open", "note": "keep me"}.Payload())

	ch, unsub, err := s.WatchSubtree("demo", "")
	if err != nil {
		t.Fatalf("watch subtree: %v", err)
	}
	defer unsub()

	if ev := nextEvent(t, ch, "the replay"); ev.Ref != ref {
		t.Fatalf("replayed ref = %s, want %s", ev.Ref, ref)
	}

	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("reviewed")})
	ev := nextEvent(t, ch, "the deep write of stage")
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
	seeded := Ref{System: "demo", Kind: "pr", ID: "1"}
	set(t, s, seeded, Obj{"stage": "open"}.Payload())

	ch, unsub, err := s.WatchSubtree("demo", "pr")
	if err != nil {
		t.Fatalf("watch subtree: %v", err)
	}
	defer unsub()

	if ev := nextEvent(t, ch, "the replay"); ev.Ref != seeded || ev.Op != Set {
		t.Fatalf("replayed event = %s %v, want %s set", ev.Ref, ev.Op, seeded)
	}

	fresh := Ref{System: "demo", Kind: "pr", ID: "2"}
	set(t, s, fresh, Obj{"stage": "open"}.Payload())
	if ev := nextEvent(t, ch, "the new entity"); ev.Ref != fresh || ev.Op != Set {
		t.Fatalf("event = %s %v, want %s set", ev.Ref, ev.Op, fresh)
	}
	remove(t, s, seeded)
	if ev := nextEvent(t, ch, "the removal"); ev.Ref != seeded || ev.Op != Delete {
		t.Fatalf("event = %s %v, want %s delete", ev.Ref, ev.Op, seeded)
	}
}
