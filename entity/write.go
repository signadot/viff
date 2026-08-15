package entity

import (
	"context"
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
)

// Path addresses a place in the verse. A Ref names an entity; Field goes further, naming
// somewhere inside its payload. Nothing else in the address is new — a Path with an empty
// Field IS a ref.
//
// It exists because "which entity" and "which place in it" are the same question asked at
// different depths, and every operation that takes one should take the other: writing a
// field, requiring a field, and (later) reading or watching one.
type Path struct {
	Ref
	// Field is the path inside the payload — {"labels","urgent"} is the `urgent` key of the
	// `labels` object. Empty means the whole payload.
	Field []string
}

// At returns the path to a place inside this entity — with no arguments, the entity itself.
func (r Ref) At(field ...string) Path { return Path{Ref: r, Field: field} }

// String spells the path as its ref followed by the field path, e.g.
// demo:pr:1.labels.urgent — the form the CLI accepts.
func (p Path) String() string {
	if len(p.Field) == 0 {
		return p.Ref.String()
	}
	return p.Ref.String() + "." + strings.Join(p.Field, ".")
}

// IsEntity reports whether the path addresses a whole entity rather than a place inside one.
func (p Path) IsEntity() bool { return len(p.Field) == 0 }

// Write is one conditional write: Value is applied at At iff every Require holds, with the
// checks and the write committing as a single step. It is the only write the store has —
// creating, updating, deleting, patching a field, and claiming an address are all this
// operation with different arguments, because the substrate underneath is one conditional
// patch and everything else was verse simulating variants on top of it.
//
// The value MERGES at its path, and a nil Value removes whatever stands there. So a
// write says what should be true at one place; it does not say what should no longer be
// true elsewhere in the entity. A writer that means to drop a field says so, by writing
// nil at that field — which is one more line at the two call sites that need it, and is
// what lets every other write skip the read that whole-value replace requires.
type Write struct {
	At    Path
	Value Payload
	// When are the preconditions, ALL of which must hold. Empty means unconditional.
	When []Require
}

// Require is a precondition: whatever stands at At must match Pattern, evaluated against
// the store state as of the commit — so there is no window between the check and the write.
type Require struct {
	// At is the place asked about. The zero Path means the write's own path, which is what
	// the common questions are about: does this already exist, is it still what I read.
	At Path
	// Pattern is a tony match against whatever stands at At:
	//
	//	Present()              something is there — an update
	//	Unchanged(cur)         it is still what I read
	//	Obj{...}.Payload()     it looks like this
	//	!not {…}, !glob, …     anything else tony.Match can say
	//
	// Ignored when Absent is set: there is no value there to ask a question about.
	Pattern Payload
	// Absent requires that NOTHING stands at At — the create.
	//
	// IT IS A FIELD RATHER THAN A PATTERN, and the difference is the whole of what a pattern
	// can and cannot say. Absence is a property of the PATH: the document has no key here, so
	// there is nothing for a pattern placed at that path to be evaluated against. The store
	// renders it as a negated presence one level up instead (absentPath).
	//
	// It used to be a pattern — `Absent()`, i.e. `!irtype null` — which the store recognized by
	// its shape and routed away before anything could evaluate it. That worked exactly as long
	// as no entity could hold null, because `!irtype null` is a real tony operator and what it
	// genuinely asks is "the value here has type null". Once null became a legal payload the
	// two questions came apart, and the pattern spelling answered the wrong one: an entity
	// standing at a path holding null would have read as nothing standing there at all, so a
	// create would have committed straight over it.
	Absent bool
}

// Present is the precondition pattern for "something is there, whatever it is". A null
// pattern is the presence test: it matches any value, so only a missing path fails it —
// the exact complement of Absent, which asks about the value's type instead.
func Present() Payload { return ir.Null() }

// Removed is how a WRITE says a value is gone — at any depth, the entity's own path included:
// `rec["delivered"] = entity.Removed()` drops a field, and it AS an action's whole patch drops
// the entity (the reaper, docs/trigger-model.md).
//
// It is spelled rather than implied, and that is the whole lesson of the convention it
// replaces. Absence used to be a bare null — `rec[k] = nil` — which read beautifully and cost
// the data model a legal value at every depth, because a store that spells absence with a null
// must then hide every null it reads. A `nil` in an Obj could not be given this meaning either,
// however tempting: `{owners: {ENG: nil}}` in source/linear is a MATCH, where a bare null is
// the presence wildcard, so one spelling would have had to mean two opposite things depending
// on which side of a comparison it landed.
//
// The entity's own path was the last place a bare null still meant removal, and it is not an
// exception any more: one operator, any depth.
func Removed() Payload { return deleteOp() }

