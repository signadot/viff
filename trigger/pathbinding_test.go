package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// A READ MAY NOT NAME A `!let` BINDING, and finds out at install.
//
// The shape below is the one a charter author writes: bind main's tip, then list the proposals
// cut from it. Tony's own `let` binds a sibling — `let: [{a: x}, {b: .[a]}]` resolves — so this
// reads as though it works. It does not, because verse answers the read BEFORE tony evaluates
// the let: `{base: .[tip]}` is matched with `.[tip]` as a literal string, no payload has that
// value, and the binding lands on the EMPTY LIST. The rule then decides on an answer nobody
// computed, silently.
//
// Filed as a panic out of go-tony (git-issue jwqdhfrxh12krpvbg9n0). v0.0.179 fixed the panic,
// which is what turned it into a wrong answer instead of a crash — the reason a refusal, not a
// recover, is what this needs.
func TestAReadCannotNameALetBinding(t *testing.T) {
	for _, tc := range []struct {
		name, charter, want string
	}{
		{
			name: "in a list read's value",
			charter: `
- name: probe
  condition:
    subject:
      path: "git.head.*"
      value: !let
        let:
        - tip: !get-path {at: "git.head.verse", path: sha}
        - fresh: !list-paths
            at: "verse-dev.merge.proposal.*"
            value: {base: .[tip]}
            path: branch
        in: {branches: .[fresh]}
  action:
    patch: {saw: yes}
`,
			want: "its value names the binding .[tip]",
		},
		{
			name: "in a read's address",
			charter: `
- name: probe
  condition:
    subject:
      path: "git.head.*"
      value: !let
        let:
        - tip: !get-path {at: "git.head.verse", path: sha}
        - one: !get-path {at: [merge, proposal, .[tip]], path: branch}
        in: {branch: .[one]}
  action:
    patch: {saw: yes}
`,
			want: "its address names the binding .[tip]",
		},
		{
			name: "in a read's path",
			charter: `
- name: probe
  condition:
    subject:
      path: "git.head.*"
      value: !let
        let:
        - field: !get-path {at: "git.head.verse", path: which}
        - one: !get-path {at: "merge.proposal.p1", path: .[field]}
        in: {branch: .[one]}
  action:
    patch: {saw: yes}
`,
			want: "its path names the binding .[field]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := New(entitytest.Store(t), task.AutoResolver{})
			err := eng.AddTrigger(mustParseCharter(t, tc.charter))
			if err == nil {
				t.Fatal("installed — a read naming a binding answers nothing, silently")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal does not say which binding or where:\n%v", err)
			}
		})
	}
}

// The refusal is about a WHOLE-NODE reference, which is what a binding reference is. A string
// that merely contains the characters is text, and a read that names no binding at all installs.
func TestAReadThatNamesNoBindingInstalls(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: probe
  condition:
    subject:
      path: "git.head.*"
      value: !let
        let:
        - fresh: !list-paths
            at: "verse-dev.merge.proposal.*"
            value: {base: "a.[tip]b"}
            path: branch
        in: {branches: .[fresh]}
  action:
    patch: {saw: yes}
`)); err != nil {
		t.Fatalf("refused a read that names no binding: %v", err)
	}
}
