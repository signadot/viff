package entity

import (
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
)

// A write MERGES at its path: fields it does not mention are left alone, and a nil value
// removes what stands there. This is the rule the whole write path rests on — replace is
// what a caller builds out of it, not the other way round.
func TestWriteMerges(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	mustWrite(t, s, Write{At: Path{Ref: ref}, Value: Obj{"stage": "open", "env": "prod"}.Payload()})

	mustWrite(t, s, Write{At: Path{Ref: ref}, Value: Obj{"stage": "reviewed"}.Payload()})
	if got := readObj(t, s, ref); got["stage"] != "reviewed" || got["env"] != "prod" {
		t.Fatalf("a write did not merge: %+v, want stage=reviewed with env=prod untouched", got)
	}

	// nil at a field removes the field; nil at the entity removes the entity.
	mustWrite(t, s, Write{At: ref.At("env")})
	if got := readObj(t, s, ref); got["env"] != nil {
		t.Fatalf("nil at a field did not remove it: %+v", got)
	}
	mustWrite(t, s, Write{At: Path{Ref: ref}})
	if _, ok, _ := s.Get(ref); ok {
		t.Fatal("nil at an entity did not remove it")
	}
}

// A write addresses a place, not just an entity: it lands inside the payload, at any depth,
// without reading or rewriting the rest of it.
func TestWriteDeepField(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	mustWrite(t, s, Write{At: Path{Ref: ref}, Value: Obj{
		"stage":  "open",
		"labels": map[string]any{"urgent": false, "area": "store"},
	}.Payload()})

	mustWrite(t, s, Write{At: ref.At("labels", "urgent"), Value: ir.FromBool(true)})

	got := readObj(t, s, ref)
	labels, _ := got["labels"].(map[string]any)
	if labels["urgent"] != true {
		t.Fatalf("deep write did not land: %+v", got)
	}
	if labels["area"] != "store" || got["stage"] != "open" {
		t.Fatalf("deep write disturbed its neighbours: %+v", got)
	}
}

// Create and update are the same call with opposite preconditions, and the refused one
// writes nothing at all.
func TestWriteCreateAndUpdate(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")

	if ok, _, err := s.Write(Write{At: Path{Ref: ref}, Value: Obj{"stage": "open"}.Payload(), When: []Require{Updated()}}); err != nil || ok {
		t.Fatalf("update of an absent entity: committed=%v err=%v, want refused", ok, err)
	}
	if _, ok, _ := s.Get(ref); ok {
		t.Fatal("a refused update created the entity")
	}

	if ok, _, err := s.Write(Write{At: Path{Ref: ref}, Value: Obj{"stage": "open"}.Payload(), When: []Require{Created()}}); err != nil || !ok {
		t.Fatalf("create of an absent entity: committed=%v err=%v, want committed", ok, err)
	}
	if ok, _, err := s.Write(Write{At: Path{Ref: ref}, Value: Obj{"stage": "hijacked"}.Payload(), When: []Require{Created()}}); err != nil || ok {
		t.Fatalf("create of an existing entity: committed=%v err=%v, want refused", ok, err)
	}
	if got := readObj(t, s, ref); got["stage"] != "open" {
		t.Fatalf("a refused create wrote anyway: %+v", got)
	}
	if ok, _, err := s.Write(Write{At: Path{Ref: ref}, Value: Obj{"stage": "reviewed"}.Payload(), When: []Require{Updated()}}); err != nil || !ok {
		t.Fatalf("update of an existing entity: committed=%v err=%v, want committed", ok, err)
	}
	if got := readObj(t, s, ref); got["stage"] != "reviewed" {
		t.Fatalf("update did not land: %+v", got)
	}
}

// Create/update hold at FIELD depth too, which is what makes "add this label, but only if
// it isn't already set" one call rather than a read and a race.
func TestWriteConditionOnAField(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	mustWrite(t, s, Write{At: Path{Ref: ref}, Value: Obj{"stage": "open"}.Payload()})

	at := ref.At("owner")
	if ok, _, _ := s.Write(Write{At: at, Value: ir.FromString("alice"), When: []Require{Created()}}); !ok {
		t.Fatal("claiming an unset field lost")
	}
	if ok, _, _ := s.Write(Write{At: at, Value: ir.FromString("bob"), When: []Require{Created()}}); ok {
		t.Fatal("claiming a set field won — the field is not a claim")
	}
	if got := readObj(t, s, ref); got["owner"] != "alice" {
		t.Fatalf("owner = %v, want the first writer", got["owner"])
	}
}

