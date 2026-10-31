package trigger

import (
	"errors"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// unwatchable is a store that refuses to open a watch while `on` — the store under load at a
// restart, where the watch a restored rule asks for hits its deadline.
type unwatchable struct {
	entity.Store
	on bool
}

func (u *unwatchable) WatchPattern(prefix []string, at *kpath.KPath, waitIfAbsent, noInit bool) (<-chan entity.Commit, func(), error) {
	if u.on {
		return nil, nil, errors.New("context deadline exceeded")
	}
	return u.Store.WatchPattern(prefix, at, waitIfAbsent, noInit)
}

// A RESTORED RULE WHOSE WATCH COULD NOT BE OPENED IS PENDING, NOT ABANDONED: it stands in the
// charter by name, status says it is arming, and when the store answers the reconciler arms it —
// and it acts on the backlog it could not see meanwhile. Before this it stood in the charter
// deaf until the next restart, counted as installed by every listing.
func TestARestoredRuleKeepsAskingForItsWatch(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, task.AutoResolver{})
	for _, name := range []string{"review", "other"} {
		if err := eng.AddTrigger(Trigger{
			Name:      name,
			Condition: Condition{Subject: Match{"path": pathOf("demo", name, "*"), "value": map[string]any{"stage": "open"}}},
			Action:    setStage("reviewed"),
		}); err != nil {
			t.Fatalf("AddTrigger %s: %v", name, err)
		}
	}

	store := &unwatchable{Store: entitytest.StoreAt(t, addr), on: true}
	back := New(store, task.AutoResolver{})
	back.every = 5 * time.Millisecond // the backstop, which is what asks again here
	back.ObserveScoped(t.Context())
	if err := back.LoadCharter(); err != nil {
		t.Fatalf("a watch the store did not answer failed the restore: %v", err)
	}
	if got := back.Held(); len(got) != 2 {
		t.Fatalf("pending = %v; the rules whose watch did not open are not in the charter", got)
	}
	if got := back.Triggers(); len(got) != 0 {
		t.Fatalf("armed = %v; a rule whose watch did not open is not armed", got)
	}
	waitArming(t, back, []string{"other", "review"})
	for _, p := range back.Pendings() {
		if !p.Arming {
			t.Fatalf("%s waits on %q, not on its watch", p.Rule, p.Cause)
		}
	}

	// Deaf meanwhile: a subject that matches is not acted on.
	ref := entity.RefAt("demo", "review", "1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	time.Sleep(30 * time.Millisecond)
	if e, _, _ := store.Get(ref); entity.ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("a rule with no watch acted: %v", e.Payload)
	}

	// A removal while pending stops the asking, as it stops the watching.
	if !back.RemoveTrigger("other") {
		t.Fatal("other was not in the charter to remove")
	}
	waitArming(t, back, []string{"review"})

	// The store answers: the rule arms, watches, and the backlog it missed is acted on.
	store.on = false
	waitStage(t, store, ref, "reviewed")
	waitArming(t, back, nil)
	if got := back.Triggers(); len(got) != 1 || got[0].Name != "review" {
		t.Fatalf("armed = %v; want review", got)
	}
}

func waitArming(t *testing.T, e *Engine, want []string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		got := e.Arming()
		if len(got) == len(want) {
			same := true
			for i := range got {
				same = same && got[i] == want[i]
			}
			if same {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("arming = %v, want %v", got, want)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
