package source

import (
	"fmt"
	"sort"
	"strings"

	"github.com/signadot/verse/entity"
)

// An ownership claim says what no other source may write, and it is spelled the way every other
// address in verse is — one pattern per level, at whatever depth — with `*` allowed at any of
// them and a trailing one making a prefix: `git.*.verse/*`, or just `git` for the whole system.
//
// It is NOT an entity.Pattern, which is what a Reflection's Covers is and what a charter's
// `when` becomes, and the difference is where the two questions are asked rather than a
// vocabulary nobody tidied. Converge evaluates Covers against stored entities whose payloads it
// can read. An install compares two DECLARATIONS, with no entities in hand — so it can only
// reason about what a claim says syntactically, and **addresses compare statically where payload
// predicates do not**.
//
// Proving predicates apart syntactically is also worse than it looks. A null in a predicate is a
// PRESENCE test (see entity.Pattern.Predicate), so the obvious rule — same field, different
// constants — proves `{repo: null}` disjoint from `{repo: verse}` when the second entails the
// first: a wrong REFUSAL, from a check whose whole job is to be conservative.
//
// A claim addresses a PLACE, not only an entity. Field continues the address into the payload,
// the way entity.Path does for a write — so two sources can share an entity without either
// being a second writer of anything: one is the single writer of `owners.ENG`, the other of
// `owners.OPS`, and they are proved apart by the same string comparison one segment further
// down. Sharing is not an exception to single-writership; it is single-writership at a deeper
// address (docs/working/shared-ownership.md).
//
// THE PATH IS NOT PART OF THE PARSEABLE SPELLING, and that is a fact about ids rather than a
// first version. `@` — the obvious separator — is trigger.ChainSep and already appears inside
// ids: `scripts/lineage.sh` writes `lineage.trace.<rule>@<subject>`, which carries both a `@`
// and two colons, so no split rule recovers a path from one end or the other. A claim that
// carries a path is therefore spelled as a document; ParseClaim reads the terse form, which is
// every claim in the tree today and every claim a person types.
//
// The cost is stated in docs/working/sources-residual.md and worth knowing: this works when a
// source's addresses encode ownership positionally, which git's do because a repository is a
// level of every address git.go writes. What is still not spellable here is ownership that is a VALUE rather than a
// place — an owner in the payload and a uuid in the ref. That wants a predicate, and predicates
// do not compare statically; the shape one would have to take is in shared-ownership.md.

// Own is one ownership claim as a source DECLARES it, and as a registration stores and serves
// it: an address, and — when the claim is to a place inside an entity rather than to the whole
// of it — the path to that place.
//
// It is a document and not a string, which is a decision the ids forced (see above): there is
// no terse spelling of a path-carrying claim, and a grammar with two forms would have to be
// read twice everywhere. The terse spelling survives where it belongs, as the value of `at` —
// an address has a spelling, and a path is a list.
//
//	{at: "git.*.verse/*"}
//	{at: "linear.project.*", path: [owners, ENG]}
//
// `at` IS THE ADDRESS AND `path` IS THE PLACE INSIDE IT, which is what every other surface
// means by the two words: `!get-path {at: [...], path: sha}` reads a field of an entity, and
// `POST /entity` takes an `at`. Spelling them any other way here would make `at` an address in
// one document and a payload path in the next.
//
// It is DECLARED but not PARSED: nothing compares two of these. ParseOwns turns them into
// Claims, once, and everything that reasons about ownership takes those — so there is no path
// through this package on which an unparseable claim is silently skipped.
type Own struct {
	// At is the address, as an address glob: a level may be `*`, and may end in one.
	// ParseClaim is what reads it.
	At string `tony:"field=at"`
	// Path is the path inside the payload, empty for the whole entity.
	Path []string `tony:"field=path,omitzero"`
}

// String renders a declaration for a message, via the claim it means.
func (o Own) String() string {
	c, err := o.Claim()
	if err != nil {
		return o.At // unparseable: quote what was written, which is what the error is about
	}
	return c.String()
}

