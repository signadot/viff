package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// OpenGitHubPR opens a pull request when none is open from the head, and finds the open one
// rather than opening a second when there is.
func TestOpenGitHubPROpensOnceAndThenFinds(t *testing.T) {
	var open []map[string]any
	var posted map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/repos/example/app/pulls" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("head") != "example:verse/train" || r.URL.Query().Get("state") != "open" {
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
	spec := GitHubPR{API: srv.URL, Owner: "example", Repo: "app", Head: "verse/train",
		Base: "master", Title: "drift", Body: "the train"}

	out, err := OpenGitHubPR(context.Background(), srv.Client(), "tok", spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "opened pull request #7") {
		t.Errorf("first push: %q", out)
	}
	if posted["head"] != "verse/train" || posted["base"] != "master" || posted["title"] != "drift" || posted["body"] != "the train" || posted["draft"] != false {
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
		if err != nil || owner != "example" || name != "app" {
			t.Fatalf("%s: %s %s %v", url, owner, name, err)
		}
	}
	for _, bad := range []string{".", "/tmp/x.git", "https://github.com/example"} {
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
	spec := GitHubPR{API: srv.URL, Owner: "example", Repo: "app", Head: "verse/train", Title: "retitled"}
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

// A TASK READS THE FORGE'S PULL REQUESTS through the runner, with the runner's token: a list that
// pages, and stops at its most and says so; one pull request with its reviews; and the refusals —
// a number that is not one, a pull request that is not there, a read the autonomy denies, and a
// runner with no github system. The forge is a test server answering as GitHub does, which is
// whole repositories on every listing item, so the page size is checked against the answer's.
func TestATaskReadsPullRequestsThroughTheRunner(t *testing.T) {
	var open int // how many pull requests are open; set per case
	var pages []string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer the-runners-own" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		pull := func(n int) map[string]any {
			return map[string]any{
				"number": n, "title": fmt.Sprintf("change %d", n), "state": "open", "draft": n == 2,
				"user": map[string]any{"login": "ana"}, "base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/app"}},
				"head":       map[string]any{"ref": fmt.Sprintf("f%d", n), "repo": map[string]any{"full_name": "acme/app"}},
				"created_at": "2026-10-20T09:00:00Z", "updated_at": "2026-10-22T09:00:00Z",
				"html_url": fmt.Sprintf("https://github.com/acme/app/pull/%d", n), "body": "why\nand how",
				"requested_reviewers": []map[string]any{{"login": "zed"}},
				"requested_teams":     []map[string]any{{"slug": "platform"}},
			}
		}
		switch path := r.URL.Path; {
		case path == "/repos/acme/app/pulls":
			pages = append(pages, r.URL.Query().Get("page"))
			if r.URL.Query().Get("state") != "open" {
				t.Errorf("listed with state %q", r.URL.Query().Get("state"))
			}
			per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			out := []map[string]any{}
			for n := (page-1)*per + 1; n <= min(page*per, open); n++ {
				out = append(out, pull(n))
			}
			_ = json.NewEncoder(w).Encode(out)
		case path == "/repos/acme/app/pulls/7":
			_ = json.NewEncoder(w).Encode(pull(7))
		case path == "/repos/acme/app/pulls/7/reviews":
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"user": {"login": "bo"}, "state": "CHANGES_REQUESTED", "submitted_at": "2026-10-21T10:00:00Z", "body": "one thing\nand another"},
				{"user": {"login": "zed"}, "state": "COMMENTED", "submitted_at": "2026-10-21T11:00:00Z", "body": ""}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message": "Not Found"}`))
		}
	}))
	defer forge.Close()

	ws := githubTree("acme/app", "https://github.com/acme/app.git")
	s := &Server{Log: io.Discard, Services: Services{GitHubAPI: forge.URL}, Ceilings: performs("github")}
	tk := Task{ID: "r1", Workspace: ws, Launch: within([]string{"acme/app"}, "github"), Credential: func(workspace.Repo) (string, error) { return "the-runners-own", nil }}
	ask := func(req Request) Reply { return s.answer(tk, frame(t, req)) }

	t.Run("a list that pages", func(t *testing.T) {
		open, pages = 60, nil
		rep := ask(Request{Op: OpGitHubListPRs, Repo: "app"})
		lines := strings.Split(rep.Out, "\n")
		if !rep.OK || len(lines) != 61 || lines[0] != "acme/app: 60 open pull request(s), most recently changed first" {
			t.Fatalf("reply: %+v", rep)
		}
		if lines[2] != "#2 change 2 — by ana, draft, main <- f2, updated 2026-10-22T09:00:00Z; review requested from zed, teams platform" {
			t.Errorf("a line: %q", lines[2])
		}
		if fmt.Sprint(pages) != "[1 2]" {
			t.Errorf("pages asked: %v", pages)
		}
	})
	t.Run("a list cut at its most says so", func(t *testing.T) {
		open, pages = prsMost+30, nil
		rep := ask(Request{Op: OpGitHubListPRs, Repo: "app"})
		if !rep.OK || !strings.HasPrefix(rep.Out, fmt.Sprintf("acme/app: %d open pull request(s), most recently changed first; the first %d, and more may be open", prsMost, prsMost)) {
			t.Fatalf("reply: %.200s", rep.Out)
		}
		if len(pages) != prsMost/prsPerPage {
			t.Errorf("asked %d pages, want %d: the read stops at its most", len(pages), prsMost/prsPerPage)
		}
	})
	t.Run("one pull request with its reviews", func(t *testing.T) {
		rep := ask(Request{Op: OpGitHubReadPR, Repo: "app", PR: "7"})
		want := "#7 change 7 (open) https://github.com/acme/app/pull/7\n" +
			"by ana, main <- f7; created 2026-10-20T09:00:00Z, updated 2026-10-22T09:00:00Z\n" +
			"review requested from zed, teams platform\n" +
			"2 review(s), oldest first\n" +
			"2026-10-21T10:00:00Z bo CHANGES_REQUESTED: one thing\n    and another\n" +
			"2026-10-21T11:00:00Z zed COMMENTED\n" +
			"The description, as its author wrote it:\nwhy\nand how"
		if !rep.OK || rep.Out != want {
			t.Fatalf("reply: %+v\nwant:\n%s", rep, want)
		}
	})
	for _, tc := range []struct {
		name, pr, refused string
	}{
		{"a number that is not one", "seven", `"seven" is not a pull request number`},
		{"a pull request that is not there", "9", "there is no pull request #9 at acme/app"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rep := ask(Request{Op: OpGitHubReadPR, Repo: "app", PR: tc.pr}); rep.Refused != tc.refused {
				t.Errorf("reply: %+v, want refused %q", rep, tc.refused)
			}
		})
	}
	t.Run("a read the autonomy denies", func(t *testing.T) {
		l := within([]string{"acme/app"}, "github")
		l.Autonomy = task.Autonomy{{Name: "no-reads", Match: &task.Match{System: "github", Verb: "read-pr"}, Then: task.Then{Mode: task.ThenDeny}}, {Then: task.Then{Mode: task.ThenAllow}}}
		denied := Task{ID: "r2", Workspace: ws, Launch: l, Credential: tk.Credential}
		if rep := s.answer(denied, frame(t, Request{Op: OpGitHubReadPR, Repo: "app", PR: "7"})); !strings.Contains(rep.Refused, "no-reads") {
			t.Errorf("reply: %+v, want the deny row named", rep)
		}
		if rep := s.answer(denied, frame(t, Request{Op: OpGitHubListPRs, Repo: "app"})); !rep.OK {
			t.Errorf("a list the row does not name: %+v", rep)
		}
	})
	t.Run("a ceiling that gives only create-pr", func(t *testing.T) {
		writes := &Server{Log: io.Discard, Services: s.Services, Ceilings: map[string]systems.Ceiling{"github": {{Verbs: map[string][]systems.Shape{"create-pr": {{}}}}}}}
		if rep := writes.answer(tk, frame(t, Request{Op: OpGitHubListPRs, Repo: "app"})); !strings.Contains(rep.Refused, "ceiling") {
			t.Errorf("reply: %+v, want the ceiling's refusal", rep)
		}
	})
	t.Run("a runner with no github system", func(t *testing.T) {
		none := &Server{Log: io.Discard, Services: s.Services, Ceilings: performs("github-issue")}
		if rep := none.answer(tk, frame(t, Request{Op: OpGitHubListPRs, Repo: "app"})); !strings.Contains(rep.Refused, "it has no `github` system") {
			t.Errorf("reply: %+v", rep)
		}
	})
}

