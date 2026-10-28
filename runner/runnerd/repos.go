package runnerd

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/systems"
)

// Repos is how a SubprocessHandler checks out a launch's repos and pushes from them: the git
// mirrors it keeps, the forge it fetches from, its token for that forge, and the ceilings the
// operator put over what any task may do there. A handler without it refuses a launch that
// names repos.
//
// The token never reaches a task: the task's process asks for a push over its channel (fd 5),
// and the runner pushes from its mirror (package delivery).
type Repos struct {
	Mirrors *workspace.Mirrors
	// Forge is where `owner/name` is fetched from: https://github.com, or a directory holding
	// `owner/name` repositories.
	Forge string
	// Token is the runner's token for the forge. Empty is right for a forge on this filesystem,
	// and refused for one over the network.
	Token string
	// Services is what the runner holds for the systems beyond its git token: a Linear
	// credential and a Slack bot token, which stay here as the token does (package delivery).
	Services delivery.Services
	// Ceilings is the operator's bounds on what a launch's task may do with each system the
	// runner performs, by system: its doer's offer's (runner.Offer.Systems), read again at each
	// act, whatever the task's autonomy said. Nil bounds nothing beyond the task's autonomy.
	Ceilings func(*runner.Launch) map[string]systems.Ceiling
}

// DefaultForge is where a runner fetches `owner/name` when its configuration names nowhere else.
const DefaultForge = "https://github.com"

// NewRepos is the repos a runner keeps under mirrors, fetched from forge (DefaultForge when
// empty) with token.
func NewRepos(mirrors, forge, token string) *Repos {
	if forge == "" {
		forge = DefaultForge
	}
	return &Repos{
		Mirrors: workspace.NewMirrors(mirrors, workspace.TokenAuth(token)),
		Forge:   forge,
		Token:   token,
	}
}

// checkout is one task's repos, from the runner's side: its workspace, and the channel its
// pushes come back on.
type checkout struct {
	l  *runner.Launch
	ws *workspace.Workspace
	ch *delivery.Channel
}

// prepare checks a launch's repos out under dir, opens the task's channel, and starts retrying
// whatever earlier tasks were approved to push to them and did not land. The workspace's root
// is the task's working directory.
func (rs *Repos) prepare(ctx context.Context, l *runner.Launch, dir string, log io.Writer) (*checkout, error) {
	repos, err := workspace.FromLaunch(l.Repos(), rs.Forge)
	if err != nil {
		return nil, err
	}
	ws, err := rs.Mirrors.Open(ctx, l.ID, repos, dir,
		workspace.AuthorFor(l.Provenance.Principal, cmp.Or(l.Who(), "agent")))
	// The mirrors were just fetched, so this is the cheapest place to retry: in the background,
	// because a backlog against a remote that is down must not hold up this task.
	go func() {
		for _, r := range repos {
			rs.reflect(context.Background(), r.Remote, log)
		}
	}()
	if err != nil {
		return nil, err
	}
	// A base older than upstream is the surprise worth naming: the task runs, and the log says
	// what it runs against.
	for _, w := range ws.Warnings {
		fmt.Fprintf(log, "runnerd: %s: %s\n", l.ID, w)
	}
	var ceilings map[string]systems.Ceiling
	if rs.Ceilings != nil {
		ceilings = rs.Ceilings(l)
	}
	ch, err := delivery.Open(l.ID, ws, rs.Mirrors, rs.credential, rs.Services, ceilings, l, log)
	if err != nil {
		_ = ws.Remove(context.Background())
		return nil, err
	}
	return &checkout{l: l, ws: ws, ch: ch}, nil
}

// settle is what happens to a task's work once its process is gone: the channel salvages what
// did not leave, and the sweep retries what was approved to. It reports whether the workspace
// is still there, the only copy of something.
func (rs *Repos) settle(c *checkout, log io.Writer) bool {
	kept := c.ch.Settle(context.Background(), c.l, false)
	for _, r := range c.ws.Repos {
		rs.reflect(context.Background(), r.Remote, log)
	}
	return kept
}

// abandon drops a checkout whose task never started.
func (c *checkout) abandon() {
	c.ch.Close()
	_ = c.ch.Child().Close()
	_ = c.ws.Remove(context.Background())
}

// ReflectAll retries, once, every push a gate approved that did not land: for a runner that
// starts with work its last run left.
func (rs *Repos) ReflectAll(ctx context.Context, log io.Writer) {
	remotes, err := rs.Mirrors.Remotes()
	if err != nil {
		fmt.Fprintf(log, "runnerd: listing mirrors: %v\n", err)
		return
	}
	for _, remote := range remotes {
		rs.reflect(ctx, remote, log)
	}
}

// reflect retries one remote's approved-but-undelivered work, and says so when it moves any.
func (rs *Repos) reflect(ctx context.Context, remote string, log io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, workspace.ForgeTimeout)
	defer cancel()
	n, err := rs.Mirrors.Reflect(ctx, remote, workspace.TokenAuth(rs.Token)(remote))
	if n > 0 {
		fmt.Fprintf(log, "runnerd: reflected %d undelivered branch(es) to %s\n", n, remote)
	}
	if err != nil {
		fmt.Fprintf(log, "runnerd: reflecting %s: %v\n", remote, err)
	}
}

// credential is what the runner's git work for a repo is done with: its token. Empty is refused
// for a forge over the network, where it would reach the far side as somebody else's
// "authentication required".
func (rs *Repos) credential(repo workspace.Repo) (string, error) {
	if rs.Token == "" && strings.Contains(repo.Remote, "://") {
		return "", fmt.Errorf("this runner has no token for %s, so it cannot reach it", repo.Remote)
	}
	return rs.Token, nil
}
