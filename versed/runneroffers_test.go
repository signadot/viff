package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/systems/systemstest"
	"github.com/signadot/verse/task"
)

// offeringFace is a runner face that offers what it is told: the agent named, and scripts.
func offeringFace(t *testing.T, verseURL string, agents ...string) (*face, *runnerd.InProcessHandler) {
	t.Helper()
	d := runnerd.NewInProcessHandler(func(l runner.Launch, reg *task.Registry) *task.Task {
		return task.NewTask(l.ID, systemstest.Opened(task.NewSupervisor(l.Environment, l.Autonomy, reg), l.Environment), &oneAction{}, okExecutor{})
	})
	d.Describe(offering(agents...))
	return provenFace(t, verseURL, d.HTTPHandler(), func() {}), d
}

// offering is a description offering agents and scripts, each building only the network
// `none`.
func offering(agents ...string) runner.Description {
	d := runner.Description{Version: runner.DescriptionVersion}
	for _, a := range agents {
		d.Offers = append(d.Offers, runner.Offer{Agent: &runner.AgentOffer{Name: a, Settings: map[string]runner.Setting{"model": {Values: []string{"small", "large"}}}},
			Systems: map[string]runner.System{"commands": {Ceiling: systems.Open()}, "network": builds(runner.NetworkNone)}})
	}
	d.Offers = append(d.Offers, runner.Offer{Script: true, Systems: map[string]runner.System{"network": builds(runner.NetworkNone)}})
	return d
}

// A RUNNER IS INSTALLED WITH WHAT IT OFFERS: the probe reads its description, the registration
// keeps it, and a check reads it again, so the record says what the runner says now. An offer
// that does not say which networks it builds, or a runner that offers nothing, is refused, at
// the install and at a check.
func TestARunnerIsInstalledWithWhatItOffers(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	face, d := offeringFace(t, ts.URL, "codex")

	resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "code", "url": face.URL, "subject": face.Subject})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	var added api.RunnerRow
	decodeResp(t, resp, &added)
	if len(added.Description().Offers) != 2 {
		t.Fatalf("add said %+v, want the offers it read", added)
	}
	rec, ok := findRunner(srv.Store, "verse.runners.code")
	if !ok || len(rec.Description().Offers) != 2 || rec.Description().Offers[0].Name() != "codex" || !rec.Description().Offers[1].Script {
		t.Fatalf("the registration holds offers %+v, want codex and scripts", rec.Described)
	}

	// The runner comes to offer another agent; a check reads it afresh.
	d.Describe(offering("codex", "claude"))
	check, err := http.Get(ts.URL + "/runners/code/check")
	if err != nil {
		t.Fatal(err)
	}
	if check.StatusCode != http.StatusOK {
		t.Fatalf("check = %s: %s", check.Status, readBody(t, check))
	}
	check.Body.Close()
	rec, _ = findRunner(srv.Store, "verse.runners.code")
	if len(rec.Description().Offers) != 3 || rec.Description().Offers[1].Name() != "claude" {
		t.Fatalf("after the check the registration holds %+v, want claude among the offers", rec.Described)
	}

	// The field is the runner's to say, not the installer's.
	resp = postTony(t, ts.URL+"/runners", map[string]any{"name": "fake", "url": face.URL, "subject": face.Subject, "offers": []any{}})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(readBody(t, resp), "read from the runner") {
		t.Fatalf("an installer writing offers = %s, want refused as the runner's to say", resp.Status)
	}

	// A runner whose offer names no network is no registration, and a check that finds one
	// keeps what the runner said before.
	d.Describe(runner.Description{Version: runner.DescriptionVersion, Offers: []runner.Offer{{Script: true}}})
	check, err = http.Get(ts.URL + "/runners/code/check")
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, check); check.StatusCode != http.StatusBadGateway || !strings.Contains(body, "the networks it builds") {
		t.Fatalf("a check finding an offer with no network = %s: %s, want 502", check.Status, body)
	}
	if rec, _ = findRunner(srv.Store, "verse.runners.code"); len(rec.Description().Offers) != 3 {
		t.Fatalf("after a refused check the registration holds %+v, want the three before", rec.Described)
	}
	resp = postTony(t, ts.URL+"/runners", provenFace(t, ts.URL, d.HTTPHandler(), func() {}).reg("silent"))
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "the networks it builds") {
		t.Fatalf("add of a runner whose offer names no network = %s: %s, want 400", resp.Status, body)
	}
	if _, ok := findRunner(srv.Store, "verse.runners.silent"); ok {
		t.Fatal("a refused runner was registered")
	}
	d.Describe(runner.Description{Version: runner.DescriptionVersion})
	resp = postTony(t, ts.URL+"/runners", provenFace(t, ts.URL, d.HTTPHandler(), func() {}).reg("empty"))
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "it offers nothing") {
		t.Fatalf("add of a runner that offers nothing = %s: %s, want 400", resp.Status, body)
	}
	// A doer offered twice: a launch names its doer, and could not say which of the two it means.
	script := runner.Offer{Script: true, Systems: map[string]runner.System{"network": {Builds: systems.Ceiling{{Attributes: map[string][]string{"mode": {runner.NetworkOpen}}}}}}}
	d.Describe(runner.Description{Version: runner.DescriptionVersion, Offers: []runner.Offer{script, script}})
	resp = postTony(t, ts.URL+"/runners", provenFace(t, ts.URL, d.HTTPHandler(), func() {}).reg("twice"))
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "it offers script twice") {
		t.Fatalf("add of a runner offering scripts twice = %s: %s, want 400", resp.Status, body)
	}
}

