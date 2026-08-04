package source_test

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/source"
)

// fakeSource answers with whatever the test hands it. It exists because most of what converge
// promises is not about git: a source that is not content-addressed, or that covers something
// it did not carry, has to be expressible without a repo behind it.
type fakeSource struct {
	system string
	answer func() source.Reflection
}

func (f *fakeSource) System() string { return f.system }

func (f *fakeSource) Reflect(context.Context, entity.Ref) (source.Reflection, error) {
	return f.answer(), nil
}

// cover is the pattern spelling the tests use: a system and kind, unnarrowed.
func cover(system, kind string) entity.Pattern {
	return entity.Pattern{System: system, Kind: kind}
}

func ref(system, kind, id string) entity.Ref {
	return entity.Ref{System: system, Kind: kind, ID: id}
}

// countingStore counts the writes converge ISSUES.
//
// It exists because the store underneath absorbs a patch that changes nothing — rewriting an
// identical payload moves no revision and emits no event — so asserting on events alone would
// pass whether or not converge skipped anything, and would be measuring logd's idempotence
// rather than verse's law. That absorption is also not something to build on: it is the
// substrate's behaviour, not this contract's, and the cost converge is avoiding (a read and a
// write per entity, per pass, over the whole slice) is paid either way.
//
// It counts READS for a second reason, and a sharper one: a pass Lists the whole slice once to
// work out what changed, so it already holds the payload of everything it is about to write.
// A per-entity read after that is a round trip for something in hand, and on a deployed docd
// it was half of what a first pass cost.
type countingStore struct {
	entity.Store
	writes int
	gets   int
}

func (c *countingStore) Write(w entity.Write) (bool, int64, error) {
	c.writes++
	return c.Store.Write(w)
}

func (c *countingStore) Get(ref entity.Ref) (entity.Entity, bool, error) {
	c.gets++
	return c.Store.Get(ref)
}

func newStore(t *testing.T) *countingStore {
	return &countingStore{Store: entitytest.Store(t)}
}

// quiet runs fn and reports whether it left the verse alone: no write issued, no revision
// moved, and nothing on the change stream. All three, because they are three different claims
// — what converge decided, what the store did with it, and what a trigger would have felt.
func quiet(t *testing.T, store *countingStore, fn func()) bool {
	t.Helper()
	writes, before := store.writes, store.Rev()
	ch, unsub := store.Observe()
	defer unsub()
	fn()
	if n := store.writes - writes; n != 0 {
		t.Logf("converge issued %d write(s) it had no reason to", n)
		return false
	}
	if store.Rev() != before {
		return false
	}
	// A commit that landed has a moment to arrive; nothing is expected here, so the wait only
	// costs a test that is going to pass anyway.
	select {
	case c := <-ch:
		t.Logf("unexpected commit at rev %d: %+v", c.Rev, c.Changes)
		return false
	case <-time.After(100 * time.Millisecond):
		return true
	}
}

// The law converge exists to keep: a second pass over unchanged truth writes nothing and emits
// nothing. Getting this wrong is not a wasted write — it re-fires every trigger over the slice,
// which is what the tree already measured once (twenty entities delivered forty events).
func TestReflectorQuietRepoIsSilent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	initRepo(t, dir)
	commit(t, dir, "a.txt", "hi", "first")
	gitRun(t, dir, "branch", "shiny")

	store := newStore(t)
	g := source.NewGit()
	g.AddRepo("A", dir)
	rf := source.NewReflector(g, store, 0)

	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if _, ok, _ := store.Get(ref(source.GitSystem, "ref", "A/shiny")); !ok {
		t.Fatal("first reconcile should have reflected A/shiny")
	}
	if !quiet(t, store, func() {
		if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
			t.Fatalf("second reconcile: %v", err)
		}
	}) {
		t.Fatal("a second reflect of a quiet repo wrote to the store")
	}
}

