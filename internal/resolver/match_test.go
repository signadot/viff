package resolver

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/internal/trigger"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
)

func about(t *testing.T, s string) trigger.Match {
	t.Helper()
	if s == "" {
		return nil
	}
	m, err := trigger.ParseExpr(s)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Two matches overlap where one ask could satisfy both: their paths could meet, and no label both
// name has two values. An absent dimension admits any ask.
func TestTwoMatchesOverlapWhereOneAskMeetsBoth(t *testing.T) {
	m := func(a string, labels map[string]string) *Match { return &Match{About: about(t, a), Labels: labels} }
	for _, c := range []struct {
		a, b *Match
		want bool
	}{
		{m("demo.pr.*", nil), m("demo.pr.*", nil), true},
		{m("demo.pr.*", nil), m(`demo.pr."1"`, nil), true},
		{m("demo.pr.*", nil), m("demo.issue.*", nil), false},
		{m("", nil), m("demo.issue.*", nil), true}, // no about is every about
		{m("demo.pr.*", map[string]string{"team": "a"}), m("demo.pr.*", map[string]string{"team": "b"}), false},
		{m("demo.pr.*", map[string]string{"team": "a"}), m("demo.pr.*", map[string]string{"kind": "x"}), true},
		{m("", map[string]string{"team": "a"}), m("", nil), true},
	} {
		if got := c.a.Overlaps(c.b); got != c.want {
			t.Errorf("%v / %v overlap = %v, want %v", c.a, c.b, got, c.want)
		}
		if got := c.b.Overlaps(c.a); got != c.want {
			t.Errorf("%v / %v overlap = %v, want %v (the other way round)", c.b, c.a, got, c.want)
		}
	}
}

// A match admits an ask about its path or beneath it, carrying every label it names with its
// value; an ask about nothing is admitted only by a match with no path.
func TestAMatchAdmitsTheAsksItWatches(t *testing.T) {
	pr := &Match{About: about(t, "demo.pr.*"), Labels: map[string]string{"team": "a"}}
	for _, c := range []struct {
		about  entity.Ref
		labels map[string]string
		want   bool
	}{
		{entity.RefAt("demo", "pr", "1"), map[string]string{"team": "a", "kind": "x"}, true},
		{entity.RefAt("demo", "pr", "1", "stage"), map[string]string{"team": "a"}, true},
		{entity.RefAt("demo", "pr", "1"), map[string]string{"team": "b"}, false},
		{entity.RefAt("demo", "pr", "1"), nil, false},
		{entity.RefAt("demo", "issue", "1"), map[string]string{"team": "a"}, false},
		{entity.Ref{}, map[string]string{"team": "a"}, false},
	} {
		if got := pr.Admits(c.about, c.labels); got != c.want {
			t.Errorf("an ask about %s with %v admitted = %v, want %v", c.about, c.labels, got, c.want)
		}
	}
	if !(&Match{}).Admits(entity.Ref{}, nil) {
		t.Error("an empty match does not admit an ask about nothing")
	}
}

// A match is required, and is refused where its overlap could not be decided or where it would
// route.
func TestAMatchIsCheckedAtInstall(t *testing.T) {
	base := Resolver{Name: "r", Method: Method{Driver: DriverService}}
	var withValue Resolver
	if err := codec.FromNode(mustNode(t, `{name: r, match: {about: {path: "demo.pr.*", value: {stage: open}}}, method: {driver: service}}`), &withValue); err != nil {
		t.Fatal(err)
	}
	if err := withValue.Check(); err == nil || !strings.Contains(err.Error(), "is a path, with no value") {
		t.Errorf("a match over a value: %v", err)
	}
	if err := base.Check(); err == nil || !strings.Contains(err.Error(), "needs a `match`") {
		t.Errorf("a resolver with no match: %v", err)
	}
	badLabel := base
	badLabel.Match = &Match{Labels: map[string]string{"a b": "c"}}
	if err := badLabel.Check(); err == nil || !strings.Contains(err.Error(), "a label's name is a word") {
		t.Errorf("a match with a label holding a space: %v", err)
	}
	ok := base
	ok.Match = &Match{}
	if err := ok.Check(); err != nil {
		t.Errorf("an empty match: %v", err)
	}
	node := mustNode(t, `{name: r, match: {to: user:peggy}, method: {driver: service}}`)
	if err := CheckFields(node); err == nil || !strings.Contains(err.Error(), "a match does not route") {
		t.Errorf("a match naming `to`: %v", err)
	}
}

func mustNode(t *testing.T, doc string) *ir.Node {
	t.Helper()
	n, err := codec.ParseNode([]byte(doc), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// THE LADDER IS RETIRED (9ce3bva0, stage 4): a resolver still written with a field of it is refused,
// told what took its place, rather than installed doing less than it says.
func TestTheLaddersFieldsAreRetired(t *testing.T) {
	for _, doc := range []string{
		`{name: r, match: {}, condition: {subject: {path: "change.pr.*"}}, method: {driver: service}}`,
		`{name: r, match: {}, byRef: true, method: {driver: service}}`,
		`{name: r, match: {}, deadline: {within: "4h"}, method: {driver: service}}`,
		`{name: r, match: {}, exclusive: true, method: {driver: service}}`,
		`{name: r, match: {}, method: {driver: person, who: peggy@example.com}}`,
		`{name: r, match: {}, method: {driver: group, group: "group:qa"}}`,
	} {
		if err := CheckFields(mustNode(t, doc)); err == nil || !strings.Contains(err.Error(), "retired") {
			t.Errorf("%s: %v, want it refused as retired", doc, err)
		}
	}
	var r Resolver
	if err := codec.FromNode(mustNode(t, `{name: r, match: {}, method: {driver: person}}`), &r); err != nil {
		t.Fatal(err)
	}
	if err := r.Check(); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Errorf("the person driver: %v, want it refused as retired", err)
	}
}
