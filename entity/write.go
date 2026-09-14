package entity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
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

// String spells the path, and ParsePath is its inverse.
//
// It is one kinded path at every depth: `demo.pr."1"` for an entity, `demo.pr."1".labels.urgent`
// for a place inside its payload, with each segment quoted as kpath needs it. An entity and a
// field of one are the same address asked at different depths, so they get the same spelling.
//
// The quoting is what lets one spelling carry both, and an id holding a dot is why it must:
// `demo.pr."a.b"` is an entity whose id is `a.b`, and `demo.pr."a.b".labels` is that entity's
// `labels` — where an unquoted `demo.pr.a.b.labels` is five segments naming something else.
// A delimiter alone cannot say which; the quoting says it.
func (p Path) String() string {
	if len(p.Field) == 0 {
		return p.Ref.String()
	}
	return p.KPath()
}

// KPath spells the path as a kinded path at EVERY depth, the ref included:
// `demo.pr.1`, `demo.pr.1.labels.urgent`, `demo.pr."a.b"`. It is what a read route takes,
// which is why it exists beside String — String picks the spelling a reader expects for the
// depth, and this one picks the grammar the wire takes.
func (p Path) KPath() string {
	return JoinSegs(append(p.Ref.Segs(), p.Field...))
}

// KPath spells a REF as a kinded path — the grammar the wire speaks, at every depth, where
// String still picks the spelling by depth for a reader. `Path.KPath` is this plus the field
// path, and it is the same rendering.
func (r Ref) KPath() string { return JoinSegs(r.Segs()) }

// A STEP IS ONE PLACE, and which one is decided by its first character — three clauses, no
// splitting, no delimiter scan:
//
//	[0]  {0}  (scott)  [*]  {*}     an array, sparse-array or keyed access
//	"[0]"  'a.b'                    the NAME inside the quotes, literally
//	labels  a.b  x@git:ref:y        the name as given
//
// One word in, one path element out, whatever is inside it. The bracketed forms are
// SELF-DELIMITING — they open and close — so recognising one needs no guess about where the
// token ends, which is what tells this apart from reading a delimiter out of a word: that
// changed how MANY segments a word was, and this does not.
//
// The quoted clause is the escape, and it costs the spelling of a field whose name literally
// begins and ends with a quote. Array access is what people address; a field named `[0]` is
// not, and verse writes none.
func StepIsAccess(step string) bool {
	if step == "" {
		return false
	}
	switch step[0] {
	case '[', '{', '(':
		return true
	}
	return false
}

// StepName is the name a step carries — itself, or what is inside its quotes. It is not asked
// of an access step, which names no field.
func StepName(step string) string {
	if len(step) >= 2 && (step[0] == '"' || step[0] == '\'') && step[len(step)-1] == step[0] {
		return step[1 : len(step)-1]
	}
	return step
}

// KPathOf renders a vector of steps as the kinded path they name, applying the three clauses —
// which is what a READ sends, since the wire takes a kpath and the server parses it with kpath.
//
// An access step is appended with no dot before it (`votes[0]`, not `votes.[0]`), which is the
// kpath grammar's own shape; a name is appended as a quoted-where-needed field. An access may
// LEAD, which is what addresses an element of a payload that is itself an array.
func KPathOf(segs []string) (string, error) {
	var out string
	for _, step := range segs {
		if StepIsAccess(step) {
			out += step // no dot before an access: `votes[0]`, and `[0]` when it leads
			continue
		}
		if out != "" {
			out += "."
		}
		out += seg(StepName(step))
	}
	return out, nil
}

