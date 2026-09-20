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

	"github.com/signadot/tony-format/go-tony/ir"
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

// RefAt builds the ref standing at these segments, at whatever depth there are.
//
// A SHORT ADDRESS IS SHORT. Two segments are two segments — a place in the tree like any
// other, which a read answers at and a write lands at — rather than a prefix to be filled out
// to the arity an address once had.
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

// Name is the last segment of the path: the key the node stands at in its parent. It is what a
// record in a place the system decides is called there — a task's id under verse.task.instance.
func (r Ref) Name() string {
	segs := r.Segs()
	if len(segs) == 0 {
		return ""
	}
	return segs[len(segs)-1]
}

// Under reports whether the ref is at or below the path prefix.
func (r Ref) Under(prefix ...string) bool { return Under(prefix, r.Segs(), Literal) }

// ChildOf reports whether the ref is exactly one segment below the path prefix: a record in a place
// the system decides, like a task under verse.task.instance or a perms record under verse.perms.node.
func (r Ref) ChildOf(prefix ...string) bool {
	return len(r.Segs()) == len(prefix)+1 && r.Under(prefix...)
}

// String spells an address as the kinded path it is, at every depth — `demo.pr."1"`,
// `verse-dev.drift.head.abc`, `git.ref."x@git:ref:y"`. ParseAddr reads back exactly this, and
// there is nothing else to read.
//
// ONE SPELLING AT EVERY DEPTH, rather than one per depth. A grammar that spelled three segments
// differently from four would need a DISCRIMINATOR: every reader recovering an address from text
// deciding which of two grammars it is looking at, and the rule restated wherever a read address
// (an address followed by a payload path) needs the same question answered with a different
// split. Quoting says what a delimiter cannot — `demo.pr."a.b"` is an id holding a dot — so one
// grammar needs no such rule.
//
// The quoting is what makes one grammar enough. A segment that would otherwise read as something
// else is quoted — a numeric id, a name holding a dot — so `demo.pr."a.b"` and `demo.pr."a"."b"`
// are different addresses on the page as well as in the store, which is exactly what the colon
// form could not say.
func (r Ref) String() string { return JoinSegs(r.Segs()) }

// MarshalText / UnmarshalText spell a Ref as its address, which is how it is written
// everywhere else — a charter, a CLI argument, `at: demo.pr.1` on the wire. Without them a
// Ref would serialize structurally, as {system, kind, id}, and the one identifier every
// surface agrees on would have two spellings depending on who wrote it.
//
// TextMarshaler is the hook both gomap paths honor (reflection and generated), the same
// reason task.Verb and task.Mode have it.
func (r Ref) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

func (r *Ref) UnmarshalText(b []byte) error {
	ref, err := ParseAddr(string(b))
	if err != nil {
		return err
	}
	*r = ref
	return nil
}

// ParseAddr reads an address: a kinded path, at any depth, and the inverse of String.
//
// It is the ONE reader. A colon is an ordinary character in a name, so `system:kind:id` is an
// address ONE segment deep whose name holds colons — which is what makes `git.ref."x@git:ref:y"`
// readable, and what a store seeded under another spelling would have to be re-seeded for.
//
// A wildcard is refused, and an empty segment with it: this answers "which one place", and
// neither names one. `PathSegs` is where both refusals live, so the read routes that DO take a
// wildcard ask it directly rather than going through here.
func ParseAddr(s string) (Ref, error) {
	segs, err := PathSegs(s)
	if err != nil {
		return Ref{}, fmt.Errorf("invalid address %q: %w", s, err)
	}
	ref, ok := RefOfAny(segs)
	if !ok {
		return Ref{}, fmt.Errorf("invalid address %q: a segment is a name, and none of them "+
			"may be empty", s)
	}
	return ref, nil
}

// Segs is a ref's path: its segments, in order, `verse` first for an address in verse's own
// half. Its length is the address's length — there is no second count that leaves the root out.
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

