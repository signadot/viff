package broker_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
)

// The full two-legged dial-in loop: verse drives a task through the Runner seam
// (Spawn + Subscribe + Cancel) while a worker dials in over claim/inbox/report. Worker
// reports become the events verse observes; verse control becomes items the worker pulls.
func TestBrokerDialInLoop(t *testing.T) {
	b := broker.New()

	// verse side: spawn + observe.
	h := b.Spawn(task.Launch{ID: "t1", Prompt: "review PR #42", Driver: "human"}, nil)
	ch, unsub := h.Subscribe()
	defer unsub()

	// worker side: dial in.
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	// claim → the worker gets the spec + an epoch (the fencing token).
	var claim struct {
		Spec struct {
			Prompt string `tony:"field=prompt"`
			Driver string `tony:"field=driver"`
		} `tony:"field=spec"`
		Epoch   int64 `tony:"field=epoch"`
		CCursor int64 `tony:"field=cCursor"`
	}
	postJSON(t, cl, srv.URL+"/tasks/t1/claim", `{"identity":"human:scott"}`, &claim)
	if claim.Spec.Prompt != "review PR #42" {
		t.Fatalf("claim spec.task=%q, want the spawn task", claim.Spec.Prompt)
	}
	if claim.Epoch == 0 {
		t.Fatal("claim returned no epoch")
	}

	// report → the worker's utterances/lifecycle land on verse's read plane (carrying epoch).
	postJSON(t, cl, srv.URL+"/tasks/t1/report",
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

	// verse control → the worker's inbox (a fresh task, so the cancel is observable
	// before terminal). Uses a second task to keep this assertion independent of the
	// completed one above.
	h2 := b.Spawn(task.Launch{ID: "t2", Prompt: "watch", Driver: "human"}, nil)
	var claim2 struct {
		Epoch   int64 `tony:"field=epoch"`
		CCursor int64 `tony:"field=cCursor"`
	}
	postJSON(t, cl, srv.URL+"/tasks/t2/claim", `{"identity":"human:scott"}`, &claim2)
	h2.Cancel("stop")
	var inbox struct {
		Items []struct {
			Kind string `tony:"field=kind"`
		} `tony:"field=items"`
	}
	getJSON(t, cl, srv.URL+"/tasks/t2/inbox?since="+itoa(claim2.CCursor)+"&epoch="+itoa(claim2.Epoch), &inbox)
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
	h := b.Spawn(task.Launch{ID: "p", Prompt: "task", Driver: "human"}, nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"a"}`, &claim)

	postJSON(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[{"kind":"status","text":"done"}],"result":"first"}`, nil)

	var second struct {
		Status   string `tony:"field=status"`
		Result   string `tony:"field=result"`
		Terminal bool   `tony:"field=terminal"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/report",
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
	h := b.Spawn(task.Launch{ID: "p", Prompt: "task", Driver: "human"}, nil)
	ch, unsub := h.Subscribe()
	defer unsub()
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"a"}`, &claim)

	h.Cancel("operator stop") // no worker report involved

	// The claim above emitted an unclaimed→running transition; skip it and assert the
	// verse-side cancel then latches cancelled (observable with no worker report).
	got := ""
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case ev := <-ch:
			if ev.Kind == task.EvStatus && ev.Text != string(task.Running) {
				got = ev.Text
				break loop
			}
		case <-deadline:
			t.Fatal("verse Cancel did not surface a cancelled status")
		}
	}
	if got != string(task.Cancelled) {
		t.Fatalf("status after cancel = %q, want cancelled from the verse-side latch", got)
	}

	// a late worker completion loses to the latch: the report is a frozen no-op.
	var late struct {
		Status string `tony:"field=status"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/report",
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
	b.Spawn(task.Launch{ID: "p", Prompt: "task", Driver: "human"}, nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	// A claims twice (a reconnect): epoch advances; the first epoch is now fenced.
	var c1, c2 struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"A"}`, &c1)
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"A"}`, &c2)
	if c2.Epoch <= c1.Epoch {
		t.Fatalf("reconnect epoch %d did not advance past %d", c2.Epoch, c1.Epoch)
	}
	if code := postCode(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(c1.Epoch)+`,"events":[{"kind":"say","text":"stale"}]}`); code != http.StatusConflict {
		t.Fatalf("stale-epoch report = %d, want 409 fenced", code)
	}
	if code := postCode(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(c2.Epoch)+`,"events":[{"kind":"say","text":"live"}]}`); code != http.StatusOK {
		t.Fatalf("current-epoch report = %d, want 200", code)
	}

	// B cannot take over a live claim...
	if code := postCode(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"B"}`); code != http.StatusConflict {
		t.Fatalf("uninvited takeover = %d, want 409", code)
	}
	// ...until verse revokes, which also fences A's current epoch.
	b.Revoke("p")
	var c3 struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"B"}`, &c3)
	if c3.Epoch <= c2.Epoch {
		t.Fatalf("post-revoke claim epoch %d did not advance", c3.Epoch)
	}
	if code := postCode(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(c2.Epoch)+`,"events":[{"kind":"say","text":"zombie"}]}`); code != http.StatusConflict {
		t.Fatalf("revoked A's report = %d, want 409 fenced", code)
	}
}

// The gate loop: a worker reports a pending gate (do gate), which shows on the snapshot and
// verse's stream; a verse-side Resolve enqueues a resolve item for the worker's inbox and
// records the decision (clearing pending). The executor never resolves its own gate.
func TestGateLoop(t *testing.T) {
	b := broker.New()
	h := b.Spawn(task.Launch{ID: "p", Prompt: "task", Driver: "human"}, nil)
	ch, unsub := h.Subscribe()
	defer unsub()
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:ci"}`, &claim)

	// worker raises a gate (report EvPending), carrying the --reason on text.
	postJSON(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[{"kind":"pending","id":"deploy-1","gate":"deploy",`+
			`"tool":"deploy_prod","text":"prod, off-hours"}]}`, nil)
	drainFor(t, ch, "pending") // verse observes the pending gate

	if _, err := b.TaskSnapshot("p"); err != nil {
		t.Fatalf("no snapshot: %v", err)
	}
	snap, _ := b.TaskSnapshot("p")
	if len(snap.Pending) != 1 || snap.Pending[0].ID != "deploy-1" {
		t.Fatalf("snapshot pending = %+v, want the raised gate", snap.Pending)
	}
	// The reason is what a resolver reads to decide, so it has to survive the projection
	// from the worker's report to the snapshot `verse gate` renders — it used to be
	// dropped by a pending map that kept only id -> gate.
	if got := snap.Pending[0]; got.Reason != "prod, off-hours" || got.Tool != "deploy_prod" ||
		got.Gate != "deploy" {
		t.Fatalf("snapshot pending = %+v, want gate/tool/reason as reported", got)
	}

	// This assertion mirrors versed's, which is a STRUCTURAL one guarded by `, ok` — a
	// signature change on one side alone makes it silently report false and brokered
	// resolution stops working with no compile error. Asserting it here is what turns that
	// into a build failure.
	rh := h.(interface {
		Resolve(decision, choice, say, by string) error
	})

	// An unknown decision id (typo, wrong task) matches no open gate and says so as
	// ErrNoSuchDecision — so verse can 404 rather than silently enqueue a phantom resolve.
	if err := rh.Resolve("deploy-nope", "proceed", "oops", "human:ops"); !errors.Is(err, task.ErrNoSuchDecision) {
		t.Fatalf("Resolve of an unknown decision = %v, want ErrNoSuchDecision", err)
	}

	// governance resolves the real gate (verse-side); the worker's inbox gets a resolve item.
	if err := rh.Resolve("deploy-1", "proceed", "ship it", "human:ops"); err != nil {
		t.Fatalf("Resolve of the open gate = %v, want nil", err)
	}
	// The worker takes it, and THAT is when verse observes a decision. Queuing an answer is
	// not delivering one — the read plane says what the worker acted on, not what was sent
	// to it — so the poll is the step that makes this a decision rather than an intent.
	var pulled struct {
		Items []api.InboxItem `tony:"field=items"`
	}
	getJSON(t, cl, srv.URL+"/tasks/p/inbox?since=0&epoch="+itoa(claim.Epoch), &pulled)
	if len(pulled.Items) == 0 {
		t.Fatal("the answer never reached the worker's inbox")
	}
	// And the poll that ACKNOWLEDGES it is the next one, asking for what comes after —
	// receiving is not taking, since that response is exactly what a lost one looks like.
	getJSON(t, cl, srv.URL+"/tasks/p/inbox?since="+itoa(pulled.Items[len(pulled.Items)-1].Cursor)+
		"&epoch="+itoa(claim.Epoch), &pulled)
	drainFor(t, ch, "decision")

	// Resolving it a second time is a DIFFERENT refusal from the first one above, and the
	// two must not collapse: one means look for your typo, the other means somebody was
	// here first. That distinction is what lets versed answer 404 and 409 respectively.
	if err := rh.Resolve("deploy-1", "proceed", "again", "human:ops"); !errors.Is(err, task.ErrAlreadyAnswered) {
		t.Fatalf("second Resolve of an answered decision = %v, want ErrAlreadyAnswered", err)
	}

	inbox, ok := b.TaskInbox("p")
	if !ok {
		t.Fatal("no inbox")
	}
	var got *api.InboxItem
	for i, it := range inbox {
		if it.Kind == "resolve" && it.Decision == "deploy-1" {
			got = &inbox[i]
		}
	}
	if got == nil || !got.Proceed {
		t.Fatalf("worker inbox missing the approved resolve: %+v", inbox)
	}
	// The answerer reaches the worker too: a dialed-in executor unblocked by a resolve is
	// entitled to know who unblocked it, and the identity is dropped nowhere along the way.
	if got.By != "human:ops" {
		t.Fatalf("resolve inbox item By = %q, want human:ops — the answerer must survive the relay", got.By)
	}
	if snap, _ := b.TaskSnapshot("p"); len(snap.Pending) != 0 {
		t.Fatalf("pending should clear after resolve: %+v", snap.Pending)
	}
}

// A re-vector (autonomy slide) must reach the worker with its payload intact: the handle
// enqueues a "vector" inbox item CARRYING the new vector, so a dialed-in worker pulls it and
// slides at its next boundary. (The bug this guards: the handle used to enqueue a bare
// "vector" marker and drop the vector, so the worker had nothing to apply.)
func TestSetVectorRelaysPayload(t *testing.T) {
	b := broker.New()
	h := b.Spawn(task.Launch{ID: "p", Prompt: "task", Driver: "human"}, nil)

	h.SetVector(&task.Vector{Default: task.Ask}, "human:scott")

	inbox, ok := b.TaskInbox("p")
	if !ok {
		t.Fatal("no inbox")
	}
	var got *api.InboxItem
	for i, it := range inbox {
		if it.Kind == "vector" {
			got = &inbox[i]
		}
	}
	if got == nil {
		t.Fatalf("worker inbox missing the vector item: %+v", inbox)
	}
	// The vector rode the item, and it is just the vector. This used to type-assert out of a
	// map[string]any and fall back to marshalling JSON and matching a substring, because
	// TaskInbox rebuilt each item as a map and the nested vector's Go type depended on
	// who had decoded it. A typed item needs neither.
	if got.Vector == nil || got.Vector.Default != task.Ask {
		t.Fatalf("vector item did not carry default=ask: %+v", got.Vector)
	}
}

// A re-vector is operator-authored, so verse observes it on the read plane immediately (an
// EvVector carrying the new default mode) — like a say-to, not waiting on the worker to echo.
func TestSetVectorObservable(t *testing.T) {
	b := broker.New()
	h := b.Spawn(task.Launch{ID: "p", Prompt: "task", Driver: "human"}, nil)
	ch, unsub := h.Subscribe()
	defer unsub()

	h.SetVector(&task.Vector{Default: task.Ask}, "human:scott")

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind != task.EvVector {
				continue
			}
			if ev.Mode != task.Ask.String() {
				t.Fatalf("EvVector should carry the new default mode %q, got %q", task.Ask.String(), ev.Mode)
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
func drainFor(t *testing.T, ch <-chan task.Frame, kind string) {
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

var _ = task.EvSay

// A failure reason is filed as a failure, not as an answer.
//
// `do complete <x>` and `do fail <why>` both ride the report's `result` field, so the broker
// used to latch both into Result — and a failed brokered task reported
// `result: "the migration is not reversible"` where an identical LOCAL one reported
// `err: "exit status 3"`. Same question, different field, decided by where the work ran.
// The mirror then ran ParseResult over that result hunting structured data in a sentence
// that was never going to carry any.
func TestFailureReasonIsAnErrNotAResult(t *testing.T) {
	b := broker.New()
	h := b.Spawn(task.Launch{ID: "p", Prompt: "migrate the schema", Driver: "human"}, nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"human:scott"}`, &claim)

	const why = "the migration is not reversible"
	postJSON(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[{"kind":"status","text":"failed"}],"result":"`+why+`"}`, nil)

	snap, err := b.TaskSnapshot("p")
	if err != nil {
		t.Fatalf("no snapshot: %v", err)
	}
	if snap.Status != task.Failed {
		t.Fatalf("status = %q, want failed", snap.Status)
	}
	if snap.Err != why {
		t.Errorf("snapshot Err = %q, want the reason %q — a failure reason belongs in Err", snap.Err, why)
	}
	if snap.Result != "" {
		t.Errorf("snapshot Result = %q, want empty — the reason is not the task's answer", snap.Result)
	}
	// The Handle agrees, since that is what the mirror projects onto the entity.
	if h.Err() != why {
		t.Errorf("Err() = %q, want %q", h.Err(), why)
	}
	if h.Result() != "" {
		t.Errorf("Result() = %q, want empty", h.Result())
	}
}

// A successful completion is unaffected: `do complete <answer>` is still a Result, and Err
// stays empty — the routing keys off the status, not off the presence of text.
func TestCompletionTextStaysAResult(t *testing.T) {
	b := broker.New()
	h := b.Spawn(task.Launch{ID: "p", Prompt: "review", Driver: "human"}, nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"human:scott"}`, &claim)
	postJSON(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[{"kind":"status","text":"done"}],"result":"LGTM"}`, nil)

	snap, _ := b.TaskSnapshot("p")
	if snap.Result != "LGTM" || snap.Err != "" {
		t.Fatalf("snapshot = {Result:%q Err:%q}, want {LGTM, empty}", snap.Result, snap.Err)
	}
	if h.Result() != "LGTM" || h.Err() != "" {
		t.Fatalf("handle = {Result:%q Err:%q}, want {LGTM \"\"}", h.Result(), h.Err())
	}
}

// A terminal takes the gates it never answered with it.
//
// It did not: the latch left the pending set untouched, so a cancelled task's open gate
// could still be answered — reporting success, and writing a decision onto the log of a run
// that had already ended. `report` was guarded post-terminal and the gate set was not.
func TestATerminalAbandonsItsOpenGates(t *testing.T) {
	b := broker.New()
	b.Spawn(task.Launch{ID: "p", Prompt: "work", Driver: "human"}, nil)
	h := b.Attach("p")
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()
	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:ci"}`, &claim)
	postJSON(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[{"kind":"pending","id":"p~d1","gate":"write"}]}`, nil)

	if err := h.Cancel("operator changed their mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	inst, _ := b.TaskSnapshot("p")
	if len(inst.Pending) != 0 {
		t.Fatalf("a cancelled task still holds %d open gate(s)", len(inst.Pending))
	}

	rh := h.(interface {
		Resolve(decision, choice, say, by string) error
	})
	// And the refusal says WHICH refusal it is. "No such decision" would send the answerer
	// looking for a typo; the gate was real and the task ended underneath them.
	if err := rh.Resolve("p~d1", "proceed", "ship it", "human:scott"); !errors.Is(err, task.ErrTaskEnded) {
		t.Fatalf("answering a gate on an ended task = %v, want ErrTaskEnded", err)
	}
}

// A report says how many of its events it applied.
//
// It is not always all of them: nothing follows a terminal, so a batch whose terminal is not
// its last entry has the remainder dropped. Answering 200 and nothing else told a worker its
// whole wind-down had landed while part of it was on the floor, with no way to find out.
func TestAReportSaysWhatItApplied(t *testing.T) {
	b := broker.New()
	b.Spawn(task.Launch{ID: "p", Prompt: "work", Driver: "human"}, nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()
	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:ci"}`, &claim)

	var out struct {
		Applied int `tony:"field=applied"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[`+
			`{"kind":"exec","tool":"deploy"},`+
			`{"kind":"status","text":"done"},`+
			`{"kind":"say","text":"and here is what I learned"},`+
			`{"kind":"exec","tool":"cleanup"}],"result":"shipped"}`, &out)

	if out.Applied != 2 {
		t.Fatalf("applied = %d for a 4-event batch whose terminal is third from last, want 2 "+
			"— the worker cannot compute a retry from a count that does not match", out.Applied)
	}
}

