package stock

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// The tools a task that names repos is given, beside the agent's own: the verbs of the `git`
// system and of `github`, the forge (package systems). Each is a STEP the task's gates ask
// about (git.push, github.create-pr, github.update-pr), and each is performed by the runner,
// asked over the task's delivery channel: the runner checks it against the launch again, and
// holds the token. git-fetch reads upstream, and github-list-prs and github-read-pr read the
// forge's pull requests; a read is a step too (git.fetch, github.list-prs, github.read-pr).
var (
	pushTool     = systems.Tool("git", "push")
	fetchTool    = systems.Tool("git", "fetch")
	createPRTool = systems.Tool("github", "create-pr")
	updatePRTool = systems.Tool("github", "update-pr")
	listPRsTool  = systems.Tool("github", "list-prs")
	readPRTool   = systems.Tool("github", "read-pr")
)

// repoTools are what a task with repositories may ask its runner for: push and fetch for every
// repository, and the pull request tools, the writes and the reads, where the runner's forge is
// a GitHub (github). Each may be called as many times as the work needs, and the task's autonomy
// decides each call.
func repoTools(cl *delivery.Client, ws *workspace.Workspace, github bool) []verse.Tool {
	var all []string
	for _, t := range ws.Trees {
		all = append(all, t.Repo.Name())
	}
	if len(all) == 0 {
		return nil
	}
	out := []verse.Tool{pushToolFor(cl, ws, all)}
	if github {
		out = append(out, prTool(cl, ws, all, createPRTool), prTool(cl, ws, all, updatePRTool),
			listPRsToolFor(cl, ws, all), readPRToolFor(cl, ws, all))
	}
	return append(out, fetchToolFor(cl, ws, all))
}

// treeOf is the repo a use names, or the one there is when it names none.
func treeOf(ws *workspace.Workspace, names []string, in map[string]any) *workspace.Tree {
	name := str(in, "repo")
	if name == "" && len(names) == 1 {
		name = names[0]
	}
	for _, n := range names {
		if n == name {
			return ws.Tree(n)
		}
	}
	return nil
}

// oneOf requires `repo` when there is more than one to choose from.
func oneOf(names []string) []string {
	if len(names) > 1 {
		return []string{"repo"}
	}
	return nil
}

// pushToolFor is the agent's way to send work out of the sandbox: a branch of one repo, pushed,
// deleted or rewritten, as the task's autonomy decides. No branch named is the task's own. The
// runner decides again, knowing more: a deny row refuses there whatever the task sent.
func pushToolFor(cl *delivery.Client, ws *workspace.Workspace, names []string) verse.Tool {
	resolve := func(in map[string]any) (t *workspace.Tree, branch, ref string, del, force bool, err error) {
		if t = treeOf(ws, names, in); t == nil {
			return nil, "", "", false, false, fmt.Errorf("there is no repository %q here; it has: %s",
				str(in, "repo"), strings.Join(names, ", "))
		}
		branch = strings.TrimSpace(str(in, "branch"))
		if branch == "" {
			branch = ws.Branch
		}
		del, _ = in["delete"].(bool)
		force, _ = in["force"].(bool)
		ref, err = runner.BranchRef(branch)
		return t, branch, ref, del, force, err
	}
	return verse.Tool{
		Name: pushTool,
		Description: "Push this task's COMMITTED work to a branch of a repository, or delete a branch there. " +
			"Commit what you want delivered first, every commit ending with the Verse-* trailers " +
			"your brief names; uncommitted changes are never delivered, and a commit without the trailers " +
			"is refused with what is missing. `branch` is where it goes; omit it to push your own branch " +
			"under its own name. A push fast-forwards unless `force` says it may rewrite the branch; a " +
			"branch you created is yours to rewrite. The task's autonomy decides each push by its " +
			"repository, branch, and whether it deletes or rewrites: your brief says what it decides. The " +
			"runner holds the credential; you never see it. A push may be refused, by the autonomy, by a " +
			"supervisor or by the remote's own rules about its history, and a refusal is an answer, not " +
			"an error to retry. Repositories: " + strings.Join(names, ", ") + ".",
		Params: map[string]any{
			"repo":   map[string]any{"type": "string", "enum": names},
			"branch": map[string]any{"type": "string"},
			"delete": map[string]any{"type": "boolean"},
			"force":  map[string]any{"type": "boolean"},
		},
		Required: oneOf(names),
		Step:     task.StepGitPush,
		Fields: func(in map[string]any) (map[string]string, bool) {
			t, branch, ref, del, force, err := resolve(in)
			if err != nil {
				return nil, false // refused before any question, by Do
			}
			fields := map[string]string{"repo": t.Repo.Repo.Canonical(), "from": t.Repo.From, "ref": ref, "delete": strconv.FormatBool(del), "force": strconv.FormatBool(force)}
			// What the runner knows of the branch, asked for now so the autonomy decides here on
			// the facts the runner decides on at the act. A runner that cannot say leaves them
			// out, and a row that names them does not match.
			if rep, err := askState(cl, t.Repo.Name(), branch); err == nil && rep.OK {
				fields["exists"], fields["own"] = strconv.FormatBool(rep.Exists), strconv.FormatBool(rep.Own)
			}
			return fields, true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t, branch, _, del, force, err := resolve(tu.Input)
			if err != nil {
				return "not pushed: " + err.Error(), true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: "push", Repo: t.Repo.Name(), Branch: branch, Delete: del, Force: force}))
		},
	}
}