// A Require with no path of its own asks about the write's own path — the common case, and
// the reason create/update need no address repeated.
func TestRequireDefaultsToTheWritesPath(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	gate := RefAt("demo", "gate", "1")
	mustWrite(t, s, Write{At: Path{Ref: gate}, Value: Obj{"open": true}.Payload()})

	// One condition about the write's own path (absent), one about somewhere else.
	ok, _, err := s.Write(Write{
		At:    Path{Ref: ref},
		Value: Obj{"stage": "open"}.Payload(),
		When: []Require{
			Created(),
			{At: Path{Ref: gate}, Pattern: Obj{"open": true}.Payload()},
		},
	})
	if err != nil || !ok {
		t.Fatalf("committed=%v err=%v, want committed", ok, err)
	}

	// The same write again: its own-path condition now fails, though the other still holds.
	ok, _, err = s.Write(Write{
		At:    Path{Ref: ref},
		Value: Obj{"stage": "again"}.Payload(),
		When: []Require{
			Created(),
			{At: Path{Ref: gate}, Pattern: Obj{"open": true}.Payload()},
		},
	})
	if err != nil || ok {
		t.Fatalf("committed=%v err=%v, want refused (the entity now exists)", ok, err)
	}
}

func mustWrite(t *testing.T, s Store, w Write) {
	t.Helper()
	ok, _, err := s.Write(w)
	if err != nil || !ok {
		t.Fatalf("write %s: committed=%v err=%v", w.At, ok, err)
	}
}

func readObj(t *testing.T, s Store, ref Ref) map[string]any {
	t.Helper()
	e, ok, err := s.Get(ref)
	if err != nil {
		t.Fatalf("get %s: %v", ref, err)
	}
	if !ok {
		return nil
	}
	return ToObj(e.Payload)
}

// An event carries the entity as of its commit, not the delta that produced it. A field
// written on its own must still be observed as the whole entity, or a consumer asking
// "does it look like this?" is answering about a fragment.
func TestEventIsLevelNotDelta(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	mustWrite(t, s, Write{At: Path{Ref: ref}, Value: Obj{"stage": "open", "env": "prod"}.Payload()})

	ch, unsub := s.Observe(ordinary()...)
	defer unsub()
	eventually(t, "the watch to be live", func() bool { return s.Rev() > 0 })

	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("reviewed")})

	select {
	case c := <-ch:
		got := ToObj(c.Changes[0].To)
		if got["stage"] != "reviewed" {
			t.Fatalf("event missed the change it reports: %+v", got)
		}
		if got["env"] != "prod" {
			t.Fatalf("event carried the delta, not the entity: %+v — a consumer reading `env` sees nothing", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event after a deep write")
	}
}

// The level state is AS OF THE COMMIT, so a transient state is still reported even once it
// has been overwritten — an event says what was true when it happened, not what is true now.
func TestEventLevelsAtItsOwnCommit(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	mustWrite(t, s, Write{At: Path{Ref: ref}, Value: Obj{"stage": "open"}.Payload()})
	first := s.Rev()

	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("reviewed")})
	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("deployed")})

	commits, err := s.Since(first, ordinary()...)
	evs := changes(commits)
	if err != nil || len(evs) != 2 {
		t.Fatalf("Since = %d events err=%v, want 2", len(evs), err)
	}
	if got := Str(evs[0].To, "stage"); got != "reviewed" {
		t.Errorf("first replayed event = %q, want the state at ITS commit (reviewed)", got)
	}
	if got := Str(evs[1].To, "stage"); got != "deployed" {
		t.Errorf("second replayed event = %q, want deployed", got)
	}
}

