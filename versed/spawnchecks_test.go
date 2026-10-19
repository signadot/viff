package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd/verserunner"
	"github.com/signadot/verse/task"
)

// A HAND RUN IS CHECKED AS THE RUNNER WILL CHECK IT, at the prompt: a run that names no doer, or
// a script given a prompt, is a 400 with the reason, and leaves no record of a task.
func TestAHandRunIsCheckedAsTheRunnerWillCheckIt(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, c := range []struct {
		what  string
		spawn map[string]any
		want  string
	}{
		{"no doer", map[string]any{"id": "bare", "prompt": "go"}, "an `agent` or a `script`"},
		{"a script given a prompt", map[string]any{"id": "both", "doer": map[string]any{"script": "true"}, "prompt": "go"}, "takes no `prompt`"},
	} {
		t.Run(c.what, func(t *testing.T) {
			resp := postTony(t, ts.URL+"/tasks", c.spawn)
			if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, c.want) {
				t.Fatalf("spawn = %s: %s, want 400 saying %q", resp.Status, body, c.want)
			}
			if _, ok, _ := srv.Store.Get(entity.VerseRef("task", "instance", c.spawn["id"].(string))); ok {
				t.Fatal("a refused run left a task record")
			}
		})
	}
}

// A DEFAULT RUNNER REACHED BY URL CHECKS ITS OWN LAUNCHES. Its offers are kept on no
// registration, so a charter naming an agent it offers is not refused for what the embedded
// runner, which it replaced, does not offer.
func TestADefaultRunnerByURLIsNotJudgedByTheEmbeddedOnesOffers(t *testing.T) {
	srv := testServer(t)
	srv.Embedded.Describe(offering()) // the embedded runner: scripts only
	face, _ := offeringFace(t, "codex")
	srv.SetRunner(client.HTTP(face.URL))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp := installAs(t, "user:"+tester, ts.URL, `[{name: r, condition: {subject: {path: "x.*", value: {go: true}}},
	  action: {patch: {go: false}, spawn: {doer: {agent: codex}, prompt: go}}}]`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a charter for the default runner's agent = %s: %s", resp.Status, readBody(t, resp))
	}
}

// A FIRING REFUSED BEFORE ITS LAUNCH IS GIVEN NO CREDENTIAL. The launch as bound is checked
// against what its runner offers before anything is minted for it, so a refused spawn's record
// says why and holds no token, and none went out.
func TestAFiringRefusedBeforeItsLaunchIsGivenNoCredential(t *testing.T) {
	srv, ts, idp := credentialServer(t)
	ana := idp.Token(authClient, "sub-ana", person("ana@example.com"))
	if code, out := bearer(t, http.MethodPost, ts.URL+"/charter", ana, "",
		deployRule(`[{name: ok, mode: auto}]`, `doer: {agent: codex}, prompt: go, credential: {audience: `+credAudience+`}`)); code != http.StatusCreated {
		t.Fatalf("install = %d %s", code, out)
	}
	// The runner describes itself after the install, and offers no codex.
	srv.Embedded.Describe(offering())
	if code, out := bearer(t, http.MethodPost, ts.URL+"/entity", ana, "", func() string {
		b, _ := codec.Marshal(api.PutEntity{At: "demo.pr.1", Payload: entity.Obj{"stage": "ready"}.Payload()}, codec.Tony)
		return string(b)
	}()); code/100 != 2 {
		t.Fatalf("put = %d %s", code, out)
	}
	rec := ruleTask(t, srv, `demo.pr."1"`)
	if rec["status"] != string(task.Failed) || !strings.Contains(entity.Str(entity.Obj(rec).Payload(), "err"), "offers no agent") {
		t.Fatalf("the task = %v, want it failed for the offer", rec)
	}
	if rec["credential"] != nil {
		t.Fatalf("a refused spawn holds a credential: %v", rec["credential"])
	}
}

// A CHARTER'S NETWORK REACHES ITS FIRING. The spawn is checked as written at install and as bound
// at each firing, so a `with` fact the binding drops passes the first and fails the second: on
// `verse up`, whose runner builds only the open network, the firing would launch with none.
func TestACharterFiresWithItsNetwork(t *testing.T) {
	srv := testServer(t)
	srv.Embedded.Describe(verserunner.Config{}.Describe(verserunner.Secrets{}, []string{runner.NetworkOpen}))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installCharter(t, ts, "", `[{name: r, condition: {subject: {path: "demo.pr.*", value: {stage: ready}}},
	  action: {patch: {stage: done}, spawn: {doer: {script: "echo hi", with: {network: {mode: open}}}, policy: [{then: allow}]}}}]`)
	if resp := postTony(t, ts.URL+"/entity", api.PutEntity{At: "demo.pr.1", Payload: entity.Obj{"stage": "ready"}.Payload()}); resp.StatusCode/100 != 2 {
		t.Fatalf("put = %s: %s", resp.Status, readBody(t, resp))
	} else {
		resp.Body.Close()
	}
	if rec := ruleTask(t, srv, `demo.pr."1"`); rec["status"] != string(task.Done) {
		t.Fatalf("the firing's task = %v, want it done with the network its charter gave it", rec)
	}
}
