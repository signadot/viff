package delivery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// OpenGitHubPR opens a pull request when none is open from the head, and finds the open one
// rather than opening a second when there is.
func TestOpenGitHubPROpensOnceAndThenFinds(t *testing.T) {
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
	spec := GitHubPR{API: srv.URL, Owner: "signadot", Repo: "signadot", Head: "verse/train",
		Base: "master", Title: "drift", Body: "the train"}

	out, err := OpenGitHubPR(context.Background(), srv.Client(), "tok", spec)
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
	out, err = OpenGitHubPR(context.Background(), srv.Client(), "tok", spec)
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

// RepoPath reads the owner and name out of a repository url at any forge, and refuses what has
// no forge: a local path has no pull requests and no issues.
func TestRepoPathIsAForgesOrNothing(t *testing.T) {
	for _, url := range []string{"https://github.com/example/app.git", "https://ghe.corp/example/app"} {
		owner, name, err := RepoPath(url)
		if err != nil || owner != "signadot" || name != "signadot" {
			t.Fatalf("%s: %s %s %v", url, owner, name, err)
		}
	}
	for _, bad := range []string{".", "/tmp/x.git", "https://github.com/signadot"} {
		if _, _, err := RepoPath(bad); err == nil {
			t.Errorf("%q resolved to a repository at a forge", bad)
		}
	}
}

// UpdateGitHubPR changes the open pull request from the head, and refuses when there is none: an
// update of nothing is a proposal that was never made.
func TestUpdateGitHubPRChangesTheOpenOneOrRefuses(t *testing.T) {
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
	spec := GitHubPR{API: srv.URL, Owner: "signadot", Repo: "signadot", Head: "verse/train", Title: "retitled"}
	if _, err := UpdateGitHubPR(context.Background(), srv.Client(), "tok", spec); err == nil || !strings.Contains(err.Error(), "no pull request is open") {
		t.Fatalf("updating with none open: %v", err)
	}
	open = append(open, map[string]any{"number": 7, "html_url": "https://github.com/example/app/pull/7"})
	out, err := UpdateGitHubPR(context.Background(), srv.Client(), "tok", spec)
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
