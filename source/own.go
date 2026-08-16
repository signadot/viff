package source

import (
	"fmt"
	"strings"

	"github.com/signadot/verse/entity"
)

// An ownership claim says what no other source may write, and it is spelled the way every other
// address in verse is — `<system>:<kind>:<id>` — with `*` allowed: `git:*:verse/*`.
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
// ids: `scripts/lineage.sh` writes `lineage:trace:<rule>@<subject>`, which carries both a `@`
// and two colons, so no split rule recovers a path from one end or the other. A claim that
// carries a path is therefore spelled as a document; ParseClaim reads the terse form, which is
// every claim in the tree today and every claim a person types.
//
// The cost is stated in docs/working/sources-residual.md and worth knowing: this works when a
// source's ids encode ownership positionally, which git's do because git.go prefixes every id
// with the repo. What is still not spellable here is ownership that is a VALUE rather than a
// place — an owner in the payload and a uuid in the ref. That wants a predicate, and predicates
// do not compare statically; the shape one would have to take is in shared-ownership.md.

// Own is one ownership claim as a source DECLARES it, and as a registration stores and serves
// it: an address, and — when the claim is to a place inside an entity rather than to the whole
// of it — the path to that place.
//
// It is a document and not a string, which is a decision the ids forced (see above): there is
// no terse spelling of a path-carrying claim, and a grammar with two forms would have to be
// read twice everywhere. The terse spelling survives where it belongs, as the value of `ref` —
// an address has a spelling, and a path is a list.
//
//	{ref: "git:*:verse/*"}
//	{ref: "linear:project:*", at: [owners, ENG]}
//
// It is DECLARED but not PARSED: nothing compares two of these. ParseOwns turns them into
// Claims, once, and everything that reasons about ownership takes those — so there is no path
// through this package on which an unparseable claim is silently skipped.
type Own struct {
	// Ref is the address, as a ref glob: `<system>:<kind>:<id>`, any segment `*`, an id
	// ending in one. ParseClaim is what reads it.
	Ref string `tony:"field=ref"`
	// At is the path inside the payload, empty for the whole entity.
	At []string `tony:"field=at,omitzero"`
}

// String renders a declaration for a message, via the claim it means.
func (o Own) String() string {
	c, err := o.Claim()
	if err != nil {
		return o.Ref // unparseable: quote what was written, which is what the error is about
	}
	return c.String()
}

// Claim parses one declaration.
func (o Own) Claim() (Claim, error) {
	c, err := ParseClaim(o.Ref)
	if err != nil {
		return Claim{}, err
	}
	c.Field = o.At
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

// Claim is one parsed ownership claim.
type Claim struct {
	// System and Kind are a literal or "" for `*` (which matches any).
	System, Kind string
	// ID is the literal id, or the literal PREFIX when Prefix is set. Empty with Prefix set is
	// `*`: every id.
	ID string
	// Prefix reports that ID is a prefix rather than the whole thing — the trailing `*`.
	Prefix bool
	// Field is the path inside the payload this claim is about — {"owners","ENG"} is the `ENG`
	// key of the `owners` object, and it is the same address entity.Path.Field names for a
	// write. Empty is the whole entity, which is every claim ParseClaim can produce.
	//
	// A claim with no path CONTAINS every claim under it, which is why an empty Field is a
	// prefix of everything rather than a wildcard: a source claiming a whole entity and one
	// claiming a slot inside it are two writers of that slot.
	Field []string
}

// ParseClaim reads a claim from its ref-glob spelling.
//
// An id glob is a literal prefix and an optional TRAILING `*`, and nothing richer. That is a
// deliberate floor rather than a first version: prefix-disjointness is a string comparison,
// while disjointness of two general globs is an automata intersection — and the whole reason
// ownership is an address is to stop proving things the hard way. A `*` anywhere else is
// refused rather than approximated, because a claim that quietly meant something narrower than
// it looked would be a single-writer guarantee resting on a typo.
func ParseClaim(s string) (Claim, error) {
	// Three ways, so an id may contain colons — the same split entity.ParseRef makes, for the
	// same reason: the id is whatever is left.
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Claim{}, fmt.Errorf("ownership claim %q: want <system>:<kind>:<id>, where any "+
			"segment may be * and an id may end in one (git:*:verse/*)", s)
	}
	var c Claim
	for i, seg := range []string{parts[0], parts[1]} {
		name := [2]string{"system", "kind"}[i]
		if seg == "*" {
			continue
		}
		if strings.Contains(seg, "*") {
			return Claim{}, fmt.Errorf("ownership claim %q: the %s may be a literal or `*`, "+
				"not a glob — only an id may carry a prefix glob", s, name)
		}
		if i == 0 {
			c.System = seg
		} else {
			c.Kind = seg
		}
	}
	id := parts[2]
	if star := strings.Index(id, "*"); star >= 0 {
		if star != len(id)-1 {
			return Claim{}, fmt.Errorf("ownership claim %q: an id glob is a prefix with a "+
				"TRAILING `*` (verse/*) — a `*` anywhere else is refused rather than "+
				"approximated, since a claim meaning something narrower than it looks is a "+
				"single-writer guarantee resting on a typo", s)
		}
		c.ID, c.Prefix = id[:star], true
	} else {
		c.ID = id
	}
	return c, nil
}

