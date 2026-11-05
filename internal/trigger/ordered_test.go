package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/task"
)

// A CONDITION ORDERS NUMBERS, one field against another in the same payload: go-tony's `!ge`,
// scaled, over its `!get-path(root)`. verse's own `!get-path` is a store read and is spotted by
// the HEAD of a tag chain (pathKind), so `!ge(0.8).get-path(root)` reaches tony as the match it
// is rather than being taken for a read of `caps.monthlySandboxes`.
//
// The case is a quota: an org at 80% of its cap is selected, one well under it is not.
func TestAConditionComparesOneFieldWithAnother(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	for _, st := range []Stratum{
		{Name: "status", Level: 0, Match: []Match{mm("quota")}},
		{Name: "alerts", Level: 10, Match: []Match{mustPathMatch(t, `{path: "demo.alert"}`)}},
	} {
		if err := eng.AddStratum(st); err != nil {
			t.Fatalf("stratum %q: %v", st.Name, err)
		}
	}
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: nearing
  stratum: status
  condition:
    subject:
      path: "quota.status.*"
      value:
        usage: {monthlySandboxes: !ge(0.8).get-path(root) caps.monthlySandboxes}
  action:
    derive:
    - path: "demo.alert"
      key: .[subject.value.org]
      value: {org: .[subject.value.org]}
`)); err != nil {
		t.Fatalf("the rule did not install: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	status := func(org string, used int) {
		entitytest.Set(t, store, entity.RefAt("quota", "status", org), entity.Obj{
			"org":   org,
			"usage": entity.Obj{"monthlySandboxes": used},
			"caps":  entity.Obj{"monthlySandboxes": 633},
		}.Payload())
	}
	status("far", 100)
	status("near", 520) // 520 ≥ 0.8 × 633 = 506.4

	// `demo.alert`, the rule, the segments of `quota.status.<org>`, and the key.
	const depth = 2 + 1 + 3 + 1
	waitDeep(t, store, []string{"demo", "alert"}, depth)
	time.Sleep(200 * time.Millisecond) // and give the far one its chance to fire wrongly
	got := store.ListAt([]string{"demo", "alert"}, depth, nil)
	if len(got) != 1 {
		t.Fatalf("want one alert, got %d: %v", len(got), got)
	}
	if org, _ := entity.ToObj(got[0].Payload)["org"].(string); org != "near" {
		t.Errorf("the alert is for %q, want near", org)
	}
}