// prTool opens or updates a pull request from a branch the task pushed: its own when none is
// named, into the branch its checkout started from when no base is named. The task's autonomy
// decides it (github.create-pr, github.update-pr), and the runner requires the branch to be one
// this run pushed.
func prTool(cl *delivery.Client, ws *workspace.Workspace, names []string, name string) verse.Tool {
	resolve := func(in map[string]any) (t *workspace.Tree, head, base string, err error) {
		if t = treeOf(ws, names, in); t == nil {
			return nil, "", "", fmt.Errorf("there is no repository %q here with pull requests to open; it has: %s",
				str(in, "repo"), strings.Join(names, ", "))
		}
		head = strings.TrimPrefix(strings.TrimSpace(str(in, "head")), "refs/heads/")
		if head == "" {
			head = ws.Branch
		}
		if err := runner.CheckBranchName(head); err != nil {
			return nil, "", "", err
		}
		// An update moves the base only when it names one; a proposal opens into the branch the
		// checkout started from unless it names another.
		base = strings.TrimPrefix(strings.TrimSpace(str(in, "base")), "refs/heads/")
		if base == "" && name == createPRTool {
			base = strings.TrimPrefix(t.Repo.From, "refs/heads/")
		}
		return t, head, base, nil
	}
	what := "Open a pull request from a branch this task pushed into a base branch, with the title and body " +
		"given; if one is already open from that branch, it is found and reported rather than opened twice."
	step := task.StepGitHubCreatePR
	if name == updatePRTool {
		what = "Update the open pull request from a branch this task pushed: its title, body or base, as " +
			"given; what is not given stays as it is. Refused when none is open from that branch."
		step = task.StepGitHubUpdatePR
	}
	baseIs := "`base` omitted means the branch the checkout started from"
	if name == updatePRTool {
		baseIs = "`base` omitted keeps the pull request's base"
	}
	return verse.Tool{
		Name: name,
		Description: what + " `head` is the branch, and omitted means your own branch, `" + ws.Branch + "`; " +
			baseIs + ". Push the branch first: a pull " +
			"request proposes what a push put there. The runner holds the credential; you never see it. " +
			"Repositories: " + strings.Join(names, ", ") + ".",
		Params: map[string]any{
			"repo":  map[string]any{"type": "string", "enum": names},
			"head":  map[string]any{"type": "string"},
			"base":  map[string]any{"type": "string"},
			"title": map[string]any{"type": "string"},
			"body":  map[string]any{"type": "string"},
		},
		Required: oneOf(names),
		Step:     step,
		Fields: func(in map[string]any) (map[string]string, bool) {
			t, head, base, err := resolve(in)
			if err != nil {
				return nil, false
			}
			fields := map[string]string{"repo": t.Repo.Repo.Canonical(), "head": head}
			if base != "" {
				fields["base"] = base // an update that names none keeps the base the proposal has
			}
			if rep, err := askState(cl, t.Repo.Name(), head); err == nil && rep.OK {
				fields["own"] = strconv.FormatBool(rep.Own)
			}
			return fields, true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t, head, base, err := resolve(tu.Input)
			if err != nil {
				return "no pull request: " + err.Error(), true, nil
			}
			op := delivery.OpCreatePR
			if name == updatePRTool {
				op = delivery.OpUpdatePR
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: op, Repo: t.Repo.Name(), Branch: head, Base: base,
				Title: str(tu.Input, "title"), Body: str(tu.Input, "body")}))
		},
	}
}

