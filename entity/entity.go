// Package entity is the entity-verse: the substrate of things an SDLC refers to
// or generates, each named by a <system, kind, id> triple. It is an *index, not a
// warehouse* — an entity holds high-level metadata (a small payload), not the full
// contents of the underlying system; anything needing depth follows the reference
// into the real system on its own.
//
// The store is observable: every change is an Event on a stream. That stream is
// what triggers (elsewhere) match on, and where process state is reflected — so
// "observe another process" is just "observe an entity."
package entity

//go:generate go tool tony-codegen -dir .

import (
	"fmt"
	"strings"
)

// Ref is a concrete, org-addressable entity: <system, kind, id>, e.g.
// "linear:issue:1090". A generic reference ("linear:issue:$id") lives a level up,
// in trigger templates; here a Ref always names one concrete thing.
type Ref struct{ System, Kind, ID string }

func (r Ref) String() string { return r.System + ":" + r.Kind + ":" + r.ID }

// MarshalText / UnmarshalText spell a Ref as its address, which is how it is written
// everywhere else — a charter, a CLI argument, `ref: demo:pr:1` on the wire. Without them a
// Ref would serialize structurally, as {system, kind, id}, and the one identifier every
// surface agrees on would have two spellings depending on who wrote it.
//
// TextMarshaler is the hook both gomap paths honor (reflection and generated), the same
// reason process.Verb and process.Mode have it.
func (r Ref) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

func (r *Ref) UnmarshalText(b []byte) error {
	ref, err := ParseRef(string(b))
	if err != nil {
		return err
	}
	*r = ref
	return nil
}

// ParseRef parses "system:kind:id". The id may itself contain colons; system and
// kind may not.
func ParseRef(s string) (Ref, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Ref{}, fmt.Errorf("invalid ref %q: want system:kind:id", s)
	}
	return Ref{System: parts[0], Kind: parts[1], ID: parts[2]}, nil
}

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
// Same standing as process.Verb's: required until go-tony v0.0.94 (a named scalar generated
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
