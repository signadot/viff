package delivery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/signadot/tony-format/git-issue/issuelib"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/systems"
)

// openIssues is a workspace on one repository, svc, checked out from a fresh origin, and the
// origin: what the trackers' acts are tried against.
func openIssues(t *testing.T, id string) (*workspace.Mirrors, *workspace.Workspace, string) {
	t.Helper()
	origin := initRepo(t, t.TempDir(), "acme/svc")
	m := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
	ws, err := m.Open(context.Background(), id,
		[]workspace.Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}, t.TempDir(), workspace.Author{})
	if err != nil {
		t.Fatal(err)
	}
	return m, ws, origin
}

// A GIT ISSUE IS FILED IN THE CHECKOUT BY THE TASK AND PUSHED BY THE RUNNER: the filing writes
// the ref with no request made, the push op sends the tracker, and the remote's own store lists
// the issue; a pull then reports it, and a push of a repository not here is an error.
func TestAGitIssueIsFiledHereAndPushedThere(t *testing.T) {
	m, ws, origin := openIssues(t, "i1")
	if _, err := ws.Trees[0].CreateIssue("the roof leaks", "when it rains"); err != nil {
		t.Fatal(err)
	}
	if remote, _ := issuelib.NewGitStoreAt(origin, io.Discard).List(false); len(remote) != 0 {
		t.Fatalf("the filing left before any push: %+v", remote)
	}
	s := &Server{Mirrors: m, Log: io.Discard, Ceilings: performs("git-issue")}
	tk := Task{ID: "i1", Workspace: ws, Launch: within([]string{"acme/svc"}, "git-issue"), Credential: func(workspace.Repo) (string, error) { return "", nil }}
	rep := s.answer(tk, frame(t, Request{Op: OpGitIssuePush, Repo: "svc"}))
	if !rep.OK || !strings.Contains(rep.Out, "pushed the issues of svc (1 ref(s))") {
		t.Fatalf("push: %+v", rep)
	}
	remote, err := issuelib.NewGitStoreAt(origin, io.Discard).List(false)
	if err != nil || len(remote) != 1 || remote[0].Title != "the roof leaks" {
		t.Fatalf("the remote's tracker lists %+v, %v", remote, err)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpGitIssuePull, Repo: "svc"})); !rep.OK || !strings.Contains(rep.Out, "1 open:\n  "+remote[0].ID+"  the roof leaks") {
		t.Errorf("pull: %+v", rep)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpGitIssuePush, Repo: "svc"})); !rep.OK || !strings.Contains(rep.Out, "nothing to push") {
		t.Errorf("a second push: %+v", rep)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpGitIssuePush, Repo: "nope"})); rep.Error == "" {
		t.Errorf("an unknown repository: %+v", rep)
	}
}

// A TRACKER IS PULLED INTO THE CHECKOUT ON REQUEST, and the answer lists the open issues: a
// script with no `git issue` of its own reads them there, and one with it reads the refs.
func TestATrackerIsPulledIntoTheCheckout(t *testing.T) {
	m, ws, origin := openIssues(t, "f2")
	filed, err := issuelib.NewGitStoreAt(origin, io.Discard).Create("the roof leaks", "# the roof leaks\n\nwhen it rains\n")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Mirrors: m, Log: io.Discard, Ceilings: performs("git-issue")}
	tk := Task{ID: "f2", Workspace: ws, Launch: within([]string{"acme/svc"}, "git-issue"), Credential: func(workspace.Repo) (string, error) { return "", nil }}
	rep := s.answer(tk, frame(t, Request{Op: OpGitIssuePull, Repo: "svc"}))
	if !rep.OK || !strings.Contains(rep.Out, "1 open:") || !strings.Contains(rep.Out, filed.ID+"  the roof leaks") || len(rep.Updated) != 1 {
		t.Fatalf("reply: %+v", rep)
	}
	r, _ := git.PlainOpen(ws.Trees[0].Dir)
	if _, err := r.Reference(plumbing.ReferenceName(filed.Ref), true); err != nil {
		t.Errorf("the checkout has no %s: %v", filed.Ref, err)
	}
}

// githubTree is a workspace on one repository at a GitHub-shaped forge.
func githubTree(name, remote string) *workspace.Workspace {
	return &workspace.Workspace{Trees: []*workspace.Tree{{Repo: workspace.Repo{Repo: runner.Repo{Repo: name, From: "main"}, Remote: remote}}}}
}

