package launch

import (
	"slices"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/systems"
)

// AN ABSENT PATH IS REFUSED AND NAMED, and the text is left as written: `$[subject.value.never]`
// on a subject without `never`, an index past a list's end. A `$` that does not open `$[` is not
// an expression, and is neither expanded nor named.
func TestAnAbsentPathIsRefusedAndNamed(t *testing.T) {
	b := Binding{
		"subject": map[string]any{"ref": "demo.pr.1", "value": map[string]any{"stage": "absent"}},
		"changes": []any{"a"}, "stage": "absent",
	}
	s := Spec{Prompt: "never=[$never] deep=[$[subject.value.never]] idx=[$[changes[5]]] ok=[$[stage]]."}
	got, err := s.Bind(b)
	if got.Prompt != s.Prompt {
		t.Fatalf("prompt = %q, want it as written", got.Prompt)
	}
	if err == nil {
		t.Fatal("Bind expanded a path the binding does not hold")
	}
	for _, want := range []string{`prompt: `, `no "never" in subject.value`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not say %q", err, want)
		}
	}
	if _, err := (Spec{Prompt: "idx=[$[changes[5]]]"}).Bind(b); err == nil || !strings.Contains(err.Error(), "no index 5 in changes") {
		t.Errorf("an index past the end: %v", err)
	}
}

// WHAT FOLLOWS AN EXPRESSION IS PROSE: `$[file].go` is the file's name and `.go`.
func TestWhatFollowsAnExpressionIsProse(t *testing.T) {
	got := bind(t, Spec{Prompt: "edit $[file].go at $[stage]. then $[stage]s"}, Binding{"file": "main", "stage": "open"})
	if want := "edit main.go at open. then opens"; got.Prompt != want {
		t.Fatalf("prompt = %q, want %q", got.Prompt, want)
	}
}

// ONE PASS: what a value brings in is not expanded again. The subject's text is data, and an
// expression in it must not be read as though the charter had written it.
func TestASubstitutedValueIsNotExpandedAgain(t *testing.T) {
	got := bind(t, Spec{Prompt: "pr: $[title]"}, Binding{"title": "costs $[owner]", "owner": "ana"})
	if got.Prompt != "pr: costs $[owner]" {
		t.Fatalf("prompt = %q, want the title as it was written", got.Prompt)
	}
}

// A REFUSAL NAMES EVERY FIELD BIND EXPANDS, and nothing Bind leaves alone: a script's text, the
// person, the virtual key, the annotations and the network are carried as written.
func TestARefusalNamesTheFieldsBindExpands(t *testing.T) {
	const ref = "$[a.b]"
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
	_, err := s.Bind(Binding{"a": map[string]any{}})
	if err == nil {
		t.Fatal("Bind expanded a.b, which the binding does not hold")
	}
	var fields []string
	for _, part := range strings.Split(err.Error(), "; ") {
		field, _, _ := strings.Cut(part, ": ")
		fields = append(fields, field)
	}
	want := []string{"agent.model", "agent.name", "environment.systems.git[0].repo", "environment.vars.V", "prompt", "subject"}
	if strings.Join(fields, ",") != strings.Join(want, ",") {
		t.Fatalf("refused fields = %q, want %q", fields, want)
	}
	// And they are the fields Bind expands: bound in full, each of them becomes the value.
	bound := bind(t, s, Binding{"a": map[string]any{"b": "X"}})
	for field, v := range map[string]string{
		"prompt": bound.Prompt, "subject": bound.Subject, "agent.name": bound.Agent.Name,
		"agent.model": bound.Agent.Settings["model"], "environment.vars.V": bound.Environment.Vars["V"],
		"environment.systems.git[0].repo": bound.Environment.Systems["git"][0].Attributes["repo"],
	} {
		if v != "X" {
			t.Errorf("%s = %q, want it expanded", field, v)
		}
	}
	if bound.Script != ref || bound.Person != ref || bound.VirtualKey != ref || bound.Agent.Annotations["x"] != ref ||
		bound.Environment.Systems["network"][0].Attributes["mode"] != ref {
		t.Errorf("Bind expanded a field it carries as written: %+v", bound)
	}
	if got := s.Reads("a"); strings.Join(slices.Sorted(slices.Values(got)), ",") != strings.Join(want, ",") || len((Spec{Script: ref}).Reads("a")) != 0 {
		t.Errorf("Reads = %q, want the fields Bind expands, and only those", got)
	}
}
