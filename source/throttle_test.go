package source_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/source"
)

// counted is a Source that records what it was asked and answers whatever it was told to.
type counted struct {
	mu    sync.Mutex
	calls []entity.Ref
	err   error
	owns  []source.Own
}

func (c *counted) System() string { return "demo" }

func (c *counted) Owns() []source.Own { return c.owns }

func (c *counted) Reflect(_ context.Context, ref entity.Ref) (source.Reflection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, ref)
	if c.err != nil {
		return source.Reflection{}, c.err
	}
	rn := source.Reflection{Covers: []entity.Pattern{source.Cover("demo", "*", "*")}}
	rn.Add(entity.RefAt("demo", "thing", "1"),
		entity.Obj{"calls": len(c.calls)})
	return rn, nil
}

func (c *counted) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// A pass repeated inside the window answers from the last one, and a pass for a DIFFERENT ref
// is a different question — so one ref's chatter cannot starve another's.
func TestThrottleAnswersFromTheLastReflectionPerRef(t *testing.T) {
	ctx := context.Background()
	src := &counted{}
	th := source.Throttle(src, time.Minute)

	a := entity.RefAt("demo", "thing", "a")
	b := entity.RefAt("demo", "thing", "b")
	for range 3 {
		if _, err := th.Reflect(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if got := src.count(); got != 1 {
		t.Fatalf("three passes over one ref inside the window asked %d times, want 1", got)
	}
	if _, err := th.Reflect(ctx, b); err != nil {
		t.Fatal(err)
	}
	if got := src.count(); got != 2 {
		t.Fatalf("a pass over another ref asked %d times in total, want 2 — the window is "+
			"per-ref, or one ref's storm silences every other", got)
	}
}

// A window of zero is no window at all: Throttle hands back the source it was given.
func TestThrottleWithNoWindowIsTheSourceItself(t *testing.T) {
	src := &counted{}
	if source.Throttle(src, 0) != source.Source(src) {
		t.Fatal("a zero window wrapped the source anyway")
	}
	ctx := context.Background()
	th := source.Throttle(src, -time.Second)
	for range 3 {
		if _, err := th.Reflect(ctx, entity.Ref{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := src.count(); got != 3 {
		t.Fatalf("asked %d times, want 3", got)
	}
}

// THE ONE THAT MATTERS: a failure is never cached. Caching one would turn a single bad minute
// into a whole window of a source claiming an external system is empty — which converge would
// believe, because an empty reflection carrying covers is an instruction to tombstone.
func TestThrottleNeverCachesAFailure(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("the external system said no")
	src := &counted{err: boom}
	th := source.Throttle(src, time.Hour)

	for range 3 {
		rn, err := th.Reflect(ctx, entity.Ref{})
		if !errors.Is(err, boom) {
			t.Fatalf("got %v, want the failure through", err)
		}
		if len(rn.Covers) != 0 || len(rn.Entities) != 0 {
			t.Fatalf("a failed reflect answered with a reflection: %+v", rn)
		}
	}
	if got := src.count(); got != 3 {
		t.Fatalf("asked %d times, want 3 — a failure was cached", got)
	}

	// And once it recovers, the answer is cached from there.
	src.mu.Lock()
	src.err = nil
	src.mu.Unlock()
	for range 2 {
		if _, err := th.Reflect(ctx, entity.Ref{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := src.count(); got != 4 {
		t.Fatalf("asked %d times, want 4 — the first success should have been cached", got)
	}
}

// The wrapper must not swallow the ownership claim: a source that owns nothing is refused an
// install beside anything it cannot be proved apart from.
func TestThrottleKeepsTheOwnershipClaim(t *testing.T) {
	src := &counted{owns: []source.Own{{At: "demo.*.acme/*"}}}
	th := source.Throttle(src, time.Minute)
	d := source.Describe(th)
	if d.System != "demo" {
		t.Fatalf("system %q, want demo", d.System)
	}
	if len(d.Owns) != 1 || d.Owns[0].At != "demo.*.acme/*" {
		t.Fatalf("owns %v, want the wrapped source's claim", d.Owns)
	}
	// A source that declines to say still declines to say through the wrapper — a claim
	// invented here would be worse than none.
	plain := source.Describe(source.Throttle(&counted{}, time.Minute))
	if len(plain.Owns) != 0 {
		t.Fatalf("owns %v, want none", plain.Owns)
	}
}
