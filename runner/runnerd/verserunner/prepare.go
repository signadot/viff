package verserunner

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// The Runner is a runnerd.Preparer: what it adds to a process per task is a workspace of the
// repositories the spawn names, the containment the environment declares around it, the egress
// out of it, and the channel a push comes back on. The process per task, the control socket it
// cannot reach, the lifeline, and the contract forwarded to it are runnerd's (SubprocessHandler),
// and the same for every runner built on it. The channel and what happens to the work when the
// process is gone are runner/runnerd/delivery's, the same for every runner that prepares a workspace.
//
// What stays here is exactly what must not cross: the git mirrors and the push credential.

// Admit is everything this runner can refuse without doing any work: the environment, the
// containment it declares, the repositories the spawn names, the driver, and the credentials a
// driver needs. Each refusal is an HTTP
// error rather than a task that fails at once, so a launch this runner will not host comes back
// as `spawn refused by <url>: <reason>` on the flow that asked for it.
func (r *Runner) Admit(sent *task.SpawnRequest) error {
	env, err := r.cfg.Environment(sent.Environment)
	if err != nil {
		return err
	}
	// The network this environment declares, checked BEFORE any work is set up. A sandbox that
	// cannot build it does not run the task with a weaker one: that difference is invisible from
	// the outside, which is the direction that costs.
	if err := r.h.Sandbox.Provides(networkFor(env)); err != nil {
		return fmt.Errorf("environment %q: %v", env.Name, err)
	}
	// The repositories, as the spawn wrote them: refused here for anything the spawn got wrong,
	// before any work is set up.
	repos, err := workspace.FromSpecs(sent.Repos)
	if err != nil {
		return fmt.Errorf("repos: %w", err)
	}
	if sent.Driver == "" {
		// Written back so the snapshot reports what actually ran. A launch that named no driver
		// would otherwise be recorded as having none, which is the one thing a reader cannot
		// recover.
		sent.Driver = orDefault(env.Driver, r.drivers.Default)
	}
	// A launch that cannot possibly finish is refused here rather than at the first model call,
	// where it arrives as somebody else's error message about an invalid key.
	if sent.Driver == driverLLM && r.secrets.PortkeyAPIKey == "" {
		return fmt.Errorf("$%s is not set in this runner's environment, so the `llm` driver has no "+
			"model credential — it is read here and sent to the task process, so it must be set for "+
			"the runner rather than for verse or for your shell", EnvPortkeyAPIKey)
	}
	if _, isAgent := r.cfg.Agents[sent.Driver]; !builtinDriver(sent.Driver) && !isAgent {
		return fmt.Errorf("this runner has no driver %q — it has %q, %q, and the agents it declares: %s",
			sent.Driver, driverLLM, task.DriverCalls, strings.Join(agentNames(r.cfg.Agents), ", "))
	}
	// The tools the spawn names are the request, and a tool this runner cannot offer for THIS
	// launch refuses it here — a push with no writable repository, a tool outside the floor, a
	// tool it has not got — rather than running the task without it. Unnamed is everything
	// the launch makes available; `[]` is no tools, and nothing to refuse.
	if sent.Tools != nil {
		offered := r.cfg.offeredFor(env, sent.Driver, repos)
		for _, name := range *sent.Tools {
			if !slices.Contains(offered, name) {
				return fmt.Errorf("tools: %q is not offered for this launch (it offers: %s)",
					name, strings.Join(offered, ", "))
			}
		}
	}
	return nil
}

// Prepare is the work a spawn is answered before: the workspace, the egress, the delivery
// channel, and the containment the process starts in. Under the runner's own context rather than
// the request's, which is gone by the time this matters; a cancel is what ends it early.
func (r *Runner) Prepare(ctx context.Context, sent task.SpawnRequest) (*runnerd.Prepared, error) {
	if r.prepareHook != nil {
		r.prepareHook(sent.ID)
	}
	env, err := r.cfg.Environment(sent.Environment)
	if err != nil {
		return nil, err
	}
	repos, err := workspace.FromSpecs(sent.Repos)
	if err != nil {
		return nil, fmt.Errorf("repos: %w", err)
	}
	// The launch this side keeps is what delivery writes into the commit trailers: the only
	// externally checkable link back into the verse from a push made under a shared credential.
	l := sent.Launch()
	l.ID, l.Driver, l.Environment = sent.ID, sent.Driver, env.Name

	ws, err := r.mirrors.Open(ctx, l.ID, repos, r.workspaces, workspace.AuthorFor(l.Provenance.Principal, l.Driver))
	// The mirrors this spawn just fetched are the mirrors anything undelivered is waiting in, so
	// this is the cheapest place to retry: the fetch has already taken the lock and paid the
	// round trip. In the background, because a backlog against a remote that is down must not
	// hold up a task that has nothing to do with it.
	go func() {
		for _, repo := range repos {
			r.reflect(context.Background(), repo.Repo)
		}
	}()
	if err != nil {
		return nil, fmt.Errorf("environment %q: %w", env.Name, err)
	}
	// A base older than upstream is the surprise worth naming: the task runs, and the log says
	// what it runs against.
	for _, w := range ws.Warnings {
		fmt.Fprintf(r.log, "prepare: %s: %s\n", l.ID, w)
	}
	if ctx.Err() != nil {
		_ = ws.Remove(context.Background())
		return nil, ctx.Err()
	}
	// The way a program in the workspace calls the task's tools, put there by this side: the
	// binary is beside the runner on an image, and the task process may not see where. Only a
	// program has a shell to call it from; the verse agent's tools are its own.
	if _, program := r.cfg.Agents[cmp.Or(sent.Driver, env.Driver, r.drivers.Default)]; program && r.callTool != "" {
		if err := installCallTool(r.callTool, ws.Root); err != nil {
			_ = ws.Remove(context.Background())
			return nil, fmt.Errorf("installing %s in the workspace: %w", CallToolBinary, err)
		}
	}
	c := &child{id: l.ID, ws: ws, env: env}
	prep, err := r.ready(c, l)
	if err != nil {
		_ = ws.Remove(context.Background())
		return nil, err
	}
	return prep, nil
}