// Claim parses one declaration.
func (o Own) Claim() (Claim, error) {
	c, err := ParseClaim(o.At)
	if err != nil {
		return Claim{}, err
	}
	c.Field = o.Path
	return c, nil
}

// ParseOwns parses a whole ownership list, once, for a caller that is about to reason with it.
//
// It is the only way into the comparison functions below, and that is the point: `owns` used to
// be stored as text and re-parsed at every call site, two of which skipped a claim that failed
// to parse — including the one that decides what a removal may delete. A skip there is an
// entity deleted because a claim could not be read. Here a bad claim is an error, once, and the
// caller that deletes has to say what it does about it.
func ParseOwns(owns []Own) ([]Claim, error) {
	out := make([]Claim, 0, len(owns))
	for _, o := range owns {
		c, err := o.Claim()
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Claim is one parsed ownership claim: a pattern per LEVEL of the address, at whatever depth,
// and optionally a path inside the payload.
//
// IT IS A TREE. Three named positions with three different rules — the first two a literal or
// `*`, the third also a prefix glob — is the arity written down three times, and it cannot say
// what a source owns once entities are deeper than three segments. One Seg per level says the
// rule once, at every level, and a claim as short as `git` owns the whole subtree under it.
type Claim struct {
	// Segs is the address pattern, one per level. A claim SHORTER than an address covers
	// everything under it, which is what makes `git` a claim on the whole system.
	Segs []Seg
	// Field is the path inside the payload this claim is about — {"owners","ENG"} is the `ENG`
	// key of the `owners` object, and it is the same address entity.Path.Field names for a
	// write. Empty is the whole entity.
	//
	// A claim with no path CONTAINS every claim under it, which is why an empty Field is a
	// prefix of everything rather than a wildcard: a source claiming a whole entity and one
	// claiming a slot inside it are two writers of that slot.
	Field []string
}

// Seg is one level of a claim: a literal name, or a literal PREFIX when Prefix is set.
//
// `*` IS THE EMPTY PREFIX, not a case of its own — every name begins with "" — so the wildcard
// and the glob are one comparison rather than two, and one three-way switch (both prefixes, one
// prefix, neither) is the whole of it at every level.
type Seg struct {
	Name   string
	Prefix bool
}

// Disjoint reports whether no name satisfies both segments.
func (s Seg) Disjoint(o Seg) bool {
	switch {
	case s.Prefix && o.Prefix:
		// Two prefixes share a name iff one extends the other.
		return !strings.HasPrefix(s.Name, o.Name) && !strings.HasPrefix(o.Name, s.Name)
	case s.Prefix:
		return !strings.HasPrefix(o.Name, s.Name)
	case o.Prefix:
		return !strings.HasPrefix(s.Name, o.Name)
	default:
		return s.Name != o.Name
	}
}

func (s Seg) String() string {
	if s.Prefix {
		return s.Name + "*"
	}
	return s.Name
}

// Head is the claim's first level as an EXACT name, and whether it is one — what a caller
// narrowing a store read may use as a prefix. A glob or a `*` there answers false, and the
// caller must read wider: a claim that does not say which system it is in cannot narrow one.
func (c Claim) Head() (string, bool) {
	if len(c.Segs) == 0 || c.Segs[0].Prefix {
		return "", false
	}
	return c.Segs[0].Name, true
}

// ParseClaim reads a claim from its address-glob spelling.
//
// It is the ADDRESS GRAMMAR — `entity.ExprSegs`, the same reader a charter's subject goes
// through — rather than a splitter of its own. A splitter here agrees with the address grammar
// only for as long as the two happen to spell things alike, and a claim is written beside the
// addresses it covers: the moment they differ, `owns` is the one place spelling them wrong.
//
// A GLOB IS A LITERAL PREFIX AND A TRAILING `*`, at every level, and nothing richer. That is a
// deliberate floor rather than a first version: prefix-disjointness is a string comparison,
// while disjointness of two general globs is an automata intersection — and the whole reason
// ownership is an address is to stop proving things the hard way. A `*` anywhere else is
// refused rather than approximated, because a claim that quietly meant something narrower than
// it looked would be a single-writer guarantee resting on a typo.
//
// The rule is the same at every level rather than the last one's alone. Restricting the first
// two to a literal or a bare `*` would be an arity restriction wearing a safety argument: the
// comparison is a string prefix wherever it is made.
func ParseClaim(s string) (Claim, error) {
	// A `*` ENDS ITS LEVEL, checked on the string before anything reads it. The address grammar
	// treats a `*` as self-delimiting, so `git.ref.*/main` parses happily as FOUR levels — `git`,
	// `ref`, `*`, `/main` — which is a claim nobody wrote, and a wider one than the id glob that
	// was meant. Refusing here keeps that from being a silent approximation.
	for i := 0; i < len(s); i++ {
		if s[i] == '*' && i+1 < len(s) && s[i+1] != '.' {
			return Claim{}, fmt.Errorf("ownership claim %q: a glob is a prefix with a TRAILING "+
				"`*` (verse/*, or `*` for a whole level) — a `*` anywhere else is refused rather "+
				"than approximated, since a claim meaning something narrower (or wider) than it "+
				"looks is a single-writer guarantee resting on a typo", s)
		}
	}
	parts, err := entity.ExprSegs(s)
	if err != nil {
		return Claim{}, fmt.Errorf("ownership claim %q: %w", s, err)
	}
	if len(parts) == 0 {
		return Claim{}, fmt.Errorf("ownership claim %q: want an address, one pattern per level, "+
			"where a level may be `*` and may end in one (git.*.verse/*)", s)
	}
	// NOTHING IN VERSE'S OWN HALF, said at the parse rather than only where a removal reads the
	// claim: a source reflects the world, and the half verse keeps about its own running is not
	// a slice anybody may register for. A claim whose first level is a `*` still reaches it, and
	// Claimed refuses that at the point — this catches the one somebody wrote down.
	if parts[0] == entity.VerseRoot {
		return Claim{}, fmt.Errorf("ownership claim %q: %q is what verse writes about its own "+
			"running, and no source may claim it — a source reflects the world", s, entity.VerseRoot)
	}
	var c Claim
	for _, p := range parts {
		if p == "" {
			return Claim{}, fmt.Errorf("ownership claim %q: a level is a name, and none of them "+
				"may be empty", s)
		}
		if star := strings.Index(p, "*"); star >= 0 {
			if star != len(p)-1 {
				return Claim{}, fmt.Errorf("ownership claim %q: a glob is a prefix with a "+
					"TRAILING `*` (verse/*) — a `*` anywhere else is refused rather than "+
					"approximated, since a claim meaning something narrower than it looks is a "+
					"single-writer guarantee resting on a typo", s)
			}
			c.Segs = append(c.Segs, Seg{Name: p[:star], Prefix: true})
			continue
		}
		c.Segs = append(c.Segs, Seg{Name: p})
	}
	return c, nil
}

// String spells the claim back, so a refusal can quote what it was given.
//
// A path-less claim round-trips through ParseClaim, which is what makes a refusal quote the
// operator's own words. A claim WITH a path does not and cannot — there is no terse spelling of
// one (see above) — so it renders for a person to read rather than for a parser: the address,
// then `at`, then the path. Nothing consumes this form.
func (c Claim) String() string {
	segs := make([]string, 0, len(c.Segs))
	for _, s := range c.Segs {
		// A bare `*` stays bare: a wildcard is a position rather than a name. Everything else is
		// quoted where the address grammar needs it, with the trailing glob left OUTSIDE the
		// quotes, since it belongs to the pattern rather than to the name.
		if s.Prefix && s.Name == "" {
			segs = append(segs, "*")
			continue
		}
		out := entity.JoinSegs([]string{s.Name})
		if s.Prefix {
			out += "*"
		}
		segs = append(segs, out)
	}
	out := strings.Join(segs, ".")
	if len(c.Field) > 0 {
		out += " at " + strings.Join(c.Field, ".")
	}
	return out
}

// Disjoint reports whether no PLACE can satisfy both claims.
//
// It PROVES, and answers false whenever it cannot. Two claims are disjoint when ANY level is,
// since a place must satisfy every one of them — and when neither parts company first, the
// SHORTER one contains the longer, exactly as a payload path does. That is the whole of it: one
// walk, the same walk the field path already used, asked once of the address and once inside
// the payload.
//
// A shorter claim is not a weaker one: `git` owns everything under it, so it overlaps
// `git.verse.ref.x` rather than being disjoint from it. That is what makes a subtree claim mean
// something, and it is the case the three fixed dimensions could not express at all.
func (c Claim) Disjoint(o Claim) bool {
	// The addresses overlap or they do not; where they do, the payload paths decide.
	return !entity.Overlap(c.Segs, o.Segs, Seg.Disjoint) || pathsDisjoint(c.Field, o.Field)
}

// pathsDisjoint reports whether two payload paths can share no place. They cannot when neither
// contains the other, and one contains the other when it is a prefix of it: a claim on `owners`
// takes `owners.ENG` with it, and a claim on nothing at all takes the whole entity.
//
// SEGMENT-WISE, and never as a string. `owners.ENG` and `owners.ENGINE` are two different
// fields, and a string-prefix test would say the first contains the second and refuse two
// perfectly disjoint sources — the same false alarm `git.*.verse*` makes against
// `verse-extras/main`, which is the mistake the id grammar is shaped to avoid. Here it costs
// nothing to avoid, because a path is a list of segments before it is ever a string.
func pathsDisjoint(a, b []string) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return true // they part company here, so no place is under both
		}
	}
	// One is a prefix of the other — including the case where one is empty, which is the whole
	// entity and so contains every path in it.
	return false
}

