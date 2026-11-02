package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
)

// `$subject.value.x` RESOLVES IN A STRING, which is where it did not.
//
// In a node position it has always been a kpath into the payload. A spawn's strings are bound by
// a flat substitution instead (launch.expand over bindingFor's map), and `value` was left out of
// that map — so the bare `subject` key matched the prefix and the rest stayed literal:
//
//	Review the drift between verse-dev.drift."at.value.sha" and verse-dev.drift."at.value.covering"
//
// went to the runner (git-issue s4rx0tr3h12ksa8fgsn0). A charter had to write the flat `$sha` in
// a prompt and `$subject.value.sha` in the hook two lines below, each the only spelling that
// works where it sits, with nothing saying so.
func TestSubjectValueResolvesInAString(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	specs := make(chan launch.Spec, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle { specs <- spec; return nil })

	at := entity.RefAt("verse-dev", "drift", "at")
	payload := func(extra string) entity.Payload {
		return entity.Obj{
			"stage": "idle", "sha": "aaa111", "covering": "bbb222", "poke": extra,
			"result": map[string]any{"report": "deep"},
		}.Payload()
	}
	entitytest.Set(t, store, at, payload("0"))

	if err := eng.AddTrigger(mustParseCharter(t, `
- name: probe
  condition:
    subject: {path: "verse-dev.drift.*", value: {stage: idle}}
  action:
    patch: {stage: claimed}
    spawn:
      agent: {name: verse}
      autonomy: [{then: allow}]
      prompt: "flat=$sha one=$subject.value.sha two=$subject.value.covering deep=$subject.value.result.report bare=$subject"
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, at, payload("1"))

	var spec launch.Spec
	select {
	case spec = <-specs:
	case <-time.After(5 * time.Second):
		t.Fatal("no spawn")
	}
	// The flat form is unchanged — every charter interpolating $sha gets the bytes it got
	// before — and the bare $subject is still the whole ref.
	want := "flat=aaa111 one=aaa111 two=bbb222 deep=deep bare=" + at.String()
	if spec.Prompt != want {
		t.Fatalf("prompt =\n  %q\nwant\n  %q", spec.Prompt, want)
	}
}

// `$subject.value` on its own is the whole payload, as tony — the same rendering `$stage` gets
// when the field it names holds an object, rather than a second opinion about text form.
func TestSubjectValueOnItsOwnIsThePayload(t *testing.T) {
	subj := entity.RefAt("demo", "pr", "1")
	payload := entity.Obj{"stage": "open", "who": map[string]any{"by": "ana"}}.Payload()
	b := bindingFor(payload, subj, payload)

	whole := b["subject.value"]
	for _, want := range []string{"stage: open", "by: ana"} {
		if !strings.Contains(whole, want) {
			t.Fatalf("$subject.value = %q, which does not carry %q", whole, want)
		}
	}
	if got := b["subject.value.who.by"]; got != "ana" {
		t.Fatalf("$subject.value.who.by = %q, want ana", got)
	}
}

// A FIELD NAME HOLDING A DOT IS NOT BOUND under `value`, itself or anything below it: the map is
// matched by literal text, so `a.b` as a name and `a` containing `b` would produce one key and
// one of them would silently win. Its top-level flat binding is untouched.
func TestADottedFieldNameIsNotBoundUnderValue(t *testing.T) {
	subj := entity.RefAt("demo", "pr", "1")
	payload := entity.Obj{
		"v1.2": map[string]any{"tag": "x"},
		"ok":   "yes",
	}.Payload()
	b := bindingFor(payload, subj, payload)

	if _, found := b["subject.value.v1.2"]; found {
		t.Error("a dotted field name was bound under value, where it cannot be told apart from a path")
	}
	if _, found := b["subject.value.v1.2.tag"]; found {
		t.Error("a field under a dotted name was bound")
	}
	if got := b["subject.value.ok"]; got != "yes" {
		t.Errorf("subject.value.ok = %q — an ordinary name beside it must still bind", got)
	}
	if b["v1.2"] == "" {
		t.Error("the flat top-level binding for a dotted name is gone")
	}
}
