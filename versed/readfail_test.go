package versed

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// failingStore is a store whose reads fail — a dead controller mount, a reset session, a
// timeout. Everything else delegates, so only the read path is under test.
type failingStore struct {
	entity.Store
	err error
}

func (f failingStore) Get(ref entity.Ref) (entity.Entity, bool, error) {
	if f.err != nil {
		return entity.Entity{}, false, f.err
	}
	return f.Store.Get(ref)
}

// A read that could not be performed must NOT be reported as "no such entity".
//
// This is the regression: a `verse connect` mount whose session had been killed made every
// content read fail, and because Store.Get returned only (Entity, bool), verse answered
// 404. An agent asked for a file it was looking at and was told the file did not exist —
// so it went to create it. The two answers lead somewhere different, so the wire has to
// tell them apart.
func TestEntityReadFailureIsNot404(t *testing.T) {
	srv := testServer(t)
	base := srv.Store
	srv.Store = failingStore{Store: base, err: errors.New("read tcp 127.0.0.1:1->127.0.0.1:2: connection reset by peer")}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	q := url.Values{"ref": {"demo:pr:1"}}
	resp, err := http.Get(ts.URL + "/entity?" + q.Encode())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		t.Fatal("a failed read was reported as 404 — the caller is told the entity does not exist")
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (verse is up, its store is not)", resp.StatusCode)
	}
	if !strings.Contains(string(body), "connection reset by peer") {
		t.Errorf("the underlying cause did not reach the caller: %q", body)
	}
}

// The other half: a genuinely absent entity is still a 404. The fix must not turn every
// miss into an error.
func TestAbsentEntityIsStill404(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	q := url.Values{"ref": {"demo:pr:does-not-exist"}}
	resp, err := http.Get(ts.URL + "/entity?" + q.Encode())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for an entity that really is absent", resp.StatusCode)
	}
}

// The content reader an agent's read_file goes through must propagate the failure rather
// than reporting the file as missing.
func TestContentReaderPropagatesReadFailure(t *testing.T) {
	base := entitytest.Store(t)
	wantErr := errors.New("mount is gone")
	c := storeContentReader{
		store: failingStore{Store: base, err: wantErr},
		kind:  "dir-content-scope1",
	}
	body, ok, err := c.ReadContent("harness/llm.go")
	if err == nil {
		t.Fatal("a failed content read reported no error — an agent would be told the file is missing")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error did not carry the cause: %v", err)
	}
	if ok || body != "" {
		t.Errorf("a failed read reported content: ok=%v body=%q", ok, body)
	}

	// And a file that is genuinely absent is still absent, without an error.
	c2 := storeContentReader{store: base, kind: "dir-content-scope1"}
	if _, ok, err := c2.ReadContent("nope.go"); ok || err != nil {
		t.Errorf("absent file: ok=%v err=%v, want (false, nil)", ok, err)
	}
}

// The other half of the 502-vs-404 distinction: an entity that is merely ABSENT — including
// one whose system has never been written — is a 404. It read as a store failure until the
// store learned to tell logd's "no such path" from a real read failure, so asking after
// anything absent on a verse that had ever been written to answered 502.
func TestAbsentEntityIs404NotBadGateway(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	// Write something, so the verse is non-empty and the queried paths are genuinely absent
	// rather than absent-because-nothing-exists.
	if _, err := srv.Store.Write(entity.Write{
		At:    entity.Ref{System: "demo", Kind: "pr", ID: "1"}.At(),
		Value: entity.Obj{"stage": "open"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}

	for _, ref := range []string{"demo:pr:2", "demo:issue:1", "motion:channel:x"} {
		resp, err := http.Get(ts.URL + "/entity?" + url.Values{"ref": {ref}}.Encode())
		if err != nil {
			t.Fatalf("get %s: %v", ref, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET /entity?ref=%s = %d, want 404 for an entity that is simply absent", ref, resp.StatusCode)
		}
	}
}