// Covers is the tombstone authority, and it BOUNDS it: an entity the reflection did not carry
// is removed only if a covered pattern claims it. Without this a source narrowed to one slice
// would delete every neighbouring one on its first pass.
func TestConvergeTombstonesOnlyWhatItCovers(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	entitytest.Set(t, store, ref("x", "a", "1"), entity.Obj{"n": 1}.Payload())
	entitytest.Set(t, store, ref("x", "b", "1"), entity.Obj{"n": 1}.Payload())

	src := &fakeSource{system: "x", answer: func() source.Reflection {
		return source.Reflection{Covers: []entity.Pattern{cover("x", "a")}}
	}}
	if err := source.NewReflector(src, store, 0).Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok, _ := store.Get(ref("x", "a", "1")); ok {
		t.Fatal("x:a:1 is covered and was not carried: it should have tombstoned")
	}
	if _, ok, _ := store.Get(ref("x", "b", "1")); !ok {
		t.Fatal("x:b:1 is covered by nothing: converge had no authority to remove it")
	}
}

// The other half of the same field, and it is how "keep history" is said. An entity a
// reflection carries but no cover claims is written and then left alone forever — which is
// what lets a git commit fall out of the bounded tip window and stay standing.
func TestConvergeKeepsWhatNoCoverClaims(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	carry := true
	src := &fakeSource{system: "x", answer: func() source.Reflection {
		rn := source.Reflection{Covers: []entity.Pattern{cover("x", "a")}}
		if carry {
			rn.Add(ref("x", "b", "1"), entity.Obj{"n": 1})
		}
		return rn
	}}
	rf := source.NewReflector(src, store, 0)
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if _, ok, _ := store.Get(ref("x", "b", "1")); !ok {
		t.Fatal("an entity under no cover should still be written")
	}
	carry = false
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if _, ok, _ := store.Get(ref("x", "b", "1")); !ok {
		t.Fatal("an entity under no cover must never be tombstoned, carried or not")
	}
}

// A source is the single writer of its slice, so its answer is what the entity IS: a field it
// stopped reporting is gone. A merge would leave the stale field standing — and since the
// unchanged-check compares against what the source SAID, the entity would then differ on every
// pass forever, firing its triggers each time. So the pass after the drop must be quiet.
func TestConvergeReplacesRatherThanMerges(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	payload := entity.Obj{"p": 1, "q": 2}
	src := &fakeSource{system: "x", answer: func() source.Reflection {
		rn := source.Reflection{Covers: []entity.Pattern{cover("x", "a")}}
		rn.Add(ref("x", "a", "1"), payload)
		return rn
	}}
	rf := source.NewReflector(src, store, 0)
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	payload = entity.Obj{"p": 1}
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	e, ok, _ := store.Get(ref("x", "a", "1"))
	if !ok {
		t.Fatal("x:a:1 vanished")
	}
	if got := entity.ToObj(e.Payload); len(got) != 1 || got["p"] == nil {
		t.Fatalf("a field the source stopped reporting should be gone, got %+v", got)
	}
	if !quiet(t, store, func() {
		if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
			t.Fatalf("third reconcile: %v", err)
		}
	}) {
		t.Fatal("the pass after a dropped field wrote again: the write and the comparison disagree")
	}
}

// A cover that lost its system does not claim nothing — an empty dimension in a Pattern is a
// WILDCARD, so it claims everything the pass looked at. Refusing it BEFORE anything is written
// is the whole point: this is the fault that costs entities rather than returning an error.
func TestConvergeRefusesACoverWithNoSystem(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	entitytest.Set(t, store, ref("x", "a", "1"), entity.Obj{"n": 1}.Payload())

	src := &fakeSource{system: "x", answer: func() source.Reflection {
		return source.Reflection{Covers: []entity.Pattern{{Kind: "a"}}}
	}}
	if err := source.NewReflector(src, store, 0).Reconcile(ctx, entity.Ref{}); err == nil {
		t.Fatal("a cover with no system should be refused")
	}
	if _, ok, _ := store.Get(ref("x", "a", "1")); !ok {
		t.Fatal("the refusal must land before anything is written")
	}
}

