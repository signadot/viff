package source

import (
	"fmt"
	"strings"
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
// The cost is stated in docs/working/sources.md and worth knowing: this works when a source's
// ids encode ownership positionally, which git's do because git.go prefixes every id with the
// repo. A source whose ownership is a field rather than an address cannot spell it here.

// Claim is one parsed ownership claim.
type Claim struct {
	// System and Kind are a literal or "" for `*` (which matches any).
	System, Kind string
	// ID is the literal id, or the literal PREFIX when Prefix is set. Empty with Prefix set is
	// `*`: every id.
	ID string
	// Prefix reports that ID is a prefix rather than the whole thing — the trailing `*`.
	Prefix bool
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
	return seg(c.System) + ":" + seg(c.Kind) + ":" + seg(id)
}

// Disjoint reports whether no entity can satisfy both claims.
//
// It PROVES, and answers false whenever it cannot. Two claims are disjoint when ANY segment is,
// since a ref must satisfy all three: two literal systems that differ, two literal kinds that
// differ, or two ids that cannot be the same string. A `*` segment is disjoint from nothing,
// which is why a claim of `git:*:*` is disjoint from nothing else in git — it claims the lot.
func (c Claim) Disjoint(o Claim) bool {
	if c.System != "" && o.System != "" && c.System != o.System {
		return true
	}
	if c.Kind != "" && o.Kind != "" && c.Kind != o.Kind {
		return true
	}
	return idsDisjoint(c, o)
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
// docs/working/sources-plan.md on why a source that declines to say gets no ownership guessed
// for it.
func Disjoint(a, b []string) (bool, error) {
	as, err := parseClaims(a)
	if err != nil {
		return false, err
	}
	bs, err := parseClaims(b)
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

func parseClaims(ss []string) ([]Claim, error) {
	out := make([]Claim, 0, len(ss))
	for _, s := range ss {
		c, err := ParseClaim(s)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
