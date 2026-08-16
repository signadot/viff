package trigger

// A stratum is one level of the charter's declared ordering. Rules live in one; a rule's
// SUBJECT must fall inside its stratum's pattern, and an action's DERIVE must fall inside some
// strictly higher stratum's. Those two constraints are the whole of the level discipline, and
// both are answered at install without reading the store.
//
// The ordering is declared rather than inferred. Inferring it would mean building a graph over
// triggers — decidable for a literal patch, best-effort through a template — and best-effort is
// the wrong shape for a guarantee: an edge a template hid is an edge the check silently does not
// have. Declared, a cross-level write is ruled out by construction, so there is nothing for a
// template to hide. See docs/strata.md.
//
// WHAT THIS IS NOT. It is not a convergence guarantee and cannot be made into one: recursion
// INSIDE a stratum is exactly what a level ordering permits, which is stratified Datalog's
// bargain. A cycle among a stratum's own rules is caught at runtime by the causal-depth throttle
// (causal.go), as it was before strata existed.
//
// The durable shape follows the charter's: one entity per stratum, trigger:strata:<name>, for
// the reasons one-entity-per-rule already turns on (charter.go). The NAME addresses and the
// LEVEL orders — a rule says `stratum: sdlc`, which reads as what it means where `10` does not
// — and because the level is then an ordinary field rather than an address, two strata sharing
// one has to be refused explicitly. It is refused: the number is the whole ordering, and a
// charter that gives it twice has not said which comes first.

import (
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
)

// StrataSystem/StrataKind name where the durable strata live: trigger:strata:<name>, beside
// the rules at trigger:rule:<name>. The id is the stratum's name, which is what makes a
// duplicate NAME impossible by construction and a duplicate LEVEL a thing to check.
const (
	StrataSystem = "trigger"
	StrataKind   = "strata"
)

// StrataRef names the entity holding one stratum.
func StrataRef(name string) entity.Ref {
	return entity.VerseRef(StrataSystem, StrataKind, name)
}

// Stratum is one level of the ordering: what it owns, and where it sits.
//
// The codec is hand-written for the reason tonycodec.go gives for Match's: `match` accepts one
// pattern bare or a union as a list, and a generator that has to be told a field takes two
// shapes is being told to write the codec anyway.
//
//tony:schemagen=verse-stratum,notag,codec=custom
type Stratum struct {
	Name  string  `tony:"field=name"`
	Level int     `tony:"field=level"`
	Match []Match `tony:"field=match"`
}

// Matches is a UNION of match patterns — "this stratum owns any of these".
//
// It is a list rather than a Match carrying an operator because a Match cannot carry one at the
// root: `system` and `kind` are refused an operator on purpose (they become a watch path and a
// store prefilter, and neither can be a predicate — see Match.Validate), and a stratum spanning
// five systems is the ordinary case rather than the exotic one. A single pattern may be written
// bare; the list form is the general one.
type Matches []Match

// ToTonyIR writes a single pattern bare and a union as a list, so a round trip gives back what
// was written rather than expanding one match into a list of one.
func (ms Matches) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	if len(ms) == 0 {
		return ir.Null(), nil
	}
	if len(ms) == 1 {
		return ms[0].ToTonyIR(opts...)
	}
	out := make([]*ir.Node, len(ms))
	for i, m := range ms {
		n, err := m.ToTonyIR(opts...)
		if err != nil {
			return nil, fmt.Errorf("match[%d]: %w", i, err)
		}
		out[i] = n
	}
	return ir.FromSlice(out), nil
}

// FromTonyIR accepts either spelling: one match (an address string or the object form), or a
// list of them.
func (ms *Matches) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = unwrap(node)
	if node == nil || node.Type == ir.NullType {
		*ms = nil
		return nil
	}
	if node.Type == ir.ArrayType {
		out := make(Matches, 0, len(node.Values))
		for i, v := range node.Values {
			var m Match
			if err := m.FromTonyIR(v, opts...); err != nil {
				return fmt.Errorf("match[%d]: %w", i, err)
			}
			out = append(out, m)
		}
		*ms = out
		return nil
	}
	var m Match
	if err := m.FromTonyIR(node, opts...); err != nil {
		return err
	}
	*ms = Matches{m}
	return nil
}

// ToTonyIR writes the stratum as `{name, level, match}`.
func (s *Stratum) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	if s == nil {
		return ir.Null(), nil
	}
	m, err := Matches(s.Match).ToTonyIR(opts...)
	if err != nil {
		return nil, fmt.Errorf("stratum %q: %w", s.Name, err)
	}
	return ir.FromMap(map[string]*ir.Node{
		"name":  ir.FromString(s.Name),
		"level": ir.FromInt(int64(s.Level)),
		"match": m,
	}), nil
}

// FromTonyIR reads it back.
func (s *Stratum) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = unwrap(node)
	if node == nil || node.Type == ir.NullType {
		return nil
	}
	if node.Type != ir.ObjectType {
		return fmt.Errorf("stratum: want an object of {name, level, match}, got %v", node.Type)
	}
	for i, field := range node.Fields {
		val := unwrap(node.Values[i])
		switch field.String {
		case "name":
			if val.Type != ir.StringType {
				return fmt.Errorf("stratum: %q must be a string, got %v", "name", val.Type)
			}
			s.Name = val.String
		case "level":
			// An INTEGER, not any number: 1.5 between two levels is a plausible thing to try
			// and would order fine, but the level is also what a person reads as the charter's
			// spine, and "level 1.5" is a renumbering nobody did on purpose.
			if val.Type != ir.NumberType || val.Int64 == nil {
				return fmt.Errorf("stratum %q: %q must be an integer, got %v", s.Name, "level", val.Type)
			}
			s.Level = int(*val.Int64)
		case "match":
			if err := (*Matches)(&s.Match).FromTonyIR(node.Values[i], opts...); err != nil {
				return fmt.Errorf("stratum %q: %w", s.Name, err)
			}
		default:
			// Named rather than ignored. A stratum has three fields and a misspelling of any
			// of them is silent otherwise — `levle: 10` would install at level 0, which is a
			// different ordering that nothing would report.
			return fmt.Errorf("stratum: unknown field %q (want name, level, match)", field.String)
		}
	}
	return nil
}

