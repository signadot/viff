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
// It also makes a rule addressable — `verse data trigger:rule:review` — and a charter
// change an ordinary observable fact.

import (
	"errors"
	"fmt"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/trigger/charter"
)

// ownRecord reports whether a charter record belongs to THIS engine's scope: the one
// stamped on it is the scope the engine acts in.
//
// A record with no scope is baseline's. Read from a scope that is what an INHERITED rule looks
// like, and skipping it is what keeps a restore from installing the whole baseline
// charter into one scope — which is a decision about what a scope means, not a
// restore detail, and is not this function's to make (docs/reference/scopes.md).
func (e *Engine) ownRecord(ent entity.Entity) bool {
	return entity.Str(ent.Payload, charter.ScopeField) == e.scope &&
		entity.Str(ent.Payload, charter.AtField) == e.confine.stamp()
}

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
	if entity.PolicyOf(e.store) == nil {
		return ""
	}
	who, err := entity.ActsAs(e.store, e.RuleRef(name))
	if err != nil {
		return ""
	}
	return who
}

// ErrCharter marks an install refused by the STORE rather than by the rule: the trigger is
// fine, the verse could not record it. A caller answering a request tells the two apart
// with errors.Is — a malformed rule is the installer's problem, an unwritable charter is
// verse's, and reporting the second as the first sends someone to debug a correct trigger.
var ErrCharter = errors.New("the charter could not be written")

// persist writes t into the verse as its charter entity.
//
// Its failure is RETURNED rather than logged, unlike the engine's reactive writes: this one
// is on a request path (`verse c -a`), and a trigger that did not persist is one the
// next restart silently drops. The installer is entitled to hear that at install time
// rather than discover it after a restart, by its absence.
func (e *Engine) persist(t Trigger, as string, take bool) error {
	doc, err := t.ToTonyIR()
	if err != nil {
		return fmt.Errorf("trigger %q: encoding it for the charter: %w", t.Name, err)
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
	if _, _, err := e.store.Write(entity.Write{
		At:        ref.At(),
		Value:     rec.Payload(),
		Principal: as,
	}); err != nil {
		return fmt.Errorf("%w: trigger %q: writing %s: %w", ErrCharter, t.Name, ref, err)
	}
	if !take {
		return nil
	}
	return e.takeForTheSystem(ref, as)
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

// forget deletes a rule's charter entity, so a removal survives the restart the install
// does. Logged rather than returned: a trigger removed from a live engine has stopped
// firing whatever the store says, so the removal has already taken effect — but an
// operator has to know that the rule will come back at the next start.
//
// Deleting an address nothing ever wrote is a clean no-op in the store, which is what lets a
// deliberately non-durable rule take this same removal path without a second one, and without
// a Warn. No durable rule can be caught by it either: a name is unique across the charter.
func (e *Engine) forget(name string) {
	e.write(charter.RuleRef(e.confine.stamp(), name).At(), nil)
}

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
// through the overlay (ownRecord). Baseline's charter running in every live scope is a
// question about what a scope means, and a restore is not where it gets decided.
func (e *Engine) LoadCharter() error {
	// At the charter's own slice. A restore is the one moment the store is largest and
	// nothing else is waiting yet, and this read at the ROOT was 18.8s of staging's 88s
	// startup.
	ents, err := entity.BelowErr(e.store, charter.IsRule, 2, entity.VerseRoot, charter.System)
	// A FAILED READ IS NOT AN EMPTY CHARTER. List answers nil either way, and taking the
	// second reading brings a verse back running NO RULES and saying nothing about it — which
	// under load is exactly when it happens. Reported as the read it is (entity.IsUnread), so
	// the restore asks again — and asked BEFORE anything is installed, so a load that reports
	// it installed nothing and the next call is the same call.
	//
	// THIS read's failure, not the store's last one: at a restore every restored rule's firings
	// read through the same store, so its shared flag condemns a load whose read was fine and
	// clears one whose read failed.
	if err != nil {
		return entity.Unread(fmt.Errorf("the charter could not be read, so nothing was restored from it: %w", err))
	}
	return e.LoadCharterFrom(ents)
}

// LoadCharterFrom is LoadCharter over records already read: the rules among ents that are this
// engine's own. It is how many engines restore from ONE read — restorePackages lists the charter
// once and hands every package's engine the lot, where each reading it for itself read the whole
// charter once per package.
func (e *Engine) LoadCharterFrom(ents []entity.Entity) error {
	var errs []error
	for _, ent := range ents {
		if !charter.IsRule(ent.Ref) || !e.ownRecord(ent) {
			continue
		}
		t, err := ruleOf(ent)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		// A rule already standing under its name is what this load would install: a load
		// asked twice — the second time because the first could not read the strata, say —
		// installs each rule once.
		if _, standing := e.triggerNamed(t.Name); standing {
			continue
		}
		// durable=false: it is already durable — that is where it came from. Re-writing it
		// would be a no-op with a revision attached, and a charter subtree watch would see
		// every restart as a charter change. As whoever owns it, which its record says.
		if err := e.addTrigger(t, false, e.OwnerOf(t.Name)); err != nil {
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
	doc := entity.Get(ent.Payload, charter.SpecField)
	if doc == nil {
		return Trigger{}, fmt.Errorf("charter %s: no %s field (payload is %v)", ent.Ref, charter.SpecField, ent.Payload)
	}
	var t Trigger
	if err := t.FromTonyIR(doc); err != nil {
		return Trigger{}, fmt.Errorf("charter %s: %w", ent.Ref, err)
	}
	t.Task = entity.Str(ent.Payload, charter.TaskField)
	if n := entity.Get(ent.Payload, charter.TaskEnvironmentField); n != nil && t.Task != "" {
		var env systems.Environment
		if err := env.FromTonyIR(n); err != nil {
			return Trigger{}, fmt.Errorf("charter %s: %s: %w", ent.Ref, charter.TaskEnvironmentField, err)
		}
		t.TaskEnvironment = &env
	}
	// The id is the name for baseline and `<address>:<name>` for a package (CharterRefAt), so
	// the check is against the tail: what has to agree is the NAME, and the package half of the
	// address is not part of it.
	if t.Name != charter.Name(ent.Ref.Name()) {
		return Trigger{}, fmt.Errorf("charter %s: the rule names itself %q; a rule is addressed by its "+
			"name, so it would install as one trigger and be removed as another", ent.Ref, t.Name)
	}
	return t, nil
}
