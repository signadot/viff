package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/process"
)

// patchEngine is a started engine over a store, for the write-path tests.
func patchEngine(t *testing.T, store *entity.LogdStore) *Engine {
	t.Helper()
	eng := New(store, process.AutoResolver{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.Start(ctx)
	return eng
}

// The reason `patch` had to replace `set`: two triggers writing under one key must not
// clobber each other. The old shallow overlay replaced whatever top-level key it touched, so
// the second writer of `verse` would have erased the first — which is exactly the shape
// docs/seq-plan.md's progress root has.
func TestPatchMergesSiblingSubtrees(t *testing.T) {
	store := entitytest.Store(t)
	eng := patchEngine(t, store)

	for _, tr := range []Trigger{{
		Name:      "first",
		Condition: Condition{When: prWhen("open")},
		Action:    Action{Patch: entity.Obj{"verse": entity.Obj{"a": entity.Obj{"step": 1}}}.Payload()},
	}, {
		Name:      "second",
		Condition: Condition{When: prWhen("open")},
		Action:    Action{Patch: entity.Obj{"verse": entity.Obj{"b": entity.Obj{"step": 2}}}.Payload()},
	}} {
		if err := eng.AddTrigger(tr); err != nil {
			t.Fatalf("AddTrigger %s: %v", tr.Name, err)
		}
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	// both branches survive, and so does the field neither of them wrote
	waitFor(t, store, prRef("1"), func(p entity.Payload) bool {
		v := entity.Get(p, "verse")
		return entity.Get(v, "a") != nil && entity.Get(v, "b") != nil
	}, "both verse.a and verse.b present")
	if got := entity.Str(mustGet(t, store, prRef("1")), "stage"); got != "open" {
		t.Errorf("untouched field lost: stage = %q, want open", got)
	}
}

// A patch can REMOVE a key. `set` could only ever overwrite one, so retracting a fact — a
// pending marker, a claim that no longer holds — had no spelling at all.
func TestPatchDeletes(t *testing.T) {
	store := entitytest.Store(t)
	eng := patchEngine(t, store)

	del, err := codec.ParseNode([]byte(`{tmp: !delete null}`), codec.Tony)
	if err != nil {
		t.Fatalf("parse patch: %v", err)
	}
	if err := eng.AddTrigger(Trigger{
		Name:      "retract",
		Condition: Condition{When: prWhen("open")},
		Action:    Action{Patch: del},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open", "tmp": "scratch"}.Payload())
	waitFor(t, store, prRef("1"), func(p entity.Payload) bool {
		return entity.Get(p, "tmp") == nil
	}, "tmp deleted")
	if got := entity.Str(mustGet(t, store, prRef("1")), "stage"); got != "open" {
		t.Errorf("delete took a sibling with it: stage = %q", got)
	}
}

// The common case is unchanged by the rename: a flat record patch behaves exactly as the
// overlay did, which is why no charter in the tree needed more than a new word.
func TestPatchFlatRecordIsUnchanged(t *testing.T) {
	store := entitytest.Store(t)
	eng := patchEngine(t, store)
	if err := eng.AddTrigger(Trigger{
		Name:      "advance",
		Condition: Condition{When: prWhen("open")},
		Action:    Action{Patch: entity.Obj{"stage": "reviewing"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open", "keep": "me"}.Payload())
	waitField(t, store, prRef("1"), "stage", "reviewing")
	if got := entity.Str(mustGet(t, store, prRef("1")), "keep"); got != "me" {
		t.Errorf("sibling field lost: keep = %q", got)
	}
}

// A named subject that does not exist yet is CREATED by its patch — how a trigger publishes a
// fact nothing has written. The absent payload is nil, and tony.Patch panics on a nil
// document, so this also pins the null-document guard in apply.
func TestPatchCreatesNamedSubject(t *testing.T) {
	store := entitytest.Store(t)
	eng := patchEngine(t, store)
	if err := eng.AddTrigger(Trigger{
		Name: "record",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "ci", "kind": "status"}),
			Subject: Match{"system": "audit", "kind": "record", "id": "latest"},
		},
		Action: Action{Patch: entity.Obj{"stage": "noted"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Obj{"state": "green"}.Payload())
	waitField(t, store, entity.Ref{System: "audit", Kind: "record", ID: "latest"}, "stage", "noted")
}

// A failing patch must fail the FIRING, not just the write. Spawning a process about a state
// the trigger failed to establish is worse than not firing: the process runs against a verse
// that never got the transition it was started for.
func TestFailedPatchDoesNotSpawn(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	spawned := make(chan string, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		spawned <- id
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// `!rename` needs a string argument; given an object it fails at apply time rather than
	// at install, which is precisely the case this guards.
	bad, err := codec.ParseNode([]byte(`{stage: !rename {}}`), codec.Tony)
	if err != nil {
		t.Fatalf("parse patch: %v", err)
	}
	if err := eng.AddTrigger(Trigger{
		Name:      "broken",
		Condition: Condition{When: prWhen("open")},
		Action: Action{
			Patch: bad,
			Spawn: &launch.Spec{Task: "must not run"},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	before := mustGet(t, store, prRef("1"))
	_ = before
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	select {
	case id := <-spawned:
		t.Fatalf("spawned %q despite a failed patch", id)
	case <-afterShort():
	}
	// and the subject is untouched — a failed write leaves the entity exactly as it was
	if got := entity.Str(mustGet(t, store, prRef("1")), "stage"); got != "open" {
		t.Errorf("failed patch still altered the subject: stage = %q, want open", got)
	}
}

// A charter carrying an operator means the same thing in both dialects. JSON has no tags, so
// it spells one as a single-key object (`{"tmp": {"delete": null}}`) — the same convention a
// formula and a match already use. The assertion is the EFFECT, not the parse: a patch that
// decodes but never deletes would pass a shape check and fail in the verse.
func TestPatchOperatorWorksInBothDialects(t *testing.T) {
	for _, c := range []struct {
		name, doc string
		f         codec.Format
	}{
		{"tony", `{name: retract, condition: {when: {system: demo, kind: pr, fields: {stage: open}}},
			action: {patch: {tmp: !delete null}}}`, codec.Tony},
		{"json", `{"name":"retract","condition":{"when":{"system":"demo","kind":"pr","fields":{"stage":"open"}}},
			"action":{"patch":{"tmp":{"delete":null}}}}`, codec.JSON},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := entitytest.Store(t)
			eng := patchEngine(t, store)
			var tr Trigger
			if err := codec.Unmarshal([]byte(c.doc), &tr, c.f); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if err := eng.AddTrigger(tr); err != nil {
				t.Fatalf("AddTrigger: %v", err)
			}
			// installed form carries the tag, whichever dialect wrote it
			got, ok := eng.Trigger("retract")
			if !ok {
				t.Fatal("trigger not installed")
			}
			out, err := codec.Marshal(got.Action.Patch, codec.Tony)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(out), "!delete") {
				t.Errorf("operator did not become a tag at install: %s", out)
			}
			// and it does the deleting
			entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open", "tmp": "scratch"}.Payload())
			waitFor(t, store, prRef("1"), func(p entity.Payload) bool {
				return entity.Get(p, "tmp") == nil
			}, "tmp deleted")
		})
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func mustGet(t *testing.T, store entity.Store, ref entity.Ref) entity.Payload {
	t.Helper()
	e, _, err := store.Get(ref)
	if err != nil {
		t.Fatalf("get %s: %v", ref, err)
	}
	return e.Payload
}

// waitFor polls until pred holds for the entity's payload, or fails naming what it wanted.
func waitFor(t *testing.T, store entity.Store, ref entity.Ref, pred func(entity.Payload) bool, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := store.Get(ref); ok && pred(e.Payload) {
			return
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(ref)
			t.Fatalf("%s never satisfied %q; payload = %+v", ref, want, e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// afterShort is the "nothing happened" window for a negative assertion.
func afterShort() <-chan time.Time { return time.After(300 * time.Millisecond) }

func containsAny(s, sub string) bool { return strings.Contains(s, sub) }
