package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// A worker raises a gate with outcomes of its own, and it is the same record a charter gate
// poses: the options are on it, the answer is by option, and the option chosen reaches the
// worker's inbox rather than a yes or no it cannot read as one.
func TestAWorkerRaisedGateCarriesItsOptionsAndTheChoiceComesBack(t *testing.T) {
	b := broker.New()
	b.Spawn(task.Launch{ID: "p", Prompt: "work", Driver: "human"}, nil)
	srv := testServer(t)
	srv.SetRunner(b)
	nonce := srv.HostBroker("", b.WorkerHandler())
	srv.reflect(srv.Store, "p", b.Attach("p"), mirror.Meta{Runner: b.Ref()})
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()
	work := httptest.NewServer(srv.BrokerHandler())
	defer work.Close()

	claim := postTony(t, work.URL+"/broker/"+nonce+"/tasks/p/claim", map[string]any{"identity": "human:w"})
	var cl struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, claim, &cl)
	claim.Body.Close()
	postTony(t, work.URL+"/broker/"+nonce+"/tasks/p/report", map[string]any{
		"epoch": cl.Epoch,
		"events": []task.Frame{{Kind: task.EvPending, ID: "p~d1", Gate: "rollout",
			Options:  []task.Option{{ID: "canary", Need: 1}, {ID: "full", Need: 2}, {ID: "abort", Need: 1}},
			Deadline: &task.Deadline{Within: "4h", Default: "abort"},
		}},
	}).Body.Close()
	var rec entity.Payload
	for range 400 {
		if e, ok, _ := srv.Store.Get(mirror.QuestionRef("p~d1")); ok {
			if _, _, at := mirror.DeadlineOf(e.Payload); at != "" {
				rec = e.Payload
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rec == nil {
		t.Fatal("the worker's question never reached the record with its deadline armed")
	}
	opts := mirror.OptionsOf(rec)
	if len(opts) != 3 || opts[1].ID != "full" || opts[1].Need != 2 {
		t.Fatalf("the record does not carry the worker's options: %+v", entity.ToObj(rec))
	}
	if within, dflt, _ := mirror.DeadlineOf(rec); within != "4h" || dflt != "abort" {
		t.Fatalf("the record does not carry the worker's deadline: %+v", entity.ToObj(rec))
	}

	// A yes is not an option here.
	bad := postTony(t, ops.URL+"/gate/resolve", api.Resolve{ID: "p~d1", Choice: "proceed", By: "human:lead"})
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("proceed on a three-option question = %s, want 400", bad.Status)
	}
	resp := postTony(t, ops.URL+"/gate/resolve", api.Resolve{ID: "p~d1", Choice: "canary", By: "human:lead"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("resolve = %s", resp.Status)
	}
	if got := question(t, srv, "p~d1"); got["verdict"] != "canary" {
		t.Fatalf("the record settled on %v, want canary: %+v", got["verdict"], got)
	}

	// The worker pulls its inbox and finds the option, not a boolean it would misread.
	inbox, err := http.Get(work.URL + "/broker/" + nonce + "/tasks/p/inbox?since=0&epoch=" + itoa(cl.Epoch))
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	var got struct {
		Items []api.InboxItem `tony:"field=items"`
	}
	decodeResp(t, inbox, &got)
	inbox.Body.Close()
	var found bool
	for _, it := range got.Items {
		if it.Kind == "resolve" && it.Decision == "p~d1" {
			found = true
			if it.Choice != "canary" || it.Proceed {
				t.Fatalf("the inbox carries %+v, want choice canary and not a yes", it)
			}
		}
	}
	if !found {
		t.Fatalf("the answer never reached the worker's inbox: %+v", got.Items)
	}
}
