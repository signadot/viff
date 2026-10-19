package versed

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// slideGates is `verse task policy <id>` as tester: the body, a PUT to the task's policy.
func slideGates(t *testing.T, url, id, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url+"/tasks/"+id+"/policy", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/tony")
	resp, err := sendAsTester(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("slide = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
}

// A SLID ROW IS ASKED UNDER THE CHARTER'S CLOCK: `task policy` sets verse's part of the new
// policy too (what silence settles on, the labels), and the task's next question is posed under
// it, not under the policy it was spawned with. `task get` and `ps` show the policy as it now
// stands, verse's part with it, and who replaced it.
func TestASlidGateIsAskedWithItsDefaultAndLabels(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := NewWithStore(func(l runner.Launch, reg *task.Registry) *task.Task {
		sup := task.NewSupervisor(l.Policy, reg)
		sup.SetStepOf(commandStep)
		return task.NewTask(l.ID, sup, &blockedDriver{release: release}, okExecutor{})
	}, entitytest.Store(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	t.Cleanup(func() {
		if tk := srv.Embedded.Lookup("slid"); tk != nil {
			tk.Cancel("the test is over", "test")
		}
	})
	resp := postTony(t, ts.URL+"/tasks", map[string]any{"id": "slid", "doer": map[string]any{"script": "true"},
		"policy": []map[string]any{{"match": map[string]any{"system": "commands", "verb": "run"}, "then": "allow"}, {"then": "allow"}}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spawn = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	slideGates(t, ts.URL, "slid", `{policy: [{name: hold, match: {system: commands, verb: run},
	  then: {ask: {within: 1h, default: deny, labels: {slack: C9}}}}, {then: allow}]}`)
	qid := waitQuestion(t, srv, "slid")
	rec, _, _ := srv.Store.Get(mirror.QuestionRef(qid))
	if _, dflt, _ := mirror.DeadlineOf(rec.Payload); dflt != task.Deny {
		t.Errorf("the slid gate's question settles on %q, want its deny", dflt)
	}
	if mirror.LabelsOf(rec.Payload)["slack"] != "C9" {
		t.Errorf("the slid gate's question says %v about itself, want its labels", mirror.LabelsOf(rec.Payload))
	}
	shows := func(what string, tk api.Task) {
		t.Helper()
		if len(tk.Policy) != 2 || tk.Policy[0].Name != "hold" || tk.Policy[0].Then.Ask == nil ||
			tk.Policy[0].Then.Ask.Default != task.Deny || tk.Policy[1].Then.Mode != task.ThenAllow ||
			tk.PolicyBy != "user:"+tester {
			t.Errorf("%s shows the policy %v by %q, want the slid one, its default with it, by the requester", what, tk.Policy, tk.PolicyBy)
		}
	}
	// The question can be posed before the slide reaches the record, which is the mirror's to
	// write as the task's events arrive.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if e, ok, _ := srv.Store.Get(mirror.Ref("slid")); ok && entity.Str(e.Payload, "policyBy") != "" {
			break
		}
	}
	var one api.Task
	decodeTony(t, getAs(t, "user:"+tester, ts.URL+"/tasks/slid"), &one)
	shows("task get", one)
	var all []api.Task
	decodeTony(t, getAs(t, "user:"+tester, ts.URL+"/tasks"), &all)
	if i := slices.IndexFunc(all, func(tk api.Task) bool { return tk.ID == "slid" }); i < 0 {
		t.Errorf("ps lists %v, without the task", all)
	} else {
		shows("ps", all[i])
	}
}

// A SLIDE IS THE REQUESTER'S, on a task a runner holds: the runner reports the policy as it
// relays it, and the record says who slid it.
func TestASlideOfGatesAloneIsTheRequestersOnARelayedTask(t *testing.T) {
	srv := testServer(t)
	face := runnerFace(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL}).Body.Close()
	id := spawnOK(t, ts, map[string]any{"id": "far", "doer": map[string]any{"agent": "fake"}, "prompt": "t", "runner": "verse.runners.gpu",
		"policy": []map[string]any{{"match": map[string]any{"system": "commands", "verb": "run"}, "then": "ask"}, {"then": "ask"}}})
	slideGates(t, ts.URL, id, `{policy: [{name: g, match: {system: commands, verb: run}, then: ask}, {then: ask}]}`)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok, _ := srv.Store.Get(mirror.Ref(id)); ok && entity.Str(e.Payload, "policyBy") != "" {
			if got := entity.Str(e.Payload, "policyBy"); got != "user:"+tester {
				t.Fatalf("policyBy = %q, want the requester", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the slide never reached the task's record")
}

// A POLICY REPLACED BY NONE IS THE DEFAULT, every call asking, which is what the runner runs a
// task with no policy under: the record says so, rather than the policy it was spawned with.
func TestAPolicyReplacedByNoneIsTheDefault(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := NewWithStore(func(l runner.Launch, reg *task.Registry) *task.Task {
		sup := task.NewSupervisor(l.Policy, reg)
		sup.SetStepOf(commandStep)
		return task.NewTask(l.ID, sup, &blockedDriver{release: release}, okExecutor{})
	}, entitytest.Store(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	t.Cleanup(func() {
		if tk := srv.Embedded.Lookup("none"); tk != nil {
			tk.Cancel("the test is over", "test")
		}
	})
	resp := postTony(t, ts.URL+"/tasks", map[string]any{"id": "none", "doer": map[string]any{"script": "true"},
		"policy": []map[string]any{{"then": "allow"}}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spawn = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	slideGates(t, ts.URL, "none", `{policy: []}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		e, _, _ := srv.Store.Get(mirror.Ref("none"))
		p := mirror.PolicyOf(e.Payload)
		if entity.Str(e.Payload, "policyBy") != "" && len(p) == 1 && p[0].Then.Mode == task.ThenAsk {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the record's policy is %v by %q, want the default, every call asking", p, entity.Str(e.Payload, "policyBy"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
