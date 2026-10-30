package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/systems/systemstest"
	"github.com/signadot/verse/task"
)

// askingServer runs local tasks whose one action is a step, so a spawn that names no autonomy
// blocks on it and there is a real task gate to answer.
func askingServer(t *testing.T) *Server {
	t.Helper()
	srv := serverWith(t, askingBuilder(t), entitytest.Store(t))
	t.Cleanup(func() { checkAskRecords(t, srv.Store) })
	return srv
}

// askingBuilder is askingServer's half that is not the store, for a test that wraps one.
//
// The verse runner's script is one action and no step: it runs whatever the autonomy says, and
// what asks is a call it makes. A test about the task's own gate wants a step, so the task
// proposes one (stepBuilder), under the gating the launch carries.
func askingBuilder(t *testing.T) runnerd.Builder {
	t.Helper()
	return stepBuilder(t)
}

// waitQuestion waits for a pending question raised by taskID and returns its id.
func waitQuestion(t *testing.T, srv *Server, taskID string) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, ent := range entity.Below(srv.Store, mirror.IsQuestion, 1, entity.VerseRoot, mirror.AskSystem) {
			if mirror.TaskFrom(ent.Payload) == taskID && entity.Str(ent.Payload, "status") == mirror.QuestionPending {
				return ent.Ref.Name()
			}
		}
		select {
		case <-deadline:
			t.Fatalf("%s never posed a question", taskID)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// The LOCAL leg of `POST /tasks/{id}/resolve`, which had no test.
//
// It is the leg that used to be forwarded rather than handled, to a runner face that read a
// body of its own, so verse's answer arrived as an empty decision id and answered nothing. The
// relayed leg was covered and the local one was not, which is exactly why the difference could
// sit there.
//
// What this pins is that both legs of this route speak one body. `verse task approve` on a
// local task must answer it, and the task must go on.
func TestTaskResolveAnswersALocalGate(t *testing.T) {
	srv := askingServer(t)
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()

	spawn := postTony(t, ops.URL+"/tasks", api.Spawn{ID: "local-1", Spawn: launch.Spec{Script: "true", Environment: stepEnv, Autonomy: stepAsks}})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	qid := waitQuestion(t, srv, "local-1")

	resp := postTony(t, ops.URL+"/tasks/local-1/resolve",
		api.Resolve{ID: qid, Choice: "proceed", By: "user:scott@example.com"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("resolving a LOCAL task gate = %s, want 204 — the body did not reach the decision", resp.Status)
	}

	// And it actually released the task, rather than merely being accepted. A 204 from a
	// route that answered nothing is the failure this test exists to catch.
	deadline := time.After(3 * time.Second)
	for {
		e, _, _ := srv.Store.Get(mirror.QuestionRef(qid))
		if entity.ToObj(e.Payload)["status"] == mirror.QuestionAnswered {
			return
		}
		select {
		case <-deadline:
			e, _, _ := srv.Store.Get(mirror.QuestionRef(qid))
			t.Fatalf("the question never settled: %+v", entity.ToObj(e.Payload))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A run is gated by the steps it takes: a gate over `commands.run` asks before a command runs,
// and `verse task run --gates` is how an ad-hoc run gets one.
func TestARunAsksAtAGateOverItsStep(t *testing.T) {
	srv := serverWith(t, stepBuilder(t), entitytest.Store(t))
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()

	spawn := postTony(t, ops.URL+"/tasks", api.Spawn{ID: "shell-1", Spawn: launch.Spec{Script: "true", Environment: stepEnv, Autonomy: task.Autonomy{{Name: "shell", Match: &task.Match{System: "commands", Verb: "run"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAllow}}}}})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	qid := waitQuestion(t, srv, "shell-1")
	e, _, _ := srv.Store.Get(mirror.QuestionRef(qid))
	if gate := entity.ToObj(e.Payload)["gate"]; gate != "shell" {
		t.Fatalf("the question is gate %v, want shell: %+v", gate, entity.ToObj(e.Payload))
	}
}

// A HOSTED TASK'S SAY AND VECTOR ARE BY WHOEVER THE REQUEST IS MADE AS, as a relayed task's are.
// Handed to the runtime's own handler as the request stands, the body's `by` is what the task
// records: asked for control as one party and written down as another, `system:docket` included.
// And a spawn is answerable to whoever asked for it, since that name travels onto the commits a
// runner delivers.
func TestAHostedTasksControlIsRecordedAsItsRequester(t *testing.T) {
	srv := askingServer(t)
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()

	spawn := postTony(t, ops.URL+"/tasks", api.Spawn{ID: "local-2", Spawn: launch.Spec{Script: "true", Environment: stepEnv, Autonomy: stepAsks}})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	waitQuestion(t, srv, "local-2") // held at its gate, so it is hosted and live
	send := func(method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, ops.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/tony")
		resp, err := sendAsTester(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	for name, resp := range map[string]*http.Response{
		"vector": send(http.MethodPut, "/tasks/local-2/autonomy", `{autonomy: [{then: ask}], by: "system:docket"}`),
		"say":    send(http.MethodPost, "/tasks/local-2/say", `{instruction: "hello", by: "user:ceo@example.com"}`),
		"spawn":  send(http.MethodPost, "/tasks", `{id: local-3, script: "true", principal: "system:charter"}`),
	} {
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s by somebody else: %s, want 400", name, resp.Status)
		}
	}
	// As itself, named or not, each is done — and the record says who slid the policy.
	for name, resp := range map[string]*http.Response{
		"vector": send(http.MethodPut, "/tasks/local-2/autonomy", `{autonomy: [{then: ask}]}`),
		"say":    send(http.MethodPost, "/tasks/local-2/say", `{instruction: "hello", by: "`+tester+`"}`),
	} {
		resp.Body.Close()
		want := http.StatusNoContent
		if name == "say" {
			want = http.StatusOK // an ask to the task, answered with its id
		}
		if resp.StatusCode != want {
			t.Errorf("%s as the requester: %s, want %d", name, resp.Status, want)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok, _ := srv.Store.Get(mirror.Ref("local-2")); ok && entity.Str(e.Payload, "autonomyBy") != "" {
			if got := entity.Str(e.Payload, "autonomyBy"); got != "user:"+tester {
				t.Errorf("autonomyBy = %q, want the requester", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("the policy's slide never reached the task's record")
}

// stepBuilder hosts a task that proposes one command, classified as a `commands.run` step, so a
// gate on that step has something to ask about. The verse runner's own script is one action and
// no step, so a test about a step's gate builds its task here.
// stepAsks is the policy a test of a task's own gate spawns under: stepBuilder's action is a
// command, and the policy gives the task the shell and asks before each, as `autonomy[0]`.
var stepAsks = task.Autonomy{{Match: &task.Match{System: "commands"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAsk}}}

// stepAsksTony is stepAsks as a spawn writes it.
const stepAsksTony = `[{match: {system: commands}, then: ask}, {then: ask}]`

// openNet is the network a test's task runs on: the host's, which the test server's local runner
// builds.
var openNet = systems.Environment{Systems: systems.Systems{"network": {{Attributes: map[string]string{"mode": runner.NetworkOpen}}}}}

// openEnv is an environment that gives the open network alone, as a spawn built of maps writes it.
var openEnv = map[string]any{"systems": map[string]any{"network": map[string]any{"mode": runner.NetworkOpen}}}

// stepEnv is the environment a test of a task's own gate spawns in: stepBuilder's action is a
// command, which is the task's only where the environment gives it, on the open network.
// stepEnvTony is it as a spawn writes it.
var stepEnv = systems.Environment{Systems: systems.Systems{
	"commands": {{Verbs: map[string][]systems.Shape{"run": {{}}}}},
	"network":  openNet.Systems["network"],
}}

const stepEnvTony = `{systems: {commands: {run: {}}, network: {mode: open}}}`

func stepBuilder(t *testing.T) runnerd.Builder {
	t.Helper()
	return func(l runner.Launch, reg *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(l.Environment, l.Autonomy, reg), l.Environment)
		sup.SetStepOf(commandStep)
		return task.NewTask(l.ID, sup, &oneAction{}, okExecutor{})
	}
}