// RefOfAny is Segs' inverse: the ref standing at these segments, at whatever depth. What is
// refused is no segments, or an empty one — a segment is a name, and a nameless level addresses
// nothing. A caller that has walked to a node has segments rather than a rendering, and
// rebuilding a string to take apart again is how an id holding a delimiter comes back as a
// different address than the one that was found.
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

// Entity is a Ref plus its payload — one node of the verse. The payload is the small
// metadata the entity carries (not the underlying system's contents); stages and other
// lifecycle facts are conventions on it — a "stage" field a trigger matches and an action
// advances — rather than separate primitives. See payload.go: it is any tony value, and an
// object is merely the common case.
//
// THE ADDRESS IS SPELLED `at`, the same word the request side asks with: `POST /entity` takes
// `at:` and `GET /state?at=` reads at one. One word at both ends, so nothing has to know which
// half of the exchange it is looking at, and the value is a kinded path at any depth.
//
// Describable as of go-tony v0.0.93, which taught the schema type mapper to consult
// TextMarshaler: the address generates as .[string], agreeing with the generated code that has
// always emitted one. Before that the signature said .[int] while the code emitted a string —
// nothing failed to compile and nothing misbehaved, so the only casualty would have been a
// published schema lying about the one identifier every surface shares. It was hand-written
// in schema/curated.tony until then.
//
//tony:schemagen=verse-entity,notag
type Entity struct {
	Ref     Ref     `tony:"field=at"`
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
	// The address the change is about, spelled the way every other surface spells one — see
	// Entity, which this agrees with deliberately: a consumer reading a change and then reading
	// the entity should not meet two words for the place.
	Ref Ref `tony:"field=at"`
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
	// Author is who the write that made this commit was made by, as the write named it
	// (Write.Principal): a user, a runner, the address of the rule or source that acted. Empty
	// when the write named nobody, and for a subtree watch, which diffs documents rather than
	// reading commits.
	Author string
	// Changes are this commit's changes at the patterns its observer asked about (see
	// changesAt), each carrying Rev: every node those patterns reach whose node differs.
	Changes []Event
	// Gap is >0 when commits were dropped before this one (a slow observer). The consumer
	// recovers them with Since; see Store.Observe.
	Gap int
	// delta is the change this commit made, as the store received it, root already stripped.
	// It is what lets Touches answer about a path Changes was not built for; see there.
	//
	// Unexported because the delta's ENCODING is the store's — a merge, a `!replace`, a
	// `!delete`, a field order — and nothing outside this package should learn to read one. A
	// commit that carries none (a subtree watch's, which diffs a reconstructed document rather
	// than reading the delta) answers from Changes instead.
	delta *ir.Node
	// hidden, when set, is what this commit's reader may not see: Touches leaves those
	// addresses out, as Only left them out of Changes, so a consumer asking either question
	// gets the same view. See Only.
	hidden func(Ref) bool
}

// Only is the commit as seen by a reader that may not see everything: Changes holds only the
// events keep admits, and Touches answers only about addresses keep admits. The delta is kept,
// since a consumer asking about a path Changes was not built for still needs it, and hidden is
// what stops that walk from answering with what Changes withheld.
func (c Commit) Only(keep func(Ref) bool) Commit {
	kept := make([]Event, 0, len(c.Changes))
	for _, ev := range c.Changes {
		if keep(ev.Ref) {
			kept = append(kept, ev)
		}
	}
	c.Changes = kept
	prior := c.hidden
	c.hidden = func(r Ref) bool { return !keep(r) || (prior != nil && prior(r)) }
	return c
}

// WithDelta is the store stamping the change it received onto the commit it emits, so a
// consumer can ask Touches about a path Changes was not built for. Package-internal by
// convention: only a Store builds a Commit.
func (c Commit) WithDelta(delta *ir.Node) Commit { c.delta = delta; return c }
