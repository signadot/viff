package versed

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/signadot/verse/runner/runnerd/runnerdtest"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// A START WHOSE READS DID NOT HAPPEN IS ASKED AGAIN, and says so meanwhile. The phases of Start
// each list a slice of the store and act on what they find; a listing that hit its deadline
// finds nothing, and a verse that took that as the answer came back with its sources
// registered and none running, and stayed that way until the next restart. Now the phase is in
// `restoring` on status until the store answers, and then what it restores is running.
func TestAStartWhoseReadsFailedIsAskedAgain(t *testing.T) {
	raw := entitytest.Store(t)

	// What the verse held when it went down: a rule and a source, installed the ordinary way.
	first := serverWith(t, runnerdtest.Scripted, raw)
	ts := httptest.NewServer(first.Handler())
	if resp := postTony(t, ts.URL+"/charter", map[string]any{
		"name":      "review",
		"condition": map[string]any{"subject": map[string]any{"path": "demo.pr.*", "value": map[string]any{"stage": "open"}}},
		"action":    map[string]any{"patch": map[string]any{"stage": "reviewed"}},
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install the rule = %s: %s", resp.Status, bodyOf(t, resp))
	}
	if resp := postTony(t, ts.URL+"/iods?probe=false", map[string]any{
		"root": "git", "name": "far", "url": "http://127.0.0.1:1",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install the source = %s: %s", resp.Status, bodyOf(t, resp))
	}
	ts.Close()
	first.stopAllIOs()

	// Back up over a store that does not answer: its reads do not happen, and the charter's
	// watch is refused.
	ph := entitytest.NewPhased(raw)
	ph.Set(entity.WatchRefused, errFor("context deadline exceeded"))
	fs := &failReads{Store: ph, on: true}
	second := serverWith(t, runnerdtest.Scripted, fs)
	second.retryWait = 10 * time.Millisecond
	second.restoreWait = 10 * time.Millisecond
	second.Start(t.Context())
	t.Cleanup(second.stopAllIOs)
	ts = httptest.NewServer(second.Handler())
	defer ts.Close()

	st := statusOf(t, ts)
	for _, phase := range []string{"restored the charter", "restored sources"} {
		if !slices.Contains(st.Restoring, phase) {
			t.Errorf("status.restoring = %v; want %q in it", st.Restoring, phase)
		}
	}
	if n := len(second.Eng.Triggers()); n != 0 {
		t.Fatalf("%d rules restored from a store that did not answer", n)
	}
	second.srcMu.Lock()
	n := len(second.ioRuns)
	second.srcMu.Unlock()
	if n != 0 {
		t.Fatalf("%d sources restored from a store that did not answer", n)
	}

	// The store answers: every phase completes, and what it restores is there.
	fs.on = false
	ph.Set(entity.WatchOpen, nil)
	deadline := time.After(3 * time.Second)
	for len(second.Restoring()) > 0 {
		select {
		case <-deadline:
			t.Fatalf("still restoring %v after the store answered", second.Restoring())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if got := userRules(second.Eng); len(got) != 1 || got[0].Name != "review" {
		t.Fatalf("charter after the store answered = %v", got)
	}
	second.srcMu.Lock()
	n = len(second.ioRuns)
	second.srcMu.Unlock()
	if n != 1 {
		t.Fatalf("%d reflectors after the store answered, want 1", n)
	}
	if st := statusOf(t, ts); len(st.Restoring) != 0 {
		t.Fatalf("status.restoring = %v on a verse that is up", st.Restoring)
	}
	// And the rule is really running, not merely listed.
	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, asTester(second), ref, entity.Obj{"stage": "open"}.Payload()) // the rule's installer's, so the rule may advance it
	deadline = time.After(3 * time.Second)
	for {
		if e, ok, _ := raw.Get(ref); ok && entity.ToObj(e.Payload)["stage"] == "reviewed" {
			return
		}
		select {
		case <-deadline:
			e, _, _ := raw.Get(ref)
			t.Fatalf("the restored rule did not act: %v", e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
