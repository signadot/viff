package charterfile

import (
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
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
