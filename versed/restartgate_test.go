package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger"
)

// docket reads `verse gate` off a server.
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

// A question whose task did not survive must leave the docket. The task is orphaned by
// reconcile — its in-process runtime died with the previous verse — and a question nobody can
// answer, listed as though somebody could, is worse than no listing: it asks a person to
// decide something that will never be read.
func TestARestartTakesADeadTasksQuestionWithIt(t *testing.T) {
	srv := testServer(t)
	entitytest.Set(t, srv.Store, mirror.Ref("run-1"), entity.Obj{
		"status": string(task.Running), "runner": "local",
	}.Payload())
	mirror.PostQuestion(srv.Store, task.Pending{
		Question: task.GateQuestion{ID: "run-1~d1", Origin: "run-1", Gate: "write"},
		Tool:     "write_file",
	}, "run-1")

	srv.reconcileTasks()

	if got := docket(t, srv); len(got) != 0 {
		t.Fatalf("the docket kept a dead task's question: %v", got)
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
// that outlived verse looks like: the worker never noticed the restart, and the task is
// still sitting on its gate waiting for somebody to decide.
func TestARestartKeepsASurvivingTasksQuestion(t *testing.T) {
	addr := entitytest.Addr(t)
	b := broker.New()
	b.Spawn(task.Launch{ID: "p1", Prompt: "migrate the schema", Driver: "human"}, nil)

	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	first.SetRunner(b)
	nonce := first.HostBroker("", b.WorkerHandler())
	h := b.Attach("p1")
	mirror.Reflect(first.Store, "p1", h, mirror.Meta{Runner: b.Ref()})

	work := httptest.NewServer(first.BrokerHandler())
	defer work.Close()
	claim := postTony(t, work.URL+"/broker/"+nonce+"/tasks/p1/claim", map[string]any{"identity": "runner:worker"})
	var cl struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, claim, &cl)
	claim.Body.Close()

	const decision = "p1~d1"
	rep := postTony(t, work.URL+"/broker/"+nonce+"/tasks/p1/report", map[string]any{
		"epoch":  cl.Epoch,
		"events": []task.Frame{{Kind: task.EvPending, ID: decision, Gate: "write", Text: "prod"}},
	})
	rep.Body.Close()
	waitDocket(t, first, decision)

	// The restart: a second server over the same store, with the same still-running broker.
	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.SetRunner(b)
	second.reconcileTasks()
	// The resume REPLAYS the worker's frames into the mirror, and the answer below must not
	// race that replay: a replayed pending frame landing after the answer can re-pose the
	// question against a precondition view that has not seen the answer yet, which is
	// git-issue nkptp9b3h12ksx3kg9n0 and not what this test is about. The replay's last
	// write is the pending frame's projection onto the task record, after the first write
	// of the resume replaced it, so that is what says the replay is done.
	replayed := func() bool {
		e, ok, _ := second.Store.Get(mirror.Ref("p1"))
		return ok && entity.Str(e.Payload, "status") == string(task.Running) &&
			entity.Str(e.Payload, "pending") == mirror.QuestionRef(decision).String()
	}
	for i := 0; !replayed(); i++ {
		if i > 400 {
			t.Fatal("the resumed task's frames were never replayed into the mirror")
		}
		time.Sleep(5 * time.Millisecond)
	}

	gates := docket(t, second)
	if len(gates) != 1 || gates[0]["id"] != decision {
		t.Fatalf("the docket after a restart = %v, want the question that outlived it", gates)
	}

	// And it is still answerable — a docket entry nobody can act on is a listing, not a
	// docket. The answer has to reach the worker that is actually blocked.
	ops := httptest.NewServer(second.Handler())
	defer ops.Close()
	resp := postTony(t, ops.URL+"/gate/resolve", api.Resolve{ID: decision, Choice: "proceed", By: "user:scott@example.com"})
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
	inbox, ok := b.TaskInbox("p1")
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
	if !delivered.Proceed || delivered.By != "user:scott@example.com" {
		t.Fatalf("the answer reached the worker garbled: %+v", delivered)
	}
}

// A question follows its task's fate rather than being judged on its own — including where
// that fate is a BELIEF. The runner package's Attach returns a handle with no reachability
// check, so a remote-hosted task is re-adopted whether or not the remote is still there,
// and it stays `running` on that belief; its question stays pending on the same belief.
//
// The optimism is pre-existing (see TestReconcileKeepsClaimsForResumedRuns, where the run
// claim follows the same rule). What this pins is that the question does not diverge from it:
// a docket that orphaned questions the reconcile believed alive would drop answerable ones,
// and one that kept questions the reconcile orphaned would list unanswerable ones. Either way
// the docket and the fleet would be telling the operator two different things.
func TestAQuestionFollowsItsTasksFate(t *testing.T) {
	srv := testServer(t)
	entitytest.Set(t, srv.Store, mirror.Ref("remote-1"), entity.Obj{
		"status": string(task.Running),
		"runner": "http://elsewhere.invalid", // remote → re-attached, believed alive
	}.Payload())
	mirror.PostQuestion(srv.Store, task.Pending{
		Question: task.GateQuestion{ID: "remote-1~d1", Origin: "remote-1", Gate: "write"},
	}, "remote-1")

	srv.reconcileTasks()

	e, _, _ := srv.Store.Get(mirror.QuestionRef("remote-1~d1"))
	if got := entity.ToObj(e.Payload); got["status"] != mirror.QuestionPending {
		t.Fatalf("status = %v, want still pending — the task was believed alive: %+v",
			got["status"], got)
	}
}

// The firing case, deterministically: a question no task raised is orphaned by a restart,
// whatever the store still says about it. The round trip below then shows it coming back —
// but only this pins that it went away, since a docket that never settled anything would pass
// that test by never having emptied.
func TestARestartOrphansAFiringsQuestion(t *testing.T) {
	srv := testServer(t)
	mirror.PostQuestion(srv.Store, task.Pending{
		Question: task.GateQuestion{ID: "verse.trigger.guard.approval~1", Origin: "guard", Gate: "approval"},
	}, "") // no task: a firing posed it, before anything spawned

	srv.reconcileTasks()

	e, _, _ := srv.Store.Get(mirror.QuestionRef("verse.trigger.guard.approval~1"))
	got := entity.ToObj(e.Payload)
	if got["status"] != mirror.QuestionOrphaned {
		t.Fatalf("status = %v, want orphaned — the firing that waited on it died with us: %+v",
			got["status"], got)
	}
	if len(docket(t, srv)) != 0 {
		t.Fatal("the orphaned question is still on the docket")
	}
}

// A firing's question is a different case from a task's, and the one easy to miss: the
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
		Condition: trigger.Condition{Subject: matchAt("demo.pr.*", map[string]any{"stage": "open"})},
		Gates:     []trigger.Gate{{Name: "approval", Mode: "ask"}},
		Action:    trigger.Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
	}

	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	if err := first.Eng.AddTrigger(rule); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, first.Store, entity.RefAt("demo", "pr", "1"),
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
	resp := postTony(t, ops.URL+"/gate/resolve", api.Resolve{ID: posed, Choice: "proceed", By: "user:scott@example.com"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("answering the re-posed question = %s, want 204 — it is listed but nothing is waiting", resp.Status)
	}
}

