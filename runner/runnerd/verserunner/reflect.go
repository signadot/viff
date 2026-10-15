package verserunner

import (
	"context"
	"fmt"

	"github.com/signadot/verse/runner/runnerd/workspace"
)

// reflectAll sweeps every remote this runner has a mirror of. Startup, because a pod that died
// holding undelivered work should get it out without waiting for the next task on that
// repository — and at startup no spawn is here to say which repositories there are, so the
// mirrors say.
func (r *Runner) reflectAll(ctx context.Context) {
	remotes, err := r.mirrors.Remotes()
	if err != nil {
		fmt.Fprintf(r.log, "verse runner: listing mirrors: %v\n", err)
		return
	}
	for _, remote := range remotes {
		r.reflect(ctx, remote)
	}
}

// reflect retries one remote's approved-but-undelivered work and says so when it moves any,
// under the runner's own token: the one every push is made with.
func (r *Runner) reflect(ctx context.Context, remote string) {
	n, err := r.mirrors.Reflect(ctx, remote, workspace.TokenAuth(r.token)(remote))
	if n > 0 {
		fmt.Fprintf(r.log, "verse runner: reflected %d undelivered branch(es) to %s\n", n, remote)
	}
	if err != nil {
		fmt.Fprintf(r.log, "verse runner: reflecting %s: %v\n", remote, err)
	}
}

// staged is everything this runner holds that did not leave, over every mirror, for the
// descriptor.
func (r *Runner) staged() []workspace.StagedWork {
	remotes, err := r.mirrors.Remotes()
	if err != nil {
		fmt.Fprintf(r.log, "verse runner: listing mirrors: %v\n", err)
		return nil
	}
	var out []workspace.StagedWork
	for _, remote := range remotes {
		work, err := r.mirrors.Staged(remote)
		if err != nil {
			fmt.Fprintf(r.log, "verse runner: listing staged work for %s: %v\n", remote, err)
			continue
		}
		out = append(out, work...)
	}
	return out
}