// A reconnecting worker resumes from what it ACKNOWLEDGED, so control issued while it was
// away is still waiting for it.
//
// The claim used to answer with the broker's current inbox cursor, which told a worker that
// had just come back to skip everything queued during its outage — and an outage is exactly
// when control is most likely to have been issued. Composed with a decision recorded at
// enqueue time, the task then waited forever on an answer every other surface reported as
// delivered.
func TestAReconnectingWorkerResumesFromWhatItAcknowledged(t *testing.T) {
	b := broker.New()
	b.Spawn(task.Launch{ID: "p", Prompt: "work", Driver: "human"}, nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	claimOnce := func() (epoch, cCursor int64) {
		t.Helper()
		var c struct {
			Epoch   int64 `tony:"field=epoch"`
			CCursor int64 `tony:"field=cCursor"`
		}
		postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:ci"}`, &c)
		return c.Epoch, c.CCursor
	}
	items := func(epoch, since int64) int {
		t.Helper()
		var out struct {
			Items []api.InboxItem `tony:"field=items"`
		}
		getJSON(t, cl, srv.URL+"/tasks/p/inbox?since="+itoa(since)+"&epoch="+itoa(epoch), &out)
		return len(out.Items)
	}

	epoch, c0 := claimOnce()
	// Two controls the worker DOES take, so its acknowledged position is not zero and a
	// cCursor of 0 cannot pass this by accident — replaying everything from the start is
	// B3's behaviour, not this rule.
	h := b.Attach("p")
	if err := h.Steer("use the staging bucket", "human:scott"); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if err := h.Pause(); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if got := items(epoch, c0); got != 2 {
		t.Fatalf("the worker's first poll saw %d items, want 2", got)
	}
	acked := int64(2) // it took both, so it has acknowledged through cursor 2

	postJSON(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(epoch)+`,"events":[{"kind":"pending","id":"p~d1","gate":"write"}]}`, nil)
	items(epoch, acked) // polls again from where it got to, then drops

	// While it is away, the gate is answered.
	rh := b.Attach("p").(interface {
		Resolve(decision, choice, say, by string) error
	})
	if err := rh.Resolve("p~d1", "proceed", "ship it", "human:scott"); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// It comes back and polls from where the broker told it to.
	epoch2, c1 := claimOnce()
	if c1 != acked {
		t.Fatalf("cCursor = %d on reconnect, want %d — the worker's acknowledged position, "+
			"not the broker's current one and not the beginning", c1, acked)
	}
	if got := items(epoch2, c1); got != 1 {
		t.Fatalf("a reconnecting worker polling from cCursor=%d got %d items, want the answer "+
			"queued while it was away", c1, got)
	}
}

// A claim nobody renews is released, so the work can be taken over.
//
// It could not be. A worker that claimed and vanished held its claim, its open gate and its
// task forever; a second worker was refused 409, and the only release was an operator
// calling Revoke, which nothing calls. The code's own comment said "the eventual health-check
// calls it" and there was no health check.
//
// The renewal is the work itself: every inbox poll and every report says the holder is alive,
// so a worker that talks to the broker at all is already keeping its lease.
func TestAnUnrenewedClaimIsReleased(t *testing.T) {
	b := broker.New()
	b.Spawn(task.Launch{ID: "p", Prompt: "work", Driver: "human"}, nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	var first struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:one"}`, &first)
	// …and it is never heard from again. Age the lease rather than waiting it out.
	b.AgeClaimForTest("p", broker.ClaimLease+time.Second)

	var second struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:two"}`, &second)
	if second.Epoch <= first.Epoch {
		t.Fatalf("takeover epoch %d did not fence the silent holder at %d", second.Epoch, first.Epoch)
	}

	// The silent one is fenced: if it comes back it comes back as a stranger, and cannot
	// write onto a log another worker now owns.
	code := postCode(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(first.Epoch)+`,"events":[{"kind":"exec","tool":"deploy"}]}`)
	if code != http.StatusConflict {
		t.Fatalf("the displaced worker's report = %d, want 409", code)
	}

	// And the task is NOT terminal. The broker cannot hear the worker, which says nothing
	// about whether the work is running — only that nobody may claim to speak for it.
	inst, _ := b.TaskSnapshot("p")
	if task.Status(inst.Status).Terminal() {
		t.Fatalf("an expired claim ended the task: %q", inst.Status)
	}
}

// A worker that keeps working keeps its claim. The lease must not expire under somebody who
// is simply on a slow link but still polling.
func TestAWorkingWorkerKeepsItsClaim(t *testing.T) {
	b := broker.New()
	b.Spawn(task.Launch{ID: "p", Prompt: "work", Driver: "human"}, nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()

	var first struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:one"}`, &first)
	b.AgeClaimForTest("p", broker.ClaimLease+time.Second)

	// It polls — which is what a worker waiting on control does, and is the renewal.
	var out struct {
		Items []api.InboxItem `tony:"field=items"`
	}
	getJSON(t, cl, srv.URL+"/tasks/p/inbox?since=0&epoch="+itoa(first.Epoch), &out)

	if code := postCode(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:two"}`); code != http.StatusConflict {
		t.Fatalf("a takeover of a claim that was just renewed = %d, want 409", code)
	}
}

// An answer is a decision when the worker has TAKEN it, not when it was queued.
//
// Resolve used to emit the decision and clear the gate at enqueue, so the read plane, the
// snapshot and the question record all reported the question settled while the worker that
// must act on it had heard nothing. If it never came back, the answer evaporated with no
// trace that it was undelivered.
//
// The two moments are separated rather than merged: the right to answer is taken at once — a
// second answerer is told somebody was here first, because two answers to one question is a
// race with one winner however slowly the answer travels — and the decision is recorded on
// acknowledgement.
func TestAnAnswerBecomesADecisionOnDelivery(t *testing.T) {
	b := broker.New()
	b.Spawn(task.Launch{ID: "p", Prompt: "work", Driver: "human"}, nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()
	ch, unsub := b.Attach("p").Subscribe()
	defer unsub()

	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:ci"}`, &claim)
	postJSON(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[{"kind":"pending","id":"d1","gate":"deploy"}]}`, nil)
	drainFor(t, ch, "pending")

	rh := b.Attach("p").(interface {
		Resolve(decision, choice, say, by string) error
	})
	if err := rh.Resolve("d1", "proceed", "ship it", "human:ops"); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Queued, and NOT yet a decision: the worker has not been heard from since.
	select {
	case ev := <-ch:
		if ev.Kind == task.EvDecision {
			t.Fatal("the answer was recorded as a decision before the worker had taken it")
		}
	case <-time.After(150 * time.Millisecond):
	}

	// But the gate is closed to a second answerer straight away.
	if err := rh.Resolve("d1", "deny", "no", "human:other"); !errors.Is(err, task.ErrAlreadyAnswered) {
		t.Fatalf("a second answer = %v, want ErrAlreadyAnswered", err)
	}

	// The worker pulls, then polls past it — and now it is a decision.
	var pulled struct {
		Items []api.InboxItem `tony:"field=items"`
	}
	getJSON(t, cl, srv.URL+"/tasks/p/inbox?since=0&epoch="+itoa(claim.Epoch), &pulled)
	last := pulled.Items[len(pulled.Items)-1].Cursor
	getJSON(t, cl, srv.URL+"/tasks/p/inbox?since="+itoa(last)+"&epoch="+itoa(claim.Epoch), &pulled)
	drainFor(t, ch, "decision")
}

// A retried report is answered, not applied a second time.
//
// A worker whose response was lost cannot know whether its events landed, and without a key
// its only choices were to drop what it observed or to report it twice — one deploy becoming
// two events at two cursors, afterwards indistinguishable from the worker having done it
// twice.
func TestARetriedReportIsAnsweredNotReapplied(t *testing.T) {
	b := broker.New()
	b.Spawn(task.Launch{ID: "p", Prompt: "work", Driver: "human"}, nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()
	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:ci"}`, &claim)

	body := `{"epoch":` + itoa(claim.Epoch) + `,"key":"r1","events":[{"kind":"exec","tool":"deploy"}]}`
	var first, again struct {
		RCursor int64 `tony:"field=rCursor"`
		Applied int   `tony:"field=applied"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/report", body, &first)
	postJSON(t, cl, srv.URL+"/tasks/p/report", body, &again) // the response never arrived

	if again != first {
		t.Fatalf("the retry was answered %+v and the original %+v; a worker cannot tell "+
			"a retry that landed from one that did not", again, first)
	}
	inst, _ := b.TaskSnapshot("p")
	if inst.Cursor != first.RCursor {
		t.Fatalf("cursor moved to %d on a retry of one deploy the worker performed once "+
			"(answered at %d)", inst.Cursor, first.RCursor)
	}

	// A DIFFERENT key is a different report, whatever it carries.
	var third struct {
		RCursor int64 `tony:"field=rCursor"`
		Applied int   `tony:"field=applied"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"key":"r2","events":[{"kind":"exec","tool":"deploy"}]}`, &third)
	if third.RCursor <= first.RCursor {
		t.Fatalf("a second, distinct report did not advance the log: %d → %d", first.RCursor, third.RCursor)
	}
}

// A worker that lost its cursor and asks from the beginning is served from where it actually
// got to, so control it has already acted on is not handed back.
//
// The broker knows what this claim acknowledged; the worker does not have to remember. What
// it has NOT taken is still waiting for it, which is the part that matters.
func TestAWorkerAskingFromTheBeginningIsServedFromWhereItGotTo(t *testing.T) {
	b := broker.New()
	b.Spawn(task.Launch{ID: "p", Prompt: "work", Driver: "human"}, nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()
	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:ci"}`, &claim)

	h := b.Attach("p")
	if err := h.Steer("use the staging bucket", "human:scott"); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if err := h.Pause(); err != nil {
		t.Fatalf("pause: %v", err)
	}
	items := func(since int64) []api.InboxItem {
		t.Helper()
		var out struct {
			Items []api.InboxItem `tony:"field=items"`
		}
		getJSON(t, cl, srv.URL+"/tasks/p/inbox?since="+itoa(since)+"&epoch="+itoa(claim.Epoch), &out)
		return out.Items
	}
	got := items(0)
	if len(got) != 2 {
		t.Fatalf("first poll = %d items, want 2", len(got))
	}
	last := got[len(got)-1].Cursor
	items(last) // acknowledges both

	if again := items(0); len(again) != 0 {
		t.Fatalf("asking from 0 after acknowledging through %d returned %d items; a worker "+
			"that forgot would re-apply every control it ever took", last, len(again))
	}

	// …and a TAKEOVER starts fresh: what the previous holder acknowledged says nothing about
	// what this worker has done, and an acknowledgement was never a promise of execution.
	b.AgeClaimForTest("p", broker.ClaimLease+time.Second)
	var second struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:other"}`, &second)
	var out struct {
		Items []api.InboxItem `tony:"field=items"`
	}
	getJSON(t, cl, srv.URL+"/tasks/p/inbox?since=0&epoch="+itoa(second.Epoch), &out)
	if len(out.Items) != 2 {
		t.Fatalf("a new worker taking over saw %d items, want the 2 still queued", len(out.Items))
	}
}

// The merged log is bounded, and a reader that falls below the window is told.
//
// It was unbounded — memory grew with the run — and its truncation was silent: a fresh
// subscribe replayed what fitted and discarded the rest with no marker at all, unlike the
// local bus. The two interacted badly: the below-watermark resync frame subscribeFrom
// already wrote could never fire, because an unbounded log has no watermark to be below.
func TestTheLogIsBoundedAndSaysWhenAReaderFellBelowIt(t *testing.T) {
	b := broker.New()
	b.Spawn(task.Launch{ID: "p", Prompt: "work", Driver: "human", Retain: 32}, nil)
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	cl := srv.Client()
	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, cl, srv.URL+"/tasks/p/claim", `{"identity":"agent:ci"}`, &claim)

	var evs strings.Builder
	for i := range 200 {
		if i > 0 {
			evs.WriteString(",")
		}
		evs.WriteString(`{"kind":"exec","tool":"noop"}`)
	}
	postJSON(t, cl, srv.URL+"/tasks/p/report",
		`{"epoch":`+itoa(claim.Epoch)+`,"events":[`+evs.String()+`]}`, nil)

	inst, _ := b.TaskSnapshot("p")
	if inst.Cursor != 201 { // 200 events plus the claim's running status
		t.Fatalf("cursor = %d, want 201 — the cursor counts everything, trimmed or not", inst.Cursor)
	}

	// Resuming from the beginning is now below the window, and says so before the replay.
	rh := b.Attach("p").(interface {
		SubscribeSince(int64) (<-chan task.Frame, func())
	})
	ch, unsub := rh.SubscribeSince(1)
	defer unsub()
	var first task.Frame
	select {
	case first = <-ch:
	case <-time.After(time.Second):
		t.Fatal("the resume delivered nothing")
	}
	if first.Kind != task.EvGap {
		t.Fatalf("first frame = %q, want a resync marker: the reader's cursor is below a "+
			"window that is now 32 events of a 201-event log", first.Kind)
	}

	// And the log really is trimmed rather than merely reported as such.
	var n int
	for {
		select {
		case <-ch:
			n++
			continue
		case <-time.After(100 * time.Millisecond):
		}
		break
	}
	if n > 40 {
		t.Fatalf("the replay delivered %d frames of a 32-event window", n)
	}
}
