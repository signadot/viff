package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
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
// "nothing is installed" about a verse that is merely busy. `verse source` did exactly that on
// a verse holding three sources, which is the one answer somebody acts on — and the two
// listings that already carried this guard (/state, /charter) are why the shape was recognised.
func TestAListingRefusesRatherThanAnsweringEmpty(t *testing.T) {
	fs := &failReads{Store: entitytest.Store(t)}
	srv := NewWithStore(ScriptedBuilder, fs)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, path := range []string{"/sources", "/runners", "/resolvers", "/gates", "/state"} {
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
