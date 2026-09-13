package source_test

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
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
	return entity.RefAt(system, kind, id)
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
	if _, ok, _ := store.Get(entity.RefAt(source.GitSystem, "A", "ref", "shiny")); !ok {
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
		t.Fatal("x.a.1 is covered and was not carried: it should have tombstoned")
	}
	if _, ok, _ := store.Get(ref("x", "b", "1")); !ok {
		t.Fatal("x.b.1 is covered by nothing: converge had no authority to remove it")
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
		t.Fatal("x.a.1 vanished")
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
		{"cover with an empty slot segment", source.Reflection{
			Covers: []entity.Pattern{{System: "x", Kind: "a", Slot: []string{"owners", ""}}},
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
	// And a slot cover is well-formed: converge honours it.
	slotted := source.Reflection{Covers: []entity.Pattern{
		{System: "x", Kind: "a", Slot: []string{"owners", "ENG"}}}}
	if err := slotted.Check("x"); err != nil {
		t.Fatalf("a slot cover should be accepted: %v", err)
	}
}

// A source whose covers say both "complete for this entity" and "complete for one slot of it"
// has contradicted itself, and BOTH readings destroy something: taking the whole spells nulls
// over the other owners, taking the slot leaves what the source meant to tombstone standing
// forever. So the pass fails, and nothing is written.
func TestCoveringAnEntityWholeAndBySlotIsRefused(t *testing.T) {
	store := entitytest.Store(t)
	shared := ref("x", "project", "p1")
	entitytest.Set(t, store, shared, entity.Obj{"owners": map[string]any{"ENG": map[string]any{}}}.Payload())

	rn := source.Reflection{Covers: []entity.Pattern{
		cover("x", "project"),
		{System: "x", Kind: "project", Slot: []string{"owners", "ENG"}},
	}}
	rn.Add(shared, entity.Obj{"owners": map[string]any{"ENG": map[string]any{"n": 1}}})

	rf := source.NewReflector(&fakeSource{system: "x", answer: func() source.Reflection { return rn }}, store, 0)
	err := rf.Reconcile(context.Background(), entity.Ref{})
	if err == nil {
		t.Fatal("a contradiction between covers should fail the pass")
	}
	for _, want := range []string{"whole", "slot"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not say %q: %v", want, err)
		}
	}
	// Nothing written: the entity still holds exactly what it did.
	got, ok, _ := store.Get(shared)
	if !ok || len(entity.Fields(entity.Get(got.Payload, "owners"))) != 1 {
		t.Fatalf("a refused pass wrote something: %v", got.Payload)
	}
}

// A slot survives the wire, which is what makes the refusal above a fact about converge rather
// than about the codec. It is one optional field on a cover — no cover written before it
// changes shape, and none of the three connectors sends one.
func TestASlotRidesTheCoverWire(t *testing.T) {
	in := entity.Pattern{System: "x", Kind: "a", Slot: []string{"owners", "ENG"}}
	n, err := codec.ToNode(source.Reflection{Covers: []entity.Pattern{in}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := codec.NodeBytes(n, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "slot") {
		t.Fatalf("a slot did not reach the wire: %s", b)
	}
	var back source.Reflection
	if err := codec.FromNode(n, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Covers) != 1 || !slices.Equal(back.Covers[0].Slot, in.Slot) {
		t.Fatalf("the slot did not come back: %+v", back.Covers)
	}
	// And a cover without one is unchanged, which is the whole claim to being additive.
	plain, err := codec.ToNode(source.Reflection{Covers: []entity.Pattern{cover("x", "a")}})
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := codec.NodeBytes(plain, codec.Tony)
	if strings.Contains(string(pb), "slot") {
		t.Fatalf("a slot-less cover grew a slot: %s", pb)
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
			rn.Add(entity.RefAt("demo", "thing", fmt.Sprint(i)),
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
	e, ok, _ := counting.Get(entity.RefAt("demo", "thing", "0"))
	if !ok || entity.Str(e.Payload, "stage") != "merged" {
		t.Fatalf("thing:0 = %+v, ok=%v — the update did not land", entity.ToObj(e.Payload), ok)
	}
	if _, ok, _ := counting.Get(entity.RefAt("demo", "thing", "19")); ok {
		t.Fatal("thing:19 left the answer and was not tombstoned")
	}
}

// TWO SOURCES, ONE ENTITY, AND A PASS COUNT THAT STOPS GROWING. This is the failure the whole
// slot design exists not to have, and it is asserted as a count rather than as an equality
// because the damage is unbounded rather than wrong-once: with a whole-payload replace each
// source spells a null over the other's slot, the other restores it, and every pass after that
// commits — forever, at the resync interval, with no error anywhere and both sources reporting
// success. Nothing in verse would catch it; the install-time disjointness proof is the only
// protection there is, and this is what makes that proof mean something.
//
// Wrote counts writes ISSUED, not commits produced, so asserting zero is stronger than counting
// commits: it says converge did not even ask.
func TestTwoSourcesShareAnEntityWithoutRewritingEachOther(t *testing.T) {
	store := entitytest.Store(t)
	shared := ref("x", "project", "p1")

	// Each source answers with a whole-entity-shaped payload carrying only its own slot, and a
	// cover that says which slot that is. Neither has heard of the other.
	owner := func(team, name string) *source.Reflector {
		src := &fakeSource{system: "x", answer: func() source.Reflection {
			rn := source.Reflection{Covers: []entity.Pattern{
				{System: "x", Kind: "project", Slot: []string{"owners", team}},
			}}
			rn.Add(shared, entity.Obj{"owners": map[string]any{team: map[string]any{"name": name}}})
			return rn
		}}
		return source.NewReflector(src, store, 0)
	}
	eng, ops := owner("ENG", "Migration"), owner("OPS", "Migration")

	wrote := func(rf *source.Reflector) int {
		n := 0
		rf.OnPass(func(p source.Pass) {
			if p.Err != nil {
				t.Fatalf("pass failed: %v", p.Err)
			}
			n = p.Wrote
		})
		if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if got := wrote(eng); got != 1 {
		t.Fatalf("ENG's first pass wrote %d, want 1", got)
	}
	if got := wrote(ops); got != 1 {
		t.Fatalf("OPS's first pass wrote %d, want 1", got)
	}
	// Both slots stand, and neither source deleted the other's.
	owners := entity.Fields(entity.Get(mustGet(t, store, shared), "owners"))
	if len(owners) != 2 || owners["ENG"] == nil || owners["OPS"] == nil {
		t.Fatalf("one source overwrote the other: %v", owners)
	}
	// And now the part that would run forever: four alternating passes, nothing to do in any.
	for i := 0; i < 2; i++ {
		if got := wrote(eng); got != 0 {
			t.Fatalf("ENG rewrote %d after OPS's pass — the comparison is not at the slot", got)
		}
		if got := wrote(ops); got != 0 {
			t.Fatalf("OPS rewrote %d after ENG's pass — the comparison is not at the slot", got)
		}
	}
}

// The last owner out takes the entity, and nobody else may. The delete is the one contended
// write in the design — every other one is at a path a single source owns — so it is
// conditional, and the condition has to tolerate the tombstone logd leaves where a departed
// sibling's key was.
func TestTheLastOwnerOutTakesTheEntity(t *testing.T) {
	store := entitytest.Store(t)
	shared := ref("x", "project", "p1")

	// A source that reflects its slot only while `has` says so; when it stops, its cover still
	// claims the slot, which is what makes the absence a tombstone rather than a silence.
	owner := func(team string, has *bool) *source.Reflector {
		src := &fakeSource{system: "x", answer: func() source.Reflection {
			rn := source.Reflection{Covers: []entity.Pattern{
				{System: "x", Kind: "project", Slot: []string{"owners", team}},
			}}
			if *has {
				rn.Add(shared, entity.Obj{"owners": map[string]any{team: map[string]any{"n": 1}}})
			}
			return rn
		}}
		return source.NewReflector(src, store, 0)
	}
	engHas, opsHas := true, true
	eng, ops := owner("ENG", &engHas), owner("OPS", &opsHas)
	pass := func(rf *source.Reflector) {
		if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
			t.Fatal(err)
		}
	}
	pass(eng)
	pass(ops)

	// ENG leaves. Its slot goes; the entity stays, because OPS is still in it.
	engHas = false
	pass(eng)
	owners := entity.Fields(entity.Get(mustGet(t, store, shared), "owners"))
	if owners["ENG"] != nil {
		t.Fatalf("ENG's slot survived its own tombstone: %v", owners)
	}
	if owners["OPS"] == nil {
		t.Fatalf("ENG's departure took OPS with it: %v", owners)
	}

	// OPS leaves too, and now there is nobody left to hold the entity up.
	opsHas = false
	pass(ops)
	if _, ok, err := store.Get(shared); err != nil || ok {
		t.Fatalf("the last owner left and the entity stands: ok=%v err=%v", ok, err)
	}
}

func mustGet(t *testing.T, s entity.Store, r entity.Ref) entity.Payload {
	t.Helper()
	e, ok, err := s.Get(r)
	if err != nil || !ok {
		t.Fatalf("get %s: ok=%v err=%v", r, ok, err)
	}
	return e.Payload
}

// A slot is REPLACED, not merged: a field the source stops reporting inside its own slot goes
// away, and a sibling slot is not touched to do it.
//
// This is the half of the whole-payload write that actually hurts, and it is not the half it
// looks like. A replace at the entity root spells its nulls at the TOP level only (withRemovals),
// and the top level of a shared entity is the container both owners write under — so a root
// write merges deeply and leaves the other owner's slot standing. What it does NOT do is remove
// anything inside the writer's own slot, so a field that has gone away stands forever, the
// entity differs from the answer on every pass, and every trigger over it fires each time.
func TestASlotIsReplacedNotMerged(t *testing.T) {
	store := entitytest.Store(t)
	shared := ref("x", "project", "p1")
	entitytest.Set(t, store, shared, entity.Obj{"owners": map[string]any{
		"OPS": map[string]any{"name": "theirs"},
	}}.Payload())

	fields := map[string]any{"name": "Migration", "lead": "ana"}
	src := &fakeSource{system: "x", answer: func() source.Reflection {
		rn := source.Reflection{Covers: []entity.Pattern{
			{System: "x", Kind: "project", Slot: []string{"owners", "ENG"}},
		}}
		rn.Add(shared, entity.Obj{"owners": map[string]any{"ENG": fields}})
		return rn
	}}
	rf := source.NewReflector(src, store, 0)
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}

	// The lead goes away upstream.
	fields = map[string]any{"name": "Migration"}
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	eng := entity.Fields(entity.Get(entity.Get(mustGet(t, store, shared), "owners"), "ENG"))
	if _, still := eng["lead"]; still {
		t.Fatalf("a field the source stopped reporting stands inside its slot: %v", eng)
	}
	if eng["name"] == nil {
		t.Fatalf("the replace took what the source is still reporting: %v", eng)
	}
	ops := entity.Fields(entity.Get(entity.Get(mustGet(t, store, shared), "owners"), "OPS"))
	if ops["name"] == nil {
		t.Fatalf("replacing ENG's slot reached into OPS's: %v", ops)
	}
	// And the pass that changes nothing writes nothing — the same entity, compared at the slot.
	n := -1
	rf.OnPass(func(p source.Pass) { n = p.Wrote })
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a settled slot wrote %d", n)
	}
}

// readingStore records the places a pass lists, which is the cost a pass carries that a count of
// writes cannot show.
type readingStore struct {
	entity.Store
	read []string
}

func (r *readingStore) ListAt(at []string, depth int, pred func(entity.Ref) bool) []entity.Entity {
	r.read = append(r.read, fmt.Sprintf("%s@%d", strings.Join(at, "."), depth))
	return r.Store.ListAt(at, depth, pred)
}

// A PASS READS WHERE ITS ANSWER IS, and nothing beside it. One repository's refs and head are
// the places its covers name, and its commits — which no cover holds — are read where they
// stand; another repository's history under the same system is never read. It is also where
// the mixed depths meet: a head three segments deep beside refs and commits four deep, each
// read at its own depth, and a second pass over the same answer writes nothing.
func TestAPassReadsThePlacesItsAnswerIsAbout(t *testing.T) {
	store := &readingStore{Store: entitytest.Store(t)}
	for i := 0; i < 3; i++ {
		entitytest.Set(t, store.Store, entity.RefAt("x", "B", "commit", fmt.Sprint(i)), entity.Obj{"sha": i}.Payload())
	}
	refs := []string{"main", "dev"}
	src := &fakeSource{system: "x", answer: func() source.Reflection {
		rn := source.Reflection{Covers: []entity.Pattern{
			source.Cover("x", "A", "ref", "*"),
			source.Cover("x", "A", "head"),
		}}
		for _, name := range refs {
			rn.Add(entity.RefAt("x", "A", "ref", name), entity.Obj{"sha": name})
		}
		rn.Add(entity.RefAt("x", "A", "head"), entity.Obj{"ref": "main"})
		rn.Add(entity.RefAt("x", "A", "commit", "c1"), entity.Obj{"sha": "c1"})
		return rn
	}}
	rf := source.NewReflector(src, store, 0)
	wrote := -1
	rf.OnPass(func(p source.Pass) { wrote = p.Wrote })
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(store.read)
	if want := []string{"x.A.commit@4", "x.A.head@3", "x.A.ref@4"}; !slices.Equal(store.read, want) {
		t.Fatalf("a pass read %v, want %v", store.read, want)
	}

	// Unchanged, it writes nothing: each place was read at its own depth, so every entity was
	// found where it was written.
	store.read = nil
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	if wrote != 0 {
		t.Fatalf("a second pass over the same answer wrote %d", wrote)
	}

	// A ref that goes is tombstoned; the commit, which no cover holds, stays.
	refs = []string{"main"}
	src.answer = func() source.Reflection {
		rn := source.Reflection{Covers: []entity.Pattern{
			source.Cover("x", "A", "ref", "*"),
			source.Cover("x", "A", "head"),
		}}
		rn.Add(entity.RefAt("x", "A", "ref", "main"), entity.Obj{"sha": "main"})
		rn.Add(entity.RefAt("x", "A", "head"), entity.Obj{"ref": "main"})
		return rn
	}
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get(entity.RefAt("x", "A", "ref", "dev")); ok {
		t.Fatal("a ref the answer stopped carrying stands under a cover")
	}
	for _, still := range []entity.Ref{entity.RefAt("x", "A", "commit", "c1"), entity.RefAt("x", "B", "commit", "0")} {
		if _, ok, _ := store.Get(still); !ok {
			t.Fatalf("%s is under no cover of this answer and was tombstoned", still)
		}
	}
}

// A path cover names its system in its first segment, literally. A `*` there is the wildcard
// an empty system is, and is refused for the same reason: it claims every system it is compared
// against.
func TestAPathCoverNamesItsSystem(t *testing.T) {
	wild := source.Cover("*", "A", "ref", "*")
	for _, c := range []struct {
		cover entity.Pattern
		want  string
	}{
		{wild, "literal system"},
		{source.Cover("y", "A", "ref", "*"), `this source owns "x"`},
	} {
		err := source.Reflection{Covers: []entity.Pattern{c.cover}}.Check("x")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("Check(%v) = %v, want a refusal naming %q", c.cover.Path, err, c.want)
		}
	}
	if err := (source.Reflection{Covers: []entity.Pattern{source.Cover("x", "A", "ref", "*")}}).Check("x"); err != nil {
		t.Fatalf("a cover at this source's own place was refused: %v", err)
	}
}
