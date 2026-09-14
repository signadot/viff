package trigger

// A SUSPENSION is one record naming a charter (or every charter), a rule of it (or all), and a
// subject (or every subject), and it covers the work about that subject in every form: a rule
// starts no firing about it, a task about it holds, a question about it is withdrawn, a hook's
// launch is parked. docs/working/suspend.md is the design; this file is the record and the
// table an engine asks.
//
// The table is one per scope and written by one writer — the route that also writes the store,
// in the same order — so it is never fed by a watch that could race that writer.

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// SuspensionKind is where the records live: verse.trigger.suspension.<id>.
const SuspensionKind = "suspension"

// Every is the wildcard a suspension spells for "every charter", "every rule" and "every
// subject", and the trailing segment of an address that covers everything beneath it.
const Every = "*"

// SuspensionRef names the record of one suspension.
func SuspensionRef(id string) entity.Ref {
	return entity.VerseRef(CharterSystem, SuspensionKind, id)
}

// IsSuspension reports whether an address is a suspension's record.
func IsSuspension(r entity.Ref) bool {
	return r.Reserved() && r.System() == CharterSystem && r.Kind() == SuspensionKind
}

// Suspension is one record: what it covers, the scope it was made in, and who holds it.
type Suspension struct {
	// Charter is an installed charter's address, "" for baseline's, Every for every charter,
	// or an address ending `.*` for every charter beneath it.
	Charter string
	// Rule is a rule's name in the charter, or Every.
	Rule string
	// Subject is an address, Every, or an address ending `.*` for everything beneath it.
	Subject string
	// Scope is the scope the suspension was made in, "" for baseline, as a rule record's.
	Scope string
	// Held is who holds it, by principal. A suspension stands while anybody does.
	Held map[string]Holder

	charter, subject     []string // parsed, without the trailing wildcard
	charterAll, charterW bool     // Every; beneath charter
	subjectAll, subjectW bool     // Every; beneath subject
}

// Holder is one party's hold on a suspension: why, since when, and the store revision the hold
// was written at, which is what the listing measures movement under the cover from.
type Holder struct {
	Why        string
	Since      string
	Occurrence int64
}

// NewSuspension reads the three parts of a suspension into their one spelling, so two spellings
// of one address are one record: addresses are parsed and rendered by segment, never compared as
// strings, since a string comparison would let a respelling fail open.
func NewSuspension(charter, rule, subject string) (Suspension, error) {
	s := Suspension{}
	switch {
	case charter == Every:
		s.Charter, s.charterAll = Every, true
	case charter == "":
	default:
		addr, wild := strings.CutSuffix(charter, "."+Every)
		st, err := ParseAt(addr)
		if err != nil {
			return Suspension{}, fmt.Errorf("charter %q: %w", charter, err)
		}
		if strings.Contains(addr, Every) {
			return Suspension{}, fmt.Errorf("charter %q: only a trailing .* is a wildcard — a "+
				"wildcard inside an address names no place whose owner could stand behind it", charter)
		}
		s.charter, s.charterW = st.Path(), wild
		s.Charter = strings.Join(s.charter, ".")
		if wild {
			s.Charter += "." + Every
		}
	}
	switch rule {
	case "", Every:
		s.Rule = Every
	default:
		if err := validTriggerName(rule); err != nil {
			return Suspension{}, fmt.Errorf("rule: %w", err)
		}
		s.Rule = rule
	}
	switch {
	case subject == "" || subject == Every:
		s.Subject, s.subjectAll = Every, true
	default:
		addr, wild := strings.CutSuffix(subject, "."+Every)
		segs, err := entity.PathSegs(addr)
		if err != nil {
			return Suspension{}, fmt.Errorf("subject %q: %w", subject, err)
		}
		if len(segs) == 0 {
			return Suspension{}, fmt.Errorf("subject %q names no address", subject)
		}
		s.subject, s.subjectW = segs, wild
		s.Subject = entity.JoinSegs(segs)
		if wild {
			s.Subject += "." + Every
		}
	}
	return s, nil
}

// ID is the record's id: `<charter>:<rule>@<subject>`, the spelling a task id shares
// (ownerOf), baseline's charter being the empty one.
func (s Suspension) ID() string { return s.Charter + AtSep + s.Rule + ChainSep + s.Subject }

// Payload is the record as it is written, holders included.
func (s Suspension) Payload() entity.Payload {
	p := entity.Obj{"charter": s.Charter, "rule": s.Rule, "subject": s.Subject}
	if s.Scope != "" {
		p[scopeField] = s.Scope
	}
	held := entity.Obj{}
	for who, h := range s.Held {
		held[who] = h.payload()
	}
	p["held"] = held
	return p.Payload()
}

func (h Holder) payload() entity.Payload {
	o := entity.Obj{"why": h.Why, "since": h.Since}
	if h.Occurrence != 0 {
		o["occurrence"] = h.Occurrence
	}
	return o.Payload()
}

// HolderPayload is one holder as the record carries it under `held.<principal>`.
func HolderPayload(h Holder) entity.Payload { return h.payload() }

// SuspensionOf reads a record back.
func SuspensionOf(p entity.Payload) (Suspension, error) {
	s, err := NewSuspension(entity.Str(p, "charter"), entity.Str(p, "rule"), entity.Str(p, "subject"))
	if err != nil {
		return Suspension{}, err
	}
	s.Scope = entity.Str(p, scopeField)
	s.Held = map[string]Holder{}
	for who, h := range entity.Fields(entity.Get(p, "held")) {
		s.Held[who] = Holder{Why: entity.Str(h, "why"), Since: entity.Str(h, "since"),
			Occurrence: mirror.OccurrenceOf(h)}
	}
	return s, nil
}

