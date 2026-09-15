package trigger

import (
	"sync"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// stamps keeps the principal each address was last written as, so a test can read what
// reached the substrate — the log does not hold a writer yet.
type stamps struct {
	entity.Store
	mu   sync.Mutex
	last map[entity.Ref]string
}

func (s *stamps) Write(w entity.Write) (bool, int64, error) {
	s.mu.Lock()
	if s.last == nil {
		s.last = map[entity.Ref]string{}
	}
	s.last[w.At.Ref] = w.Principal
	s.mu.Unlock()
	return s.Store.Write(w)
}

func (s *stamps) by(ref entity.Ref) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last[ref]
}

// A firing writes as the rule that fired: via the address of its record, whatever the chain has
// descended to beneath it. Nobody typed a firing, so no person's name goes on its writes.
func TestAFiringWritesAsItsRule(t *testing.T) {
	st := &stamps{Store: entitytest.Store(t)}
	eng := New(st, task.AutoResolver{})

	want := entity.Via(CharterRefAt("", "review"))
	if got := eng.Principal("review.onDone.onFailed"); got != want {
		t.Fatalf("Principal(review.onDone.onFailed) = %q, want %q", got, want)
	}
	pr := entity.RefAt("demo", "pr", "1")
	if _, _, err := eng.caused("review.onDone", 0).Write(entity.Write{
		At: pr.At(), Value: entity.Obj{"stage": "reviewed"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := st.by(pr); got != want {
		t.Fatalf("a firing's write reached the store as %q, want the rule %q", got, want)
	}

	// A packaged rule is spelled with its package, the way its record is.
	pkg := New(st, task.AutoResolver{})
	sub, err := ParseAt("drift")
	if err != nil {
		t.Fatal(err)
	}
	pkg.Confine(sub)
	if got, want := pkg.Principal("head-moved"), entity.Via(CharterRefAt(sub.Stamp(), "head-moved")); got != want {
		t.Fatalf("a packaged rule's principal = %q, want %q", got, want)
	}
}

// An install is by somebody, and the rule's record is written as them — which is what makes
// the charter act as its installer: a firing's address resolves to the record's owner.
func TestAnInstallWritesTheRuleAsItsInstaller(t *testing.T) {
	st := &stamps{Store: entitytest.Store(t)}
	eng := New(st, task.AutoResolver{})
	tr := Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}
	if err := eng.InstallTriggerAs("scott@example.com", tr); err != nil {
		t.Fatal(err)
	}
	if got := st.by(CharterRefAt("", "review")); got != "scott@example.com" {
		t.Fatalf("the rule record was written as %q, want the installer", got)
	}
	// Restored at a start, the record is re-written by nobody in particular: the store's own
	// default, which is what an empty principal leaves to it.
	eng2 := New(st, task.AutoResolver{})
	if err := eng2.LoadCharter(); err != nil {
		t.Fatal(err)
	}
	if err := eng2.InstallTrigger(tr); err != nil {
		t.Fatal(err)
	}
	if got := st.by(CharterRefAt("", "review")); got != "" {
		t.Fatalf("an install with no principal wrote the record as %q", got)
	}
}
