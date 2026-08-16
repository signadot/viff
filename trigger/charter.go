package trigger

// The charter is durable: an installed trigger is written into the verse as a
// trigger:rule:<name> entity, and read back at startup.
//
// Before this, a charter lived only in the engine's memory. Everything else a restart needs
// was already durable — a task is mirrored, a sequence's position is a fact on its
// subject, a claim is a lease in the store, and resume rehydrates a chain's hooks from it —
// so the one thing that did not survive was the program itself. A verse came back up with
// its work in progress intact and nothing left to react to it.
//
// It is one entity per rule rather than one charter document, because that is what the
// operations already are: installing writes one entity, removing deletes one, and neither
// needs to read the others (no read-modify-write, so two installs cannot lose each other).
// It also makes a rule addressable — `verse state get trigger:rule:review` — and a charter
// change an ordinary observable fact.

import (
	"errors"
	"fmt"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
)

// CharterSystem/CharterKind name where the durable charter lives: trigger:rule:<name>, the
// id being the trigger's name (which is unique by construction — AddTrigger refuses a
// duplicate — so the address needs nothing else to be distinct).
const (
	CharterSystem = "trigger"
	CharterKind   = "rule"
)

// CharterRef names the entity holding one rule.
func CharterRef(name string) entity.Ref {
	return entity.VerseRef(CharterSystem, CharterKind, name)
}

// specField is where a durable record keeps the declaration it was installed from — the rule
// on trigger:rule. One name for it across record kinds, because it is one idea: a goal kept
// its own definition under this same key while goals existed.
//
// It holds the declaration as STRUCTURE, written under entity.Raw. A charter is full of
// operator tags — `id: !glob "hotfix-*"`, `patch: {tmp: !delete null}` — and a write is a
// tony patch, so an unescaped payload has them executed by the store rather than stored by
// it: not a rule that reads back wrong, one that cannot be read back at all (the entity's own
// patch log stops applying, "glob patching null gave cannot patch with glob operation"). That
// is what forced the text form here, and `!raw` is the escape that lifts it — the subtree is
// stored verbatim, tags intact, interpreting nothing at any depth.
//
// Structure rather than text because the record is then the rule itself: `verse state get
// trigger:rule:review` shows a document a person can read field by field, a charter change is
// an ordinary observable fact about a shape rather than about a string, and nothing has to
// re-parse to ask a question about it. Raw also REPLACES, so re-installing a rule cannot
// leave a field of the version before it standing in the version after — which a merged
// structural write would, and which the text form got for free by being one scalar.
const specField = "spec"

// scopeField is the federation a record belongs to — absent for baseline. It is what makes a
// scope's charter restorable: a scoped store reads baseline layered underneath, so a listing
// in a scope returns both, and only the record can say which is which. See Engine.SetScope.
const scopeField = "scope"

// ownRecord reports whether a charter record belongs to THIS engine's federation: the scope
// stamped on it is the scope the engine acts in.
//
// A record with no scope is baseline's. Read from a scope that is what an INHERITED rule looks
// like, and skipping it is what keeps `verse connect` from installing the whole baseline
// charter into one dev's federation — which is a decision about what a scope means, not a
// restore detail, and is not this function's to make (docs/reference/connect.md).
func (e *Engine) ownRecord(ent entity.Entity) bool {
	return entity.Str(ent.Payload, scopeField) == e.scope
}

// record builds a declaration's durable payload: the spec, plus the scope when this engine
// acts in one. One builder, so a rule and a stratum cannot drift on either.
func (e *Engine) record(doc *ir.Node) entity.Payload {
	p := entity.Obj{specField: entity.Raw(doc)}
	if e.scope != "" {
		p[scopeField] = e.scope
	}
	return p.Payload()
}

// ErrCharter marks an install refused by the STORE rather than by the rule: the trigger is
// fine, the verse could not record it. A caller answering a request tells the two apart
// with errors.Is — a malformed rule is the installer's problem, an unwritable charter is
// verse's, and reporting the second as the first sends someone to debug a correct trigger.
var ErrCharter = errors.New("the charter could not be written")