// waitDocket waits until id is listed on a server's docket.
func waitDocket(t *testing.T, srv *Server, id string) {
	t.Helper()
	// Bounded by TIME, not by a count of reads: how many reads fit in the wait depends on how busy
	// the machine is, and under the full suite a count ran out before the question was posted.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, g := range docket(t, srv) {
			if g["id"] == id {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never reached the docket", id)
}

// A task that ends leaves no question on the docket, whichever way it ended.
//
// Only one of the two ways settled them. A local task ends by emitting an EvDecision for
// its open gate, which the mirror settles; a brokered one latches terminal and emits only an
// EvStatus — so the docket went on offering a question for a run that was over, and answering
// it reported "no such question".
func TestABrokeredTaskEndingSettlesItsQuestions(t *testing.T) {
	b := broker.New()
	b.Spawn(task.Launch{ID: "p", Prompt: "work", Driver: "human"}, nil)
	srv := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, entitytest.Addr(t)))
	srv.SetRunner(b)
	nonce := srv.HostBroker("", b.WorkerHandler())
	mirror.Reflect(srv.Store, "p", b.Attach("p"), mirror.Meta{Runner: b.Ref()})
	work := httptest.NewServer(srv.BrokerHandler())
	defer work.Close()

	claim := postTony(t, work.URL+"/broker/"+nonce+"/tasks/p/claim", map[string]any{"identity": "runner:w"})
	var cl struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, claim, &cl)
	claim.Body.Close()
	postTony(t, work.URL+"/broker/"+nonce+"/tasks/p/report", map[string]any{
		"epoch":  cl.Epoch,
		"events": []task.Frame{{Kind: task.EvPending, ID: "p~d1", Gate: "write"}},
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
		t.Fatalf("the docket still offers %v for a task that ended", got)
	}
	e, _, _ := srv.Store.Get(mirror.QuestionRef("p~d1"))
	if got := entity.ToObj(e.Payload); got["status"] != mirror.QuestionOrphaned {
		t.Fatalf("question status = %v, want orphaned — nobody answered it and nobody now can: %+v",
			got["status"], got)
	}
}

// A RESUMED task is covered by whoever was covering it.
//
// The resolver its spawn named is on the entity, so a restart reads it back rather than
// remembering it — and the standing layer is consulted for its questions exactly as it was
// before. Neither survived the first cut of this: reconcile rebuilt its own Meta by hand and
// dropped both, silently, which is what an optional field on a struct three call sites
// construct will do.
func TestAResumedTaskKeepsItsResolver(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	const resolverRef = "verse.resolvers.security-review"
	entitytest.Set(t, first.Store, mirror.Ref("p"), entity.Obj{
		"status": string(task.Running), "runner": "http://elsewhere.invalid",
		"flow": `change.pr."1"`, "resolver": resolverRef,
	}.Payload())

	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.reconcileTasks()

	e, _, _ := second.Store.Get(mirror.Ref("p"))
	if got := entity.Str(e.Payload, "resolver"); got != resolverRef {
		t.Fatalf("a resumed task is covered by %q, want the resolver it was spawned with", got)
	}
}