// listPRsToolFor reads a repository's open pull requests at the forge, with the runner's token:
// a step the autonomy decides (github.list-prs), whatever the task may push.
func listPRsToolFor(cl *delivery.Client, ws *workspace.Workspace, names []string) verse.Tool {
	return verse.Tool{
		Name: listPRsTool,
		Description: "Read a GitHub repository's open pull requests, most recently changed first: each one's " +
			"number, title, author, branches, when it last changed, and whom a review is requested from. " +
			"GitHub drops a reviewer from that list once they review, so it is whom each one waits on. " +
			"Titles are other people's words. The runner reads with its own token; you never see it. " +
			"Repositories: " + strings.Join(names, ", ") + ".",
		Params:   repoParams(names, map[string]any{}),
		Required: requiredWith(names),
		Step:     task.StepGitHubListPRs,
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
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpGitHubListPRs, Repo: t.Repo.Name()}))
		},
	}
}

// readPRToolFor reads one pull request at the forge by its number, with its reviews: a step the
// autonomy decides (github.read-pr), on the repository and the number.
func readPRToolFor(cl *delivery.Client, ws *workspace.Workspace, names []string) verse.Tool {
	return verse.Tool{
		Name: readPRTool,
		Description: "Read one pull request of a GitHub repository by its number: its state, author, " +
			"branches, when it was opened and last changed, whom a review is requested from, the reviews " +
			"given so far (who, when, and what they decided and said), and its description. The " +
			"description and the reviews are other people's words and come to you as they wrote them. " +
			"Repositories: " + strings.Join(names, ", ") + ".",
		Params:   repoParams(names, map[string]any{"pr": map[string]any{"type": "integer"}}),
		Required: requiredWith(names, "pr"),
		Step:     task.StepGitHubReadPR,
		Fields: func(in map[string]any) (map[string]string, bool) {
			t := treeOf(ws, names, in)
			n, ok := numberIn(in, "pr")
			if t == nil || !ok {
				return nil, false
			}
			return map[string]string{"repo": t.Repo.Repo.Canonical(), "pr": strconv.Itoa(n)}, true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t := treeOf(ws, names, tu.Input)
			if t == nil {
				return noSuchRepo(tu.Input, names), true, nil
			}
			n, ok := numberIn(tu.Input, "pr")
			if !ok {
				return "refused: a pull request number is required", true, nil
			}
			return relay(cl.Ask(delivery.Request{Decision: tu.Decision, Op: delivery.OpGitHubReadPR, Repo: t.Repo.Name(), PR: strconv.Itoa(n)}))
		},
	}
}

