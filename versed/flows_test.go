package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/task"
)

// idleHandle is a task that is running and does nothing: the engine holds a flow open around
// it until its bus closes, which is what makes "there is live work about this subject" a state
// a test can stand in rather than race against.
type idleHandle struct{ ch chan task.Frame }

func newIdleHandle(t *testing.T) *idleHandle {
	h := &idleHandle{ch: make(chan task.Frame)}
	t.Cleanup(func() { close(h.ch) }) // the reaper exits with the test rather than the process
	return h
}

func (h *idleHandle) Subscribe() (<-chan task.Frame, func())           { return h.ch, func() {} }
func (h *idleHandle) SubscribeSince(int64) (<-chan task.Frame, func()) { return h.ch, func() {} }
func (h *idleHandle) Pause() error                                     { return nil }
func (h *idleHandle) Resume() error                                    { return nil }
func (h *idleHandle) Cancel(string) error                              { return nil }
func (h *idleHandle) Steer(string, string) error                       { return nil }
func (h *idleHandle) SetVector(*task.Vector, string) error             { return nil }
func (h *idleHandle) Result() string                                   { return "" }
func (h *idleHandle) Err() string                                      { return "" }

// A flow is EVERY ENGINE's, on both surfaces: a live task is tracked by the engine that
// spawned it and a package's charter runs in one of its own, so baseline's registry alone is
// blind to the work a packaged rule started — `verse flow` listed nothing and `status` said
// `flows: 0` while it ran. Staging is that verse: its whole charter sits under `verse-dev`.
func TestFlowsAreEveryEnginesOnBothSurfaces(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	flows := func(q string) []string {
		t.Helper()
		resp, err := http.Get(ts.URL + "/flows" + q)
		if err != nil {
			t.Fatalf("flows: %v", err)
		}
		var out []string
		decodeResp(t, resp, &out)
		return out
	}
	statusFlows := func(q string) int64 {
		t.Helper()
		resp, err := http.Get(ts.URL + "/status" + q)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		var m map[string]any
		decodeResp(t, resp, &m)
		n, ok := codec.AsInt64(m["flows"])
		if !ok {
			t.Fatalf("flows is not a number: %v", m["flows"])
		}
		return n
	}

	if got := flows(""); len(got) != 0 {
		t.Fatalf("flows = %v, want none on a verse with no live work", got)
	}

	// A package, and nothing in baseline: the deployment this is about. Installing the charter
	// is what puts its rules in an engine of their own, which is the engine this is about.
	if code, msg := declareReads(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring imports = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {path: [git, head, *]}, subject: {path: [notes, seen, head]}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing a package = %d: %s", code, msg)
	}
	pkg := srv.packageEngineIfRunning("notes")
	if pkg == nil {
		t.Fatal("the package installed and is running in no engine")
	}

	// A task of that package's, live — adopted rather than spawned, so the flow is open for
	// as long as the assertions take rather than for as long as a shell command runs.
	subject := entity.RefAt("notes", "seen", "head").String()
	pkg.Adopt(subject, "note-the-head-run", newIdleHandle(t))

	if n := len(srv.Eng.Flows()); n != 0 {
		t.Fatalf("baseline holds %d flows over work it did not start", n)
	}
	if got := flows(""); len(got) != 1 || got[0] != subject {
		t.Fatalf("flows = %v, want [%s] — a package's live work is this verse's", got, subject)
	}
	if n := statusFlows(""); n != 1 {
		t.Fatalf("status flows = %d over a list of 1", n)
	}

	// ONE SUBJECT, ONE ROW. A subject is the question "what is happening to this entity", and
	// two engines with work about the same one are one busy subject.
	srv.Eng.Adopt(subject, "baseline-run", newIdleHandle(t))
	if got := flows(""); len(got) != 1 {
		t.Fatalf("flows = %v, want one row for one subject busy in two engines", got)
	}
	if n := statusFlows(""); n != int64(len(flows(""))) {
		t.Fatalf("status flows = %d but the list has %d", n, len(flows("")))
	}

	// A SCOPE ANSWERS FOR ITSELF: a federation nobody has opened has no live work, and says so
	// rather than reporting baseline's.
	if got := flows("?scope=nobody"); len(got) != 0 {
		t.Fatalf("scope flows = %v, want none — a scope is not baseline", got)
	}
	if n := statusFlows("?scope=nobody"); n != 0 {
		t.Fatalf("status flows = %d in a scope with no live work", n)
	}
}
