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