// contains reports whether every entity the inner match can pick is one this union owns.
//
// CONTAINMENT, not intersection, and the difference is the whole point of check #1: a rule whose
// subject is `{kind: pr}` selects prs in every system, so a stratum owning only `gh` does not
// contain it even though the two plainly overlap. Answering "do they overlap" there would admit
// a rule that acts outside its own level.
func (ms Matches) contains(inner Match) bool {
	for _, m := range ms {
		if matchContains(m, inner) {
			return true
		}
	}
	return false
}

// overlaps reports whether any entity could satisfy both unions — the disjointness check, read
// positively.
func (ms Matches) overlaps(other []Match) bool {
	for _, a := range ms {
		for _, b := range other {
			if matchOverlap(a, b) {
				return true
			}
		}
	}
	return false
}

// matchContains reports whether outer picks everything inner can.
//
// Only system and kind are consulted, which is exactly why a stratum's match is restricted to
// those two (validate, below). An unset dimension on the outer is a wildcard and contains
// anything; an unset dimension on the INNER is the case that must fail, since "any system"
// is not contained by "system gh".
func matchContains(outer, inner Match) bool {
	if s := outer.System(); s != "" && s != inner.System() {
		return false
	}
	if k := outer.Kind(); k != "" && k != inner.Kind() {
		return false
	}
	return true
}

// matchOverlap reports whether some ref could satisfy both. Unset is a wildcard on either side,
// so it overlaps everything at that dimension.
func matchOverlap(a, b Match) bool {
	if x, y := a.System(), b.System(); x != "" && y != "" && x != y {
		return false
	}
	if x, y := a.Kind(), b.Kind(); x != "" && y != "" && x != y {
		return false
	}
	return true
}

// validate reports whether the stratum is installable on its own terms. What it does not check
// is anything about the OTHER strata — a duplicate level, an overlap — because those are
// properties of the set and belong to addStratum.
func (s Stratum) validate() error {
	if err := validTriggerName(s.Name); err != nil {
		return fmt.Errorf("stratum name: %w", err)
	}
	if s.Level < 0 {
		return fmt.Errorf("stratum %q has level %d: a level orders, and a negative one buys "+
			"nothing an insertion above zero does not", s.Name, s.Level)
	}
	if len(s.Match) == 0 {
		return fmt.Errorf("stratum %q has no match: a stratum's pattern says both what subjects "+
			"it owns and what derivations it accepts from below, so a stratum without one owns "+
			"nothing and accepts nothing", s.Name)
	}
	for i, m := range s.Match {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("stratum %q match[%d]: %w", s.Name, i, err)
		}
		// A stratum's pattern is a SYSTEM-AND-KIND pattern, and this is load-bearing rather
		// than tidy. Check #2 asks whether a derive's target lands in some higher stratum, and
		// at install the target's ref is {system, kind, id: engine-generated} — the id is a
		// function of (rule, subject), and neither is known until fire time. A stratum pattern
		// that constrained the id would make that question undecidable at exactly the moment
		// the whole proposal is bought for. Disjointness has the same shape: over two segments
		// it is a comparison, over payload predicates it is not decidable at all.
		if m.ID() != "" || m.dim(mKeyID) != nil {
			return fmt.Errorf("stratum %q match[%d] constrains `id`: a stratum's pattern may "+
				"name a system and a kind and nothing below, because a derived entity's id is "+
				"the engine's (a function of rule and subject) and is not known until the rule "+
				"fires — a pattern that read it could not be checked at install", s.Name, i)
		}
		if m.HasPredicate() {
			return fmt.Errorf("stratum %q match[%d] carries a payload predicate: a stratum owns "+
				"ADDRESSES, not states. What an entity looks like changes as rules fire, so a "+
				"level that depended on it would not be a level", s.Name, i)
		}
	}
	// Within one stratum, two patterns that overlap are harmless (a union is a union), so
	// nothing is checked here beyond each pattern standing on its own.
	return nil
}

// Strata returns the installed strata, ordered by level. A copy, so a caller cannot mutate the
// engine's ordering by holding the slice.
func (e *Engine) Strata() []Stratum {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	out := append([]Stratum(nil), e.strata...)
	sort.Slice(out, func(i, j int) bool { return out[i].Level < out[j].Level })
	return out
}

// ErrNoStratum marks a removal naming one that is not installed — told apart from a removal
// REFUSED because rules still name it, which is a different answer to a different question.
var ErrNoStratum = errors.New("no such stratum")

// Stratum returns one installed stratum by name.
func (e *Engine) Stratum(name string) (Stratum, bool) { return e.stratum(name) }

// stratum looks one up by name.
func (e *Engine) stratum(name string) (Stratum, bool) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	for _, s := range e.strata {
		if s.Name == name {
			return s, true
		}
	}
	return Stratum{}, false
}

// AddStratum installs a stratum — durably, as its trigger:strata:<name> entity.
func (e *Engine) AddStratum(s Stratum) error { return e.addStratum(s, true) }

