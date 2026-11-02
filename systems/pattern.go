package systems

import (
	"bytes"
	"fmt"
	"regexp/syntax"
	"slices"
	"strings"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/encode"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
	"github.com/signadot/tony-format/go-tony/mergeop"
)

// A STEP IS MATCHED BY A TONY MATCH. A row's match, an environment's shape and a runner's ceiling
// write each field as a pattern in tony's own grammar, the one a charter's `value:` and `verse
// data --if` take: a literal is the value it is, `null` any value, and an operator says the rest
// (`!glob "refs/heads/verse/*"`, `!regexp "git push"`, `!or [none, allow]`, `!not true`). The
// systems name no operator of their own. `!glob` is Go's path.Match, whose `*` stops at a `/`;
// `!regexp` is Go's regexp, anchored where it says so.
//
// ONE FIELD READS OTHERWISE: one of the form path, an address, is at or under its pattern, as a
// charter's `path:` reads one. The pattern is a list of segments, or an address spelled as a
// kinded path (`play.issues.*`), and each segment is a name, `*` for any one, or an operator over
// one segment.
//
// A check that holds one pattern within another (a row within an outer autonomy, a shape within
// a ceiling) reads patterns against patterns, never against a value: FieldContains answers yes
// only where every value inner matches, outer matches too, and FieldMeets answers no only where
// no value matches both. Each leans toward refusing, so a check built on them is sound, and an
// operator they do not model is still a pattern a row may write: it is held within another only
// where the two are the same.

// MatchField is whether a step's value of a field of form meets pattern. An absent value meets
// nothing, `null` included, as tony's object match has it.
func MatchField(form string, pattern, value *ir.Node) bool {
	pattern, value = ir.Uncomment(pattern), ir.Uncomment(value)
	if value == nil {
		return false
	}
	if isAny(pattern) {
		return true
	}
	if op, branches := combinator(pattern); op != "" {
		switch op {
		case "or":
			return slices.ContainsFunc(branches, func(b *ir.Node) bool { return MatchField(form, b, value) })
		case "and":
			return !slices.ContainsFunc(branches, func(b *ir.Node) bool { return !MatchField(form, b, value) })
		case "not":
			return !MatchField(form, branches[0], value)
		}
	}
	if form == FormPath {
		return pathMatch(pattern, value)
	}
	ok, err := safeMatch(value, canon(form, pattern))
	return err == nil && ok
}

// FieldContains is whether every value inner matches, outer matches too: inner is within outer.
// It answers yes only where that holds, so a check refusing on its no is sound.
func FieldContains(form string, outer, inner *ir.Node) bool {
	outer, inner = canon(form, ir.Uncomment(outer)), canon(form, ir.Uncomment(inner))
	if isAny(outer) {
		return true
	}
	if isAny(inner) {
		return false
	}
	iop, ibranches := combinator(inner)
	oop, obranches := combinator(outer)
	switch {
	case iop == "or":
		return !slices.ContainsFunc(ibranches, func(b *ir.Node) bool { return !FieldContains(form, outer, b) })
	case oop == "and":
		return !slices.ContainsFunc(obranches, func(b *ir.Node) bool { return !FieldContains(form, b, inner) })
	case oop == "not":
		return !FieldMeets(form, obranches[0], inner)
	case iop == "not":
		return false
	}
	if oop == "or" && slices.ContainsFunc(obranches, func(b *ir.Node) bool { return FieldContains(form, b, inner) }) {
		return true
	}
	if iop == "and" && slices.ContainsFunc(ibranches, func(b *ir.Node) bool { return FieldContains(form, outer, b) }) {
		return true
	}
	if oop != "" || iop != "" {
		return outer.DeepEqual(inner)
	}
	if form == FormPath {
		return pathContains(outer, inner)
	}
	if isLiteral(inner) {
		return MatchField(form, outer, inner)
	}
	if o, ok := textPattern(form, outer); ok && o.exact {
		if i, ok := textPattern(form, inner); ok {
			return containsTokens(o.tokens, i.tokens)
		}
	}
	if outer.Tag == "" && inner.Tag == "" && outer.Type == ir.ObjectType && inner.Type == ir.ObjectType {
		for i, k := range outer.Fields {
			v := ir.Get(inner, k.String)
			if v == nil || !FieldContains("", outer.Values[i], v) {
				return false
			}
		}
		return true
	}
	return outer.DeepEqual(inner)
}