// Disjoint reports whether every claim of a is provably disjoint from every claim of b — the
// question an install asks of two registrations.
//
// EVERY pair, because ownership is a union: one claim overlapping one other claim is two writers
// on that slice, whatever the rest of the two lists say.
//
// An unparseable claim is an error rather than a false, so an install refuses with the reason
// instead of with the conclusion. A registration that claims NOTHING is disjoint from
// everything, which is the honest reading of an empty list — see the note in
// docs/sources.md on why a source that declines to say gets no ownership guessed
// for it.
func Disjoint(a, b []Own) (bool, error) {
	as, err := ParseOwns(a)
	if err != nil {
		return false, err
	}
	bs, err := ParseOwns(b)
	if err != nil {
		return false, err
	}
	for _, x := range as {
		for _, y := range bs {
			if !x.Disjoint(y) {
				return false, nil
			}
		}
	}
	return true, nil
}

// Claimed returns the PLACES at ref that these claims own — an entity.Path each, whose Field is
// empty when the claim is to the whole entity. Empty when they own nothing there.
//
// It is Disjoint asked about a point: a ref is a claim with no wildcards, and a claim covers it
// exactly when the two are not disjoint. Naming it rather than open-coding that identity twice,
// because the two callers are far apart — an install proving two registrations are separable,
// and a removal deciding what a `--forget` may tombstone — and they must not drift, or verse
// would delete on removal something it never proved this source owned.
//
// PLACES RATHER THAN A YES/NO, and that is what a slot claim forces. "Does this source own
// something at this ref" is the wrong question for the caller that deletes: a source owning
// `owners.ENG` of a shared project owns nothing else in it, and a removal that took the entity
// would take the other owners' slots with it. So the answer says WHERE, and a `--forget` writes
// a tombstone at each place it is given — which for every claim in the tree today is the entity
// itself, exactly as it was.
//
// A place CONTAINED by another is dropped, so a source that claims both an entity and a slot
// inside it is asked to delete the entity once rather than twice. Deleting the deeper one after
// the shallower would write into an entity that is already gone.
func Claimed(claims []Claim, ref entity.Ref) []entity.Path {
	// NOTHING IN VERSE'S OWN HALF, whatever a claim says. ParseClaim refuses one that NAMES it,
	// but a wildcard first level — `*.*.*`, which a source with no neighbour may register —
	// still reaches `verse.task.instance.x` and `verse.trigger.rule.y` as readily as the world's. The caller that deletes writes straight to the store, below
	// the refusal that guards the HTTP face, so `verse source remove --forget` was the one
	// client-reachable path that could tombstone the charter. A source reflects the world; the
	// half verse keeps about itself is not a slice anybody may claim.
	if ref.Reserved() {
		return nil
	}
	// The ref AS A CLAIM: one exact level per segment, at whatever depth it is. A claim covers
	// it exactly when the two are not disjoint, which is how a short claim (`git`) is asked
	// about a long address without either side knowing how deep the other goes.
	point := Claim{Segs: make([]Seg, 0, len(ref.Segs()))}
	for _, s := range ref.Segs() {
		point.Segs = append(point.Segs, Seg{Name: s})
	}
	var out []entity.Path
	for _, c := range claims {
		// A claim DEEPER than the ref covers something beneath it, not the ref: `slack.*.ws/*`
		// owns the workspaces under a channel, and a scalar standing at `slack.foo` is not one of
		// them. Disjoint answers "could they meet" and says yes to that pair, since one is an
		// ancestor of the other — which is right for an install and wrong for what a removal
		// deletes, where it tombstoned the ancestor and everything beside the claim with it.
		if len(c.Segs) > len(point.Segs) || c.Disjoint(point) {
			continue
		}
		out = append(out, ref.At(c.Field...))
	}
	return shallowest(out)
}

