package delivery

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd/verserunner/agent"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// Client is a task process's end of the channel: the workspace the task works in, the launch
// whose provenance its commits carry, and the connection to the runner that holds the
// credential. Its tools are the whole of what the task may ask for.
type Client struct {
	Conn      net.Conn
	Workspace *workspace.Workspace
	// Launch is the task as it was launched, read when a push needs its provenance: the tools
	// are built before the launch arrives.
	Launch func() task.Launch
}

// Attach opens the task process's end of the channel, inherited as descriptor fd, for the
// workspace reopened in that process. The launch is read when a tool runs, since one process is
// one task and the tools are built before the launch arrives.
func Attach(fd uintptr, ws *workspace.Workspace, launch func() task.Launch) (*Client, error) {
	conn, err := net.FileConn(os.NewFile(fd, "deliver"))
	if err != nil {
		return nil, fmt.Errorf("the delivery channel on fd %d: %w", fd, err)
	}
	return &Client{Conn: conn, Workspace: ws, Launch: launch}, nil
}

// Tools is what this task may ask its runner for: push, where some repository permits one;
// create-pr and update-pr, where one of those is at a forge; and fetch, where there is a
// repository at all. Each is an ordinary tool of the task's loop — the vector gates it, the read
// plane narrates it — and any may be called as many times as the work needs.
func (c *Client) Tools() []agent.Tool {
	var out []agent.Tool
	if t, ok := c.PushTool(); ok {
		out = append(out, t)
	}
	out = append(out, c.PullTools()...)
	if t, ok := c.FetchTool(); ok {
		out = append(out, t)
	}
	return out
}

// PushToolName is what the agent calls it, and the name is load-bearing rather than cosmetic:
// task.ToolUse.isPush() reads it, so a `onPush: true` gate matches an action named this whatever
// ref it carries. The effect below is what a `resources:` predicate discriminates on.
const PushToolName = "push"