// A GITHUB ISSUE IS OPENED AT THE FORGE with the runner's token, and the answer is its number and
// url. The forge here is a test server that checks the token and the body. A repository that is
// not at GitHub is refused: there is no forge to open an issue at.
func TestAGitHubIssueIsOpenedWithTheRunnersToken(t *testing.T) {
	var got struct{ Auth, Path, Title, Body string }
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Auth, got.Path = r.Header.Get("Authorization"), r.URL.Path
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		got.Title, got.Body = in["title"], in["body"]
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number": 7, "html_url": "https://github.com/acme/app/issues/7"}`))
	}))
	defer forge.Close()
	ws := githubTree("acme/app", "https://github.com/acme/app.git")
	s := &Server{Log: io.Discard, Services: Services{GitHubAPI: forge.URL}, Ceilings: performs("github-issue")}
	tk := Task{ID: "i2", Workspace: ws, Launch: within([]string{"acme/app"}, "github-issue"), Credential: func(workspace.Repo) (string, error) { return "the-runners-own", nil }}
	rep := s.answer(tk, frame(t, Request{Op: OpGitHubIssueCreate, Repo: "app", Title: "the roof leaks", Body: "when it rains"}))
	if !rep.OK || rep.Out != "opened issue #7 at acme/app: https://github.com/acme/app/issues/7" {
		t.Fatalf("reply: %+v", rep)
	}
	if got.Auth != "Bearer the-runners-own" || got.Path != "/repos/acme/app/issues" || got.Title != "the roof leaks" || got.Body != "when it rains" {
		t.Errorf("the forge saw %+v", got)
	}
	local := githubTree("x/l", "/tmp/x.git")
	if rep := s.answer(Task{ID: "i3", Workspace: local, Launch: tk.Launch, Credential: tk.Credential}, frame(t, Request{Op: OpGitHubIssueCreate, Repo: "l", Title: "x"})); rep.Refused == "" {
		t.Errorf("a local repository: %+v, want a refusal", rep)
	}
}