// addStratum installs a stratum, writing it to the store when durable.
//
// The two set-properties live here rather than in validate because neither is a property of the
// stratum being installed: both are about the ordering it is joining.
func (e *Engine) addStratum(s Stratum, durable bool) error {
	if err := s.validate(); err != nil {
		return err
	}
	e.trigMu.Lock()
	for _, x := range e.strata {
		if x.Name == s.Name {
			e.trigMu.Unlock()
			return fmt.Errorf("stratum %q already exists (remove it first)", s.Name)
		}
		// The level is the ordering, and two strata holding one have not said which comes
		// first. Refused rather than resolved: neither answer is the author's, and "strictly
		// higher" stops being a question with one answer the moment it is ambiguous.
		if x.Level == s.Level {
			e.trigMu.Unlock()
			return fmt.Errorf("stratum %q wants level %d, which %q already holds: the level IS "+
				"the ordering, so two strata at one level do not say which comes first. Give it "+
				"its own (the gaps are there to be used)", s.Name, s.Level, x.Name)
		}
		// Without disjointness the ordering is vacuous: an entity landing in the intersection
		// of two strata is both above and at, and a derive into it has two answers.
		if Matches(x.Match).overlaps(s.Match) {
			e.trigMu.Unlock()
			return fmt.Errorf("stratum %q overlaps %q: an entity matching both would be at two "+
				"levels at once, and \"strictly higher\" would have two answers for it. Strata "+
				"must be pairwise disjoint", s.Name, x.Name)
		}
	}
	e.strata = append(e.strata, s)
	e.trigMu.Unlock()
	if durable {
		if err := e.persistStratum(s); err != nil {
			e.removeStratum(s.Name)
			return err
		}
	}
	// A rule installed before its stratum was held rather than refused; this is what it was
	// waiting for.
	e.promote(s.Name)
	return nil
}

// RemoveStratum removes a stratum by name. It is REFUSED while any installed rule names it.
//
// Not symmetry with anything — a rule can be removed freely — but because the alternative is
// silent: those rules passed check #1 against a pattern that would no longer exist, and nothing
// re-runs a check when its subject disappears. They would go on firing, unstratified, having
// been told they were ordered. That is a guarantee withdrawn without saying so, which is worse
// than an error the operator can answer by removing the rules first.
func (e *Engine) RemoveStratum(name string) error {
	e.trigMu.Lock()
	var users []string
	for _, t := range e.triggers {
		if t.Stratum == name {
			users = append(users, t.Name)
		}
	}
	found := false
	for _, s := range e.strata {
		if s.Name == name {
			found = true
			break
		}
	}
	e.trigMu.Unlock()
	if !found {
		return fmt.Errorf("%w: %q", ErrNoStratum, name)
	}
	if len(users) > 0 {
		sort.Strings(users)
		shown := users
		if len(shown) > 3 {
			shown = shown[:3]
		}
		return fmt.Errorf("stratum %q still holds %d rule(s) (%v): removing it would leave them "+
			"installed and unchecked — every subject that passed its pattern would be answering "+
			"to nothing. Remove the rules first, or move them", name, len(users), shown)
	}
	e.removeStratum(name)
	e.write(StrataRef(name).At(), nil)
	return nil
}

// removeStratum drops it from the in-memory ordering. Split out so addStratum can undo itself
// when the write fails.
func (e *Engine) removeStratum(name string) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	out := e.strata[:0]
	for _, s := range e.strata {
		if s.Name != name {
			out = append(out, s)
		}
	}
	e.strata = out
}

// nameTaken reports a name already in use, by an installed rule or a held one. Called with
// trigMu HELD, so the check and the append that follows it are one step: two concurrent installs
// of a name must not both find it free.
func (e *Engine) nameTaken(name string) error {
	for _, existing := range e.triggers {
		if existing.Name == name {
			return fmt.Errorf("trigger %q already exists (remove it first)", name)
		}
	}
	for _, held := range e.pending {
		if held.Name == name {
			return fmt.Errorf("trigger %q is already held, waiting for stratum %q (remove it "+
				"first)", name, held.Stratum)
		}
	}
	return nil
}

// unhold drops a held rule, by name.
func (e *Engine) unhold(name string) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	out := e.pending[:0]
	for _, t := range e.pending {
		if t.Name != name {
			out = append(out, t)
		}
	}
	e.pending = out
}

// promote installs the rules that were held for this stratum.
//
// Collected under the lock and installed OUTSIDE it: addTrigger takes trigMu itself, and a
// promotion running under the caller's lock would deadlock on the first rule.
func (e *Engine) promote(stratum string) {
	e.trigMu.Lock()
	var ready []Trigger
	held := e.pending[:0]
	for _, t := range e.pending {
		if t.Stratum == stratum {
			ready = append(ready, t)
			continue
		}
		held = append(held, t)
	}
	e.pending = held
	e.trigMu.Unlock()
	for _, t := range ready {
		// durable=false: it was written when it was held. A rule that fails its checks NOW —
		// its subject is outside the stratum that just arrived — is reported and dropped from
		// memory; its entity stays, so the next restart says the same thing rather than
		// quietly forgetting a rule someone wrote.
		if err := e.addTrigger(t, false); err != nil {
			e.logger.Warn("held rule does not fit the stratum it named",
				"trigger", t.Name, "stratum", stratum, "err", err)
			continue
		}
		e.logger.Info("held rule installed: its stratum arrived", "trigger", t.Name, "stratum", stratum)
	}
}