// A scalar payload is an entity like any other: Modify must read-modify-write it, not treat
// it as absent. The distinction the CAS makes is between something and nothing, not between
// an object and everything else — a store that conflates them demands an absence that never
// comes and retries to its deadline.
func TestModifyOnAScalarPayload(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "count", "1")
	mustWrite(t, s, Write{At: Path{Ref: ref}, Value: Val(100)})

	if err := Modify(t.Context(), s, ref, func(cur Payload) Payload {
		return Obj{"note": "milestone"}.Payload()
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	got, ok, err := s.Get(ref)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if Str(got.Payload, "note") != "milestone" {
		t.Fatalf("modify did not land on a scalar payload: %+v", got.Payload)
	}
}

// A replay levels too, from the very beginning of the log — where there is no state to read
// as a base and the deltas themselves are the whole history. A catch-up that delivered
// fragments here would be worse than useless: it is exactly the path a consumer falls back
// to when it has missed something.
func TestSinceLevelsFromTheStart(t *testing.T) {
	s := openStore(t)
	start := s.Rev() // fix the low-watermark before anything is written
	ref := RefAt("demo", "pr", "1")
	mustWrite(t, s, Write{At: Path{Ref: ref}, Value: Obj{"stage": "open", "env": "prod"}.Payload()})
	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("reviewed")})

	commits, err := s.Since(start, ordinary()...)
	evs := changes(commits)
	if err != nil || len(evs) != 2 {
		t.Fatalf("Since(%d) = %d events err=%v, want 2", start, len(evs), err)
	}
	last := ToObj(evs[len(evs)-1].To)
	if last["stage"] != "reviewed" || last["env"] != "prod" {
		t.Fatalf("replayed deep write = %+v, want the whole entity", last)
	}
}

// Modify is a read and a conditional write, so two writers to one ref cannot lose an
// update: the loser's precondition fails and it re-reads and re-applies.
func TestModifyNoLostUpdateViaWrite(t *testing.T) {
	concurrentIncrement(t, openStore(t))
}

