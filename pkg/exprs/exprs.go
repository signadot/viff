// Package exprs answers, without the values, what go-tony evaluates in a charter's strings: which
// expressions a string holds, and which names each one reads.
//
// A charter's values are expanded by go-tony (eval.ExpandStringWithOptions for text,
// eval.ExpandIRWithOptions for a value that is one `.[…]`), and a firing binds the names they
// read. Several things are decided before there are values to evaluate against: whether a value
// is bound at all (Templated), which part of the subject a firing has to read at a revision
// (Reads), and, at install, what a derive or a ceiling may assume about a value it cannot know
// yet. Each of those asks this package, so that what is decided and what is evaluated are the
// same expressions.
package exprs

import (
	"strconv"
	"strings"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	tonyeval "github.com/signadot/tony-format/go-tony/eval"
)

// Span is one expression in a string: its text as the evaluator gets it, escapes undone and
// trimmed, and where it stands, Start at its `$[` or `.[` and End just past its `]`.
type Span struct {
	Expr       string
	Start, End int
}

// Expressions is each expression go-tony's expansion evaluates in s, in order: s whole, where it
// is one `.[…]` and nothing else (Whole), and otherwise each `$[…]`. `$$[` is a literal `$[`, and
// a `$[` that nothing closes is text to the end of s.
//
// The scan is go-tony's own (eval's scanExpression, which is unexported: tony-format issue
// y5n12a3gh12kraxfx5n0 asks for it), copied rather than approximated, so a nested bracket, a `]`
// in a quoted string and an escaped `\]` end an expression here where they end it there.
func Expressions(s string) []Span {
	if raw := tonyeval.GetRaw(s); raw != "" {
		return []Span{{Expr: strings.TrimSpace(raw), End: len(strings.TrimRight(s, "\n"))}}
	}
	var out []Span
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "$$[") {
			i += 3
			continue
		}
		if !strings.HasPrefix(s[i:], "$[") {
			i++
			continue
		}
		key, end, ok := scan(s, i+2, true)
		if !ok {
			break
		}
		out = append(out, Span{Expr: strings.TrimSpace(key), Start: i, End: end})
		i = end
	}
	return out
}

// scan is go-tony's scanExpression: the expression that starts at v[from], up to the `]` that
// closes it, with the escapes undone, and where the text after the `]` begins; false when
// nothing closes it. With quotes, a `]` in a quoted string closes nothing, and a quote nothing
// closes is read again without quotes, ending at the first `]`.
func scan(v string, from int, quotes bool) (string, int, bool) {
	var key []byte
	depth := 0
	var quote byte
	for i := from; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\\':
			if i+1 < len(v) {
				key = append(key, v[i+1])
				i++
			}
		case quote != 0:
			if c == quote {
				quote = 0
			}
			key = append(key, c)
		case quotes && (c == '"' || c == '\'' || c == '`'):
			quote = c
			key = append(key, c)
		case c == '[':
			depth++
			key = append(key, c)
		case c == ']' && depth > 0:
			depth--
			key = append(key, c)
		case c == ']':
			return string(key), i + 1, true
		default:
			key = append(key, c)
		}
	}
	if quote != 0 {
		return scan(v, from, false)
	}
	return "", 0, false
}

// Whole is whether v is one `.[…]` and nothing else, which a firing replaces whole with the
// expression's value: the test the expansion makes (eval.GetRaw).
func Whole(v string) bool { return tonyeval.GetRaw(v) != "" }

// Templated is whether v holds an expression a firing replaces: a `$[…]`, or a v that is one
// `.[…]` and nothing else (Whole). A `$` that opens none is text, as it is where the value is
// used: `$HOME`, `$5`; and so is a `.[` in a value it is not the whole of.
//
// It is the cheap test, and errs toward bound: `$$[` and a `$[` nothing closes are text to the
// expansion and Templated here. Expressions is the exact one.
func Templated(v string) bool { return strings.Contains(v, "$[") || Whole(v) }

// Path is a name an expression reads from its environment, then each field or index under it
// that the expression names as a constant: `subject.value.round` and `subject?.value["round"]`
// are [subject value round], `items[0].id` is [items 0 id], and `items[i].id` is [items], since
// which element is not known until the expression runs (and `i` is a read of its own).
type Path []string

// Env is expr-lang's name for the environment itself. `$env["pr-number"]` is a read of
// `pr-number`, and `$env` alone, or under a step that is not a constant, reads every name.
const Env = "$env"

