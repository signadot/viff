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

import (
	"fmt"
	"strings"
)

// Ref is a concrete, org-addressable entity: <system, kind, id>, e.g.
// "linear:issue:1090". A generic reference ("linear:issue:$id") lives a level up,
// in trigger templates; here a Ref always names one concrete thing.
type Ref struct{ System, Kind, ID string }

func (r Ref) String() string { return r.System + ":" + r.Kind + ":" + r.ID }

// ParseRef parses "system:kind:id". The id may itself contain colons; system and
// kind may not.
func ParseRef(s string) (Ref, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Ref{}, fmt.Errorf("invalid ref %q: want system:kind:id", s)
	}
	return Ref{System: parts[0], Kind: parts[1], ID: parts[2]}, nil
}

// Payload is the small metadata an entity carries — a few descriptive fields, not
// the underlying system's contents. Stages and other lifecycle facts are
// conventions on this payload (a "stage" field a trigger matches and an action
// advances), not a separate primitive.
type Payload map[string]any

func (p Payload) clone() Payload {
	if p == nil {
		return nil
	}
	c := make(Payload, len(p))
	for k, v := range p {
		c[k] = v
	}
	return c
}

// Entity is a Ref plus its payload — one node of the verse.
type Entity struct {
	Ref     Ref
	Payload Payload
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

// Event is one observed change in the entity-verse — the unit triggers match on.
// To carries the change's payload (what was set); it is nil on Delete. From is a
// best-effort prior payload — MemStore and Delete populate it, the logd store leaves
// it nil on Set — so it is informational, never matched on. There is no "pure
// signal" event: anything worth triggering on is a state change, so it stays
// durable, replayable, and uniform.
type Event struct {
	Ref  Ref
	Op   Op
	From Payload
	To   Payload
	// Rev is the store revision at which this change happened — a monotonic cursor.
	// A consumer tracks the highest Rev it has processed and catches up by querying
	// the store from that Rev (Store.Since), so a hold/pause/disconnect costs a
	// cursor, not a growing buffer. 0 on non-store signals (e.g. a gap marker).
	Rev int64
	Gap int // >0: this many events were dropped before this one (a slow observer)
}
