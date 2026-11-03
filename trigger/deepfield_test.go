package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
)

// bound is spec bound against b, for a binding that must expand.
func bound(t *testing.T, spec launch.Spec, b launch.Binding) launch.Spec {
	t.Helper()
	got, err := spec.Bind(b)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	return got
}

// refused is why spec does not bind against b, failing where it does.
func refused(t *testing.T, spec launch.Spec, b launch.Binding) string {
	t.Helper()
	got, err := spec.Bind(b)
	if err == nil {
		t.Fatalf("Bind expanded %q to %q, want it refused", spec.Prompt, got.Prompt)
	}
	return err.Error()
}

// A PATH UNDER A TOP-LEVEL FIELD BINDS IN A SPAWN'S STRINGS, as a path under `subject.value`
// does and as a patch resolves it at any depth: `$[data.severity]` on `{data: {severity: high}}`
// is `high`.
func TestAPathUnderATopLevelFieldBindsInASpawn(t *testing.T) {
	payload := entity.Obj{"data": map[string]any{"severity": "high", "tags": map[string]any{"area": "auth"}}}.Payload()
	b := bindingFor(payload, entity.RefAt("demo", "pr", "1"), payload)
	spec := launch.Spec{Prompt: "sev=$[data.severity] area=$[data.tags.area] deep=$[subject.value.data.severity]"}
	if got, want := bound(t, spec, b).Prompt, "sev=high area=auth deep=high"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}

// A PAYLOAD FIELD CALLED `subject` DOES NOT STAND IN FOR THE SUBJECT, nor any path under it: with
// `subject.value` left out (a hook with no commit to read at), a payload's `subject: {value: …}`
// must not be read there.
func TestAPayloadsSubjectFieldDoesNotBindUnderSubject(t *testing.T) {
	payload := entity.Obj{"subject": map[string]any{"value": "forged"}}.Payload()
	b := bindingFor(payload, entity.RefAt("demo", "pr", "1"), nil)
	subj, _ := b["subject"].(map[string]any)
	if v, ok := subj["value"]; ok {
		t.Errorf("subject.value = %v, bound from the payload's own `subject` field", v)
	}
	if subj["ref"] != entity.RefAt("demo", "pr", "1").String() {
		t.Errorf("subject.ref = %v, want the resolved subject", subj["ref"])
	}
}

// AN ELEMENT OF A LIST BINDS IN A SPAWN'S STRINGS, at any depth and under `subject.value` too, as
// a patch resolves `.[result.findings[0].id]`. An index past the end is not present, and is refused.
func TestAListElementBindsInASpawn(t *testing.T) {
	payload := entity.Obj{"data": map[string]any{
		"items":    []any{"a", "b"},
		"findings": []any{map[string]any{"id": "x"}},
		"grid":     []any{[]any{"p", "q"}},
	}}.Payload()
	b := bindingFor(payload, entity.RefAt("demo", "pr", "1"), payload)
	spec := launch.Spec{Prompt: "first=$[data.items[0]] id=$[data.findings[0].id] cell=$[data.grid[0][1]] " +
		"deep=$[subject.value.data.items[1]] deepid=$[subject.value.data.findings[0].id]"}
	if got, want := bound(t, spec, b).Prompt, "first=a id=x cell=q deep=b deepid=x"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
	for prompt, want := range map[string]string{
		"gone=$[data.items[5]]":                     "no index 5 in data.items",
		"deep=$[subject.value.data.findings[1].id]": "no index 1 in subject.value.data.findings",
	} {
		if why := refused(t, launch.Spec{Prompt: prompt}, b); !strings.Contains(why, want) {
			t.Errorf("%s: refused as %q, want it to say %q", prompt, why, want)
		}
	}
}

// A PATH PAST A NULL DOES NOT RESOLVE, in a spawn's strings as in a patch: a task's result keeps a
// null it wrote, so an onDone's `$[data.findings[0].id]` on `{findings: null}` is refused. A bare
// written null is `null`. A null `subject.value` is refused past it the same way.
func TestAPathPastANullIsNotPresentInASpawn(t *testing.T) {
	st := entity.Obj{"status": "done"}
	st["data"] = mirror.ParseResult(`{"findings": null, "summary": {"n": null}}`)
	b := bindingFor(st.Payload(), entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	for _, prompt := range []string{"first=[$[data.findings[0].id]]", "n=[$[data.summary.n.z]]"} {
		refused(t, launch.Spec{Prompt: prompt}, b)
	}
	if got, want := bound(t, launch.Spec{Prompt: "n=[$[data.summary.n]]"}, b).Prompt, "n=[null]"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}

	nb := bindingFor(st.Payload(), entity.RefAt("demo", "pr", "1"), ir.Null())
	refused(t, launch.Spec{Prompt: "$[subject.value.stage]"}, nb)
}