// String spells the claim back, so a refusal can quote what it was given.
//
// A path-less claim round-trips through ParseClaim, which is what makes a refusal quote the
// operator's own words. A claim WITH a path does not and cannot — there is no terse spelling of
// one (see above) — so it renders for a person to read rather than for a parser: the ref, then
// `at`, then the path. Nothing consumes this form.
func (c Claim) String() string {
	seg := func(s string) string {
		if s == "" {
			return "*"
		}
		return s
	}
	id := c.ID
	if c.Prefix {
		id += "*"
	}
	out := seg(c.System) + ":" + seg(c.Kind) + ":" + seg(id)
	if len(c.Field) > 0 {
		out += " at " + strings.Join(c.Field, ".")
	}
	return out
}

// Disjoint reports whether no PLACE can satisfy both claims.
//
// It PROVES, and answers false whenever it cannot. Two claims are disjoint when ANY segment is,
// since a place must satisfy all four: two literal systems that differ, two literal kinds that
// differ, two ids that cannot be the same string, or two paths neither of which contains the
// other. A `*` segment is disjoint from nothing, which is why a claim of `git:*:*` is disjoint
// from nothing else in git — it claims the lot.
//
// The path is one more segment and not a new kind of reasoning, which is the whole reason it is
// spelled as an address: adding a fourth string comparison to three is a change to this
// function's length and not to what it is able to prove.
func (c Claim) Disjoint(o Claim) bool {
	if c.System != "" && o.System != "" && c.System != o.System {
		return true
	}
	if c.Kind != "" && o.Kind != "" && c.Kind != o.Kind {
		return true
	}
	if idsDisjoint(c, o) {
		return true
	}
	return pathsDisjoint(c.Field, o.Field)
}

// pathsDisjoint reports whether two payload paths can share no place. They cannot when neither
// contains the other, and one contains the other when it is a prefix of it: a claim on `owners`
// takes `owners.ENG` with it, and a claim on nothing at all takes the whole entity.
//
// SEGMENT-WISE, and never as a string. `owners.ENG` and `owners.ENGINE` are two different
// fields, and a string-prefix test would say the first contains the second and refuse two
// perfectly disjoint sources — the same false alarm `git:*:verse*` makes against
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

// idsDisjoint is the whole of the id comparison, and it is three string cases.
func idsDisjoint(a, b Claim) bool {
	switch {
	case a.Prefix && b.Prefix:
		// Two prefixes share an id iff one extends the other.
		return !strings.HasPrefix(a.ID, b.ID) && !strings.HasPrefix(b.ID, a.ID)
	case a.Prefix:
		return !strings.HasPrefix(b.ID, a.ID)
	case b.Prefix:
		return !strings.HasPrefix(a.ID, b.ID)
	default:
		return a.ID != b.ID
	}
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
	point := Claim{System: ref.System(), Kind: ref.Kind(), ID: ref.ID()}
	var out []entity.Path
	for _, c := range claims {
		if c.Disjoint(point) {
			continue
		}
		out = append(out, entity.Path{Ref: ref, Field: c.Field})
	}
	return shallowest(out)
}

// shallowest drops any place another place already contains, duplicates included.
func shallowest(places []entity.Path) []entity.Path {
	var out []entity.Path
	for _, p := range places {
		held := false
		for _, q := range out {
			if holds(q.Field, p.Field) {
				held = true
				break
			}
		}
		if held {
			continue
		}
		kept := out[:0:0]
		for _, q := range out {
			if !holds(p.Field, q.Field) {
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
