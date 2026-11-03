package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// A CHANGE ELSEWHERE WHILE THE SUBJECT IS FIRING IS NOT LOST. The rule copies a repo's branch into
// an open pr, and its condition reads the repo, so a change there wakes it. The pr's firing is
// held open by hand; the repo moves meanwhile, and the firing that move wakes is kept and fired
// when the held one ends — unless the subject stopped qualifying by then. A change to the SUBJECT
// meanwhile is not kept: the running firing is usually its author (git-issue 8z4azhtjh12krmmqqsn0).
func TestAChangeElsewhereDuringAFiringIsNotLost(t *testing.T) {
	eng, store := newEng(t)
	repo := entity.RefAt("git", "repo", "abc")
	entitytest.Set(t, store, repo, entity.Obj{"branch": "main"}.Payload())
	if err := eng.AddTrigger(Trigger{
		Name:      "copy",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": readOf(`!let {let: [{repo: !get-path {at: "git.repo.abc"}}], in: {stage: open}}`)}},
		Action: Action{Patch: entity.Obj{
			"branch": readOf(`!get-path {at: [git, repo, .[subject.value.head]], path: branch}`),
		}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	pr := entity.RefAt("demo", "pr", "1")
	branchOf := func() string {
		e, _, _ := store.Get(pr)
		return entity.Str(e.Payload, "branch")
	}
	waitBranch := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); branchOf() != want; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("branch is %q, want %q", branchOf(), want)
			}
		}
	}
	entitytest.Set(t, store, pr, entity.Obj{"stage": "open", "head": "abc"}.Payload())
	waitBranch("main")
	time.Sleep(100 * time.Millisecond) // the firing its own write woke has ended

	held := func() *flight {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			if fl, ok := eng.claimFlight("copy", pr); ok {
				return fl
			}
			if time.Now().After(deadline) {
				t.Fatal("the subject never stopped firing")
			}
		}
	}
	kept := func(fl *flight) bool {
		fl.mu.Lock()
		defer fl.mu.Unlock()
		return fl.again != nil
	}
	waitKept := func(fl *flight) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !kept(fl); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the repo moving during the firing woke nothing that was kept")
			}
		}
	}

	fl := held()
	entitytest.Set(t, store, pr, entity.Obj{"stage": "open", "head": "abc", "branch": "main", "note": "edited"}.Payload())
	time.Sleep(300 * time.Millisecond)
	if kept(fl) {
		t.Fatal("a change to the subject itself was kept to fire again")
	}
	eng.endFlight(ctx, "copy", pr)

	fl = held()
	entitytest.Set(t, store, repo, entity.Obj{"branch": "dev"}.Payload())
	waitKept(fl)
	if got := branchOf(); got != "main" {
		t.Fatalf("branch moved to %q while the subject was still firing", got)
	}
	eng.endFlight(ctx, "copy", pr)
	waitBranch("dev")

	fl = held()
	entitytest.Set(t, store, repo, entity.Obj{"branch": "release"}.Payload())
	waitKept(fl)
	entitytest.Set(t, store, pr, entity.Obj{"stage": "closed", "head": "abc", "branch": "dev"}.Payload())
	eng.endFlight(ctx, "copy", pr)
	time.Sleep(300 * time.Millisecond)
	if got := branchOf(); got != "dev" {
		t.Fatalf("a kept firing fired on a subject the rule no longer selects: branch %q", got)
	}
}
