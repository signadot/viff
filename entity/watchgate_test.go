package entity

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// A drop ends every watch a store holds, and every one of them wants back at once. The line
// (watchGate) is what makes that a few at a time: each opening picture is a read the store
// serves, a session has a few reads at a time, and a session asked for them all at once has
// its ping queue behind them and its heartbeat tear it down into another round of the same
// (git-issue wz2pp5j0). Measured rather than reasoned: the most watches opening at once never
// exceeds the slots, across the first opens and across the drop, and every watch is back and
// live after it.
func TestADropReopensWatchesAFewAtATime(t *testing.T) {
	docd := startDocd(t, startLogd(t))
	g := newGate(t, docd.ClientTCPAddr())
	s, err := OpenLogdStore(LogdConfig{Addr: g.addr(), ClientID: "verse-test", WatchSlots: 2})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	const n = 12
	// Something at every slice, so each opening picture is a read with something in it.
	for i := 0; i < n; i++ {
		if _, _, err := s.Write(Write{
			At:    RefAt("demo", fmt.Sprintf("w%d", i), "x").At(),
			Value: Obj{"stage": "open"}.Payload(),
		}); err != nil {
			t.Fatalf("write w%d: %v", i, err)
		}
	}
	var seen [n]atomic.Int64
	for i := 0; i < n; i++ {
		ch, stop, err := s.WatchSubtree(3, "demo", fmt.Sprintf("w%d", i))
		if err != nil {
			t.Fatalf("watch w%d: %v", i, err)
		}
		t.Cleanup(stop)
		go func(i int, ch <-chan Commit) {
			for range ch {
				seen[i].Add(1)
			}
		}(i, ch)
	}
	// Every watch has its opening picture: the first diff reports the whole slice.
	holdsWithin(t, 20*time.Second, "every watch opened", func() bool {
		for i := range seen {
			if seen[i].Load() == 0 {
				return false
			}
		}
		return true
	})
	if h := s.gate.high.Load(); h > 2 {
		t.Fatalf("%d watches were opening at once; the line allows 2", h)
	}

	before := subtreeReopens.Load()
	g.cut()
	holdsWithin(t, 30*time.Second, "every watch re-established", func() bool {
		return subtreeReopens.Load() >= before+n
	})
	holdsWithin(t, 10*time.Second, "the line drained", func() bool {
		waiting, opening := s.WatchesWaiting()
		return waiting == 0 && opening == 0
	})
	if h := s.gate.high.Load(); h > 2 {
		t.Fatalf("%d watches were opening at once across the drop; the line allows 2", h)
	}

	// And they are live: a write at one slice reaches its watch.
	was := seen[3].Load()
	if _, _, err := s.Write(Write{
		At:    RefAt("demo", "w3", "y").At(),
		Value: Obj{"stage": "open"}.Payload(),
	}); err != nil {
		t.Fatalf("write after the drop: %v", err)
	}
	holdsWithin(t, 10*time.Second, "the re-established watch reports a write", func() bool {
		return seen[3].Load() > was
	})
}

// A watch on a path holding nothing is seeded with Absent, and gives its slot back on it: a
// store with one slot opens a second watch after such a one, rather than waiting for a write
// to the first's path that may never come.
func TestAWatchOnNothingHoldsNoSlot(t *testing.T) {
	docd := startDocd(t, startLogd(t))
	s, err := OpenLogdStore(LogdConfig{Addr: docd.ClientTCPAddr(), ClientID: "verse-test", WatchSlots: 1})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	first, stop, err := s.WatchSubtree(3, "demo", "nothing")
	if err != nil {
		t.Fatalf("watch on nothing: %v", err)
	}
	t.Cleanup(stop)
	go func() {
		for range first {
		}
	}()

	type opened struct {
		stop func()
		err  error
	}
	second := make(chan opened, 1)
	go func() {
		_, stop, err := s.WatchSubtree(3, "demo", "nothing-either")
		second <- opened{stop, err}
	}()
	select {
	case o := <-second:
		if o.err != nil {
			t.Fatalf("second watch: %v", o.err)
		}
		o.stop()
	case <-time.After(10 * time.Second):
		t.Fatal("the second watch waited on the one slot, which a watch on nothing kept past its Absent")
	}
}

// holdsWithin polls cond until it holds or the deadline passes, and fails with why otherwise.
func holdsWithin(t *testing.T, within time.Duration, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %s: %s", within, why)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
