package versed

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// task run (POST /tasks) goes through the runner, not straight to the local
// runtime: under a broker runner the ad-hoc run lands on the broker, and it is mirrored as a
// task:instance entity like any other task.
func TestTaskRunHonorsRunner(t *testing.T) {
	srv := testServer(t)
	srv.UseBroker("http://verse.test:9090")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Post(ts.URL+"/tasks", "application/json",
		strings.NewReader(`{"prompt":"ad-hoc run","driver":"human"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var sp struct {
		ID string `tony:"field=id"`
	}
	decodeResp(t, resp, &sp)
	resp.Body.Close()
	if sp.ID == "" {
		t.Fatal("no id returned from task run")
	}

	// honored the runner: the run is on the broker, not the local runtime.
	lr, _ := ts.Client().Get(ts.URL + "/tasks")
	var list []map[string]any
	decodeResp(t, lr, &list)
	lr.Body.Close()
	var pol string
	for _, m := range list {
		if m["id"] == sp.ID {
			pol, _ = m["runner"].(string)
		}
	}
	if pol != "broker" {
		t.Fatalf("runner=%q, want broker (task run should honor the runner)", pol)
	}

	// mirrored: it is a task:instance entity like any other task.
	if _, ok, _ := srv.Store.Get(entity.RefAt("task", "instance", sp.ID)); !ok {
		t.Fatal("task run was not mirrored as a task:instance entity")
	}
}

// attach bridges across backends: a brokered task's events are streamed as SSE by
// versed subscribing the broker handle (which replays the merged log then goes live).
func TestBrokeredAttachStreams(t *testing.T) {
	srv := testServer(t)
	b := srv.UseBroker("http://verse.test:9090")
	h := b.Spawn(task.Launch{ID: "bp1", Prompt: "t", Driver: "human"}, nil)
	h.Steer("hello there", "op") // an EvSay lands on the merged log before attach
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/tasks/bp1/events")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type=%q, want text/event-stream", ct)
	}

	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				lines <- d
			}
		}
		close(lines)
	}()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case d, ok := <-lines:
			if !ok {
				t.Fatal("stream ended without the replayed say")
			}
			if strings.Contains(d, "hello there") {
				return // the merged-log event reached the attach stream
			}
		case <-deadline:
			t.Fatal("timed out waiting for the say on the brokered attach stream")
		}
	}
}

// /status describes the runner along its two legs — `resume` (verse-facing re-attach)
// and `dial` (worker-facing) — so a worker can discover where to dial in and the two are
// not confused. For a broker: kind=broker, resume=ephemeral, dial=the hosted face.
func TestStatusShowsBrokerRunner(t *testing.T) {
	srv := testServer(t)
	srv.UseBroker("http://verse.test:9090")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	defer resp.Body.Close()
	var st struct {
		Runner struct {
			Kind   string `tony:"field=kind"`
			Resume string `tony:"field=resume"`
			Dial   string `tony:"field=dial"`
		} `tony:"field=runner"`
	}
	decodeResp(t, resp, &st)
	if st.Runner.Kind != "broker" || st.Runner.Resume != "ephemeral" {
		t.Fatalf("runner = %+v, want kind=broker resume=ephemeral", st.Runner)
	}
	if !strings.HasPrefix(st.Runner.Dial, "http://verse.test:9090/broker/") {
		t.Fatalf("runner.dial=%q, want the hosted worker face", st.Runner.Dial)
	}
}

// The default local runner reports kind=local, ephemeral, no worker dial leg.
func TestStatusShowsLocalRunner(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	defer resp.Body.Close()
	var st struct {
		Runner struct {
			Kind   string  `tony:"field=kind"`
			Resume string  `tony:"field=resume"`
			Dial   *string `tony:"field=dial"`
		} `tony:"field=runner"`
	}
	decodeResp(t, resp, &st)
	if st.Runner.Kind != "local" || st.Runner.Resume != "ephemeral" || st.Runner.Dial != nil {
		t.Fatalf("runner = %+v, want kind=local resume=ephemeral dial=null", st.Runner)
	}
}

// task list unions the local runtime with the active runner's own tasks: under a
// broker runner, a brokered task appears with runner=broker and its dialURL, and get
// resolves it across backends. This is how a broker URL is found via `task list`/`get`.
func TestTaskListUnionsBrokered(t *testing.T) {
	srv := testServer(t)
	srv.UseBroker("http://verse.test:9090")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Spawn through the API (not the broker directly) so it mirrors a task:instance
	// entity, as production does — the unified list filters by scope membership via that
	// entity, so a task with no entity would (correctly) not appear.
	sp, err := ts.Client().Post(ts.URL+"/tasks", "application/json",
		strings.NewReader(`{"id":"bp1","prompt":"brokered review","driver":"human"}`))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	sp.Body.Close()

	// list: the brokered task shows up, tagged and carrying its dial base.
	resp, err := ts.Client().Get(ts.URL + "/tasks")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer resp.Body.Close()
	var list []map[string]any
	decodeResp(t, resp, &list)
	var found map[string]any
	for _, m := range list {
		if m["id"] == "bp1" {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("brokered task not in unified list: %+v", list)
	}
	if found["runner"] != "broker" {
		t.Fatalf("runner=%v, want broker", found["runner"])
	}
	if d, _ := found["dialURL"].(string); !strings.HasPrefix(d, "http://verse.test:9090/broker/") {
		t.Fatalf("dialURL=%v, want the broker face", found["dialURL"])
	}

	// get: resolves the brokered id across backends.
	gr, err := ts.Client().Get(ts.URL + "/tasks/bp1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer gr.Body.Close()
	if gr.StatusCode != http.StatusOK {
		t.Fatalf("get brokered = %d, want 200", gr.StatusCode)
	}
	var got map[string]any
	decodeResp(t, gr, &got)
	if got["runner"] != "broker" || got["prompt"] != "brokered review" {
		t.Fatalf("get = %+v, want the brokered task tagged broker", got)
	}
}

// task inbox is an operator peek at the control queued for a brokered task's worker
// (unfenced), and cancel/say route to the runner handle so they work for brokered tasks.
func TestTaskInboxAndControlRouting(t *testing.T) {
	srv := testServer(t)
	b := srv.UseBroker("http://verse.test:9090")
	b.Spawn(task.Launch{ID: "bp1", Prompt: "brokered task", Driver: "human"}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	cl := ts.Client()

	// say routes to the broker: it enqueues a control item for the worker.
	resp, err := cl.Post(ts.URL+"/tasks/bp1/say", "application/json",
		strings.NewReader(`{"instruction":"focus on auth","by":"op"}`))
	if err != nil {
		t.Fatalf("say: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("say routing = %d, want 204", resp.StatusCode)
	}

	// inbox shows it, unfenced.
	ir, err := cl.Get(ts.URL + "/tasks/bp1/inbox")
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	defer ir.Body.Close()
	// A bare list, not {items: [...]}: the operator inbox answers like every other
	// collection this server serves.
	var items []api.InboxItem
	decodeResp(t, ir, &items)
	var sawSay bool
	for _, it := range items {
		if it.Kind == "say" && it.Text == "focus on auth" && it.By == "op" {
			sawSay = true
		}
	}
	if !sawSay {
		t.Fatalf("inbox missing the routed say: %+v", items)
	}

	// cancel routes to the broker too: the task latches terminal (verse-authoritative).
	cr, err := cl.Post(ts.URL+"/tasks/bp1/cancel", "application/json", nil)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cr.Body.Close()
	if cr.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel routing = %d, want 204", cr.StatusCode)
	}
	gr, _ := cl.Get(ts.URL + "/tasks/bp1")
	var snap struct {
		Status string `tony:"field=status"`
	}
	decodeResp(t, gr, &snap)
	gr.Body.Close()
	if snap.Status != "cancelled" {
		t.Fatalf("after cancel status=%q, want cancelled", snap.Status)
	}
}

// UseBroker installs a dial-in broker as the runner and composes its published dial base
// from the given verse-reachable face + the minted nonce, so a spawned task advertises
// where a worker dials in.
func TestUseBrokerPublishesDialBase(t *testing.T) {
	srv := testServer(t)
	b := srv.UseBroker("http://verse.test:9090/")

	h := b.Spawn(task.Launch{ID: "x", Prompt: "task", Driver: "human"}, nil)
	u := h.(interface{ DialURL() string }).DialURL()
	if !strings.HasPrefix(u, "http://verse.test:9090/broker/") {
		t.Fatalf("dial base = %q, want <face>/broker/<nonce>", u)
	}
	if strings.HasSuffix(u, "/broker/") {
		t.Fatalf("dial base %q has an empty nonce", u)
	}
}

// A worker dials in through verse's own broker face: HostBroker mounts a broker's
// worker handler under /broker/<nonce>, and a claim routed through BrokerHandler reaches
// it with the prefix stripped — proving verse can host a dial-in runner under its URL
// without exposing a separate service.
func TestHostBrokerRoutesWorkerDialIn(t *testing.T) {
	srv := testServer(t)

	b := broker.New()
	b.Spawn(task.Launch{ID: "p1", Prompt: "review PR #7", Driver: "human"}, nil) // verse side registers the task
	nonce := srv.HostBroker("", b.WorkerHandler())

	ts := httptest.NewServer(srv.BrokerHandler())
	defer ts.Close()

	// claim, routed through /broker/<nonce>/tasks/p1/claim.
	resp, err := ts.Client().Post(
		ts.URL+"/broker/"+nonce+"/tasks/p1/claim", "application/json",
		strings.NewReader(`{"identity":"human:scott"}`))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("claim through broker mount = %d, want 200", resp.StatusCode)
	}
	var claim struct {
		Spec struct {
			Prompt string `tony:"field=prompt"`
		} `tony:"field=spec"`
	}
	decodeResp(t, resp, &claim)
	if claim.Spec.Prompt != "review PR #7" {
		t.Fatalf("claim spec.task=%q, want the spawn task through the mount", claim.Spec.Prompt)
	}

	// an unregistered nonce is not found.
	bad, err := ts.Client().Post(
		ts.URL+"/broker/deadbeef/tasks/p1/claim", "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("bad-nonce claim: %v", err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusNotFound {
		t.Fatalf("unregistered nonce = %d, want 404", bad.StatusCode)
	}
}

// A runner that did not answer must not read as a runner with nothing running.
//
// It first read that way silently — the union dropped its rows and answered a clean, short 200
// (git-issue 6mnmxj6mh12krz3ae1n0). Then it failed the whole list, which was right while the
// LIST depended on the runner. It no longer does: per-task runners made the rows come from
// the task:instance mirror, which is written at spawn and complete without asking anybody,
// so an unreachable runner now costs live DETAIL and not rows. This pins the property all three
// were reaching for — the answer never quietly gets shorter.
func TestTaskListSurvivesAnUnreachableRunner(t *testing.T) {
	srv := testServer(t)
	// A runner url that is listening at bind time and gone by the call — the torn-down
	// runner, which with per-task runners is an ordinary state rather than an exotic one.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	srv.SetRunner(runner.HTTP(deadURL))

	// Work that IS on it, recorded the way a spawn records it.
	if _, _, err := srv.Store.Write(entity.Write{
		At: mirror.Ref("stranded").At(),
		Value: entity.Obj{
			"status": "running", "prompt": "on the dead runner", "runner": deadURL,
		}.Payload(),
	}); err != nil {
		t.Fatalf("writing the task:instance: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/tasks")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("list = %s: %s — the rows do not depend on the runner answering", resp.Status, body)
	}
	var list []map[string]any
	decodeResp(t, resp, &list)
	var found map[string]any
	for _, m := range list {
		if m["id"] == "stranded" {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("list = %+v, want the task on the unreachable runner — a verse with less "+
			"work in it than it has is the failure this whole line of work is about", list)
	}
	if found["runner"] != deadURL {
		t.Fatalf("runner = %v, want the url it is on", found["runner"])
	}
	if found["status"] != "running" {
		t.Fatalf("status = %v, want the record's own account when the runtime cannot be asked",
			found["status"])
	}
}

// `task get` on an unreachable runner is not a 404. TaskSnapshot always read its status,
// so no refusal passed for an answer — but unreachable, refused and never-heard-of all
// collapsed into one false, and the route could only render that as not-found. So an operator
// was sent to look for a typo in an id that was never wrong, for a task that is running
// somewhere they cannot currently see (git-issue 6mnmxj6mh12krz3ae1n0).
func TestTaskGetSeparatesUnreachableFromUnknown(t *testing.T) {
	srv := testServer(t)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	srv.SetRunner(runner.HTTP(deadURL))

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/tasks/whatever")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatal("an unreachable runner answered 404: the id reads as wrong when the runner is")
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %s, want 502", resp.Status)
	}

	// And the id that IS wrong still 404s, or this would have traded one lie for another: a
	// live runner saying "never heard of it" is exactly the case 404 is for.
	live := testServer(t)
	lts := httptest.NewServer(live.Handler())
	defer lts.Close()
	miss, err := lts.Client().Get(lts.URL + "/tasks/nope")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer miss.Body.Close()
	if miss.StatusCode != http.StatusNotFound {
		t.Fatalf("an unknown id on a reachable verse = %s, want 404", miss.Status)
	}
}