// A read-modify-write that DROPS a field drops it in the store: fn is handed the whole
// payload and answers with the whole payload, so what it leaves out it means to leave out.
func TestModifyDropsFields(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	mustWrite(t, s, Write{At: ref.At(), Value: Obj{"stage": "open", "note": "x"}.Payload()})

	if err := Modify(t.Context(), s, ref, func(cur Payload) Payload {
		return Obj{"stage": Str(cur, "stage")}.Payload() // answers without `note`
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	got := readObj(t, s, ref)
	if got["stage"] != "open" {
		t.Fatalf("modify lost the field it kept: %+v", got)
	}
	if _, still := got["note"]; still {
		t.Fatalf("modify kept a field its answer left out: %+v", got)
	}
}

// Absence is an ANSWER, not a failure. A read of an entity whose id, kind, or system has
// never been written must say "nothing is there" — the distinction Get exists to preserve,
// and the one a caller acts on: told a thing does not exist, an agent goes and creates it;
// told the store is unreachable, it waits.
func TestAbsentReadsAreNotFailures(t *testing.T) {
	s := openStore(t)
	mustWrite(t, s, Write{At: RefAt("demo", "pr", "1").At(), Value: Obj{"a": "1"}.Payload()})

	// An entity whose payload is a SCALAR, so a read below it meets something that cannot
	// hold an entity. logd calls that path_conflict rather than not_found (go-tony v0.0.187),
	// and it is still nothing standing there — the distinction is about what is in the store,
	// not about whether the read succeeded. Both classifications read as absent; if this one
	// ever reads as a FAILURE, `verse data demo pr 1 sub` answers 502 and says the store is
	// unreachable.
	mustWrite(t, s, Write{At: RefAt("demo", "scalar", "1").At(), Value: Raw(mustParseNode(t, `5`))})

	for _, ref := range []Ref{
		RefAt("demo", "pr", "2"),               // the id has never been written
		RefAt("demo", "issue", "1"),            // nor the kind
		VerseRef("motion", "channel", "x"),     // nor the system
		RefAt("demo", "scalar", "1", "sub"),    // and here an ancestor is a number
		RefAt("demo", "scalar", "1", "s", "u"), // two below it, so nothing resolves at all
	} {
		e, ok, err := s.Get(ref)
		if err != nil {
			t.Errorf("Get(%s) reported a read failure for an absent entity: %v", ref, err)
		}
		if ok || e.Payload != nil {
			t.Errorf("Get(%s) = %+v ok=%v, want absent", ref, e.Payload, ok)
		}
	}
}

// set and remove are the arrange-a-fixture spellings for tests that are not about writing.
// (entitytest has the same pair for every other package; this package cannot import it.)
func set(t *testing.T, s Store, ref Ref, p Payload) {
	t.Helper()
	mustWrite(t, s, Write{At: ref.At(), Value: p})
}

func remove(t *testing.T, s Store, ref Ref) {
	t.Helper()
	mustWrite(t, s, Write{At: ref.At()})
}

// A tombstone is not a value. Removing a field leaves a null AT its key, at whatever depth
// it was removed, and a reader shown `labels: {urgent: null}` would report a label that is
// still there holding nothing — so the read strips them all the way down.
func TestRemovedFieldsAreInvisibleAtAnyDepth(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	mustWrite(t, s, Write{At: ref.At(), Value: Obj{
		"stage":  "open",
		"labels": map[string]any{"urgent": true, "area": "store"},
	}.Payload()})

	mustWrite(t, s, Write{At: ref.At("labels", "urgent")}) // remove the nested field

	got := readObj(t, s, ref)
	labels, _ := got["labels"].(map[string]any)
	if _, still := labels["urgent"]; still {
		t.Fatalf("a removed nested field is still visible: %+v", got)
	}
	if labels["area"] != "store" || got["stage"] != "open" {
		t.Fatalf("removing a nested field disturbed its neighbours: %+v", got)
	}
}

// A write whose content is UNCHANGED produces no commit at all: the revision does not advance
// and no observer hears anything, on either path.
//
// This is load-bearing for the trigger engine and worth pinning here rather than leaving as
// prose. Every trigger is re-evaluated at every commit touching what it reads, so what stops a
// rule running forever is that its own effect raises no further evaluation. A latching action
// writes a new value ONCE — the next firing selects nothing — and an action that re-writes
// what is already there is inert. Neither is a compare-and-swap result: entity.Modify's CAS
// retries on failure, so it prevents lost updates, not repeated work. It is the substrate
// declining to record a change that is not one.
func TestSameValueWriteRaisesNoCommit(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"stage": "open"}.Payload())

	root, unsubRoot := s.Observe(ordinary()...)
	defer unsubRoot()
	sub, unsubSub, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("WatchSubtree: %v", err)
	}
	defer unsubSub()
	<-sub // the level-triggered seed
	time.Sleep(200 * time.Millisecond)

	before := s.Rev()
	set(t, s, ref, Obj{"stage": "open"}.Payload()) // the same value, again
	if after := s.Rev(); after != before {
		t.Fatalf("revision advanced %d → %d for a write that changed nothing", before, after)
	}
	select {
	case c := <-root:
		t.Fatalf("the root watch reported a same-value write: rev=%d %+v", c.Rev, c.Changes)
	case <-time.After(500 * time.Millisecond):
	}
	select {
	case c := <-sub:
		t.Fatalf("the subtree watch reported a same-value write: rev=%d %+v", c.Rev, c.Changes)
	case <-time.After(500 * time.Millisecond):
	}

	// A real change still does, so this is suppression and not a broken watch.
	set(t, s, ref, Obj{"stage": "reviewed"}.Payload())
	if ev := oneChange(t, root, "a real change"); Str(ev.To, "stage") != "reviewed" {
		t.Fatalf("root watch missed a real change: %+v", ev)
	}
}

