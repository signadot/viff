package entity

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/mergeop"
	"github.com/signadot/tony-format/go-tony/parse"
)

// doc is a tony document as a payload — how a test says what a value IS, rather than
// assembling it node by node. Operators and tags are the whole subject here, and every one of
// them is a tag.
func doc(t *testing.T, src string) Payload {
	t.Helper()
	n, err := parse.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return n
}

// same reports whether two payloads are the same data, comparing tags literally — the
// question every test here asks, and the one Equal cannot answer across a round trip that
// drops the parser's layout tags.
func same(a, b Payload) bool { return mergeop.RawEqual(a, b) }

// A value written under Raw is STORED rather than executed, operators and all. This is the
// whole point: a rule, a goal, any tony document which is itself written in the operator
// grammar, could not be written into the verse at all before the escape existed.
func TestRawStoresOperatorsAsData(t *testing.T) {
	store := openStore(t)
	ref := RefAt("trigger", "rule", "hotfix")
	rule := doc(t, `{id: !glob "hotfix-*", patch: {tmp: !delete null, stage: reviewed}}`)

	set(t, store, ref, Obj{"spec": Raw(rule)}.Payload())

	e, ok, err := store.Get(ref)
	if err != nil || !ok {
		t.Fatalf("the entity does not read back: ok=%v err=%v", ok, err)
	}
	if got := Get(e.Payload, "spec"); !same(got, rule) {
		t.Fatalf("the document came back changed:\n got: %v\nwant: %v", got, rule)
	}
}

// Raw REPLACES what stands at its path. A record that holds a document is rewritten whole
// when the document changes — a merge would leave the fields of the version before standing
// in the version after, which for a rule means a condition nobody wrote.
func TestRawReplacesRatherThanMerges(t *testing.T) {
	store := openStore(t)
	ref := RefAt("trigger", "rule", "hotfix")
	set(t, store, ref, Obj{"spec": Raw(doc(t, `{id: "a", gates: [approval]}`))}.Payload())

	next := doc(t, `{id: "a"}`)
	set(t, store, ref, Obj{"spec": Raw(next)}.Payload())

	e, _, _ := store.Get(ref)
	if got := Get(e.Payload, "spec"); !same(got, next) {
		t.Fatalf("spec = %v; the gate the new rule does not have survived the write", got)
	}
}

