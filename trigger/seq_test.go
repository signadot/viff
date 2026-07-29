package trigger

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
)

// seqEngine records the ids it spawns, in order, and drives them to completion.
func seqEngine(t *testing.T, store entity.Store) (*Engine, func() []string) {
	t.Helper()
	eng := New(store, process.AutoResolver{})
	var mu sync.Mutex
	var ids []string
	eng.SetSpawner(func(id string, spec launch.Spec) process.Handle {
		mu.Lock()
		ids = append(ids, id)
		mu.Unlock()
		return startHookProc(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.ObserveScoped(ctx)
	return eng, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ids...)
	}
}

func seqFacts(t *testing.T, store entity.Store, ref entity.Ref, trig string) entity.Payload {
	t.Helper()
	e, ok, _ := store.Get(ref)
	if !ok {
		return nil
	}
	return entity.Get(entity.Get(entity.Get(e.Payload, VerseRoot), CharterKey), trig)
}

// The elements run IN ORDER, one process at a time, each id naming its position in the
// charter — which is the whole reason a chain is a kpath rather than a label.
func TestSeqRunsInOrder(t *testing.T) {
	store := entitytest.Store(t)
	eng, spawned := seqEngine(t, store)

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Seq: []Action{
				{Spawn: &launch.Spec{Task: "lint $subject"}},
				{Spawn: &launch.Spec{Task: "review $subject"}},
				{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
			},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "reviewed")

	got := spawned()
	want := []string{"review.seq.0@demo:pr:1", "review.seq.1@demo:pr:1"}
	if len(got) != len(want) {
		t.Fatalf("spawned %v; want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("spawn %d = %q; want %q (order is the point)", i, got[i], want[i])
		}
	}
}

// A set-only element completes immediately — it starts no process, so nothing will ever
// report its completion. Without the advance loop it would stall the sequence forever.
func TestSeqSetOnlyElementDoesNotStall(t *testing.T) {
	store := entitytest.Store(t)
	eng, _ := seqEngine(t, store)
	if err := eng.AddTrigger(Trigger{
		Name:      "marks",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{Seq: []Action{
			{Patch: entity.Obj{"a": 1}.Payload()},
			{Patch: entity.Obj{"b": 2}.Payload()},
			{Patch: entity.Obj{"stage": "through"}.Payload()},
		}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "through")
}

// The progress facts are the seam between the two axes: a parallel branch is woken by a
// `when`, and a `when` can only match facts. `done` is published rather than derived because
// a match cannot compare `step` against `steps` — it compares a value to a pattern.
func TestSeqPublishesProgress(t *testing.T) {
	store := entitytest.Store(t)
	eng, _ := seqEngine(t, store)
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{Seq: []Action{
			{Spawn: &launch.Spec{Task: "one"}},
			{Spawn: &launch.Spec{Task: "two"}},
		}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	waitFor(t, store, prRef("1"), func(p entity.Payload) bool {
		f := entity.Get(entity.Get(entity.Get(p, VerseRoot), CharterKey), "review")
		d := entity.Get(f, seqDone)
		return d != nil && d.Bool
	}, "verse.review.done latched")

	f := seqFacts(t, store, prRef("1"), "review")
	if got := entity.Get(f, seqSteps); got == nil || got.Int64 == nil || *got.Int64 != 2 {
		t.Errorf("steps = %v; want 2 (len(seq), known at install)", got)
	}
	if got := entity.Get(f, seqStep); got == nil || got.Int64 == nil || *got.Int64 != 2 {
		t.Errorf("step = %v; want 2", got)
	}
	if entity.Str(f, seqShape) == "" {
		t.Error("no shape hash published; a charter edit under a running sequence would go unnoticed")
	}
	// the author's own fields are untouched — the reserved root is a sibling, not a takeover
	if got := entity.Str(mustGet(t, store, prRef("1")), "stage"); got != "open" {
		t.Errorf("subject's own payload disturbed: stage = %q", got)
	}
}

// The carrier's onDone means "after the WHOLE sequence", which is what makes a sequence a unit
// with its own completion rather than a flat list of hooks.
func TestSeqCarrierOnDoneFiresAfterAll(t *testing.T) {
	store := entitytest.Store(t)
	eng, spawned := seqEngine(t, store)
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Seq: []Action{
				{Spawn: &launch.Spec{Task: "one"}},
				{Spawn: &launch.Spec{Task: "two"}},
			},
			OnDone: &Action{Patch: entity.Obj{"stage": "sequence-done"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "sequence-done")

	// and it fired once, after BOTH elements
	if got := spawned(); len(got) != 2 {
		t.Errorf("spawned %v; want exactly the two elements", got)
	}
}

// Fail-stop: an element that fails runs its own off-ramp and the sequence ends there. The
// element after it must never start — a sequence is an order, not a list of attempts.
func TestSeqFailStops(t *testing.T) {
	store := entitytest.Store(t)
	eng, spawned := seqEngine(t, store)
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Seq: []Action{
				{
					Spawn:    &launch.Spec{Task: "this will fail"},
					OnFailed: &Action{Patch: entity.Obj{"stage": "rejected"}.Payload()},
				},
				{Spawn: &launch.Spec{Task: "must not run"}},
			},
			OnDone: &Action{Patch: entity.Obj{"stage": "must-not-reach"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "rejected")

	for _, id := range spawned() {
		if id == "review.seq.1@demo:pr:1" {
			t.Error("the element after a failure ran; a sequence is fail-stop")
		}
	}
	if got := entity.Str(mustGet(t, store, prRef("1")), "stage"); got == "must-not-reach" {
		t.Error("the carrier's onDone fired though the sequence never completed")
	}
}

// Overlap is prevented by an atomic CLAIM, not by a predicate over facts. That distinction is
// what this pins: the claim is taken in the store, so two firings racing through
// `go e.fire(...)` cannot both win it — and it covers the window BETWEEN elements, where no
// process is live and a registry-based check had nothing to say.
func TestSeqClaimExcludesOverlap(t *testing.T) {
	store := entitytest.Store(t)
	eng, _ := seqEngine(t, store)
	ref := prRef("1")

	if !eng.claimRun("review", ref) {
		t.Fatal("first claim lost; nothing else holds it")
	}
	if eng.claimRun("review", ref) {
		t.Error("a second claim on a held run won — overlap is possible")
	}
	// a different subject is a different run
	if !eng.claimRun("review", prRef("2")) {
		t.Error("a claim on another subject was refused")
	}
	eng.releaseRun("review", ref)
	if !eng.claimRun("review", ref) {
		t.Error("the claim was not released when the run ended")
	}
}

// The claim lives in the `process` system, which the engine holds out of the default match
// set — so taking one cannot wake the trigger that took it. Machinery must not be reactive
// fuel; this is the same guard reflected processes already ride.
func TestSeqClaimIsNotReactiveFuel(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.Start(ctx) // root dispatch, so a bare `{}` match is installable at all
	// A `when` of {} matches every change it is ALLOWED to — it names no system, so the guard
	// is what decides. The subject is one named entity, since the question here is only whether
	// a claim write wakes anything at all.
	if err := eng.AddTrigger(Trigger{
		Name:      "watchall",
		Condition: Condition{Subject: mm("demo:pr:1"), When: MatchWhen(Match{})},
		Action:    Action{Patch: entity.Obj{"seen": true}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	eng.claimRun("review", prRef("1"))
	time.Sleep(150 * time.Millisecond) // give a dispatch, if one were coming, time to arrive
	// the claim entity exists…
	if _, ok, _ := store.Get(mirror.ClaimRef(spawnID("review", prRef("1")))); !ok {
		t.Fatal("claim not written")
	}
	// …and nothing reacted to it
	if f := eng.Fired(); f != 0 {
		t.Errorf("a trigger fired on a claim write (fired=%d); machinery ignited on its own machinery", f)
	}
}

// Repeating is a separate question from overlapping, and now a separate mechanism: `done`
// says a sequence at this shape already finished. Editing the sequence changes the shape and
// frees it.
func TestSeqDoneIsARepeatPolicy(t *testing.T) {
	store := entitytest.Store(t)
	eng, _ := seqEngine(t, store)
	ref := prRef("1")
	tr := Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{Seq: []Action{
			{Spawn: &launch.Spec{Task: "one"}},
			{Spawn: &launch.Spec{Task: "two"}},
		}, OnDone: &Action{Patch: entity.Obj{"stage": "done"}.Payload()}},
	}
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	shape := seqShapeHash(tr.Action.Seq)

	if eng.seqDone("review", ref, shape) {
		t.Error("an untouched subject reads as already done")
	}
	if err := eng.patchEntity(ref, entity.Obj{
		VerseRoot: entity.Obj{CharterKey: entity.Obj{"review": entity.Obj{seqStep: 2, seqDone: true, seqShape: shape}}},
	}.Payload()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !eng.seqDone("review", ref, shape) {
		t.Error("a completed sequence does not read as done")
	}
	if eng.seqDone("review", ref, "a-different-shape") {
		t.Error("a completed run still settles a DIFFERENT sequence; an edit should free it")
	}
}

// Two triggers sequencing the same subject keep their own progress: the reserved root is
// per-trigger, and a patch merges rather than replacing the key it touches. This is the case
// that made `patch` a prerequisite for `seq`.
func TestSeqProgressPerTriggerDoesNotClobber(t *testing.T) {
	store := entitytest.Store(t)
	eng, _ := seqEngine(t, store)
	for _, name := range []string{"alpha", "beta"} {
		if err := eng.AddTrigger(Trigger{
			Name:      name,
			Condition: Condition{Subject: prSubject("open")},
			Action:    Action{Seq: []Action{{Spawn: &launch.Spec{Task: "work"}}}},
		}); err != nil {
			t.Fatalf("AddTrigger %s: %v", name, err)
		}
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	waitFor(t, store, prRef("1"), func(p entity.Payload) bool {
		v := entity.Get(entity.Get(p, VerseRoot), CharterKey)
		for _, n := range []string{"alpha", "beta"} {
			d := entity.Get(entity.Get(v, n), seqDone)
			if d == nil || !d.Bool {
				return false
			}
		}
		return true
	}, "both triggers' sequences finished, both recorded")
}

// navigate is what makes a chain a lookup key into the charter rather than a label — the
// property both sequence-advance and (later) hook rehydration on resume rest on.
func TestNavigate(t *testing.T) {
	tr := Trigger{
		Name: "review",
		Action: Action{
			Seq: []Action{
				{Spawn: &launch.Spec{Task: "zero"}},
				{
					Spawn:    &launch.Spec{Task: "one"},
					OnFailed: &Action{Spawn: &launch.Spec{Task: "one-failed"}},
				},
			},
			OnDone: &Action{Spawn: &launch.Spec{Task: "after"}},
		},
	}
	for chain, want := range map[string]string{
		"review":                 "",
		"review.seq[0]":          "zero",
		"review.seq[1]":          "one",
		"review.seq[1].onFailed": "one-failed",
		"review.onDone":          "after",
	} {
		got, ok := navigate(tr, chain)
		if !ok {
			t.Errorf("navigate(%q) failed", chain)
			continue
		}
		if want == "" {
			continue // the root action itself has no spawn here
		}
		if got.Spawn == nil || got.Spawn.Task != want {
			t.Errorf("navigate(%q) = %+v; want the action spawning %q", chain, got.Spawn, want)
		}
	}
	for _, bad := range []string{
		"other.seq[0]",       // a different trigger
		"review.seq[9]",      // past the end — a charter that shrank under a live chain
		"review.seq",         // the list, not an element
		"review.onCancelled", // a hook that is not declared
		"review[0]",          // an index with no seq before it
	} {
		if _, ok := navigate(tr, bad); ok {
			t.Errorf("navigate(%q) resolved; want a miss", bad)
		}
	}
}

// The two chain renderings round-trip. The id form exists because brackets are shell-hostile,
// and it is lossless only because Action's field set is closed: a numeric segment is an index
// exactly where `seq` precedes it.
func TestChainRenderingsRoundTrip(t *testing.T) {
	for _, canonical := range []string{
		"review",
		"review.onDone",
		"review.seq[0]",
		"review.seq[12].onFailed",
		"review.onDone.onFailed",
	} {
		id := chainID(canonical)
		if back := chainKPath(id); back != canonical {
			t.Errorf("chainKPath(chainID(%q)) = %q", canonical, back)
		}
		if got := seqChainSegments(id); got {
			t.Logf("%s -> %s", canonical, id)
		}
	}
	// the id form is paste-safe, which is the reason it exists at all
	const globs = `*?[]{}()~!$&;'"<>|`
	for _, canonical := range []string{"review.seq[0]", "review.seq[12].onFailed"} {
		id := chainID(canonical)
		for _, r := range globs {
			if containsRune(id, r) {
				t.Errorf("id %q carries %q, which a shell reads as punctuation", id, r)
			}
		}
	}
}

// seqIndexOf reads an element chain back into (carrier, index) — the step completion needs to
// know which sequence it belonged to and how far along it was.
func TestSeqIndexOf(t *testing.T) {
	if c, i, ok := seqIndexOf("review.seq[2]"); !ok || c != "review" || i != 2 {
		t.Errorf(`seqIndexOf("review.seq[2]") = (%q, %d, %v)`, c, i, ok)
	}
	if c, i, ok := seqIndexOf("review.onDone.seq[0]"); !ok || c != "review.onDone" || i != 0 {
		t.Errorf(`nested carrier = (%q, %d, %v)`, c, i, ok)
	}
	for _, bad := range []string{"review", "review.onDone", "review.seq"} {
		if _, _, ok := seqIndexOf(bad); ok {
			t.Errorf("seqIndexOf(%q) claimed an element", bad)
		}
	}
}

// The install-time rules, each with the reason it exists.
func TestSeqValidation(t *testing.T) {
	eng := New(entitytest.Store(t), process.AutoResolver{})
	cond := Condition{Subject: Match{"system": "demo", "kind": "pr"}}
	cases := []struct {
		name string
		act  Action
	}{
		{"spawn and seq together", Action{
			Spawn: &launch.Spec{Task: "x"},
			Seq:   []Action{{Spawn: &launch.Spec{Task: "y"}}},
		}},
		{"onDone on a non-last element", Action{Seq: []Action{
			{Spawn: &launch.Spec{Task: "x"}, OnDone: &Action{Patch: entity.Obj{"a": 1}.Payload()}},
			{Spawn: &launch.Spec{Task: "y"}},
		}}},
		{"nested seq via an element", Action{Seq: []Action{
			{Seq: []Action{{Spawn: &launch.Spec{Task: "inner"}}}},
		}}},
		{"nested seq via an element's hook", Action{Seq: []Action{
			{
				Spawn:    &launch.Spec{Task: "x"},
				OnFailed: &Action{Seq: []Action{{Spawn: &launch.Spec{Task: "inner"}}}},
			},
		}}},
	}
	for i, c := range cases {
		err := eng.AddTrigger(Trigger{Name: "t" + string(rune('a'+i)), Condition: cond, Action: c.act})
		if err == nil {
			t.Errorf("%s: installed; want rejection", c.name)
		}
	}
	// and a hook on a seq-carrying action is fine — a sequence is something that completes
	if err := eng.AddTrigger(Trigger{
		Name:      "ok",
		Condition: cond,
		Action: Action{
			Seq:    []Action{{Spawn: &launch.Spec{Task: "x"}}},
			OnDone: &Action{Patch: entity.Obj{"done": true}.Payload()},
		},
	}); err != nil {
		t.Errorf("onDone with a seq and no spawn was rejected: %v", err)
	}
}

// The mirror publishes the CANONICAL chain, so a reader (and the flow tree) sees the kpath
// rather than the id's flattened rendering.
func TestSeqMirrorsCanonicalChain(t *testing.T) {
	store := entitytest.Store(t)
	eng, _ := seqEngine(t, store)
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action:    Action{Seq: []Action{{Spawn: &launch.Spec{Task: "work"}}}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	ref := mirror.Ref("review.seq.0@demo:pr:1")
	waitFor(t, store, ref, func(p entity.Payload) bool {
		return entity.Str(p, "chain") == "review.seq[0]"
	}, `process entity carries chain "review.seq[0]"`)
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func seqChainSegments(string) bool { return false }
