// Package charter is the shape of a charter's records: where a rule, a stratum and a
// package's declaration stand in the verse, what each record holds, and the records as they
// change (Follow). The engine (package trigger) is derived from them: an install is a write
// here, a removal a delete, and what the engine runs is what it reads back.
//
// THE RECORD IS THE RULE. An engine that kept its charter in memory with a durable copy beside
// it would have two that can disagree — a rule standing in the store and in neither of the
// engine's lists, an install landed and not placed — and the disagreement would be somebody's
// to notice. One source, read by everything, is the shape that cannot disagree with itself.
//
// It is one record per rule rather than one charter document, because that is what the
// operations already are: installing writes one record, removing deletes one, and neither
// needs to read the others (no read-modify-write, so two installs cannot lose each other). It
// also makes a rule addressable — `verse data trigger:rule:review` — and a charter change an
// ordinary observable fact.
package charter

import (
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// System and the kinds name where the records live: trigger:rule:<id> for a rule,
// trigger:strata:<id> for a stratum, trigger:package:<address> for a package's declaration.
// A rule's id is its name, which is unique within a charter by construction, so the address
// needs nothing else to be distinct; a package's records carry its address in the id (ID).
const (
	System      = "trigger"
	RuleKind    = "rule"
	StratumKind = "strata"
	PackageKind = "package"
)

// Prefix is the place every charter record stands below — verse's own half of the tree, then
// the system — and Depth the length of a record's path: verse, the system, the kind, the id.
// What a watch over the whole charter is opened on (Follow).
var Prefix = []string{entity.VerseRoot, System}

const Depth = 4

// IsRule, IsStratum and IsPackage are the namespace's own questions, asked by anything that
// lists the charter: whether an address is a rule's, a stratum's or a package declaration's.
func IsRule(r entity.Ref) bool    { return r.ChildOf(entity.VerseRoot, System, RuleKind) }
func IsStratum(r entity.Ref) bool { return r.ChildOf(entity.VerseRoot, System, StratumKind) }
func IsPackage(r entity.Ref) bool { return r.ChildOf(entity.VerseRoot, System, PackageKind) }

// RuleRef names the record holding one rule of the charter installed at an address, and
// StratumRef one stratum; at is "" for baseline.
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
func RuleRef(at, name string) entity.Ref { return entity.VerseRef(System, RuleKind, ID(at, name)) }

func StratumRef(at, name string) entity.Ref {
	return entity.VerseRef(System, StratumKind, ID(at, name))
}

// PackageRef names the record holding one package's declaration: what it reads and where it
// sends. A record of its own, rather than a field stamped on each rule, because the
// declaration is a property of the PACKAGE and not of any rule in it: two installs into one
// address would otherwise each carry their own answer, and the reads graph would depend on
// which rule you read. The id is the address, so a package has exactly one.
func PackageRef(at string) entity.Ref { return entity.VerseRef(System, PackageKind, at) }

// ID is the id a rule's or a stratum's record takes: the name for baseline, `<address>:<name>`
// for a package. One function, so a rule and a stratum cannot drift on the spelling, and the
// one a question's `from` spells a rule by (mirror.RecordID).
func ID(at, name string) string { return mirror.RecordID(at, name) }

// Name is the name inside a record id — the tail after the package address, and the whole id
// for baseline. The split is on the LAST colon: an address may hold none (`drift`) or one
// (`demo.pr` is dotted, so still none), and a name may not hold one, so the last colon is the
// divider whenever there is a package at all.
func Name(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// The fields of a rule's or a stratum's record.
//
// SpecField holds the declaration it was installed from, as STRUCTURE written under
// entity.Raw. A charter is full of operator tags — `id: !glob "hotfix-*"`, `patch: {tmp:
// !delete null}` — and a write is a tony patch, so an unescaped payload has them executed by
// the store rather than stored by it: not a rule that reads back wrong, one that cannot be
// read back at all. `!raw` is the escape: the subtree is stored verbatim, tags intact,
// interpreting nothing at any depth. Structure rather than text because the record is then
// the rule itself: `verse data get trigger:rule:review` shows a document a person can read
// field by field, and a charter change is an observable fact about a shape rather than a
// string. Raw also REPLACES, so re-installing a rule cannot leave a field of the version
// before it standing in the version after.
//
// ScopeField is the scope the record belongs to, absent for baseline: a scoped store reads
// baseline layered underneath, so a listing in a scope returns both, and only the record can
// say which is which. AtField is the ADDRESS the charter was installed at — the subtree its
// rules may write in — absent for baseline, which may write anywhere: the same idea one axis
// over. A scope says whose COPY of the verse a rule acts in; an address says which part of
// the verse it acts on.
//
// TaskField is the task whose tools installed a rule, absent for one a person or the system
// installed, and TaskEnvironmentField what that task could hand on when it did, the systems
// alone. ReadsField is a package declaration's: the dotted addresses its rules may read
// outside their own.
const (
	SpecField            = "spec"
	ScopeField           = "scope"
	AtField              = "at"
	TaskField            = "task"
	TaskEnvironmentField = "taskEnvironment"
	ReadsField           = "reads"
)

// Payload builds a declaration's record: the spec, plus the scope when the charter acts in one
// and the address it was installed at. One builder, so a rule and a stratum cannot drift on
// either. WHOSE it is, is not in it: that is its owner record's to say, and a second statement
// beside it is one that can disagree.
func Payload(spec *ir.Node, scope, at string) entity.Obj {
	p := entity.Obj{SpecField: entity.Raw(spec)}
	if scope != "" {
		p[ScopeField] = scope
	}
	if at != "" {
		p[AtField] = at
	}
	return p
}

// A Record is one rule's or one stratum's record as it stands: where, whose charter — by scope
// and address — and the declaration it holds, which the engine decodes.
type Record struct {
	Ref  entity.Ref
	Kind string // RuleKind or StratumKind
	Name string // the name inside the id (Name)
	// Scope and At are the charter the record belongs to: a scope's own, or baseline's; a
	// package's, by its address, or baseline's.
	Scope, At string
	// Spec is the declaration as it was installed, nil where the record holds none.
	Spec *ir.Node
	// Task is the task whose tools installed a rule, and TaskEnvironment what it could hand on;
	// "" and nil for a person's or the system's.
	Task            string
	TaskEnvironment *ir.Node
	// Rev is the revision the record stood at when read.
	Rev int64
}

// Decode reads a record off its entity. The entity has to be a rule's or a stratum's
// (IsRule, IsStratum); anything else under the prefix is not a record of this kind.
func Decode(ent entity.Entity, rev int64) (Record, error) {
	r := Record{Ref: ent.Ref, Name: Name(ent.Ref.Name()), Rev: rev}
	switch {
	case IsRule(ent.Ref):
		r.Kind = RuleKind
	case IsStratum(ent.Ref):
		r.Kind = StratumKind
	default:
		return r, fmt.Errorf("%s is not a rule's or a stratum's record", ent.Ref)
	}
	r.Scope = entity.Str(ent.Payload, ScopeField)
	r.At = entity.Str(ent.Payload, AtField)
	r.Spec = entity.Get(ent.Payload, SpecField)
	r.Task = entity.Str(ent.Payload, TaskField)
	if r.Task != "" {
		r.TaskEnvironment = entity.Get(ent.Payload, TaskEnvironmentField)
	}
	return r, nil
}

// A Change is one record as the charter's watch delivers it: the record set, or the record
// deleted — then the Ref, Kind and Name alone say which, with Rev the deletion's.
type Change struct {
	Record
	Deleted bool
}

// Follow opens the watch on every charter record of the store: one handle, answered at once
// (entity.Store.Watch), whose stream is the records as they change, the opening picture first.
// Opened ahead of the store's line, as the charter's restore should be: a progress consumer,
// so that Progress can ask how far current the stream is (Rev on each delivery, and a mark
// when asked).
func Follow(store entity.Store) (*entity.Watch, error) {
	return store.Watch(Prefix, entity.PrefixPattern(Prefix, Depth), entity.WatchOptions{WaitIfAbsent: true, Progress: true})
}

// Changes is one commit of the follow as records: each rule's or stratum's set or deletion at
// the commit, in the order delivered; a package declaration or anything else under the prefix
// is not one. A record that does not decode is skipped here and left to the engine, which
// reads the entity itself when it is placed.
func Changes(c entity.Commit) []Change {
	var out []Change
	for _, ev := range c.Changes {
		if !IsRule(ev.Ref) && !IsStratum(ev.Ref) {
			continue
		}
		if ev.Op == entity.Delete {
			kind := RuleKind
			if IsStratum(ev.Ref) {
				kind = StratumKind
			}
			out = append(out, Change{Record: Record{Ref: ev.Ref, Kind: kind, Name: Name(ev.Ref.Name()), Rev: c.Rev}, Deleted: true})
			continue
		}
		r, err := Decode(entity.Entity{Ref: ev.Ref, Payload: ev.To}, c.Rev)
		if err != nil {
			continue
		}
		out = append(out, Change{Record: r})
	}
	return out
}