// PushTool is the capability the agent exercises to send work OUT of the sandbox: a branch of
// one repository, pushed or deleted, if the spawn permits it.
//
// What the agent may say is a repository and a branch. Whether it may is the allowance's answer
// (workspace.Repo.Permit), read here so the agent is told before anything is gated, and again in
// the runner, which is the side that must not trust what the task sent. A branch a `new` pattern
// matches is the run's own and may be created, rewritten and deleted; one in `existing` may be
// updated, rewritten only when the spawn said `force`, never deleted; anything else is refused
// with the allowance in words. No branch named means the task's own.
//
// The GIT is done on the runner's side, and that is the point of the arrangement rather than an
// implementation detail. What crosses the boundary is a REQUEST and never a credential: this
// side asks "push <repo> to <branch>", the runner checks the allowance again, stages the work and
// pushes it from its mirror with its own token, and answers with what happened.
//
// false when no repository here may push at all: offering a tool whose every use is refused
// teaches an agent that the loop is broken.
func (c *Client) PushTool() (agent.Tool, bool) {
	ws := c.Workspace
	if c.Conn == nil {
		return agent.Tool{}, false
	}
	var names, allowed []string
	for _, t := range ws.Trees {
		if t.Repo.Writes() { // the condition Offered states
			names = append(names, t.Repo.Name)
			allowed = append(allowed, t.Repo.Name+" "+t.Repo.Allowance())
		}
	}
	if len(names) == 0 {
		return agent.Tool{}, false
	}
	var required []string
	if len(names) > 1 {
		required = []string{"repo"}
	}
	repoOf := func(in map[string]any) *workspace.Tree {
		name, _ := in["repo"].(string)
		if name == "" && len(names) == 1 {
			return ws.Tree(names[0])
		}
		return ws.Tree(name)
	}
	resolve := func(in map[string]any) (t *workspace.Tree, branch, ref string, force bool, err error) {
		t = repoOf(in)
		if t == nil {
			name, _ := in["repo"].(string)
			return nil, "", "", false, fmt.Errorf("there is no repository %q in this workspace that may push — "+
				"it has: %s", name, strings.Join(names, ", "))
		}
		branch, _ = in["branch"].(string)
		branch = strings.TrimSpace(branch)
		if branch == "" {
			branch = ws.Branch
		}
		del, _ := in["delete"].(bool)
		ref, force, err = t.Repo.Permit(branch, del)
		return t, branch, ref, force, err
	}
	return agent.Tool{
		Name: PushToolName,
		Description: "Push this task's COMMITTED work to a branch of a repository, or delete a branch a run " +
			"of this charter created. Commit what you want delivered first, every commit ending with the " +
			"Verse-* trailers your brief names; uncommitted changes are never delivered, and a commit " +
			"without the trailers is refused with what is missing. `branch` is where it goes; omit it to " +
			"push your own branch under its own name. What each repository permits: " +
			strings.Join(allowed, "; ") + ". The runner holds the credential; you never see it. A push may " +
			"be refused — by a supervisor, or by the remote's own rules about its history — and a refusal " +
			"is an answer, not an error to retry. You may push as many times as the work needs.",
		Params: map[string]any{
			"repo":   map[string]any{"type": "string", "enum": names},
			"branch": map[string]any{"type": "string"},
			"delete": map[string]any{"type": "boolean"},
		},
		Required: required,
		Derive: func(tu *task.ToolUse) {
			if tu.Input == nil {
				tu.Input = map[string]any{} // a push with nothing said: the task's own branch
			}
			t, _, ref, _, err := resolve(tu.Input)
			if err != nil {
				// No effect, deliberately. An action with no effect matches no capability gate,
				// so nobody is asked to approve something that cannot happen — which is what "a
				// branch outside the allowance is refused BEFORE the gate" comes to in a loop
				// where classification runs first and the executor runs last.
				return
			}
			// The approver reads which repository and the ref that will be touched, not the
			// short name the agent typed.
			tu.Input["repo"] = t.Repo.Name
			tu.Input["to"] = ref
			verb := task.Push
			if del, _ := tu.Input["delete"].(bool); del {
				// DELETE rather than PUSH, so a charter gates removing a branch on its own. A push
				// to a run's own branch is recoverable and this is the one act here that destroys
				// something, and `verbs: [delete]` is how somebody says that in a vector.
				verb = task.Delete
			}
			tu.Effects = append(tu.Effects, task.Effect{Verb: verb, Resource: task.GitRef(ref)})
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t, branch, _, _, err := resolve(tu.Input)
			if err != nil {
				// Said in the agent's vocabulary, as a refusal rather than a failure: the agent
				// re-plans with what it may do instead.
				return "not pushed: " + err.Error(), true, nil
			}
			if del, _ := tu.Input["delete"].(bool); del {
				return retire(c.Conn, t, branch)
			}
			return push(c.Conn, t, branch)
		},
	}, true
}

const FetchToolName = "fetch"

