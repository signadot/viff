package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// unreadable is a store whose reads do not happen — a store under load, where a read exceeds
// the session budget and List answers nil.
type unreadable struct {
	entity.Store
	on bool
}

func (u *unreadable) List(pred func(entity.Ref) bool) []entity.Entity {
	if u.on {
		return nil
	}
	return u.Store.List(pred)
}
func (u *unreadable) ListAt(at []string, depth int, pred func(entity.Ref) bool) []entity.Entity {
	if u.on {
		return nil
	}
	return u.Store.ListAt(at, depth, pred)
}
func (u *unreadable) ReadError() error {
	if u.on {
		return errRead
	}
	return nil
}

type readErr struct{}

func (readErr) Error() string { return "context deadline exceeded" }

var errRead = readErr{}

// A RESTORE THAT COULD NOT READ SAYS SO, rather than restoring nothing quietly.
//
// List answers nil both when a slice holds nothing and when the read did not happen. Taking the
// second reading brings a verse back running NO RULES — under load, which is exactly when the
// read fails — and nothing anywhere says the program is gone.
func TestARestoreDistinguishesEmptyFromUnread(t *testing.T) {
	store := &unreadable{Store: entitytest.Store(t)}
	eng := New(store, task.AutoResolver{})

	// An empty verse restores cleanly and says nothing is wrong, because nothing is.
	if err := eng.LoadCharter(); err != nil {
		t.Fatalf("an empty charter is not an error: %v", err)
	}
	if err := eng.LoadStrata(); err != nil {
		t.Fatalf("empty strata are not an error: %v", err)
	}

	// A store that could not be read is a different fact, and has to arrive as one.
	store.on = true
	err := eng.LoadCharter()
	if err == nil {
		t.Fatal("an unreadable store restored the charter 'successfully' — a verse would come " +
			"back running no rules with nothing said about it")
	}
	if !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("the failure does not say what happened: %v", err)
	}
	if err := eng.LoadStrata(); err == nil {
		t.Error("an unreadable store restored the strata 'successfully'")
	}
}

// neighboured is a store whose shared read flag says what ANOTHER reader's last read did, as a
// store does while restored rules fire through it, apart from whether this caller's read happened.
type neighboured struct {
	entity.Store
	fail bool  // this caller's listing does not happen
	flag error // the last read anybody else made
}

func (n *neighboured) ListAt(at []string, depth int, pred func(entity.Ref) bool) []entity.Entity {
	out, _ := n.ListAtErr(at, depth, pred)
	return out
}
func (n *neighboured) ListAtErr(at []string, depth int, pred func(entity.Ref) bool) ([]entity.Entity, error) {
	if n.fail {
		return nil, errRead
	}
	return entity.ListAtErr(n.Store, at, depth, pred)
}
func (n *neighboured) ReadError() error { return n.flag }

// A RESTORE ANSWERS FOR ITS OWN READ, not for the store's last one. At startup every restored
// rule's firings read through the same store, so its shared flag is a neighbour's: one that
// failed condemned a load whose read was fine, and the phase was asked again whole; one that
// succeeded cleared a load whose read had failed, and it restored nothing, saying nothing.
func TestARestoreAnswersForItsOwnRead(t *testing.T) {
	addr := entitytest.Addr(t)
	first := New(entitytest.StoreAt(t, addr), task.AutoResolver{})
	if err := first.InstallStratum(Stratum{Name: "drift", Level: 20, Match: []Match{mm("gh")}}); err != nil {
		t.Fatalf("InstallStratum: %v", err)
	}
	if err := first.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*")}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	// A neighbour's failure: this read happened, so the load restores.
	store := &neighboured{Store: entitytest.StoreAt(t, addr), flag: errRead}
	eng := New(store, task.AutoResolver{})
	if err := eng.LoadStrata(); err != nil {
		t.Errorf("strata whose read happened were refused for a neighbour's failure: %v", err)
	}
	if err := eng.LoadCharter(); err != nil {
		t.Errorf("a charter whose read happened was refused for a neighbour's failure: %v", err)
	}
	if len(eng.Strata()) != 1 || len(eng.Triggers()) != 1 {
		t.Errorf("restored %d strata and %d rules, want 1 and 1", len(eng.Strata()), len(eng.Triggers()))
	}

	// A neighbour's success: this read did not happen, and the load says so to be asked again.
	store = &neighboured{Store: entitytest.StoreAt(t, addr), fail: true}
	eng = New(store, task.AutoResolver{})
	if err := eng.LoadStrata(); !entity.IsUnread(err) {
		t.Errorf("strata whose read failed loaded with %v, want a read that did not happen", err)
	}
	if err := eng.LoadCharter(); !entity.IsUnread(err) {
		t.Errorf("a charter whose read failed loaded with %v, want a read that did not happen", err)
	}
}
