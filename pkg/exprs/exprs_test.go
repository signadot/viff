package exprs

import (
	"reflect"
	"strings"
	"testing"

	tonyeval "github.com/signadot/tony-format/go-tony/eval"
)

// Expressions finds what go-tony's expansion evaluates, and nothing else. Each case is checked
// against the evaluator itself: every expression found is evaluated, and the text left over is
// what the expansion writes around them.
func TestExpressionsAreTheOnesTheExpansionEvaluates(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"no expression", nil},
		{"$HOME and $5", nil},
		{"$[a]", []string{"a"}},
		{"x $[a] y $[b.c] z", []string{"a", "b.c"}},
		{"$[ a ]", []string{"a"}},
		{"$[items[0].id]", []string{"items[0].id"}},
		{"$[m[k[0]]]", []string{"m[k[0]]"}},
		{`$[m["]"]]`, []string{`m["]"]`}},
		{"$[m[`]`]]", []string{"m[`]`]"}},
		{`$[a ?? "x\]"]`, []string{`a ?? "x]"`}},
		{"$$[a]", nil},
		{"$$[a] $[b]", []string{"b"}},
		{"$[a", nil},
		{".[a]", []string{"a"}},
		{".[a.b ?? \"none\"]", []string{`a.b ?? "none"`}},
		{".[a] and .[b]", nil},
		{"x.[a]", nil},
		{".[0].name", nil},
	} {
		var got []string
		for _, sp := range Expressions(tc.in) {
			got = append(got, sp.Expr)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Expressions(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A span is where the expansion replaces: the text between spans is what it writes as is.
func TestExpressionSpansAreWhatTheExpansionReplaces(t *testing.T) {
	env := map[string]any{"a": "A", "b": map[string]any{"c": "C"}}
	for _, in := range []string{"x $[a] y $[b.c] z", "$[a]$[a]", "pre-$[ a ]-post"} {
		var b strings.Builder
		last := 0
		for _, sp := range Expressions(in) {
			b.WriteString(in[last:sp.Start])
			v, err := tonyeval.ExpandString("$["+sp.Expr+"]", env)
			if err != nil {
				t.Fatal(err)
			}
			b.WriteString(v)
			last = sp.End
		}
		b.WriteString(in[last:])
		want, err := tonyeval.ExpandString(in, env)
		if err != nil {
			t.Fatal(err)
		}
		if b.String() != want {
			t.Errorf("spans of %q rebuild %q; the expansion writes %q", in, b.String(), want)
		}
	}
}

func TestReads(t *testing.T) {
	for _, tc := range []struct {
		expr string
		want []string
	}{
		{"stage", []string{"stage"}},
		{"subject.value.round", []string{"subject.value.round"}},
		{"subject?.value?.round ?? 0", []string{"subject.value.round"}},
		{`subject["value"]`, []string{"subject.value"}},
		{"subject", []string{"subject"}},
		{"subject.ref + subject.name", []string{"subject.ref", "subject.name"}},
		{"items[0].id", []string{"items.0.id"}},
		{"items[i].id", []string{"items", "i"}},
		{"items[-1]", []string{"items"}},
		{`$env["pr-number"]`, []string{"pr-number"}},
		{"$env", []string{"$env"}},
		{"$env[k]", []string{"$env", "k"}},
		{"pr-number", []string{"pr", "number"}},
		{`get(subject.value, "x") ?? "none"`, []string{"subject.value"}},
		{`data.findings | filter(.status == "open")`, []string{"data.findings"}},
		{"map(items, #.id)", []string{"items"}},
		{"all(items, # > limit)", []string{"items", "limit"}},
		{"let x = subject.value; x.round + y", []string{"subject.value", "y"}},
		{"let subject = 1; subject", nil},
		{`status == "done" ? result : err`, []string{"status", "result", "err"}},
		{`"subject.value" + 'x'`, nil},
		{"{a: b, c: [d]}", []string{"b", "d"}},
		{"s.foo()", []string{"s"}},
	} {
		got, err := Reads(tc.expr)
		if err != nil {
			t.Errorf("Reads(%q): %v", tc.expr, err)
			continue
		}
		var gs []string
		for _, p := range got {
			gs = append(gs, strings.Join(p, "."))
		}
		if !reflect.DeepEqual(gs, tc.want) {
			t.Errorf("Reads(%q) = %q, want %q", tc.expr, gs, tc.want)
		}
	}
	if _, err := Reads("a +"); err == nil {
		t.Error("Reads of an expression that does not parse answered no error")
	}
}
