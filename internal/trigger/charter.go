package trigger

// The charter is its records: an installed rule is written into the viff instance as a
// trigger:rule:<name> entity, and the engine follows the records (follow.go).
//
// Everything else a restart needs is durable — a task is mirrored, a sequence's position is a
// fact on its subject, a claim is a debt in the store, and a task's hooks are looked up by its
// chain when it ends — so the program is too: a viff instance comes back up with its work in progress intact and
// the charter standing to react to it.
//
// It is one entity per rule rather than one charter document, because that is what the
// operations already are: installing writes one entity, removing deletes one, and neither
// needs to read the others (no read-modify-write, so two installs cannot lose each other).
// It also makes a rule addressable — `viff data trigger:rule:review` — and a charter
// change an ordinary observable fact.

import (
	"errors"
	"fmt"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/internal/trigger/charter"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/systems"
)

// record builds a declaration's durable payload (charter.Payload): the spec, plus the scope
// when this engine acts in one and the address it was installed at.
func (e *Engine) record(doc *ir.Node) entity.Payload { return e.recordObj(doc).Payload() }

func (e *Engine) recordObj(doc *ir.Node) entity.Obj {
	return charter.Payload(doc, e.scope, e.confine.stamp())
}

// installer is who an install acting as `as` is by: `as`, or the principal the engine's own
// store writes as when the install names none — which is who the record is then written as.
func (e *Engine) installer(as string) string {
	if as != "" {
		return as
	}
	return entity.PrincipalOf(e.store)
}

// RuleRef is where the rule of this name stands in this engine's charter, and StratumRef a
// level: the address its content is at, and the one its perms record is about.
func (e *Engine) RuleRef(name string) entity.Ref { return charter.RuleRef(e.confine.stamp(), name) }

func (e *Engine) StratumRef(name string) entity.Ref {
	return charter.StratumRef(e.confine.stamp(), name)
}

// OwnerOf is whose the rule of this name is, and so who it fires as: the owner on its record
// (entity.ActsAs). "" for a rule with none, which fires as nobody, for a mirror that did not
// answer, and over a store that attributes nothing.
func (e *Engine) OwnerOf(name string) string {
	who, _ := e.ownerOf(name)
	return who
}

// ownerOf is OwnerOf with the mirror not answering apart from the rule having no record: "" and
// the error for the first, "" and none for the second and over a store that attributes nothing.
func (e *Engine) ownerOf(name string) (string, error) {
	if entity.PolicyOf(e.store) == nil {
		return "", nil
	}
	return entity.ActsAs(e.store, e.RuleRef(name))
}

// asOwner is whose a rule placed from its record is vetted as — and so armed as, since what a
// pass vets it as is what it fires as — or the cause it is held for instead. A place acts as
// the owner on its own record and as nobody without one (entity.ActsAs), so a rule whose record
// is not here is not vetted as anybody: its install writes the record after the rule (persist),
// and a follow that delivers the rule first would arm it as nobody, its backlog fired at once
// and every read refused, and the firing it gets over what already stood spent. Held for the
// record instead, it is armed by the pass that follows it (InstallTriggerAs kicks once the
// record is in), and arms over the same backlog as whose it is. A record the mirror did not
// answer for is a read the vetting needs that did not happen. Over a store that attributes
// nothing a rule is nobody's and is vetted so, with nothing to wait for.
func (e *Engine) asOwner(name string) (string, *cause) {
	as, err := e.ownerOf(name)
	switch {
	case err != nil:
		return "", &cause{causeRead, "whose it is: " + err.Error()}
	case as == "" && entity.PolicyOf(e.store) != nil:
		return "", &cause{causeOwner, ""}
	}
	return as, nil
}

// ErrCharter marks an install refused by the STORE rather than by the rule: the trigger is
// fine, the viff instance could not record it. A caller answering a request tells the two apart
// with errors.Is — a malformed rule is the installer's problem, an unwritable charter is
// viff's, and reporting the second as the first sends someone to debug a correct trigger.
var ErrCharter = errors.New("the charter could not be written")

