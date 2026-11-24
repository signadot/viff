package charterfile

import (
	"net/url"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/codec"
)

// A RULE A TASK INSTALLED reads back as its author wrote it, and the task is said beside it.
func TestARuleATaskInstalledIsSaidBesideIt(t *testing.T) {
	rule, err := codec.ParseNode([]byte(`{name: build, task: installer, action: {patch: {seen: true}}}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	clean, notes := unmarked(rule, "repos.r.task", false)
	var text strings.Builder
	if err := codec.EncodeNode(&text, clean, codec.Tony); err != nil || strings.Contains(text.String(), "installer") {
		t.Errorf("the rule reads %q (%v), still marked", text.String(), err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "rule build was installed by task installer") {
		t.Errorf("the notes = %q, want one naming the task", notes)
	}
}

// AN INSTALL ACTS AT ONE ADDRESS, which everything it installs lies at or under: the prune's, or
// the deepest address its charters share, each at its own `at:` or the query's.
func TestAnInstallActsAtTheAddressItsChartersShare(t *testing.T) {
	for _, c := range []struct {
		query, docs, want string
	}{
		{"prune=play.issues", "at: play.issues.issue.a", "play.issues"},
		{"prune=play.issues", "", "play.issues"},
		{"", "at: play.issues.issue.a", "play.issues.issue.a"},
		{"", "at: play.issues.issue.a\n---\nat: play.issues.issue.b", "play.issues.issue"},
		{"", "at: play.a\n---\nat: other.b", ""},
		{"at=play.x", "{name: r, condition: {subject: x}, action: {patch: {}}}", "play.x"},
		{"at=play.x", "rules: []", "play.x"},
		{"", "rules: []", ""},
		{"at=play.x", "", "play.x"},
	} {
		q, _ := url.ParseQuery(c.query)
		var docs []*ir.Node
		if c.docs != "" {
			var err error
			if docs, err = codec.ParseNodes([]byte(c.docs), codec.Tony); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := InstallAt(q, docs); err != nil || got != c.want {
			t.Errorf("%q %q: at %q (%v), want %q", c.query, c.docs, got, err, c.want)
		}
	}
}
