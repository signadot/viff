// Package entity is the entity-verse: the substrate of things an SDLC refers to
// or generates, each named by a <system, kind, id> triple. It is an *index, not a
// warehouse* — an entity holds high-level metadata (a small payload), not the full
// contents of the underlying system; anything needing depth follows the reference
// into the real system on its own.
//
// The store is observable: every change is an Event on a stream. That stream is
// what triggers (elsewhere) match on, and where task state is reflected — so
// "observe another task" is just "observe an entity."
package entity

//go:generate go tool tony-codegen -dir .

import (
	"fmt"
	"strings"
)

// Ref is a concrete, org-addressable entity: <system, kind, id>, e.g.
// "linear:issue:1090". A generic reference ("linear:issue:$id") lives a level up,
// in trigger templates; here a Ref always names one concrete thing.
//
// ITS SEGMENTS ARE UNEXPORTED, and that is the whole point of this step. A ref is an ADDRESS,
// and an address is a sequence of segments that happens to be three long today; every caller
// that reached into the struct was writing that arity down again. They go through RefAt and
// the three readers below, so the day the representation becomes a path this file changes and
// they do not.
//
// It stays a comparable struct rather than becoming a slice: a Ref is a map key and an
// equality test in a hundred places (`ev.Ref == subj`, `map[Ref]Payload`), and a slice is
// neither.
//
// THE ROOT is which half of the verse the address is in: empty for everything the world is
// about — a PR, a build, a person, a doer somebody registered — and `verse` for what verse
// writes about its own running. It is a segment like the others (it renders, it is walked, it
// is a level in the document) and it is not one of the three: a task is `verse:task:instance:x`,
// so `system` is still `task` and the code asking "is this a task" is asking about the same
// segment it always was.
type Ref struct {
	root string // "" = the world's, VerseRoot = verse's own
	// key is the segments BELOW the root, joined by sep — a STRING because a Ref is a map key
	// and an equality test in a hundred places, and a slice is neither comparable nor a key.
	// sep is a byte no address can hold: every segment comes from a path, a ref or a document
	// key, all of which are text somebody typed.
	key string
}

// sep joins a ref's segments internally. It never reaches a rendering: String writes the
// address, Segs hands back the parts.
const sep = "\x00"

// Sep is sep for the packages that build addresses beside this one — trigger's Subtree keeps
// its segments the same way, and for the same reason.
const Sep = sep

// VerseRoot is the one reserved first segment: everything verse writes about its own running
// lives under it, and nothing else may.
//
// Two things come from the boundary being an address rather than a convention. A reader gets
// `verse data` back — the top of the tree is the systems the world is about, with one key
// beside them holding the machinery — and a WRITER can be refused: the entity routes turn away
// a write under this segment (versed.reservedWrite), where before, anyone who could reach the
// store could write a rule, a task record or somebody else's answer.
const VerseRoot = "verse"

// RefAt builds the ref standing at these segments.
//
// Fewer than Depth segments names a PREFIX — a subtree rather than an entity — and it is
// padded, which is exactly what `Ref{System: s, Kind: k}` did when it was written by hand: a
// precondition over a whole vote log addresses `vote:<motion>` and the empty third segment is
// what makes the store path end there. Padding is the bridge, not the destination: when the
// representation becomes a path, a short address is short.
func RefAt(segs ...string) Ref {
	var r Ref
	if len(segs) > 0 && segs[0] == VerseRoot {
		r.root, segs = VerseRoot, segs[1:]
	}
	// Trailing empties are dropped rather than kept, because a SHORT ADDRESS IS SHORT: two
	// segments name a subtree, and padding it to three with an empty made every walk render a
	// level nobody named. It is what `Ref{System: s, Kind: k}` meant when it was written by
	// hand, said once here instead of at each caller.
	for len(segs) > 0 && segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	r.key = strings.Join(segs, sep)
	return r
}

// VerseRef builds an address in verse's own half of the tree: `VerseRef("task", "instance", id)`
// is `verse:task:instance:<id>`. It is a separate constructor rather than a flag on RefAt so
// that reading a call says which half it is in.
func VerseRef(segs ...string) Ref {
	return RefAt(append([]string{VerseRoot}, segs...)...)
}

// Root is which half of the verse the address is in — empty for the world's, VerseRoot for
// verse's own. It is what a pattern compares against, and what Segs puts first.
func (r Ref) Root() string { return r.root }