// deleteOp is how verse spells "this is gone" IN A WRITE: a null wearing `!delete`, which the
// store executes as a removal.
//
// The tag is the whole of it. A BARE null is a value — logd stores one and hands it back — so
// spelling absence with one meant hiding every bare null on read, and that made a legal value
// unstorable at every depth (see nodeToPayload). The "!" is load-bearing too: mergeop.SplitChild
// strips a tag's first byte before looking the operator up, so WithTag("delete") names "elete",
// which is no operator at all and leaves an ordinary null behind.
func deleteOp() Payload { return ir.Null().WithTag(deleteTag) }

// deleteTag is the operator tag a removal wears, on the way in AND on the way back: verse
// writes it (deleteOp) and logd forwards it on the change stream, which is how the delta walk
// tells a removed entity from one whose payload is null (walkEntityChanges).
const deleteTag = "!delete"

// Created requires that nothing stands at the write's own path — the create.
func Created() Require { return Require{Absent: true} }

// Updated requires that something already stands at the write's own path — the update.
func Updated() Require { return Require{Pattern: Present()} }

// Unchanged spells "what stands here is still exactly what I read" — the compare-and-swap half
// of a read-modify-write, over the payload the caller read.
//
// A null needs its own spelling, and that is the whole reason this is a function rather than a
// call to AsData at each site. AsData leaves a bare null alone, and a bare null PATTERN is
// Present(): it matches any value. So expecting a null payload to be unchanged would have asked
// only that something still be there, and a concurrent write of anything at all would have
// satisfied it — a lost update, silently, on exactly the entities the store had just started
// letting you write. `!irtype null` is the question actually meant: the value here has type
// null, and null has exactly one value, so that is the value.
func Unchanged(cur Payload) Payload {
	if cur != nil && cur.Type == ir.NullType && cur.Tag == "" {
		return ir.Null().WithTag(irTypeTag)
	}
	return AsData(cur)
}

// irTypeTag is tony's type-match operator. `!irtype null` asks whether what stands here has
// type null — which, null having one inhabitant, is whether it IS null.
const irTypeTag = "!irtype"

// path resolves a Require's target: its own At, or the write's when it has none.
func (r Require) path(of Write) Path {
	if r.At.System == "" && r.At.Kind == "" && r.At.ID == "" && len(r.At.Field) == 0 {
		return of.At
	}
	return r.At
}

// Modify atomically read-modify-writes an entity: fn receives the current payload (nil if
// absent) and returns the new one, written under a precondition that nothing changed in
// between — so two writers to one ref cannot lose an update. fn must be pure (it may run
// more than once) and must not mutate its argument.
//
// It is a function over Store, not a method on it, because it is exactly a read and a
// conditional write: the store owes callers the primitive, and compare-and-swap is what you
// build with it. Whole-value replace comes with it — fn is handed the whole payload and its
// answer is the whole payload — so a field fn drops is dropped, which is what a
// read-modify-write means.
//
// Everything here is DATA, on both sides: fn is handed what stands in the store and answers
// with what should stand there, so neither the value nor the expect-unchanged precondition
// may interpret a tag the entity happens to hold as data (a charter is full of them, and a
// record carrying one used to delete itself the moment anything else about it changed).
// AsData escapes those and nothing else — the explicit nulls withRemovals adds are how a
// dropped field is dropped, and they still have to execute.
//
// It gives up when ctx is done, returning that error, and does not swallow a store failure.
func Modify(ctx context.Context, s Store, ref Ref, fn func(cur Payload) Payload) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("modify %s: %w", ref, err)
		}
		cur, exists, err := s.Get(ref)
		if err != nil {
			return fmt.Errorf("modify %s: %w", ref, err)
		}
		when := Require{Absent: true} // expect-absent: this is a create
		next := fn(nil)
		if exists {
			when = Require{Pattern: Unchanged(cur.Payload)} // expect-unchanged: this is an update
			next = fn(cur.Payload)
		}
		// The removals go on AFTER the escape, and the order is load-bearing. AsData makes a
		// stored document inert — including a charter's own `patch: {tmp: !delete null}`, which
		// is data — while these markers are verse's and must still execute. Adding them first
		// and escaping afterwards would store the removal instead of performing it; exempting
		// `!delete` inside AsData would make a stored one executable, and it lands in the
		// PRECONDITION too, where a delete is not something a match can be evaluated with.
		value := AsData(next)
		if exists {
			value = withRemovals(value, cur.Payload)
		}
		ok, _, err := s.Write(Write{At: ref.At(), Value: value, When: []Require{when}})
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		// The precondition did not hold — someone else wrote. Re-read and re-apply.
	}
}

