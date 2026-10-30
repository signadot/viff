package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// A CHARTER IS HELD TO ITS RUNNER'S CEILINGS WHEN IT IS INSTALLED, on its environment as written:
// a repository its firing binds installs under a ceiling of an owner's, since a binding could be
// within it, and one written outside the ceiling is refused with the ceiling named.
func TestACharterIsHeldToItsRunnersCeilingsAtInstall(t *testing.T) {
	srv := testServer(t)
	noLocalRunner(t, srv)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	h := runnerd.NewInProcessHandler(func(l runner.Launch, _ *task.Registry) *task.Task { return nil })
	h.Describe(runner.Description{Version: runner.DescriptionVersion, Offers: []runner.Offer{{Script: true,
		Systems: map[string]runner.System{"network": builds(runner.NetworkNone, runner.NetworkOpen), "git": {Ceiling: systems.Ceiling{{
			Attributes: map[string][]string{"repo": {"github.com/signadot/**"}},
			Verbs:      map[string][]systems.Shape{"fetch": {{}}, "push": {{"ref": "refs/heads/verse/**"}}},
		}}}}}}})
	face := provenFace(t, ts.URL, h.HTTPHandler(), func() {})
	if resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "ceiled", "url": face.URL, "subject": face.Subject}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("runner add = %s", resp.Status)
	}
	install := func(name, git string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/charter", strings.NewReader(`
- name: `+name+`
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action:
    patch: {stage: started}
    spawn: {script: "true", environment: {systems: {git: `+git+`}}}
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
	if code, body := install("bound", `{repo: $subject.value.repo, fetch: {}, push: {ref: "refs/heads/verse/auto/*"}}`); code != http.StatusCreated {
		t.Errorf("a bound repository = %d %s, want it installed", code, body)
	}
	for name, git := range map[string]string{
		"outside": `{repo: other/app, fetch: {}}`,
		"owner":   `{repo: "github.com/other/$subject.value.name", fetch: {}}`,
		"wider":   `{repo: signadot/app, push: {ref: "refs/heads/**"}}`,
	} {
		if code, body := install(name, git); code == http.StatusCreated || !strings.Contains(body, "beyond this runner's ceiling for git") {
			t.Errorf("%s = %d %s, want it refused naming the ceiling", name, code, body)
		}
	}
}