// Holders is who holds it, sorted.
func (s Suspension) Holders() []string {
	out := make([]string, 0, len(s.Held))
	for who := range s.Held {
		out = append(out, who)
	}
	sort.Strings(out)
	return out
}

// CoversCharter reports whether the suspension names the charter at this address (none for
// baseline's) and the rule by this name in it. A rule of "" — work no rule started, a task run
// by hand — is named only by a suspension of every rule of every charter.
func (s Suspension) CoversCharter(at []string, rule string) bool {
	if rule == "" {
		return s.charterAll && s.Rule == Every
	}
	if s.Rule != Every && s.Rule != rule {
		return false
	}
	switch {
	case s.charterAll:
		return true
	case s.charterW:
		return beneath(s.charter, at)
	default:
		return segsEqual(s.charter, at)
	}
}

// CoversSubject reports whether the suspension names this subject. Work about no subject is
// named only by a suspension of every subject.
func (s Suspension) CoversSubject(subj entity.Ref) bool {
	if s.subjectAll {
		return true
	}
	if subj == (entity.Ref{}) {
		return false
	}
	segs := subj.Segs()
	if s.subjectW {
		return beneath(s.subject, segs)
	}
	return segsEqual(s.subject, segs)
}

// EverySubject reports whether it names every subject — what a whole rule being held means.
func (s Suspension) EverySubject() bool { return s.subjectAll }

// EveryCharter reports whether it names every charter.
func (s Suspension) EveryCharter() bool { return s.charterAll }

// CharterPlace is the node control over the charter side is asked at, when the suspension names
// one: a package's address (its prefix, for a wildcard), or a baseline rule's own record. A
// suspension of every charter, or of every rule of baseline's, names none.
func (s Suspension) CharterPlace() (entity.Ref, bool) {
	switch {
	case s.charterAll:
		return entity.Ref{}, false
	case len(s.charter) > 0:
		return entity.RefAt(s.charter...), true
	case s.Rule != Every:
		return CharterRef(s.Rule), true
	}
	return entity.Ref{}, false
}

// SubjectPlace is the node control over the subject side is asked at, when the suspension names
// one: the subject, or the prefix a wildcard stands beneath.
func (s Suspension) SubjectPlace() (entity.Ref, bool) {
	if s.subjectAll {
		return entity.Ref{}, false
	}
	return entity.RefAt(s.subject...), true
}

// Within reports whether everything s covers, o covers too: what a lift says when the pair it
// lifted is still held by something wider.
func (s Suspension) Within(o Suspension) bool {
	switch {
	case o.charterAll:
	case s.charterAll:
		return false
	case o.charterW:
		if !(beneath(o.charter, s.charter) || (s.charterW && segsEqual(o.charter, s.charter))) {
			return false
		}
	default:
		if s.charterW || !segsEqual(o.charter, s.charter) {
			return false
		}
	}
	if o.Rule != Every && o.Rule != s.Rule {
		return false
	}
	switch {
	case o.subjectAll:
		return true
	case s.subjectAll:
		return false
	case o.subjectW:
		return beneath(o.subject, s.subject) || (s.subjectW && segsEqual(o.subject, s.subject))
	default:
		return !s.subjectW && segsEqual(o.subject, s.subject)
	}
}

// beneath reports whether segs stands strictly beneath prefix.
func beneath(prefix, segs []string) bool {
	if len(segs) <= len(prefix) {
		return false
	}
	return segsEqual(prefix, segs[:len(prefix)])
}

func segsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Suspensions is one scope's table: the suspensions in force, held in memory and written only by
// the route that writes their records. Until it has loaded it answers suspended for everything,
// since a table that failed open on a restart would resume what somebody stopped on purpose.
type Suspensions struct {
	mu      sync.RWMutex
	loaded  bool
	entries map[string]Suspension
	engines []*Engine
}

// NewSuspensions is an empty table that has not loaded.
func NewSuspensions() *Suspensions {
	return &Suspensions{entries: map[string]Suspension{}}
}

// SetSuspensions gives the engine its scope's table, which it asks wherever work would make
// progress. An engine with none suspends nothing.
func (e *Engine) SetSuspensions(t *Suspensions) {
	e.susp = t
	if t == nil {
		return
	}
	t.mu.Lock()
	t.engines = append(t.engines, e)
	t.mu.Unlock()
}

// Detach drops an engine from the table, when the engine stops.
func (t *Suspensions) Detach(e *Engine) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, x := range t.engines {
		if x == e {
			t.engines = append(t.engines[:i], t.engines[i+1:]...)
			return
		}
	}
}

// Load fills the table from the records its scope's store holds, and marks it loaded.
func (t *Suspensions) Load(records []Suspension) {
	t.mu.Lock()
	for _, s := range records {
		t.entries[s.ID()] = s
	}
	t.loaded = true
	t.mu.Unlock()
}

// Loaded reports whether the table has loaded.
func (t *Suspensions) Loaded() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.loaded
}

// Get is one suspension by id.
func (t *Suspensions) Get(id string) (Suspension, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s, ok := t.entries[id]
	return s, ok
}

// List is every suspension in force, by id.
func (t *Suspensions) List() []Suspension {
	t.mu.RLock()
	out := make([]Suspension, 0, len(t.entries))
	for _, s := range t.entries {
		out = append(out, s)
	}
	t.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// Put records a suspension — new, or with its holders changed — once its record is written.
func (t *Suspensions) Put(s Suspension) {
	t.mu.Lock()
	t.entries[s.ID()] = s
	t.mu.Unlock()
}

// Remove drops a suspension once its record is deleted.
func (t *Suspensions) Remove(id string) {
	t.mu.Lock()
	delete(t.entries, id)
	t.mu.Unlock()
}