// TargetOf is the place a WRITE addresses, from the steps themselves: the entity, and either
// the object fields inside it or — when any step is an array access — the kpath fragment that
// reaches it (Write.Sub).
//
// AN ADDRESS IS AS DEEP AS IT IS. Where there are Depth segments or more, those name the entity
// and the rest is a path inside its payload; where there are fewer, all of them name the entity,
// because there is nothing left over to be a field OF anything. A short address is not an error:
// nothing in the document says where an entity ends (docs/working/entity-tree.md), so a write
// naming two segments is a write at two segments, and it is the same rule the selections keep —
// N segments identify entities at depth N.
//
// The split at Depth is a DEFAULT for the ambiguous half rather than a fact about the store. At
// four segments `demo pr 1 stage` reaches a field of an entity and `a b c d` could as easily be
// an entity, and the two produce the same document either way — entity-hood is a lens, and the
// write records which lens it used, not a boundary anything reads back.
//
// The entity's own segments may not be an access: an id is a name, and an entity is not an
// element of anything.
func TargetOf(segs []string) (Path, string, error) {
	want := Depth
	if len(segs) > 0 && StepName(segs[0]) == VerseRoot {
		want++
	}
	if len(segs) < want {
		want = len(segs)
	}
	names := make([]string, 0, want)
	for _, step := range segs[:want] {
		if StepIsAccess(step) {
			return Path{}, "", fmt.Errorf("%q: %q addresses an array element, and an entity is "+
				"named by its segments", strings.Join(segs, " "), step)
		}
		names = append(names, StepName(step))
	}
	// RefOfAny, not RefOf: the depth is the address's own, so the only thing left to refuse is
	// a nameless level.
	ref, ok := RefOfAny(names)
	if !ok {
		return Path{}, "", fmt.Errorf("%q does not name an entity: a segment is a name, and a "+
			"nameless level addresses nothing", strings.Join(segs, " "))
	}
	rest := segs[want:]
	access := false
	for _, step := range rest {
		if StepIsAccess(step) {
			access = true
		}
	}
	if !access {
		field := make([]string, 0, len(rest))
		for _, step := range rest {
			field = append(field, StepName(step))
		}
		return Path{Ref: ref, Field: field}, "", nil
	}
	sub, err := KPathOf(rest)
	if err != nil {
		return Path{}, "", fmt.Errorf("%q: %w", strings.Join(segs, " "), err)
	}
	return Path{Ref: ref}, sub, nil
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
	At Path
	// Sub is the place inside the entity when it is richer than a Path can spell: an ARRAY
	// ACCESS, and anything under it — `votes[0]`, `votes(scott).choice`. It is a kinded path
	// fragment below the entity, and it replaces At.Field when set.
	//
	// A FIELD ALONGSIDE, rather than Path growing a segment kind, because Path is what
	// ownership claims and the reflector are written in (source/own.go, valueAt) and "is
	// votes[0] disjoint from votes[1]" is a question those would then have to answer. Nothing
	// but a command line addresses an element: a charter patches a field, and so does every
	// source. The same shape api.PutEntity already uses, where Ref and Path sit side by side
	// because the ref spelling cannot carry a fourth segment.
	Sub   string
	Value Payload
	// Principal is who acts: the party this write is attributed to. A user by email, a runner
	// by name, or the address of the rule or source that acted — one string, because the
	// perms mirror is what tells them apart, by looking each up (see Attribution). It is
	// SELF-ASSERTED at the api, and the store trusts it no harder than `--as`: what it buys
	// is that every write names a party the store can later check against what its place
	// permits, and that the check reads the write itself rather than a side record only some
	// paths keep.
	//
	// Empty is unattributed. A store wrapped by As fills it, which is how a request's or a
	// firing's principal reaches every write made on its behalf — the compare-and-swap ones
	// through Modify included — without each call site carrying it.
	//
	// It rides the write to the log once the log can hold it (tony-format
	// cn1n32yph12ks5wrmhn0); until then it reaches the store's own check and the owner record
	// an entity gets at creation, and no further.
	Principal string
	// When are the preconditions, ALL of which must hold. Empty means unconditional.
	//
	// A precondition about a FIELD is built by nesting the pattern under the path's segments,
	// as an object keyed by field name (logdstore.nestPattern). One about an ELEMENT cannot
	// nest that way — an index is no key — so it is asked with `!at`, which walks the kpath
	// from the entity and applies the pattern where it lands (logdstore.elemPattern). A
	// require naming no place of its own is about the write's own place, element included.
	When []Require
}

// ErrNotDescendable is a write refused because the path it descends through holds a value that
// is not an object — an array or a scalar, which a descent would replace. It is the CALLER's
// error, not the store's: nothing failed, and nothing will change if it is retried.
var ErrNotDescendable = errors.New("the write would replace what it descends through")

// PatchLands reports whether every nested object in patch has somewhere to land in cur.
//
// A patch merges key by key where BOTH sides are objects and replaces where the target is not
// one — which is well-defined and right, and is also how `patch: {votes: {bo: yes}}` over
// `votes: [{by: ana}, {by: bo}]` writes an object holding the one field and loses both ballots,
// with the rule reporting success and nothing logged (git-issue mbjpdr6sh12ksa5zgsn0).
//
// ABSENT IS FINE, and that is where this differs from the guard on a write path. A put names a
// path in its steps and the steps assert it stands; a patch is a DOCUMENT describing the state
// an entity should reach, and a nested key it does not carry yet is that document's ordinary
// meaning — every derive that builds structure writes one. What is refused is landing on a
// VALUE: an array, a scalar, a null.
//
// It needs no precondition and costs no round trip. Modify already reads the current payload
// and hands it to its callback under a compare-and-swap, so the question is asked in process,
// against the very payload the patch is about to be applied to.
func PatchLands(cur, patch Payload) error { return patchLands(cur, patch, nil) }

