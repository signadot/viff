package entity

import (
	"strconv"
	"testing"
	"time"
)

// A SUBTREE WATCH SURVIVES A BURST IT CANNOT KEEP UP WITH. NOT RUN BY DEFAULT — set
// GOTEST_LONG=1; provoking it takes thousands of writes.
//
// The hazard is that a watch can END for reasons the consumer did not cause — logd drops a
// watcher whose buffer it cannot drain — and a consumer that only ever waits to be told is a
// consumer that waits forever whenever the telling is what breaks. That is not hypothetical: it
// is how a slice under sustained writes came to stop delivering and never recover, and a trigger
// over that slice simply stopped firing, with no error anywhere a charter author would see.
//
// What is asserted is the property, not the mechanism: after a burst the watch is STILL LIVE, so
// a write that lands afterwards is reported. Deliberately nothing about how long the burst itself
// takes to drain, or how many of its events arrive — that is the substrate's throughput, it is
// being worked on, and a test that pinned it would fail for the wrong reason every time it moved.
// Whether recovery comes from the server naming the end (go-tony ≥ v0.0.112) or from the heal
// timer noticing the silence (see subtreeHealAfter) is likewise not this test's business.
func TestSubtreeWatchSurvivesABurst(t *testing.T) {
	requireLong(t)
	defer func(a, m time.Duration) { subtreeHealAfter, subtreeHealMax = a, m }(subtreeHealAfter, subtreeHealMax)
	subtreeHealAfter, subtreeHealMax = 2*time.Second, 4*time.Second

	s := openStore(t)
	ch, unsub, err := s.WatchSubtree(3, "bench", "k")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()

	const marker = "the-one-that-matters"
	seen := make(chan Ref, 8192)
	go func() {
		for c := range ch {
			for _, ev := range c.Changes {
				select {
				case seen <- ev.Ref:
				default: // the burst is not what this test reads; only the marker matters
				}
			}
		}
	}()

	// The burst: enough to outrun the watcher and get it dropped.
	for i := range 2000 {
		if ok, _, err := s.Write(Write{
			At: RefAt("bench", "k", strconv.Itoa(i)).At(), Value: Obj{"n": i}.Payload(),
		}); err != nil || !ok {
			t.Fatalf("write %d: ok=%v err=%v", i, ok, err)
		}
	}

	// Wait for the stream to go QUIET before the marker, which is what keeps this independent
	// of how fast the substrate drains: quiet means either the burst finished arriving or the
	// watch is dead, and the marker tells those apart. Writing the marker while the backlog is
	// still draining would instead queue it behind however long that takes, and the test would
	// be measuring throughput again.
	quiet := 3 * time.Second
	drain := time.After(5 * time.Minute)
	for settled := false; !settled; {
		select {
		case <-seen:
		case <-time.After(quiet):
			settled = true
		case <-drain:
			settled = true
		}
	}

	want := RefAt("bench", "k", marker)
	if ok, _, err := s.Write(Write{At: want.At(), Value: Obj{"n": -1}.Payload()}); err != nil || !ok {
		t.Fatalf("marker write: ok=%v err=%v", ok, err)
	}

	deadline := time.After(90 * time.Second)
	for {
		select {
		case ref := <-seen:
			if ref == want {
				return // the watch is still delivering
			}
		case <-deadline:
			t.Fatal("a write made after the burst was never reported: the watch stopped " +
				"delivering and did not come back, so a trigger over this slice has gone quiet")
		}
	}
}