// FieldMeets is whether some value could match both patterns. It answers no only where none
// can, so a check that skips what does not meet skips nothing it should read.
func FieldMeets(form string, a, b *ir.Node) bool {
	a, b = canon(form, ir.Uncomment(a)), canon(form, ir.Uncomment(b))
	if isAny(a) || isAny(b) {
		return true
	}
	for _, p := range [][2]*ir.Node{{a, b}, {b, a}} {
		x, y := p[0], p[1]
		switch op, branches := combinator(x); op {
		case "or":
			return slices.ContainsFunc(branches, func(br *ir.Node) bool { return FieldMeets(form, br, y) })
		case "and":
			return !slices.ContainsFunc(branches, func(br *ir.Node) bool { return !FieldMeets(form, br, y) })
		case "not":
			return !FieldContains(form, branches[0], y)
		}
	}
	if form == FormPath {
		return pathMeets(a, b)
	}
	if isLiteral(a) {
		return MatchField(form, b, a)
	}
	if isLiteral(b) {
		return MatchField(form, a, b)
	}
	if x, ok := textPattern(form, a); ok {
		if y, ok := textPattern(form, b); ok {
			return meetTokens(x.tokens, y.tokens)
		}
	}
	if a.Tag == "" && b.Tag == "" && a.Type == ir.ObjectType && b.Type == ir.ObjectType {
		for i, k := range a.Fields {
			if v := ir.Get(b, k.String); v != nil && !FieldMeets("", a.Values[i], v) {
				return false
			}
		}
	}
	return true
}

// isAny is whether a pattern matches any value: `null`, or nothing written.
func isAny(n *ir.Node) bool { return n == nil || (n.Tag == "" && n.Type == ir.NullType) }

// isLiteral is whether a pattern is a value, which matches itself alone: an untagged scalar.
func isLiteral(n *ir.Node) bool {
	return n != nil && n.Tag == "" && (n.Type == ir.StringType || n.Type == ir.BoolType || n.Type == ir.NumberType)
}

// combinator is the operator a pattern is, where it is one the analysis reads through: `or` and
// `and` over a list of branches, and `not` over one. Empty for any other pattern.
func combinator(n *ir.Node) (op string, branches []*ir.Node) {
	if n == nil || n.Tag == "" {
		return "", nil
	}
	pre, tag, args, child, err := mergeop.SplitChild(n)
	if err != nil || pre != "" || len(args) != 0 || child == nil {
		return "", nil
	}
	switch tag {
	case "or", "and":
		if child.Type == ir.ArrayType {
			return tag, child.Values
		}
	case "not":
		return tag, []*ir.Node{child}
	}
	return "", nil
}

// operand is the operator a pattern applies and what it applies it to, for one that takes text
// (`glob`, `regexp`); empty for an untagged pattern or any other.
func operand(n *ir.Node) (op, text string) {
	if n == nil || n.Tag == "" {
		return "", ""
	}
	pre, tag, args, child, err := mergeop.SplitChild(n)
	if err != nil || pre != "" || len(args) != 0 || child == nil || child.Tag != "" || child.Type != ir.StringType {
		return "", ""
	}
	return tag, child.String
}

// safeMatch is tony.Match with a floor under it: a pattern that panics is no match, not a dead
// runner.
func safeMatch(value, pattern *ir.Node) (ok bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			ok, err = false, fmt.Errorf("the pattern could not be evaluated: %v", r)
		}
	}()
	return tony.Match(value, pattern)
}

// canon is a pattern as its form spells values once: a repository's literal or glob in its
// canonical spelling (Canonical), so `acme/*` is `github.com/acme/*`. A regexp is read as
// written, against the canonical spelling. Other forms are as written.
func canon(form string, n *ir.Node) *ir.Node {
	if form != FormRepo || n == nil || !canonRepo(n, false) {
		return n
	}
	out := n.Clone()
	canonRepo(out, true)
	return out
}

// canonRepo is whether a pattern names some repository other than canonically: an untagged
// string, one under `!glob` or `!not`, or a branch of `!or` and `!and`. With write, it spells each
// canonically, in place.
func canonRepo(n *ir.Node, write bool) bool {
	n = ir.Uncomment(n)
	if n == nil {
		return false
	}
	changes := false
	last := n.Tag
	if i := strings.LastIndexAny(last, ".!"); i >= 0 {
		last = last[i+1:]
	}
	switch n.Type {
	case ir.StringType:
		if c := Canonical(FormRepo, n.String); (last == "" || last == "glob" || last == "not") && c != n.String {
			changes = true
			if write {
				n.String = c
			}
		}
	case ir.ArrayType:
		if last == "or" || last == "and" {
			for _, v := range n.Values {
				changes = canonRepo(v, write) || changes
			}
		}
	}
	return changes
}

