package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/systems/systemstest"
	"github.com/signadot/verse/task"
)

// A REGISTRATION KEPT FROM AN OLDER VERSE IS ASKED AGAIN: one holding its offers under `offers`,
// as an older verse kept them, or a description of an older version, is no description this
// verse reads; a spawn naming the runner asks it afresh, and what it says replaces the old under
// `described`, `offers` gone.
func TestAStaleRegistrationIsAskedAgain(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	face, _ := offeringFace(t, ts.URL, "codex")
	if resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "code", "url": face.URL, "subject": face.Subject}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s", resp.Status)
	}
	ref := runnerEntityRef("code")
	for what, stale := range map[string]entity.Obj{
		"offers, as an older verse kept them": {"described": entity.Removed(), "offers": []any{map[string]any{"agent": "codex"}}},
		"a description of an older version":   {"described": map[string]any{"version": 1, "offers": []any{map[string]any{"script": true}}}},
	} {
		t.Run(what, func(t *testing.T) {
			if _, _, err := srv.Store.Write(entity.Write{At: ref.At(), Value: stale.Payload()}); err != nil {
				t.Fatal(err)
			}
			if e, _ := findRunner(srv.Store, "verse.runners.code"); e.Current() {
				t.Fatal("the stale registration reads as current")
			}
			spawnOK(t, ts, map[string]any{"spawn": map[string]any{"agent": map[string]any{"name": "codex"}, "prompt": "go", "runner": "verse.runners.code"}})
			e, ok := findRunner(srv.Store, "verse.runners.code")
			if !ok || !e.Current() || e.Description().Offers[0].Name() != "codex" {
				t.Fatalf("after the spawn the registration holds %+v, want the runner's description afresh", e.Described)
			}
			raw, _, _ := srv.Store.Get(ref)
			if entity.Get(raw.Payload, "offers") != nil {
				t.Errorf("the old `offers` field is still on the registration: %v", raw.Payload)
			}
		})
	}
}

// A SPAWN NAMING A MODEL ITS RUNNER'S AGENT DOES NOT TAKE IS REFUSED AT INSTALL, with the values
// the agent takes named.
func TestAModelTheAgentDoesNotTakeIsRefusedAtInstall(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	face, _ := offeringFace(t, ts.URL, "codex")
	if resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "code", "url": face.URL, "subject": face.Subject}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s", resp.Status)
	}
	install := func(name, model string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/charter", strings.NewReader(`
- name: `+name+`
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action:
    patch: {stage: started}
    spawn: {runner: verse.runners.code, agent: {name: codex, model: `+model+`}, prompt: go}
`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "text/tony")
		resp, err := sendAsTester(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, readBody(t, resp)
	}
	if code, body := install("small", "small"); code != http.StatusCreated {
		t.Errorf("a model the agent takes = %d %s", code, body)
	}
	if code, body := install("huge", "huge"); code == http.StatusCreated || !strings.Contains(body, `"huge" is none of small, large`) {
		t.Errorf("a model the agent does not take = %d %s, want it refused naming what it takes", code, body)
	}
	if code, body := install("bound", "$subject.value.model"); code != http.StatusCreated {
		t.Errorf("a model the firing binds = %d %s, want it installed: the launch is checked bound", code, body)
	}
}

// WHO RAN A TASK IS ON ITS RECORD, as `agent`, written as a spawn's is (its name beside its
// settings), and as what ran once the runner reports: the agent it resolved (its default, where
// the spawn named none) and the settings it ran with, its default model filled in. A script's
// record says `script: true` and names no agent.
func TestATasksAgentIsOnItsRecord(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	d := runnerd.NewInProcessHandler(func(l runner.Launch, reg *task.Registry) *task.Task {
		p := task.NewTask(l.ID, systemstest.Opened(task.NewSupervisor(l.Environment, l.Autonomy, reg), l.Environment), &oneAction{}, okExecutor{})
		if l.Script == "" {
			model := l.AgentSettings().Model()
			if model == "" {
				model = "large" // the runner's default, where the launch named none
			}
			p.SetAgent("codex", map[string]string{runner.SettingModel: model})
		}
		return p
	})
	d.Describe(offering("codex"))
	face := provenFace(t, ts.URL, d.HTTPHandler(), func() {})
	if resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "code", "url": face.URL, "subject": face.Subject}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s", resp.Status)
	}
	for _, c := range []struct {
		name  string
		spawn map[string]any
		agent map[string]any // nil: a script's record
	}{
		{"named, the default model", map[string]any{"agent": map[string]any{"name": "codex"}, "prompt": "go"}, map[string]any{"name": "codex", "model": "large"}},
		{"named, a model", map[string]any{"agent": map[string]any{"name": "codex", "model": "small"}, "prompt": "go"}, map[string]any{"name": "codex", "model": "small"}},
		{"the runner's default agent", map[string]any{"prompt": "go"}, map[string]any{"name": "codex", "model": "large"}},
		{"a script", map[string]any{"script": "true"}, nil},
	} {
		c.spawn["runner"] = "verse.runners.code"
		id := spawnOK(t, ts, map[string]any{"spawn": c.spawn})
		waitTerminal(t, srv.Store, id)
		rec := instanceOf(t, srv, id)
		if c.agent == nil {
			if rec["script"] != true || rec["agent"] != nil {
				t.Errorf("%s: the record says script %v, agent %v; want script: true and no agent", c.name, rec["script"], rec["agent"])
			}
			continue
		}
		got, _ := rec["agent"].(map[string]any)
		if len(got) != len(c.agent) || got["name"] != c.agent["name"] || got["model"] != c.agent["model"] {
			t.Errorf("%s: the record's agent %v, want %v", c.name, rec["agent"], c.agent)
		}
		if rec["doer"] != nil || rec["settings"] != nil {
			t.Errorf("%s: the record still carries doer %v or settings %v", c.name, rec["doer"], rec["settings"])
		}
	}
}

// A SPAWN'S AGENT GOES ON THE RECORD WITHOUT ITS ANNOTATIONS: they are the runner's plumbing, the
// virtual key's name among them, and the record says the credential apart.
func TestARecordsAgentHasNoAnnotations(t *testing.T) {
	a := recordAgent(&runner.Agent{Name: "verse", Settings: map[string]string{"model": "m"},
		Annotations: map[string]string{AnnotationVirtualKey: "vk-review"}})
	if a.Name != "verse" || a.Settings["model"] != "m" || a.Annotations != nil {
		t.Fatalf("recorded as %+v, want the name and the settings alone", a)
	}
	if recordAgent(nil) != nil {
		t.Error("no agent is recorded as none")
	}
}
