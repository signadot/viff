package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// A FIRING THAT CANNOT BE RESOLVED WRITES NOTHING. The patch is what takes the subject out of
// the rule's selection, so one that lands ahead of a derive that then fails latches the subject
// over a write that never happened: the rule never fires again to try, and the verse says the
// work was done and holds no record of it.
//
// The rule below is the shape that lost a finding on staging: a fan-out whose element names a
// field one of the findings did not carry. Both findings' entities and the subject's own patch
// are resolved before anything is written, so the firing fails with the subject where it was,
// and a corrected answer is a commit the rule fires on.
func TestAFiringThatCannotBeResolvedWritesNothing(t *testing.T) {
	src := `
- name: reflected
  level: 0
  match: {path: "git"}
- name: findings
  level: 10
  match: {path: "drift"}
---
- name: fan
  stratum: reflected
  condition:
    subject: {path: "git.commit.*", value: {reviewed: pending}}
  action:
    patch: {reviewed: done}
    derive: [{path: "drift.finding", value: !each {over: $subject.value.f, key: $item.sym, value: {sym: $item.sym, proposed: $item.proposedAt}}}]
`
	trigs, _, strata, _, ok := parseBlock(src)
	if !ok {
		t.Fatal("did not parse as a charter")
	}
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	for _, s := range strata {
		if err := eng.addStratum(s); err != nil {
			t.Fatalf("stratum %q: %v", s.Name, err)
		}
	}
	if err := eng.addTrigger(trigs[0], ""); err != nil {
		t.Fatalf("does not install: %v", err)
	}

	subj := entity.RefAt("git", "commit", "verse/abc")
	fire := func(findings string) error {
		t.Helper()
		payload, err := codec.ParseNode([]byte(`{reviewed: pending, f: `+findings+`}`), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Write(entity.Write{At: subj.At(), Value: payload}); err != nil {
			t.Fatal(err)
		}
		oc := occasion{commit: entity.Commit{Rev: store.Rev()}, snap: &snapCache{}, ev: entity.Event{Ref: subj, To: payload}}
		_, err = trigs[0].Action.applyOne("fan", oc, subj, eng, fromSubject)
		return err
	}
	derived := func() int {
		return len(store.ListAt([]string{"drift", "finding"}, 2+1+len(subj.Segs())+1, nil))
	}

	// The second finding carries no proposedAt. The first resolves, and is not written either.
	err := fire(`[{sym: "entity.a", proposedAt: "r1"} {sym: "entity.b"}]`)
	if err == nil || !strings.Contains(err.Error(), "proposedAt") {
		t.Fatalf("the firing's error = %v, want it to name the field that was not there", err)
	}
	if got := entity.Str(mustGet(t, store, subj), "reviewed"); got != "pending" {
		t.Errorf("the subject is %q: the patch landed ahead of a derive that failed, so the rule will not fire on it again", got)
	}
	if n := derived(); n != 0 {
		t.Errorf("%d findings were written by a firing that failed", n)
	}

	// The same rule, over an answer that resolves: the patch and both findings land.
	if err := fire(`[{sym: "entity.a", proposedAt: "r1"} {sym: "entity.b", proposedAt: "r2"}]`); err != nil {
		t.Fatalf("a firing that resolves failed: %v", err)
	}
	if got := entity.Str(mustGet(t, store, subj), "reviewed"); got != "done" {
		t.Errorf("the subject is %q after a firing that resolved", got)
	}
	if n := derived(); n != 2 {
		t.Errorf("%d findings were written, want 2", n)
	}
}
