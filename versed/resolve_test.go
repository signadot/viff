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
	"github.com/signadot/verse/process"
)

// The resolve body is one type for both routes (api.Resolve), and these are the tests
// that keep it that way. They exist because the two routes previously carried two
// vocabularies — `{id, approve}` here, `{decisionId, proceed}` on the process route —
// and nothing noticed: no test posted either body, so the drift was invisible to
// everything except a person reading both handlers at once.
//
// Posting the SHARED type is the point. A field renamed in api.Resolve now fails to
// compile here and at every call site, instead of decoding to a zero value and
// answering "unknown or already-answered decision" at runtime.

// postTony sends v as tony to path and returns the response.
func postTony(t *testing.T, url string, v any) *http.Response {
	t.Helper()
	b, err := codec.Marshal(v, codec.Tony)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/tony", strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

// An engine gate posted to the registry is answered through POST /gate/resolve carrying
// api.Resolve — the id names the gate, approve carries the answer, and say rides along
// as steering.
func TestGateResolveAcceptsAPIResolve(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Raise a gate the way the engine does, and block on it as the engine would.
	const id = "trigger:review:review~1"
	answered := make(chan process.Resolution, 1)
	go func() {
		res, _ := srv.Reg.Resolve(context.Background(),
			process.GateQuestion{ID: id, Origin: "review", Gate: "review", Mode: process.Ask},
			process.ToolUse{Name: "trigger:review"})
		answered <- res
	}()
	waitPending(t, srv, id)

	resp := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: id, Approve: true, Say: []string{"ship it"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("gate resolve: status = %s, want 204", resp.Status)
	}

	select {
	case res := <-answered:
		if !res.Proceed {
			t.Error("gate resolved to deny; the approve field did not arrive")
		}
		if len(res.Steer) != 1 || res.Steer[0] != "ship it" {
			t.Errorf("steer = %v, want [ship it] — the say field did not arrive", res.Steer)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gate never resolved: the posted body did not decode into api.Resolve")
	}
}

// The unification regression, on a REAL pending decision: a body naming its gate `id`
// and its answer `approve` must resolve it on the process route too.
//
// It has to be a real one. Against the old handler this same body decoded to an empty
// decision id, and an empty id looks up nothing — so every cheaper assertion (400? 404?
// 501?) comes out identical whether the body decoded or not. Only a decision that is
// actually open distinguishes "answered it" from "silently answered nothing".
func TestProcessResolveAcceptsAPIResolve(t *testing.T) {
	srv := testServer(t)
	b := broker.New()
	b.Spawn("p1", "review PR #7", "human", nil)
	srv.SetRunner(b) // the resolve route relays to a worker only under a broker runner
	nonce := srv.HostBroker("", b.WorkerHandler())

	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()
	work := httptest.NewServer(srv.BrokerHandler())
	defer work.Close()

	// A worker claims the process and reports a gate it is blocked on — which is how a
	// decision becomes open and resolvable.
	claim := postTony(t, work.URL+"/broker/"+nonce+"/processes/p1/claim", map[string]any{"identity": "human:scott"})
	var cl struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, claim, &cl)
	claim.Body.Close()

	const decision = "p1~d1"
	rep := postTony(t, work.URL+"/broker/"+nonce+"/processes/p1/report", map[string]any{
		"epoch":  cl.Epoch,
		"events": []process.Frame{{Kind: process.EvPending, ID: decision, Gate: "write"}},
	})
	rep.Body.Close()

	// Now answer it the way `verse process resolve` does.
	resp := postTony(t, ops.URL+"/processes/p1/resolve", api.Resolve{ID: decision, Approve: true})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("process resolve = %s, want 204 — api.Resolve did not reach the decision (the routes have drifted apart again)", resp.Status)
	}

	// And it is gone: a second answer finds nothing open, which is what proves the first
	// one landed on the named decision rather than on an empty id.
	again := postTony(t, ops.URL+"/processes/p1/resolve", api.Resolve{ID: decision, Approve: true})
	defer again.Body.Close()
	if again.StatusCode != http.StatusNotFound {
		t.Errorf("re-resolve = %s, want 404 (already answered)", again.Status)
	}
}

func waitPending(t *testing.T, srv *Server, id string) {
	t.Helper()
	for range 100 {
		for _, p := range srv.Reg.Pending() {
			if p.Question.ID == id {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("gate %s never became pending", id)
}
