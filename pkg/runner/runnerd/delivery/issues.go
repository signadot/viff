package delivery

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/signadot/verse/pkg/io/linear"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/task"
)

// AN ISSUE TRACKER IS A SYSTEM WITH A NAME, and each act on one is a verb of it. GitHub's issues
// and a Linear team are remote trackers, where every write is a call made with a credential, so
// each is performed here, by the runner, over the task's channel: `github-issue.create`,
// `github-issue.comment`, `github-issue.close`, and `linear-issue.create`, `.comment`, `.close`.
// A repository's own git-issue tracker is distributed, the way its code is: a task files,
// comments and closes in its checkout with no credential, in its own process, as it commits;
// `git-issue.push` is what sends the tracker to the remote and is the runner's act, as a push
// is, and `git-issue.pull` brings the remote's tracker into the checkout.
//
// The ops are the steps' names: one word on the wire for one act.
const (
	OpGitIssuePull       = "git-issue.pull"
	OpGitIssuePush       = "git-issue.push"
	OpGitHubIssueCreate  = "github-issue.create"
	OpGitHubIssueComment = "github-issue.comment"
	OpGitHubIssueClose   = "github-issue.close"
	OpLinearIssueCreate  = "linear-issue.create"
	OpLinearIssueComment = "linear-issue.comment"
	OpLinearIssueClose   = "linear-issue.close"
)

// gitIssuePull answers a `git-issue.pull`: the mirror's tracker up to the remote's, then the
// checkout's up to the mirror's, and the open issues as the checkout now lists them.
func (s *Server) gitIssuePull(ctx context.Context, tk Task, req Request) Reply {
	t := tk.Workspace.Tree(req.Repo)
	if t == nil {
		return Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Repo)}
	}
	if why := s.refused(tk, req.Decision, task.NewStep(task.StepGitIssuePull, map[string]string{"repo": t.Repo.Repo.Canonical()})); why != "" {
		return Reply{Refused: why}
	}
	mirror, err := s.Mirrors.FetchIssues(ctx, t.Repo.Remote)
	if err != nil {
		fmt.Fprintf(s.Log, "delivery: %s: pulling the issues of %s: %v\n", tk.ID, t.Repo.Name(), err)
		return Reply{Error: err.Error()}
	}
	moved, open, err := t.RefreshIssues(mirror)
	if err != nil {
		fmt.Fprintf(s.Log, "delivery: %s: refreshing the issues of %s: %v\n", tk.ID, t.Repo.Name(), err)
		return Reply{Error: err.Error()}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "pulled the issues of %s: ", t.Repo.Name())
	if len(moved) == 0 {
		b.WriteString("nothing new. ")
	} else {
		fmt.Fprintf(&b, "%d ref(s) moved. ", len(moved))
	}
	fmt.Fprintf(&b, "%d open:", len(open))
	for _, i := range open {
		fmt.Fprintf(&b, "\n  %s  %s", i.ID, i.Title)
	}
	if len(moved) > 0 {
		fmt.Fprintf(s.Log, "delivery: %s: pulled the issues of %s: %s\n", tk.ID, t.Repo.Name(), strings.Join(moved, ", "))
	}
	return Reply{OK: true, Out: b.String(), Updated: moved}
}

// gitIssuePush answers a `git-issue.push`: the checkout's tracker sent to the remote with the
// repository's credential (workspace.Mirrors.PushIssues).
func (s *Server) gitIssuePush(ctx context.Context, tk Task, req Request) Reply {
	t := tk.Workspace.Tree(req.Repo)
	if t == nil {
		return Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Repo)}
	}
	if why := s.refused(tk, req.Decision, task.NewStep(task.StepGitIssuePush, map[string]string{"repo": t.Repo.Repo.Canonical()})); why != "" {
		return Reply{Refused: why}
	}
	tok, err := tk.Credential(t.Repo)
	if err != nil {
		return Reply{Error: err.Error()}
	}
	n, refusal, err := s.Mirrors.PushIssues(ctx, t.Dir, t.Repo.Remote, workspace.TokenAuth(tok)(t.Repo.Remote))
	switch {
	case err != nil:
		return Reply{Error: err.Error()}
	case refusal != "":
		return Reply{Refused: refusal}
	case n == 0:
		return Reply{OK: true, Out: "nothing to push: the remote has every issue this checkout holds"}
	}
	out := fmt.Sprintf("pushed the issues of %s (%d ref(s)) to %s", t.Repo.Name(), n, t.Repo.Remote)
	fmt.Fprintf(s.Log, "delivery: %s: %s\n", tk.ID, out)
	return Reply{OK: true, Out: out}
}

