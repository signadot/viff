package entity

import (
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
)

// The reserved payload namespace: everything viff itself records about an entity, kept out
// of the author's own document under ONE key.
//
//	viff:
//	  charter:               what the engine records about rules acting on this entity
//	    review:              …by trigger name
//	      step:  2
//	      steps: 3
//	      done:  true
//	  meta:                  what viff records about the entity ITSELF
//	    vanishing: "2026-07-29T13:04:05Z"
//
// The two halves are separated because they answer to different things — `charter` grows a key
// per rule, `meta` grows a field per lifecycle concern — and because a flat reservation made
// them collide: a trigger NAMED `vanishing` would have written its progress exactly where the
// lifecycle marker lives.
//
// The name is plain `viff`, and that is not settled. A sigil would say "reserved" at a glance
// and free the word for authors; `@viff`, `~viff` and `$viff` all parse unquoted, address
// through kpath, and round-trip through the yaml renderer. `.viff` does NOT and is the one to
// avoid: kpath parses `.@sys.meta.vanishing` back as `@sys.meta.vanishing`, silently dropping
// the sigil rather than refusing it.
const (
	// ReservedRoot is the one payload key that is not the author's.
	ReservedRoot = "viff"
	// CharterKey holds per-trigger bookkeeping, keyed by trigger name.
	CharterKey = "charter"
	// MetaKey holds what viff records about the entity itself.
	MetaKey = "meta"
)

// VanishingField marks an entity as ON ITS WAY OUT: the intent to remove it, written before
// the removal happens, at `@sys.meta.vanishing`.
//
// It exists so that a DISAPPEARANCE can be observed the way an appearance is. A trigger's cause
// is the rising edge of a store predicate — "something matching this now exists" — and there is
// no way to say the negative: a match asks what is there. Writing the intent down first turns
// the negative into a positive, so `on: {…, value: {viff: {meta: {vanishing: null}}}}` is an
// ordinary predicate rising, and the model stays existence-positive throughout.
//
// The actual removal is then INERT, correctly: when the entity finally goes, a predicate over
// it falls from true to false, and nothing fires on a falling edge. The reactive moment was the
// intent; the removal is bookkeeping.
//
// Its VALUE is the instant the intent was recorded, and charters should ask about PRESENCE
// rather than about the value — `{vanishing: null}` is tony's present-any-value idiom (see
// entity.TestNullPresenceAbsenceIdioms). That is what leaves room for the field to grow into a
// record — who asked, why, a grace period — without every charter that matched on `true`
// having to be rewritten.
const VanishingField = "vanishing"

// Vanishing reports whether a payload carries the vanishing marker, whatever its value.
func Vanishing(p Payload) bool {
	return Get(Get(Get(p, ReservedRoot), MetaKey), VanishingField) != nil
}

// VanishPatch is the patch that marks an entity as vanishing, stamped with the instant the
// intent was recorded. Write it at the entity's own path, through the ordinary write path, so
// entering the sink state is a commit like any other and every rule watching that subtree is
// evaluated at it.
//
// The instant is the caller's: this package holds no clock, and the engine's is injectable so
// a test can make a deadline arrive without waiting for one.
func VanishPatch(at time.Time) Payload {
	return Obj{ReservedRoot: Obj{MetaKey: Obj{
		VanishingField: at.UTC().Format(time.RFC3339),
	}.Payload()}.Payload()}.Payload()
}

// namesVanishing reports whether a predicate asks about the vanishing marker — the OPT-IN that
// lets a selection see entities on their way out.
//
// It looks for the literal path `@sys.meta.vanishing`, which is enough because that is the
// only spelling of the question. A predicate that merely mentions `viff` — to match a rule's
// progress under `charter`, say — is not asking about lifecycle and does not opt in.
func namesVanishing(pred *ir.Node) bool {
	meta := field(field(pred, ReservedRoot), MetaKey)
	if meta == nil {
		return false
	}
	return ir.Get(meta, VanishingField) != nil
}

// field reads an object key of a predicate node, or nil when there is no object there.
func field(n *ir.Node, key string) *ir.Node {
	if n == nil || n.Type != ir.ObjectType {
		return nil
	}
	return ir.Get(n, key)
}