// Node is the place in the tree this claim's owner record sits at: the claim's levels up to
// the first that is a glob, and whether there is one. `git.verse` is its own node;
// `linear.issue.ENG-*` is `linear.issue`, the deepest literal place everything it could match
// lies beneath. A claim whose first level is a glob names no place, and answers false.
//
// IT IS OWNERSHIP, NOT SINGLE-WRITERSHIP. Two sources whose claims are disjoint may share a
// node — two teams' issues both under `linear.issue` — and the claims still prove neither writes
// the other's slice. What the node decides is who is answerable for what is reflected beneath
// it: whoever installed the source, so two sources sharing one must share an installer.
func (c Claim) Node() (entity.Ref, bool) {
	var segs []string
	for _, s := range c.Segs {
		if s.Prefix {
			break
		}
		segs = append(segs, s.Name)
	}
	if len(segs) == 0 {
		return entity.Ref{}, false
	}
	return entity.RefAt(segs...), true
}

// Nodes is the places a registration's claims own, shallowest first and with any node beneath
// another dropped: an owner record at `git` already owns `git.verse`.
func Nodes(claims []Claim) []entity.Ref {
	var all []entity.Ref
	for _, c := range claims {
		if n, ok := c.Node(); ok {
			all = append(all, n)
		}
	}
	sort.Slice(all, func(i, j int) bool { return len(all[i].Segs()) < len(all[j].Segs()) })
	var out []entity.Ref
	for _, n := range all {
		within := false
		for _, o := range out {
			if under(n.Segs(), o.Segs()) {
				within = true
				break
			}
		}
		if !within {
			out = append(out, n)
		}
	}
	return out
}

// under reports whether segs is at or beneath prefix.
func under(segs, prefix []string) bool { return entity.Under(prefix, segs, entity.Literal) }

// shallowest drops any place another place already contains, duplicates included.
func shallowest(places []entity.Path) []entity.Path {
	var out []entity.Path
	for _, p := range places {
		held := false
		for _, q := range out {
			if holds(q.Ref.Segs(), p.Ref.Segs()) {
				held = true
				break
			}
		}
		if held {
			continue
		}
		kept := out[:0:0]
		for _, q := range out {
			if !holds(p.Ref.Segs(), q.Ref.Segs()) {
				kept = append(kept, q)
			}
		}
		out = append(kept, p)
	}
	return out
}

// holds reports whether the place at path a contains the one at b — a is a prefix of b, so
// deleting a takes b with it.
func holds(a, b []string) bool { return len(a) <= len(b) && !pathsDisjoint(a, b) }