// ── text ────────────────────────────────────────────────────────────────────────────────────

// textPat is a pattern over text as tokens: a character as itself, and the two runs a pattern
// can leave open. exact is whether the tokens spell just the strings the pattern matches; where
// they spell more, a check may meet on them but may not hold anything within them.
type textPat struct {
	tokens []int
	exact  bool
}

// The runs: one within a path's segment, which a glob's `*` is, and one of anything, which a
// regexp's open end is.
const (
	tokWithin = -1
	tokAcross = -2
)

// textPattern is a pattern over a field's text as tokens: a literal, a glob or a regexp. False
// for any other.
func textPattern(form string, n *ir.Node) (textPat, bool) {
	n = canon(form, n)
	if n == nil {
		return textPat{}, false
	}
	if n.Tag == "" {
		if n.Type != ir.StringType {
			return textPat{}, false
		}
		return textPat{tokens: literalTokens(n.String), exact: true}, true
	}
	switch op, text := operand(n); op {
	case "glob":
		return globTokens(text)
	case "regexp":
		return regexpTokens(text)
	}
	return textPat{}, false
}

func literalTokens(s string) []int {
	out := make([]int, 0, len(s))
	for _, r := range s {
		out = append(out, int(r))
	}
	return out
}

// globTokens reads a glob as path.Match does: `*` a run within a segment, `\` quoting what
// follows. A `?` or a class is read as a run within a segment or anything, which holds more
// than it matches, so the pattern is not exact.
func globTokens(g string) (textPat, bool) {
	p := textPat{exact: true}
	rs := []rune(g)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '*':
			p.tokens = append(p.tokens, tokWithin)
		case '?':
			p.tokens, p.exact = append(p.tokens, tokWithin), false
		case '[':
			end := i + 1
			if end < len(rs) && rs[end] == '^' {
				end++
			}
			if end < len(rs) && rs[end] == ']' {
				end++
			}
			for end < len(rs) && rs[end] != ']' {
				if rs[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(rs) {
				return textPat{}, false // path.Match refuses it
			}
			p.tokens, p.exact = append(p.tokens, tokAcross), false
			i = end
		case '\\':
			if i+1 >= len(rs) {
				return textPat{}, false
			}
			i++
			p.tokens = append(p.tokens, int(rs[i]))
		default:
			p.tokens = append(p.tokens, int(rs[i]))
		}
	}
	return p, true
}

// regexpTokens reads a regexp as tokens: its literal text where it has some, a run of anything
// where it is not anchored, and a run of anything for whatever else it says, which makes it not
// exact. `^refs/heads/verse/` is exactly every string beginning so, and `git push` every string
// holding it.
func regexpTokens(s string) (textPat, bool) {
	re, err := syntax.Parse(s, syntax.Perl)
	if err != nil {
		return textPat{}, false
	}
	subs := []*syntax.Regexp{re}
	if re.Op == syntax.OpConcat {
		subs = re.Sub
	}
	p := textPat{exact: true}
	i := 0
	if len(subs) > 0 && subs[0].Op == syntax.OpBeginText {
		i = 1
	} else {
		p.tokens = append(p.tokens, tokAcross)
	}
	for ; i < len(subs) && subs[i].Op == syntax.OpLiteral && subs[i].Flags&syntax.FoldCase == 0; i++ {
		for _, r := range subs[i].Rune {
			p.tokens = append(p.tokens, int(r))
		}
	}
	switch rest := subs[i:]; {
	case len(rest) == 0:
		p.tokens = append(p.tokens, tokAcross)
	case len(rest) == 1 && rest[0].Op == syntax.OpEndText:
	default:
		p.tokens, p.exact = append(p.tokens, tokAcross), false
	}
	return p, true
}

// containsTokens is whether every string inner's tokens spell, outer's spell too.
func containsTokens(o, i []int) bool {
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
			ok = within(a+1, b) || (b < len(i) && within(a, b+1))
		case o[a] == tokWithin:
			ok = within(a+1, b) || (b < len(i) && (i[b] == tokWithin || (i[b] > 0 && i[b] != '/')) && within(a, b+1))
		default:
			ok = b < len(i) && i[b] == o[a] && within(a+1, b+1)
		}
		memo[key] = ok
		return ok
	}
	return within(0, 0)
}

