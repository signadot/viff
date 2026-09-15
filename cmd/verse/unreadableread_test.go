package main

import (
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/versed"
)

// flapping is a store whose reads FAIL, beside a reader whose reads SUCCEED — the ordinary state
// of a busy verse, where the engine, the sources and this request all read the same store.
//
// It models what a real `LogdStore` does on both faces: a listing that answers a slice and
// records its failure on the store, and one that answers its OWN error. `ReadError` is one field
// for every reader, and "the next read that succeeds clears it", so whether a request's own
// failure is still there when the handler asks depends on who else read in between.
type flapping struct {
	entity.Store
	mu        sync.Mutex
	readErr   error
	neighbour bool // a healthy read lands between this request's read and the guard
}

func (f *flapping) note(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readErr = err
}

func (f *flapping) ReadError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readErr
}

func (f *flapping) read() ([]entity.Entity, error) {
	err := errors.New("context deadline exceeded")
	f.note(err) // this read did not happen…
	if f.neighbour {
		f.note(nil) // …and somebody else's did, before the handler looked
	}
	return nil, err
}

func (f *flapping) List(func(entity.Ref) bool) []entity.Entity {
	out, _ := f.read()
	return out
}

func (f *flapping) ListAt([]string, int, func(entity.Ref) bool) []entity.Entity {
	out, _ := f.read()
	return out
}

func (f *flapping) ReadAt([]string) (*ir.Node, bool, error) {
	_, err := f.read()
	return nil, false, err
}

func (f *flapping) ListErr(func(entity.Ref) bool) ([]entity.Entity, error) { return f.read() }

func (f *flapping) ListAtErr([]string, int, func(entity.Ref) bool) ([]entity.Entity, error) {
	return f.read()
}

// A READ REPORTS ITS OWN FAILURE, so a request that could not read the store says so however
// busy its neighbours are.
//
// The guard used to ask the STORE for the last error it remembered, which is a different
// question: a neighbour's success hid this request's failure and the handler answered 200 with
// an empty document, and a neighbour's failure would have refused a request whose read was fine.
// It surfaced as `--paths` disagreeing with the document form (git-issue tyr7f6dhh12ksdd4gnn0) —
// the two were never asked at the same moment, and the window between the read and the guard is
// microseconds.
func TestAFailedReadIsRefusedHoweverBusyTheNeighbours(t *testing.T) {
	st := &flapping{Store: entitytest.Store(t)}
	srv := versed.NewWithStore(versed.ScriptedBuilder, st)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	// The read fails every time below. What changes is only whether somebody else's read landed
	// in between — and that is what the answer must NOT depend on.
	for _, neighbour := range []bool{false, true} {
		st.neighbour = neighbour
		for _, args := range [][]string{
			{"data", "git"},
			{"data", "git", "--paths"},
		} {
			out, err := runCLI(t, args...)
			t.Logf("verse %-22s neighbour=%-5v → out=%q err=%v",
				strings.Join(args, " "), neighbour, out, err)
			if err == nil {
				t.Errorf("verse %s (neighbour=%v) answered %q for a read that did not happen",
					strings.Join(args, " "), neighbour, out)
			}
		}
	}
}