// A SPAWN GOES WHERE ITS DOER IS OFFERED, and is checked against that offer where it is written:
// a hand run at the prompt, a charter at its install. What some runner takes goes there; what none
// offers, or none takes, is refused saying so, before a task record is written. The local runner is
// one of the runners, with its own offers.
func TestASpawnGoesWhereItsDoerIsOffered(t *testing.T) {
	srv := testServer(t)
	describeLocal(t, srv, offering()) // the local runner: scripts only, as a keyless `verse up`'s
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	face, _ := offeringFace(t, ts.URL, "codex")
	postTony(t, ts.URL+"/runners", map[string]any{"name": "code", "url": face.URL, "subject": face.Subject}).Body.Close()

	for _, c := range []struct {
		what  string
		spawn map[string]any
		want  string // "" is accepted
		on    string // where an accepted spawn runs: the registration
	}{
		{"an agent a runner offers", map[string]any{"spawn": map[string]any{"agent": map[string]any{"name": "codex"}, "prompt": "go"}}, "", "verse.runners.code"},
		{"an agent none offers", map[string]any{"spawn": map[string]any{"agent": map[string]any{"name": "claude"}, "prompt": "go"}}, "no runner offers the agent claude", ""},
		{"a model the offer does not take", map[string]any{"spawn": map[string]any{"agent": map[string]any{"name": "codex", "model": "huge"}, "prompt": "go"}}, `no runner takes this spawn: verse.runners.code: agent.model: codex on this runner takes no such model`, ""},
		{"the default agent, which none offers", map[string]any{"spawn": map[string]any{"prompt": "go"}}, "no runner offers the agent verse", ""},
		{"a script, which both offer, first by name", map[string]any{"spawn": map[string]any{"script": "true"}}, "", "verse.runners.code"},
	} {
		t.Run(c.what, func(t *testing.T) {
			c.spawn["id"] = "run-" + strings.ReplaceAll(c.what, " ", "-")
			resp := postTony(t, ts.URL+"/tasks", c.spawn)
			body := readBody(t, resp)
			switch {
			case c.want == "" && resp.StatusCode != http.StatusOK:
				t.Fatalf("spawn = %s: %s, want it accepted", resp.Status, body)
			case c.want != "" && (resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, c.want)):
				t.Fatalf("spawn = %s: %s, want 400 saying %q", resp.Status, body, c.want)
			}
			if c.want != "" {
				if _, ok, _ := srv.Store.Get(entity.VerseRef("task", "instance", c.spawn["id"].(string))); ok {
					t.Fatal("a refused spawn left a task record")
				}
				return
			}
			if got := instanceOf(t, srv, c.spawn["id"].(string))["runnerName"]; got != c.on {
				t.Fatalf("the task's record says runnerName %v, want %q", got, c.on)
			}
		})
	}

	// A charter's spawn, at install.
	rule := func(agent string) string {
		return `[{name: r, condition: {subject: {path: "x.*", value: {go: true}}},
		  action: {patch: {go: false}, spawn: {agent: {name: ` + agent + `}, prompt: go}}}]`
	}
	if resp := installAs(t, "user:"+tester, ts.URL, rule("claude")); resp.StatusCode != http.StatusBadRequest ||
		!strings.Contains(readBody(t, resp), "no runner offers the agent claude") {
		t.Fatalf("a charter naming an agent no runner offers installed: %s", resp.Status)
	}
	if resp := installAs(t, "user:"+tester, ts.URL, rule("codex")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("a charter naming an agent a runner offers = %s: %s", resp.Status, readBody(t, resp))
	}
}