// A payload read out of the store goes back into it as DATA. Modify is a read-modify-write,
// so without the escape a record carrying a document executes its own operators the moment
// anything else about it changes — the probe that established this deleted the entity.
func TestModifyDoesNotExecuteWhatItStores(t *testing.T) {
	store := openStore(t)
	ref := RefAt("goal", "instance", "ship")
	spec := doc(t, `{doneWhen: !fact {facts: {system: ci}}, patch: {tmp: !delete null}}`)
	set(t, store, ref, Obj{"spec": Raw(spec), "status": "active"}.Payload())

	if err := Modify(t.Context(), store, ref, func(cur Payload) Payload {
		p := ToObj(cur)
		if p == nil {
			return cur // absent: nothing to modify (Modify offers fn the create first)
		}
		p["spec"] = Get(cur, "spec") // carried through as the node it is
		p["status"] = "blocked"
		return p.Payload()
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}

	e, ok, _ := store.Get(ref)
	if !ok {
		t.Fatal("the entity is gone: the modify applied the document it was storing")
	}
	if got := Get(e.Payload, "spec"); !same(got, spec) {
		t.Fatalf("spec after an unrelated modify = %v; want %v", got, spec)
	}
	if got := Str(e.Payload, "status"); got != "blocked" {
		t.Fatalf("status = %q; the modify itself did not land", got)
	}
}

// The expect-unchanged precondition is a MATCH against what stands in the store, so it has
// the same problem in the other direction: a stored `!delete` is not a patch operation to run
// against the document, it is what the document says. Two writers to one record still cannot
// lose each other.
func TestModifyRetriesOverAStoredOperator(t *testing.T) {
	store := openStore(t)
	ref := RefAt("goal", "instance", "ship")
	spec := doc(t, `{patch: {tmp: !delete null}}`)
	set(t, store, ref, Obj{"spec": Raw(spec), "n": 0}.Payload())

	for range 5 {
		if err := Modify(t.Context(), store, ref, func(cur Payload) Payload {
			p := ToObj(cur)
			if p == nil {
				return cur
			}
			p["spec"] = Get(cur, "spec")
			p["n"] = asInt(p["n"]) + 1
			return p.Payload()
		}); err != nil {
			t.Fatalf("modify: %v", err)
		}
	}
	e, _, _ := store.Get(ref)
	if got := asInt(ToObj(e.Payload)["n"]); got != 5 {
		t.Fatalf("n = %d, want 5: a precondition over stored operators does not hold", got)
	}
}

// BOTH kinds of null survive inside a stored spec, because both are data.
//
// The tagged one was always the point of `Raw`: `patch: {tmp: !delete null}` in a stored rule is
// an OPERATION, and a read that drops it takes it out of a charter that reads back otherwise
// intact.
//
// The bare one is the change. It used to be dropped — verse spelled "this field is gone" by
// writing a bare null and hid every bare null it read — which made a legal value unstorable at
// any depth, `!raw` included, since the escape is consumed by read time. The rule that paid for
// it is the absence latch: `value: !not {reviewed: null}` came back as `!not {}`, a perfectly
// good pattern meaning something else, and selected nothing from the first restart onward.
// Absence is `!delete` on the way in now, and a null is a null.
func TestBothKindsOfNullAreDataInAStoredSpec(t *testing.T) {
	store := openStore(t)
	ref := RefAt("trigger", "rule", "hotfix")
	set(t, store, ref, Obj{"spec": Raw(doc(t, `{tmp: !delete null, gone: null, stage: open}`))}.Payload())

	e, _, _ := store.Get(ref)
	spec := Get(e.Payload, "spec")
	if got := ir.Get(spec, "tmp"); got == nil {
		t.Fatalf("spec = %v; the !delete was read as a tombstone and dropped", spec)
	}
	if got := ir.Get(spec, "gone"); got == nil {
		t.Fatalf("spec = %v; a bare null in stored DATA did not survive — this is the shape the "+
			"absence latch is written in, and dropping it is what made a charter stop firing "+
			"after a restart", spec)
	}
	if got := ir.Get(spec, "stage"); got == nil || got.String != "open" {
		t.Fatalf("spec = %v; the ordinary field did not survive either", spec)
	}
}

// AsData escapes what a patch would interpret and nothing else. The nulls a whole-value
// replace writes to DROP a field must still be executed, or a read-modify-write stops being
// one: a field the caller removed would come back as a null standing in the record.
func TestAsDataLeavesRemovalsExecutable(t *testing.T) {
	store := openStore(t)
	ref := RefAt("demo", "pr", "1")
	set(t, store, ref, Obj{"stage": "open", "tmp": "x"}.Payload())

	if err := Modify(t.Context(), store, ref, func(cur Payload) Payload {
		return Obj{"stage": "reviewed"}.Payload() // tmp dropped
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	e, _, _ := store.Get(ref)
	if got := Get(e.Payload, "tmp"); got != nil {
		t.Fatalf("tmp = %v; a dropped field was stored as a null rather than removed", got)
	}
}

// AsData is idempotent over an explicit Raw. The two compose because a record built inside a
// Modify still needs the whole-document escape for its spec — Raw replaces, where AsData
// leaves everything around it merging.
func TestAsDataLeavesAnExplicitRawAlone(t *testing.T) {
	rule := doc(t, `{id: !glob "hotfix-*"}`)
	p := Obj{"spec": Raw(rule)}.Payload()
	if got, want := Get(AsData(p), "spec").Tag, Get(p, "spec").Tag; got != want {
		t.Fatalf("tag = %q, want %q: AsData escaped a value that was already escaped", got, want)
	}
}

// A composed tag is escaped whole, and recognized as escaped wherever the `!raw` sits in it: a
// patch dispatches on the first REGISTERED label rather than on the head, so `!bracket.glob` is
// a glob and `!bracket.raw` is already data. Reading only the head would escape the first
// twice and the second not at all.
func TestAsDataReadsAComposedTag(t *testing.T) {
	if got := AsData(ir.FromString("hotfix-*").WithTag("!bracket.glob")).Tag; got != "!raw.bracket.glob" {
		t.Fatalf("escaped tag = %q, want !raw.bracket.glob", got)
	}
	if got := AsData(ir.FromString("x").WithTag("!bracket.raw")).Tag; got != "!bracket.raw" {
		t.Fatalf("escaped tag = %q; an already-escaped value was escaped again", got)
	}
	// A layout tag on its own is not data, and does not put the value out of a merge's reach.
	if got := AsData(ir.FromKeyVals(nil).WithTag("!bracket")).Tag; got != "!bracket" {
		t.Fatalf("escaped tag = %q, want !bracket left alone", got)
	}
}

// A tag survives being carried through a read-modify-write. A merge COMPOSES the patch's tag
// onto the document's, so a record rewritten n times held `doneWhen: !fact.fact.fact…` — the
// reason the escape is about every tag rather than only the operations.
func TestTagIsNotComposedByRepeatedWrites(t *testing.T) {
	store := openStore(t)
	ref := RefAt("goal", "instance", "ship")
	spec := doc(t, `{doneWhen: !fact {facts: {system: ci}}}`)
	set(t, store, ref, Obj{"spec": Raw(spec), "n": 0}.Payload())

	for range 3 {
		if err := Modify(t.Context(), store, ref, func(cur Payload) Payload {
			p := ToObj(cur)
			if p == nil {
				return cur
			}
			p["spec"] = Get(cur, "spec")
			p["n"] = asInt(p["n"]) + 1
			return p.Payload()
		}); err != nil {
			t.Fatalf("modify: %v", err)
		}
	}
	e, _, _ := store.Get(ref)
	got := ir.Get(Get(e.Payload, "spec"), "doneWhen")
	if got == nil || got.Tag != "!fact" {
		t.Fatalf("doneWhen tag = %q, want !fact", got.Tag)
	}
}
