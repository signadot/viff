package viffd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// asksAt records the path each catch-up was asked about.
type asksAt struct {
	entity.Store
	mu sync.Mutex
	at [][]string
}

func (a *asksAt) Since(from int64, at ...*kpath.KPath) ([]entity.Commit, error) {
	a.mu.Lock()
	for _, p := range at {
		prefix, _ := entity.LiteralPrefix(p)
		a.at = append(a.at, prefix)
	}
	if len(at) == 0 {
		a.at = append(a.at, nil)
	}
	a.mu.Unlock()
	return a.Store.Since(from, at...)
}

func (a *asksAt) asked() [][]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]string{}, a.at...)
}

// A WATCH CATCHES UP OVER THE SLICE IT WATCHES, not over the whole viff.
//
// Both halves of `/watch` are narrowed by the same path: the live stream opens at the prefix,
// and the catch-up replays there too. It is invisible in the ANSWER — the selector filtered the
// wide replay down to the same events — and it is the whole cost of the request: the replay
// reads the document at its starting commit and every patch in the range that touches the
// watched path, so a one-slice watch used to pay for the size of the store
// (git-issue 3qf2z59dh12ksdazg9n0).
func TestAWatchCatchesUpOverTheSliceItWatches(t *testing.T) {
	srv := testServer(t)
	// Something standing at the slice, because a watch on a path holding nothing is refused
	// before there is a catch-up to make (the same answer a read of it gives).
	entitytest.Set(t, srv.Store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	store := &asksAt{Store: srv.Store}
	srv.Store = store
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	get := func(q url.Values) {
		t.Helper()
		req, _ := http.NewRequest("GET", ts.URL+"/watch?"+q.Encode(), nil)
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		resp, err := http.DefaultClient.Do(req.WithContext(ctx))
		if err != nil {
			return // the stream stays open; the catch-up has already happened
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	}

	get(url.Values{"since": {"0"}, "at": {"demo.pr"}})
	asked := store.asked()
	if len(asked) == 0 {
		t.Fatal("the watch never asked the store to catch up")
	}
	last := asked[len(asked)-1]
	if len(last) != 2 || last[0] != "demo" || last[1] != "pr" {
		t.Errorf("the catch-up was asked at %v, want the watched slice [demo pr]", last)
	}

	// AND AT THE ROOT WHEN THE CALLER NAMES NO PATH, because that is what they asked about.
	get(url.Values{"since": {"0"}})
	asked = store.asked()
	if last := asked[len(asked)-1]; len(last) != 0 {
		t.Errorf("a watch over the whole viff instance caught up at %v, want the root", last)
	}
}
