package trigger

import (
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
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
