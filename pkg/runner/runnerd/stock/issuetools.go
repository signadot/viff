package stock

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/delivery"
	"github.com/signadot/verse/pkg/runner/runnerd/verse"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// AN ISSUE TRACKER IS A SYSTEM WITH A NAME, and each tool here is one verb of one system
// (package systems): `git-issue` (a repository's own tracker: create, comment, close, pull,
// push), `github-issue` (the forge's: create, comment, close) and `linear-issue` (a Linear
// team's: create, comment, close). Each verb is a step the task's autonomy decides
// (task.StepGitIssueCreate, …). The remote trackers' verbs the runner performs with a credential.
// A repository's own tracker is written in the checkout, in the task's own process, with no
// credential, as a commit is; `git-issue-push` makes the issues leave, and `git-issue-pull`
// reads the remote's.
var (
	gitIssueCreateTool     = systems.Tool("git-issue", "create")
	gitIssueCommentTool    = systems.Tool("git-issue", "comment")
	gitIssueCloseTool      = systems.Tool("git-issue", "close")
	gitIssuePullTool       = systems.Tool("git-issue", "pull")
	gitIssuePushTool       = systems.Tool("git-issue", "push")
	githubIssueCreateTool  = systems.Tool("github-issue", "create")
	githubIssueCommentTool = systems.Tool("github-issue", "comment")
	githubIssueCloseTool   = systems.Tool("github-issue", "close")
	linearIssueCreateTool  = systems.Tool("linear-issue", "create")
	linearIssueCommentTool = systems.Tool("linear-issue", "comment")
	linearIssueCloseTool   = systems.Tool("linear-issue", "close")
)

// issueTools are the issue tools a task has: git-issue's for the repositories the launch names
// under its environment's `git-issue`, github-issue's for the repositories of its `git` where the runner's
// forge is a GitHub (github), and linear-issue's where the runner holds a Linear credential
// (linear-issue).
func issueTools(cl *delivery.Client, ws *workspace.Workspace, trackers []runner.Tracker, github, linear bool) []verse.Tool {
	var all, forge []string
	for _, t := range ws.Trees {
		if slices.ContainsFunc(trackers, func(tr runner.Tracker) bool { return tr.Repo == t.Repo.Repo.Repo }) {
			all = append(all, t.Repo.Name())
		}
		if github {
			forge = append(forge, t.Repo.Name())
		}
	}
	var out []verse.Tool
	if len(all) > 0 {
		out = append(out, gitIssueCreate(ws, all), gitIssueComment(ws, all), gitIssueClose(ws, all),
			gitIssuePull(cl, ws, all), gitIssuePush(cl, ws, all))
	}
	if len(forge) > 0 {
		out = append(out, githubIssueCreate(cl, ws, forge),
			githubIssueVerb(cl, ws, forge, githubIssueCommentTool, delivery.OpGitHubIssueComment, task.StepGitHubIssueComment),
			githubIssueVerb(cl, ws, forge, githubIssueCloseTool, delivery.OpGitHubIssueClose, task.StepGitHubIssueClose))
	}
	if linear {
		out = append(out, linearIssueCreate(cl),
			linearIssueVerb(cl, linearIssueCommentTool, delivery.OpLinearIssueComment, task.StepLinearIssueComment),
			linearIssueVerb(cl, linearIssueCloseTool, delivery.OpLinearIssueClose, task.StepLinearIssueClose))
	}
	return out
}

// repoParams is `repo` among names, and the verb's own parameters beside it.
func repoParams(names []string, own map[string]any) map[string]any {
	all := map[string]any{"repo": map[string]any{"type": "string", "enum": names}}
	for k, v := range own {
		all[k] = v
	}
	return all
}

// requiredWith is the required parameters: `repo` when there is more than one to name, and rest.
func requiredWith(names []string, rest ...string) []string {
	if len(names) > 1 {
		return append([]string{"repo"}, rest...)
	}
	return rest
}

func noSuchRepo(in map[string]any, names []string) string {
	return fmt.Sprintf("there is no repository %q here for this; it has: %s", str(in, "repo"), strings.Join(names, ", "))
}

