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

	"github.com/signadot/tony-format/go-tony/ir"
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
	Ref Ref `tony:"field=ref"`
	// Spelled *ir.Node rather than the Payload alias: tony-codegen does not resolve a type
	// alias — in-package it silently becomes an int, across packages a hard error (item 18
	// on tony-format#f69agjyeh12ksa25cnn0). The alias stays the vocabulary everywhere else.
	Payload *ir.Node `tony:"field=payload,omitzero"`
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
// They exist for the reason process.Verb's do: tony-codegen resolves a named scalar through
// its TextMarshaler, and without one the generated code does not compile — at v0.0.93 it
// emits ir.FromString(s.Op) with no conversion from the named type (earlier it was an int64
// conversion and a .[int] schema). TextMarshaler is the hook both gomap paths honor, so it
// is how a scalar vocabulary declares its wire form.
//
// Deliberately permissive on the way in: an unknown op decodes rather than erroring, so a
// new op from a newer peer is not a decode failure. Validation belongs where an op is
// interpreted.
func (o Op) MarshalText() ([]byte, error) { return []byte(o), nil }

func (o *Op) UnmarshalText(b []byte) error { *o = Op(b); return nil }

// Event is one observed change in the entity-verse — the unit triggers match on.
// To carries the change's payload (what was set); it is nil on Delete. From is a
// best-effort prior payload — MemStore and Delete populate it, the logd store leaves
// it nil on Set — so it is informational, never matched on. There is no "pure
// signal" event: anything worth triggering on is a state change, so it stays
// durable, replayable, and uniform.
//
//tony:schemagen=verse-event,notag
type Event struct {
	Ref Ref `tony:"field=ref"`
	Op  Op  `tony:"field=op"`
	// From and To are payloads, so they are open values; To is null on a Delete. Spelled
	// *ir.Node rather than the Payload alias for the same reason Entity.Payload is —
	// tony-codegen does not resolve an alias (item 18).
	From *ir.Node `tony:"field=from,omitzero"`
	To   *ir.Node `tony:"field=to,omitzero"`
	// Rev is the store revision at which this change happened — a monotonic cursor.
	// A consumer tracks the highest Rev it has processed and catches up by querying
	// the store from that Rev (Store.Since), so a hold/pause/disconnect costs a
	// cursor, not a growing buffer. 0 on non-store signals (e.g. a gap marker).
	Rev int64 `tony:"field=rev"`
	Gap int   `tony:"field=gap,omitzero"` // >0: this many events dropped before this one (a slow observer)
}