// A DRAFT IS ASKED FOR AS ONE: GitHub opens the pull request as a draft, and the answer says so.
func TestOpenGitHubPROpensADraft(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte("[]"))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"number": 9, "html_url": "https://github.com/example/app/pull/9"}`))
	}))
	defer srv.Close()
	out, err := OpenGitHubPR(context.Background(), srv.Client(), "tok", GitHubPR{API: srv.URL, Owner: "example",
		Repo: "app", Head: "verse/sso", Base: "master", Title: "sso", Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if posted["draft"] != true {
		t.Errorf("opened with %v, want draft: true", posted)
	}
	if !strings.Contains(out, "opened draft pull request #9") {
		t.Errorf("said %q", out)
	}
}

// A PULL REQUEST'S CHECKS ARE READ WITH IT: failing and unfinished by name and link, passing and
// skipped counted, check runs and commit statuses alike.
func TestReadGitHubPRSaysItsHeadsChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/app/pulls/9":
			w.Write([]byte(`{"number": 9, "title": "sso", "state": "open", "draft": true, "html_url": "u",
				"user": {"login": "ana"}, "base": {"ref": "master"}, "head": {"ref": "verse/deliver/sso", "sha": "abcdef1234567"}}`))
		case "/repos/acme/app/pulls/9/reviews":
			w.Write([]byte(`[]`))
		case "/repos/acme/app/commits/abcdef1234567/check-runs":
			w.Write([]byte(`{"check_runs": [
				{"name": "Control Backend CI", "status": "completed", "conclusion": "failure", "html_url": "https://ci/1"},
				{"name": "Control Frontend CI", "status": "in_progress", "html_url": "https://ci/2"},
				{"name": "Detect Changes", "status": "completed", "conclusion": "success"},
				{"name": "IaC", "status": "completed", "conclusion": "skipped"}]}`))
		case "/repos/acme/app/commits/abcdef1234567/status":
			w.Write([]byte(`{"statuses": [{"context": "sandbox", "state": "success"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	out, err := ReadGitHubPR(context.Background(), srv.Client(), "tok", srv.URL, "acme", "app", 9)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"checks on abcdef123: 1 failing, 1 not done, 2 passing, 1 skipped",
		"failing: Control Backend CI (failure) https://ci/1",
		"not done: Control Frontend CI (in_progress) https://ci/2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the read does not say %q:\n%s", want, out)
		}
	}
}