// FetchTool is the agent's way to see upstream move: the checkout's `refs/remotes/mirror/*` are
// where they were when the task began, and a task whose base moved under it — or that must
// fast-forward a branch, or wants a sibling's delivery — has no other way to learn that.
//
// It is a `fetch` and not a `pull` because what to do with a newer `mirror/main` is the agent's
// own git in its own checkout: rebase, merge, diff or nothing. The runner has no strategy to
// impose and performs none. The git that reaches the network runs on the PARENT's side, where
// the credential already is and where the mirror is fetched on every spawn anyway; what comes
// back is a list of refs that moved. So a private repository fetches exactly as a public one
// does, and no credential crosses.
//
// The effect is a `network` read of the repository's host, so a vector that asks about the
// network asks about this, and a charter that trusts reads lets it run.
func (c *Client) FetchTool() (agent.Tool, bool) {
	ws := c.Workspace
	if c.Conn == nil || len(ws.Trees) == 0 {
		return agent.Tool{}, false
	}
	names := make([]string, 0, len(ws.Trees))
	for _, t := range ws.Trees {
		names = append(names, t.Repo.Name)
	}
	repoOf := func(in map[string]any) *workspace.Tree {
		name, _ := in["repo"].(string)
		if name == "" && len(ws.Trees) == 1 {
			return ws.Trees[0]
		}
		return ws.Tree(name)
	}
	var required []string
	if len(names) > 1 {
		required = []string{"repo"}
	}
	return agent.Tool{
		Name: FetchToolName,
		Description: "Bring this checkout's view of a repository's upstream up to date: the runner " +
			"fetches the remote and moves the `mirror/*` refs here (`mirror/main`, …) to where its " +
			"branches now are. Your own branch and working tree are not touched — rebase onto or " +
			"diff against `mirror/<branch>` yourself. Use it when the base may have moved under you, " +
			"before pushing to a branch that must fast-forward, or to read what another task " +
			"delivered. Repositories: " + strings.Join(names, ", ") + ".",
		Params: map[string]any{
			"repo": map[string]any{"type": "string", "enum": names},
		},
		Required: required,
		Derive: func(tu *task.ToolUse) {
			t := repoOf(tu.Input)
			if t == nil {
				return // no effect: an unknown repository is refused before any gate
			}
			tu.Input["repo"] = t.Repo.Name
			tu.Input["from"] = t.Repo.Repo
			tu.Effects = append(tu.Effects, task.Effect{Verb: task.Network, Resource: task.Host(remoteHost(t.Repo.Repo))})
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t := repoOf(tu.Input)
			if t == nil {
				name, _ := tu.Input["repo"].(string)
				return fmt.Sprintf("there is no repository %q in this workspace — it has: %s",
					name, strings.Join(names, ", ")), true, nil
			}
			rep, err := ask(c.Conn, Request{Op: "fetch", Repo: t.Repo.Name})
			switch {
			case err != nil:
				return "", false, fmt.Errorf("asking the runner to fetch %s: %w", t.Repo.Name, err)
			case rep.Error != "":
				// An answer rather than a failure: a remote that cannot be reached now is
				// something the agent can work around or try again, and `mirror/*` stand.
				return "not fetched — " + rep.Error + "; mirror/* are as they were", true, nil
			case len(rep.Updated) == 0:
				return "fetched " + t.Repo.Name + ": nothing new; mirror/* already match upstream", false, nil
			}
			return "fetched " + t.Repo.Name + ": " + strings.Join(rep.Updated, ", "), false, nil
		},
	}, true
}

// remoteHost is the host a repository URL reaches, for the effect a fetch of it derives; the
// whole string when it has no host to speak of (a path, in tests).
func remoteHost(remote string) string {
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		return u.Host
	}
	if _, rest, ok := strings.Cut(remote, "@"); ok { // git@host:path
		if host, _, ok := strings.Cut(rest, ":"); ok {
			return host
		}
	}
	return remote
}

// retire asks the runner to delete a branch the allowance permits deleting. There is nothing to
// commit and nothing to stage: a retirement removes something rather than sending something.
func retire(conn net.Conn, t *workspace.Tree, branch string) (string, bool, error) {
	return relay(ask(conn, Request{Op: "push", Repo: t.Repo.Name, Branch: branch, Delete: true}))
}

// push asks the runner to push this repository's committed work to a branch. The runner does the
// whole of it — checks the branch against the allowance, checks the commits carry the trailers,
// stages the work in its mirror, pushes from there — and answers with what happened. This side
// never holds a credential and never pushes, which is what keeps the secret out of the process
// the model's shell shares.
func push(conn net.Conn, t *workspace.Tree, branch string) (string, bool, error) {
	return relay(ask(conn, Request{Op: "push", Repo: t.Repo.Name, Branch: branch}))
}

// relay turns the runner's answer into the tool's: a refusal is an answer the agent re-plans on,
// a failure is an error, and anything else is what happened.
func relay(rep Reply, err error) (string, bool, error) {
	switch {
	case err != nil:
		return "", false, fmt.Errorf("asking the runner: %w", err)
	case rep.Refused != "":
		return "refused: " + rep.Refused, true, nil
	case rep.Error != "":
		return "", false, fmt.Errorf("%s", rep.Error)
	}
	return rep.Out, false, nil
}

func ask(conn net.Conn, req Request) (Reply, error) {
	body, err := codec.Marshal(req, codec.Tony)
	if err != nil {
		return Reply{}, err
	}
	if err := writeFrame(conn, body); err != nil {
		return Reply{}, err
	}
	raw, err := readFrame(bufio.NewReader(conn))
	if err != nil {
		return Reply{}, err
	}
	var rep Reply
	if err := codec.Unmarshal(raw, &rep, codec.Tony); err != nil {
		return Reply{}, err
	}
	return rep, nil
}
