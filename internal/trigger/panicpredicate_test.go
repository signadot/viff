package trigger

import (
	"context"
	"testing"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/task"
)

// A PREDICATE THAT PANICS DOES NOT TAKE THE VIFF DOWN WITH IT.
//
// `!let {let: [{}], in: …}` — an empty binding, which is what a typo leaves behind — makes
// go-tony index a binding list that has no element 0. A charter is user input and its
// predicates are evaluated on the engine's watch goroutine at every commit landing under the
// rule, so before this the first such change exited viffd. Nothing else a rule can say does
// that: unparseable is refused at install, a malformed operator is a non-match, a read that
// cannot resolve fails its own firing.
//
// Two things are asked here, and the second is why this is a test rather than a recover nobody
// checks: the broken rule does not fire (a selector that cannot answer must not select), and
// THE RULE BESIDE IT STILL DOES. A recover that quietly wedged the watch would pass the first
// half and be worthless.
func TestAPanickingPredicateDoesNotStopTheEngine(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	if err := eng.AddTrigger(mustParseCharter(t, `
- name: broken
  condition:
    subject:
      path: "demo.pr.*"
      value: !let
        let: [{}]
        in: {stage: open}
  action:
    patch: {broken: fired}
`)); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: sound
  condition:
    subject:
      path: "demo.pr.*"
      value: {stage: open}
  action:
    patch: {sound: fired}
`)); err != nil {
		t.Fatalf("install: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())

	// The sound rule lands, which is only observable at all if the engine is still running.
	waitField(t, store, ref, "sound", "fired")

	e, _, _ := store.Get(ref)
	if got := entity.Str(e.Payload, "broken"); got != "" {
		t.Errorf("the rule whose predicate panicked fired anyway (broken=%q)", got)
	}
}
