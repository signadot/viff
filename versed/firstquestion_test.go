package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
)

// heldRecord holds every write to one task's record until it is let go — the window a loaded
// machine opens by chance, held open on purpose.
type heldRecord struct {
	entity.Store
	ref  entity.Ref
	gate chan struct{}
	once sync.Once
}

func (h *heldRecord) Write(w entity.Write) (bool, int64, error) {
	if w.At.Ref == h.ref {
		<-h.gate
	}
	return h.Store.Write(w)
}

func (h *heldRecord) let() { h.once.Do(func() { close(h.gate) }) }

// A run's first question is OWNED when it is posed, whether or not the run's own record has
// landed.
//
// Both spawn paths start the task and then write its record, so a task that asks straight away
// asks inside that window. The layer rule finds an owner by the question's subject, and a
// subject that had to come off the record would be missing there: no owner, no instance for the
// binding covering the run, and an exclusive binding's question open to whoever answers first.
// Left to load, that one window shows up as three tests failing three different ways — an
// instance never minted, a 404 where a 403 is due, an override that does not name who it
// overrode (git-issue nkptp9b3h12ksx3kg9n0). Held open here, it is there on every run.
func TestARunsFirstQuestionIsOwnedBeforeItsRecordLands(t *testing.T) {
	held := &heldRecord{Store: entitytest.Store(t), ref: mirror.Ref("run-1"), gate: make(chan struct{})}
	srv := NewWithStore(askingBuilder(t), held)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer held.let() // before the server closes: the spawn's handler is parked on the gate
	mustInstall(t, ts.URL, exclusiveBinding("payments-review", "payments"))
	pr(t, srv, "1", "payments")

	// The spawn cannot answer while its record is held, so it is made from beside the test.
	spawned := make(chan string, 1)
	go func() {
		b, _ := codec.Marshal(map[string]any{"id": "run-1", "prompt": "true", "subject": `change.pr."1"`}, codec.Tony)
		resp, err := http.Post(ts.URL+"/tasks", "application/tony", strings.NewReader(string(b)))
		if err != nil {
			spawned <- err.Error()
			return
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			spawned <- resp.Status + ": " + strings.TrimSpace(string(raw))
			return
		}
		spawned <- ""
	}()

	qid := waitQuestion(t, srv, "run-1")
	if _, ok, _ := held.Store.Get(mirror.Ref("run-1")); ok {
		t.Fatal("the run's record landed before its question; the window this is about was not held")
	}
	// Read once, with no waiting: inside the window there is nothing more to wait for.
	q := question(t, srv, qid)
	if q["subject"] != `change.pr."1"` || q["resolver"] != "verse.resolvers.security.payments-review" {
		t.Fatalf("a question posed before its run's record carries no layer: %+v", q)
	}
	if _, ok, err := srv.Store.Get(resolver.InstanceRef("payments-review", "run-1")); err != nil || !ok {
		t.Fatalf("the binding that owns the question has no instance to answer it through (ok=%v, err=%v)", ok, err)
	}
	resp := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: qid, Choice: "proceed", By: "human:scott"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a person answering an exclusive binding's question without override = %s, want 403", resp.Status)
	}

	held.let()
	if why := <-spawned; why != "" {
		t.Fatalf("spawn: %s", why)
	}
}
