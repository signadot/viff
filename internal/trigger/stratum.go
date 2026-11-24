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
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/internal/trigger/charter"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/exprs"
	"go.viff.dev/viff/pkg/launch"
)

// Stratum is one level of the ordering: what it owns, and where it sits.
//
// The codec is hand-written for the reason tonycodec.go gives for Match's: `match` accepts one
// pattern bare or a union as a list, and a generator that has to be told a field takes two
// shapes is being told to write the codec anyway.
//
//tony:schemagen=viff-stratum,notag,codec=custom
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
	node = ir.Uncomment(node)
	if node == nil || node.Type == ir.NullType {
		*ms = nil
		return nil
	}
	// A TAGGED ARRAY IS ONE MATCH, not a union of its steps, and the tag is what says so —
	// `!viff-path [viff-dev, drift, finding]` is an address, and its three steps are not three
	// matches. tonycodec's own note on the tag names this hazard ("the union form a stratum's
	// `match` takes is already one"), and reading it wrong is not a parse error: each step reads
	// as a bare name, which matches a system and so matches nearly everything.
	//
	// It cost a restart. A stratum written down as a path came back as a union that overlapped
	// its neighbour, so restore refused it — and then refused every rule whose derive needed the
	// level it named. The charter SHRANK across a restart, silently, in the store's own copy of
	// itself: `viff-dev.drift` lost `ledger` and `comment-drift`, `viff-dev.merge` lost `train`
	// and the two rules that run on it.
	if node.Type == ir.ArrayType && tagName(node) == ViffPathTag {
		var m Match
		if err := m.FromTonyIR(node, opts...); err != nil {
			return err
		}
		*ms = Matches{m}
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
	node = ir.Uncomment(node)
	if node == nil || node.Type == ir.NullType {
		return nil
	}
	if node.Type != ir.ObjectType {
		return fmt.Errorf("stratum: want an object of {name, level, match}, got %v", node.Type)
	}
	for i, field := range node.Fields {
		val := ir.Uncomment(node.Values[i])
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

// matchContains reports whether outer picks everything inner can: its path is no longer than the
// inner's, and at each position it admits every name the inner one does (segContains).
func matchContains(outer, inner Match) bool {
	return entity.Under(outer.SegNodes(), inner.SegNodes(), segContains)
}

// segContains reports whether every name the inner segment admits, the outer one admits too.
//
// SEGMENT BY SEGMENT, and the outer path may be SHORTER: `[viff-dev, drift]` contains
// `[viff-dev, drift, head]` (entity.Under). An outer `*` contains anything at its position. An
// inner literal is contained by an outer segment that admits it — the same name, or an operator
// that matches it. An inner `*` or operator admits names nobody listed, so only an outer `*` can
// contain it: `!glob "a*"` does not contain a `*` that could be `b`.
func segContains(outer, inner *ir.Node) bool {
	if isAnySeg(outer) {
		return true
	}
	if lit, ok := entity.PathLiteral(inner); ok {
		return entity.SegMatches(outer, lit)
	}
	return false
}

// Overlaps reports whether some address could satisfy both matches' paths.
func (m Match) Overlaps(o Match) bool { return matchOverlap(m, o) }

// matchOverlap reports whether some address could satisfy both paths.
func matchOverlap(a, b Match) bool {
	return entity.Overlap(a.SegNodes(), b.SegNodes(), segDisjoint, entity.PathIsDescend)
}

// segDisjoint reports whether two segments admit no common name. It PROVES, and answers false
// whenever it cannot: two literals are disjoint unless equal, a literal and an operator are when
// the operator does not match the literal, and anything with a `*` or two operators may meet.
func segDisjoint(a, b *ir.Node) bool {
	al, aLit := entity.PathLiteral(a)
	bl, bLit := entity.PathLiteral(b)
	switch {
	case aLit && bLit:
		return al != bl
	case aLit:
		return !entity.SegMatches(b, al)
	case bLit:
		return !entity.SegMatches(a, bl)
	}
	return false
}

// isAnySeg reports whether a segment is the bare wildcard, which admits every name.
func isAnySeg(n *ir.Node) bool {
	return n != nil && n.Tag == "" && n.Type == ir.StringType && n.String == "*"
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
		// A stratum owns ADDRESSES, not states: what a node looks like changes as rules fire, so a
		// level that depended on it would not be a level.
		if m.HasPredicate() {
			return fmt.Errorf("stratum %q match[%d] carries a payload predicate: a stratum owns "+
				"ADDRESSES, not states. What a node looks like changes as rules fire, so a "+
				"level that depended on it would not be a level", s.Name, i)
		}
		if len(m.SegNodes()) == 0 {
			return fmt.Errorf("stratum %q match[%d] names no path: a stratum owns the addresses under "+
				"a place, and one naming none would own the whole tree", s.Name, i)
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
	return e.stratumLocked(name)
}

// stratumLocked is stratum with trigMu HELD.
func (e *Engine) stratumLocked(name string) (Stratum, bool) {
	for _, s := range e.strata {
		if s.Name == name {
			return s, true
		}
	}
	return Stratum{}, false
}

// AddStratum installs a stratum — durably, as its trigger:strata:<name> entity — refusing a
// name in use. InstallStratum replaces.
func (e *Engine) AddStratum(s Stratum) error {
	if _, stands := e.stratum(s.Name); stands {
		return fmt.Errorf("stratum %q already exists (remove it first)", s.Name)
	}
	return e.InstallStratumAs(context.Background(), "", s)
}

// InstallStratum is AddStratum for a CHARTER FILE: a level already installed EXACTLY as declared
// is a no-op, and one that DIFFERS is REPLACED — the same answer InstallTrigger gives a rule,
// because the file is what the operator means and re-applying an edited one is the ordinary way
// to change a charter.
//
// The rules in a stratum were checked against it when they installed (checkLevels), so moving
// it under them would leave a charter whose ordering nobody has verified. The check is RE-RUN
// rather than the change refused: every rule standing in this charter faces the level
// discipline again against the new ordering, and one that no longer fits refuses the CHANGE,
// naming itself. Nothing is left standing unchecked, and nothing has to be taken down to get
// it. It is RecheckReads one axis over, which is what an operator NARROWING a `reads:`
// declaration already gets.
func (e *Engine) InstallStratum(s Stratum) error {
	return e.InstallStratumAs(context.Background(), "", s)
}

// InstallStratumAs is InstallStratum on behalf of a principal, the way InstallTriggerAs is,
// answered once the engine's follow has the level's record (reached), bounded by ctx.
//
// A LEVEL IS APPLIED TO THE ORDERING BEFORE IT IS WRITTEN, where a rule is written and then
// placed from its record (follow.go): the checks a level faces are over the ordering it joins
// — the set's properties, and every rule's fit under the edited level — and are made in
// memory, with the record written last so that a level the engine would not accept is not
// one the records say. The follow finds the record saying what the ordering already does.
func (e *Engine) InstallStratumAs(ctx context.Context, as string, s Stratum) error {
	if e.isClosing() {
		return ErrClosing
	}
	old, ok := e.stratum(s.Name)
	if !ok {
		return e.addStratumTaking(ctx, s, as, true)
	}
	if old.Level == s.Level && matchesText(old.Match) == matchesText(s.Match) {
		return nil // the same level, declared again: the file has been applied twice
	}
	// Asked before anything moves, for the reason InstallTriggerAs asks: the swap writes the
	// standing record through the engine's own store.
	if err := e.MayRemoveStratum(as, s.Name); err != nil {
		return err
	}
	return e.replaceStratum(ctx, old, s, as)
}

// MayRemoveStratum is MayRemoveTrigger for a level.
func (e *Engine) MayRemoveStratum(principal, name string) error {
	if principal == "" || entity.IsSystem(principal) {
		return nil
	}
	if _, ok := e.stratum(name); !ok {
		return nil
	}
	return entity.Check(e.store, principal, entity.OwnerLevel, charter.StratumRef(e.confine.stamp(), name), "removing it")
}

// RemoveStratumAs is RemoveStratum on behalf of a principal, refused where they may not.
func (e *Engine) RemoveStratumAs(ctx context.Context, principal, name string) error {
	if err := e.MayRemoveStratum(principal, name); err != nil {
		return err
	}
	return e.removeStratumAt(ctx, name)
}

// replaceStratum swaps an installed level for an edited one, and puts the old one back if the
// rules standing in this charter do not survive the change.
//
// The order is addStratumTaking's, for its reasons: the set-properties first (against the other
// strata — the level being replaced is not its own neighbour), then the swap in memory, then the
// re-check, and the durable record LAST, so a level the engine would not accept is not one the
// records say.
//
// The re-check runs OFF trigMu, so a firing in flight can see the edited level before the check
// that puts it back has finished. checkLevels takes the lock itself and holding it across them
// would deadlock, and the window is the one an install already has between appending a level and
// promoting the rules held for it — install-time, and bounded by a syntactic check.
func (e *Engine) replaceStratum(ctx context.Context, old, s Stratum, as string) error {
	if err := s.validate(); err != nil {
		return err
	}
	e.trigMu.Lock()
	if err := e.checkStrataSetLocked(s, s.Name); err != nil {
		e.trigMu.Unlock()
		return err
	}
	e.swapStratumLocked(s)
	e.trigMu.Unlock()
	if err := e.recheckLevels(); err != nil {
		e.swapStratum(old)
		return fmt.Errorf("stratum %q cannot become level %d %s (it is level %d %s): %w",
			s.Name, s.Level, matchesText(s.Match), old.Level, matchesText(old.Match), err)
	}
	// WRITTEN OVER. The spec is stored raw, so the write replaces it whole — a match that lost a
	// dimension does not keep it — and nothing is deleted first: a delete would take the
	// level's owner record with it, and the grants on it, and hand the level to whoever edited
	// it. A write that does not land leaves the old record standing, so there is nothing to put
	// back in the store.
	rev, err := e.persistStratum(s, as, true)
	if err != nil {
		e.swapStratum(old)
		return err
	}
	return e.reached(ctx, rev)
}

// RestoreStratum puts a level back as it stood — the undo of a replacement, for a transaction
// that is backing out. The engine's own act, asked of nobody, and it CLAIMS NOTHING: put back
// as an install by nobody would be, the level would be taken for the system, and its installer
// refused the next time they edited it.
func (e *Engine) RestoreStratum(ctx context.Context, old Stratum) error {
	if _, ok := e.stratum(old.Name); !ok {
		return e.addStratumTaking(ctx, old, "", false)
	}
	e.swapStratum(old)
	rev, err := e.persistStratum(old, "", false)
	if err != nil {
		return err
	}
	return e.reached(ctx, rev)
}

// recheckLevels asks the level discipline of every rule already installed, which is what an
// operator EDITING a level needs — exactly as RecheckReads is what one narrowing an `reads:`
// needs. A rule that fitted the ordering that stands and does not fit the edited one must refuse
// the CHANGE rather than be quietly left at a level nothing checked it against.
//
// A rule that comes back DORMANT is not a failure: that is checkLevels saying the rule names a
// stratum which is not installed, and the one being replaced keeps its name, so no installed rule
// can lose the level it declared here.
func (e *Engine) recheckLevels() error {
	for _, t := range e.Triggers() {
		if _, err := e.checkLevels(t); err != nil {
			return fmt.Errorf("rule %q no longer fits: %w — it is installed, so change the level to "+
				"keep it, or remove the rule first", t.Name, err)
		}
	}
	return nil
}

// checkStrataSetLocked asks the two properties that are about the ORDERING s is joining rather
// than about s itself: a name already taken, a level already held, and an overlap. Called with
// trigMu HELD, so the check and the append or swap that follows it are one step.
//
// `replacing` names the stratum s stands in for, and is skipped: an edited level is not its own
// neighbour, so comparing it with what it replaces would report every edit as a duplicate level.
// Empty for a fresh install, where a name already there is the refusal.
func (e *Engine) checkStrataSetLocked(s Stratum, replacing string) error {
	for _, x := range e.strata {
		if replacing != "" && x.Name == replacing {
			continue
		}
		if x.Name == s.Name {
			return fmt.Errorf("stratum %q already exists (remove it first)", s.Name)
		}
		// The level is the ordering, and two strata holding one have not said which comes
		// first. Refused rather than resolved: neither answer is the author's, and "strictly
		// higher" stops being a question with one answer the moment it is ambiguous.
		if x.Level == s.Level {
			return fmt.Errorf("stratum %q wants level %d, which %q already holds: the level IS "+
				"the ordering, so two strata at one level do not say which comes first. Give it "+
				"its own (the gaps are there to be used)", s.Name, s.Level, x.Name)
		}
		// Without disjointness the ordering is vacuous: an entity landing in the intersection
		// of two strata is both above and at, and a derive into it has two answers.
		if Matches(x.Match).overlaps(s.Match) {
			return fmt.Errorf("stratum %q overlaps %q: an entity matching both would be at two "+
				"levels at once, and \"strictly higher\" would have two answers for it. Strata "+
				"must be pairwise disjoint", s.Name, x.Name)
		}
	}
	return nil
}

// swapStratum puts s in place of the installed stratum of the same name.
func (e *Engine) swapStratum(s Stratum) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	e.swapStratumLocked(s)
}

// swapStratumLocked is swapStratum for a caller already holding trigMu.
func (e *Engine) swapStratumLocked(s Stratum) {
	for i := range e.strata {
		if e.strata[i].Name == s.Name {
			e.strata[i] = s
			return
		}
	}
}

// addStratum puts a stratum into the ordering, with nothing written: for the check's engine
// (Check), and for an install before it writes. The two set-properties live here rather than in
// validate because neither is a property of the stratum being installed: both are about the
// ordering it is joining.
func (e *Engine) addStratum(s Stratum) error {
	if err := s.validate(); err != nil {
		return err
	}
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	if err := e.checkStrataSetLocked(s, ""); err != nil {
		return err
	}
	e.strata = append(e.strata, s)
	return nil
}

// addStratumTaking installs a stratum durably, saying whether a system install claims the
// record, and answers once the follow has it.
func (e *Engine) addStratumTaking(ctx context.Context, s Stratum, as string, take bool) error {
	if err := e.addStratum(s); err != nil {
		return err
	}
	rev, err := e.persistStratum(s, as, take)
	if err != nil {
		e.removeStratum(s.Name)
		return err
	}
	// A rule installed before its stratum was held rather than refused; this is what it was
	// waiting for.
	e.promote(s.Name)
	return e.reached(ctx, rev)
}

// RemoveStratum removes a stratum by name. It is REFUSED while any installed rule names it.
//
// Not symmetry with anything — a rule can be removed freely — but because the alternative is
// silent: those rules passed check #1 against a pattern that would no longer exist, and nothing
// re-runs a check when its subject disappears. They would go on firing, unstratified, having
// been told they were ordered. That is a guarantee withdrawn without saying so, which is worse
// than an error the operator can answer by removing the rules first.
func (e *Engine) RemoveStratum(name string) error {
	return e.removeStratumAt(context.Background(), name)
}

// removeStratumAt is the removal, asked of nobody: the record is deleted, and the ordering
// follows — here, since the level's checks are over the ordering in memory, and through the
// follow, which finds it gone already.
func (e *Engine) removeStratumAt(ctx context.Context, name string) error {
	e.trigMu.Lock()
	var users []string
	for _, t := range e.triggers {
		if t.Stratum == name {
			users = append(users, t.Name)
			continue
		}
		// A RULE THAT DERIVES INTO IT IS A USER TOO, and counting only the rules that LIVE in a
		// stratum let the other kind through: `ledger` owns `viff-dev.drift.finding`, no rule
		// declares `stratum: ledger`, and removing it left `comment-drift` deriving into a level
		// that no longer existed. That is precisely the state install refuses ("derive into X
		// matches no stratum: every derived entity has to belong to a level"), reached through
		// the back door — and this refusal's own sentence, "installed and unchecked", is what
		// it becomes.
		for _, d := range t.Action.allDerives() {
			if at := e.stratumForLocked(d.match()); at != nil && at.Name == name {
				users = append(users, t.Name)
				break
			}
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
		// PATHS, not names: a path is where to look, and a name is only something to search
		// for — the same rule name can stand in several packages.
		sort.Strings(users)
		var shown []string
		for i, u := range users {
			if i == 3 {
				shown = append(shown, "…")
				break
			}
			shown = append(shown, e.RuleRef(u).String())
		}
		return fmt.Errorf("stratum %q at %s still holds %d rule(s) (%s): removing it would leave "+
			"them installed and unchecked — every subject that passed its pattern would be "+
			"answering to nothing. Remove the rules first, or move them",
			name, charter.StratumRef(e.confine.stamp(), name), len(users), strings.Join(shown, ", "))
	}
	ref := charter.StratumRef(e.confine.stamp(), name)
	_, rev, err := e.store.Write(entity.Write{At: ref.At()})
	if err != nil {
		return fmt.Errorf("%w: stratum %q: deleting %s: %w", ErrCharter, name, ref, err)
	}
	e.removeStratum(name)
	return e.reached(ctx, rev)
}

// removeStratum drops it from the in-memory ordering.
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
	for _, h := range e.pending {
		if h.Name == name {
			return fmt.Errorf("trigger %q is already pending, %s (remove it first)", name, h.cause)
		}
	}
	return nil
}

// promote asks the reconciler for a pass: a rule held for this stratum is armed by it, now that
// the stratum is here (reconcile.go).
func (e *Engine) promote(string) { e.kick() }

// persistStratum writes s into the viff instance as its stratum entity. Raw for the same reason a rule
// is raw: a match is full of operator tags, and an unescaped write has the store execute them
// rather than store them (see charter.go's charter.SpecField).
func (e *Engine) persistStratum(s Stratum, as string, take bool) (int64, error) {
	doc, err := s.ToTonyIR()
	if err != nil {
		return 0, fmt.Errorf("stratum %q: encoding it: %w", s.Name, err)
	}
	ref := charter.StratumRef(e.confine.stamp(), s.Name)
	_, rev, err := e.store.Write(entity.Write{
		At:        ref.At(),
		Value:     e.record(doc),
		Principal: as,
	})
	if err != nil {
		return 0, fmt.Errorf("%w: stratum %q: writing %s: %w", ErrCharter, s.Name, ref, err)
	}
	if !take {
		return rev, nil
	}
	return rev, e.takeForTheSystem(ref, as)
}

// stratumOf decodes one stratum record. A name disagreeing with the address is refused for the
// reason ruleOf gives: the name is what a rule references and the address is what a removal
// deletes, so the two diverging leaves a stratum nothing can get rid of.
func stratumOf(rec charter.Record) (Stratum, error) {
	if rec.Spec == nil {
		return Stratum{}, fmt.Errorf("stratum %s: no %s field", rec.Ref, charter.SpecField)
	}
	var s Stratum
	if err := s.FromTonyIR(rec.Spec); err != nil {
		return Stratum{}, fmt.Errorf("stratum %s: %w", rec.Ref, err)
	}
	if s.Name != rec.Name { // the tail: a package's records carry its address
		return Stratum{}, fmt.Errorf("stratum %s: it names itself %q; a stratum is addressed by "+
			"its name, so it would install as one and be removed as another", rec.Ref, s.Name)
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
			at := e.stratumFor(rt.place())
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
		target := d.match()
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
	return stratumIn(all, target)
}

// stratumForLocked is stratumFor for a caller already holding trigMu — Strata takes it.
func (e *Engine) stratumForLocked(target Match) *Stratum {
	all := append([]Stratum(nil), e.strata...)
	return stratumIn(all, target)
}

func stratumIn(all []Stratum, target Match) *Stratum {
	for i := range all {
		if Matches(all[i].Match).contains(target) {
			return &all[i]
		}
	}
	return nil
}

// deriveTarget is one derive's address and content: the place it writes, then the payload.
type deriveTarget struct {
	// Path is the place the derive writes under.
	Path []string
	// Key makes the derived id a function of (rule, subject, KEY) rather than of (rule,
	// subject) — one entity per value of something, where a plain derive gives exactly one.
	//
	// It is `!each`'s key without the fan-out, and it exists because "one per tip" could not be
	// said: a merge train needs a batch per main, and a derive re-firing at a new tip wrote the
	// same address, so the SAME gate id was asked and the second batch inherited the first
	// batch's answer. A gate is identified by (trigger, gate, subject); if the subject cannot
	// vary, two batches are one subject, asked about as one.
	//
	// It keys on STATE deliberately, which `!each` already does (`key: .[item.symbol]`), and the
	// invariant is the one a fan-out rests on rather than an exception to it: a derive may carry
	// state exactly when its address is a function of that same state. A re-fire reading the
	// same subject computes the same key, writes the same bytes to the same address, and dies as
	// the no-op that lets a rule read a higher level safely.
	Key entity.Payload
	// ID is `key` with the composition dropped: the derived entity is the place plus this
	// value, verbatim, so `viff-dev.drift.head.aaaa1111` is an address a charter can SPELL.
	//
	// That is the whole difference, and it is the reader's. A keyed id is
	// `head-moved@git.viff.head@aaaa1111` — a function of (rule, subject, key), which no
	// charter can compose, so anything wanting that record has to describe it instead of naming
	// it. Describing it costs a glob and a predicate at every reader; naming it costs nothing.
	//
	// WHAT THE PREFIX BOUGHT, and who pays for it now: two subjects of one rule that compute the
	// same key share one entity here, where `key` gives them one each. So `id` says the key
	// identifies the record ACROSS subjects — `head-moved`'s does, its subject being the single
	// entity `git.viff.head` — and `key` says it identifies it only within one. Getting that
	// backwards is the clobber this charter already paid for once, one plane over.
	//
	// The PLACE is still the engine's: a derive may only write inside its own stratum, checked
	// at install, and `id` does not touch that. What the author takes on is the id within it.
	ID      entity.Payload
	Payload entity.Payload
	// Each is set when this target fans out: one derived entity per element of `over`,
	// addressed by `key`. Payload is nil then, and vice versa.
	Each *eachSpec
}

// segs is the target's address.
func (d deriveTarget) segs() []string { return d.Path }

// refFor is the entity this derive writes: the target's own place, then the segments the engine
// derived for it.
//
// SEGMENTS RATHER THAN AN ID, so a derived entity is as deep as what it is about — the target,
// the rule, and the subject's own address. What that costs is stated where it is paid: a reader
// of derived entities says how deep they are, and how deep they are follows from the depth of
// the subjects the producing rule selects.
func (d deriveTarget) refFor(segs ...string) entity.Ref {
	return entity.RefAt(append(append([]string{}, d.segs()...), segs...)...)
}

// match is the target as the pattern the level and confinement checks compare against. A
// literal `id` is part of the address the checks can see, so a level may own exactly the
// entity a projection writes rather than the whole place beneath it.
func (d deriveTarget) match() Match {
	segs := make([]*ir.Node, 0, len(d.Path)+1)
	for _, sg := range d.Path {
		segs = append(segs, ir.FromString(sg))
	}
	if lit := literalKey(d.ID); lit != "" {
		segs = append(segs, ir.FromString(lit))
	}
	return Match{mKeyPath: segs}
}

func (d deriveTarget) addr() string { return entity.JoinSegs(d.segs()) }

// eachSpec is `!each {over, key, value}` — the fan-out form of a derive.
//
// It exists because one firing derives exactly one entity, and a task that answers with twelve
// findings has eleven of them left as prose (git-issue e95q3frq, and now). The invariant it must not break is the one the level
// ordering rests on: a derive's content is fixed by its address, so a re-fire writes the same
// bytes to the same place and dies as a no-op. A fan-out keeps that in the generalised form —
// **content may carry state exactly when the address is a function of that same state** — which
// is what `key` declares and why it is not optional.
type eachSpec struct {
	Over entity.Payload // one whole `.[…]`: the collection to walk
	Key  entity.Payload // one whole `.[…]` read from the element: the address suffix
	// ID is Key with the composition dropped, exactly as a single derive's is: the element
	// lands at the place plus this value, so a fan-out can write onto entities a DIFFERENT
	// rule owns the naming of. That is the whole use — a fan-out addressed by `key` can only
	// ever create its own copies, and "mark every commit that is already reflected" needs to
	// land on the record the commit's own rule derives. See deriveTarget.ID.
	ID    entity.Payload
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
var eachFields = map[string]bool{"over": true, "key": true, "id": true, "value": true}

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
		case "id":
			spec.ID = n.Values[i]
		case "value":
			spec.Value = n.Values[i]
		}
	}
	if seen["key"] && seen["id"] {
		return nil, fmt.Errorf("!each has both `key` and `id`, which are the same question " +
			"answered twice: `key` addresses each element beneath this rule and subject, `id` " +
			"addresses it by the value alone. Pick the one that says whether two subjects " +
			"fanning out over the same value write one entity or two")
	}
	for _, want := range []string{"over", "value"} {
		if !seen[want] {
			return nil, fmt.Errorf("!each is missing %q: it walks `over`, addresses each element "+
				"by `key` or `id`, and writes `value`", want)
		}
	}
	if !seen["key"] && !seen["id"] {
		return nil, fmt.Errorf("!each is missing an address: it walks `over` and writes `value`, " +
			"and each element needs a `key` (composed under this rule and subject) or an `id` " +
			"(the value alone)")
	}
	return spec, nil
}

// wholeExpr reports the expression a node consists ENTIRELY of: a string that is one `.[…]` and
// nothing else, answered as written. An expression embedded in text interpolates and yields a
// string, which is right for a prompt and wrong for both of these fields: `over` names a
// collection to walk and `key` names an identity, and neither has a meaning as "some text with a
// value in it".
func wholeExpr(n entity.Payload) (string, bool) {
	n = ir.Uncomment(n)
	if n == nil || n.Type != ir.StringType || !exprs.Whole(n.String) {
		return "", false
	}
	return n.String, true
}

// derives reads THIS action's derive block into its targets: a list of entries, each naming the
// place it writes under as a path.
func (a Action) derives() []deriveTarget {
	// UNWRAPPED AT EVERY LEVEL, because a charter is where people write comments and a head
	// comment WRAPS the value it precedes; a comment is not a container.
	d := ir.Uncomment(a.Derive)
	if d == nil || d.Type != ir.ArrayType {
		return nil // refused by validate; nothing to walk
	}
	var out []deriveTarget
	for _, el := range d.Values {
		t, ok := derivePathTarget(ir.Uncomment(el))
		if !ok {
			continue // refused by validate; nothing to walk
		}
		out = append(out, t)
	}
	return out
}

// derivePathTarget reads one entry of the list form: an address as a path, and the content —
// a literal `value`, or an `!each` fan-out, exactly as the map form takes.
func derivePathTarget(el *ir.Node) (deriveTarget, bool) {
	if el == nil || el.Type != ir.ObjectType {
		return deriveTarget{}, false
	}
	var t deriveTarget
	var content *ir.Node
	for i, key := range el.Fields {
		if key == nil || key.Type != ir.StringType {
			continue
		}
		val := ir.Uncomment(el.Values[i])
		switch key.String {
		case "path":
			segs, ok := addressSegs(val)
			if !ok {
				return deriveTarget{}, false
			}
			t.Path = segs
		case "value":
			content = val
		case "key":
			t.Key = val
		case "id":
			t.ID = val
		}
	}
	if len(t.Path) == 0 {
		return deriveTarget{}, false
	}
	if isEach(content) {
		t.Each, _ = readEach(content)
	} else {
		t.Payload = content
	}
	return t, true
}

// deriveContentNode is the `value` of one list-form derive entry — what goes at the address.
func deriveContentNode(el *ir.Node) *ir.Node {
	if el == nil || el.Type != ir.ObjectType {
		return nil
	}
	for i, key := range el.Fields {
		if key != nil && key.Type == ir.StringType && key.String == "value" {
			return ir.Uncomment(el.Values[i])
		}
	}
	return nil
}

// addressSegs reads an address written as a list of names — `[viff-dev, drift, head]`, or the
// same under `!viff-path`. A derive TARGET is a place to write, so its segments are names: an
// operator selects, and there is nothing to select when the question is where to put something.
func addressSegs(n *ir.Node) ([]string, bool) {
	if n == nil {
		return nil, false
	}
	if n.Type == ir.StringType {
		segs, err := entity.PathSegs(n.String)
		return segs, err == nil && len(segs) > 0
	}
	if n.Type != ir.ArrayType {
		return nil, false
	}
	out := make([]string, 0, len(n.Values))
	for _, v := range n.Values {
		text, ok := stepText(ir.Uncomment(v))
		if !ok || text == "" {
			return nil, false
		}
		out = append(out, text)
	}
	return out, len(out) > 0
}

// allDerives reads the derives of this action AND everything below it — a sequence's elements,
// a completion hook's action. An install check has to see the whole tree, because a derive
// inside an onDone crosses a level exactly as one at the top does.
func (a Action) allDerives() []deriveTarget {
	out := a.derives()
	for _, el := range a.Seq {
		out = append(out, el.allDerives()...)
	}
	for _, h := range []*Action{a.OnDone, a.OnFailed, a.OnCancelled} {
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
	// A SUSPENSION IS NOT A DERIVATION. The table a suspension holds by is written by the route
	// that writes its record, and by nothing that observes the store, so a record a rule derived
	// would suspend nothing while every listing said it did.
	for _, t := range a.derives() {
		if segs := t.segs(); len(segs) >= 2 && segs[0] == entity.ViffRoot && segs[1] == charter.System &&
			(len(segs) == 2 || segs[2] == SuspensionKind) {
			return fmt.Errorf("action.derive into %s: a suspension is made by `viff flow suspend`, "+
				"and a record a rule wrote there would hold nothing", strings.Join(segs, "."))
		}
	}
	// Past the comment wrapper before asking what the shape is — see derives(), which walks the
	// same block and refused a correctly written rule for the same reason.
	d := ir.Uncomment(a.Derive)
	// Each entry names its place as a path, and its content is a literal payload or an `!each`.
	if d.Type == ir.ArrayType {
		if len(d.Values) == 0 {
			return fmt.Errorf("action.derive is empty: it names where to write and what goes there")
		}
		for i, el := range d.Values {
			t, ok := derivePathTarget(ir.Uncomment(el))
			if !ok {
				return fmt.Errorf("action.derive[%d] must be {path: \"<a.kinded.path>\", value: …}: the "+
					"path is where it writes and the value is what goes there", i)
			}
			if isEach(deriveContentNode(ir.Uncomment(el))) {
				// The `!each` NODE, not the entry holding it: the entry carries `path` and
				// `value`, and the fan-out's closed keyspace is over/key/value.
				if err := validateEach(deriveContentNode(ir.Uncomment(el))); err != nil {
					return fmt.Errorf("action.derive[%d]: %w", i, err)
				}
				continue
			}
			where := strings.Join(t.Path, ".")
			if err := checkLiteralDerive(t.Payload, where); err != nil {
				return err
			}
			if t.Key != nil && t.ID != nil {
				return fmt.Errorf("action.derive.%s has both `key` and `id`, which are the same "+
					"question answered twice: `key` addresses one entity per value BENEATH this "+
					"rule and subject, `id` addresses it by the value alone. Pick the one that "+
					"says whether two subjects with the same value are one record or two", where)
			}
			if err := checkDeriveKey(t.Key, where); err != nil {
				return err
			}
			if err := checkDeriveID(t.ID, where); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("action.derive is a list of {path: \"<a.kinded.path>\", value: …} entries: the path " +
		"is the place it writes under and the value is what goes there")
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
	if _, ok := wholeExpr(spec.Over); !ok {
		return fmt.Errorf("!each `over` must be one expression and nothing else, the collection " +
			"to walk (`over: .[data.findings]`), not text with one in it: an expression embedded " +
			"in a string interpolates to text, and there is nothing to walk in that")
	}
	// `key` or `id` — the same check, since they differ in what the engine puts in FRONT of
	// the value, not in what the value may be read from.
	which, spelling := spec.Key, "key"
	if spec.ID != nil {
		which, spelling = spec.ID, "id"
	}
	key, ok := wholeExpr(which)
	if !ok {
		return fmt.Errorf("!each `%s` must be one expression and nothing else, read from the "+
			"element (`%s: .[item.symbol]`, or composed, `'.[item.symbol + \"-\" + item.kind]'`)",
			spelling, spelling)
	}
	if bad := readsOnly(ir.FromString(key), false, refItem); bad != "" {
		return fmt.Errorf("!each `%s` is %s, which reads %s: an element's address must be read "+
			"out of THE ELEMENT, `%s`, and nothing else. That is the whole of what makes a "+
			"fan-out safe — the same answer yields the same addresses — and one read from "+
			"anywhere else would give every element the same address", spelling, key, bad, refItem)
	}
	// AND IT READS THE ELEMENT. Refusing every name but `item` leaves a key that reads no name at
	// all, `.["fixed"]` or `.[1 + 1]`, and that is the same address for every element: each
	// writes over the last, and the fan-out derives one entity.
	if reads, _ := exprs.ReadsIn(key); !slices.ContainsFunc(reads, func(r exprs.Path) bool { return r[0] == refItem }) {
		return fmt.Errorf("!each `%s` is %s, which reads nothing of the element: every element "+
			"would land at one address, each writing over the last. An element's address is read "+
			"out of THE ELEMENT, `%s: .[item.symbol]`", spelling, key, spelling)
	}
	if spec.Value == nil || spec.Value.Type != ir.ObjectType {
		// Same reason a plain derive's payload must be an object: the engine writes the subject
		// linkage into it, and a scalar has nowhere to hold it.
		return fmt.Errorf("!each `value` must be an object: the engine writes the subject this " +
			"derivation is about into it, which a scalar has nowhere to hold")
	}
	if bad := readsOnly(spec.Value, true, refItem, refSubject); bad != "" {
		return fmt.Errorf("!each `value` reads %s: an element's payload may read the ELEMENT "+
			"(`%s`) and the firing's subject (`%s` and its parts), and nothing else. "+
			"`%s.commit` is refused here for the reason it is refused in a plain derive: it is "+
			"a function of WHEN the firing happened rather than of what it is about, so a derive "+
			"carrying one writes something new on every lap", bad, refItem, refSubject, refSubject)
	}
	return nil
}

// refItem is the element binding inside a fan-out. `item` rather than `resultItem` because
// the construct is meant to work over `subject.value.something` too, and a name that hard-codes
// where the collection came from would have to be renamed or duplicated the first time it does.
const refItem = "item"

// A derive may read its SUBJECT and nothing else — every part of it, including the subject's
// state, but not the occasion of the firing.
//
// The line is not "identity, never state", which is where this started and which was too
// strict. What the ordering actually needs is content THE LEVELS ABOVE CANNOT INFLUENCE, and an
// expression cannot reach another entity at all: it reads the firing's own subject, or a
// task's result. Nothing above can write the subject — a patch touches only its own subject
// and a derive only climbs — so an upward read that wakes the rule without changing the subject
// resolves subject.value to the same bytes, writes them to the same address, and dies as a
// no-op. That is the property "reads may go anywhere" rests on, and subject.value keeps it.
//
// subject.commit does not, and it is the one part refused here. It is a function of WHEN the
// firing happened rather than of the subject, so a read from above changes it:
//
//	L (level 0, subject S, reads D)   derive: { at: .[subject.commit] }
//	H (level 10, subject D)           patch:  { seen: true }
//
//	S changes → L derives D{at: 100} → H patches D → L's read wakes it at commit 102 →
//	D{at: 102}, a real change → H fires → …
//
// Unbounded, across a level, paced only by the throttle — the cycle the ordering exists to rule
// out, and it would cost the read side its freedom (`read at your level or below` would have to
// come back). The engine's own linkage stays commit-free for exactly this reason: recording the
// causing commit at @sys.meta.derivedFrom would reintroduce the loop from inside the engine.
//
// readsOnly is the check, over every expression in a document: the first that reads a name
// outside allowed, or subject.commit, as text to quote; "" when none does. `$env` reads every
// name the environment holds, which is allowed exactly where the environment holds allowed and
// nothing else (env). An expression that does not parse is answered with its error, since what
// it reads cannot be said.
func readsOnly(n entity.Payload, env bool, allowed ...string) string {
	bad := ""
	eachText(n, func(s string) {
		for _, sp := range exprs.Expressions(s) {
			if bad != "" {
				return
			}
			reads, err := exprs.Reads(sp.Expr)
			if err != nil {
				bad = fmt.Sprintf("%q, which does not parse (%v)", sp.Expr, err)
				return
			}
			for _, r := range reads {
				if !deriveMayRead(r, env, allowed) {
					bad = fmt.Sprintf("%q in %q", strings.Join(r, "."), sp.Expr)
					return
				}
			}
		}
	})
	return bad
}

// deriveMayRead is whether a derive may read r: a name in allowed, and never subject.commit.
func deriveMayRead(r exprs.Path, env bool, allowed []string) bool {
	if r[0] == exprs.Env {
		return env
	}
	if !slices.Contains(allowed, r[0]) {
		return false
	}
	return r[0] != refSubject || len(r) < 2 || r[1] != subjPartCommit
}

// eachElements resolves a fan-out's `over` and yields the elements to derive from.
//
// `over` must name a LIST. That is narrower than it could be — a kpath can select across
// structure — and the narrowness is the point: a fan-out over something that is not a list
// would derive it as a single element whose `key` cannot resolve, and a fan-out that quietly
// derives one entity is the failure this whole construct exists to stop being possible. A
// charter that wants a nested answer flattened says so in its answer contract, where the shape is
// already declared, or in the expression: `.[data.groups | flatten()]`.
func (e *Engine) eachElements(spec *eachSpec, env launch.Binding) ([]entity.Payload, error) {
	over, _ := wholeExpr(spec.Over) // install-checked
	node, err := launch.ExpandValue(over, env)
	if err != nil {
		return nil, fmt.Errorf("!each over %s: %w", over, err)
	}
	if node.Type != ir.ArrayType {
		return nil, fmt.Errorf("!each over %s is %s, and a fan-out walks a LIST: ask for one in "+
			"the answer contract (`findings: !irtype []`) rather than deriving from a shape "+
			"nobody declared", over, node.Type)
	}
	return node.Values, nil
}

// eachEnvFor builds the environment ONE element resolves against: the subject, and `item`.
//
// The firing's own payload is deliberately not in it. Install already refuses a `value` that
// reads anything else, and this is the same rule said structurally — a derive reaches its
// subject and its element, and a reader of this function does not have to trust a check
// somewhere else to know that.
func eachEnvFor(subject map[string]any, item entity.Payload) launch.Binding {
	return launch.Binding{refSubject: subject, refItem: anyOf(item)}
}

// eachSegs addresses one derived element: (rule, subject, key), one segment per part.
//
// THE KEY IS A SEGMENT, so it needs neither flattening nor a hash. Both were an ID's: the keys
// this construct invites are exactly the ones a flattening would rewrite — `source/github.Reflect`
// carries a slash, Go's own spelling of a method carries more — and two DISTINCT keys that
// flattened alike shared one entity, which is a fan-out silently losing a member, invisible
// because the ledger simply reads short. Nothing flattens now, so nothing can collide.
func eachSegs(chain string, ref entity.Ref, key string) []string {
	return append(deriveSegs(chain, ref), key)
}

// deriveFromField is where the engine records what a derived entity is about, under the reserved
// root: @sys.meta.derivedFrom = {rule, subject}.
//
// Engine-owned, on the exact precedent a spawn sets — "the subject is the engine's to set, not
// the charter's" — and for the same reason: a derivation is about exactly one subject, and which
// one is the Condition's answer, not something the charter should be trusted to interpolate.
const deriveFromField = "derivedFrom"

// deriveContent resolves what a derive writes: its expressions evaluated against `subject` alone,
// then the engine's linkage merged in.
func (e *Engine) deriveContent(payload entity.Payload, chain string, oc occasion, subj entity.Ref, projection bool) (entity.Payload, error) {
	sn, err := e.deriveSubject(oc, subj, payload)
	if err != nil {
		return nil, err
	}
	// Against an environment holding ONLY the subject, not the payload that fired the action: a
	// derive reaches its own subject and nothing else, and that is structural here rather than a
	// rule enforced only at install.
	expanded, err := expandPatch(payload, launch.Binding{refSubject: sn}, e.readerFor(oc))
	if err != nil {
		return nil, err
	}
	return deriveLinkage(expanded, chain, subj, projection)
}

// deriveSubject is the subject a derive's content reads: the parts its expressions ask for
// (wantsOf), with `commit` cleared whatever they asked for. Install refuses subject.commit in a
// derive, and clearing it here means the expression cannot resolve either: two ends saying the
// same thing, so relaxing one alone cannot open the other. A derive that wrote something new on
// every lap would wake the level above, which wakes the rule again.
func (e *Engine) deriveSubject(oc occasion, subj entity.Ref, content entity.Payload) (map[string]any, error) {
	w := wantsOf(content)
	w.commit = false
	return e.subjectFor(oc, subj, w)
}

// eachContent resolves what ONE element derives: its `value` with `item` and the subject sn
// evaluated, then the engine's linkage. deriveContent's twin. sn is deriveSubject's for
// spec.Value, asked once per firing by the caller: it is the same for every element.
func (e *Engine) eachContent(spec *eachSpec, chain string, oc occasion, subj entity.Ref, sn map[string]any, item entity.Payload) (entity.Payload, error) {
	expanded, err := expandPatch(spec.Value, eachEnvFor(sn, item), e.readerFor(oc))
	if err != nil {
		return nil, err
	}
	return deriveLinkage(expanded, chain, subj, false)
}

// eachKey resolves one element's address suffix.
//
// An empty key FAILS THE FIRING, and this is the residual's open question answered: derive the
// others and record the refusal, or refuse the lot. Refuse. A fan-out that drops a member
// produces a ledger that is quietly short, and short is the one failure a reader cannot see —
// where a refused firing is loud, is about the whole answer, and leaves the previous state
// standing to be re-derived when the answer is fixed.
func eachKey(spec *eachSpec, env launch.Binding, at int) (string, error) {
	which, spelling := spec.Key, "key"
	if spec.ID != nil {
		which, spelling = spec.ID, "id"
	}
	s, err := keyText(which, env)
	if err != nil {
		return "", fmt.Errorf("!each %s on element %d: %w", spelling, at, err)
	}
	return s, nil
}

// keyText resolves a key expression — a fan-out element's, or a single derive's — to the text
// that goes in the address.
//
// An EMPTY key is a failure rather than a blank segment, in both spellings and for one reason:
// an address that silently collapses to the unkeyed one is the fan-out deriving a single entity,
// or the keyed derive re-asking a question it has already had answered. Both are the failure the
// key was written to prevent, and neither is visible in what stands afterwards. A NULL key is
// an empty one, and not the segment `null`: two elements that both lack the field would
// otherwise share an entity. A composite is not a name at all.
func keyText(key entity.Payload, env launch.Binding) (string, error) {
	if lit := literalKey(key); lit != "" {
		return lit, nil // an author-named id: the same address for every subject
	}
	x, _ := wholeExpr(key) // install-checked
	n, err := launch.ExpandValue(x, env)
	if err != nil {
		return "", fmt.Errorf("key %s: %w", x, err)
	}
	s, ok := scalarText(n)
	if !ok {
		return "", fmt.Errorf("key %s is %s, which is not a name: an address segment is the "+
			"text of a scalar", x, ir.Uncomment(n).Type)
	}
	if s == "" {
		return "", fmt.Errorf("key %s is empty: an address read from a value only tells two "+
			"of them apart while every one of them carries it", x)
	}
	return s, nil
}

// deriveLinkage merges the engine's record of what this derivation is about into the content the
// charter wrote.
//
// A PROJECTION — a derive with a literal `id`, one entity for every subject — records the rule
// and not the subject. The entity is about whichever subject fired last, and naming it would
// make every firing new bytes: `mirror.schedule.date` would commit at every hourly tick rather
// than once a day, which is the whole point of a fact standing at its own address.
func deriveLinkage(payload entity.Payload, chain string, subj entity.Ref, projection bool) (entity.Payload, error) {
	from := entity.Obj{"rule": chainKPath(chain)}
	if !projection {
		from["subject"] = subj.String()
	}
	link := entity.Obj{
		entity.ReservedRoot: entity.Obj{
			entity.MetaKey: entity.Obj{
				deriveFromField: from,
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
	// A PATH renders as the address it is, never as the Go map holding it.
	if segs := m.Segs(); len(segs) > 0 {
		out := strings.Join(segs, ".")
		if p := m.Predicate(); p != nil {
			out += " " + matchText(map[string]any{mKeyValue: p})
		}
		return out
	}
	return matchText(map[string]any(m))
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

// checkDeriveKey checks a single derive's `key` — the same two questions `!each`'s key answers,
// minus the one about the element.
//
// It must be ONE expression and nothing else, for the reason a fan-out's must: one embedded in
// text interpolates to a string, and an address assembled out of prose is not one anybody can
// predict or find. And it may read only what the CONTENT may — the subject, including its state
// — since the two are the same claim: the address is a function of the state, which is exactly
// what makes carrying that state in the payload sound.
// It is `id`'s check too: the two differ in what the engine puts in front of the value, not in
// what the value may be read from.
func checkDeriveKey(key entity.Payload, where string) error {
	if key == nil {
		return nil
	}
	x, ok := wholeExpr(key)
	if !ok {
		return fmt.Errorf("action.derive.%s `key` must be one expression and nothing else "+
			"(`key: .[%s.value.sha]`): an expression embedded in a string interpolates to text, "+
			"and an address is not something to assemble out of prose", where, refSubject)
	}
	if bad := readsOnly(key, true, refSubject); bad != "" {
		return fmt.Errorf("action.derive.%s `key` is %s, which reads %s: a keyed derive's address "+
			"may be read from the firing's SUBJECT and nothing else — %s.commit included, since a "+
			"key that is a function of WHEN the rule fired writes a new entity on every lap",
			where, x, bad, refSubject)
	}
	return nil
}

// checkDeriveID is checkDeriveKey for `id`, which admits one more spelling: A LITERAL. `id:
// date` names one entity for every subject the rule ever fires about — the projection shape,
// where the latest firing's bytes stand at an address a reader can spell. It is sound for the
// reason a keyed derive is: the address is fixed and the content is the subject's, so a
// re-fire about the same subject is a no-op, and a later subject is the later value. The base
// charter's calendar is the customer: `mirror.schedule.date` is derived from every hourly tick.
func checkDeriveID(id entity.Payload, where string) error {
	if literalKey(id) != "" {
		return nil
	}
	return checkDeriveKey(id, where)
}

// literalKey is the text of a key that is a plain string holding no expression, "" otherwise.
func literalKey(key entity.Payload) string {
	key = ir.Uncomment(key)
	if key == nil || key.Type != ir.StringType || key.String == "" || exprs.Templated(key.String) {
		return ""
	}
	return key.String
}

// checkLiteralDerive is the content check both derive spellings share: an object (the engine
// writes the subject linkage into it, and expressions that read the subject alone.
func checkLiteralDerive(payload entity.Payload, where string) error {
	if payload == nil || payload.Type != ir.ObjectType {
		return fmt.Errorf("action.derive.%s must be an object: the engine writes the subject this "+
			"derivation is about into it, which a scalar has nowhere to hold", where)
	}
	if bad := readsOnly(payload, true, refSubject); bad != "" {
		return fmt.Errorf("action.derive.%s reads %s: a derive may read the firing's SUBJECT "+
			"(`%s` and its parts) and nothing else. That is what the whole ordering rests on: "+
			"nothing above a derive's level can write its subject, so a re-fire woken from above "+
			"writes the same bytes to the same address — a no-op the store ends by itself, which "+
			"is why a rule may read a HIGHER level without that read becoming a cycle. "+
			"%s.commit is refused for the same reason: it is a function of WHEN the rule fired, "+
			"so a derive carrying it writes something new on every lap", where, bad, refSubject,
			refSubject)
	}
	return nil
}