// githubIssue answers a `github-issue.create`, `.comment` or `.close` at the forge with the
// runner's token for the repository.
func (s *Server) githubIssue(ctx context.Context, tk Task, req Request) Reply {
	t := tk.Workspace.Tree(req.Repo)
	if t == nil {
		return Reply{Error: fmt.Sprintf("no repository %q in this workspace", req.Repo)}
	}
	if _, has := s.Ceilings["github-issue"]; !has {
		return Reply{Refused: "this runner files nothing at GitHub for its tasks: it has no `github-issue` system"}
	}
	fields := map[string]string{"repo": t.Repo.Repo.Canonical()}
	if req.Op != OpGitHubIssueCreate {
		fields["issue"] = req.Issue
	}
	if why := s.refused(tk, req.Decision, task.NewStep(req.Op, fields)); why != "" {
		return Reply{Refused: why}
	}
	owner, name, err := RepoPath(t.Repo.Remote)
	if err != nil {
		return Reply{Refused: err.Error()}
	}
	tok, err := tk.Credential(t.Repo)
	if err != nil {
		return Reply{Error: err.Error()}
	}
	issues := fmt.Sprintf("%s/repos/%s/%s/issues", s.Services.GitHubAPI, owner, name)
	var out string
	switch req.Op {
	case OpGitHubIssueCreate:
		var issue struct {
			Number  int    `json:"number"`
			HTMLURL string `json:"html_url"`
		}
		if err := githubAPI(ctx, s.http(), tok, http.MethodPost, issues, map[string]string{"title": req.Title, "body": req.Body}, &issue); err != nil {
			return Reply{Error: err.Error()}
		}
		out = fmt.Sprintf("opened issue #%d at %s/%s: %s", issue.Number, owner, name, issue.HTMLURL)
	case OpGitHubIssueComment, OpGitHubIssueClose:
		n, err := strconv.Atoi(req.Issue)
		if err != nil || n <= 0 {
			return Reply{Refused: fmt.Sprintf("%q is not an issue number", req.Issue)}
		}
		one := fmt.Sprintf("%s/%d", issues, n)
		if req.Op == OpGitHubIssueComment {
			var c struct {
				HTMLURL string `json:"html_url"`
			}
			if err := githubAPI(ctx, s.http(), tok, http.MethodPost, one+"/comments", map[string]string{"body": req.Body}, &c); err != nil {
				return Reply{Error: err.Error()}
			}
			out = fmt.Sprintf("commented on issue #%d at %s/%s: %s", n, owner, name, c.HTMLURL)
		} else {
			var i struct {
				State   string `json:"state"`
				HTMLURL string `json:"html_url"`
			}
			if err := githubAPI(ctx, s.http(), tok, http.MethodPatch, one, map[string]string{"state": "closed"}, &i); err != nil {
				return Reply{Error: err.Error()}
			}
			out = fmt.Sprintf("closed issue #%d at %s/%s: %s", n, owner, name, i.HTMLURL)
		}
	}
	fmt.Fprintf(s.Log, "delivery: %s: %s\n", tk.ID, out)
	return Reply{OK: true, Out: out}
}

// linearIssue answers a `linear-issue.create`, `.comment` or `.close` with the runner's Linear
// credential. A runner without one answers so, naming how it gets one.
func (s *Server) linearIssue(ctx context.Context, tk Task, req Request) Reply {
	if s.Services.LinearAuth == "" {
		return Reply{Error: "this runner has no Linear credential (LINEAR_API_KEY), so it cannot act on a Linear issue"}
	}
	fields := map[string]string{}
	if req.Op == OpLinearIssueCreate {
		fields["team"] = strings.ToUpper(req.Team)
	} else {
		fields["issue"] = strings.ToUpper(req.Issue)
	}
	if why := s.refused(tk, req.Decision, task.NewStep(req.Op, fields)); why != "" {
		return Reply{Refused: why}
	}
	auth, base, hc := s.Services.LinearAuth, s.Services.LinearAPI, s.http()
	var out string
	switch req.Op {
	case OpLinearIssueCreate:
		created, err := linear.CreateIssue(ctx, auth, base, hc, req.Team, req.Title, req.Body)
		if err != nil {
			return Reply{Error: err.Error()}
		}
		out = fmt.Sprintf("created %s: %s", created.Identifier, created.URL)
	case OpLinearIssueComment:
		c, err := linear.Comment(ctx, auth, base, hc, req.Issue, req.Body)
		if err != nil {
			return Reply{Error: err.Error()}
		}
		out = fmt.Sprintf("commented on %s: %s", strings.ToUpper(req.Issue), c.URL)
	case OpLinearIssueClose:
		c, err := linear.CloseIssue(ctx, auth, base, hc, req.Issue)
		if err != nil {
			return Reply{Error: err.Error()}
		}
		out = fmt.Sprintf("closed %s (%s): %s", c.Identifier, c.State, c.URL)
	}
	fmt.Fprintf(s.Log, "delivery: %s: %s\n", tk.ID, out)
	return Reply{OK: true, Out: out}
}

func (s *Server) http() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}