// persistStratum writes s into the verse as its stratum entity. Raw for the same reason a rule
// is raw: a match is full of operator tags, and an unescaped write has the store execute them
// rather than store them (see charter.go's specField).
func (e *Engine) persistStratum(s Stratum) error {
	doc, err := s.ToTonyIR()
	if err != nil {
		return fmt.Errorf("stratum %q: encoding it: %w", s.Name, err)
	}
	ref := StrataRef(s.Name)
	if _, _, err := e.store.Write(entity.Write{
		At:    ref.At(),
		Value: entity.Obj{specField: entity.Raw(doc)}.Payload(),
	}); err != nil {
		return fmt.Errorf("%w: stratum %q: writing %s: %w", ErrCharter, s.Name, ref, err)
	}
	return nil
}

// LoadStrata installs the strata standing in the verse. Called at startup BEFORE LoadCharter,
// because a rule names its stratum and the ordering has to be there to name.
//
// A stratum that cannot be decoded or installed is reported and skipped, as a rule is: the rest
// of the ordering is still worth having, and the rules that named the broken one are then held
// rather than lost.
func (e *Engine) LoadStrata() error {
	var errs []error
	for _, ent := range e.store.List(func(r entity.Ref) bool {
		return r.Reserved() && r.System() == StrataSystem && r.Kind() == StrataKind
	}) {
		s, err := stratumOf(ent)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := e.addStratum(s, false); err != nil {
			errs = append(errs, fmt.Errorf("stratum %s: %w", ent.Ref, err))
		}
	}
	return errors.Join(errs...)
}

// stratumOf decodes one stratum entity. A name disagreeing with the address is refused for the
// reason ruleOf gives: the name is what a rule references and the address is what a removal
// deletes, so the two diverging leaves a stratum nothing can get rid of.
func stratumOf(ent entity.Entity) (Stratum, error) {
	doc := entity.Get(ent.Payload, specField)
	if doc == nil {
		return Stratum{}, fmt.Errorf("stratum %s: no %s field (payload is %v)", ent.Ref, specField, ent.Payload)
	}
	var s Stratum
	if err := s.FromTonyIR(doc); err != nil {
		return Stratum{}, fmt.Errorf("stratum %s: %w", ent.Ref, err)
	}
	if s.Name != ent.Ref.ID() {
		return Stratum{}, fmt.Errorf("stratum %s: it names itself %q; a stratum is addressed by "+
			"its name, so it would install as one and be removed as another", ent.Ref, s.Name)
	}
	return s, nil
}

// checkLevels is the level discipline at install: check #1 over the rule's subject, and check #2
// over every derive in its action tree.
//
// It returns (installed, error). Not installed and no error is the DORMANT case — the rule names
// a stratum that is not here yet — which is held rather than refused so that the order of two
// independent installs does not matter. That is the property one-entity-per-rule exists to
// protect, and refusing here would spend it.
func (e *Engine) checkLevels(t Trigger) (bool, error) {
	if t.Stratum == "" {
		// The implicit stratum: no pattern, so check #1 is vacuous, which is exactly today's
		// semantics for a charter that declares no strata.
		//
		// A derive from here is REFUSED, though, and this is the one place the implicit stratum
		// is not simply permissive. Check #2 wants a STRICTLY HIGHER stratum and an unstratified
		// rule has no level, so the question has no answer rather than a lenient one. Reading it
		// leniently would hand back the unchecked cross-entity write in the first release that
		// has the feature — and a derive is the whole reason the ordering exists.
		if d := t.Action.allDerives(); len(d) > 0 {
			return false, fmt.Errorf("action derives into %s but the rule declares no `stratum:`: "+
				"a derive must land STRICTLY ABOVE the rule that writes it, and a rule outside "+
				"the ordering has no level to be above. Put the rule in a stratum", d[0].addr())
		}
		return true, nil
	}
	st, ok := e.stratum(t.Stratum)
	if !ok {
		return false, nil // dormant: held until its stratum arrives
	}
	if !Matches(st.Match).contains(t.Condition.Subject) {
		return false, fmt.Errorf("subject %s is outside stratum %q (%s): a rule acts on what it "+
			"selects, and a stratum's pattern says which subjects it owns — a rule reaching past "+
			"it is acting at a level it did not declare",
			subjectText(t.Condition.Subject), st.Name, matchesText(st.Match))
	}
	// A path read inside a DERIVE must resolve at or below this rule's own level. Derive
	// inertness rests on the levels above being unable to influence what a derive writes, and a
	// read reaching upward is precisely how they would: the thing above changes, the derive
	// writes different bytes, the level above wakes, and it goes round — across a level, which
	// is the cycle the ordering exists to exclude. At or below cannot do that, because an entity
	// there is written only by rules at that level or derived into from below it.
	//
	// A patch is unconstrained: nothing downstream depends on a patch being reproducible.
	for _, d := range t.Action.allDerives() {
		targets, err := readTargets(d.Payload)
		if err != nil {
			return false, err // shape; Action.validate says it better, but this path sees hooks too
		}
		for _, rt := range targets {
			at := e.stratumFor(Match{mKeySystem: rt.System, mKeyKind: rt.Kind})
			if at == nil {
				return false, fmt.Errorf("derive into %s reads %s, which is in no stratum: a read "+
					"inside a derive has to be at or below the rule's own level, and an entity "+
					"outside the ordering has no level to compare. Put it in a stratum, or move the "+
					"read into the `patch`, where nothing constrains it", d.addr(), rt)
			}
			if at.Level > st.Level {
				return false, fmt.Errorf("derive into %s reads %s, which is in stratum %q (level %d), "+
					"above %q (level %d): a derive may READ at or below its own level and no higher. "+
					"Reading upward is how a level above would change what this derive writes, and a "+
					"derive that writes something new every lap wakes that level again — a cycle "+
					"across a level, which is what the ordering rules out. A `patch` may read it",
					d.addr(), rt, at.Name, at.Level, st.Name, st.Level)
			}
		}
	}
	above := e.strataAbove(st.Level)
	for _, d := range t.Action.allDerives() {
		target := Match{mKeySystem: d.System, mKeyKind: d.Kind}
		var landed *Stratum
		for i := range above {
			if Matches(above[i].Match).contains(target) {
				landed = &above[i]
				break
			}
		}
		if landed == nil {
			// Told apart deliberately: landing nowhere is a missing stratum, landing at or below
			// is a cycle waiting to happen, and they are different mistakes to have made.
			if at := e.stratumFor(target); at != nil {
				return false, fmt.Errorf("derive into %s lands in stratum %q (level %d), which is "+
					"not above %q (level %d): a derive must strictly increase level — that is what "+
					"makes a cascade finite, and a derive sideways or downward is a cycle the "+
					"ordering was supposed to rule out",
					d.addr(), at.Name, at.Level, st.Name, st.Level)
			}
			return false, fmt.Errorf("derive into %s matches no stratum: every derived entity has "+
				"to belong to a level, and there is none that owns %s (declared strata: %s)",
				d.addr(), d.addr(), strataText(e.Strata()))
		}
	}
	return true, nil
}

