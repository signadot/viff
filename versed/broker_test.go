package versed

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/entity"
)

// process run (POST /processes) goes through the controlPolicy, not straight to the local
// runtime: under a broker policy the ad-hoc run lands on the broker, and it is mirrored as a
// process:instance entity like any other process.
func TestProcessRunHonorsPolicy(t *testing.T) {
	srv := New(ScriptedBuilder)
	srv.UseBroker("http://verse.test:9090")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Post(ts.URL+"/processes", "application/json",
		strings.NewReader(`{"task":"ad-hoc run","driver":"human"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var sp struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&sp)
	resp.Body.Close()
	if sp.ID == "" {
		t.Fatal("no id returned from process run")
	}

	// honored the policy: the run is on the broker, not the local runtime.
	lr, _ := ts.Client().Get(ts.URL + "/processes")
	var list []map[string]any
	_ = json.NewDecoder(lr.Body).Decode(&list)
	lr.Body.Close()
	var pol string
	for _, m := range list {
		if m["id"] == sp.ID {
			pol, _ = m["policy"].(string)
		}
	}
	if pol != "broker" {
		t.Fatalf("run policy=%q, want broker (process run should honor the policy)", pol)
	}

	// mirrored: it is a process:instance entity like any other process.
	if _, ok, _ := srv.Store.Get(entity.Ref{System: "process", Kind: "instance", ID: sp.ID}); !ok {
		t.Fatal("process run was not mirrored as a process:instance entity")
	}
}

// attach bridges across backends: a brokered process's events are streamed as SSE by
// versed subscribing the broker handle (which replays the merged log then goes live).
func TestBrokeredAttachStreams(t *testing.T) {
	srv := New(ScriptedBuilder)
	b := srv.UseBroker("http://verse.test:9090")
	h := b.Spawn("bp1", "t", "human", nil)
	h.Steer("hello there", "op") // an EvSay lands on the merged log before attach
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/processes/bp1/events")
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

// /status describes the run policy along its two legs — `resume` (verse-facing re-attach)
// and `dial` (worker-facing) — so a worker can discover where to dial in and the two are
// not confused. For a broker: kind=broker, resume=ephemeral, dial=the hosted face.
func TestStatusShowsBrokerPolicy(t *testing.T) {
	srv := New(ScriptedBuilder)
	srv.UseBroker("http://verse.test:9090")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	defer resp.Body.Close()
	var st struct {
		Policy struct{ Kind, Resume, Dial string }
	}
	_ = json.NewDecoder(resp.Body).Decode(&st)
	if st.Policy.Kind != "broker" || st.Policy.Resume != "ephemeral" {
		t.Fatalf("policy = %+v, want kind=broker resume=ephemeral", st.Policy)
	}
	if !strings.HasPrefix(st.Policy.Dial, "http://verse.test:9090/broker/") {
		t.Fatalf("policy.dial=%q, want the hosted worker face", st.Policy.Dial)
	}
}

// The default local policy reports kind=local, ephemeral, no worker dial leg.
func TestStatusShowsLocalPolicy(t *testing.T) {
	srv := New(ScriptedBuilder)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	defer resp.Body.Close()
	var st struct {
		Policy struct {
			Kind, Resume string
			Dial         *string
		}
	}
	_ = json.NewDecoder(resp.Body).Decode(&st)
	if st.Policy.Kind != "local" || st.Policy.Resume != "ephemeral" || st.Policy.Dial != nil {
		t.Fatalf("policy = %+v, want kind=local resume=ephemeral dial=null", st.Policy)
	}
}

// process list unions the local runtime with the active policy's own processes: under a
// broker policy, a brokered process appears with policy=broker and its dialURL, and get
// resolves it across backends. This is how a broker URL is found via `process list`/`get`.
func TestProcessListUnionsBrokered(t *testing.T) {
	srv := New(ScriptedBuilder)
	srv.UseBroker("http://verse.test:9090")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Spawn through the API (not the broker directly) so it mirrors a process:instance
	// entity, as production does — the unified list filters by scope membership via that
	// entity, so a process with no entity would (correctly) not appear.
	sp, err := ts.Client().Post(ts.URL+"/processes", "application/json",
		strings.NewReader(`{"id":"bp1","task":"brokered review","driver":"human"}`))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	sp.Body.Close()

	// list: the brokered process shows up, tagged and carrying its dial base.
	resp, err := ts.Client().Get(ts.URL + "/processes")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer resp.Body.Close()
	var list []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&list)
	var found map[string]any
	for _, m := range list {
		if m["id"] == "bp1" {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("brokered process not in unified list: %+v", list)
	}
	if found["policy"] != "broker" {
		t.Fatalf("policy=%v, want broker", found["policy"])
	}
	if d, _ := found["dialURL"].(string); !strings.HasPrefix(d, "http://verse.test:9090/broker/") {
		t.Fatalf("dialURL=%v, want the broker face", found["dialURL"])
	}

	// get: resolves the brokered id across backends.
	gr, err := ts.Client().Get(ts.URL + "/processes/bp1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer gr.Body.Close()
	if gr.StatusCode != http.StatusOK {
		t.Fatalf("get brokered = %d, want 200", gr.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(gr.Body).Decode(&got)
	if got["policy"] != "broker" || got["task"] != "brokered review" {
		t.Fatalf("get = %+v, want the brokered process tagged broker", got)
	}
}

// process inbox is an operator peek at the control queued for a brokered process's worker
// (unfenced), and cancel/say route to the policy handle so they work for brokered processes.
func TestProcessInboxAndControlRouting(t *testing.T) {
	srv := New(ScriptedBuilder)
	b := srv.UseBroker("http://verse.test:9090")
	b.Spawn("bp1", "brokered task", "human", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	cl := ts.Client()

	// say routes to the broker: it enqueues a control item for the worker.
	resp, err := cl.Post(ts.URL+"/processes/bp1/say", "application/json",
		strings.NewReader(`{"instruction":"focus on auth","by":"op"}`))
	if err != nil {
		t.Fatalf("say: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("say routing = %d, want 204", resp.StatusCode)
	}

	// inbox shows it, unfenced.
	ir, err := cl.Get(ts.URL + "/processes/bp1/inbox")
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	defer ir.Body.Close()
	var box struct {
		Items []struct{ Kind, Text, By string }
	}
	_ = json.NewDecoder(ir.Body).Decode(&box)
	var sawSay bool
	for _, it := range box.Items {
		if it.Kind == "say" && it.Text == "focus on auth" && it.By == "op" {
			sawSay = true
		}
	}
	if !sawSay {
		t.Fatalf("inbox missing the routed say: %+v", box.Items)
	}

	// cancel routes to the broker too: the process latches terminal (verse-authoritative).
	cr, err := cl.Post(ts.URL+"/processes/bp1/cancel", "application/json", nil)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cr.Body.Close()
	if cr.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel routing = %d, want 204", cr.StatusCode)
	}
	gr, _ := cl.Get(ts.URL + "/processes/bp1")
	var snap struct{ Status string }
	_ = json.NewDecoder(gr.Body).Decode(&snap)
	gr.Body.Close()
	if snap.Status != "cancelled" {
		t.Fatalf("after cancel status=%q, want cancelled", snap.Status)
	}
}

// UseBroker installs a dial-in broker as the policy and composes its published dial base
// from the given verse-reachable face + the minted nonce, so a spawned process advertises
// where a worker dials in.
func TestUseBrokerPublishesDialBase(t *testing.T) {
	srv := New(ScriptedBuilder)
	b := srv.UseBroker("http://verse.test:9090/")

	h := b.Spawn("x", "task", "human", nil)
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
// it with the prefix stripped — proving verse can host a dial-in policy under its URL
// without exposing a separate service.
func TestHostBrokerRoutesWorkerDialIn(t *testing.T) {
	srv := New(ScriptedBuilder)

	b := broker.New()
	b.Spawn("p1", "review PR #7", "human", nil) // verse side registers the process
	nonce := srv.HostBroker("", b.WorkerHandler())

	ts := httptest.NewServer(srv.BrokerHandler())
	defer ts.Close()

	// claim, routed through /broker/<nonce>/processes/p1/claim.
	resp, err := ts.Client().Post(
		ts.URL+"/broker/"+nonce+"/processes/p1/claim", "application/json",
		strings.NewReader(`{"identity":"human:scott"}`))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("claim through broker mount = %d, want 200", resp.StatusCode)
	}
	var claim struct {
		Spec struct{ Task string }
	}
	_ = json.NewDecoder(resp.Body).Decode(&claim)
	if claim.Spec.Task != "review PR #7" {
		t.Fatalf("claim spec.task=%q, want the spawn task through the mount", claim.Spec.Task)
	}

	// an unregistered nonce is not found.
	bad, err := ts.Client().Post(
		ts.URL+"/broker/deadbeef/processes/p1/claim", "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("bad-nonce claim: %v", err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusNotFound {
		t.Fatalf("unregistered nonce = %d, want 404", bad.StatusCode)
	}
}
