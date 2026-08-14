package entity

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/parse"
)

// `!let` binds a value into a pattern, which is how a charter says "this field differs from that
// value" — the sweep's staleness test, where the value is main's tip and the field is the base a
// proposal was cut from (docs/working/merge-gate-residual.md).
//
// The NEGATED case is pinned here because it was silently wrong: `!not` was dropped when `.[tip]`
// was substituted, so "differs" answered "equals" — no error, the opposite verdict, in a rule
// whose action deletes branches. tony-format cv90ehkvh12krm4sfxn0, fixed in go-tony v0.0.133.
// A predicate that inverts in silence is worth a test even after its cause is gone.
func TestLetBindsAValueIntoAMatch(t *testing.T) {
	pat := func(src string) Pattern {
		t.Helper()
		n, err := parse.Parse([]byte(src), parse.ParseTony())
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		return Pattern{Predicate: n}
	}
	// A proposal cut from abc123.
	doc := Obj{"base": "abc123", "state": "open"}.Payload()

	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"equal to the bound value", `!let {let: [{tip: abc123}], in: {base: .[tip]}}`, true},
		{"not equal to a different one", `!let {let: [{tip: zzz999}], in: {base: !not .[tip]}}`, true},
		{"not equal to its own value", `!let {let: [{tip: abc123}], in: {base: !not .[tip]}}`, false},
		{"equal to a different one", `!let {let: [{tip: zzz999}], in: {base: .[tip]}}`, false},
		// The tag survives substitution for every operator that wears one, not just !not.
		{"a glob over the binding", `!let {let: [{pat: "abc*"}], in: {base: !glob .[pat]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pat(tc.src).matchPayload(doc); got != tc.want {
				t.Fatalf("%s\nagainst %v: match = %v, want %v", tc.src, ToObj(doc), got, tc.want)
			}
		})
	}
}