// meetTokens is whether some string both token lists spell.
func meetTokens(x, y []int) bool {
	wild := func(t int) bool { return t == tokWithin || t == tokAcross }
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
			ok = true
		case i < len(x) && j < len(y):
			switch {
			case !wild(x[i]) && !wild(y[j]):
				ok = x[i] == y[j] && meet(i+1, j+1)
			case !wild(x[i]):
				ok = takes(y[j], x[i]) && meet(i+1, j)
			case !wild(y[j]):
				ok = takes(x[i], y[j]) && meet(i, j+1)
			default:
				ok = meet(i+1, j) || meet(i, j+1)
			}
		}
		memo[key] = ok
		return ok
	}
	return meet(0, 0)
}

// writtenTokens is an attribute's value as a charter writes it, each binding still to be bound
// (`$…`) a run of anything: the values it could be once a firing binds it.
func writtenTokens(v string) []int {
	var out []int
	last := 0
	for _, loc := range bound.FindAllStringIndex(v, -1) {
		out = append(out, literalTokens(v[last:loc[0]])...)
		out = append(out, tokAcross)
		last = loc[1]
	}
	return append(out, literalTokens(v[last:])...)
}

// meetsWritten is whether a pattern over a field of form could match some value a written
// attribute binds to.
func meetsWritten(form string, pattern *ir.Node, v string) bool {
	pattern = ir.Uncomment(pattern)
	if isAny(pattern) {
		return true
	}
	if op, branches := combinator(pattern); op == "or" {
		return slices.ContainsFunc(branches, func(b *ir.Node) bool { return meetsWritten(form, b, v) })
	}
	p, ok := textPattern(form, pattern)
	if !ok {
		return true
	}
	return meetTokens(p.tokens, writtenTokens(Canonical(form, v)))
}

// ── addresses ───────────────────────────────────────────────────────────────────────────────

