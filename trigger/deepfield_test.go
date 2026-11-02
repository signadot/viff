package trigger

import (
	"slices"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
)

// A PATH UNDER A TOP-LEVEL FIELD BINDS IN A SPAWN'S STRINGS, as a path under `$subject.value`
// does and as a patch resolves it at any depth: `$data.severity` on `{data: {severity: high}}` is
// `high`. It was refused as not present, which it was.
func TestAPathUnderATopLevelFieldBindsInASpawn(t *testing.T) {
	payload := entity.Obj{"data": map[string]any{"severity": "high", "tags": map[string]any{"area": "auth"}}}.Payload()
	b := bindingFor(payload, entity.RefAt("demo", "pr", "1"), payload)
	spec := launch.Spec{Prompt: "sev=$data.severity area=$data.tags.area deep=$subject.value.data.severity"}
	if unbound := spec.Unbound(b); len(unbound) != 0 {
		t.Errorf("unbound = %q, want none: every path named is present", unbound)
	}
	if got, want := spec.Bind(b).Prompt, "sev=high area=auth deep=high"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}

// A PAYLOAD FIELD CALLED `subject` DOES NOT STAND IN FOR THE SUBJECT, nor any path under it: with
// `$subject.value` left unbound (a hook with no commit to read at), a payload's
// `subject: {value: …}` must not bind there.
func TestAPayloadsSubjectFieldDoesNotBindUnderSubject(t *testing.T) {
	payload := entity.Obj{"subject": map[string]any{"value": "forged"}}.Payload()
	b := bindingFor(payload, entity.RefAt("demo", "pr", "1"), nil)
	if v, ok := b["subject.value"]; ok {
		t.Errorf("subject.value = %q, bound from the payload's own `subject` field", v)
	}
	if b["subject"] != entity.RefAt("demo", "pr", "1").String() {
		t.Errorf("subject = %q, want the resolved subject", b["subject"])
	}
}

// AN ELEMENT OF A LIST BINDS IN A SPAWN'S STRINGS, at any depth and under `$subject.value` too, as
// a patch resolves `$result.findings[0].id`. No element was bound, so each was refused as not
// present, which it was. An index past the end is not present, and is refused.
func TestAListElementBindsInASpawn(t *testing.T) {
	payload := entity.Obj{"data": map[string]any{
		"items":    []any{"a", "b"},
		"findings": []any{map[string]any{"id": "x"}},
		"grid":     []any{[]any{"p", "q"}},
	}}.Payload()
	b := bindingFor(payload, entity.RefAt("demo", "pr", "1"), payload)
	spec := launch.Spec{Prompt: "first=$data.items[0] id=$data.findings[0].id cell=$data.grid[0][1] " +
		"deep=$subject.value.data.items[1] deepid=$subject.value.data.findings[0].id"}
	if unbound := spec.Unbound(b); len(unbound) != 0 {
		t.Errorf("unbound = %q, want none: every element named is present", unbound)
	}
	if got, want := spec.Bind(b).Prompt, "first=a id=x cell=q deep=b deepid=x"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
	past := launch.Spec{Prompt: "gone=$data.items[5] deep=$subject.value.data.findings[1].id"}
	want := []string{"prompt: $data.items[5]", "prompt: $subject.value.data.findings[1].id"}
	if got := past.Unbound(b); !slices.Equal(got, want) {
		t.Errorf("unbound = %q, want %q", got, want)
	}
}

// A TOP-LEVEL NAME SPELLED LIKE A PATH KEEPS ITS SPELLING: on `{a: {b: nested}, "a.b": flat}`,
// `$a.b` is the field called `a.b`, and not `b` under `a`, whichever the map ranged over last.
func TestADottedTopLevelNameKeepsItsSpelling(t *testing.T) {
	payload := entity.Obj{"a": map[string]any{"b": "nested"}, "a.b": "flat"}.Payload()
	for range 20 {
		b := bindingFor(payload, entity.RefAt("demo", "pr", "1"), payload)
		if got := b["a.b"]; got != "flat" {
			t.Fatalf("a.b = %q, want the top-level field `a.b`, flat", got)
		}
	}
}

// A PATH PAST A NULL IS NOT PRESENT, in a spawn's strings as in a patch (a path into null does not
// resolve): a task's result keeps a null it wrote, so an onDone's `$data.findings[0].id` on
// `{findings: null}` is refused, not rendered as `[0].id`. A bare `$data.summary.n` is "". A null
// `$subject.value` is refused past it the same way.
func TestAPathPastANullIsNotPresentInASpawn(t *testing.T) {
	st := entity.Obj{"status": "done"}
	st["data"] = mirror.ParseResult(`{"findings": null, "summary": {"n": null}}`)
	b := bindingFor(st.Payload(), entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	spec := launch.Spec{Prompt: "first=[$data.findings[0].id] n=[$data.summary.n.z]"}
	want := []string{"prompt: $data.findings[0].id", "prompt: $data.summary.n.z"}
	if got := spec.Unbound(b); !slices.Equal(got, want) {
		t.Errorf("unbound = %q, want %q", got, want)
	}
	bare := launch.Spec{Prompt: "n=[$data.summary.n]"}
	if got, want := bare.Bind(b).Prompt, "n=[]"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}

	nb := bindingFor(st.Payload(), entity.RefAt("demo", "pr", "1"), ir.Null())
	sv := launch.Spec{Prompt: "$subject.value.stage"}
	if got, want := sv.Unbound(nb), []string{"prompt: $subject.value.stage"}; !slices.Equal(got, want) {
		t.Errorf("unbound = %q, want %q", got, want)
	}
}