// strataAbove returns the strata strictly above a level, nearest first — so a derive lands in
// the LOWEST stratum that owns it, which is the only answer disjointness leaves anyway.
func (e *Engine) strataAbove(level int) []Stratum {
	var out []Stratum
	for _, s := range e.Strata() {
		if s.Level > level {
			out = append(out, s)
		}
	}
	return out
}

// stratumFor finds the stratum owning a target, at any level — used only to say a better thing
// about a derive that landed in the wrong place.
func (e *Engine) stratumFor(target Match) *Stratum {
	all := e.Strata()
	for i := range all {
		if Matches(all[i].Match).contains(target) {
			return &all[i]
		}
	}
	return nil
}

// deriveTarget is one derive's address and content: <system>.<kind>, then the payload.
type deriveTarget struct {
	System, Kind string
	Payload      entity.Payload
	// Each is set when this target fans out: one derived entity per element of `over`,
	// addressed by `key`. Payload is nil then, and vice versa.
	Each *eachSpec
}

func (d deriveTarget) addr() string { return d.System + ":" + d.Kind }

// eachSpec is `!each {over, key, value}` — the fan-out form of a derive.
//
// It exists because one firing derives exactly one entity, and a task that answers with twelve
// findings has eleven of them left as prose (docs/working/derive-residual.md, and now
// docs/working/derive-each-plan.md). The invariant it must not break is the one the level
// ordering rests on: a derive's content is fixed by its address, so a re-fire writes the same
// bytes to the same place and dies as a no-op. A fan-out keeps that in the generalised form —
// **content may carry state exactly when the address is a function of that same state** — which
// is what `key` declares and why it is not optional.
type eachSpec struct {
	Over  entity.Payload // a single $reference to the collection to walk
	Key   entity.Payload // a single $reference, rooted at $item — the address suffix
	Value entity.Payload // the payload template, per element
}

// eachTag names the fan-out. Matched through semTags rather than against Node.Tag, because a
// tag is a dot-separated CHAIN carrying layout the parser wrote: the same charter reads
// `!each` in block style and `!bracket.each` in flow style, and comparing the string would
// have quietly fanned out one of them and derived a payload with three odd fields from the
// other.
const eachTag = "!each"

// eachFields is the closed keyspace under the tag. Closed on purpose: the residual worried
// that reserved keys make a payload with a field called `over` unspellable, and a TAG answers
// that — under it, the keys are the construct's and an unknown one is a typo rather than data.
var eachFields = map[string]bool{"over": true, "key": true, "value": true}

// isEach reports whether this derive target's value carries the fan-out tag.
func isEach(n entity.Payload) bool {
	if n == nil {
		return false
	}
	for _, t := range semTags(n.Tag) {
		if head, _, _ := ir.TagArgs(t); head == eachTag {
			return true
		}
	}
	return false
}

// readEach pulls the triple out of a tagged node. It reports what is missing rather than
// filling in a default: every one of the three is load-bearing, and a fan-out with no `key`
// is the shape whose address would stop being a function of its content.
func readEach(n entity.Payload) (*eachSpec, error) {
	if n.Type != ir.ObjectType {
		return nil, fmt.Errorf("!each takes an object of over/key/value, got %v", n.Type)
	}
	spec := &eachSpec{}
	seen := map[string]bool{}
	for i, f := range n.Fields {
		name := f.String
		if !eachFields[name] {
			return nil, fmt.Errorf("!each has no field %q — it takes over, key and value", name)
		}
		seen[name] = true
		switch name {
		case "over":
			spec.Over = n.Values[i]
		case "key":
			spec.Key = n.Values[i]
		case "value":
			spec.Value = n.Values[i]
		}
	}
	for _, want := range []string{"over", "key", "value"} {
		if !seen[want] {
			return nil, fmt.Errorf("!each is missing %q: it walks `over`, addresses each element "+
				"by `key`, and writes `value`", want)
		}
	}
	return spec, nil
}

// soleRef reports the reference a node consists ENTIRELY of. A reference embedded in text
// interpolates and yields a string, which is right for a prompt and wrong for both of these
// fields: `over` names a collection to walk and `key` names an identity, and neither has a
// meaning as "some text with a value in it".
func soleRef(n entity.Payload) (string, bool) {
	if n == nil || n.Type != ir.StringType {
		return "", false
	}
	s := n.String
	if l := refAt(s); l == len(s) && l > 0 {
		return s[1:], true
	}
	return "", false
}

