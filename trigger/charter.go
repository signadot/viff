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
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// CharterSystem/CharterKind name where the durable charter lives: trigger:rule:<name>, the
// id being the trigger's name (which is unique by construction — AddTrigger refuses a
// duplicate — so the address needs nothing else to be distinct).
const (
	CharterSystem = "trigger"
	CharterKind   = "rule"
)

// IsCharterRule reports whether an address is a durable rule's — the namespace's own question,
// asked by anything that lists the charter.
func IsCharterRule(r entity.Ref) bool {
	return r.ChildOf(entity.VerseRoot, CharterSystem, CharterKind)
}

// CharterRef names the entity holding one of BASELINE's rules.
func CharterRef(name string) entity.Ref { return CharterRefAt("", name) }

// CharterRefAt names the entity holding one rule of the charter installed at an address.
//
// A PACKAGE'S RECORDS ARE ADDRESSED BY PACKAGE — `verse:trigger:rule:drift:head-moved` — and
// baseline's keep the bare name. Without that the id is the rule's name alone, so two packages
// with a rule (or a stratum) of the same name write one record: the second install silently
// replaces the first's, both engines keep running from memory, and the restart is where one
// package's rules quietly do not come back. Two groups not having to agree on names is most of
// what a package is FOR, so the address is what has to carry the package.
//
// The package goes FIRST, so a listing sorted by address groups a package's rules together —
// which is the order somebody reading `verse data verse.trigger.rule` wants. The id may hold
// colons (ParseRef splits the first two and the rest is the id), so nothing else has to change.
func CharterRefAt(at, name string) entity.Ref {
	return entity.VerseRef(CharterSystem, CharterKind, recordID(at, name))
}

// RecordName is recordName for a reader outside the package — the server lists a charter by the
// names its records carry, and the package half of the id is not part of a rule's name.
func RecordName(id string) string { return recordName(id) }

// recordName is the name inside a record id — the tail after the package address, and the whole
// id for baseline. The split is on the LAST colon: an address may hold none (`drift`) or one
// (`demo.pr` is dotted, so still none), and a rule's name may not hold one, so the last colon
// is the divider whenever there is a package at all.
func recordName(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// recordID is the id a charter record takes: the name for baseline, `<address>:<name>` for a
// package. One function, so a rule and a stratum cannot drift on the spelling.
func recordID(at, name string) string { return mirror.RecordID(at, name) }

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
// Structure rather than text because the record is then the rule itself: `verse data get
// trigger:rule:review` shows a document a person can read field by field, a charter change is
// an ordinary observable fact about a shape rather than about a string, and nothing has to
// re-parse to ask a question about it. Raw also REPLACES, so re-installing a rule cannot
// leave a field of the version before it standing in the version after — which a merged
// structural write would, and which the text form got for free by being one scalar.
const specField = "spec"

// scopeField is the scope a record belongs to — absent for baseline. It is what makes a
// scope's charter restorable: a scoped store reads baseline layered underneath, so a listing
// in a scope returns both, and only the record can say which is which. See Engine.SetScope.
const scopeField = "scope"

// atField is the ADDRESS a charter was installed at — the subtree its rules may write in,
// absent for the baseline charter, which may write anywhere.
//
// It is the same idea as `scope` one axis over. A scope says whose COPY of the verse a rule
// acts in; an address says which part of the verse it acts on. Both are stamped for the same
// reason: the records live in one listing, and only the record can say which charter a rule
// belongs to.
const atField = "at"

// AtField is atField for a reader outside the package — the server groups the charter by it to
// bring every package back at a restart.
const AtField = atField

// ownRecord reports whether a charter record belongs to THIS engine's scope: the one
// stamped on it is the scope the engine acts in.
//
// A record with no scope is baseline's. Read from a scope that is what an INHERITED rule looks
// like, and skipping it is what keeps a restore from installing the whole baseline
// charter into one scope — which is a decision about what a scope means, not a
// restore detail, and is not this function's to make (docs/reference/scopes.md).
func (e *Engine) ownRecord(ent entity.Entity) bool {
	return entity.Str(ent.Payload, scopeField) == e.scope &&
		entity.Str(ent.Payload, atField) == e.confine.stamp()
}

// record builds a declaration's durable payload: the spec, plus the scope when this engine
// acts in one and the address it was installed at. One builder, so a rule and a stratum cannot
// drift on either. WHOSE it is, is not in it: that is its owner record's to say, and a second
// statement beside it is one that can disagree.
func (e *Engine) record(doc *ir.Node) entity.Payload {
	p := entity.Obj{specField: entity.Raw(doc)}
	if e.scope != "" {
		p[scopeField] = e.scope
	}
	if at := e.confine.stamp(); at != "" {
		p[atField] = at
	}
	return p.Payload()
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
func (e *Engine) RuleRef(name string) entity.Ref { return CharterRefAt(e.confine.stamp(), name) }

func (e *Engine) StratumRef(name string) entity.Ref { return StrataRefAt(e.confine.stamp(), name) }

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
	ref := CharterRefAt(e.confine.stamp(), t.Name)
	if _, _, err := e.store.Write(entity.Write{
		At:        ref.At(),
		Value:     e.record(doc),
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
	e.write(CharterRefAt(e.confine.stamp(), name).At(), nil)
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
	ents, err := entity.BelowErr(e.store, IsCharterRule, 2, entity.VerseRoot, CharterSystem)
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
		if !IsCharterRule(ent.Ref) || !e.ownRecord(ent) {
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
		if err := e.addTrigger(t, false, e.OwnerOf(t.Name), false); err != nil {
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
	// The id is the name for baseline and `<address>:<name>` for a package (CharterRefAt), so
	// the check is against the tail: what has to agree is the NAME, and the package half of the
	// address is not part of it.
	if t.Name != recordName(ent.Ref.Name()) {
		return Trigger{}, fmt.Errorf("charter %s: the rule names itself %q; a rule is addressed by its "+
			"name, so it would install as one trigger and be removed as another", ent.Ref, t.Name)
	}
	return t, nil
}