// child is what this side keeps of one task while it runs: its workspace, the environment the
// workspace was opened under, the way out, and the channel its pushes come back on.
type child struct {
	id     string
	ws     *workspace.Workspace
	env    Environment
	egress *runnerd.EgressServer
	ch     *delivery.Channel
}

// ready is what the task's process gets, once the workspace is: its configuration on stdin, an
// environment built from the allowlist, the delivery channel as fd 5, the egress when the
// environment restricts the network, and the network the sandbox builds around all of it.
func (r *Runner) ready(c *child, l task.Launch) (*runnerd.Prepared, error) {
	env, ws, network := c.env, c.ws, networkFor(c.env)

	ch, err := delivery.Open(c.id, ws, r.mirrors, r.credentialFor, l, r.log)
	if err != nil {
		return nil, err
	}
	c.ch = ch
	// The way out, when the network is restricted. It is a socket on this side of the boundary,
	// bound into the sandbox and dialled by the task process — which is what makes an allowlist
	// a route rather than a setting.
	if network.Mode == runnerd.NetworkRestricted {
		eg, err := runnerd.ServeEgress(r.control, c.id, env.Egress, r.log)
		if err != nil {
			c.close()
			ch.Child().Close()
			return nil, err
		}
		c.egress, network.Egress = eg, eg.Sock()
	}

	// What the task's process is told: the runner's configuration without the catalogue of
	// environments, its secrets, and the workspace prepared for it — with the repositories as
	// the trees resolved them.
	bases := map[string]string{}
	repos := make([]workspace.Repo, 0, len(ws.Trees))
	for _, t := range ws.Trees {
		bases[t.Repo.Name] = t.Head
		repos = append(repos, t.Repo)
	}
	own := r.cfg
	own.Environments = nil
	blob, err := codec.Marshal(taskConfig{Config: own, Secrets: r.secrets, Task: &taskPrep{
		ID: c.id, Workspace: ws.Root, Branch: ws.Branch, Brief: ws.Brief(), Bases: bases,
		Repos: repos, Environment: env, Egress: egressAt(r.h.Sandbox, c.egress),
	}}, codec.Tony)
	if err != nil {
		c.close()
		ch.Child().Close()
		return nil, err
	}
	return &runnerd.Prepared{
		Dir:    ws.Root,
		Config: blob,
		// The environment the whole task tree gets, built from the allowlist rather than
		// inherited. This is where the credential split is enforced: the runner reads its git
		// token out of its own environment, and a process started with that environment would
		// hand it to the task's shell. An allowlist applied to the process that hosts everything
		// is a floor; applied to the agent binary alone it never was.
		Env:     buildEnv(env.Env),
		Extra:   []*os.File{ch.Child()}, // fd 5: the delivery channel (child.go)
		Network: network,
		Serve:   ch.Serve,
		Ended:   func(final task.Snapshot) { r.ended(c, l) },
	}, nil
}

// close shuts the task's way out and its channel. The way out closes with the task that had it:
// a proxy outliving its task is a route into this runner's network with nobody's name on it.
func (c *child) close() {
	if c.ch != nil {
		c.ch.Close()
	}
	c.egress.Stop()
}

// egressAt is where the task sees its egress socket, which only the sandbox knows; "" for a task
// with no restricted network, which is how it is told there is none.
func egressAt(sb runnerd.Sandbox, eg *runnerd.EgressServer) string {
	if eg == nil {
		return ""
	}
	return sb.EgressAt(eg.Sock())
}

// ended is what happens to a task's work once its process is gone: the channel settles it —
// salvaged if it did not leave, the workspace removed unless it is the only copy — and then the
// sweep of what was authorized to leave.
func (r *Runner) ended(c *child, l task.Launch) {
	c.egress.Stop()
	ctx := context.Background()
	c.ch.Settle(ctx, l, r.keep)
	// And sweep what was staged to leave. Whether a pending ref landed is the remote's to say, and
	// the sweep asks it: a ref that landed is already-up-to-date and goes, one that did not is retried, and one the
	// remote refuses moves to `held` so it is not retried forever. Here rather than only on the
	// next spawn, so `GET /runner` stops listing work the world already has within seconds of a
	// task ending rather than whenever somebody next uses this repository.
	for _, repo := range c.ws.Repos {
		r.reflect(ctx, repo.Repo)
	}
}

// credentialFor is the credential a push to a repository is made with: this runner's own forge
// token, which is what fetches its mirrors too. Nothing here reads a ref, and nothing here knows
// what the secret is for — which is what keeps this from being a git feature. Empty is right for
// a local path, and refused for a forge: an empty credential reaches the far side as somebody
// else's "authentication required".
func (r *Runner) credentialFor(repo workspace.Repo) (string, error) {
	if r.token == "" && strings.Contains(repo.Repo, "://") {
		return "", fmt.Errorf("this runner has no git token (-git-token-env), so it cannot push to %s", repo.Repo)
	}
	return r.token, nil
}
