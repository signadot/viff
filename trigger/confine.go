package trigger

import (
	"fmt"
	"strings"

	"github.com/signadot/verse/entity"
)

// A CONFINED ENGINE may only write inside one subtree of the verse.
//
// It is what makes a charter's isolation a guarantee rather than a convention. Two groups
// running rules in one verse are isolated when neither can write where the other reads, and
// "neither does today" is a property of the rules somebody happens to have installed — it stops
// being true the moment one is edited. A confinement is a property of the ENGINE, so it holds
// over whatever the charter says next.
//
// READS ARE NOT CONFINED, deliberately. A rule's `on`/`when` is a cause, and "react to what
// happened over there, record what it means over here" is the shape most real rules have: the
// drift charter reads `git:*` and writes `drift:*`. What isolation needs is that nobody else's
// state MOVES, and a read moves nothing.
//
// Two checks, and both are needed:
//
//   - At INSTALL, statically: a rule whose subject or derive target provably falls outside is
//     refused, so the failure lands on whoever wrote it, at the moment they wrote it, naming
//     the address. This is `Matches.contains` — the same containment `checkLevels` asks about
//     a stratum — asked of one more outer pattern.
//   - At RUN, dynamically: `patchEntity` refuses a write outside. This is the half that makes
//     it a guarantee: a derive's address can be templated (`!each` keys, `$subject` in a key),
//     and a static check over templates is best-effort, which stratum.go's own opening comment
//     already names as the wrong shape for a promise.
//
// What it does NOT confine is a spawn. A rule's task acts in the world under its `grant`, and
// store confinement is not world confinement — see docs/provider-selection.md for the dial that
// is. Nor does it confine what the ENGINE writes about its own running (a task's mirror, a
// motion, a posed question): those are verse's, they land in verse's half of the tree, and a
// charter cannot address them anyway.

// Confine narrows what this engine's charter may write to one subtree. The zero subtree is no
// confinement, which is what a baseline engine has.
//
// It is set before rules are installed and does not change afterwards: a confinement that could
// be widened later is not one, since the rules that were refused under the old one are exactly
// the ones somebody would widen it for.
func (e *Engine) Confine(at Subtree) { e.confine = at }

// Confinement is the subtree this engine may write in, zero when it may write anywhere.
func (e *Engine) Confinement() Subtree { return e.confine }

// confined reports whether a subtree is set at all.
func (s Subtree) confined() bool { return s != Subtree{} }

// match renders the confinement as the pattern the containment checks compare against.
func (s Subtree) match() Match {
	m := Match{}
	if s[0] != "" {
		m[mKeyRoot] = s[0]
	}
	if s[1] != "" {
		m[mKeySystem] = s[1]
	}
	if s[2] != "" {
		m[mKeyKind] = s[2]
	}
	return m
}

// String is the confinement as an address prefix — what an error message shows.
func (s Subtree) String() string {
	if !s.confined() {
		return "*"
	}
	return strings.Join(s.Path(), ":") + ":*"
}

// checkConfined refuses a rule that would write outside this engine's confinement.
//
// It asks about WRITES only: the subject, because a `patch` writes it, and every derive target,
// because a derive writes one. `on` and `when` are reads and are not asked about.
//
// A subject that NAMES one entity is checked the same way a selecting one is — the containment
// question is about the address, and naming does not make it somebody else's to write.
func (e *Engine) checkConfined(t Trigger) error {
	if !e.confine.confined() {
		return nil
	}
	outer := Matches{e.confine.match()}
	if !outer.contains(t.Condition.Subject) {
		return fmt.Errorf("subject %s is outside %s: a charter installed at an address may only "+
			"write inside it, and its action patches the subject it selects",
			subjectText(t.Condition.Subject), e.confine)
	}
	for _, d := range t.Action.allDerives() {
		if !outer.contains(Match{mKeySystem: d.System, mKeyKind: d.Kind}) {
			return fmt.Errorf("derive into %s is outside %s: a charter installed at an address "+
				"may only write inside it", d.addr(), e.confine)
		}
	}
	return nil
}

// checkConfinedStratum refuses a stratum whose patterns reach outside the confinement.
//
// A stratum says which subjects it owns, and a rule's subject must fall inside its stratum's
// pattern (checkLevels). A stratum reaching outside would therefore be either inert — no rule
// inside the confinement can satisfy it — or a claim about somebody else's addresses, and both
// are better refused than left standing as an ordering nothing can use.
func (e *Engine) checkConfinedStratum(st Stratum) error {
	if !e.confine.confined() {
		return nil
	}
	outer := Matches{e.confine.match()}
	for _, m := range st.Match {
		if !outer.contains(m) {
			return fmt.Errorf("stratum %q owns %s, which is outside %s: a charter's levels are "+
				"about its own addresses", st.Name, matchesText(st.Match), e.confine)
		}
	}
	return nil
}

// mayWrite is the run-time half: the refusal that makes the confinement a guarantee rather than
// a check somebody's template can walk past.
//
// It is asked on the CHARTER's writes — a patch, a derive — and not on the engine's own, which
// are about verse's running rather than about the world and live in the reserved half.
func (e *Engine) mayWrite(ref entity.Ref) error {
	if !e.confine.confined() {
		return nil
	}
	if e.confine.covers(ref) {
		return nil
	}
	return fmt.Errorf("%s is outside %s: this charter may only write inside the address it was "+
		"installed at", ref, e.confine)
}

// stamp is the confinement as a charter record writes it down — the address a package was
// installed at, empty for a charter that may write anywhere.
//
// It is the kinded-path spelling (`demo.pr`) rather than the ref one, because it names a
// SUBTREE rather than an entity, and the two grammars already split that way: a colon address
// names a thing, a dotted path names a place.
func (s Subtree) Stamp() string { return s.stamp() }

func (s Subtree) stamp() string {
	if !s.confined() {
		return ""
	}
	return strings.Join(s.Path(), ".")
}

// ParseAt reads a charter's address — the subtree a package is installed at — from the kinded
// path an operator types or a record stamps.
//
// One or two segments below a root: `demo`, `demo.pr`, `verse.task` — except that last one is
// refused, since verse's own half is not somebody's to install a charter into.
func ParseAt(at string) (Subtree, error) {
	if at == "" {
		return Subtree{}, nil
	}
	segs := strings.Split(at, ".")
	if segs[0] == entity.VerseRoot {
		return Subtree{}, fmt.Errorf("a charter may not be installed at %s: %s is verse's own "+
			"half of the tree, and a charter installed there could write the records verse "+
			"keeps about its own running", at, entity.VerseRoot)
	}
	if len(segs) > 2 {
		return Subtree{}, fmt.Errorf("a charter's address is a system and optionally a kind "+
			"(`demo`, `demo.pr`); %q names %d segments, which is an entity rather than a place "+
			"to install a charter", at, len(segs))
	}
	var out Subtree
	for i, sg := range segs {
		if sg == "" {
			return Subtree{}, fmt.Errorf("a charter's address has an empty segment: %q", at)
		}
		out[i+1] = sg // [0] is the root, which a charter's address never names
	}
	return out, nil
}
