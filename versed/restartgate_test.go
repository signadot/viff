package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
	"github.com/signadot/verse/trigger"
)

// docket reads `verse gates` off a server.
func docket(t *testing.T, srv *Server) []map[string]any {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/gates")
	if err != nil {
		t.Fatalf("get gates: %v", err)
	}
	defer resp.Body.Close()
	var gates []map[string]any
	decodeResp(t, resp, &gates)
	return gates
}

// A question whose process did not survive must leave the docket. The process is orphaned by
// reconcile — its in-process runtime died with the previous verse — and a question nobody can
// answer, listed as though somebody could, is worse than no listing: it asks a person to
// decide something that will never be read.
func TestARestartTakesADeadProcessesQuestionWithIt(t *testing.T) {
	srv := testServer(t)
	entitytest.Set(t, srv.Store, mirror.Ref("run-1"), entity.Obj{
		"status": string(process.Running), "runner": "local",
	}.Payload())
	mirror.PostQuestion(srv.Store, process.Pending{
		Question: process.GateQuestion{ID: "run-1~d1", Origin: "run-1", Gate: "write"},
		Tool:     "write_file",
	}, "run-1")

	srv.reconcileProcesses()

	if got := docket(t, srv); len(got) != 0 {
		t.Fatalf("the docket kept a dead process's question: %v", got)
	}
	// The record stays, settled rather than deleted: what was asked and why it was never
	// answered is the trail, and an entry that vanishes is one nobody can account for.
	e, ok, _ := srv.Store.Get(mirror.QuestionRef("run-1~d1"))
	if !ok {
		t.Fatal("the question was deleted rather than settled")
	}
	if got := entity.ToObj(e.Payload); got["status"] != mirror.QuestionOrphaned {
		t.Fatalf("status = %v, want orphaned: %+v", got["status"], got)
	}
}

// And the positive: work that OUTLIVED verse keeps its question, and answering it still
// reaches the worker.
//
// The broker here is created outside both servers and handed to each, which is what a broker
// that outlived verse looks like: the worker never noticed the restart, and the process is
// still sitting on its gate waiting for somebody to decide.
func TestARestartKeepsASurvivingProcessesQuestion(t *testing.T) {
	addr := entitytest.Addr(t)
	b := broker.New()
	b.Spawn(process.Launch{ID: "p1", Task: "migrate the schema", Driver: "human"}, nil)

	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	first.SetRunner(b)
	nonce := first.HostBroker("", b.WorkerHandler())
	h := b.Attach("p1")
	mirror.Reflect(first.Store, "p1", h, mirror.Meta{Runner: b.Ref()})

	work := httptest.NewServer(first.BrokerHandler())
	defer work.Close()
	claim := postTony(t, work.URL+"/broker/"+nonce+"/processes/p1/claim", map[string]any{"identity": "human:worker"})
	var cl struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, claim, &cl)
	claim.Body.Close()

	const decision = "p1~d1"
	rep := postTony(t, work.URL+"/broker/"+nonce+"/processes/p1/report", map[string]any{
		"epoch":  cl.Epoch,
		"events": []process.Frame{{Kind: process.EvPending, ID: decision, Gate: "write", Text: "prod"}},
	})
	rep.Body.Close()
	waitDocket(t, first, decision)

	// The restart: a second server over the same store, with the same still-running broker.
	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.SetRunner(b)
	second.reconcileProcesses()

	gates := docket(t, second)
	if len(gates) != 1 || gates[0]["id"] != decision {
		t.Fatalf("the docket after a restart = %v, want the question that outlived it", gates)
	}

	// And it is still answerable — a docket entry nobody can act on is a listing, not a
	// docket. The answer has to reach the worker that is actually blocked.
	ops := httptest.NewServer(second.Handler())
	defer ops.Close()
	resp := postTony(t, ops.URL+"/gate/resolve", api.Resolve{ID: decision, Approve: true, By: "human:scott"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("answering after a restart = %s, want 204", resp.Status)
	}
	if got := docket(t, second); len(got) != 0 {
		t.Fatalf("the question stayed on the docket after being answered: %v", got)
	}

	// Reaching the worker is the claim, so read the worker's own inbox rather than trusting
	// the 204. A route that accepted the answer and delivered it nowhere would pass every
	// assertion above and leave the person at the terminal blocked forever.
	inbox, ok := b.ProcessInbox("p1")
	if !ok {
		t.Fatal("no worker inbox")
	}
	var delivered *api.InboxItem
	for i, it := range inbox {
		if it.Kind == "resolve" && it.Decision == decision {
			delivered = &inbox[i]
		}
	}
	if delivered == nil {
		t.Fatalf("the answer never reached the worker: %+v", inbox)
	}
	if !delivered.Proceed || delivered.By != "human:scott" {
		t.Fatalf("the answer reached the worker garbled: %+v", delivered)
	}
}