// derives reads THIS action's derive block into its targets. The address is exactly two levels
// of nesting and everything below is payload, so there is no boundary to find — which is what
// lets the form be read without a delimiter.
func (a Action) derives() []deriveTarget {
	// UNWRAPPED AT EVERY LEVEL, because a charter is where people write comments and a head
	// comment WRAPS the value it precedes. `# one entity per symbol` above `derive:` made this
	// walk see a Comment where the address should be, so the block read as no derives at all
	// and the install refused a rule that was written correctly. A comment is not a container:
	// asking what a value's structure IS means stepping past what was said about it (ir/nav.go).
	d := unwrap(a.Derive)
	if d == nil || d.Type != ir.ObjectType {
		return nil
	}
	var out []deriveTarget
	for i, sysName := range d.Fields {
		sys := unwrap(d.Values[i])
		if sys == nil || sys.Type != ir.ObjectType {
			continue // refused by validate; nothing to walk
		}
		for j, kindName := range sys.Fields {
			t := deriveTarget{System: sysName.String, Kind: kindName.String}
			if v := unwrap(sys.Values[j]); isEach(v) {
				// A malformed one is refused at install (validateDerive), so anything reaching
				// here parses; a reader that cannot is left as a target with neither half set,
				// which the firing path refuses rather than treating as an empty payload.
				t.Each, _ = readEach(v)
			} else {
				t.Payload = v
			}
			out = append(out, t)
		}
	}
	return out
}

// allDerives reads the derives of this action AND everything below it — a sequence's elements,
// a completion hook's action. An install check has to see the whole tree, because a derive
// inside an onDone crosses a level exactly as one at the top does.
func (a Action) allDerives() []deriveTarget {
	out := a.derives()
	for _, el := range a.Seq {
		out = append(out, el.allDerives()...)
	}
	for _, h := range []*Action{a.OnDone, a.OnFailed, a.OnCancelled, a.OnOrphaned} {
		if h != nil {
			out = append(out, h.allDerives()...)
		}
	}
	return out
}

// validateDerive checks one action's derive block: the address is two levels, the content is an
// object, and — the load-bearing one — the content is LITERAL.
func (a Action) validateDerive() error {
	if a.Derive == nil {
		return nil
	}
	// Past the comment wrapper before asking what the shape is — see derives(), which walks the
	// same block and refused a correctly written rule for the same reason.
	d := unwrap(a.Derive)
	if d.Type != ir.ObjectType {
		return fmt.Errorf("action.derive must be an object addressed <system>.<kind>, got %v", d.Type)
	}
	if len(d.Fields) == 0 {
		return fmt.Errorf("action.derive is empty: it addresses <system>.<kind> and writes what " +
			"goes there")
	}
	for i, sysName := range d.Fields {
		sys := unwrap(d.Values[i])
		if sys == nil || sys.Type != ir.ObjectType || len(sys.Fields) == 0 {
			return fmt.Errorf("action.derive.%s must be an object of kinds: the address is exactly "+
				"two levels — <system>.<kind> — and everything below is the payload", sysName.String)
		}
		for j, kindName := range sys.Fields {
			payload := unwrap(sys.Values[j])
			if isEach(payload) {
				if err := validateEach(payload); err != nil {
					return fmt.Errorf("action.derive.%s.%s: %w", sysName.String, kindName.String, err)
				}
				continue
			}
			if payload == nil || payload.Type != ir.ObjectType {
				// The engine writes the subject linkage into the derived entity, and there is
				// nowhere to put it on a scalar. That linkage is what makes literal content
				// sufficient — `do:approve` says "approve the thing this is about", and the
				// linkage says which — so a payload that cannot carry it is not a derive.
				return fmt.Errorf("action.derive.%s.%s must be an object: the engine writes the "+
					"subject this derivation is about into it, which a scalar has nowhere to hold",
					sysName.String, kindName.String)
			}
			if ref := firstBadRef(payload); ref != "" {
				return fmt.Errorf("action.derive.%s.%s carries the reference %q: a derive may "+
					"reference the firing's IDENTITY and never its STATE, and $%s is the only "+
					"reference that is identity. The distinction is what the whole ordering rests "+
					"on: a derived entity's address is a function of (rule, subject), so a derive "+
					"whose content is also fixed by (rule, subject) writes the same bytes to the "+
					"same address on every re-fire — a no-op the store ends by itself, which is why "+
					"a rule may read a HIGHER level without that read becoming a cycle. A value read "+
					"from a payload changes as rules fire, so a derive carrying one would make "+
					"exactly that read live",
					sysName.String, kindName.String, ref, refSubject)
			}
		}
	}
	return nil
}

