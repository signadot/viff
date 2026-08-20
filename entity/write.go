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
// The entity's own segments may not be an access: an id is a name, and an entity is not an
// element of anything.
func TargetOf(segs []string) (Path, string, error) {
	want := Depth
	if len(segs) > 0 && StepName(segs[0]) == VerseRoot {
		want++
	}
	if len(segs) < want {
		return Path{}, "", fmt.Errorf("an address starts with system kind id, and %q names %d of "+
			"the %d", strings.Join(segs, " "), len(segs), want)
	}
	names := make([]string, 0, want)
	for _, step := range segs[:want] {
		if StepIsAccess(step) {
			return Path{}, "", fmt.Errorf("%q: %q addresses an array element, and an entity is "+
				"named by its segments", strings.Join(segs, " "), step)
		}
		names = append(names, StepName(step))
	}
	ref, ok := RefOf(names)
	if !ok {
		return Path{}, "", fmt.Errorf("%q: its first %d segments do not name an entity",
			strings.Join(segs, " "), want)
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
	// When are the preconditions, ALL of which must hold. Empty means unconditional.
	//
	// A precondition is built by NESTING the pattern under the path's segments, as an object
	// keyed by field name (logdstore.nestPattern), so it cannot express one at an array index —
	// there is no key to nest under. That is an implementation limit and not a principle: tony
	// matches an array position perfectly well, and what is missing is a constructor for an
	// array-shaped pattern beside the map walk. Until there is one, a conditional write at a
	// Sub is refused where it is asked for rather than silently checked against the wrong
	// place (git-issue zf1sz225h12ksm6agsn0).
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
