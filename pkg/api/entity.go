package api

import "github.com/signadot/verse/pkg/entity"

// PutEntity is one conditional write (`POST /entity`) — the store's single write, on the
// wire. Creating, updating, patching a field and removing one are all this request with
// different fields set.
//
// THE ADDRESS IS ONE KINDED PATH, at whatever depth, and it is the whole place written:
// `demo.pr.1` is an entity, `demo.pr.1.labels.urgent` is a field of one, and nothing on the
// wire says which — entity-hood is a LENS rather than a stored fact, so a write that had to
// declare where the entity ended was
// answering a question the store does not ask. It carried three spellings to avoid it: a
// three-segment `ref`, a `path` vector for anything deeper, and a `field` list for the place
// inside. They are one path, so that is what this takes — the same grammar the READ takes
// (`GET /state?at=`).
//
// A KINDED PATH RATHER THAN A VECTOR because the quoting is the grammar's own: an entity id
// may hold a dot (a file path is a perfectly good id), and `demo.pr."a.b"` says so where a
// bare dotted string could not. That was the argument for the vector, and kpath answers it
// without a second spelling.
//
// The value MERGES at that place: fields it does not mention are left alone, and an absent
// (null) Payload removes whatever stands there. A writer that means to drop a field says
// so, by writing null at it.
//
// Payload is a tony node, not a map: an entity payload is ANY tony value (see
// entity.Payload), so `verse data demo count 1 --put 42` has to survive the wire as the
// number it is. A map here would have re-imposed on the wire exactly the object constraint
// the store just dropped.
//
//tony:schemagen=verse-put-entity,notag
type PutEntity struct {
	// At is the place written, as a kinded path: `demo.pr.1`, `demo.pr.1.labels.urgent`,
	// `demo.pr."a.b"`.
	At string `tony:"field=at"`
	// Sub is the place inside the address when it is richer than names: an array access, and
	// anything under it — `votes[0]`, `votes(walter).choice`. A kinded path fragment below At.
	// See entity.Path.Access for why it rides beside the address rather than inside it.
	Sub     string         `tony:"field=sub,omitzero"`
	Payload entity.Payload `tony:"field=payload,omitzero"`
	// When are preconditions, ALL of which must hold. They are checked WITH the write, in
	// one step at the store, so there is no window between the check and the write for
	// something to change underneath. An empty When is an unconditional write.
	When []Condition `tony:"field=when,omitzero"`
	// Grants is what the node the write CREATES holds out, in the list `verse perms --grants`
	// sets on a record — `[{read: [ana@example.com]}]`, and `[]` for nothing — in place of the
	// default it would copy from the nearest `child-grants` above. The write is then a create:
	// the node lands with its record in one commit, or not at all, and a node that stands is
	// refused (409); what it holds out is changed with `verse perms`.
	Grants entity.Payload `tony:"field=grants,omitzero"`
}

// Condition is one precondition on a write: what must be true, and where.
//
// At names the place asked about, in the same grammar the write's own address is in; empty
// means the write's own place — the array element it names, where Sub names one — which is
// what create ("nothing is there yet") and update ("something is") are about.
//
//tony:schemagen=verse-condition,notag
type Condition struct {
	At string `tony:"field=at,omitzero"`
	// Exists asks only whether anything stands there: false is a create, true is an update.
	// It is a pointer so that "did not ask" is distinguishable from "asked for absence".
	Exists *bool `tony:"field=exists,omitzero"`
	// Pattern asks what it looks like — a tony match, so it can say more than equality.
	Pattern entity.Payload `tony:"field=pattern,omitzero"`
}