// persist writes t into the viff instance as its charter entity, and answers the revision the write
// landed at: what a caller waits for the follow to reach. A write that changed nothing lands
// at none.
//
// Its failure is RETURNED rather than logged, unlike the engine's reactive writes: this one
// is on a request path (`viff c -a`), and the record is the rule, so a trigger that did not
// persist is one the engine never runs. The installer hears that at install time.
func (e *Engine) persist(t Trigger, as string, take bool) (int64, error) {
	doc, err := t.ToTonyIR()
	if err != nil {
		return 0, fmt.Errorf("trigger %q: encoding it for the charter: %w", t.Name, err)
	}
	ref := charter.RuleRef(e.confine.stamp(), t.Name)
	// The installing task is written with the rule, or taken off: a write merges, and a rule a
	// person installs over a task's is theirs, held by nothing the task was.
	rec := e.recordObj(doc)
	rec[charter.TaskField], rec[charter.TaskEnvironmentField] = entity.Removed(), entity.Removed()
	if t.Task != "" {
		rec[charter.TaskField] = t.Task
		if t.TaskEnvironment != nil {
			rec[charter.TaskEnvironmentField] = systems.Environment{Systems: t.TaskEnvironment.Systems}
		}
	}
	_, rev, err := e.store.Write(entity.Write{
		At:        ref.At(),
		Value:     rec.Payload(),
		Principal: as,
	})
	if err != nil {
		return 0, fmt.Errorf("%w: trigger %q: writing %s: %w", ErrCharter, t.Name, ref, err)
	}
	if !take {
		return rev, nil
	}
	return rev, e.takeForTheSystem(ref, as)
}

// takeForTheSystem gives a record the system installed an owner record of its own, the
// system's. A write by the system records none, since the system owns the root and a node reads
// as its parent's — and the parent here is a seed row that holds `owner` beneath it out to
// everyone, so that anybody may install a rule: with no record to stop at, that grant reaches
// the system's own rules, and whoever rewrites one has written what fires as the system. A
// person's install needs nothing here: their write records them.
func (e *Engine) takeForTheSystem(ref entity.Ref, as string) error {
	installer := e.installer(as)
	if !entity.IsSystem(installer) {
		return nil
	}
	displaced, err := entity.ClaimForSystem(e.store, ref, installer)
	if err != nil {
		return fmt.Errorf("%w: taking %s for %s: %w", ErrCharter, ref, installer, err)
	}
	if displaced != "" {
		// Somebody's record stood on a name the system decides. Said, because it is either a
		// squat or a person's rule the system's charter has just been given the name of.
		e.logger.Warn("a record stood on a name the system installs at, and is replaced by the system's",
			"at", ref.String(), "was", displaced, "now", installer)
	}
	return nil
}

// forget deletes a rule's record, and answers the revision the delete landed at. Deleting an
// address nothing ever wrote is a clean no-op in the store, which is what lets a removal of a
// name nothing holds take this same path, with no second one. Its failure is returned: the
// record is the rule, so a delete that did not land is a removal that did not happen.
func (e *Engine) forget(name string) (int64, error) {
	ref := charter.RuleRef(e.confine.stamp(), name)
	_, rev, err := e.store.Write(entity.Write{At: ref.At()})
	if err != nil {
		return 0, fmt.Errorf("%w: trigger %q: deleting %s: %w", ErrCharter, name, ref, err)
	}
	return rev, nil
}

// ruleOf decodes one charter record back into a trigger.
//
// A name that disagrees with the address is refused rather than reconciled. The two are
// written together and can only diverge by a hand edit, and neither answer is safe: the
// name is what a spawn id and a chain are built from, while the address is what a removal
// deletes — so installing under one and deleting under the other would leave a rule nothing
// can get rid of.
func ruleOf(rec charter.Record) (Trigger, error) {
	if rec.Spec == nil {
		return Trigger{}, fmt.Errorf("charter %s: no %s field", rec.Ref, charter.SpecField)
	}
	var t Trigger
	if err := t.FromTonyIR(rec.Spec); err != nil {
		return Trigger{}, fmt.Errorf("charter %s: %w", rec.Ref, err)
	}
	t.Task = rec.Task
	if rec.TaskEnvironment != nil && t.Task != "" {
		var env systems.Environment
		if err := env.FromTonyIR(rec.TaskEnvironment); err != nil {
			return Trigger{}, fmt.Errorf("charter %s: %s: %w", rec.Ref, charter.TaskEnvironmentField, err)
		}
		t.TaskEnvironment = &env
	}
	// The id is the name for baseline and `<address>:<name>` for a package (charter.ID), so
	// the check is against the tail: what has to agree is the NAME, and the package half of the
	// address is not part of it.
	if t.Name != rec.Name {
		return Trigger{}, fmt.Errorf("charter %s: the rule names itself %q; a rule is addressed by its "+
			"name, so it would install as one trigger and be removed as another", rec.Ref, t.Name)
	}
	return t, nil
}
