package delivery

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// EnsurePull opens a pull request when none is open from the head, and finds the open one
// rather than opening a second when there is.
func TestEnsurePullOpensOnceAndThenFinds(t *testing.T) {
	var open []map[string]any
	var posted map[string]string
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/repos/example/app/pulls" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("head") != "signadot:verse/train" || r.URL.Query().Get("state") != "open" {
				t.Errorf("listed with %q — not this head's open pull requests", r.URL.RawQuery)
			}
			json.NewEncoder(w).Encode(open)
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			open = append(open, map[string]any{"number": 7, "html_url": "https://github.com/example/app" + "/pull/7"})
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(open[0])
		}
	}))
	defer srv.Close()
	spec := PullSpec{API: srv.URL, Owner: "signadot", Repo: "signadot", Head: "verse/train",
		Base: "master", Title: "drift", Body: "the train"}

	out, err := EnsurePull(context.Background(), srv.Client(), "tok", spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "opened pull request #7") {
		t.Errorf("first push: %q", out)
	}
	if posted["head"] != "verse/train" || posted["base"] != "master" || posted["title"] != "drift" || posted["body"] != "the train" {
		t.Errorf("opened with %v — not what was asked", posted)
	}
	if auth != "Bearer tok" {
		t.Errorf("authenticated as %q — the credential the push was released", auth)
	}

	posted = nil
	out, err = EnsurePull(context.Background(), srv.Client(), "tok", spec)
	if err != nil {
		t.Fatal(err)
	}
	if posted != nil {
		t.Error("a second pull request was opened from a head that already has one")
	}
	if !strings.Contains(out, "#7") || !strings.Contains(out, "open") {
		t.Errorf("second push: %q", out)
	}
}

// PullAPI reads the forge out of a repository url, and refuses what is not GitHub: a local path
// has no pull requests, and another forge has another API.
func TestPullAPIIsGitHubsOrNothing(t *testing.T) {
	api, owner, name, err := PullAPI("https://github.com/example/app.git")
	if err != nil || api != GitHubAPI || owner != "signadot" || name != "signadot" {
		t.Fatalf("github url: %s %s %s %v", api, owner, name, err)
	}
	for _, bad := range []string{".", "/tmp/x.git", "https://gitlab.com/example/app", "https://github.com/signadot"} {
		if _, _, _, err := PullAPI(bad); err == nil {
			t.Errorf("%q resolved to a pull request API", bad)
		}
	}
}

// UpdatePull changes the open pull request from the head, and refuses when there is none: an
// update of nothing is a proposal that was never made.
func TestUpdatePullChangesTheOpenOneOrRefuses(t *testing.T) {
	var open []map[string]any
	var patched map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/example/app/pulls":
			json.NewEncoder(w).Encode(open)
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/example/app/pulls/7":
			if err := json.NewDecoder(r.Body).Decode(&patched); err != nil {
				t.Fatal(err)
			}
			json.NewEncoder(w).Encode(map[string]any{"number": 7, "html_url": "https://github.com/example/app/pull/7"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	spec := PullSpec{API: srv.URL, Owner: "signadot", Repo: "signadot", Head: "verse/train", Title: "retitled"}
	if _, err := UpdatePull(context.Background(), srv.Client(), "tok", spec); err == nil || !strings.Contains(err.Error(), "no pull request is open") {
		t.Fatalf("updating with none open: %v", err)
	}
	open = append(open, map[string]any{"number": 7, "html_url": "https://github.com/example/app/pull/7"})
	out, err := UpdatePull(context.Background(), srv.Client(), "tok", spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "updated pull request #7") {
		t.Errorf("update: %q", out)
	}
	// Only what was given is changed: a title, and neither a body nor a base.
	if patched["title"] != "retitled" || len(patched) != 1 {
		t.Errorf("patched %v, want the title alone", patched)
	}
}

// A pull request tool call resolves against the workspace and the allowance: the task's own
// branch when none is named, the checkout's base when none is named, and only a branch the task
// may push, at a forge that has pull requests.
func TestAPullRequestIsFromABranchTheTaskMayPush(t *testing.T) {
	github := &workspace.Tree{Repo: workspace.Repo{Name: "s", Repo: "https://github.com/example/app.git", Base: "master",
		Access: task.AccessWrite, Push: &task.RepoPush{New: []string{"verse/train", "verse/auto/*"}}}}
	local := &workspace.Tree{Repo: workspace.Repo{Name: "l", Repo: "/tmp/x.git", Base: "main",
		Access: task.AccessWrite, Push: &task.RepoPush{New: []string{"verse/*"}}}}
	ws := &workspace.Workspace{Branch: "verse/auto/t1", Trees: []*workspace.Tree{github, local}}

	// Only the forge repository offers the tools.
	c := &Client{Conn: fakeConn{}, Workspace: ws}
	tools := c.PullTools()
	if len(tools) != 2 || tools[0].Name != CreatePullToolName || tools[1].Name != UpdatePullToolName {
		t.Fatalf("pull tools = %v", tools)
	}
	if !strings.Contains(tools[0].Description, "Repositories: s.") {
		t.Errorf("the tool offers %q, want the forge repository alone", tools[0].Description)
	}

	_, spec, err := pullSpec(ws, []string{"s"}, map[string]any{"title": "t"})
	if err != nil {
		t.Fatal(err)
	}
	want := PullSpec{API: GitHubAPI, Owner: "signadot", Repo: "signadot", Head: "verse/auto/t1", Base: "master", Title: "t"}
	if spec != want {
		t.Errorf("defaults resolved to %+v, want %+v", spec, want)
	}
	if _, spec, err := pullSpec(ws, []string{"s"}, map[string]any{"head": "refs/heads/verse/train", "base": "release"}); err != nil || spec.Head != "verse/train" || spec.Base != "release" {
		t.Errorf("named head and base: %+v %v", spec, err)
	}
	if _, _, err := pullSpec(ws, []string{"s"}, map[string]any{"head": "main"}); err == nil || !strings.Contains(err.Error(), "may push") {
		t.Errorf("a pull request from a branch the task may not push: %v", err)
	}
	if _, _, err := pullSpec(ws, []string{"s"}, map[string]any{"repo": "l"}); err == nil {
		t.Error("a pull request at a local path resolved")
	}
}

// fakeConn stands in for the channel where a test only needs the tools built.
type fakeConn struct{ net.Conn }
