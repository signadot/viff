package trigger

import (
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

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
// IT BOUNDS DAMAGE, NOT INTERFERENCE, and that limit is filed rather than fixed: two packages
// can still feed each other through the store — A reads what B writes while B reads what A
// writes — and neither charter's author can see the loop in their own charter. Nothing catches
// it today, because the level discipline is per package and the cycle throttle bounds a chain
// within one engine. The shape of an answer is an acyclic import graph between packages, where
// a package declares what it reads and the graph is checked at install. See git-issue
// dpz3jxzdh12ksb6mg5n0; it needs two packages deliberately arranged to feed each other, which is
// not what anybody hits on the way in.
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

// At is the ADDRESS of the charter this engine runs — `verse-dev.merge` — and empty for
// baseline. It is the confinement said as a place rather than as a permission, and it is what
// a record has to carry for anything to know afterwards WHOSE rule wrote it.
//
// A rule's name is unique within a charter and not across them: two packages may each hold a
// `review`, which is most of what packages are for. So a name alone does not identify a rule,
// and everything that stored one — a motion's `trigger`, a gate's `origin` — stored half an
// identity. What that cost is in ResumeMotions.
func (e *Engine) At() string { return e.confine.KPath() }

// KnowsElsewhere tells this engine how to ask whether a rule by some name is installed in
// ANOTHER engine of the same verse — a package's charter, in practice.
//
// It exists for one question, asked once at startup: a motion left open by a previous verse
// that does not say which charter opened it (`at` postdates it, and baseline never writes one).
// Baseline is the engine that sweeps those, and "this rule is not in my charter" is the wrong
// test — a package's rule is in nobody's charter as far as baseline can see, so baseline
// settled live package gates as though their rules had been deleted. The rule was there; the
// engine holding it was not this one.
//
// nil is "there is no other engine", which is what a lone baseline has and what most tests
// have, and it restores exactly the old reading.
func (e *Engine) KnowsElsewhere(fn func(name string) bool) { e.elsewhere = fn }

func (e *Engine) knownElsewhere(name string) bool { return e.elsewhere != nil && e.elsewhere(name) }

// confined reports whether a subtree is set at all.
func (s Subtree) confined() bool { return s != Subtree{} }

// match renders the confinement as the pattern the containment checks compare against.
//
// It writes the address into the dimensions a Match has today — root, then system, then kind —
// which is what containment compares. A subtree deeper than that has no dimension left to hold
// its tail, and that is the next step of docs/working/entity-tree.md rather than a silent
// truncation: it is refused where it is parsed (ParseAt), so nothing reaches here that this
// cannot say.
func (s Subtree) match() Match {
	segs := s.Path()
	// DEEPER THAN THE DIMENSIONS is a path, and it must be: truncating to system-and-kind here
	// would make a charter installed at `verse-dev.drift.head` compare as one installed at
	// `verse-dev.drift`, so its confinement would silently widen by a level.
	if len(segs) > 2 || (len(segs) > 0 && segs[0] == entity.VerseRoot && len(segs) > 3) {
		nodes := make([]*ir.Node, 0, len(segs))
		for _, sg := range segs {
			nodes = append(nodes, ir.FromString(sg))
		}
		return Match{mKeyPath: nodes}
	}
	m := Match{}
	if len(segs) > 0 && segs[0] == entity.VerseRoot {
		m[mKeyRoot] = entity.VerseRoot
		segs = segs[1:]
	}
	for i, k := range []string{mKeySystem, mKeyKind} {
		if i < len(segs) && segs[i] != "" {
			m[k] = segs[i]
		}
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
// It asks about WRITES only, and the subject is a write ONLY WHEN THE ACTION PATCHES IT. That
// distinction is the difference between a useful package and a useless one, and the charter we
// run is what says so: comment-drift's `head-moved` selects `git:head:verse` and derives into
// `drift:head` — it reads a source as its subject and writes somewhere else entirely. A rule
// like that belongs to the `drift` package, and a check that asked only "is the subject inside"
// would refuse it, leaving packages unable to react to any source — which two charters both
// reading `git:*` is the ordinary case for, not the exotic one.
//
// So: a `derive` target is always checked, since a derive writes one; the subject is checked
// when the action, one of its completion hooks, a sequence element or a gate's `onDenied`
// carries a `patch`, since that is what writes the subject.
//
// A subject that NAMES one entity is checked the same way a selecting one is — the containment
// question is about the address, and naming does not make it somebody else's to write.
func (e *Engine) checkConfined(t Trigger) error {
	if !e.confine.confined() {
		return nil
	}
	outer := Matches{e.confine.match()}
	if t.writesSubject() && !outer.contains(t.Condition.Subject) {
		return fmt.Errorf("subject %s is outside %s: a charter installed at an address may only "+
			"write inside it, and this rule patches the subject it selects (a rule that only "+
			"READS its subject — deriving elsewhere from it — may select anywhere)",
			subjectText(t.Condition.Subject), e.confine)
	}
	for _, d := range t.Action.allDerives() {
		if !outer.contains(d.match()) {
			return fmt.Errorf("derive into %s is outside %s: a charter installed at an address "+
				"may only write inside it", d.addr(), e.confine)
		}
	}
	return nil
}

// A STRATUM IS NOT CONFINED, and the charter we run is why: comment-drift's lowest level,
// `reflected`, owns `system: git` — the source slice its rules select from and never write. A
// stratum says which subjects a level OWNS for ordering purposes, and a package's rules select
// from what they read, so a stratum naming a source is the ordinary case rather than a reach.
//
// Nothing is lost by leaving it open. What a stratum governs is the DIRECTION of a derive
// (strictly higher), and where a derive may land is checked against the confinement on its own
// — so a level naming somebody else's slice cannot become a write into it.

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
// A PLACE OF ANY DEPTH: `demo`, `demo.pr`, `verse-dev.drift.head`. It used to be one or two
// segments, because a charter writes where it is installed and an address was three deep — so a
// third segment named an entity rather than a place. An address is as deep as the tree now
// (docs/working/entity-tree.md), and so is a charter's.
//
// `verse` is still refused as the first segment: verse's own half is not somebody's to install a
// charter into.
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
	for _, sg := range segs {
		if sg == "" {
			return Subtree{}, fmt.Errorf("a charter's address has an empty segment: %q", at)
		}
	}
	return SubtreeAt(segs...), nil // no root: a charter's address never names one
}

// writesSubject reports whether a rule writes the entity it selects — which is what makes the
// subject's address a write rather than a read.
//
// A `patch` is the only thing that writes the subject: a `derive` writes the address it names,
// a `spawn` writes a task record in verse's own half, and neither is the subject. The walk goes
// through the same places `allDerives` looks — hooks, sequence elements — plus a gate's
// `onDenied`, which is an action about the same subjects and is how a refusal is recorded.
func (t Trigger) writesSubject() bool {
	if t.Action.writesSubject() {
		return true
	}
	for _, g := range t.AllGates() {
		if g.OnDenied != nil && g.OnDenied.writesSubject() {
			return true
		}
	}
	return false
}

func (a Action) writesSubject() bool {
	if a.Patch != nil {
		return true
	}
	for _, el := range a.Seq {
		if el.writesSubject() {
			return true
		}
	}
	for _, h := range []*Action{a.OnDone, a.OnFailed, a.OnCancelled, a.OnOrphaned} {
		if h != nil && h.writesSubject() {
			return true
		}
	}
	return false
}