// persist writes t into the verse as its charter entity.
//
// Its failure is RETURNED rather than logged, unlike the engine's reactive writes: this one
// is on a request path (`verse trigger add`), and a trigger that did not persist is one the
// next restart silently drops. The installer is entitled to hear that at install time
// rather than discover it after a restart, by its absence.
func (e *Engine) persist(t Trigger) error {
	doc, err := t.ToTonyIR()
	if err != nil {
		return fmt.Errorf("trigger %q: encoding it for the charter: %w", t.Name, err)
	}
	ref := CharterRef(t.Name)
	if _, _, err := e.store.Write(entity.Write{
		At:    ref.At(),
		Value: e.record(doc),
	}); err != nil {
		return fmt.Errorf("%w: trigger %q: writing %s: %w", ErrCharter, t.Name, ref, err)
	}
	return nil
}

// forget deletes a rule's charter entity, so a removal survives the restart the install
// does. Logged rather than returned: a trigger removed from a live engine has stopped
// firing whatever the store says, so the removal has already taken effect — but an
// operator has to know that the rule will come back at the next start.
//
// Deleting an address nothing ever wrote is a clean no-op in the store, which is what lets a
// deliberately non-durable rule take this same removal path without a second one, and without
// a Warn. No durable rule can be caught by it either: a name is unique across the charter.
func (e *Engine) forget(name string) { e.write(CharterRef(name).At(), nil) }

// LoadCharter installs the rules standing in the verse — the durable charter — into this
// engine. Called at startup, before observation begins, so that entering subtree mode
// gives each restored rule its watch (see ObserveScoped).
//
// Restoring a rule is an ordinary install, which means it REPLAYS: a subtree watch is
// level-triggered at the open, so a restored trigger acts on what already matches it. That
// is the same property installing a trigger has always had, and it is why a charter states
// transitions as latches rather than edges — see docs/change-events.md. A restart is then
// just an install over a verse that has moved on.
//
// A rule that cannot be decoded or installed is REPORTED AND SKIPPED, and its entity is
// left alone: the rest of the charter is still a program worth running, and a rule verse
// could not read is not one it should quietly delete. Errors are joined, so one bad rule
// names itself rather than hiding behind "the charter failed to load".
//
// IN A SCOPE it restores the scope's OWN rules and passes over the baseline ones it can see
// through the overlay (ownRecord). Baseline's charter running in every live federation is a
// question about what a scope means, and a restore is not where it gets decided.
func (e *Engine) LoadCharter() error {
	var errs []error
	for _, ent := range e.store.List(func(r entity.Ref) bool {
		return r.Reserved() && r.System() == CharterSystem && r.Kind() == CharterKind
	}) {
		if !e.ownRecord(ent) {
			continue
		}
		t, err := ruleOf(ent)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		// durable=false: it is already durable — that is where it came from. Re-writing it
		// would be a no-op with a revision attached, and a charter subtree watch would see
		// every restart as a charter change.
		if err := e.addTrigger(t, false); err != nil {
			errs = append(errs, fmt.Errorf("charter %s: %w", ent.Ref, err))
		}
	}
	return errors.Join(errs...)
}

// ruleOf decodes one charter entity back into a trigger.
//
// A name that disagrees with the address is refused rather than reconciled. The two are
// written together and can only diverge by a hand edit, and neither answer is safe: the
// name is what a spawn id and a chain are built from, while the address is what a removal
// deletes — so installing under one and deleting under the other would leave a rule nothing
// can get rid of.
func ruleOf(ent entity.Entity) (Trigger, error) {
	doc := entity.Get(ent.Payload, specField)
	if doc == nil {
		return Trigger{}, fmt.Errorf("charter %s: no %s field (payload is %v)", ent.Ref, specField, ent.Payload)
	}
	var t Trigger
	if err := t.FromTonyIR(doc); err != nil {
		return Trigger{}, fmt.Errorf("charter %s: %w", ent.Ref, err)
	}
	if t.Name != ent.Ref.ID() {
		return Trigger{}, fmt.Errorf("charter %s: the rule names itself %q; a rule is addressed by its "+
			"name, so it would install as one trigger and be removed as another", ent.Ref, t.Name)
	}
	return t, nil
}