// gitIssueCreate files an issue in a repository's own tracker, in this checkout: a ref, as `git
// issue create` here would write, and nothing has left until `git-issue-push`. In the task's
// process, with no credential, as a commit is; a step all the same, which a row decides.
func gitIssueCreate(ws *workspace.Workspace, names []string) verse.Tool {
	return verse.Tool{
		Name: gitIssueCreateTool,
		Description: "File an issue in a repository's own git-issue tracker (the issues `git issue` keeps as refs " +
			"in the repository), with the title and body given. It is written here, in the checkout, like a " +
			"commit: `git-issue-push` is what sends the tracker to the remote. What you read back is the " +
			"issue's id and its ref. Repositories: " + strings.Join(names, ", ") + ".",
		Params: repoParams(names, map[string]any{
			"title": map[string]any{"type": "string"},
			"body":  map[string]any{"type": "string"},
		}),
		Required: requiredWith(names, "title"),
		Step:     task.StepGitIssueCreate,
		Fields: func(in map[string]any) (map[string]string, bool) {
			t := treeOf(ws, names, in)
			if t == nil {
				return nil, false
			}
			return map[string]string{"repo": t.Repo.Repo.Canonical()}, true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t := treeOf(ws, names, tu.Input)
			if t == nil {
				return noSuchRepo(tu.Input, names), true, nil
			}
			issue, err := t.CreateIssue(str(tu.Input, "title"), str(tu.Input, "body"))
			if err != nil {
				if strings.Contains(err.Error(), "needs a title") {
					return "not filed: an issue needs a title", true, nil
				}
				return "", false, err
			}
			return fmt.Sprintf("filed issue %s in %s (%s); it is in this checkout until `%s` sends the tracker to the remote",
				issue.ID, t.Repo.Name(), issue.Ref, gitIssuePushTool), false, nil
		},
	}
}

// localIssueVerb is a verb on the checkout's own tracker, by the issue's id: performed here, as
// a commit is, and the step a row decides by the repo and the issue.
func localIssueVerb(ws *workspace.Workspace, names []string, name, step, what string, params map[string]any, required []string,
	do func(t *workspace.Tree, in map[string]any) (string, bool, error)) verse.Tool {
	params["issue"] = map[string]any{"type": "string"}
	return verse.Tool{
		Name:        name,
		Description: what + " Repositories: " + strings.Join(names, ", ") + ".",
		Params:      repoParams(names, params),
		Required:    requiredWith(names, required...),
		Step:        step,
		Fields: func(in map[string]any) (map[string]string, bool) {
			t := treeOf(ws, names, in)
			if t == nil {
				return nil, false
			}
			return map[string]string{"repo": t.Repo.Repo.Canonical(), "issue": strings.TrimSpace(str(in, "issue"))}, true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t := treeOf(ws, names, tu.Input)
			if t == nil {
				return noSuchRepo(tu.Input, names), true, nil
			}
			if strings.TrimSpace(str(tu.Input, "issue")) == "" {
				return "refused: an issue id is required", true, nil
			}
			return do(t, tu.Input)
		},
	}
}

func gitIssueComment(ws *workspace.Workspace, names []string) verse.Tool {
	return localIssueVerb(ws, names, gitIssueCommentTool, task.StepGitIssueComment,
		"Comment on an issue in a repository's own git-issue tracker, by the issue's id (or an unambiguous "+
			"prefix of it), with the body given. Written here, in the checkout: `git-issue-push` sends it.",
		map[string]any{"body": map[string]any{"type": "string"}}, []string{"issue", "body"},
		func(t *workspace.Tree, in map[string]any) (string, bool, error) {
			if strings.TrimSpace(str(in, "body")) == "" {
				return "not commented: a comment needs a body", true, nil
			}
			issue, err := t.CommentIssue(str(in, "issue"), str(in, "body"))
			if err != nil {
				return "", false, err
			}
			return fmt.Sprintf("commented on issue %s in %s; it is in this checkout until `%s`", issue.ID, t.Repo.Name(), gitIssuePushTool), false, nil
		})
}

func gitIssueClose(ws *workspace.Workspace, names []string) verse.Tool {
	return localIssueVerb(ws, names, gitIssueCloseTool, task.StepGitIssueClose,
		"Close an issue in a repository's own git-issue tracker, by the issue's id (or an unambiguous prefix "+
			"of it), naming the commit that closed it when there is one. Written here, in the checkout: "+
			"`git-issue-push` sends it.",
		map[string]any{"commit": map[string]any{"type": "string"}}, []string{"issue"},
		func(t *workspace.Tree, in map[string]any) (string, bool, error) {
			issue, err := t.CloseIssue(str(in, "issue"), str(in, "commit"))
			if err != nil {
				return "", false, err
			}
			return fmt.Sprintf("closed issue %s in %s; it is in this checkout until `%s`", issue.ID, t.Repo.Name(), gitIssuePushTool), false, nil
		})
}