// Reserved reports whether the address is in verse's own half — what the write refusal asks,
// and what a namespace's own predicate (mirror.IsTask and friends) asks first.
func (r Ref) Reserved() bool { return r.root == VerseRoot }

// System, Kind and ID are the three-segment reading of an address, and they are true while
// every address has three segments. What is asking is usually "which namespace is this" —
// `ref.System == mirror.System` — and that question survives the representation changing;
// this is where it will be answered differently when it does.
func (r Ref) System() string { return r.seg(0) }
func (r Ref) Kind() string   { return r.seg(1) }
func (r Ref) ID() string     { return r.seg(2) }

// seg is the nth segment below the root, or "" — the positional reading the three above are.
func (r Ref) seg(n int) string {
	for i, from := 0, 0; from <= len(r.key); i++ {
		to := strings.Index(r.key[from:], sep)
		if to < 0 {
			if i == n {
				return r.key[from:]
			}
			return ""
		}
		if i == n {
			return r.key[from : from+to]
		}
		from += to + len(sep)
	}
	return ""
}

// Len is how many segments the address has below its root: the root is a separate field, not
// one of these, so it is not counted here.
func (r Ref) Len() int {
	if r.key == "" {
		return 0
	}
	return strings.Count(r.key, sep) + 1
}

func (r Ref) String() string {
	segs := r.Segs()
	// THE COLON FORM NAMES THREE SEGMENTS, which is what every stored ref, every charter and
	// every command line holds — so an address of that shape renders exactly as it always did.
	// A deeper one has no colon spelling that can be read back (an id may contain colons, so
	// `a:b:c:d` is already `a:b:` + an id), and renders as the dotted path, which can.
	if r.Len() == Depth {
		return strings.Join(segs, ":")
	}
	return JoinSegs(segs)
}

// MarshalText / UnmarshalText spell a Ref as its address, which is how it is written
// everywhere else — a charter, a CLI argument, `ref: demo:pr:1` on the wire. Without them a
// Ref would serialize structurally, as {system, kind, id}, and the one identifier every
// surface agrees on would have two spellings depending on who wrote it.
//
// TextMarshaler is the hook both gomap paths honor (reflection and generated), the same
// reason task.Verb and task.Mode have it.
func (r Ref) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

func (r *Ref) UnmarshalText(b []byte) error {
	ref, err := ParseRef(string(b))
	if err != nil {
		return err
	}
	*r = ref
	return nil
}

// ParseRef parses "system:kind:id", or "verse:system:kind:id" for verse's own half. The id may
// itself contain colons; the segments before it may not.
//
// THE RESERVED SEGMENT IS A PREFIX, not a fourth dimension, and that is what keeps the grammar
// decidable. `a:b:c:d` is ambiguous the moment an address can be four segments — an id may hold
// colons, and `verse:task:instance:review@demo:pr:1` is a real one — so exactly one leading
// segment is read as a root, by name, and everything after it is the three an address has had
// all along. An arbitrary-depth address has a spelling of its own coming (`!verse-path [a b c]`,
// a vector rather than a string); this is not it, and does not pretend to be.
func ParseRef(s string) (Ref, error) {
	rest, reserved := strings.CutPrefix(s, VerseRoot+":")
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Ref{}, fmt.Errorf("invalid ref %q: want system:kind:id", s)
	}
	if reserved {
		return VerseRef(parts[0], parts[1], parts[2]), nil
	}
	return RefAt(parts[0], parts[1], parts[2]), nil
}

// ParseAddr reads back exactly what String wrote, AT ANY DEPTH — and it exists because
// ParseRef cannot, which is not a gap in ParseRef but the point of it.
//
// The two spellings serve different readers. `system:kind:id` is what a person types, what a
// charter holds and what every stored record from before paths carries, and its id may hold
// colons — so it is read with a fixed arity and everything past the second colon is the id. The
// dotted path is what an address DEEPER than that renders as, because `a:b:c:d` re-reads as
// three segments plus an id holding a colon. Neither can be read as the other without knowing
// which was written.
//
// SO THE DISCRIMINATOR IS WHERE THE COLONS ARE. String only ever puts a colon in the LAST
// segment (system and kind are names; an id may hold anything), so a dotted string whose first
// segment holds a colon is `system:kind:<id with dots>` and nothing else. That is the whole
// rule, and it is decidable without a marker:
//
//	verse-dev.drift.head.head-moved@git:head:verse   → 4 segments (the colons are in the id)
//	demo:pr:a.b.c.d                                  → system demo, kind pr, id a.b.c.d
//
// USE IT WHERE A REF IS BEING RECOVERED, not where one is being typed: a task's subject, a
// motion's subject, a flow. Charter and CLI input keep ParseRef, because there a dotted string
// is an address FOLLOWED BY A PAYLOAD PATH (`demo.pr.1.stage`, splitAddress) and reading it as a
// four-segment entity would silently swallow the field.
//
// It was a completion hook that found this. `applyHook` recovered its subject with ParseRef, so
// a rule whose subject was four segments deep patched — through the mangled ref — outside its own
// package, and the confinement refused the write. The review that had just done its work could
// not record that it had: docs/working/entity-tree.md §7.
func ParseAddr(s string) (Ref, error) {
	if segs, ok := DottedAddr(s); ok {
		return RefAt(segs...), nil
	}
	return ParseRef(s)
}