// pathPattern is a path pattern's segments: a list, or an address spelled as a kinded path, `*`
// for any one segment.
func pathPattern(n *ir.Node) ([]*ir.Node, error) {
	n = ir.Uncomment(n)
	switch {
	case n == nil:
		return nil, fmt.Errorf("an address is a list of segments or a path (play.issues.*)")
	case n.Tag == "" && n.Type == ir.ArrayType:
		out := make([]*ir.Node, len(n.Values))
		for i, s := range n.Values {
			s = ir.Uncomment(s)
			if s == nil || (s.Tag == "" && s.Type != ir.StringType) {
				return nil, fmt.Errorf("segment %d: a name, `*`, or a match over one name", i)
			}
			if s.Tag == "" && s.String == ".." {
				return nil, fmt.Errorf("segment %d: `..`: an address is at or under its pattern already", i)
			}
			out[i] = s
		}
		return out, nil
	case n.Tag == "" && n.Type == ir.StringType:
		if n.String == "" {
			return nil, nil
		}
		kp, err := kpath.Parse(n.String)
		if err != nil {
			return nil, fmt.Errorf("%q: %v", n.String, err)
		}
		var out []*ir.Node
		for p := kp; p != nil; p = p.Next {
			switch {
			case p.Field != nil:
				out = append(out, ir.FromString(*p.Field))
			case p.FieldAll:
				out = append(out, ir.FromString("*"))
			case p.Descend:
				return nil, fmt.Errorf("%q: `..`: an address is at or under its pattern already", n.String)
			default:
				return nil, fmt.Errorf("%q: %q is not a name or `*`", n.String, p.String())
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("an address is a list of segments or a path (play.issues.*)")
}

// Address is a kinded path as a step carries it in a field of the form path: its segments in
// order, a name as itself and any other segment (`(b)`, `[0]`) as its own text, so every address
// verse reads is one a row's pattern sees, and lies under the names above it. The root, "", is
// no segments. A path that does not read is an error, for the runner to refuse the step on.
func Address(s string) (*ir.Node, error) {
	if s == "" {
		return ir.FromSlice([]*ir.Node{}), nil
	}
	kp, err := kpath.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("the address %q does not read: %v", s, err)
	}
	var segs []*ir.Node
	for p := kp; p != nil; p = p.Next {
		switch {
		case p.Field != nil:
			segs = append(segs, ir.FromString(*p.Field))
		case p.FieldAll:
			segs = append(segs, ir.FromString("*"))
		default:
			one := *p
			one.Next = nil
			segs = append(segs, ir.FromString(one.String()))
		}
	}
	return ir.FromSlice(segs), nil
}

// pathValue is an address as a step carries it: its names, in order.
func pathValue(n *ir.Node) ([]string, bool) {
	n = ir.Uncomment(n)
	if n == nil || n.Type != ir.ArrayType {
		return nil, false
	}
	out := make([]string, len(n.Values))
	for i, s := range n.Values {
		s = ir.Uncomment(s)
		if s == nil || s.Type != ir.StringType {
			return nil, false
		}
		out[i] = s.String
	}
	return out, true
}

// segMatch is whether one name meets one segment of a pattern: `*` any, an untagged name
// itself, and an operator as tony matches it.
func segMatch(want *ir.Node, seg string) bool {
	if want.Tag == "" {
		return want.String == "*" || want.String == seg
	}
	ok, err := safeMatch(ir.FromString(seg), want)
	return err == nil && ok
}

// segLiteral is a segment's name, where it names one.
func segLiteral(s *ir.Node) (string, bool) {
	if s.Tag != "" || s.String == "*" {
		return "", false
	}
	return s.String, true
}

func pathMatch(pattern, value *ir.Node) bool {
	segs, err := pathPattern(pattern)
	names, ok := pathValue(value)
	if err != nil || !ok || len(names) < len(segs) {
		return false
	}
	for i, s := range segs {
		if !segMatch(s, names[i]) {
			return false
		}
	}
	return true
}

// pathContains is whether every address inner reaches, outer reaches: outer is no longer, and
// at each of its segments admits every name inner's does.
func pathContains(outer, inner *ir.Node) bool {
	os, err := pathPattern(outer)
	if err != nil {
		return false
	}
	is, err := pathPattern(inner)
	if err != nil || len(is) < len(os) {
		return false
	}
	for k, o := range os {
		if o.Tag == "" && o.String == "*" {
			continue
		}
		if lit, ok := segLiteral(is[k]); ok {
			if !segMatch(o, lit) {
				return false
			}
		} else if !o.DeepEqual(is[k]) {
			return false
		}
	}
	return true
}

// pathMeets is whether some address both reach: as far as the shorter goes, no segment of one
// is disjoint from the other's.
func pathMeets(a, b *ir.Node) bool {
	as, err := pathPattern(a)
	if err != nil {
		return true
	}
	bs, err := pathPattern(b)
	if err != nil {
		return true
	}
	for k := 0; k < len(as) && k < len(bs); k++ {
		al, aLit := segLiteral(as[k])
		bl, bLit := segLiteral(bs[k])
		switch {
		case aLit && bLit && al != bl:
			return false
		case aLit && !bLit && !segMatch(bs[k], al):
			return false
		case bLit && !aLit && !segMatch(as[k], bl):
			return false
		}
	}
	return true
}

// ── words ───────────────────────────────────────────────────────────────────────────────────

// PathText is an address a step carries in its one spelling: its names joined by `.`, and a
// segment that is no name (`(b)`, `[0]`) after the one before it, as a kinded path writes it.
func PathText(names []string) string {
	var b strings.Builder
	for i, n := range names {
		if i > 0 && !strings.HasPrefix(n, "(") && !strings.HasPrefix(n, "[") && !strings.HasPrefix(n, "{") {
			b.WriteByte('.')
		}
		b.WriteString(n)
	}
	return b.String()
}

// Plain is a pattern as its author wrote it, less the tags a parser attaches to record how it
// was spelled (`!bracket`, `!literal`), at every depth: an untagged pattern is then a literal,
// and an operator the first tag it carries. n is not changed.
func Plain(n *ir.Node) *ir.Node {
	if n == nil {
		return nil
	}
	out := n.Clone()
	out.Parent, out.ParentIndex, out.ParentField = nil, 0, ""
	stripLayout(out)
	return out
}

func stripLayout(n *ir.Node) {
	if n == nil {
		return
	}
	n.Tag = ir.TagRemove(ir.TagRemove(n.Tag, "!bracket"), "!literal")
	for _, f := range n.Fields {
		stripLayout(f)
	}
	for _, v := range n.Values {
		stripLayout(v)
	}
}

// unknownOp is the first tag in a pattern that names no operator of tony's, which tony would
// pass over and match the value under it by equality: `!gloob "x"` is never a glob.
func unknownOp(n *ir.Node) string {
	if n == nil {
		return ""
	}
	for tag := n.Tag; tag != ""; {
		head, _, rest := ir.TagArgs(tag)
		if name := strings.TrimPrefix(head, "!"); head != "!bracket" && head != "!literal" && mergeop.Lookup(name) == nil {
			return head
		}
		tag = rest
	}
	for _, v := range n.Values {
		if t := unknownOp(v); t != "" {
			return t
		}
	}
	return ""
}

// Binds is whether a pattern holds a binding a firing would fill (`$…`) where it is read as
// text: a literal or a glob, never a regexp, whose `$` is the end of the text.
func Binds(n *ir.Node) bool {
	n = ir.Uncomment(n)
	if n == nil {
		return false
	}
	if n.Type == ir.StringType {
		op, _ := operand(n)
		return op != "regexp" && strings.Contains(n.String, "$")
	}
	return slices.ContainsFunc(n.Values, Binds)
}

// Words is a pattern in one line, as a refusal quotes it: a literal as its text, anything else
// in tony (`!glob refs/heads/*`).
func Words(n *ir.Node) string {
	n = ir.Uncomment(n)
	switch {
	case n == nil:
		return "null"
	case n.Tag == "" && n.Type == ir.StringType:
		return n.String
	case n.Type == ir.ArrayType || n.Type == ir.ObjectType:
		parts := make([]string, len(n.Values))
		for i, v := range n.Values {
			parts[i] = Words(v)
			if n.Type == ir.ObjectType {
				parts[i] = Words(n.Fields[i]) + ": " + parts[i]
			}
		}
		open, end := "[", "]"
		if n.Type == ir.ObjectType {
			open, end = "{", "}"
		}
		if n.Tag != "" {
			open = n.Tag + " " + open
		}
		return open + strings.Join(parts, ", ") + end
	}
	var b bytes.Buffer
	if err := encode.Encode(n, &b, encode.EncodeWire(true)); err != nil {
		return n.String
	}
	return strings.TrimSpace(b.String())
}

// FieldText is a step's field as text, for a comparison by spelling (a block's unique
// attribute) and a refusal that quotes it: empty where the step does not carry it.
func FieldText(fields *ir.Node, name string) string {
	v := stepValue(fields, name)
	if v == nil {
		return ""
	}
	return ValueText("", v)
}

// stepValue is a step's value of the field name, nil where it carries none: a step built with no
// fields carries none of them.
func stepValue(fields *ir.Node, name string) *ir.Node {
	fields = ir.Uncomment(fields)
	if fields == nil || fields.Type != ir.ObjectType {
		return nil
	}
	return ir.Get(fields, name)
}

// ValueText is a step's value of a field of form as text: a string as itself, an address (the
// form path) as its names joined by `.`, anything else in tony.
func ValueText(form string, v *ir.Node) string {
	v = ir.Uncomment(v)
	if v == nil {
		return ""
	}
	if names, ok := pathValue(v); ok && form == FormPath {
		return PathText(names)
	}
	return Words(v)
}

// OneOf is the pattern that matches each of values and nothing else: the value for one, `!or`
// of them for several.
func OneOf(values ...string) *ir.Node {
	if len(values) == 1 {
		return ir.FromString(values[0])
	}
	items := make([]*ir.Node, len(values))
	for i, v := range values {
		items[i] = ir.FromString(v)
	}
	return ir.FromSlice(items).WithTag("!or")
}

// Literals is the values a pattern of literals matches: the one it is, or each branch of an
// `!or` of them. False for any other pattern.
func Literals(n *ir.Node) ([]string, bool) {
	n = ir.Uncomment(n)
	if n != nil && n.Tag == "" && n.Type == ir.StringType {
		return []string{n.String}, true
	}
	op, branches := combinator(n)
	if op != "or" {
		return nil, false
	}
	var out []string
	for _, b := range branches {
		vs, ok := Literals(b)
		if !ok {
			return nil, false
		}
		out = append(out, vs...)
	}
	return out, true
}

// Glob is the pattern `!glob g`, for a pattern built in code.
func Glob(g string) *ir.Node { return ir.FromString(g).WithTag("!glob") }

// Regexp is the pattern `!regexp re`, for a pattern built in code.
func Regexp(re string) *ir.Node { return ir.FromString(re).WithTag("!regexp") }
