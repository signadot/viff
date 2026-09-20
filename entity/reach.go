package entity

// REACH AND OVERLAP are the two questions asked of a pattern, and each is asked here and nowhere
// else.
//
// A pattern is a sequence of segments, and a segment ADMITS a name: a literal admits itself, `*`
// admits any, a charter's operator admits what it matches, a claim's prefix what begins with it.
// Around that one per-segment question the walk is the same for every kind of pattern — it is the
// walk kpath's MatchesPrefix and Matches make over their own segments — so the walk is written
// once and the segment's question is passed in. kpath has no segment for an operator or a prefix,
// which is why the walk is verse's rather than a call into kpath.

// Under reports whether path is at or under what pattern reaches: the pattern is no longer than
// the path, and at every position the pattern's segment admits the path's. An empty pattern is
// the root, and everything is under it.
func Under[P, T any](pattern []P, path []T, admits func(P, T) bool) bool {
	if len(path) < len(pattern) {
		return false
	}
	for i, s := range pattern {
		if !admits(s, path[i]) {
			return false
		}
	}
	return true
}

// Above reports whether path is a strict ancestor of something pattern reaches: the path is
// shorter than the pattern, and the pattern's segments admit every segment the path has. So a
// write at the path is a write over what the pattern reaches.
func Above[P, T any](pattern []P, path []T, admits func(P, T) bool) bool {
	if len(path) >= len(pattern) {
		return false
	}
	for i, seg := range path {
		if !admits(pattern[i], seg) {
			return false
		}
	}
	return true
}

// Overlap reports whether some path can be reached by both patterns: as far as the shorter goes,
// no position holds two segments that admit no common name. Past the shorter there is nothing to
// disagree about — it is then an ancestor of what the longer reaches, and reaches it too.
func Overlap[S any](a, b []S, disjoint func(S, S) bool) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if disjoint(a[i], b[i]) {
			return false
		}
	}
	return true
}

// Literal is a segment that admits exactly its own name.
func Literal(want, seg string) bool { return want == seg }

// Wild is a segment that admits its own name, or any name when it is `*` — the pattern of names a
// path written as strings spells.
func Wild(want, seg string) bool { return want == "*" || want == seg }
