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
// A DESCENT MAKES THIS A WALK RATHER THAN A LOOP, because position stops lining up: `..` reaches
// zero or more segments, so the two patterns no longer agree on what "position i" is. Both are
// asked of the SAME path, so the walk emits that path one segment at a time and each pattern
// either spends a segment of its own on it or, standing on a descent, absorbs it and stays. That
// is `patternReaches` (pattern.go) with a pattern on both sides instead of an address on one.
//
// `descends` says which segments are the descent. A pattern kind that cannot hold one passes
// NoDescent, and the walk is then the loop this used to be.
func Overlap[S any](a, b []S, disjoint func(S, S) bool, descends func(S) bool) bool {
	// Every step raises i+j, so the walk terminates; the memo only keeps a pair of descents from
	// costing exponentially.
	seen := make(map[[2]int]bool)
	var meet func(i, j int) bool
	meet = func(i, j int) bool {
		if i == len(a) || j == len(b) {
			return true
		}
		if v, ok := seen[[2]int{i, j}]; ok {
			return v
		}
		var r bool
		switch {
		case descends(a[i]):
			// The descent ends here, or it absorbs the segment b spends next. Two descents
			// facing each other need no third case: both absorbing the same segment leaves the
			// walk where it stood, and whatever that could reach, ending one of them reaches.
			r = meet(i+1, j) || meet(i, j+1)
		case descends(b[j]):
			r = meet(i, j+1) || meet(i+1, j)
		default:
			r = !disjoint(a[i], b[j]) && meet(i+1, j+1)
		}
		seen[[2]int{i, j}] = r
		return r
	}
	return meet(0, 0)
}

// NoDescent is the `descends` of a pattern whose segments cannot be one — a source's claim, whose
// segment is a name and a prefix flag and nothing else.
func NoDescent[S any](S) bool { return false }

// Literal is a segment that admits exactly its own name.
func Literal(want, seg string) bool { return want == seg }

// Wild is a segment that admits its own name, or any name when it is `*` — the pattern of names a
// path written as strings spells.
func Wild(want, seg string) bool { return want == "*" || want == seg }