// validateEach checks a fan-out at install: the triple is there, `over` and `key` are single
// references, `key` is rooted at the element, and `value` is an object whose references reach
// the element or the subject's identity and nothing else.
//
// This is where the plain form's rule is RELAXED, and it is worth being exact about what
// replaces it. A plain derive may not carry state because its address is (rule, subject) and a
// stateful payload at a fixed address would make a read of a higher level live. A fan-out's
// address is (rule, subject, key) and `key` is read out of the element — so the same answer
// yields the same addresses carrying the same payloads, which is the same no-op by a different
// route. Content may carry state exactly when the address is a function of that same state.
func validateEach(n entity.Payload) error {
	spec, err := readEach(n)
	if err != nil {
		return err
	}
	if _, ok := soleRef(spec.Over); !ok {
		return fmt.Errorf("!each `over` must be a single reference to the collection to walk " +
			"(`over: $data.findings`), not text with one in it: a reference embedded in a string " +
			"interpolates to text, and there is nothing to walk in that")
	}
	key, ok := soleRef(spec.Key)
	if !ok {
		return fmt.Errorf("!each `key` must be a single reference (`key: $item.symbol`)")
	}
	if key != refItem && !strings.HasPrefix(key, refItem+".") {
		return fmt.Errorf("!each `key` is %q: an element's address must be read out of THE "+
			"ELEMENT, so it is rooted at $%s. That is the whole of what makes a fan-out safe — "+
			"the same answer yields the same addresses — and a key read from anywhere else would "+
			"give every element the same one", "$"+key, refItem)
	}
	if spec.Value == nil || spec.Value.Type != ir.ObjectType {
		// Same reason a plain derive's payload must be an object: the engine writes the subject
		// linkage into it, and a scalar has nowhere to hold it.
		return fmt.Errorf("!each `value` must be an object: the engine writes the subject this " +
			"derivation is about into it, which a scalar has nowhere to hold")
	}
	if ref := firstBadEachRef(spec.Value); ref != "" {
		return fmt.Errorf("!each `value` carries the reference %q: an element's payload may "+
			"reference the ELEMENT ($%s) and the firing's identity ($%s and its parts), and "+
			"nothing else. $%s.commit is refused here for the reason it is refused in a plain "+
			"derive: it is a function of WHEN the firing happened rather than of what it is "+
			"about, so a derive carrying one writes something new on every lap",
			ref, refItem, refSubject, refSubject)
	}
	return nil
}

// refItem is the element binding inside a fan-out. `$item` rather than `$resultItem` because
// the construct is meant to work over `$subject.value.something` too, and a name that hard-codes
// where the collection came from would have to be renamed or duplicated the first time it does.
const refItem = "item"

// firstBadEachRef is firstBadRef with the element admitted.
func firstBadEachRef(n entity.Payload) string {
	bad := ""
	eachRef(n, func(ref string) {
		if bad != "" {
			return
		}
		if ref == refItem || strings.HasPrefix(ref, refItem+".") {
			return
		}
		if !deriveMayReference(ref) {
			bad = ref
		}
	})
	return bad
}

// A derive may reference its SUBJECT and nothing else — every part of it, including the
// subject's state, but not the occasion of the firing.
//
// The line is not "identity, never state", which is where this started and which was too
// strict. What the ordering actually needs is content THE LEVELS ABOVE CANNOT INFLUENCE, and a
// reference cannot reach another entity at all: it reads the firing's own subject, or a
// task's result. Nothing above can write the subject — a patch touches only its own subject
// and a derive only climbs — so an upward read that wakes the rule without changing the subject
// resolves $subject.value to the same bytes, writes them to the same address, and dies as a
// no-op. That is the property "reads may go anywhere" rests on, and $subject.value keeps it.
//
// $subject.commit does not, and it is the one part refused here. It is a function of WHEN the
// firing happened rather than of the subject, so a read from above changes it:
//
//	L (level 0, subject S, reads D)   derive: { at: $subject.commit }
//	H (level 10, subject D)           patch:  { seen: true }
//
//	S changes → L derives D{at: 100} → H patches D → L's read wakes it at commit 102 →
//	D{at: 102}, a real change → H fires → …
//
// Unbounded, across a level, paced only by the throttle — the cycle the ordering exists to rule
// out, and it would cost the read side its freedom (`read at your level or below` would have to
// come back). The engine's own linkage stays commit-free for exactly this reason: recording the
// causing commit at verse.meta.derivedFrom would reintroduce the loop from inside the engine.
func deriveMayReference(ref string) bool {
	if ref == refSubject {
		return true
	}
	switch subjectPart(ref) {
	case subjPartRef, subjPartSystem, subjPartKind, subjPartID, subjPartValue:
		return true
	}
	return false
}

// firstBadRef reports the first $reference in a subtree that a derive may not carry, or "" when
// every one of them is admissible.
func firstBadRef(n entity.Payload) string {
	bad := ""
	eachRef(n, func(ref string) {
		if bad == "" && !deriveMayReference(ref) {
			bad = ref
		}
	})
	return bad
}

// eachElements resolves a fan-out's `over` and yields the elements to derive from.
//
// `over` must name a LIST. That is narrower than it could be — a kpath can select across
// structure, and the residual reached for ListKPath so an answer would not have to put its items
// in one array at one depth — and the narrowness is the point at this size: `$data.findings` and
// `$data.findings.*` would otherwise be two spellings, one yielding the list as a single element
// whose `key` cannot resolve, and a fan-out that quietly derives one entity is the failure this
// whole construct exists to stop being possible. A charter that wants a nested answer flattened
// says so in its answer contract, where the shape is already declared.
func (e *Engine) eachElements(spec *eachSpec, root entity.Payload) ([]entity.Payload, error) {
	ref, _ := soleRef(spec.Over) // install-checked
	node, err := deref(root, ref)
	if err != nil {
		return nil, fmt.Errorf("!each over $%s: %w", ref, err)
	}
	if node == nil || node.Type != ir.ArrayType {
		got := "nothing"
		if node != nil {
			got = node.Type.String()
		}
		return nil, fmt.Errorf("!each over $%s is %s, and a fan-out walks a LIST: ask for one in "+
			"the answer contract (`findings: !irtype []`) rather than deriving from a shape "+
			"nobody declared", ref, got)
	}
	return node.Values, nil
}

