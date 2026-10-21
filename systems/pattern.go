package systems

import (
	"regexp"
	"strings"
)

// The patterns a row's match, an environment's shape and a runner's ceiling write a field's value
// with: `*` matches within a `/` segment, `**` across segments, and anything else is the text it
// is. A field of the form text (a command, Crossing) has no segments, so there a `*` matches
// across everything.

// Crossing is whether a step's field crosses `/` with a `*`: one of the form text, where a `/` is
// just a character, so `*git push*` matches `cd x && git push origin main`.
func Crossing(step, field string) bool {
	system, verb, ok := Split(step)
	if !ok {
		return false
	}
	d, ok := Get(system)
	if !ok {
		return false
	}
	fields, _ := d.StepFields(verb)
	for _, f := range fields {
		if f.Name == field {
			return f.Form == FormText
		}
	}
	return false
}

// Match is whether value is one of the values pattern spells. crossing is Crossing's.
func Match(pattern, value string, crossing bool) bool {
	var re strings.Builder
	re.WriteString("(?s)^") // `*` crosses a newline: a command can be several lines
	for i := 0; i < len(pattern); i++ {
		switch {
		case pattern[i] == '*' && (crossing || strings.HasPrefix(pattern[i:], "**")):
			re.WriteString(".*")
			if !crossing {
				i++
			}
		case pattern[i] == '*':
			re.WriteString("[^/]*")
		default:
			re.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	re.WriteString("$")
	ok, _ := regexp.MatchString(re.String(), value)
	return ok
}

// PatternContains is whether every value inner matches, outer matches too: inner is within
// outer. It is sound, never saying so of an inner some value of which outer would refuse, which
// is what a check that refuses on its answer needs: `refs/heads/verse/**` contains
// `refs/heads/verse/auto/*`, and `refs/heads/*` does not contain `refs/heads/**`.
func PatternContains(outer, inner string, crossing bool) bool {
	o, i := tokens(outer, crossing), tokens(inner, crossing)
	memo := map[[2]int]bool{}
	var within func(a, b int) bool
	within = func(a, b int) bool {
		key := [2]int{a, b}
		if v, ok := memo[key]; ok {
			return v
		}
		var ok bool
		switch {
		case a == len(o):
			ok = b == len(i)
		case o[a] == tokAcross:
			// Matches any run of inner's: skip it, or let it take one more.
			ok = within(a+1, b) || (b < len(i) && within(a, b+1))
		case o[a] == tokWithin:
			// Matches a run within a segment: inner's `*`, or any character but `/`.
			ok = within(a+1, b) || (b < len(i) && (i[b] == tokWithin || (i[b] > 0 && i[b] != '/')) && within(a, b+1))
		default:
			ok = b < len(i) && i[b] == o[a] && within(a+1, b+1)
		}
		memo[key] = ok
		return ok
	}
	return within(0, 0)
}

// Intersect is whether some value matches both patterns: a row's and a shape's can both hold of a
// step. Exact over the tokens: `refs/*/x` and `refs/heads/*` meet at `refs/heads/x`, though
// neither contains the other.
func Intersect(a, b string, crossing bool) bool {
	x, y := tokens(a, crossing), tokens(b, crossing)
	wild := func(t int) bool { return t == tokWithin || t == tokAcross }
	// takes is whether wildcard w can produce character c.
	takes := func(w, c int) bool { return w == tokAcross || c != '/' }
	memo := map[[2]int]bool{}
	var meet func(i, j int) bool
	meet = func(i, j int) bool {
		key := [2]int{i, j}
		if v, ok := memo[key]; ok {
			return v
		}
		memo[key] = false // a cycle produces nothing new
		var ok bool
		switch {
		case i == len(x) && j == len(y):
			ok = true
		case i < len(x) && wild(x[i]) && meet(i+1, j), j < len(y) && wild(y[j]) && meet(i, j+1):
			ok = true // a wildcard producing nothing
		case i < len(x) && j < len(y):
			// One character produced by both: a literal advances, a wildcard may stay.
			switch {
			case !wild(x[i]) && !wild(y[j]):
				ok = x[i] == y[j] && meet(i+1, j+1)
			case !wild(x[i]):
				ok = takes(y[j], x[i]) && meet(i+1, j)
			case !wild(y[j]):
				ok = takes(x[i], y[j]) && meet(i, j+1)
			}
		}
		memo[key] = ok
		return ok
	}
	return meet(0, 0)
}

// A pattern's tokens: a character as itself, and the two wildcards as negative numbers.
const (
	tokWithin = -1 // `*`
	tokAcross = -2 // `**`, and a `*` where it crosses
)

func tokens(pattern string, crossing bool) []int {
	var out []int
	for i := 0; i < len(pattern); i++ {
		switch {
		case pattern[i] == '*' && crossing:
			out = append(out, tokAcross)
		case strings.HasPrefix(pattern[i:], "**"):
			out = append(out, tokAcross)
			i++
		case pattern[i] == '*':
			out = append(out, tokWithin)
		default:
			out = append(out, int(pattern[i]))
		}
	}
	return out
}