func patchLands(cur, patch Payload, at []string) error {
	p := ir.Uncomment(patch)
	if p == nil || p.Type != ir.ObjectType {
		return nil
	}
	c := ir.Uncomment(cur)
	for i, f := range p.Fields {
		name := fieldName(f)
		v := ir.Uncomment(p.Values[i])
		// A SCALAR OR AN ARRAY REPLACES OUTRIGHT, which is what writing one says. `!delete null`
		// is the removal mark and lands here too, as it should: it is not a merge into anything.
		//
		// THE TAG IS NOT CONSULTED, and that is deliberate rather than an omission. A layout tag
		// is what the parser attaches to flow style, so `{votes: {bo: yes}}` carries one and a
		// tag test would skip every JSON and single-line charter — the same hazard as
		// git-issue srrwnc0kh12kr1r1e1n0. A match operator cannot be here at all: CheckPatchable
		// refuses one in a written value before the log can hold it.
		if v.Type != ir.ObjectType {
			continue
		}
		if c == nil || c.Type != ir.ObjectType {
			continue // nothing to land on and nothing to lose: the patch builds it
		}
		here := append(append([]string{}, at...), name)
		target := ir.Uncomment(ir.Get(c, name))
		if target == nil {
			continue // absent: the patch creates the key, which is what a patch is for
		}
		if target.Type != ir.ObjectType {
			return fmt.Errorf("the patch merges an object into %s, where %s stands: "+
				"nothing would merge and the value would be replaced. Write the whole value "+
				"at %s, or patch a field inside it",
				strings.Join(here, "."), standing(target), strings.Join(here, "."))
		}
		if err := patchLands(target, p.Values[i], here); err != nil {
			return err
		}
	}
	return nil
}

