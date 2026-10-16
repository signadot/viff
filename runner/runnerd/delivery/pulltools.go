package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/signadot/verse/runner/runnerd/verserunner/agent"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// The two pull request tools. A pull request is the one forge write that is not a ref, and it is
// a TOOL rather than an option on a push because a task decides it: opening one is a proposal
// with a title and a body the task writes, updating one is a decision that the proposal changed,
// and a charter gates each on its own (`verbs: [create]` or a `pull` resource).
const (
	CreatePullToolName = "create-pr"
	UpdatePullToolName = "update-pr"
)

// PullTools are offered where a repository the task may push to is at a forge that has pull
// requests: nothing, for a local path or another forge.
func (c *Client) PullTools() []agent.Tool {
	if c.Conn == nil {
		return nil
	}
	var names []string
	for _, t := range c.Workspace.Trees {
		if _, _, _, err := PullAPI(t.Repo.Repo); err == nil && t.Repo.Writes() { // as Offered states
			names = append(names, t.Repo.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return []agent.Tool{c.pullTool(CreatePullToolName, task.Create, names), c.pullTool(UpdatePullToolName, task.Write, names)}
}

// pullSpec is the pull request a tool call asks for, resolved against the workspace: which
// repository, from which branch (the task's own when none is named, and only one the task may
// push), into which base (the checkout's base when none is named), with the title and body the
// task wrote.
func pullSpec(ws *workspace.Workspace, names []string, in map[string]any) (*workspace.Tree, PullSpec, error) {
	name, _ := in["repo"].(string)
	if name == "" && len(names) == 1 {
		name = names[0]
	}
	t := ws.Tree(name)
	if t == nil || !t.Repo.Writes() {
		return nil, PullSpec{}, fmt.Errorf("there is no repository %q in this workspace with pull requests to open — "+
			"it has: %s", name, strings.Join(names, ", "))
	}
	api, owner, repo, err := PullAPI(t.Repo.Repo)
	if err != nil {
		return nil, PullSpec{}, err
	}
	head, _ := in["head"].(string)
	head = strings.TrimPrefix(strings.TrimSpace(head), "refs/heads/")
	if head == "" {
		head = ws.Branch
	}
	if _, _, err := t.Repo.Permit(head, false); err != nil {
		return nil, PullSpec{}, fmt.Errorf("a pull request is from a branch this task may push, and %v", err)
	}
	base, _ := in["base"].(string)
	base = strings.TrimPrefix(strings.TrimSpace(base), "refs/heads/")
	if base == "" {
		base = t.Repo.Base
	}
	title, _ := in["title"].(string)
	body, _ := in["body"].(string)
	return t, PullSpec{API: api, Owner: owner, Repo: repo, Head: head, Base: base, Title: title, Body: body}, nil
}

func (c *Client) pullTool(name string, verb task.Verb, names []string) agent.Tool {
	ws := c.Workspace
	var required []string
	if len(names) > 1 {
		required = []string{"repo"}
	}
	what := "Open a pull request from a branch this task pushed into a base branch, with the title and body given; " +
		"if one is already open from that branch, it is found and reported rather than opened twice."
	if name == UpdatePullToolName {
		what = "Update the open pull request from a branch this task pushed: its title, body or base, as given. " +
			"Refused when none is open from that branch."
	}
	return agent.Tool{
		Name: name,
		Description: what + " `head` is the branch, and omitted means your own branch, `" + ws.Branch + "`; " +
			"`base` omitted means the branch the checkout started from. Push the branch first: a pull request " +
			"proposes what a push put there. The runner holds the credential; you never see it. " +
			"Repositories: " + strings.Join(names, ", ") + ".",
		Params: map[string]any{
			"repo":  map[string]any{"type": "string", "enum": names},
			"head":  map[string]any{"type": "string"},
			"base":  map[string]any{"type": "string"},
			"title": map[string]any{"type": "string"},
			"body":  map[string]any{"type": "string"},
		},
		Required: required,
		Derive: func(tu *task.ToolUse) {
			if tu.Input == nil {
				tu.Input = map[string]any{}
			}
			t, spec, err := pullSpec(ws, names, tu.Input)
			if err != nil {
				return // no effect: refused in Do, before any gate
			}
			// What the approver reads: which repository, from which branch, into which.
			tu.Input["repo"], tu.Input["head"], tu.Input["base"] = t.Repo.Name, spec.Head, spec.Base
			tu.Effects = append(tu.Effects, task.Effect{Verb: verb, Resource: task.PullRes(spec.Owner + "/" + spec.Repo + ":" + spec.Base)})
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t, spec, err := pullSpec(ws, names, tu.Input)
			if err != nil {
				return "no pull request: " + err.Error(), true, nil
			}
			// The runner performs it, with its own token: this side names the pull request and
			// reads what happened.
			return relay(ask(c.Conn, Request{Op: "pull", Repo: t.Repo.Name, Branch: spec.Head,
				Base: spec.Base, Title: spec.Title, Body: spec.Body, Update: name == UpdatePullToolName}))
		},
	}
}