// gitIssuePull brings a repository's tracker into this checkout: the runner fetches the issue
// refs into its mirror and sets them here, under their own names, so `git issue list` and `git
// issue show <id>` in the checkout read what the remote holds. A read, and a step a row decides.
func gitIssuePull(cl *delivery.Client, ws *workspace.Workspace, names []string) verse.Tool {
	return verse.Tool{
		Name: gitIssuePullTool,
		Step: task.StepGitIssuePull,
		Fields: func(in map[string]any) (map[string]string, bool) {
			t := treeOf(ws, names, in)
			if t == nil {
				return nil, false
			}
			return map[string]string{"repo": t.Repo.Repo.Canonical()}, true
		},
		Description: "Bring a repository's git-issue tracker into this checkout: the runner fetches its issue " +
			"refs (`refs/git-issues/*`) from the remote and sets them here, so `git issue list` and `git issue " +
			"show <id>` in the checkout read what the remote holds. The result lists the open issues, id and " +
			"title. Your branches and working tree are not touched. Repositories: " + strings.Join(names, ", ") + ".",
		Params:   repoParams(names, nil),
		Required: requiredWith(names),
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t := treeOf(ws, names, tu.Input)
			if t == nil {
				return noSuchRepo(tu.Input, names), true, nil
			}
			rep, err := cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpGitIssuePull, Repo: t.Repo.Name()})
			switch {
			case err != nil:
				return "", false, fmt.Errorf("asking the runner to pull the issues of %s: %w", t.Repo.Name(), err)
			case rep.Error != "":
				return "not pulled: " + rep.Error + "; the issues here are as they were", true, nil
			}
			return rep.Out, false, nil
		},
	}
}

// gitIssuePush sends this checkout's tracker to the repository's remote: every issue the task
// filed or changed here, as one push the runner makes with its credential. It is the act that
// makes an issue leave, and the step a gate decides.
func gitIssuePush(cl *delivery.Client, ws *workspace.Workspace, names []string) verse.Tool {
	return verse.Tool{
		Name: gitIssuePushTool,
		Description: "Send this checkout's git-issue tracker to the repository's remote: every issue filed or " +
			"changed here (`git-issue-create`, or `git issue` in your shell) goes, as one push the runner makes " +
			"with its own credential. Refused when an issue was edited at the remote too: pull first. " +
			"Repositories: " + strings.Join(names, ", ") + ".",
		Params:   repoParams(names, nil),
		Required: requiredWith(names),
		Step:     task.StepGitIssuePush,
		Fields: func(in map[string]any) (map[string]string, bool) {
			t := treeOf(ws, names, in)
			if t == nil {
				return nil, false
			}
			return map[string]string{"repo": t.Repo.Repo.Canonical()}, true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t := treeOf(ws, names, tu.Input)
			if t == nil {
				return noSuchRepo(tu.Input, names), true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpGitIssuePush, Repo: t.Repo.Name()}))
		},
	}
}

// githubIssueCreate opens an issue at the GitHub repository a workspace repository is on, with the
// runner's token, as `github-create-pr` and `github-update-pr` do. Offered for a repository at the
// forge whatever the task may push to it: an issue is not a branch.
func githubIssueCreate(cl *delivery.Client, ws *workspace.Workspace, names []string) verse.Tool {
	return verse.Tool{
		Name: githubIssueCreateTool,
		Description: "Open an issue at the GitHub repository, with the title and body given. The runner opens it " +
			"with its own token; what you read back is the issue's number and url. " +
			"Repositories: " + strings.Join(names, ", ") + ".",
		Params: repoParams(names, map[string]any{
			"title": map[string]any{"type": "string"},
			"body":  map[string]any{"type": "string"},
		}),
		Required: requiredWith(names, "title"),
		Step:     task.StepGitHubIssueCreate,
		Fields: func(in map[string]any) (map[string]string, bool) {
			t := treeOf(ws, names, in)
			if t == nil || strings.TrimSpace(str(in, "title")) == "" {
				return nil, false
			}
			return map[string]string{"repo": t.Repo.Repo.Canonical()}, true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t := treeOf(ws, names, tu.Input)
			if t == nil {
				return noSuchRepo(tu.Input, names), true, nil
			}
			title := str(tu.Input, "title")
			if strings.TrimSpace(title) == "" {
				return "not filed: an issue needs a title", true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpGitHubIssueCreate, Repo: t.Repo.Name(), Title: title, Body: str(tu.Input, "body")}))
		},
	}
}

