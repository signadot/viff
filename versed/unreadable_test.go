package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/trigger"
)

// failReads is a store whose reads do not happen — the shape of a store under load, where a
// read exceeds the session budget and List answers nil.
type failReads struct {
	entity.Store
	on bool
}

func (f *failReads) List(pred func(entity.Ref) bool) []entity.Entity {
	if f.on {
		return nil
	}
	return f.Store.List(pred)
}
func (f *failReads) ListAt(at []string, depth int, pred func(entity.Ref) bool) []entity.Entity {
	if f.on {
		return nil
	}
	return f.Store.ListAt(at, depth, pred)
}
func (f *failReads) ReadAt(at []string) (*ir.Node, bool, error) {
	if f.on {
		return nil, false, errFor("context deadline exceeded")
	}
	return f.Store.ReadAt(at)
}
func (f *failReads) ReadError() error {
	if f.on {
		return errFor("context deadline exceeded")
	}
	return nil
}

// A FAILED READ IS NOT AN EMPTY LIST, on every listing an operator reads state from.
//
// List answers nil when the read did not happen, so a listing that renders it verbatim says
// "nothing is installed" about a verse that is merely busy. `verse iod` did exactly that on
// a verse holding three sources, which is the one answer somebody acts on — and the two
// listings that already carried this guard (/state, /charter) are why the shape was recognised.
func TestAListingRefusesRatherThanAnsweringEmpty(t *testing.T) {
	fs := &failReads{Store: entitytest.Store(t)}
	srv := NewWithStore(ScriptedBuilder, fs)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, path := range []string{"/iods", "/runners", "/resolvers", "/asks", "/asks?all=true", "/state"} {
		fs.on = false
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s on a healthy store = %s", path, resp.Status)
		}

		fs.on = true
		resp, err = http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s with an unreadable store = %s (%s); want 503 — an empty answer here "+
				"reads as 'nothing is installed'", path, resp.Status, body)
		}
	}
}

// charterUnread is a store whose listing of the charter does not happen while every other read
// does, so its shared flag, set by whoever read last, says all is well.
type charterUnread struct{ entity.Store }

func (c charterUnread) ListAt(at []string, depth int, pred func(entity.Ref) bool) []entity.Entity {
	out, _ := c.ListAtErr(at, depth, pred)
	return out
}
func (c charterUnread) ListAtErr(at []string, depth int, pred func(entity.Ref) bool) ([]entity.Entity, error) {
	if slices.Equal(at, []string{entity.VerseRoot, trigger.CharterSystem}) {
		return nil, errFor("context deadline exceeded")
	}
	return entity.ListAtErr(c.Store, at, depth, pred)
}
func (charterUnread) ReadError() error { return nil }

// WHICH PACKAGES EXIST IS TWO READS, and each answers for itself: one check of the store's flag
// after both let the schedules' listing clear the charter's failure, and a restore found no
// packages to bring back.
func TestPackagesSeenAnswersForEachRead(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, charterUnread{entitytest.Store(t)})
	if names, err := srv.packagesSeenErr(); !entity.IsUnread(err) {
		t.Fatalf("packages read as %v with %v while the charter could not be read, want a read that did not happen", names, err)
	}
}
