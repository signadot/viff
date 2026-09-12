// Probe: what verse pays the store for ONE rule evaluation, and how that scales with the log.
//
// sfwhh9sv asks for the verse side of the read that did not come back. The engine reads
// `store.AsOf(c.Rev, paths(reads)...)` per (rule, commit); this measures what that costs as the
// log grows, narrowed and not, idle and under concurrent writes.
//
// Run: go test ./scratchpad/ -run TestAsOfCost -v -count=1
package asofcost

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// grow writes n entities under `src.<repo>.commit.<i>`, one commit each — a source's first pass.
func grow(t testing.TB, s entity.Store, from, n int) {
	t.Helper()
	for i := from; i < from+n; i++ {
		ref := entity.RefAt("src", "repo", "commit", fmt.Sprintf("c%06d", i))
		entitytest.Set(t, s, ref, entity.Obj{
			"sha": fmt.Sprintf("%040x", i), "subject": "a commit message of an ordinary length",
			"author": "someone@example.com", "when": "2026-09-16T10:43:54Z",
		}.Payload())
	}
}

func ms(d time.Duration) string { return fmt.Sprintf("%6.1fms", float64(d.Microseconds())/1000) }

func TestAsOfCost(t *testing.T) {
	store := entitytest.Store(t)

	// The two subtrees a drift-shaped rule reads: its own cursor, and the head it compares to.
	head := entity.RefAt("src", "repo", "head")
	cursor := entity.RefAt("drive", "cursor", "at")
	entitytest.Set(t, store, head, entity.Obj{"sha": "aaa"}.Payload())
	entitytest.Set(t, store, cursor, entity.Obj{"sha": "aaa", "stage": "idle"}.Payload())

	reads := [][]string{{"drive", "cursor", "at"}, {"src", "repo", "head"}}

	time1 := func(f func() error) time.Duration {
		start := time.Now()
		if err := f(); err != nil {
			t.Fatalf("read: %v", err)
		}
		return time.Since(start)
	}

	t.Logf("%8s %12s %12s %12s", "commits", "AsOf(within)", "AsOf(root)", "AsOf(rev-1)")
	total := 0
	for _, step := range []int{100, 400, 500, 1000, 1000} {
		grow(t, store, total, step)
		total += step
		rev := store.Head()
		var narrowed, whole, before time.Duration
		// Three reads of the same commit: what the engine asks (narrowed), the whole root, and
		// the C-1 an `on` edge adds.
		narrowed = time1(func() error { _, err := store.AsOf(rev, reads...); return err })
		whole = time1(func() error { _, err := store.AsOf(rev); return err })
		before = time1(func() error { _, err := store.AsOf(rev-1, reads...); return err })
		t.Logf("%8d %12s %12s %12s", total, ms(narrowed), ms(whole), ms(before))
	}
}

// AND UNDER LOAD, which is the storm: reads of a delivered commit while writes land behind them.
func TestAsOfCostUnderWrites(t *testing.T) {
	store := entitytest.Store(t)
	head := entity.RefAt("src", "repo", "head")
	cursor := entity.RefAt("drive", "cursor", "at")
	entitytest.Set(t, store, head, entity.Obj{"sha": "aaa"}.Payload())
	entitytest.Set(t, store, cursor, entity.Obj{"sha": "aaa", "stage": "idle"}.Payload())
	reads := [][]string{{"drive", "cursor", "at"}, {"src", "repo", "head"}}

	grow(t, store, 0, 1000) // a log of a realistic size

	ctx, cancel := context.WithCancel(context.Background())
	var written atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 100000; ; i++ {
			if ctx.Err() != nil {
				return
			}
			ref := entity.RefAt("src", "other", "commit", fmt.Sprintf("c%06d", i))
			if _, _, err := store.Write(entity.Write{At: ref.At(), Value: entity.Obj{
				"sha": fmt.Sprintf("%040x", i), "subject": "another pass, landing beside the reads",
			}.Payload()}); err != nil {
				return
			}
			written.Add(1)
		}
	}()
	defer func() { cancel(); <-done }()
	time.Sleep(250 * time.Millisecond) // let the writer get going

	var worst time.Duration
	var sum time.Duration
	const n = 20
	for i := 0; i < n; i++ {
		rev := store.Head()
		start := time.Now()
		if _, err := store.AsOf(rev, reads...); err != nil {
			t.Fatalf("read at rev %d: %v", rev, err)
		}
		d := time.Since(start)
		sum += d
		if d > worst {
			worst = d
		}
	}
	t.Logf("%d narrowed reads while writing: mean %s worst %s (writes landed: %d)",
		n, ms(sum/n), ms(worst), written.Load())
}

// DOES A SLOW READ BLOCK A CHEAP ONE? The engine's evaluate read is narrowed and costs ~2ms;
// the firing path's snapshot (trigger/deref.go, trigger/trigger.go) is not narrowed and costs
// seconds at this size. If they share the session's capacity, the cheap read waits for the slow
// one — which is how a 2ms read spends a 10s budget.
func TestACheapReadBehindAWholeRootRead(t *testing.T) {
	store := entitytest.Store(t)
	head := entity.RefAt("src", "repo", "head")
	cursor := entity.RefAt("drive", "cursor", "at")
	entitytest.Set(t, store, head, entity.Obj{"sha": "aaa"}.Payload())
	entitytest.Set(t, store, cursor, entity.Obj{"sha": "aaa", "stage": "idle"}.Payload())
	reads := [][]string{{"drive", "cursor", "at"}, {"src", "repo", "head"}}
	grow(t, store, 0, 3000)

	rev := store.Head()
	// Alone, for the baseline.
	start := time.Now()
	if _, err := store.AsOf(rev, reads...); err != nil {
		t.Fatal(err)
	}
	alone := time.Since(start)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var roots atomic.Int64
	for i := 0; i < 4; i++ { // four firings' snapshots in flight, which a storm easily has
		go func() {
			for ctx.Err() == nil {
				if _, err := store.AsOf(rev); err == nil {
					roots.Add(1)
				}
			}
		}()
	}
	go func() { defer close(done); <-ctx.Done() }()
	time.Sleep(300 * time.Millisecond)

	var worst, sum time.Duration
	const n = 10
	for i := 0; i < n; i++ {
		s := time.Now()
		if _, err := store.AsOf(rev, reads...); err != nil {
			t.Fatalf("narrowed read failed while root reads ran: %v", err)
		}
		d := time.Since(s)
		sum += d
		if d > worst {
			worst = d
		}
	}
	cancel()
	<-done
	t.Logf("narrowed read alone %s; behind 4 whole-root reads: mean %s worst %s (%d root reads done)",
		ms(alone), ms(sum/n), ms(worst), roots.Load())
}

// The growth curve, further out: what a whole-verse read costs as a store fills, which is what
// decides when it crosses the 10s budget.
func TestWholeRootGrowth(t *testing.T) {
	store := entitytest.Store(t)
	total := 0
	t.Logf("%8s %12s %12s", "commits", "AsOf(root)", "AsOf(within)")
	for _, step := range []int{1000, 1000, 1000, 1000, 1000, 1000} {
		grow(t, store, total, step)
		total += step
		rev := store.Head()
		start := time.Now()
		if _, err := store.AsOf(rev); err != nil {
			t.Fatalf("root read at %d commits: %v", total, err)
		}
		root := time.Since(start)
		start = time.Now()
		if _, err := store.AsOf(rev, []string{"src", "repo", "head"}); err != nil {
			t.Fatal(err)
		}
		t.Logf("%8d %12s %12s", total, ms(root), ms(time.Since(start)))
	}
}
