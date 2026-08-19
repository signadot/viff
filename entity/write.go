package entity

import (
	"context"
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

// String spells the path, and ParsePath is its inverse — which it was not before, and the
// reason is the delimiter.
//
// An ENTITY is spelled as its ref, `demo:pr:1`, because that is the ref's own spelling and
// nothing about identity changes here. A path that continues INTO a payload is spelled as a
// kinded path, `demo.pr.1.labels.urgent`, with each segment quoted as kpath needs it.
//
// The two cannot be one spelling until kpath takes a delimiter option, and the reason is an id
// with a dot in it: `demo:pr:a.b` is an entity whose id is `a.b`, and `demo:pr:a.b.labels` can
// be read as that entity's `labels` or as an entity named `a.b.labels`. The colon spelling has
// no way to say which. The dot spelling does — `demo.pr."a.b".labels` — so that is the one that
// carries a field path. This used to claim it emitted "the form the CLI accepts"; the CLI's
// ParseRef never implemented it, and this is what implementing it turned out to require.
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
	var out string
	for _, s := range append(p.Ref.Segs(), p.Field...) {
		if out != "" {
			out += "."
		}
		out += seg(s)
	}
	return out
}

// ParsePath parses the one address a command takes: an entity, or a place inside one.
//
//	demo:pr:1                    an entity, by ref — dots and all belong to the id
//	demo.pr.1                    the same entity, as a kinded path
//	demo.pr.1.labels.urgent      a place inside its payload
//	demo.pr."a.b".labels         …an id with a dot in it, which is why quoting exists
//
// WHICHEVER DELIMITER COMES FIRST is the one the address is written in, and that single rule
// is what keeps both spellings unambiguous:
//
//	demo:pr:a.b            `:` first — a ref, so the dots belong to the id
//	git.ref.x@git:ref:y    `.` first — a kinded path, so the colons belong to the id
//
// Trying a ref first instead does not work, and the second line is why: SplitN on ":" happily
// reads `git.ref.x@git:ref:y` as the system `git.ref.x@git`, which is not a thing anybody
// wrote. Nor does trying a kpath first, which reads `demo:pr:1.labels` as a system called
// `demo:pr:1`. Each spelling has to be recognized before it is parsed.
//
// So every address that ever worked still names what it named — a ref is a ref — and the dot
// spelling is what carries a field path.
//
// The field path is object fields only. `[0]` and `(scott)` address into an ARRAY, and they are
// refused here, where the message can say so.
//
// THE REASON HAS CHANGED, and the old one is worth knowing because it was load-bearing twice.
// It was "the substrate would reject them", which was simply wrong — logd patches an in-range
// index perfectly well. Then it was the real hazard: an index PAST THE END was ACCEPTED and the
// log was unreadable ever after, so a bad write did not fail, it succeeded and took every later
// read with it. That is fixed upstream (go-tony v0.0.149, tony-format 7cdvym1fh12ksmd5g5n0):
// the patch is now refused, naming the length, and nothing is damaged.
//
// What is left is ordinary work rather than a hazard. A write path is `Ref` + a list of object
// FIELD NAMES, and offering an element means teaching that list to carry a kpath segment —
// through `writePath`, through `segments()`, and through the preconditions built from it. So
// this stays refused until somebody does that, and entity/arraywrite_test.go pins the three
// answers the substrate gives so the reasoning is checked rather than inherited.
//
// Reading into an array is a different matter and is offered: it navigates a materialized
// document here rather than at the substrate, and `verse data` takes both spellings.
func ParsePath(s string) (Path, error) {
	s = fromSteps(s)
	if colonFirst(s) {
		ref, err := ParseRef(s)
		if err != nil {
			return Path{}, fmt.Errorf("path %q: %w", s, err)
		}
		return Path{Ref: ref}, nil
	}
	kp, err := kpath.Parse(s)
	if err != nil {
		return Path{}, fmt.Errorf("path %q: %w", s, err)
	}
	var segs []string
	for p := kp; p != nil; p = p.Next {
		switch {
		case p.Field != nil:
			segs = append(segs, *p.Field)
		case p.FieldAll, p.IndexAll, p.SparseIndexAll:
			return Path{}, fmt.Errorf("path %q: a wildcard names several places, and this names one", s)
		default:
			return Path{}, fmt.Errorf("path %q: a write does not address an array element — an "+
				"index that is out of range by the time it lands takes the log with it, and "+
				"nothing here can tell. Address the field that holds the array", s)
		}
	}
	want := Depth
	if len(segs) > 0 && segs[0] == VerseRoot {
		want++ // verse's own half is one level deeper: verse.task.instance.<id>
	}
	if len(segs) < want {
		return Path{}, fmt.Errorf("path %q: an address starts with system.kind.id (or the ref "+
			"system:kind:id), and this names %d of the %d", s, len(segs), want)
	}
	ref, ok := RefOf(segs[:want])
	if !ok {
		return Path{}, fmt.Errorf("path %q: its first %d segments do not name an entity", s, want)
	}
	return Path{Ref: ref, Field: segs[want:]}, nil
}

