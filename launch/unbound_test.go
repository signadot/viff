package launch

import (
	"slices"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/systems"
)

// A REFERENCE THAT REACHES PAST A KEY IS NOT HALF-SUBSTITUTED. `$subject.value.never` on a
// subject without `never` matched the shorter `subject.value` and kept the rest of its own
// spelling, so the text read as the whole value followed by `.never`. It is left whole now, and
// Unbound names it, which is what a firing refuses on.
func TestAReferencePastAKeyIsLeftWholeAndNamed(t *testing.T) {
	b := Binding{"subject": "demo.pr.1", "subject.value": "stage: absent", "stage": "absent"}
	s := Spec{Prompt: "never=[$never] deep=[$subject.value.never] flat=[$stage.never] idx=[$stage[0]] ok=[$stage]."}

	got := s.Bind(b).Prompt
	want := "never=[$never] deep=[$subject.value.never] flat=[$stage.never] idx=[$stage[0]] ok=[absent]."
	if got != want {
		t.Fatalf("prompt =\n  %q\nwant\n  %q", got, want)
	}
	gotRefs := s.Unbound(b)
	wantRefs := []string{"prompt: $stage.never", "prompt: $stage[0]", "prompt: $subject.value.never"}
	if !slices.Equal(gotRefs, wantRefs) {
		t.Fatalf("Unbound = %q, want %q — a bare $never stays as written and is not named", gotRefs, wantRefs)
	}
}

// What ends a reference is what always ended one: a full stop, a space, a letter run on from the
// key. None of those is a reference reaching past it.
func TestAKeyFollowedByPunctuationIsNotAReferencePastIt(t *testing.T) {
	b := Binding{"stage": "open", "sub": "S"}
	s := Spec{Prompt: "at $stage. then $stage.\n$stage.5 $stages $sub-x"}
	if refs := s.Unbound(b); len(refs) != 0 {
		t.Fatalf("Unbound = %q, want none", refs)
	}
	if got := s.Bind(b).Prompt; got != "at open. then open.\nopen.5 opens S-x" {
		t.Fatalf("prompt = %q", got)
	}
}

// ONE PASS: what a value brings in is not expanded again. The subject's text is data, and a `$`
// in it must not be read as though the charter had written it.
func TestASubstitutedValueIsNotExpandedAgain(t *testing.T) {
	b := Binding{"title": "costs $owner", "owner": "ana"}
	if got := (Spec{Prompt: "pr: $title"}).Bind(b).Prompt; got != "pr: costs $owner" {
		t.Fatalf("prompt = %q, want the title as it was written", got)
	}
}

// Unbound reads every field Bind expands, and nothing Bind leaves alone: a field it missed
// would launch with a reference it never saw, and one it read and Bind does not would refuse
// a script over its own `$b`.
func TestUnboundReadsWhatBindExpands(t *testing.T) {
	const ref = "$a.b"
	s := Spec{
		Prompt: ref, Subject: ref, Script: ref, Person: ref, VirtualKey: ref,
		Agent: &runner.Agent{Name: ref, Settings: map[string]string{"model": ref}, Annotations: map[string]string{"x": ref}},
		Environment: systems.Environment{
			Vars: map[string]string{"V": ref},
			Systems: systems.Systems{
				"git":     {{Attributes: map[string]string{"repo": ref}, Verbs: map[string][]systems.Shape{"push": {{"ref": ir.FromString(ref)}}}}},
				"network": {{Attributes: map[string]string{"mode": ref}}},
			},
		},
	}
	got := s.Unbound(Binding{"a": "A"})
	want := []string{
		"agent.model: $a.b", "agent.name: $a.b", "environment.systems.git[0].repo: $a.b",
		"environment.vars.V: $a.b", "prompt: $a.b", "subject: $a.b",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Unbound =\n  %q\nwant\n  %q", got, want)
	}
	// And they are the fields Bind expands: bound in full, each of them becomes the value.
	bound := s.Bind(Binding{"a.b": "X"})
	for field, v := range map[string]string{
		"prompt": bound.Prompt, "subject": bound.Subject, "agent.name": bound.Agent.Name,
		"agent.model": bound.Agent.Settings["model"], "environment.vars.V": bound.Environment.Vars["V"],
		"environment.systems.git[0].repo": bound.Environment.Systems["git"][0].Attributes["repo"],
	} {
		if v != "X" {
			t.Errorf("%s = %q: Unbound names it and Bind does not expand it", field, v)
		}
	}
	if !s.Mentions("a.b") || (Spec{Script: ref}).Mentions("a") {
		t.Error("Mentions does not read the fields Bind expands, and only those")
	}
}
