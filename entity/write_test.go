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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}

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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	gate := Ref{System: "demo", Kind: "gate", ID: "1"}
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	mustWrite(t, s, Write{At: Path{Ref: ref}, Value: Obj{"stage": "open", "env": "prod"}.Payload()})

	ch, unsub := s.Observe()
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	mustWrite(t, s, Write{At: Path{Ref: ref}, Value: Obj{"stage": "open"}.Payload()})
	first := s.Rev()

	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("reviewed")})
	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("deployed")})

	commits, ok := s.Since(first)
	evs := changes(commits)
	if !ok || len(evs) != 2 {
		t.Fatalf("Since = %d events ok=%v, want 2", len(evs), ok)
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
	ref := Ref{System: "demo", Kind: "count", ID: "1"}
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	mustWrite(t, s, Write{At: Path{Ref: ref}, Value: Obj{"stage": "open", "env": "prod"}.Payload()})
	mustWrite(t, s, Write{At: ref.At("stage"), Value: ir.FromString("reviewed")})

	commits, ok := s.Since(start)
	evs := changes(commits)
	if !ok || len(evs) != 2 {
		t.Fatalf("Since(%d) = %d events ok=%v, want 2", start, len(evs), ok)
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
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
	mustWrite(t, s, Write{At: Ref{System: "demo", Kind: "pr", ID: "1"}.At(), Value: Obj{"a": "1"}.Payload()})

	for _, ref := range []Ref{
		{System: "demo", Kind: "pr", ID: "2"},        // the id has never been written
		{System: "demo", Kind: "issue", ID: "1"},     // nor the kind
		{System: "motion", Kind: "channel", ID: "x"}, // nor the system
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	set(t, s, ref, Obj{"stage": "open"}.Payload())

	root, unsubRoot := s.Observe()
	defer unsubRoot()
	sub, unsubSub, err := s.WatchSubtree("demo", "pr")
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}

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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}

	if err := ModifyFrom(t.Context(), s, ref, Obj{"stage": "ghost"}.Payload(), true,
		func(Payload) Payload { return Obj{"stage": "open"}.Payload() }); err != nil {
		t.Fatalf("modify from a snapshot of nothing: %v", err)
	}
	if got := readObj(t, s, ref); got["stage"] != "open" {
		t.Fatalf("the write did not land: %+v", got)
	}
}

// ONE ADDRESS, and ParsePath is Path.String's inverse — which the two were not before.
//
// The colon spelling names an entity WHOLE: dots and all belong to the id, which is what makes
// it unambiguous and what keeps every address that ever worked meaning what it meant. The dot
// spelling carries a field path, quoting an id that needs it.
func TestParsePathIsStringInverted(t *testing.T) {
	for _, tc := range []struct {
		in    string
		want  Path
		spell string // what String gives back
	}{
		{"demo:pr:1", Ref{"demo", "pr", "1"}.At(), "demo:pr:1"},
		{"demo.pr.1", Ref{"demo", "pr", "1"}.At(), "demo:pr:1"},
		// A numeric id comes back QUOTED — `."1"` — which is the kpath printer being conservative
		// rather than a second meaning: `.1` parses as the field named 1 either way.
		{"demo.pr.1.labels.urgent", Ref{"demo", "pr", "1"}.At("labels", "urgent"), `demo.pr."1".labels.urgent`},
		// An id carrying colons, `@` and `+` needs no quoting in either spelling.
		{"git:ref:x@git:ref:y", Ref{"git", "ref", "x@git:ref:y"}.At(), "git:ref:x@git:ref:y"},
		{"git.ref.x@git:ref:y", Ref{"git", "ref", "x@git:ref:y"}.At(), "git:ref:x@git:ref:y"},
		// A DOTTED id: the colon spelling gives it to the id, the dot spelling needs quotes —
		// and that is the whole reason the two spellings are not one.
		{"demo:pr:a.b", Ref{"demo", "pr", "a.b"}.At(), "demo:pr:a.b"},
		{`demo.pr."a.b"`, Ref{"demo", "pr", "a.b"}.At(), "demo:pr:a.b"},
		{`demo.pr."a.b".labels`, Ref{"demo", "pr", "a.b"}.At("labels"), `demo.pr."a.b".labels`},
	} {
		got, err := ParsePath(tc.in)
		if err != nil {
			t.Errorf("ParsePath(%q): %v", tc.in, err)
			continue
		}
		if got.Ref != tc.want.Ref || strings.Join(got.Field, "\x00") != strings.Join(tc.want.Field, "\x00") {
			t.Errorf("ParsePath(%q) = %+v, want %+v", tc.in, got, tc.want)
			continue
		}
		if got.String() != tc.spell {
			t.Errorf("ParsePath(%q).String() = %q, want %q", tc.in, got.String(), tc.spell)
			continue
		}
		// …and round-trips: whatever String spells, ParsePath reads back the same place.
		again, err := ParsePath(got.String())
		if err != nil {
			t.Errorf("ParsePath(%q) — its own spelling — %v", got.String(), err)
			continue
		}
		if again.String() != got.String() {
			t.Errorf("%q does not round-trip: %q then %q", tc.in, got.String(), again.String())
		}
	}
}

// A write path is object fields. An array element is refused HERE, where the message can say
// why, rather than rendered into a kpath the store will reject: the store patches at a kpath of
// object fields, so there is nowhere for `(scott)` to land.
func TestParsePathRefusesWhatAWriteCannotReach(t *testing.T) {
	for _, bad := range []string{
		"merge.proposal.X.votes(scott)",
		"merge.proposal.X.votes[0]",
		"demo.pr.*",
		"demo.pr",
		"demo",
		"",
	} {
		if got, err := ParsePath(bad); err == nil {
			t.Errorf("ParsePath(%q) = %+v, want a refusal", bad, got)
		}
	}
}
