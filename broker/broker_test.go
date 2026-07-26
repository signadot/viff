package broker_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/process"
)

// The full two-legged dial-in loop: verse drives a process through the ControlPolicy seam
// (Spawn + Subscribe + Cancel) while a worker dials in over claim/inbox/report. Worker
// reports become the events verse observes; verse control becomes items the worker pulls.
func TestBrokerDialInLoop(t *testing.T) {
	b := broker.New()

	// verse side: spawn + observe.
	h := b.Spawn("t1", "review PR #42", "human", nil)
	ch, unsub := h.Subscribe()
	defer unsub()

	// worker side: dial in.
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	// claim → the worker gets the spec + an epoch (the fencing token).
	var claim struct {
		Spec struct {
			Task   string `tony:"field=task"`
			Driver string `tony:"field=driver"`
		} `tony:"field=spec"`
		Epoch   int64 `tony:"field=epoch"`
		CCursor int64 `tony:"field=cCursor"`
	}
	postJSON(t, cl, srv.URL+"/processes/t1/claim", `{"identity":"human:scott"}`, &claim)
	if claim.Spec.Task != "review PR #42" {
		t.Fatalf("claim spec.task=%q, want the spawn task", claim.Spec.Task)
	}
	if claim.Epoch == 0 {
		t.Fatal("claim returned no epoch")
	}

	// report → the worker's utterances/lifecycle land on verse's read plane (carrying epoch).
	postJSON(t, cl, srv.URL+"/processes/t1/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[{"kind":"say","text":"looking"},{"kind":"status","text":"done"}],"result":"looks good"}`, nil)

	// Read until both worker reports arrive (the claim also emitted a running transition).
	seen := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for !seen["say:looking"] || !seen["status:done"] {
		select {
		case ev := <-ch:
			seen[ev.Kind+":"+ev.Text] = true
			if ev.Cursor == 0 {
				t.Fatal("reported event carried no cursor")
			}
		case <-deadline:
			t.Fatalf("verse did not observe the worker's reports: %v", seen)
		}
	}
	if h.Result() != "looks good" {
		t.Fatalf("Result()=%q, want the reported result", h.Result())
	}

	// verse control → the worker's inbox (a fresh process, so the cancel is observable
	// before terminal). Uses a second process to keep this assertion independent of the
	// completed one above.
	h2 := b.Spawn("t2", "watch", "human", nil)
	var claim2 struct {
		Epoch   int64 `tony:"field=epoch"`
		CCursor int64 `tony:"field=cCursor"`
	}
	postJSON(t, cl, srv.URL+"/processes/t2/claim", `{"identity":"human:scott"}`, &claim2)
	h2.Cancel("stop")
	var inbox struct {
		Items []struct {
			Kind string `tony:"field=kind"`
		} `tony:"field=items"`
	}
	getJSON(t, cl, srv.URL+"/processes/t2/inbox?since="+itoa(claim2.CCursor)+"&epoch="+itoa(claim2.Epoch), &inbox)
	var gotCancel bool
	for _, it := range inbox.Items {
		if it.Kind == "cancel" {
			gotCancel = true
		}
	}
	if !gotCancel {
		t.Fatalf("worker inbox did not carry the cancel: %+v", inbox.Items)
	}
}

// terminal-once: a second terminal report is a frozen no-op — the first result/status
// stays authoritative, and a lost-ack retry reads as success (200 + terminal), not error.
func TestTerminalOnce(t *testing.T) {
	b := broker.New()
	h := b.Spawn("p", "task", "human", nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/processes/p/claim", `{"identity":"a"}`, &claim)

	postJSON(t, cl, srv.URL+"/processes/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[{"kind":"status","text":"done"}],"result":"first"}`, nil)

	var second struct {
		Status   string `tony:"field=status"`
		Result   string `tony:"field=result"`
		Terminal bool   `tony:"field=terminal"`
	}
	postJSON(t, cl, srv.URL+"/processes/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[{"kind":"status","text":"failed"}],"result":"second"}`, &second)
	if !second.Terminal || second.Status != "done" || second.Result != "first" {
		t.Fatalf("post-terminal report should echo the frozen terminal, got %+v", second)
	}
	if h.Result() != "first" {
		t.Fatalf("Result()=%q, want the first (latched) result", h.Result())
	}
}

// Fork-1(b): verse Cancel latches terminal on verse's own authority — observable with no
// worker report — and a late worker `completed` loses to the latch.
func TestVerseAuthoritativeCancel(t *testing.T) {
	b := broker.New()
	h := b.Spawn("p", "task", "human", nil)
	ch, unsub := h.Subscribe()
	defer unsub()
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/processes/p/claim", `{"identity":"a"}`, &claim)

	h.Cancel("operator stop") // no worker report involved

	// The claim above emitted an unclaimed→running transition; skip it and assert the
	// verse-side cancel then latches cancelled (observable with no worker report).
	got := ""
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case ev := <-ch:
			if ev.Kind == process.EvStatus && ev.Text != string(process.Running) {
				got = ev.Text
				break loop
			}
		case <-deadline:
			t.Fatal("verse Cancel did not surface a cancelled status")
		}
	}
	if got != string(process.Cancelled) {
		t.Fatalf("status after cancel = %q, want cancelled from the verse-side latch", got)
	}

	// a late worker completion loses to the latch: the report is a frozen no-op.
	var late struct {
		Status string `tony:"field=status"`
	}
	postJSON(t, cl, srv.URL+"/processes/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[{"kind":"status","text":"done"}],"result":"done"}`, &late)
	if late.Status != "cancelled" {
		t.Fatalf("late completion status=%q, want the latched cancelled", late.Status)
	}
}

// Fork-2(a): a same-identity reconnect mints a fresh epoch that fences the prior generation
// (a zombie of the same worker), and a takeover by a different identity is legal only after
// the claim is released (Revoke).
func TestFenceAndTakeover(t *testing.T) {
	b := broker.New()
	b.Spawn("p", "task", "human", nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	// A claims twice (a reconnect): epoch advances; the first epoch is now fenced.
	var c1, c2 struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/processes/p/claim", `{"identity":"A"}`, &c1)
	postJSON(t, cl, srv.URL+"/processes/p/claim", `{"identity":"A"}`, &c2)
	if c2.Epoch <= c1.Epoch {
		t.Fatalf("reconnect epoch %d did not advance past %d", c2.Epoch, c1.Epoch)
	}
	if code := postCode(t, cl, srv.URL+"/processes/p/report",
		`{"epoch":`+itoa(c1.Epoch)+`,"events":[{"kind":"say","text":"stale"}]}`); code != http.StatusConflict {
		t.Fatalf("stale-epoch report = %d, want 409 fenced", code)
	}
	if code := postCode(t, cl, srv.URL+"/processes/p/report",
		`{"epoch":`+itoa(c2.Epoch)+`,"events":[{"kind":"say","text":"live"}]}`); code != http.StatusOK {
		t.Fatalf("current-epoch report = %d, want 200", code)
	}

	// B cannot take over a live claim...
	if code := postCode(t, cl, srv.URL+"/processes/p/claim", `{"identity":"B"}`); code != http.StatusConflict {
		t.Fatalf("uninvited takeover = %d, want 409", code)
	}
	// ...until verse revokes, which also fences A's current epoch.
	b.Revoke("p")
	var c3 struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/processes/p/claim", `{"identity":"B"}`, &c3)
	if c3.Epoch <= c2.Epoch {
		t.Fatalf("post-revoke claim epoch %d did not advance", c3.Epoch)
	}
	if code := postCode(t, cl, srv.URL+"/processes/p/report",
		`{"epoch":`+itoa(c2.Epoch)+`,"events":[{"kind":"say","text":"zombie"}]}`); code != http.StatusConflict {
		t.Fatalf("revoked A's report = %d, want 409 fenced", code)
	}
}

// The gate loop: a worker reports a pending gate (do gate), which shows on the snapshot and
// verse's stream; a verse-side Resolve enqueues a resolve item for the worker's inbox and
// records the decision (clearing pending). The executor never resolves its own gate.
func TestGateLoop(t *testing.T) {
	b := broker.New()
	h := b.Spawn("p", "task", "human", nil)
	ch, unsub := h.Subscribe()
	defer unsub()
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/processes/p/claim", `{"identity":"agent:ci"}`, &claim)

	// worker raises a gate (report EvPending).
	postJSON(t, cl, srv.URL+"/processes/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[{"kind":"pending","id":"deploy-1","gate":"deploy"}]}`, nil)
	drainFor(t, ch, "pending") // verse observes the pending gate

	if _, ok := b.ProcessSnapshot("p"); !ok {
		t.Fatal("no snapshot")
	}
	snap, _ := b.ProcessSnapshot("p")
	if len(snap.Pending) != 1 || snap.Pending[0].ID != "deploy-1" {
		t.Fatalf("snapshot pending = %+v, want the raised gate", snap.Pending)
	}

	rh := h.(interface {
		Resolve(string, bool, string) bool
	})

	// resolving an unknown decision id (typo, wrong process) matches no open gate and
	// reports false — so verse can 404 rather than silently enqueue a phantom resolve.
	if rh.Resolve("deploy-nope", true, "oops") {
		t.Fatal("Resolve of an unknown decision reported success")
	}

	// governance resolves the real gate (verse-side); the worker's inbox gets a resolve item.
	if !rh.Resolve("deploy-1", true, "ship it") {
		t.Fatal("Resolve of the open gate reported failure")
	}
	drainFor(t, ch, "decision") // verse observes the resolution; pending clears

	// resolving it a second time now finds nothing pending (already answered) -> false.
	if rh.Resolve("deploy-1", true, "again") {
		t.Fatal("second Resolve of an answered decision reported success")
	}

	inbox, ok := b.ProcessInbox("p")
	if !ok {
		t.Fatal("no inbox")
	}
	var got map[string]any
	for _, it := range inbox {
		if it["kind"] == "resolve" && it["decision"] == "deploy-1" {
			got = it
		}
	}
	if got == nil || got["proceed"] != true {
		t.Fatalf("worker inbox missing the approved resolve: %+v", inbox)
	}
	if snap, _ := b.ProcessSnapshot("p"); len(snap.Pending) != 0 {
		t.Fatalf("pending should clear after resolve: %+v", snap.Pending)
	}
}

// A re-vector (autonomy slide) must reach the worker with its payload intact: the handle
// enqueues a "vector" inbox item CARRYING the new vector, so a dialed-in worker pulls it and
// slides at its next boundary. (The bug this guards: the handle used to enqueue a bare
// "vector" marker and drop the vector, so the worker had nothing to apply.)
func TestSetVectorRelaysPayload(t *testing.T) {
	b := broker.New()
	h := b.Spawn("p", "task", "human", nil)

	h.SetVector(&process.Vector{Default: process.Ask}, "human:scott")

	inbox, ok := b.ProcessInbox("p")
	if !ok {
		t.Fatal("no inbox")
	}
	var got map[string]any
	for _, it := range inbox {
		if it["kind"] == "vector" {
			got = it
		}
	}
	if got == nil {
		t.Fatalf("worker inbox missing the vector item: %+v", inbox)
	}
	// The vector rode the item (not dropped). ProcessInbox marshals via the struct, so the
	// nested vector reads as its JSON shape: {"default":"ask"}.
	vec, _ := got["vector"].(*process.Vector)
	if vec == nil || vec.Default != process.Ask {
		// tolerate map-shaped decode if a caller round-trips through JSON
		raw, _ := json.Marshal(got["vector"])
		if !strings.Contains(string(raw), `"default":"ask"`) {
			t.Fatalf("vector item did not carry default=ask: vec=%+v raw=%s", vec, raw)
		}
	}
}

// A re-vector is operator-authored, so verse observes it on the read plane immediately (an
// EvVector carrying the new default mode) — like a say-to, not waiting on the worker to echo.
func TestSetVectorObservable(t *testing.T) {
	b := broker.New()
	h := b.Spawn("p", "task", "human", nil)
	ch, unsub := h.Subscribe()
	defer unsub()

	h.SetVector(&process.Vector{Default: process.Ask}, "human:scott")

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind != process.EvVector {
				continue
			}
			if ev.Mode != process.Ask.String() {
				t.Fatalf("EvVector should carry the new default mode %q, got %q", process.Ask.String(), ev.Mode)
			}
			if ev.By != "human:scott" {
				t.Fatalf("EvVector should attribute the operator, got By=%q", ev.By)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for an EvVector on the read plane")
		}
	}
}

// drainFor reads events until one of the given kind arrives (or times out).
func drainFor(t *testing.T, ch <-chan process.Frame, kind string) {
	t.Helper()
	for {
		select {
		case ev := <-ch:
			if ev.Kind == kind {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for a %q event", kind)
		}
	}
}

func postJSON(t *testing.T, cl *http.Client, url, body string, out any) {
	t.Helper()
	resp, err := cl.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if out != nil {
		decodeResp(t, resp, out)
	}
}

func postCode(t *testing.T, cl *http.Client, url, body string) int {
	t.Helper()
	resp, err := cl.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func getJSON(t *testing.T, cl *http.Client, url string, out any) {
	t.Helper()
	resp, err := cl.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	decodeResp(t, resp, out)
}

// decodeResp reads a response in the format it DECLARES. These tests send JSON bodies on
// purpose — that is the negotiation being exercised — but what comes back follows the
// server's default, so assuming JSON on the way in AND out would only test one half.
func decodeResp(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	f, ok := codec.FormatForMedia(resp.Header.Get("Content-Type"))
	if !ok {
		f = codec.Default()
	}
	if err := codec.Unmarshal(body, out, f); err != nil {
		t.Fatalf("decoding a %v response: %v\n%s", f, err, body)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

var _ = process.EvSay