// A question follows its process's fate rather than being judged on its own — including where
// that fate is a BELIEF. The runner package's Attach returns a handle with no reachability
// check, so a remote-hosted process is re-adopted whether or not the remote is still there,
// and it stays `running` on that belief; its question stays pending on the same belief.
//
// The optimism is pre-existing (see TestReconcileKeepsClaimsForResumedRuns, where the run
// claim follows the same rule). What this pins is that the question does not diverge from it:
// a docket that orphaned questions the reconcile believed alive would drop answerable ones,
// and one that kept questions the reconcile orphaned would list unanswerable ones. Either way
// the docket and the fleet would be telling the operator two different things.
func TestAQuestionFollowsItsProcessesFate(t *testing.T) {
	srv := testServer(t)
	entitytest.Set(t, srv.Store, mirror.Ref("remote-1"), entity.Obj{
		"status": string(process.Running),
		"runner": "http://elsewhere.invalid", // remote → re-attached, believed alive
	}.Payload())
	mirror.PostQuestion(srv.Store, process.Pending{
		Question: process.GateQuestion{ID: "remote-1~d1", Origin: "remote-1", Gate: "write"},
	}, "remote-1")

	srv.reconcileProcesses()

	e, _, _ := srv.Store.Get(mirror.QuestionRef("remote-1~d1"))
	if got := entity.ToObj(e.Payload); got["status"] != mirror.QuestionPending {
		t.Fatalf("status = %v, want still pending — the process was believed alive: %+v",
			got["status"], got)
	}
}

// The firing case, deterministically: a question no process raised is orphaned by a restart,
// whatever the store still says about it. The round trip below then shows it coming back —
// but only this pins that it went away, since a docket that never settled anything would pass
// that test by never having emptied.
func TestARestartOrphansAFiringsQuestion(t *testing.T) {
	srv := testServer(t)
	mirror.PostQuestion(srv.Store, process.Pending{
		Question: process.GateQuestion{ID: "trigger:guard:approval~1", Origin: "guard", Gate: "approval"},
	}, "") // no process: a firing posed it, before anything spawned

	srv.reconcileProcesses()

	e, _, _ := srv.Store.Get(mirror.QuestionRef("trigger:guard:approval~1"))
	got := entity.ToObj(e.Payload)
	if got["status"] != mirror.QuestionOrphaned {
		t.Fatalf("status = %v, want orphaned — the firing that waited on it died with us: %+v",
			got["status"], got)
	}
	if len(docket(t, srv)) != 0 {
		t.Fatal("the orphaned question is still on the docket")
	}
}

