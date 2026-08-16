package entity

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// What the substrate takes, so a number chosen for pacing is chosen against evidence.
//
// NOT RUN BY DEFAULT — set GOTEST_LONG=1. It writes 100k entities under 1k live subtree
// watches, which is minutes of wall clock.
//
// The question it answers is not "how fast can this go" but "what does a steady background hum
// cost". The cycle throttle paces a tripped chain at a fixed interval (docs/working/
// cycle-throttle.md), and that interval wants sanity-checking against a real write rate: too
// short and a paced runaway is indistinguishable from load, too long and a control loop someone
// meant to write crawls.
//
// No figures are recorded in this file on purpose. What it measures belongs to the substrate and
// moves with it; a comment asserting last month's number would be read as a promise. Run it.
//
// Knobs, for a smaller or larger probe:
//
//	BENCH_WRITES=100000 BENCH_WATCHES=1000 GOTEST_LONG=1 go test ./entity/ -run LoadUnderWatches -v -timeout 3600s
func TestLoadUnderWatches(t *testing.T) {
	requireLong(t)

	writes := envInt("BENCH_WRITES", 100_000)
	watches := envInt("BENCH_WATCHES", 1_000)
	perKind := writes / watches
	if perKind < 1 {
		perKind = 1
	}

	s := openStore(t)

	// Every watch is on its own (system, kind) slice, the shape a scoped engine takes: one
	// subtree watch per installed trigger, all multiplexed on the one session.
	var delivered atomic.Int64
	var lastEvent atomic.Int64 // unix nanos of the most recent event seen
	var wg sync.WaitGroup
	openStart := time.Now()
	for i := range watches {
		ch, unsub, err := s.WatchSubtree("bench", "k"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("watch %d: %v", i, err)
		}
		defer unsub()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range ch {
				delivered.Add(int64(len(c.Changes)))
				lastEvent.Store(time.Now().UnixNano())
			}
		}()
	}
	openTook := time.Since(openStart)
	t.Logf("opened %d subtree watches in %v (%v each)", watches, openTook, openTook/time.Duration(watches))

	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)

	// Writes, sequentially: the session serializes them anyway, and a clean per-op number is
	// what a pacing interval has to be compared against.
	start := time.Now()
	report := writes / 10
	if report < 1 {
		report = 1
	}
	for i := range writes {
		ref := RefAt("bench", "k"+strconv.Itoa(i%watches), strconv.Itoa(i/watches))
		ok, _, err := s.Write(Write{At: ref.At(), Value: Obj{"n": i}.Payload()})
		if err != nil || !ok {
			t.Fatalf("write %d: ok=%v err=%v", i, ok, err)
		}
		if (i+1)%report == 0 {
			el := time.Since(start)
			t.Logf("  %d/%d writes in %v (%.0f/s, %v/op), %d events delivered",
				i+1, writes, el.Round(time.Millisecond),
				float64(i+1)/el.Seconds(), el/time.Duration(i+1), delivered.Load())
		}
	}
	wrote := time.Since(start)

	// Let the watch stream catch up, so the lag reported is delivery and not the harness.
	deadline := time.Now().Add(60 * time.Second)
	for delivered.Load() < int64(writes) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	lag := time.Duration(0)
	if n := lastEvent.Load(); n > 0 {
		lag = time.Unix(0, n).Sub(start.Add(wrote))
	}

	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	t.Logf("RESULT writes=%d watches=%d perKind=%d", writes, watches, perKind)
	t.Logf("  wrote in %v — %.0f writes/s, %v per write",
		wrote.Round(time.Millisecond), float64(writes)/wrote.Seconds(), wrote/time.Duration(writes))
	t.Logf("  events delivered %d/%d, last event %v after the last write",
		delivered.Load(), writes, lag.Round(time.Millisecond))
	t.Logf("  heap %s → %s (+%s)", mib(m0.HeapAlloc), mib(m1.HeapAlloc), mib(m1.HeapAlloc-m0.HeapAlloc))

	if delivered.Load() < int64(writes) {
		t.Errorf("only %d of %d events were delivered — the watch stream did not keep up",
			delivered.Load(), writes)
	}
}

func mib(b uint64) string { return fmt.Sprintf("%.0fMiB", float64(b)/(1<<20)) }

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
