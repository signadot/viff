package launch

import (
	"testing"
)

// A call's ARGUMENTS are bound like any other field, per value.
//
// This is the practical difference between a list in a field and a list inside the prompt. A
// prompt is expanded as one blob, so a value with a quote in it has to survive being spliced
// into text that is parsed back as a document; here it is an argument, whatever is in it.
func TestBindExpandsCallInputs(t *testing.T) {
	s := Spec{Calls: []Call{{
		Tool: "run_bash",
		Input: map[string]any{
			"command": "git merge --ff-only $ref",
			"env":     map[string]any{"SUBJECT": "$subject"},
			"args":    []any{"--message", "about $subject"},
			"timeout": 30,
		},
		Fatal: &[]string{"err"},
	}}}
	got := s.Bind(Binding{"ref": `origin/it's-fine`, "subject": "demo:pr:1"})

	in := got.Calls[0].Input
	if in["command"] != `git merge --ff-only origin/it's-fine` {
		t.Errorf("command = %q", in["command"])
	}
	// Nested, because a charter writing `input: {env: {REF: $ref}}` means the same thing by it
	// and a rule that stopped at the first level is a rule nobody could remember.
	if env := in["env"].(map[string]any); env["SUBJECT"] != "demo:pr:1" {
		t.Errorf("nested map not expanded: %v", env)
	}
	if args := in["args"].([]any); args[1] != "about demo:pr:1" {
		t.Errorf("nested list not expanded: %v", args)
	}
	if in["timeout"] != 30 {
		t.Errorf("a non-string argument was mangled: %v", in["timeout"])
	}
	// The original is untouched: Bind is pure, and a charter's own spec is reused per firing.
	if s.Calls[0].Input["command"] != "git merge --ff-only $ref" {
		t.Error("Bind mutated the charter's own spec")
	}
}

// The tool NAME is not expanded, on the argument Tools already makes: it names something from a
// fixed vocabulary, so a $var in one could only name a tool that does not exist — or one chosen
// by whoever last wrote the entity being matched, which is authority arriving through a payload.
func TestBindLeavesTheToolNameAlone(t *testing.T) {
	s := Spec{Calls: []Call{{Tool: "$tool", Fatal: &[]string{"$outcome"}}}}
	got := s.Bind(Binding{"tool": "push", "outcome": "err"})
	if got.Calls[0].Tool != "$tool" {
		t.Errorf("tool = %q, want it left as written", got.Calls[0].Tool)
	}
	if got.Calls[0].Fatal == nil || (*got.Calls[0].Fatal)[0] != "$outcome" {
		t.Errorf("fatal = %v, want it left as written", got.Calls[0].Fatal)
	}
}
