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
	//	Absent()               nothing is there — a create
	//	Present()              something is there — an update
	//	Obj{...}.Payload()     it looks like this
	//	!not {…}, !glob, …     anything else tony.Match can say
	Pattern Payload
}

// Present is the precondition pattern for "something is there, whatever it is". A null
// pattern is the presence test: it matches any value, so only a missing path fails it —
// the exact complement of Absent, which asks about the value's type instead.
func Present() Payload { return ir.Null() }

// Created requires that nothing stands at the write's own path — the create.
func Created() Require { return Require{Pattern: Absent()} }

// Updated requires that something already stands at the write's own path — the update.
func Updated() Require { return Require{Pattern: Present()} }

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
		when := Require{Pattern: Absent()} // expect-absent: this is a create
		next := fn(nil)
		if exists {
			when = Require{Pattern: AsData(cur.Payload)} // expect-unchanged: this is an update
			next = fn(cur.Payload)
			next = withRemovals(next, cur.Payload)
		}
		ok, _, err := s.Write(Write{At: ref.At(), Value: AsData(next), When: []Require{when}})
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
func ReplaceFrom(s Store, ref Ref, cur, next Payload) error {
	if _, _, err := s.Write(Write{At: ref.At(), Value: AsData(withRemovals(next, cur))}); err != nil {
		return fmt.Errorf("replace %s: %w", ref, err)
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
	when := Require{Pattern: Absent()} // expect-absent: this is a create
	next := fn(nil)
	if exists {
		when = Require{Pattern: AsData(cur)} // expect-unchanged: this is an update
		next = fn(cur)
		next = withRemovals(next, cur)
	}
	ok, _, err := s.Write(Write{At: ref.At(), Value: AsData(next), When: []Require{when}})
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

// withRemovals adds an explicit null for every field of old that next does not mention, so
// a read-modify-write that drops a field drops it in the store too. Only objects have
// fields to drop; any other value replaces outright.
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
			out.Values = append(out.Values, ir.Null())
		}
	}
	return out
}