// ReplaceFrom writes next as the WHOLE value at ref, given cur — what the caller knows stands
// there — and with NO precondition.
//
// It is for the single writer, and it is the operation the ownership rule was supposed to buy:
// a slice with one writer has no concurrent writer to lose an update to, so the
// compare-and-swap every other write here carries is protecting against something that cannot
// happen. Reflected slices are exactly that case (docs/sources.md, "Ownership = single writer =
// no CAS"), and until this existed they paid for CAS anyway, because Modify was the only way to
// get replace semantics.
//
// WHAT IT COSTS, measured rather than assumed, against a local logd (see the repro in the
// go-tony issue): an unconditional patch runs at ~1500/second and does not slow as the document
// grows; a conditional one runs at 3–24/second. Two orders of magnitude, for a guarantee the
// caller does not need. That is the whole reason this is a separate function rather than a flag.
//
// cur is what makes it a REPLACE rather than a merge: a write merges at its path, so a field
// that has gone away goes away only if somebody writes an explicit null at it. A caller whose
// cur is stale leaves such a field standing — which is why this is for the writer that has just
// read the slice it is writing, and not a general-purpose call.
func ReplaceFrom(s Store, ref Ref, cur, next Payload) error { return ReplaceAt(s, ref.At(), cur, next) }

// ReplaceAt is ReplaceFrom at a place INSIDE an entity: cur and next are the values that stand
// and should stand AT that path, and everything outside it is untouched — not merged, not
// re-read, not mentioned.
//
// That last part is the whole of what it is for. A replace of a whole payload spells a null at
// every field the new value does not carry, which is correct when the writer owns the entity
// and destructive when it owns one slot of it: the nulls would land on the other owners. Scoped
// here, the removals are computed within the slot and cannot reach a sibling.
//
// It carries no precondition, for ReplaceFrom's reason one level down: two writers at disjoint
// paths of one entity are not two writers of anything, so there is no update to lose. What
// makes that true is the install-time proof that their claims are disjoint (source/own.go), and
// nothing here re-checks it.
func ReplaceAt(s Store, at Path, cur, next Payload) error {
	if _, _, err := s.Write(Write{At: at, Value: withRemovals(AsData(next), cur)}); err != nil {
		return fmt.Errorf("replace %s: %w", at, err)
	}
	return nil
}

// ModifyFrom is Modify with the read already done: cur is what the caller believes stands at
// ref, and exists says whether anything does.
//
// It exists for the caller that has just read a whole SLICE and is now writing part of it back
// — the source reflector's converge, which Lists everything under a source's system to work out
// what changed and then, entity by entity, asked Modify to Get each one again. That second read
// is half the round trips a pass makes, for payloads already in hand.
//
// IT IS NOT A WEAKER WRITE. The precondition is the one Modify uses — expect-absent for a
// create, expect-unchanged for an update — so a caller whose snapshot has gone stale does not
// lose an update; it gets a write that does not commit, and falls back to Modify, which
// re-reads and re-applies exactly as if it had been called in the first place. The cost of a
// wrong guess is one extra round trip on that entity; the saving on a right one is half of them
// on every entity.
//
// fn keeps Modify's contract: it is handed what stands there (nil when nothing does) and
// answers with what should, whole-value.
func ModifyFrom(ctx context.Context, s Store, ref Ref, cur Payload, exists bool, fn func(cur Payload) Payload) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("modify %s: %w", ref, err)
	}
	when := Require{Absent: true} // expect-absent: this is a create
	next := fn(nil)
	if exists {
		when = Require{Pattern: Unchanged(cur)} // expect-unchanged: this is an update
		next = fn(cur)
	}
	value := AsData(next)
	if exists {
		value = withRemovals(value, cur)
	}
	ok, _, err := s.Write(Write{At: ref.At(), Value: value, When: []Require{when}})
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	// The snapshot was stale — someone else wrote, or it never stood there at all. Modify is
	// the general case, and re-reading is exactly what it does.
	return Modify(ctx, s, ref, fn)
}

// withRemovals adds an explicit deleteOp() for every field of old that next does not mention, so
// a read-modify-write that drops a field drops it in the store too. The tag is what makes it a
// removal rather than a value — a bare null would be stored and handed back like any other
// field, so the marker has to be the tagged one deleteOp() returns, and it executes as a removal
// rather than surviving to the next read. Only objects have fields to drop; any other value
// replaces outright.
func withRemovals(next, old Payload) Payload {
	if next == nil || next.Type != ir.ObjectType || old == nil || old.Type != ir.ObjectType {
		return next
	}
	have := map[string]bool{}
	for _, f := range next.Fields {
		have[f.String] = true
	}
	out := clone(next)
	for _, f := range old.Fields {
		if !have[f.String] {
			out.Fields = append(out.Fields, ir.FromString(f.String))
			out.Values = append(out.Values, deleteOp())
		}
	}
	return out
}