// githubIssueVerb is a comment on or a close of an issue at the forge, by number, performed by
// the runner with its token.
func githubIssueVerb(cl *delivery.Client, ws *workspace.Workspace, names []string, name, op, step string) verse.Tool {
	what := "Close an issue at the GitHub repository, by its number. The runner closes it with its own token."
	params := map[string]any{"issue": map[string]any{"type": "integer"}}
	required := []string{"issue"}
	if op == delivery.OpGitHubIssueComment {
		what = "Comment on an issue at the GitHub repository, by its number, with the body given. The runner " +
			"comments with its own token; what you read back is the comment's url."
		params["body"] = map[string]any{"type": "string"}
		required = append(required, "body")
	}
	return verse.Tool{
		Name:        name,
		Description: what + " Repositories: " + strings.Join(names, ", ") + ".",
		Params:      repoParams(names, params),
		Required:    requiredWith(names, required...),
		Step:        step,
		Fields: func(in map[string]any) (map[string]string, bool) {
			t := treeOf(ws, names, in)
			n, ok := issueNumber(in)
			if t == nil || !ok {
				return nil, false
			}
			return map[string]string{"repo": t.Repo.Repo.Canonical(), "issue": strconv.Itoa(n)}, true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t := treeOf(ws, names, tu.Input)
			if t == nil {
				return noSuchRepo(tu.Input, names), true, nil
			}
			n, ok := issueNumber(tu.Input)
			if !ok {
				return "refused: an issue number is required", true, nil
			}
			if op == delivery.OpGitHubIssueComment && strings.TrimSpace(str(tu.Input, "body")) == "" {
				return "not commented: a comment needs a body", true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: op, Repo: t.Repo.Name(), Issue: strconv.Itoa(n), Body: str(tu.Input, "body")}))
		},
	}
}

// issueNumber is the issue a call names at the forge, as a number, however the model spelled it.
func issueNumber(in map[string]any) (int, bool) { return numberIn(in, "issue") }

// numberIn is a number at the forge a call names under key (an issue, a pull request), however
// the model spelled it: an integer, a float that is one, or a string with or without a `#`.
func numberIn(in map[string]any, key string) (int, bool) {
	switch v := in[key].(type) {
	case int:
		return v, v > 0
	case int64:
		return int(v), v > 0
	case float64:
		return int(v), v > 0 && v == float64(int(v))
	case string:
		n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(v), "#"))
		return n, err == nil && n > 0
	}
	return 0, false
}

// linearIssueCreate creates a Linear issue in a team named by its key, with the credential the
// runner holds. It needs no repository: a tracker is not a checkout.
func linearIssueCreate(cl *delivery.Client) verse.Tool {
	return verse.Tool{
		Name: linearIssueCreateTool,
		Description: "Create a Linear issue in a team, named by its key (`ENG`), with the title and description " +
			"given. The runner creates it with its own credential; what you read back is the issue's " +
			"identifier (`ENG-42`) and url.",
		Params: map[string]any{
			"team":        map[string]any{"type": "string"},
			"title":       map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
		},
		Required: []string{"team", "title"},
		Step:     task.StepLinearIssueCreate,
		Fields: func(in map[string]any) (map[string]string, bool) {
			team := strings.ToUpper(strings.TrimSpace(str(in, "team")))
			if team == "" || strings.TrimSpace(str(in, "title")) == "" {
				return nil, false
			}
			return map[string]string{"team": team}, true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			team, title := strings.ToUpper(strings.TrimSpace(str(tu.Input, "team"))), str(tu.Input, "title")
			switch {
			case team == "":
				return "not filed: a Linear issue needs a team key (ENG, …)", true, nil
			case strings.TrimSpace(title) == "":
				return "not filed: an issue needs a title", true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpLinearIssueCreate, Team: team, Title: title, Body: str(tu.Input, "description")}))
		},
	}
}

// linearIssueVerb is a comment on or a close of a Linear issue, by identifier, performed by the
// runner with its credential.
func linearIssueVerb(cl *delivery.Client, name, op, step string) verse.Tool {
	what := "Close a Linear issue, by its identifier (`ENG-42`): it moves to its team's completed state. The " +
		"runner does it with its own credential."
	params := map[string]any{"issue": map[string]any{"type": "string"}}
	required := []string{"issue"}
	if op == delivery.OpLinearIssueComment {
		what = "Comment on a Linear issue, by its identifier (`ENG-42`), with the body given. The runner comments " +
			"with its own credential; what you read back is the comment's url."
		params["body"] = map[string]any{"type": "string"}
		required = append(required, "body")
	}
	return verse.Tool{
		Name: name, Description: what, Params: params, Required: required,
		Step: step,
		Fields: func(in map[string]any) (map[string]string, bool) {
			id := strings.ToUpper(strings.TrimSpace(str(in, "issue")))
			if id == "" {
				return nil, false
			}
			return map[string]string{"issue": id}, true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			id := strings.ToUpper(strings.TrimSpace(str(tu.Input, "issue")))
			switch {
			case id == "":
				return "refused: an issue identifier is required (ENG-42)", true, nil
			case op == delivery.OpLinearIssueComment && strings.TrimSpace(str(tu.Input, "body")) == "":
				return "not commented: a comment needs a body", true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: op, Issue: id, Body: str(tu.Input, "body")}))
		},
	}
}