// ModifyFrom is Modify with the read already done, and it has to be the same write. A caller
// that has just Listed a slice holds every payload in it, and asking the store for one of them
// again is a round trip for something already in hand.
func TestModifyFromWritesWithoutReading(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")

	// Create: nothing stands there, and the caller says so.
	if err := ModifyFrom(t.Context(), s, ref, nil, false, func(cur Payload) Payload {
		if cur != nil {
			t.Errorf("fn was handed %v for a create, want nil", ToObj(cur))
		}
		return Obj{"stage": "open", "note": "x"}.Payload()
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := readObj(t, s, ref); got["stage"] != "open" {
		t.Fatalf("create did not land: %+v", got)
	}

	// Update from a correct snapshot, dropping a field — the whole-value contract Modify has.
	cur, _, err := s.Get(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := ModifyFrom(t.Context(), s, ref, cur.Payload, true, func(Payload) Payload {
		return Obj{"stage": "merged"}.Payload() // answers without `note`
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := readObj(t, s, ref)
	if got["stage"] != "merged" {
		t.Fatalf("update did not land: %+v", got)
	}
	if _, still := got["note"]; still {
		t.Fatalf("a field the answer left out survived: %+v", got)
	}
}

// THE SNAPSHOT IS A GUESS, and a wrong one must not cost an update. The precondition is
// Modify's own, so a stale `cur` fails to commit and falls back to Modify — which re-reads and
// re-applies fn against what is actually there.
func TestModifyFromFallsBackOnAStaleSnapshot(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"n": 1}.Payload())

	// A snapshot from before somebody else's write. fn increments, so a lost update would be
	// visible as a 2 where a 3 belongs — it must be re-applied against the CURRENT value.
	stale := Obj{"n": 1}.Payload()
	set(t, s, ref, Obj{"n": 2}.Payload())

	if err := ModifyFrom(t.Context(), s, ref, stale, true, func(cur Payload) Payload {
		return Obj{"n": asInt(ToObj(cur)["n"]) + 1}.Payload()
	}); err != nil {
		t.Fatalf("modify from a stale snapshot: %v", err)
	}
	if got := readObj(t, s, ref); got["n"] != int64(3) {
		t.Fatalf("n = %v (%T), want 3 — the stale snapshot's answer was committed instead of "+
			"being re-applied against what is there", got["n"], got["n"])
	}
}

// The other wrong guess: claiming something stands there when nothing does. The expect-unchanged
// precondition cannot hold against an absent entity, so this is the same fallback.
func TestModifyFromFallsBackWhenNothingIsThere(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")

	if err := ModifyFrom(t.Context(), s, ref, Obj{"stage": "ghost"}.Payload(), true,
		func(Payload) Payload { return Obj{"stage": "open"}.Payload() }); err != nil {
		t.Fatalf("modify from a snapshot of nothing: %v", err)
	}
	if got := readObj(t, s, ref); got["stage"] != "open" {
		t.Fatalf("the write did not land: %+v", got)
	}
}

// ONE ADDRESS: PathOf is Path.KPath's inverse, over the segments themselves.
//
// There is no string to parse, so there is no spelling to get wrong — the round trip is
// segments in, segments out, and every character is ordinary inside one.
func TestPathOfRoundTripsEverySegment(t *testing.T) {
	for _, segs := range [][]string{
		{"demo", "pr", "1"},
		{"demo", "pr", "1", "labels", "urgent"},
		{"git", "ref", "x@git.ref.y"},
		{"demo", "pr", "a.b"},
		{"demo", "pr", "a.b", "labels"},
		{"a.b.c", "pr", "1"},
		{"a b", "pr", "1"},
		{"verse", "task", "instance", "run-1"},
		{"demo"},
		{"demo", "pr"},
		{"verse", "task"},
		{"git", "ref"},
	} {
		got, err := TargetOf(segs)
		if err != nil {
			t.Errorf("TargetOf(%q): %v", segs, err)
			continue
		}
		back := got.Ref.Segs()
		if strings.Join(back, "\x00") != strings.Join(segs, "\x00") {
			t.Errorf("TargetOf(%q) → %q, which is not the same address", segs, back)
		}
		// …and the kpath it renders to parses back to the same segments, which is what the
		// wire depends on: the CLI joins once and the server reads with ParseAt.
		at, err := ParseAt(got.KPath())
		if err != nil {
			t.Errorf("%q renders %q, which does not parse: %v", segs, got.KPath(), err)
			continue
		}
		var read []string
		for kp := at; kp != nil; kp = kp.Next {
			if kp.Field == nil {
				t.Errorf("%q renders %q, whose segment %v is not a name", segs, got.KPath(), kp)
				break
			}
			read = append(read, *kp.Field)
		}
		if strings.Join(read, "\x00") != strings.Join(segs, "\x00") {
			t.Errorf("%q renders %q, which reads back as %q", segs, got.KPath(), read)
		}
	}
}

// A WRITE REACHES AN ARRAY ELEMENT, and the address that reaches it is a STEP: the names before
// it are the path, and the access and everything under it is the place inside the node there.
//
// AT ANY LENGTH, which is the whole of what the split cost: `merge proposal X votes [0]` and
// `merge proposal [0]` are the same grammar, and the second was refused because the names before
// an access had to reach an entity's own depth first (docs/working/paths-and-nodes.md, row 13).
func TestAWriteReachesAnArrayElement(t *testing.T) {
	for _, c := range []struct {
		segs   []string
		path   Ref
		access string
	}{
		{[]string{"merge", "proposal", "X", "votes", "[0]"}, RefAt("merge", "proposal", "X", "votes"), "[0]"},
		{[]string{"merge", "proposal", "X", "votes", "{0}"}, RefAt("merge", "proposal", "X", "votes"), "{0}"},
		{[]string{"merge", "proposal", "X", "votes", "(scott)"}, RefAt("merge", "proposal", "X", "votes"), "(scott)"},
		{[]string{"merge", "proposal", "X", "votes", "[*]"}, RefAt("merge", "proposal", "X", "votes"), "[*]"},
		{[]string{"merge", "proposal", "X", "votes", "[0]", "choice"}, RefAt("merge", "proposal", "X", "votes"), "[0].choice"},
		// An access may LEAD, which addresses an element of a node that is itself an array.
		{[]string{"merge", "proposal", "X", "[0]"}, RefAt("merge", "proposal", "X"), "[0]"},
		// …at an entity's own depth, and shorter still.
		{[]string{"merge", "proposal", "[0]"}, RefAt("merge", "proposal"), "[0]"},
		{[]string{"demo", "[0]"}, RefAt("demo"), "[0]"},
	} {
		at, err := TargetOf(c.segs)
		if err != nil {
			t.Errorf("TargetOf(%q): %v", c.segs, err)
			continue
		}
		if at.Ref != c.path || at.Access != c.access {
			t.Errorf("TargetOf(%q) = %s + %q, want %s + %q", c.segs, at.Ref, at.Access, c.path, c.access)
		}
	}
	// A NAME that merely needs quoting is still a name, not an access.
	if at, err := TargetOf([]string{"demo", "pr", "1", "a.b"}); err != nil || at.Access != "" {
		t.Errorf(`"a.b" became an access: %+v err=%v`, at, err)
	}
	// And the quoted clause is how an id that LOOKS like an access is named.
	at, err := TargetOf([]string{"merge", "proposal", `"[0]"`})
	if err != nil || at.Access != "" || at.Ref != RefAt("merge", "proposal", "[0]") {
		t.Errorf(`TargetOf(merge proposal "[0]") = (%+v, %v), want the name [0]`, at, err)
	}
}

// AN ADDRESS IS AS DEEP AS IT IS, and a write keeps every segment of it: there is no entity to
// split off, at any length.
//
// The short cases are what this exists for. `demo.pr` is a place in the tree like any other — a
// read answers at it, a watch reports it, a selection of two segments identifies nodes two deep —
// so a write there is a write there. Refusing it was the last of the three-segment arity,
// surviving in the one parse that turns argv into an address.
func TestTheAddressIsAsDeepAsItIs(t *testing.T) {
	for _, segs := range [][]string{
		{"demo"},
		{"demo", "pr"},
		{"demo", "pr", "1"},
		{"demo", "pr", "1", "stage"},
		{"demo", "pr", "1", "labels", "urgent"},
		// Verse's own half is one segment deeper, and the same rule reads it.
		{"verse", "task"},
		{"verse", "task", "instance", "run-1"},
		{"verse", "task", "instance", "run-1", "stage"},
	} {
		at, err := TargetOf(segs)
		if err != nil {
			t.Errorf("TargetOf(%q): %v", segs, err)
			continue
		}
		if at.Access != "" {
			t.Errorf("TargetOf(%q) access = %q, want none", segs, at.Access)
		}
		if want := RefAt(segs...); at.Ref != want {
			t.Errorf("TargetOf(%q) = %s, want %s", segs, at.Ref, want)
		}
	}
	// A nameless level addresses nothing, at every depth — which is all that is left to refuse.
	for _, bad := range [][]string{{}, {""}, {"demo", ""}, {"", "pr"}} {
		if got, err := TargetOf(bad); err == nil {
			t.Errorf("TargetOf(%q) = %+v, want a refusal", bad, got)
		}
	}
}