// Reads is every name expr reads from its environment, as a Path each, in the order they are
// written; a name read twice is there twice.
//
// A name the expression binds itself is not a read: one a `let` declares, within the expression
// the `let` is over, and `#` and the `.field` shorthand inside a predicate (`filter`, `map`,
// `all`, …), which are the element being walked. So `data.findings | filter(.status == "open")`
// reads `data.findings`, and nothing named `status`. A function's name is not a read either.
//
// An expression that does not parse answers the parser's error, and no reads.
func Reads(expr string) ([]Path, error) {
	tree, err := parser.Parse(expr)
	if err != nil {
		return nil, err
	}
	var out []Path
	walk(tree.Node, nil, &out)
	return out, nil
}

// ReadsIn is Reads over every expression of s (Expressions), the first error stopping it.
func ReadsIn(s string) ([]Path, error) {
	var out []Path
	for _, sp := range Expressions(s) {
		p, err := Reads(sp.Expr)
		if err != nil {
			return nil, err
		}
		out = append(out, p...)
	}
	return out, nil
}

// walk adds what n reads to out. bound is the names a `let` around n declares.
func walk(n ast.Node, bound map[string]bool, out *[]Path) {
	switch x := n.(type) {
	case nil:
	case *ast.IdentifierNode:
		if !bound[x.Value] {
			*out = append(*out, rooted(Path{x.Value}))
		}
	case *ast.MemberNode, *ast.ChainNode:
		member(n, bound, out)
	case *ast.SliceNode:
		walk(x.Node, bound, out)
		walk(x.From, bound, out)
		walk(x.To, bound, out)
	case *ast.UnaryNode:
		walk(x.Node, bound, out)
	case *ast.BinaryNode:
		walk(x.Left, bound, out)
		walk(x.Right, bound, out)
	case *ast.CallNode:
		// A function is called by name and is not in the environment; a method is a step of
		// what it is called on, which member reads.
		if _, named := x.Callee.(*ast.IdentifierNode); !named {
			walk(x.Callee, bound, out)
		}
		for _, a := range x.Arguments {
			walk(a, bound, out)
		}
	case *ast.BuiltinNode:
		for _, a := range x.Arguments {
			walk(a, bound, out)
		}
	case *ast.PredicateNode:
		walk(x.Node, bound, out)
	case *ast.ConditionalNode:
		walk(x.Cond, bound, out)
		walk(x.Exp1, bound, out)
		walk(x.Exp2, bound, out)
	case *ast.VariableDeclaratorNode:
		walk(x.Value, bound, out)
		inner := map[string]bool{x.Name: true}
		for k := range bound {
			inner[k] = true
		}
		walk(x.Expr, inner, out)
	case *ast.SequenceNode:
		for _, s := range x.Nodes {
			walk(s, bound, out)
		}
	case *ast.ArrayNode:
		for _, s := range x.Nodes {
			walk(s, bound, out)
		}
	case *ast.MapNode:
		for _, s := range x.Pairs {
			walk(s, bound, out)
		}
	case *ast.PairNode:
		walk(x.Key, bound, out)
		walk(x.Value, bound, out)
	}
	// Constants and the predicate's pointer (`#`, `#index`) read nothing.
}

// member reads a chain of steps, `a.b[0]?.c`: the name it starts from and each constant step,
// up to the first step that is not one, and whatever that step's own expression reads.
func member(n ast.Node, bound map[string]bool, out *[]Path) {
	var steps []*ast.MemberNode // outermost first
	base := n
	for {
		switch c := base.(type) {
		case *ast.ChainNode:
			base = c.Node
			continue
		case *ast.MemberNode:
			steps = append(steps, c)
			base = c.Node
			continue
		}
		break
	}
	var path Path
	if id, ok := base.(*ast.IdentifierNode); ok && !bound[id.Value] {
		path = Path{id.Value}
	} else {
		walk(base, bound, out)
	}
	open := path != nil
	for i := len(steps) - 1; i >= 0; i-- {
		st := steps[i]
		if open && !st.Method {
			if s, ok := constant(st.Property); ok {
				path = append(path, s)
				continue
			}
		}
		if open {
			*out = append(*out, rooted(path))
			open = false
		}
		walk(st.Property, bound, out)
	}
	if open {
		*out = append(*out, rooted(path))
	}
}

// constant is a step's text where the step is a constant: a field's name, or an index.
func constant(n ast.Node) (string, bool) {
	switch p := n.(type) {
	case *ast.StringNode:
		return p.Value, true
	case *ast.IntegerNode:
		return strconv.Itoa(p.Value), true
	}
	return "", false
}

// rooted is p as a read of a name: `$env["x"]…` reads `x`.
func rooted(p Path) Path {
	if len(p) > 1 && p[0] == Env {
		return p[1:]
	}
	return p
}