// Require is a precondition: whatever stands at At must match Pattern, evaluated against
// the store state as of the commit — so there is no window between the check and the write.
type Require struct {
	// At is the place asked about. The zero Path means the write's own place — the array
	// element it names, where it names one — which is what the common questions are about:
	// does this already exist, is it still what I read.
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

// deleteName is the merge operation a removal names, and deleteTag is how that operation is
// written. Both exist because the two are asked for in different places: a write SPELLS the
// tag, and the walk that reads a delta asks for the NAME (opIn), since a tag composes and the
// operation a patch executes is the first registered label in the chain rather than the whole
// string.
//
// It is what a removal wears on the way in AND on the way back: verse writes it (deleteOp) and
// logd forwards it on the change stream, which is how the delta walk tells a removed entity
// from one whose payload is null (walkEntityChanges).
const (
	deleteName = "delete"
	deleteTag  = "!" + deleteName
)

// Created requires that nothing stands at the write's own path — the create.
func Created() Require { return Require{Absent: true} }

// Updated requires that something already stands at the write's own path — the update.
func Updated() Require { return Require{Pattern: Present()} }

// Unchanged spells "what stands here is still exactly what I read" — the compare-and-swap half
// of a read-modify-write, over the payload the caller read.
//
// A null needs its own spelling, and that is the whole reason this is a function rather than a
// call to AsPattern at each site. AsPattern leaves a bare null alone, and a bare null PATTERN is
// Present(): it matches any value. So expecting a null payload to be unchanged would have asked
// only that something still be there, and a concurrent write of anything at all would have
// satisfied it — a lost update, silently, on exactly the entities the store had just started
// letting you write. `!irtype null` is the question actually meant: the value here has type
// null, and null has exactly one value, so that is the value.
func Unchanged(cur Payload) Payload {
	if cur != nil && cur.Type == ir.NullType && cur.Tag == "" {
		return ir.Null().WithTag(irTypeTag)
	}
	return AsPattern(cur)
}

// irTypeTag is tony's type-match operator. `!irtype null` asks whether what stands here has
// type null — which, null having one inhabitant, is whether it IS null.
const irTypeTag = "!irtype"

// atOwnPlace reports whether the require asks about the WRITE's own place rather than naming
// one of its own. That is the zero At, and it is the difference between a guard about an array
// ELEMENT and one about the entity that holds it: a write at `votes[0]` whose require names no
// place is asking about `votes[0]`, while one that spells the entity out is asking about the
// entity, and the two are answered by different shapes (logdstore.elemPattern).
func (r Require) atOwnPlace() bool {
	return r.At.System() == "" && r.At.Kind() == "" && r.At.ID() == "" && len(r.At.Field) == 0
}

// path resolves a Require's target: its own At, or the write's when it has none.
func (r Require) path(of Write) Path {
	if r.atOwnPlace() {
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

// JoinSegs renders a VECTOR OF STEPS as the kinded path they name, quoting each segment as
// kpath needs it.
//
// It is what lets an address be typed as separate words — `verse data demo pr 1` — and it is
// more than a convenience: a segment handed over whole never has to be escaped by the person
// typing it. `demo.pr."a.b"` and `git.ref."x@git:ref:y"` are what the quoting is FOR, and both
// are exactly the addresses that are hardest to type correctly in one token. As a vector they
// are `demo pr a.b` and `git ref x@git:ref:y`, and the shell's own quoting is the only quoting
// anybody does.
func JoinSegs(segs []string) string {
	var out string
	for _, s := range segs {
		if out != "" {
			out += "."
		}
		out += seg(s)
	}
	return out
}

// JoinSteps renders a vector of steps as the line a LISTING prints: the segments separated by
// SPACES, each one quoted only if a shell would otherwise mis-split or expand it.
//
//	demo pr 1
//	demo pr a.b                 a dot is ordinary in a step — this is the form's whole point
//	demo pr "a b.txt"           a space is the delimiter, so the segment carries quotes
//	git ref "*"                 an id that IS an asterisk, kept from globbing
//
// STEPS ARE THE ADDRESSING MODEL'S OWN SPELLING, on the way in and on the way out. `verse data
// demo pr a.b` needs no escaping from the person typing it, because the shell has already
// separated the words; a listing that answers the same way hands back exactly what it takes.
//
// THE QUOTING IS THE SHELL'S, not kpath's, and that is what makes this unambiguous where the
// bare form was not. A segment holding a space used to print as two words for one segment and
// read back as a different address — the objection that moved this listing to kinded paths
// (`demo.pr."a b.txt"`) for a while. Quoting the segment answers it in the grammar the line is
// actually read in: `xargs` and `read -r -a` both take `demo pr "a b.txt"` as three words, where
// the kpath form defeats xargs precisely because xargs strips quoting it does not know about.
//
// WHAT IT CANNOT SURVIVE is a shell that expands inside double quotes anyway — `!` under
// interactive history expansion is the one real case. A segment holding one is quoted here and
// still needs the reader's care; nothing in this grammar can fix that from the writing end.
func JoinSteps(segs []string) string {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		out = append(out, quoteStep(s))
	}
	return strings.Join(out, " ")
}

// quoteStep is one step as a shell word: bare when it already is one, and quoted when it is not
// — including when it is EMPTY, which would otherwise vanish from the line entirely.
//
// SINGLE QUOTES BY PREFERENCE, because they are literal: nothing inside one is expanded, globbed
// or substituted, so the segment survives whatever the reader's shell would otherwise do to it.
// Double quotes are the fallback for a segment that contains a single quote, and they are the
// weaker form — `xargs` reads `\"` as an unterminated quote rather than an escape, which is
// checked and is why they are not the default.
//
// A segment holding BOTH quote characters has no spelling here that xargs will take. It is
// rendered double-quoted, which a shell reads correctly and xargs does not; nothing about the
// listing can fix that, and an id containing both is not a shape any address in this system
// produces.
func quoteStep(s string) string {
	if s != "" && !strings.ContainsFunc(s, shellSpecial) {
		return s
	}
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// ShellWord is one argument as a word that a shell and `xargs` both read back as itself: bare
// when it already is one, and single-quoted otherwise, a single quote inside it spelled as the
// quote closed, the character backslash-escaped and the quote reopened, which both readers take.
// It is quoteStep with that one extension, for a line whose reader is `xargs -L1` as often as a
// shell: `verse flow --args` prints rows as the flags that name them.
func ShellWord(s string) string {
	if s != "" && !strings.ContainsFunc(s, shellSpecial) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellSpecial reports whether a rune stops a string from being a bare shell word. It is a
// DENY list of what the shell acts on rather than an allow list of what is safe, so a character
// nobody thought about prints as itself rather than being needlessly quoted — and the ones here
// are the ones that split, glob, redirect, substitute or comment.
func shellSpecial(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '"', '\'', '\\', '*', '?', '[', ']', '{', '}',
		'(', ')', '$', '`', '&', ';', '|', '<', '>', '#', '!', '~', '^':
		return true
	}
	return false
}

// ExprSegs is a kinded path as its raw segments, WITH `*` allowed as a whole segment — the
// reader for an address that SELECTS rather than names: a charter's subject
// (trigger.ParseExpr), a source's ownership claim (source.ParseClaim).
//
// It is separate from PathSegs because the two answer different questions, and the difference
// is exactly the wildcard: PathSegs is asked where something is read or written, and a `*`
// names no one place. Both live here so that "what are the segments of this string" has one
// answer, whatever is going to be done with them — every caller that grew its own splitter
// grew a different grammar, and each one drifted the day the spelling moved.
//
// An empty segment is refused rather than dropped, which kpath will not do for us: `demo..1`
// parses as the two fields `demo` and `1`, so a template that expanded to nothing would select
// something narrower instead of being refused.
func ExprSegs(s string) ([]string, error) {
	// A LEADING dot is the one thing kpath does not object to and this must: `.demo` parses as
	// `demo`, so an address with a stray dot in front would silently name a different place.
	// Everything else about dots belongs to the parser and reports better than a guard here can
	// — a trailing `foo.` is "expected field at end of string", and a leading or trailing `..`
	// is a recursive descent, refused below by its own name rather than as a typo.
	if strings.HasPrefix(s, ".") && !strings.HasPrefix(s, "..") {
		return nil, fmt.Errorf("address %q begins with an empty segment: a segment is a name, "+
			"and `*` is how a position is left open", s)
	}
	kp, err := kpath.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("address %q: %w", s, err)
	}
	var out []string
	for p := kp; p != nil; p = p.Next {
		switch {
		case p.Descend:
			// `..` IS RECURSIVE DESCENT, and it is refused here rather than approximated. kpath
			// carries it as its own thing and says why: it "names a set rather than a step", so
			// a path holding one is not a stored path. An address selects entities AT a depth
			// (entity.Store.ListAt), and a set spanning depths is a different question — the one
			// a reader of derived entities wants and cannot yet ask.
			//
			// Refused EXPLICITLY because the alternatives are both bad: flattened to a `*` it
			// silently selects one level where the author asked for every level, and left to the
			// lexical guard above it reads as an empty segment, which sends somebody looking for
			// a typo they did not make.
			return nil, fmt.Errorf("address %q: `..` is recursive descent, which selects across "+
				"DEPTHS — and an address selects at one. It is not supported here yet: name the "+
				"levels, or leave one open with `*`", s)
		case p.Field != nil:
			out = append(out, *p.Field)
		case p.FieldAll:
			out = append(out, "*")
		default:
			return nil, fmt.Errorf("address %q: %q is not a segment — an address names entities, "+
				"and an element of a payload is not one", s, p.String())
		}
	}
	return out, nil
}

// PathSegs is a kinded path as its raw segments — the names, unquoted, in order.
//
// It is the inverse of JoinSegs, and it goes through the kpath parser rather than splitting on
// dots: a segment may CONTAIN a dot (`demo.pr."a.b"`), so the only correct split is the one that
// knows the quoting. Wildcards and indexes are refused here for the same reason ParsePath
// refuses them — this answers "which names", and neither is one.
func PathSegs(at string) ([]string, error) {
	if at == "" {
		return nil, nil
	}
	// The same leading-dot guard ExprSegs carries, for the same reason and so that the two
	// readers of one grammar agree on what an address is: `.demo` parses as `demo`.
	if strings.HasPrefix(at, ".") && !strings.HasPrefix(at, "..") {
		return nil, fmt.Errorf("path %q begins with an empty segment: a segment is a name", at)
	}
	kp, err := kpath.Parse(at)
	if err != nil {
		return nil, err
	}
	var out []string
	for p := kp; p != nil; p = p.Next {
		if p.Descend {
			return nil, fmt.Errorf("path %q: `..` is recursive descent, which names a set of "+
				"places rather than one; this asks which place", at)
		}
		if p.Field == nil {
			return nil, fmt.Errorf("path %q: %q is not a name", at, p.String())
		}
		out = append(out, *p.Field)
	}
	return out, nil
}
