package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// countingStore wraps a store and counts the reads a handler makes through it.
type countingStore struct {
	entity.Store
	reads int
}

func (c *countingStore) Get(ref entity.Ref) (entity.Entity, bool, error) {
	c.reads++
	return c.Store.Get(ref)
}
func (c *countingStore) List(pred func(entity.Ref) bool) []entity.Entity {
	c.reads++
	return c.Store.List(pred)
}
func (c *countingStore) ListAt(at []string, depth int, pred func(entity.Ref) bool) []entity.Entity {
	c.reads++
	return c.Store.ListAt(at, depth, pred)
}
func (c *countingStore) Rev() int64 { c.reads++; return c.Store.Rev() }

// THE READINESS ENDPOINT MAKES NO STORE READ, at any load.
//
// That is the whole of what it is for. `/status` is a census — it counts gates, runners and
// sources, and Rev syncs — so probing it asked docd for the verse every five seconds, made
// readiness a function of the STORE's load rather than of this process, and left a root watch
// behind per attempt. Staging sat 0/1 for half an hour on a healthy store.
func TestHealthzReadsNothing(t *testing.T) {
	cs := &countingStore{Store: entitytest.Store(t)}
	srv := NewWithStore(ScriptedBuilder, cs)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz = %s: %s", resp.Status, body)
	}
	if !strings.Contains(string(body), "ok") {
		t.Errorf("/healthz body = %q; want it to say ok", body)
	}
	if cs.reads != 0 {
		t.Errorf("/healthz made %d store read(s); it must answer from this process alone", cs.reads)
	}
}

// …and the census still reads, which is what makes it a census. Stated as the other half so a
// later change cannot quietly make /status cheap by making it useless.
func TestStatusStillReadsTheStore(t *testing.T) {
	cs := &countingStore{Store: entitytest.Store(t)}
	srv := NewWithStore(ScriptedBuilder, cs)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	resp.Body.Close()
	if cs.reads == 0 {
		t.Error("/status made no store read; it reports gates, runners and sources")
	}
}