// DottedAddr reads the path spelling, or reports that this is not one.
//
// Exported because ParseAddr is not the only reader that has to make this call. A READ ADDRESS
// (trigger.splitAddress) is an address followed by a payload path, so it cannot use ParseAddr —
// it needs the same discriminator and a different split. Stating the rule twice is how the two
// would come to disagree about which spelling a string is.
func DottedAddr(s string) ([]string, bool) {
	if !strings.Contains(s, ".") {
		return nil, false // no dots: the colon form, or nothing
	}
	segs, err := PathSegs(s)
	if err != nil || len(segs) < 2 {
		return nil, false
	}
	// A colon in the first segment says the dots are inside an ID: `demo:pr:a.b.c.d` splits on
	// dots into four fields and is three segments.
	if strings.Contains(segs[0], ":") {
		return nil, false
	}
	return segs, true
}

// Segs is a ref's segments, in order — the form everything that WALKS an address speaks,
// as against the three names a caller writes.
//
// The distinction is the point. A store path, a subtree watch, a document's key levels and
// the entity a document's leaf stands at are all "a sequence of segments", and each of them
// used to say so by naming system, kind and id — which is how the arity came to be stated in
// a dozen places instead of one. They ask here now, so the arity is `Depth`'s business and
// theirs is the walk.
func (r Ref) Segs() []string {
	var out []string
	if r.root != "" {
		out = append(out, r.root)
	}
	if r.key == "" {
		return out
	}
	return append(out, strings.Split(r.key, sep)...)
}

// RefOf is Segs' inverse: the ref standing at these segments.
//
// It reports whether the segments name an entity at all, which today means exactly three of
// them, none empty — the same question ParseRef answers about a string. A caller that has
// walked to a leaf has segments rather than a rendering, and rebuilding the string for
// ParseRef to take apart again is how an id containing a colon comes back as a different
// entity than the one that was found.
func RefOf(segs []string) (Ref, bool) {
	// One leading `verse` is the root, and what follows it is the three an address has.
	want := Depth
	if len(segs) > 0 && segs[0] == VerseRoot {
		want++
	}
	if len(segs) != want {
		return Ref{}, false
	}
	for _, s := range segs {
		if s == "" {
			return Ref{}, false
		}
	}
	return RefAt(segs...), true
}

// RefOfAny is RefOf without the arity: the segments name a place at whatever depth, and what is
// refused is an empty one — a segment is a name, and a nameless level addresses nothing.
//
// RefOf keeps asking about the three an address has always had, because a walk at a FIXED depth
// is still what most callers want and "is this an entity" is exactly that question for them.
func RefOfAny(segs []string) (Ref, bool) {
	if len(segs) == 0 {
		return Ref{}, false
	}
	for _, s := range segs {
		if s == "" {
			return Ref{}, false
		}
	}
	return RefAt(segs...), true
}

// Depth is how many segments name an entity BELOW its root — system, kind, id.
//
// It is the ONE place the arity is written down. Everything else asks Segs, RefOf or this
// constant, which is what makes an address of another depth a change to this line and its
// grammar rather than to every walk in the tree. An address in verse's own half is one segment
// deeper, and `Segs` is what says so.
const Depth = 3