// A TASK COMMENTS ON A PULL REQUEST through the runner, on its conversation and in an inline
// comment's thread, with the runner's token. The forge is a test server answering as GitHub
// does: pull request 71 with review comments 900 (a thread's first) and 902 (a reply in it), a
// review comment 950 on pull request 72, issue 5, and conversation comment 800. Each refusal
// names what the number or the id is, and where it names a verb, which.
func TestATaskCommentsOnAPullRequestThroughTheRunner(t *testing.T) {
	type posted struct{ path, body string }
	var posts []posted
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer the-runners-own" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		notFound := func() {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message": "Not Found"}`))
		}
		review := func(id, inReplyTo int64, pr int) {
			c := map[string]any{"id": id, "pull_request_url": fmt.Sprintf("https://api.github.com/repos/acme/app/pulls/%d", pr),
				"html_url": fmt.Sprintf("https://github.com/acme/app/pull/%d#discussion_r%d", pr, id)}
			if inReplyTo != 0 {
				c["in_reply_to_id"] = inReplyTo
			}
			_ = json.NewEncoder(w).Encode(c)
		}
		if r.Method == http.MethodPost {
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			posts = append(posts, posted{r.URL.Path, in["body"]})
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"html_url": "https://github.com/acme/app/posted"})
			return
		}
		switch r.URL.Path {
		case "/repos/acme/app/pulls/71":
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 71, "state": "closed", "merged": true})
		case "/repos/acme/app/issues/5":
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 5})
		case "/repos/acme/app/pulls/comments/900":
			review(900, 0, 71)
		case "/repos/acme/app/pulls/comments/902":
			review(902, 900, 71)
		case "/repos/acme/app/pulls/comments/950":
			review(950, 0, 72)
		case "/repos/acme/app/issues/comments/800":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 800})
		default:
			notFound()
		}
	}))
	defer forge.Close()

	ws := githubTree("acme/app", "https://github.com/acme/app.git")
	s := &Server{Log: io.Discard, Services: Services{GitHubAPI: forge.URL}, Ceilings: performs("github")}
	tk := Task{ID: "c1", Workspace: ws, Launch: within([]string{"acme/app"}, "github"), Credential: func(workspace.Repo) (string, error) { return "the-runners-own", nil }}
	ask := func(req Request) Reply { return s.answer(tk, frame(t, req)) }

	t.Run("a comment on a pull request's conversation, merged or not", func(t *testing.T) {
		posts = nil
		rep := ask(Request{Op: OpGitHubCommentPR, Repo: "app", PR: "71", Body: "fixed in abc123"})
		if !rep.OK || rep.Out != "commented on pull request #71 at acme/app: https://github.com/acme/app/posted" {
			t.Fatalf("reply: %+v", rep)
		}
		if len(posts) != 1 || posts[0] != (posted{"/repos/acme/app/issues/71/comments", "fixed in abc123"}) {
			t.Errorf("posted %v", posts)
		}
	})
	t.Run("a reply in a thread", func(t *testing.T) {
		posts = nil
		rep := ask(Request{Op: OpGitHubReplyComment, Repo: "app", PR: "71", Comment: "900", Body: "renamed"})
		if !rep.OK || rep.Out != "replied in the thread of comment 900 on pull request #71 at acme/app: https://github.com/acme/app/posted" {
			t.Fatalf("reply: %+v", rep)
		}
		if len(posts) != 1 || posts[0] != (posted{"/repos/acme/app/pulls/71/comments/900/replies", "renamed"}) {
			t.Errorf("posted %v", posts)
		}
	})
	t.Run("a reply to a reply lands on the thread's first comment", func(t *testing.T) {
		posts = nil
		rep := ask(Request{Op: OpGitHubReplyComment, Repo: "app", PR: "71", Comment: "902", Body: "done"})
		if !rep.OK || rep.Out != "replied in the thread of comment 900 (902 is a reply in it) on pull request #71 at acme/app: https://github.com/acme/app/posted" {
			t.Fatalf("reply: %+v", rep)
		}
		if len(posts) != 1 || posts[0].path != "/repos/acme/app/pulls/71/comments/900/replies" {
			t.Errorf("posted %v", posts)
		}
	})
	for _, tc := range []struct {
		name    string
		req     Request
		refused string
	}{
		{"an issue's number to comment-pr", Request{Op: OpGitHubCommentPR, PR: "5", Body: "x"}, "#5 is an issue: comment on it with github-issue-comment"},
		{"a number that is nothing", Request{Op: OpGitHubCommentPR, PR: "6", Body: "x"}, "there is no pull request #6 at acme/app"},
		{"a pr that is not a number", Request{Op: OpGitHubCommentPR, PR: "seven", Body: "x"}, `"seven" is not a pull request number`},
		{"a conversation comment to reply-comment", Request{Op: OpGitHubReplyComment, PR: "71", Comment: "800", Body: "x"},
			"comment 800 is on a conversation, not in a review thread, and has no thread to reply in: comment on the pull request with github-comment-pr"},
		{"an id that is no comment", Request{Op: OpGitHubReplyComment, PR: "71", Comment: "801", Body: "x"}, "no comment 801 at acme/app"},
		{"a comment on another pull request", Request{Op: OpGitHubReplyComment, PR: "71", Comment: "950", Body: "x"}, "comment 950 is on pull request #72, not #71"},
		{"a comment id that is not a number", Request{Op: OpGitHubReplyComment, PR: "71", Comment: "abc", Body: "x"}, `"abc" is not a comment id`},
		{"an empty body", Request{Op: OpGitHubCommentPR, PR: "71", Body: " "}, "a comment needs a body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts = nil
			tc.req.Repo = "app"
			if rep := ask(tc.req); rep.Refused != tc.refused {
				t.Errorf("reply: %+v, want refused %q", rep, tc.refused)
			}
			if len(posts) != 0 {
				t.Errorf("a refusal posted %v", posts)
			}
		})
	}
	t.Run("a row pinning the task to one pull request", func(t *testing.T) {
		l := within([]string{"acme/app"}, "github")
		var only71 task.Row
		if err := only71.FromTony([]byte(`{match: {system: github, verb: reply-comment, pr: 71}, then: allow}`)); err != nil {
			t.Fatal(err)
		}
		l.Autonomy = task.Autonomy{only71, {Name: "nothing-else", Then: task.Then{Mode: task.ThenDeny}}}
		pin := Task{ID: "c2", Workspace: ws, Launch: l, Credential: tk.Credential}
		if rep := s.answer(pin, frame(t, Request{Op: OpGitHubReplyComment, Repo: "app", PR: "71", Comment: "900", Body: "ok"})); !rep.OK {
			t.Errorf("a reply on 71: %+v", rep)
		}
		if rep := s.answer(pin, frame(t, Request{Op: OpGitHubReplyComment, Repo: "app", PR: "72", Comment: "950", Body: "ok"})); !strings.Contains(rep.Refused, "nothing-else") {
			t.Errorf("a reply on 72: %+v, want the deny row named", rep)
		}
	})
	t.Run("a ceiling that lists verbs without them", func(t *testing.T) {
		reads := &Server{Log: io.Discard, Services: s.Services, Ceilings: map[string]systems.Ceiling{"github": {{Verbs: map[string][]systems.Shape{"read-pr": {{}}}}}}}
		if rep := reads.answer(tk, frame(t, Request{Op: OpGitHubCommentPR, Repo: "app", PR: "71", Body: "x"})); !strings.Contains(rep.Refused, "ceiling") {
			t.Errorf("reply: %+v, want the ceiling's refusal", rep)
		}
	})
	t.Run("a runner with no github system", func(t *testing.T) {
		none := &Server{Log: io.Discard, Services: s.Services, Ceilings: performs("github-issue")}
		if rep := none.answer(tk, frame(t, Request{Op: OpGitHubCommentPR, Repo: "app", PR: "71", Body: "x"})); !strings.Contains(rep.Refused, "it has no `github` system") {
			t.Errorf("reply: %+v", rep)
		}
	})
}