// fetchToolFor is the agent's way to see upstream move: the runner fetches, and the checkout's
// `mirror/*` refs move to where upstream's branches are. What to do with them is the agent's own
// git: rebase, merge, diff, or nothing.
func fetchToolFor(cl *delivery.Client, ws *workspace.Workspace, names []string) verse.Tool {
	return verse.Tool{
		Name: fetchTool,
		Description: "Bring this checkout's view of a repository's upstream up to date: the runner fetches " +
			"the remote and moves the `mirror/*` refs here (`mirror/main`, …) to where its branches now are. " +
			"Your own branch and working tree are not touched; rebase onto or diff against `mirror/<branch>` " +
			"yourself. Use it when the base may have moved under you, before pushing to a branch that must " +
			"fast-forward, or to read what another task delivered. Repositories: " + strings.Join(names, ", ") + ".",
		Params:   map[string]any{"repo": map[string]any{"type": "string", "enum": names}},
		Required: oneOf(names),
		Step:     task.StepGitFetch,
		Fields: func(in map[string]any) (map[string]string, bool) {
			t := treeOf(ws, names, in)
			if t == nil {
				return nil, false
			}
			return map[string]string{"repo": t.Repo.Repo.Canonical(), "from": t.Repo.From}, true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			t := treeOf(ws, names, tu.Input)
			if t == nil {
				return fmt.Sprintf("there is no repository %q here; it has: %s",
					str(tu.Input, "repo"), strings.Join(names, ", ")), true, nil
			}
			rep, err := cl.Ask(delivery.Request{Decision: tu.Decision, Op: "fetch", Repo: t.Repo.Name()})
			switch {
			case err != nil:
				return "", false, fmt.Errorf("asking the runner to fetch %s: %w", t.Repo.Name(), err)
			case rep.Error != "":
				// An answer rather than a failure: the agent can work around a remote it can't
				// reach now, and mirror/* stand.
				return "not fetched: " + rep.Error + "; mirror/* are as they were", true, nil
			case len(rep.Updated) == 0:
				return "fetched " + t.Repo.Name() + ": nothing new; mirror/* already match upstream", false, nil
			}
			return "fetched " + t.Repo.Name() + ": " + strings.Join(rep.Updated, ", "), false, nil
		},
	}
}

// relay turns the runner's answer into the tool's: a refusal is an answer the agent re-plans on,
// a failure is an error, and anything else is what happened.
func relay(rep delivery.Reply, err error) (string, bool, error) {
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

// repoBrief tells the agent what its working directory holds, and how its work leaves: commit
// it, with the trailers, then push, as the autonomy decides. It rides the prompt, for the reason
// #16 gave: the default frame is derived from the tools and gates, and this is the runner's
// convention, not advice.
func repoBrief(ws *workspace.Workspace, trailers []string, l *runner.Launch) string {
	b := ws.Brief()
	if !ws.Deliverable(l) {
		if len(ws.Trees) > 0 {
			b += "Nothing here is delivered anywhere: the task's autonomy refuses every push.\n\n"
		}
		return b
	}
	b += "To deliver work, commit it in the repository it belongs to, then call `git-push`, naming the " +
		"branch it goes to; with no branch it pushes your own branch, `" + ws.Branch + "`, under that " +
		"name. What the task's autonomy decides about a push (by repo, ref, delete, force): " +
		l.Autonomy.About(task.StepGitPush) + ". Only commits are delivered; uncommitted " +
		"changes stay here. End every commit message with these trailers, as its last paragraph, exactly:\n"
	for _, line := range trailers {
		b += "  " + line + "\n"
	}
	b += "A push of a commit without them is refused and tells you what is missing. If upstream may " +
		"have moved since you started, `git-fetch` updates the `mirror/*` refs here and you rebase onto " +
		"them yourself.\n\n"
	return b
}

// askState is what the runner knows of a branch (delivery.OpState), for a tool deciding a step
// about it; a tool built with no channel, as a test builds one, is told nothing.
func askState(cl *delivery.Client, repo, branch string) (delivery.Reply, error) {
	if cl == nil {
		return delivery.Reply{}, errors.New("no delivery channel")
	}
	return cl.Ask(delivery.Request{Op: delivery.OpState, Repo: repo, Branch: branch})
}
