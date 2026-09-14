package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/task"
)

// idleHandle is a task that is running and does nothing: the engine holds a flow open around
// it until its bus closes, which is what makes "there is live work about this subject" a state
// a test can stand in rather than race against. It counts each control verb it is sent, so a
// test can say WHICH task a steer reached rather than only how many.
type idleHandle struct {
	ch    chan task.Frame
	mu    sync.Mutex
	verbs map[string]int
}

func newIdleHandle(t *testing.T) *idleHandle {
	h := &idleHandle{ch: make(chan task.Frame), verbs: map[string]int{}}
	t.Cleanup(func() { close(h.ch) }) // the reaper exits with the test rather than the process
	return h
}

func (h *idleHandle) got(verb string) error {
	h.mu.Lock()
	h.verbs[verb]++
	h.mu.Unlock()
	return nil
}

// count is how many times the handle was sent verb.
func (h *idleHandle) count(verb string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.verbs[verb]
}

func (h *idleHandle) Subscribe() (<-chan task.Frame, func())           { return h.ch, func() {} }
func (h *idleHandle) SubscribeSince(int64) (<-chan task.Frame, func()) { return h.ch, func() {} }
func (h *idleHandle) Pause() error                                     { return h.got("pause") }
func (h *idleHandle) Resume() error                                    { return h.got("resume") }
func (h *idleHandle) Cancel(string) error                              { return h.got("cancel") }
func (h *idleHandle) Steer(string, string) error                       { return h.got("say") }
func (h *idleHandle) SetVector(*task.Vector, string) error             { return h.got("vector") }
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

// A steer reaches every engine its scope may see, and only those: a scope owns what it creates
// (docs/reference/connect.md#what-a-scope-owns). Baseline's verbs reach baseline's work and a
// package's, summed; a scope's reach the scope's own; and a scope asked to steer work it merely
// sees through the overlay is refused naming whose it is, rather than answering `affected: 0`
// over work that keeps running — or, worse, stopping baseline's work from a scope.
func TestFlowSteeringActsOnItsOwn(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	steer := func(verb, q, subject string) (int, api.Steered, string) {
		t.Helper()
		body := `{subject: "` + subject + `", text: "hurry", by: "user:tester"}`
		resp, err := http.Post(ts.URL+"/flow/"+verb+q, "application/tony", strings.NewReader(body))
		if err != nil {
			t.Fatalf("flow %s: %v", verb, err)
		}
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return resp.StatusCode, api.Steered{}, strings.TrimSpace(string(msg))
		}
		var out api.Steered
		decodeResp(t, resp, &out)
		return resp.StatusCode, out, ""
	}
	verbs := []string{"pause", "resume", "say", "cancel"}

	// A package's task, live, and nothing in baseline: the deployment this is about.
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
	subject := entity.RefAt("notes", "seen", "head").String()
	packaged := newIdleHandle(t)
	pkg.Adopt(subject, "note-the-head-run", packaged)

	// Baseline's steer reaches the package's task: the row `verse flow` lists is steerable.
	for _, verb := range verbs {
		code, got, msg := steer(verb, "", subject)
		if code != 200 || got.Affected != 1 {
			t.Fatalf("flow %s = %d %+v %s, want affected 1 over a package's task", verb, code, got, msg)
		}
		if packaged.count(verb) != 1 {
			t.Fatalf("flow %s reached %d packaged tasks, want the one", verb, packaged.count(verb))
		}
	}

	// Busy in baseline too: the count is of tasks reached, so two.
	base := newIdleHandle(t)
	srv.Eng.Adopt(subject, "baseline-run", base)
	if code, got, msg := steer("pause", "", subject); code != 200 || got.Affected != 2 {
		t.Fatalf("flow pause = %d %+v %s, want 2 over one subject busy in two engines", code, got, msg)
	}
	if base.count("pause") != 1 || packaged.count("pause") != 2 {
		t.Fatalf("pause reached baseline %d, package %d; want 1 and 2", base.count("pause"), packaged.count("pause"))
	}

	// A SCOPE SEES BASELINE'S WORK AND MAY NOT STEER IT. Refused, naming whose it is, and no
	// handle is touched — the bug this pins is `--scope me` cancelling baseline's work.
	for _, verb := range verbs {
		code, got, msg := steer(verb, "?scope=me", subject)
		if code != http.StatusConflict {
			t.Fatalf("flow %s --scope me = %d %+v %s, want 409 over work the scope did not start", verb, code, got, msg)
		}
		if !strings.Contains(msg, "baseline's") || !strings.Contains(msg, "without --scope") {
			t.Fatalf("flow %s --scope me refused with %q, want it to name baseline and say how to re-address", verb, msg)
		}
	}
	if base.count("cancel") != 0 || packaged.count("cancel") != 1 {
		t.Fatalf("a scoped steer reached baseline's task (%d) or the package's (%d) — a scope steered baseline",
			base.count("cancel"), packaged.count("cancel"))
	}

	// The scope's own task, about the same subject: the scope's steer reaches it and it alone.
	mine := newIdleHandle(t)
	srv.scopeEngine("me").Adopt(subject, "me-run", mine)
	if code, got, msg := steer("say", "?scope=me", subject); code != 200 || got.Affected != 1 {
		t.Fatalf("flow say --scope me = %d %+v %s, want 1 over the scope's own task", code, got, msg)
	}
	if mine.count("say") != 1 || base.count("say") != 0 || packaged.count("say") != 1 {
		t.Fatalf("say --scope me reached mine %d, baseline %d, package %d; want 1, 0, 1",
			mine.count("say"), base.count("say"), packaged.count("say"))
	}

	// And baseline may not steer the scope's: once baseline's own work is gone, its steer over
	// the subject is refused naming the scope rather than answering zero.
	close(base.ch)
	close(packaged.ch)
	base.ch, packaged.ch = make(chan task.Frame), make(chan task.Frame) // the cleanups close these
	for i := 0; ; i++ {
		if len(srv.Eng.Flows()) == 0 && len(pkg.Flows()) == 0 {
			break
		}
		if i > 200 {
			t.Fatal("baseline's tasks were not reaped after their buses closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	code, got, msg := steer("cancel", "", subject)
	if code != http.StatusConflict || !strings.Contains(msg, "scope me's") || !strings.Contains(msg, "--scope me") {
		t.Fatalf("flow cancel from baseline = %d %+v %q, want 409 naming scope me", code, got, msg)
	}
	if mine.count("cancel") != 0 {
		t.Fatal("baseline's steer cancelled the scope's task")
	}

	// A scope nobody has opened, over a subject nobody is working: a normal zero.
	if code, got, msg := steer("pause", "?scope=nobody", "demo.pr.99"); code != 200 || got.Affected != 0 {
		t.Fatalf("flow pause --scope nobody = %d %+v %s, want a plain 0", code, got, msg)
	}
}
