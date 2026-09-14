package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// The resolve body is one type for both routes (api.Resolve), and these are the tests
// that keep it that way. They exist because the two routes previously carried two
// vocabularies — `{id, approve}` here, `{decisionId, proceed}` on the task route —
// and nothing noticed: no test posted either body, so the drift was invisible to
// everything except a person reading both handlers at once.
//
// Posting the SHARED type is the point. A field renamed in api.Resolve now fails to
// compile here and at every call site, instead of decoding to a zero value and
// answering "unknown or already-answered decision" at runtime.

// postTony sends v as tony to path and returns the response, as the tester: the principal the
// CLI would send from its config's `user:`, so what a test does is somebody's, the way what a
// person does is. A test about the unattributed request sends none (postAs).
func postTony(t *testing.T, url string, v any) *http.Response {
	t.Helper()
	b, err := codec.Marshal(v, codec.Tony)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return postAs(t, tester, url, "application/tony", string(b))
}

// tester is who a test acts as.
const tester = "tester@example.com"

// An engine gate posted to the registry is answered through POST /gate/resolve carrying
// api.Resolve — the id names the gate, choice carries the answer, and say rides along
// as steering.
func TestGateResolveAcceptsAPIResolve(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Raise a gate the way the engine does, and block on it as the engine would.
	const id = "verse.trigger.review.review~1"
	answered := make(chan task.Resolution, 1)
	go func() {
		res, _ := srv.Reg.Resolve(context.Background(),
			task.GateQuestion{ID: id, Origin: "review", Gate: "review", Mode: task.Ask},
			task.ToolUse{Name: "verse.trigger.review"})
		answered <- res
	}()
	waitPending(t, srv, id)

	resp := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: id, Choice: "proceed", By: "human:scott", Say: []string{"ship it"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("gate resolve: status = %s, want 204", resp.Status)
	}

	select {
	case res := <-answered:
		if !res.Proceed {
			t.Error("gate resolved to deny; the choice did not arrive")
		}
		if len(res.Steer) != 1 || res.Steer[0] != "ship it" {
			t.Errorf("steer = %v, want [ship it] — the say field did not arrive", res.Steer)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gate never resolved: the posted body did not decode into api.Resolve")
	}
}

// The unification regression, on a REAL pending decision: a body naming its gate `id`
// and its answer `choice` must resolve it on the task route too.
//
// It has to be a real one. Against the old handler this same body decoded to an empty
// decision id, and an empty id looks up nothing — so every cheaper assertion (400? 404?
// 501?) comes out identical whether the body decoded or not. Only a decision that is
// actually open distinguishes "answered it" from "silently answered nothing".
func TestTaskResolveAcceptsAPIResolve(t *testing.T) {
	srv := testServer(t)
	b := broker.New()
	b.Spawn(task.Launch{ID: "p1", Prompt: "review PR #7", Driver: "human"}, nil)
	srv.SetRunner(b) // the resolve route relays to a worker only under a broker runner
	nonce := srv.HostBroker("", b.WorkerHandler())

	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()
	work := httptest.NewServer(srv.BrokerHandler())
	defer work.Close()

	// A worker claims the task and reports a gate it is blocked on — which is how a
	// decision becomes open and resolvable.
	claim := postTony(t, work.URL+"/broker/"+nonce+"/tasks/p1/claim", map[string]any{"identity": "runner:scott"})
	var cl struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, claim, &cl)
	claim.Body.Close()

	const decision = "p1~d1"
	rep := postTony(t, work.URL+"/broker/"+nonce+"/tasks/p1/report", map[string]any{
		"epoch":  cl.Epoch,
		"events": []task.Frame{{Kind: task.EvPending, ID: decision, Gate: "write"}},
	})
	rep.Body.Close()

	// Now answer it the way `verse task resolve` does.
	resp := postTony(t, ops.URL+"/tasks/p1/resolve", api.Resolve{ID: decision, Choice: "proceed", By: "human:scott"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("task resolve = %s, want 204 — api.Resolve did not reach the decision (the routes have drifted apart again)", resp.Status)
	}

	// And it is gone: a second answer finds nothing open, which is what proves the first
	// one landed on the named decision rather than on an empty id. It is a CONFLICT, not a
	// 404 — the question exists and somebody else settled it.
	again := postTony(t, ops.URL+"/tasks/p1/resolve", api.Resolve{ID: decision, Choice: "proceed", By: "human:alice"})
	defer again.Body.Close()
	if again.StatusCode != http.StatusConflict {
		t.Errorf("re-resolve = %s, want 409 (already answered)", again.Status)
	}
}

// `verse gate approve` must answer an EXECUTOR-raised gate, whose id carries no owner.
//
// The unified `verse gate` lists engine and task gates together and tells the reader to
// "answer one with `verse gate`". For a gate raised by `task do gate` that was false: the
// id is <name>-<hex>, deliberately free of GateSep (shortID keeps it clear of anything a
// shell or URL reads as punctuation), so splitting on GateSep recovered no owner, no task
// matched, and the answer fell through to the engine registry — which has never heard of it.
// 404, on a gate listed one line above with its `task:` field populated.
func TestGateResolveRoutesAnOwnerlessTaskGate(t *testing.T) {
	srv := testServer(t)
	b := broker.New()
	b.Spawn(task.Launch{ID: "p1", Prompt: "review the auth change", Driver: "human"}, nil)
	srv.SetRunner(b)
	nonce := srv.HostBroker("", b.WorkerHandler())

	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()
	work := httptest.NewServer(srv.BrokerHandler())
	defer work.Close()

	claim := postTony(t, work.URL+"/broker/"+nonce+"/tasks/p1/claim", map[string]any{"identity": "runner:scott"})
	var cl struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, claim, &cl)
	claim.Body.Close()

	// The shape `task do gate` mints: a gate name and a hex suffix, no GateSep anywhere.
	const decision = "deploy-approval-dbc7b607"
	if strings.Contains(decision, task.GateSep) {
		t.Fatalf("decision %q carries GateSep; this test is about ids that do NOT", decision)
	}
	rep := postTony(t, work.URL+"/broker/"+nonce+"/tasks/p1/report", map[string]any{
		"epoch":  cl.Epoch,
		"events": []task.Frame{{Kind: task.EvPending, ID: decision, Gate: "deploy-approval"}},
	})
	rep.Body.Close()

	// Answer it the way an operator reading `verse gate` does — by id alone, with no
	// task id to help, through the same route `verse gate approve` posts to.
	resp := postTony(t, ops.URL+"/gate/resolve", api.Resolve{ID: decision, Choice: "proceed", By: "human:alice"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("gate resolve = %s, want 204 — an ownerless task gate must still route", resp.Status)
	}

	// It reached the worker, carrying who answered.
	inbox, ok := b.TaskInbox("p1")
	if !ok {
		t.Fatal("no inbox")
	}
	var got *api.InboxItem
	for i, it := range inbox {
		if it.Kind == "resolve" && it.Decision == decision {
			got = &inbox[i]
		}
	}
	if got == nil || !got.Proceed {
		t.Fatalf("worker inbox missing the approved resolve: %+v", inbox)
	}
	if got.By != "human:alice" {
		t.Errorf("resolve By = %q, want human:alice", got.By)
	}
}

// waitPending waits until a question is on the docket AND PUBLISHED — both, because the two
// happen in that order and a test that stops at the first reads the store in the window between
// them.
//
// task.Registry.Resolve puts the question in `pending` under its lock and calls OnPost after
// releasing it, so `Reg.Pending()` reports a question whose record is not written yet. Waiting
// on the registry alone was a race that a slower write turns from rare into reliable — which is
// how it was found, one write path later.
func waitPending(t *testing.T, srv *Server, id string) {
	t.Helper()
	for range 100 {
		for _, p := range srv.Reg.Pending() {
			if p.Question.ID == id {
				if _, ok, _ := srv.Store.Get(mirror.QuestionRef(id)); ok {
					return
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("gate %s never became pending", id)
}

// A gate listing omits what is genuinely absent rather than rendering it as "".
//
// A brokered gate has no origin and no mode by construction — an executor raises one
// without deciding who answers it or under which dial. Printed as `origin: ""` those sat
// beside a `reason: ""` that was a real drop, and nothing told the two apart. An absent key
// is a visible absence; an empty string is camouflage for a bug.
func TestGateListingOmitsAbsentFields(t *testing.T) {
	srv := testServer(t)
	b := broker.New()
	h := b.Spawn(task.Launch{ID: "p1", Prompt: "migrate the schema", Driver: "human"}, nil)
	srv.SetRunner(b)
	nonce := srv.HostBroker("", b.WorkerHandler())
	// The docket is a slice of the store, so what puts a task on it is the same thing
	// that makes it a member of a scope: the mirror. Spawning straight on the broker skips
	// the mirror, so attach one — otherwise the listing is empty and this test passes for
	// the wrong reason.
	mirror.Reflect(srv.Store, "p1", h, mirror.Meta{Runner: "broker"})

	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()
	work := httptest.NewServer(srv.BrokerHandler())
	defer work.Close()

	claim := postTony(t, work.URL+"/broker/"+nonce+"/tasks/p1/claim", map[string]any{"identity": "runner:scott"})
	var cl struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, claim, &cl)
	claim.Body.Close()

	rep := postTony(t, work.URL+"/broker/"+nonce+"/tasks/p1/report", map[string]any{
		"epoch": cl.Epoch,
		"events": []task.Frame{{
			Kind: task.EvPending, ID: "deploy-1", Gate: "deploy-approval", Text: "prod, off-hours",
		}},
	})
	rep.Body.Close()

	// The docket is a projection, so it settles rather than being written by the report that
	// caused it: the worker's frame travels to the mirror, which publishes the question. Poll
	// for it — asserting on the first read would be asserting on the race.
	var gates []map[string]any
	deadline := time.After(2 * time.Second)
	for len(gates) == 0 {
		resp, err := http.Get(ops.URL + "/gates")
		if err != nil {
			t.Fatalf("get gates: %v", err)
		}
		decodeResp(t, resp, &gates)
		resp.Body.Close()
		if len(gates) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the reported gate never reached the docket")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if len(gates) != 1 {
		t.Fatalf("gates = %v, want exactly the one raised", gates)
	}
	g := gates[0]

	// What the worker reported is present, including the reason a resolver reads to decide.
	for k, want := range map[string]string{
		"id": "deploy-1", "gate": "deploy-approval", "reason": "prod, off-hours",
		"task": "p1", "where": "task",
	} {
		if got, _ := g[k].(string); got != want {
			t.Errorf("gate[%q] = %q, want %q", k, got, want)
		}
	}
	// What it did not report is ABSENT, not blank — so a future drop reads as a missing
	// key rather than blending into the empty ones.
	for _, k := range []string{"origin", "mode", "tool", "subject"} {
		if v, present := g[k]; present {
			t.Errorf("gate[%q] = %#v, want the key absent — an unset field must not render as \"\"", k, v)
		}
	}
}