// Entity is a Ref plus its payload — one node of the verse. The payload is the small
// metadata the entity carries (not the underlying system's contents); stages and other
// lifecycle facts are conventions on it — a "stage" field a trigger matches and an action
// advances — rather than separate primitives. See payload.go: it is any tony value, and an
// object is merely the common case.
//
// Describable as of go-tony v0.0.93, which taught the schema type mapper to consult
// TextMarshaler: `ref` now generates as .[string], agreeing with the generated code that has
// always emitted one. Before that the signature said .[int] while the code emitted a string —
// nothing failed to compile and nothing misbehaved, so the only casualty would have been a
// published schema lying about the one identifier every surface shares. It was hand-written
// in schema/curated.tony until then.
//
//tony:schemagen=verse-entity,notag
type Entity struct {
	Ref     Ref     `tony:"field=ref"`
	Payload Payload `tony:"field=payload,omitzero"`
}

// Op classifies a change to the verse. It is deliberately two-valued: a change
// either SET an entity (it now has a payload) or DELETED it. Create-vs-update is
// NOT distinguished — knowing "was this the first time?" needs prior state the
// durable (logd) substrate doesn't carry on its change stream, and requiring it
// would tie the store to an uncommon capability. Express "once" as a state latch,
// not an edge (see docs/change-events.md).
type Op string

const (
	Set    Op = "set"    // the entity was created or updated; To holds the new payload
	Delete Op = "delete" // the entity was removed (a tombstone)
)

// MarshalText / UnmarshalText spell an Op as its text, which is what it already is.
//
// Same standing as task.Verb's: required until go-tony v0.0.94 (a named scalar generated
// as an int64 conversion, then as an unconverted assignment — neither compiled), kept now
// because declaring a scalar vocabulary's wire form explicitly is right on its own terms
// rather than because a generator needs it.
//
// Deliberately permissive on the way in: an unknown op decodes rather than erroring, so a
// new op from a newer peer is not a decode failure. Validation belongs where an op is
// interpreted.
func (o Op) MarshalText() ([]byte, error) { return []byte(o), nil }

func (o *Op) UnmarshalText(b []byte) error { *o = Op(b); return nil }

// Event is one observed change in the entity-verse — the unit triggers match on.
// To carries the entity's whole LEVEL state as of this commit, not the delta that was
// written; it is nil on Delete. There is no "pure signal" event: anything worth
// triggering on is a state change, so it stays durable, replayable, and uniform.
//
// There is deliberately no pre-image. One existed while the in-memory store did, which
// could read the prior value under its write lock; the docd/logd path dropped it and
// nothing missed it. Reinstating it would be cheap (the pre-delta document is in hand in
// the watch loop) but it is the wrong affordance to offer: an event is a level, and
// `Op` does not distinguish create from update, so a consumer reasoning from a diff is
// reasoning against the grain. Once-semantics come from a state latch instead — see
// docs/change-events.md.
//
//tony:schemagen=verse-event,notag
type Event struct {
	Ref Ref `tony:"field=ref"`
	Op  Op  `tony:"field=op"`
	// To is a payload, so it is an open value; it is null on a Delete.
	To Payload `tony:"field=to,omitzero"`
	// Rev is the store revision at which this change happened — a monotonic cursor.
	// A consumer tracks the highest Rev it has processed and catches up by querying
	// the store from that Rev (Store.Since), so a hold/pause/disconnect costs a
	// cursor, not a growing buffer. 0 on non-store signals (e.g. a gap marker).
	Rev int64 `tony:"field=rev"`
	Gap int   `tony:"field=gap,omitzero"` // >0: this many events dropped before this one (a slow observer)
}

// Commit is one atomic change to the verse: a revision, and every entity it changed. It is
// the unit of OBSERVATION, where Event is the unit of change — a store writes many branches
// in one transaction, and the entities of that transaction share a revision and are true
// together or not at all.
//
// Observation is framed this way because the framing cannot be recovered downstream. Events
// of one commit share a Rev, but a consumer reading them one at a time cannot tell whether
// the next one belongs to the same commit without waiting for the one after it — so a stream
// of events can only be regrouped by guessing, while the store, which built them from a
// single delta, knows exactly. Giving each consumer the grouping means none of them has to
// invent it, and it is what lets a question be asked AT a commit (Store.AsOf) rather than
// after some part of one.
//
// Changes may be empty: a commit that changed nothing this consumer can see (a subtree watch
// whose slice was untouched, or a re-established watch whose diff closed to nothing) still
// happened, and saying so is how a cursor advances without inventing changes.
type Commit struct {
	// Rev is the revision this commit landed at — the cursor a consumer tracks.
	Rev int64
	// Changes are the per-entity changes of this commit, each carrying Rev.
	Changes []Event
	// Gap is >0 when commits were dropped before this one (a slow observer). The consumer
	// recovers them with Since; see Store.Observe.
	Gap int
}
