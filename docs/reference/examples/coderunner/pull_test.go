package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A destination that opens a pull request is checked where the config is read, for the three
// ways it could be written that would fail after the work was already on the forge.
func TestAPullDestinationIsCheckedAtStartup(t *testing.T) {
	github := "https://github.com/example/app"
	train := Destination{Name: "train", Ref: "refs/heads/verse/train", Pull: &Pull{Base: "master"}}
	for name, repo := range map[string]Repo{
		// A pull request is from a ref work went to; a retirement sends nothing anywhere.
		"a retirement that opens a pull request": {Name: "s", Repo: github, Base: "master",
			Destinations: []Destination{{Name: "retire", Ref: "refs/heads/verse/auto/*/*", Delete: true, Pull: &Pull{}}}},
		// A local path has no pull requests.
		"a local repository": {Name: "s", Repo: ".", Base: "master", Destinations: []Destination{train}},
		// Another forge has another API, which is a second implementation and not a second case.
		"another forge": {Name: "s", Repo: "https://gitlab.com/example/app", Base: "master",
			Destinations: []Destination{train}},
	} {
		env := Environment{Name: "e", Repos: []Repo{repo}}
		if err := checkDests(env.Name, env.Credentials, repo); err == nil {
			t.Errorf("%s loaded — it would fail at the first push instead", name)
		}
	}

	// And the API host is a route of its own: the push host is not it.
	env := Environment{
		Name:   "e",
		Repos:  []Repo{{Name: "s", Repo: github, Base: "master", Destinations: []Destination{train}}},
		Egress: []string{"api.portkey.ai", "github.com"},
	}
	err := checkReachable(env)
	if err == nil {
		t.Fatal("an environment that opens pull requests with no route to the API loaded")
	}
	if !strings.Contains(err.Error(), "api.github.com") {
		t.Errorf("the refusal does not name the host that is missing: %v", err)
	}
	env.Egress = append(env.Egress, "api.github.com")
	if err := checkReachable(env); err != nil {
		t.Fatalf("the API is on the allowlist and it was still refused: %v", err)
	}
}

// The spec the parent resolves: the pushed ref is the head, the environment's text is expanded
// the way the ref is, and an empty base is the repository's own.
func TestAPullSpecIsTheEnvironmentsAndNotTheTasks(t *testing.T) {
	tree := &Tree{
		Repo: Repo{Name: "s", Repo: "https://github.com/example/app.git", Base: "master",
			Destinations: []Destination{{Name: "train", Ref: "refs/heads/verse/train/$sha",
				Pull: &Pull{Title: "drift at $sha", Body: "task $task"}}}},
		Head: "0123456789abcdef",
	}
	spec, err := tree.pullSpec(tree.Repo.Destinations[0], autoBranchPrefix+"t1")
	if err != nil {
		t.Fatal(err)
	}
	want := pullSpec{API: githubAPI, Owner: "signadot", Repo: "signadot",
		Head: "verse/train/01234567", Base: "master", Title: "drift at 01234567", Body: "task t1"}
	if spec != want {
		t.Errorf("resolved\n %+v\nwant\n %+v", spec, want)
	}
}

// ensurePull opens a pull request when none is open from the head, and finds the open one
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
	spec := pullSpec{API: srv.URL, Owner: "signadot", Repo: "signadot", Head: "verse/train",
		Base: "master", Title: "drift", Body: "the train"}

	out, err := ensurePull(context.Background(), srv.Client(), "tok", spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "opened pull request #7") {
		t.Errorf("first push: %q", out)
	}
	if posted["head"] != "verse/train" || posted["base"] != "master" || posted["title"] != "drift" || posted["body"] != "the train" {
		t.Errorf("opened with %v — not what the environment resolved", posted)
	}
	if auth != "Bearer tok" {
		t.Errorf("authenticated as %q — the credential the push was released", auth)
	}

	posted = nil
	out, err = ensurePull(context.Background(), srv.Client(), "tok", spec)
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
