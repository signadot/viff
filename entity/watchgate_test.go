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
	eventuallyWithin(t, 20*time.Second, "every watch opened", func() bool {
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
	eventuallyWithin(t, 30*time.Second, "every watch re-established", func() bool {
		return subtreeReopens.Load() >= before+n
	})
	eventuallyWithin(t, 10*time.Second, "the line drained", func() bool {
		return s.gate.waiting.Load() == 0 && s.gate.inFlight.Load() == 0
	})
	if h := s.gate.high.Load(); h > 2 {
		t.Fatalf("%d watches were opening at once across the drop; the line allows 2", h)
	}

	// And every one is back and live: a write at each slice reaches its watch. (The reopen
	// count above is reached by one watch reopening twice as well; this is not.)
	var was [n]int64
	for i := range was {
		was[i] = seen[i].Load()
	}
	for i := 0; i < n; i++ {
		if _, _, err := s.Write(Write{
			At:    RefAt("demo", fmt.Sprintf("w%d", i), "y").At(),
			Value: Obj{"stage": "open"}.Payload(),
		}); err != nil {
			t.Fatalf("write w%d after the drop: %v", i, err)
		}
	}
	eventuallyWithin(t, 10*time.Second, "every re-established watch reports its write", func() bool {
		for i := range seen {
			if seen[i].Load() <= was[i] {
				return false
			}
		}
		return true
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

// A watch asked for its progress (WatchSubtreeProgress) is the watermark's, and every
// attributed read above the watermark waits on it: it opens ahead of the line, where a plain
// watch waits its turn.
func TestAProgressWatchOpensAheadOfTheLine(t *testing.T) {
	docd := startDocd(t, startLogd(t))
	s, err := OpenLogdStore(LogdConfig{Addr: docd.ClientTCPAddr(), ClientID: "verse-test", WatchSlots: 1})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// The one slot is held for the length of the test, and given back only once the store
	// is closed, so the watch left in line is refused rather than opened on a closing store.
	if err := s.gate.take(t.Context()); err != nil {
		t.Fatalf("hold the slot: %v", err)
	}
	t.Cleanup(func() {
		s.Close()
		s.gate.give()
	})

	type opened struct {
		stop func()
		err  error
	}
	plain := make(chan opened, 1)
	go func() {
		_, stop, err := s.WatchSubtree(3, "demo", "plain")
		plain <- opened{stop, err}
	}()
	asked := make(chan opened, 1)
	go func() {
		_, stop, _, err := s.WatchSubtreeProgress(3, "demo", "asked")
		asked <- opened{stop, err}
	}()
	select {
	case o := <-asked:
		if o.err != nil {
			t.Fatalf("progress watch: %v", o.err)
		}
		o.stop()
	case <-time.After(10 * time.Second):
		t.Fatal("the progress watch waited in the line")
	}
	select {
	case o := <-plain:
		if o.err == nil {
			o.stop()
		}
		t.Fatal("the plain watch opened past a held slot")
	case <-time.After(time.Second):
	}
	eventually(t, "the plain watch is in line", func() bool { return s.gate.waiting.Load() == 1 })
}

// A store configured with no line (WatchSlots below zero) opens every watch as it is asked
// for: the off switch, for a deployment where the line turns out to be in the way.
func TestNoLineOpensEveryWatchAtOnce(t *testing.T) {
	docd := startDocd(t, startLogd(t))
	s, err := OpenLogdStore(LogdConfig{Addr: docd.ClientTCPAddr(), ClientID: "verse-test", WatchSlots: -1})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if s.gate.slots != nil {
		t.Fatal("a store with WatchSlots below zero keeps a line")
	}
	for i := 0; i < 6; i++ {
		_, stop, err := s.WatchSubtree(3, "demo", fmt.Sprintf("w%d", i))
		if err != nil {
			t.Fatalf("watch w%d: %v", i, err)
		}
		t.Cleanup(stop)
	}
	if w, o := s.gate.waiting.Load(), s.gate.inFlight.Load(); w != 0 || o != 0 {
		t.Fatalf("no line, yet %d waiting and %d opening were counted", w, o)
	}
}
