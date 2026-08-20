package trigger

import (
	"context"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// ABSENCE AS THE INITIAL STATE, and a field pinned once — the two halves of what keeps a
// derived record's facts from fighting the workflow's decisions about it.
//
// A derive merges its literal fields every time it runs, so a state written into one is
// restated on every re-derive: a merged proposal was dragged back to undecided and retired that
// way on staging, and `base` — the commit a branch was cut from — tracked main's current tip
// instead of staying put, which is the left side of the staleness test converging on the right.
//
// The shape this pins is the fix (docs/working/merge-gate-residual.md): the derive owns facts,
// a latching patch pins what is time-dependent ONCE, and the absence of a field is what
// "nobody has decided" means — so there is no literal for a derive to restate.
func TestAbsenceIsUndecidedAndAPinHolds(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	head := entity.RefAt("git", "head", "verse")
	setPayload(t, store, head, `{sha: aaa111}`)

	// rule 1b (pin base once) and rule 3's selector (undecided AND passed by), as proposed.
	for _, src := range []string{`
- name: proposal-based
  condition:
    subject: {path: [merge, proposal, *], value: !not.has-path base}
  action:
    patch: {base: !get-path {at: [git, head, verse], path: sha}}
`, `
- name: proposal-outrun
  condition:
    subject:
      system: merge
      kind: proposal
      value: !and
      - !not.has-path state
      - !let
        let: [{tip: !get-path {at: [git, head, verse], path: sha}}]
        in: {base: !not .[tip]}
  action:
    patch: {state: stale}
`} {
		if err := eng.AddTrigger(mustParseCharter(t, src)); err != nil {
			t.Fatalf("install: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// A proposal appears with no base and no state — what the trimmed derive writes.
	fresh := entity.RefAt("merge", "proposal", "fresh")
	setPayload(t, store, fresh, `{branch: "verse/auto/x", sha: bbb}`)
	waitField(t, store, fresh, "base", "aaa111") // 1b pinned it, once

	// A decided one is left alone even when main passes it by.
	decided := entity.RefAt("merge", "proposal", "decided")
	setPayload(t, store, decided, `{branch: "verse/auto/y", sha: ccc, base: aaa111, state: merged}`)

	setPayload(t, store, head, `{sha: zzz999}`) // main moves
	waitField(t, store, fresh, "state", "stale")
	if got := field(t, store, decided, "state"); got != "merged" {
		t.Fatalf("a decided proposal became %v — absence is not being read as undecided", got)
	}
	// And the pin held: base is still the commit it was cut from, not the new tip.
	if got := field(t, store, fresh, "base"); got != "aaa111" {
		t.Fatalf("base = %v, want the pinned aaa111 — it tracked the tip", got)
	}
}
