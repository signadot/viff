package entity

import "testing"

// A subtree watch reports a REMOVAL, not only the writes. The slice is maintained as a
// document and the entity set is read out of it, so a delta that takes an entity away has
// to land in the document the same way one that changes it does — otherwise the entity is
// still standing in the watcher's copy, and every consumer of the watch goes on acting on
// something the store no longer holds.
func TestWatchSubtreeReportsARemoval(t *testing.T) {
	s := openStore(t)
	keep := RefAt("demo", "pr", "1")
	gone := RefAt("demo", "pr", "2")
	set(t, s, keep, Obj{"stage": "open"}.Payload())
	set(t, s, gone, Obj{"stage": "open"}.Payload())

	ch, unsub, err := s.WatchSubtree(Depth, "demo", "pr")
	if err != nil {
		t.Fatalf("watch subtree: %v", err)
	}
	defer unsub()
	r := reader(ch)
	// The open replays both, in ref order.
	for range 2 {
		if ev := r.next(t, "the replay of what already stood there"); ev.Op != Set {
			t.Fatalf("replayed %s as %v, want set", ev.Ref, ev.Op)
		}
	}

	mustWrite(t, s, Write{At: gone.At()}) // no value: take it away
	ev := r.next(t, "the removal")
	if ev.Ref != gone || ev.Op != Delete {
		t.Fatalf("event = %s %v, want %s delete", ev.Ref, ev.Op, gone)
	}
	r.quiet(t, "anything after the removal")
}