// fromSteps folds the THIRD spelling — an address as steps, `demo pr a.b` — into the kinded
// path the other two already parse to, by the same whichever-comes-first rule: a space before
// any `:` or `.` means the whole token is a vector of steps, and each step is handed to
// JoinSegs whole, so its own dots and colons are quoted rather than read as delimiters.
//
// It is here rather than only in the CLI's argv walk because that walk sees steps only when
// the shell split them, and the shell does that only when nobody quoted the address. A script
// that writes `verse data "$ref"` hands the whole address over as one token — the correct shell
// idiom — so the same string has to read the same way whether the shell split it or not. One
// rule, so the two forms are the same address.
//
// (`--paths` answers in kinded paths rather than steps, so what a listing hands back arrives
// here as a kpath. Steps remain what a command line makes easy on the way IN.)
//
// A step holding a space is not addressable THIS WAY — steps are space-separated, so the space
// is the delimiter — which is the same trade the printed form makes: `--paths` writes them
// space-separated, so a space inside one does not read back. The ref spelling still names it,
// and so does the quoted kinded path.
//
// That last clause is what this used to get wrong. `"a b".pr.1` is a kinded path whose SYSTEM
// holds a space, and the steps test found that space before the first `.` and split the address
// through the middle of a quoted segment. Only the first segment could reach it — anywhere later
// there is a `.` in front of the space — so `demo."p r".1` parsed and `"a b".pr.1` did not, which
// is drift rather than a trade. The delimiters belong to the kpath grammar, and so does the
// quoting: see firstDelim.
func fromSteps(s string) string {
	if !spaceFirst(s) {
		return s
	}
	return JoinSegs(strings.Fields(s))
}

// spaceFirst reports whether an address is written as steps: an UNQUOTED space before any
// unquoted `:` or `.`.
//
// QUOTED, because a segment may CONTAIN any of the three. `"a b".pr.1` is one kinded path whose
// system holds a space, and reading that space as the steps delimiter split it into `"a` and
// `b".pr.1` — an address that names two segments where three were written, refused with a
// message quoting the wreckage back. Only the FIRST segment could reach it: a space later in
// the path always has a `.` before it, which is what the old comparison happened to test.
//
// The delimiters and the quoting are the kpath grammar's, so the scan is the one the grammar
// implies rather than three IndexByte calls that agree with it most of the time.
func spaceFirst(s string) bool {
	kind, _ := firstDelim(s)
	return kind == ' '
}

// firstDelim answers the first delimiter that is not inside a quoted segment, and where it is.
// A kpath quotes a field that needs it (token.Quote), so a `.`, `:` or space between quotes is
// part of a name rather than a break between two.
func firstDelim(s string) (delim byte, at int) {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == '\\' {
				i++ // an escaped character cannot close the quote
				continue
			}
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == ' ' || c == ':' || c == '.':
			return c, i
		}
	}
	return 0, -1
}

// colonFirst reports whether an address is written in the ref spelling: a `:` before any `.`.
// An address with neither is neither, and reads as a kinded path so that the message about it
// names the three segments it is missing.
func colonFirst(s string) bool {
	kind, _ := firstDelim(s)
	return kind == ':'
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
	if r.At.System() == "" && r.At.Kind() == "" && r.At.ID() == "" && len(r.At.Field) == 0 {
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

// JoinSegs renders a VECTOR OF STEPS as the kinded path they name, joining the segments with a
// `/` and dropping any that are empty, quoting the rest as kpath needs it.
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
	kp, err := kpath.Parse(at)
	if err != nil {
		return nil, err
	}
	var out []string
	for p := kp; p != nil; p = p.Next {
		if p.Field == nil {
			return nil, fmt.Errorf("path %q: %q is not a name", at, p.String())
		}
		out = append(out, *p.Field)
	}
	return out, nil
}

// ReadPath renders a command-line address as the kinded path a READ takes, and the kpath it
// parsed to — a ref spelled with `:` normalized into the one grammar the read routes speak.
//
// IT APPLIES THE DELIMITER RULE rather than trying a ref first, and that is the whole reason it
// exists here instead of at the call site. `ParseRef` succeeds on `smokesd.feature.x@gh:issue:1`
// — three colon-delimited parts, the first holding dots — so a caller that tries it first reads
// a dotted path as a ref whose system is `smokesd.feature.x@gh`, and answers about an entity
// nobody named. Whichever delimiter comes FIRST is the one in use, which is what ParsePath
// already says and what this now says too.
func ReadPath(s string) (string, *kpath.KPath, error) {
	s = fromSteps(s)
	if colonFirst(s) {
		ref, err := ParseRef(s)
		if err != nil {
			return "", nil, err
		}
		at := ref.At().KPath()
		kp, err := ParseAt(at)
		return at, kp, err
	}
	kp, err := ParseAt(s)
	if err != nil {
		return "", nil, err
	}
	return s, kp, nil
}