// eachRootFor builds the root ONE element resolves against: the subject, and `$item`.
//
// The firing's own payload is deliberately not in it. Install already refuses a `value` that
// references anything else, and this is the same rule said structurally — a derive reaches its
// subject and its element, and a reader of this function does not have to trust a check
// somewhere else to know that.
func eachRootFor(subject *ir.Node, item entity.Payload) entity.Payload {
	return ir.FromMap(map[string]*ir.Node{
		refSubject: subject,
		refItem:    item.DeepCopy(),
	})
}

// eachID addresses one derived element: (rule, subject, key).
//
// The hash is not decoration. safeSeg flattens the characters an id may not hold, and the keys
// this construct invites are exactly the ones that need flattening — `source/github.Reflect`
// carries a slash, and Go's own spelling of a method carries more. Two DISTINCT keys that
// flatten alike would otherwise share one entity, which is the fan-out silently losing a member:
// the failure that is invisible because the ledger simply reads short. So a rewritten key takes
// the hash of what it was, the same way a rewritten subject does in spawnID.
func eachID(chain string, ref entity.Ref, key string) string {
	base := spawnID(chain, ref)
	safe := safeSeg(key)
	if safe == key {
		return base + ChainSep + safe
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return fmt.Sprintf("%s%s%s%s%08x", base, ChainSep, safe, hashSep, h.Sum32())
}

// deriveFromField is where the engine records what a derived entity is about, under the reserved
// root: verse.meta.derivedFrom = {rule, subject}.
//
// Engine-owned, on the exact precedent a spawn sets — "the subject is the engine's to set, not
// the charter's" — and for the same reason: a derivation is about exactly one subject, and which
// one is the Condition's answer, not something the charter should be trusted to interpolate.
const deriveFromField = "derivedFrom"

// deriveContent resolves what a derive writes: its $subject references expanded, then the
// engine's linkage merged in.
func (e *Engine) deriveContent(payload entity.Payload, chain string, oc occasion, subj entity.Ref) (entity.Payload, error) {
	// The wants are read off the derive itself, and `commit` is cleared whatever it asked for:
	// install refuses $subject.commit here, and clearing it means the reference cannot resolve
	// either. Two ends saying the same thing, so relaxing one alone cannot open the other.
	w := wantsOf(payload)
	w.commit = false
	sn, err := e.subjectFor(oc, subj, w)
	if err != nil {
		return nil, err
	}
	// Against a root holding ONLY the subject, not the payload that fired the action: a derive
	// reaches its own subject and nothing else, and that is structural here rather than a rule
	// enforced only at install.
	expanded, err := expandPatch(payload, refRoot(nil, sn), e.readerFor(oc))
	if err != nil {
		return nil, err
	}
	return deriveLinkage(expanded, chain, subj)
}

// eachContent resolves what ONE element derives: its `value` with `$item` and the subject
// expanded, then the engine's linkage. deriveContent's twin, and it clears `commit` for the same
// reason — a derive that writes something new on every lap wakes the level above, which wakes
// the rule again.
func (e *Engine) eachContent(spec *eachSpec, chain string, oc occasion, subj entity.Ref, item entity.Payload) (entity.Payload, error) {
	w := wantsOf(spec.Value)
	w.commit = false
	sn, err := e.subjectFor(oc, subj, w)
	if err != nil {
		return nil, err
	}
	expanded, err := expandPatch(spec.Value, eachRootFor(sn, item), e.readerFor(oc))
	if err != nil {
		return nil, err
	}
	return deriveLinkage(expanded, chain, subj)
}

// eachKey resolves one element's address suffix.
//
// An empty key FAILS THE FIRING, and this is the residual's open question answered: derive the
// others and record the refusal, or refuse the lot. Refuse. A fan-out that drops a member
// produces a ledger that is quietly short, and short is the one failure a reader cannot see —
// where a refused firing is loud, is about the whole answer, and leaves the previous state
// standing to be re-derived when the answer is fixed.
func eachKey(spec *eachSpec, root entity.Payload, at int) (string, error) {
	ref, _ := soleRef(spec.Key) // install-checked
	n, err := deref(root, ref)
	if err != nil {
		return "", fmt.Errorf("!each key $%s on element %d: %w", ref, at, err)
	}
	s, ok := scalarText(n)
	if !ok || s == "" {
		return "", fmt.Errorf("!each key $%s is empty on element %d: every element must carry "+
			"the field its address is read from, and one that does not cannot be told apart "+
			"from the next", ref, at)
	}
	return s, nil
}

// deriveLinkage merges the engine's record of what this derivation is about into the content the
// charter wrote.
func deriveLinkage(payload entity.Payload, chain string, subj entity.Ref) (entity.Payload, error) {
	link := entity.Obj{
		entity.ReservedRoot: entity.Obj{
			entity.MetaKey: entity.Obj{
				deriveFromField: entity.Obj{
					"rule":    chainKPath(chain),
					"subject": subj.String(),
				},
			},
		},
	}.Payload()
	out, err := tony.Patch(payload.DeepCopy(), link)
	if err != nil {
		return nil, fmt.Errorf("recording what the derivation is about: %w", err)
	}
	return out, nil
}

// subjectText / matchesText / strataText render for error messages only.
func subjectText(m Match) string {
	if s, ok := m.address(); ok {
		return s
	}
	return fmt.Sprintf("%v", map[string]any(m))
}

func matchesText(ms []Match) string {
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		parts = append(parts, subjectText(m))
	}
	return fmt.Sprintf("%v", parts)
}

func strataText(all []Stratum) string {
	if len(all) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(all))
	for _, s := range all {
		parts = append(parts, fmt.Sprintf("%s=%d %s", s.Name, s.Level, matchesText(s.Match)))
	}
	return fmt.Sprintf("%v", parts)
}