// The rest of Check, as messages rather than as behaviour: a source is the single writer of ONE
// slice, so neither a cover nor an entity may reach outside it, and a cover carries no op —
// converge compares against what stands in the store, which has none.
func TestReflectionCheckRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		rn   source.Reflection
	}{
		{"cover in another system", source.Reflection{
			Covers: []entity.Pattern{cover("y", "a")},
		}},
		{"cover carrying an op", source.Reflection{
			Covers: []entity.Pattern{{System: "x", Kind: "a", Op: entity.Set}},
		}},
		{"entity in another system", source.Reflection{
			Entities: []entity.Entity{{Ref: ref("y", "a", "1")}},
		}},
		{"entity with no id", source.Reflection{
			Entities: []entity.Entity{{Ref: ref("x", "a", "")}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.rn.Check("x"); err == nil {
				t.Fatal("should be refused")
			}
		})
	}
	ok := source.Reflection{Covers: []entity.Pattern{cover("x", "a")}}
	ok.Add(ref("x", "a", "1"), entity.Obj{"n": 1})
	if err := ok.Check("x"); err != nil {
		t.Fatalf("a well-formed reflection should pass: %v", err)
	}
}

// A PASS READS THE SLICE ONCE. converge Lists everything under the systems it touches to work
// out what changed, so it already holds the payload of every entity it is about to write —
// and asking the store for each one again is a second round trip for something in hand. On a
// deployed docd that was half of what a first pass cost: ~700 entities at ~0.8/second, a
// quarter of an hour to land a repository.
//
// Nothing here measures time. The invariant is the one that produced the time: converge makes
// NO per-entity reads, whether it is creating, updating, or leaving things alone.
func TestConvergeDoesNotReReadWhatItJustListed(t *testing.T) {
	ctx := context.Background()
	counting := newStore(t)

	var answer source.Reflection
	src := &fakeSource{system: "demo", answer: func() source.Reflection { return answer }}
	mine := entity.Obj{"repo": "r"}.Payload()
	build := func(stage string, n int) source.Reflection {
		rn := source.Reflection{Covers: []entity.Pattern{
			{System: "demo", Kind: "thing", Predicate: mine},
		}}
		for i := range n {
			rn.Add(entity.Ref{System: "demo", Kind: "thing", ID: fmt.Sprint(i)},
				entity.Obj{"repo": "r", "stage": stage})
		}
		return rn
	}
	rf := source.NewReflector(src, counting, 0)

	// A first pass: every entity is a create.
	answer = build("open", 20)
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	if got := counting.gets; got != 0 {
		t.Fatalf("a first pass made %d per-entity reads, want 0 — it had just Listed them", got)
	}

	// A second pass over the same answer: everything is unchanged, so nothing is written.
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	if got := counting.gets; got != 0 {
		t.Fatalf("an unchanged pass made %d per-entity reads, want 0", got)
	}

	// A third pass where every entity changed, plus a tombstone for one that is gone.
	answer = build("merged", 19)
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	if got := counting.gets; got != 0 {
		t.Fatalf("an updating pass made %d per-entity reads, want 0", got)
	}

	// And it is still correct: the updates landed and the missing one is gone.
	e, ok, _ := counting.Get(entity.Ref{System: "demo", Kind: "thing", ID: "0"})
	if !ok || entity.Str(e.Payload, "stage") != "merged" {
		t.Fatalf("thing:0 = %+v, ok=%v — the update did not land", entity.ToObj(e.Payload), ok)
	}
	if _, ok, _ := counting.Get(entity.Ref{System: "demo", Kind: "thing", ID: "19"}); ok {
		t.Fatal("thing:19 left the answer and was not tombstoned")
	}
}