// A firing's question is a different case from a process's, and the one easy to miss: the
// thing waiting on the answer was a goroutine in the verse, so a restart ends it whatever the
// store still says. Left pending, the docket would list a question whose answer nothing could
// receive — and answering it returns 404, because the registry that held it is empty.
//
// It comes back by being ASKED AGAIN. The rule is reinstalled, replays over what already
// matches, and poses the same question under the same id — which is what a deterministic gate
// id buys and what makes settling the stale one safe.
func TestARestartReposesAFiringsQuestion(t *testing.T) {
	addr := entitytest.Addr(t)
	rule := trigger.Trigger{
		Name:      "guard",
		Condition: trigger.Condition{Subject: trigger.Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Gates:     []trigger.Gate{{Name: "approval", Mode: "ask"}},
		Action:    trigger.Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
	}

	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	if err := first.Eng.AddTrigger(rule); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, first.Store, entity.Ref{System: "demo", Kind: "pr", ID: "1"},
		entity.Obj{"stage": "open"}.Payload())
	var posed string
	for range 400 {
		if g := docket(t, first); len(g) == 1 {
			posed, _ = g[0]["id"].(string)
			break
		}
	}
	if posed == "" {
		t.Fatal("the rule never posed its gate")
	}
	stop() // the verse goes down, and the firing waiting on that answer goes with it

	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())

	// Same id, and answerable: the replay re-posed it, so the record the reconcile settled
	// is pending again with something actually waiting on it this time.
	var back bool
	for range 400 {
		for _, g := range docket(t, second) {
			if g["id"] == posed {
				back = true
			}
		}
		if back {
			break
		}
	}
	if !back {
		t.Fatalf("the question did not come back under its own id (%s); docket = %v", posed, docket(t, second))
	}

	ops := httptest.NewServer(second.Handler())
	defer ops.Close()
	resp := postTony(t, ops.URL+"/gate/resolve", api.Resolve{ID: posed, Approve: true, By: "human:scott"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("answering the re-posed question = %s, want 204 — it is listed but nothing is waiting", resp.Status)
	}
}

// waitDocket waits until id is listed on a server's docket.
func waitDocket(t *testing.T, srv *Server, id string) {
	t.Helper()
	for range 400 {
		for _, g := range docket(t, srv) {
			if g["id"] == id {
				return
			}
		}
	}
	t.Fatalf("%s never reached the docket", id)
}

// A process that ends leaves no question on the docket, whichever way it ended.
//
// Only one of the two ways settled them. A local process ends by emitting an EvDecision for
// its open gate, which the mirror settles; a brokered one latches terminal and emits only an
// EvStatus — so the docket went on offering a question for a run that was over, and answering
// it reported "no such question".
func TestABrokeredProcessEndingSettlesItsQuestions(t *testing.T) {
	b := broker.New()
	b.Spawn(process.Launch{ID: "p", Task: "work", Driver: "human"}, nil)
	srv := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, entitytest.Addr(t)))
	srv.SetRunner(b)
	nonce := srv.HostBroker("", b.WorkerHandler())
	mirror.Reflect(srv.Store, "p", b.Attach("p"), mirror.Meta{Runner: b.Ref()})
	work := httptest.NewServer(srv.BrokerHandler())
	defer work.Close()

	claim := postTony(t, work.URL+"/broker/"+nonce+"/processes/p/claim", map[string]any{"identity": "human:w"})
	var cl struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, claim, &cl)
	claim.Body.Close()
	postTony(t, work.URL+"/broker/"+nonce+"/processes/p/report", map[string]any{
		"epoch":  cl.Epoch,
		"events": []process.Frame{{Kind: process.EvPending, ID: "p~d1", Gate: "write"}},
	}).Body.Close()
	waitDocket(t, srv, "p~d1")

	if err := b.Attach("p").Cancel("operator changed their mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	for range 400 {
		if len(docket(t, srv)) == 0 {
			break
		}
	}
	if got := docket(t, srv); len(got) != 0 {
		t.Fatalf("the docket still offers %v for a process that ended", got)
	}
	e, _, _ := srv.Store.Get(mirror.QuestionRef("p~d1"))
	if got := entity.ToObj(e.Payload); got["status"] != mirror.QuestionOrphaned {
		t.Fatalf("question status = %v, want orphaned — nobody answered it and nobody now can: %+v",
			got["status"], got)
	}
}