// A LINEAR ISSUE IS CREATED WITH THE RUNNER'S CREDENTIAL: the team is found by its key, the issue
// made in it, and the answer is the identifier a person types and the url. Without a credential
// the runner says so, and where it is configured.
func TestALinearIssueIsCreatedWithTheRunnersCredential(t *testing.T) {
	var auths []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		var in struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch {
		case strings.Contains(in.Query, "teams("):
			if in.Variables["key"] != "ENG" {
				_, _ = w.Write([]byte(`{"data":{"teams":{"nodes":[]}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"teams":{"nodes":[{"id":"team-uuid"}]}}}`))
		case strings.Contains(in.Query, "issueCreate("):
			if in.Variables["team"] != "team-uuid" || in.Variables["title"] != "the roof leaks" || in.Variables["description"] != "when it rains" {
				t.Errorf("issueCreate variables: %v", in.Variables)
			}
			_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"issue-uuid","identifier":"ENG-42","url":"https://linear.app/acme/issue/ENG-42"}}}}`))
		default:
			t.Errorf("unexpected query: %s", in.Query)
		}
	}))
	defer api.Close()
	s := &Server{Log: io.Discard, Services: Services{LinearAuth: "lin_api_x", LinearAPI: api.URL}, Ceilings: performs("linear-issue")}
	tk := Task{ID: "i4", Workspace: &workspace.Workspace{}, Launch: within(nil, "linear-issue")}
	rep := s.answer(tk, frame(t, Request{Op: OpLinearIssueCreate, Team: "ENG", Title: "the roof leaks", Body: "when it rains"}))
	if !rep.OK || rep.Out != "created ENG-42: https://linear.app/acme/issue/ENG-42" {
		t.Fatalf("reply: %+v", rep)
	}
	if len(auths) != 2 || auths[0] != "lin_api_x" || auths[1] != "lin_api_x" {
		t.Errorf("Linear saw %v, want the runner's credential on both calls", auths)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpLinearIssueCreate, Team: "NOPE", Title: "x"})); rep.Error == "" || !strings.Contains(rep.Error, "no team NOPE") {
		t.Errorf("an unknown team: %+v", rep)
	}
	none := &Server{Log: io.Discard, Ceilings: performs("linear-issue")}
	if rep := none.answer(tk, frame(t, Request{Op: OpLinearIssueCreate, Team: "ENG", Title: "x"})); !strings.Contains(rep.Error, "LINEAR_API_KEY") {
		t.Errorf("a runner without a credential: %+v, want it to say where one is configured", rep)
	}
}

// COMMENT AND CLOSE AT THE REMOTE TRACKERS go through the runner with its credential, naming
// the issue as the tracker does: a number at the forge, an identifier at Linear.
func TestCommentAndCloseAtTheRemoteTrackers(t *testing.T) {
	var forgeSaw []string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		forgeSaw = append(forgeSaw, r.Method+" "+r.URL.Path+" "+in["body"]+in["state"])
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"state":"closed","html_url":"https://github.com/acme/app/issues/7#c1"}`))
	}))
	defer forge.Close()
	var linearSaw []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch {
		case strings.Contains(in.Query, "states("):
			linearSaw = append(linearSaw, "states")
			_, _ = w.Write([]byte(`{"data":{"issue":{"id":"i42","team":{"key":"ENG","states":{"nodes":[{"id":"s9","name":"Done","type":"completed"}]}}}}}`))
		case strings.Contains(in.Query, "issue(id:"):
			linearSaw = append(linearSaw, "issue")
			_, _ = w.Write([]byte(`{"data":{"issue":{"id":"i42"}}}`))
		case strings.Contains(in.Query, "commentCreate("):
			linearSaw = append(linearSaw, "comment:"+in.Variables["body"].(string))
			_, _ = w.Write([]byte(`{"data":{"commentCreate":{"success":true,"comment":{"id":"c1","url":"https://linear.app/x/issue/ENG-42#c1"}}}}`))
		case strings.Contains(in.Query, "issueUpdate("):
			linearSaw = append(linearSaw, "update:"+in.Variables["state"].(string))
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"identifier":"ENG-42","url":"https://linear.app/x/issue/ENG-42"}}}}`))
		}
	}))
	defer api.Close()
	ws := githubTree("acme/app", "https://github.com/acme/app.git")
	s := &Server{Log: io.Discard, Services: Services{GitHubAPI: forge.URL, LinearAuth: "k", LinearAPI: api.URL}, Ceilings: performs("github-issue", "linear-issue")}
	tk := Task{ID: "v1", Workspace: ws, Launch: within([]string{"acme/app"}, "github-issue", "linear-issue"), Credential: func(workspace.Repo) (string, error) { return "tok", nil }}

	if rep := s.answer(tk, frame(t, Request{Op: OpGitHubIssueComment, Repo: "app", Issue: "7", Body: "on it"})); !rep.OK || !strings.Contains(rep.Out, "commented on issue #7 at acme/app") {
		t.Errorf("github comment: %+v", rep)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpGitHubIssueClose, Repo: "app", Issue: "7"})); !rep.OK || !strings.Contains(rep.Out, "closed issue #7 at acme/app") {
		t.Errorf("github close: %+v", rep)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpGitHubIssueClose, Repo: "app", Issue: "x"})); rep.Refused == "" {
		t.Errorf("a non-number: %+v", rep)
	}
	if got := strings.Join(forgeSaw, "; "); got != "POST /repos/acme/app/issues/7/comments on it; PATCH /repos/acme/app/issues/7 closed" {
		t.Errorf("the forge saw %q", got)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpLinearIssueComment, Issue: "ENG-42", Body: "on it"})); !rep.OK || rep.Out != "commented on ENG-42: https://linear.app/x/issue/ENG-42#c1" {
		t.Errorf("linear comment: %+v", rep)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpLinearIssueClose, Issue: "ENG-42"})); !rep.OK || rep.Out != "closed ENG-42 (Done): https://linear.app/x/issue/ENG-42" {
		t.Errorf("linear close: %+v", rep)
	}
	if got := strings.Join(linearSaw, " "); got != "issue comment:on it states update:s9" {
		t.Errorf("Linear saw %q", got)
	}
}

// performs is a runner's ceilings for the systems named, each allowing everything.
func performs(names ...string) map[string]systems.Ceiling {
	out := map[string]systems.Ceiling{}
	for _, s := range names {
		out[s] = systems.Open()
	}
	return out
}

// frame is a request as it arrives on the channel.
func frame(t *testing.T, req Request) []byte {
	t.Helper()
	raw, err := codec.Marshal(req, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
