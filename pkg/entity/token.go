package entity

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// An address becomes an ID TOKEN wherever viff names something after what it is about: a task
// id (`review.onDone@demo.pr.1`), a derived entity's id, a resolver's cover
// (`security-review@demo.pr.1`). All of those cross a URL path segment and get pasted back onto
// a command line, so the token has to be legible, injective, and free of characters those two
// spend.
//
// IT IS BUILT FROM THE SEGMENTS, NEVER FROM THE RENDERING, and that is the whole point of this
// file. `Ref.String` quotes a segment that would otherwise read as something else — a numeric
// id is the everyday case, `demo.pr."1"` — and a quote is exactly what an id may not carry. So
// sanitising a rendering would rewrite the commonest address and push it down the lossy branch,
// and worse, would make every id a function of the SPELLING, which has moved once already.
// Segments have neither problem: they are the address itself, and quoting is something that
// happens to them on the way to a page.

// segJoin separates the segments inside a token. It is '.' because that is what separates them
// everywhere else; SegToken says what the choice costs and how it is paid for.
const segJoin = "."

// HashSep marks the disambiguating hash a lossy flattening forces. Its own character, so
// nothing reading a token has to guess whether a '-' is structure or part of a name.
//
// Exported because a trigger NAME may not contain one: a task id built on it would spend the
// character as punctuation (trigger.validTriggerName).
const HashSep = "+"

// Token is this address as one id token.
func (r Ref) Token() string { return SegToken(r.Segs()) }

// SegToken flattens a segment vector into one id token.
//
// THE HASH IS NOT DECORATION. Two distinct addresses must not flatten to one token, and the
// flattening is lossy in two ways: SafeSeg rewrites a character an id may not hold (a slash, a
// '#'), and the join itself cannot tell `["a.b","c"]` from `["a","b","c"]`. Either makes the
// token ambiguous, so either takes the hash — over the segments separated by a NUL, so the hash
// keeps apart exactly what the join merged.
func SegToken(segs []string) string {
	safe := make([]string, len(segs))
	lossy := false
	for i, s := range segs {
		safe[i] = SafeSeg(s)
		if safe[i] != s || strings.Contains(s, segJoin) {
			lossy = true
		}
	}
	out := strings.Join(safe, segJoin)
	if !lossy {
		return out // nothing was rewritten and nothing was merged: no collision possible
	}
	return out + HashSep + SegHash(segs)
}

// SegHash is a short hash of a segment vector, injective the way the vector is: the segments are
// separated by a NUL, which no segment can hold, so `[ab,c]` and `[a,bc]` hash differently.
func SegHash(segs []string) string {
	h := fnv.New32a()
	for _, s := range segs {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%08x", h.Sum32())
}

// SafeSeg makes one segment usable inside a URL path segment and a shell word: a token routes
// through `/tasks/{id}`, which matches one segment, so characters that break a path (slashes,
// fragment, query, whitespace) become '_'.
//
// It also rewrites '~', which breaks no path but DELIMITS a gate id from its owner (task.GateSep)
// — an owner id carrying one would make `resolve` split it in the wrong place and route the
// answer to a task that does not exist. The character is written here rather than imported so
// that the substrate does not depend on the task package; trigger, which imports both, holds the
// test that they still agree.
//
// It is lossy, which is what SegToken's hash is for.
func SafeSeg(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', '#', '?', '%', ' ', '\t', '\n', '~':
			return '_'
		}
		return r
	}, s)
}
