package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/signadot/verse/task"
)

// Work waits in the MIRROR, and the mirror is where it waits whether or not it ever leaves.
//
// The old arrangement had one copy of a task's work — the commit in that task's own clone — so a
// push that failed left the workspace as the only thing holding it, and `os.RemoveAll` on a
// directory was the difference between a result and nothing. That is why `finish` grew a rule
// about KEEPING workspaces, and the rule was ugly because it was standing in for durability.
//
// So delivery is two hops now: commit, push into this runner's mirror, then push the mirror to
// origin. The first hop is local, costs a thin pack against a repository that already has every
// base object, and cannot fail for any reason the network owns.
//
// Staged ALWAYS rather than only when something goes wrong. Salvage-only code is code that runs
// exclusively when things are already broken, which is the worst moment for it to be the path
// nothing exercises — this example has been bitten twice by exactly that shape (a workspace
// removed unconditionally, a delivery that tested for dirt instead of work). Here the recovery
// path is the ordinary path minus its last step.
//
// # Two namespaces, and the difference is authorization
//
//   - refs/pending/<task>/<repo> — work whose delivery was APPROVED and did not land. Safe to
//     retry, because somebody already said yes.
//   - refs/held/<task>/<repo> — work that was refused at the gate, or whose task never reached
//     its delivery step at all. NEVER pushed automatically.
//
// One namespace would have been simpler and wrong. The whole point of making the push a gated
// action is that an operator can decline it; a retry loop that swept a single namespace would
// push work somebody declined, silently, later. The split makes the retry safe by construction
// rather than by remembering.
//
// # Why the mirror and not somewhere else
//
// It is already per repository, already has a lock a receive-pack needs, and already holds the
// objects — so the push is thin. A separate outbox would need alternates to be, and would be a
// second store to reason about.
//
// Prune cannot reach these. `ensure` fetches `+refs/heads/*` and `+refs/tags/*` with Prune set,
// and go-git's pruneRemotes only removes refs matching the REVERSE of a configured refspec, so a
// namespace outside those two is untouched. That is the whole reason the layout is what it is,
// and it is pinned by a test.
const (
	stagePending = "pending" // approved, not landed: retryable
	stageHeld    = "held"    // not authorized to leave, or never got that far
)

// stagedRef is where one repository's work waits in the mirror.
func stagedRef(kind, taskID, repo string) string {
	return fmt.Sprintf("refs/%s/%s/%s", kind, safeSeg(taskID), safeSeg(repo))
}

// Delivery is what became of one workspace's work. The three lists are exhaustive over the
// repositories that had any, which is what makes the workspace decision a lookup rather than a
// guess: Loose is non-empty exactly when the checkout is still the only copy of something.
type Delivery struct {
	Pushed []string // "<repo> -> <target>": it reached the far side
	Staged []string // mirror refs still holding work
	Loose  []string // repos whose work is in NEITHER — do not remove this workspace
	Err    error
}

// Deliver commits whatever the task produced, stages it in the mirror, pushes it to the ref the
// environment named, and drops the staged copy once the far side has it.
//
// Dropping it on success is the retention rule, and it needs no TTL: what is left under
// refs/pending is exactly the work this runner holds that the world does not.
func (w *Workspace) Deliver(ctx context.Context, l task.Launch, result string) Delivery {
	return w.settle(ctx, l, result, stagePending, true)
}

// Salvage is the same two hops minus the last one: commit what is there and put it in the mirror
// under `held`, for work that was refused at the gate or whose task never got that far.
//
// It exists so that "the task did not deliver" stops meaning "the work is in a directory somebody
// has to know to look in". A refused push is a decision about where work goes, not a decision to
// destroy it.
func (w *Workspace) Salvage(ctx context.Context, l task.Launch) Delivery {
	return w.settle(ctx, l, "", stageHeld, false)
}

func (w *Workspace) settle(ctx context.Context, l task.Launch, result, kind string, send bool) Delivery {
	var d Delivery
	var errs []error
	for _, t := range w.Trees {
		// A repository with nowhere to push is a reference tree: nothing leaves and nothing is
		// staged, because there is no target to hold work FOR.
		if t.PushTarget(w.Branch) == "" {
			continue
		}
		work, err := t.commitWork(l, result)
		if err != nil {
			errs = append(errs, err)
			d.Loose = append(d.Loose, t.Repo.Name)
			continue
		}
		if !work {
			continue // genuinely untouched
		}
		ref := stagedRef(kind, l.ID, t.Repo.Name)
		if err := w.mirrors.stage(ctx, t, w.Branch, ref); err != nil {
			// Staging is the local hop, so this failing means the disk or the git binary, not
			// the network. The workspace is the only copy and must be kept.
			errs = append(errs, err)
			d.Loose = append(d.Loose, t.Repo.Name)
			continue
		}
		if !send {
			d.Staged = append(d.Staged, ref)
			continue
		}
		target := t.PushTarget(w.Branch)
		if err := t.push(ctx, w.Branch, target); err != nil {
			// The work is safe: it went into the mirror a moment ago. Say where, because the
			// operator's next question is exactly that.
			errs = append(errs, fmt.Errorf("%w (the work is in this runner's mirror at %s)", err, ref))
			d.Staged = append(d.Staged, ref)
			continue
		}
		if err := w.mirrors.unstage(ctx, t.Repo.Repo, ref); err != nil {
			// Not a delivery failure — the work is at the far side. A stale pending ref will be
			// retried later and push nothing, which is harmless and noisy rather than wrong.
			errs = append(errs, fmt.Errorf("dropping %s after delivery: %w", ref, err))
		}
		d.Pushed = append(d.Pushed, t.Repo.Name+" -> "+target)
	}
	d.Err = errors.Join(errs...)
	return d
}

// stage pushes a task's branch into the mirror under ref.
func (m *Mirrors) stage(ctx context.Context, t *Tree, branch, ref string) error {
	dir := m.dirFor(t.Repo.Repo)
	lock := m.lockFor(dir)
	lock.Lock()
	defer lock.Unlock()

	// Pinned to the mirror path in memory, for the reason the delivery remote is pinned to the
	// environment's url: the task can write `.git/config` and go-git re-reads it at push time.
	remote := git.NewRemote(t.repo.Storer, &config.RemoteConfig{
		Name: stageRemote, URLs: []string{dir},
	})
	spec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:%s", branch, ref))
	err := remote.PushContext(ctx, &git.PushOptions{
		RemoteName: stageRemote, RefSpecs: []config.RefSpec{spec}, Force: true,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("repo %s: stage %s: %w", t.Repo.Name, ref, err)
	}
	return nil
}

// unstage drops a staged ref once its work is at the far side.
func (m *Mirrors) unstage(_ context.Context, repo, ref string) error {
	dir := m.dirFor(repo)
	lock := m.lockFor(dir)
	lock.Lock()
	defer lock.Unlock()

	r, err := git.PlainOpen(dir)
	if err != nil {
		return err
	}
	// A local ref removal rather than a delete-push: the mirror is a repository this process
	// owns, and there is no transport worth involving.
	if err := r.Storer.RemoveReference(plumbing.ReferenceName(ref)); err != nil &&
		!errors.Is(err, plumbing.ErrReferenceNotFound) {
		return err
	}
	return nil
}

// Reflect pushes every PENDING ref for one repository to the target its task named, and drops the
// ones that land. This is the retry, and it is the reason `pending` and `held` are different
// namespaces: everything it touches has already been through a gate that said yes.
//
// Called after a mirror is fetched (so a spawn against a repository sweeps that repository's
// backlog) and once at startup. No timer and no loop of its own: the work is bounded by what is
// there, and a runner nobody is using has nothing to retry for.
func (m *Mirrors) Reflect(ctx context.Context, r Repo) (int, error) {
	if r.Push == "" {
		return 0, nil
	}
	dir := m.dirFor(r.Repo)
	lock := m.lockFor(dir)
	lock.Lock()
	defer lock.Unlock()

	mirror, err := git.PlainOpen(dir)
	if err != nil {
		return 0, nil // no mirror yet: nothing has ever been staged here
	}
	refs, err := mirror.References()
	if err != nil {
		return 0, err
	}
	type pending struct{ ref, target string }
	var todo []pending
	prefix := "refs/" + stagePending + "/"
	if err := refs.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().String()
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok {
			return nil
		}
		taskID, repo, ok := strings.Cut(rest, "/")
		if !ok || repo != safeSeg(r.Name) {
			return nil // another repository's staged work, or a shape nothing here wrote
		}
		todo = append(todo, pending{ref: name, target: strings.ReplaceAll(r.Push, "$task", taskID)})
		return nil
	}); err != nil {
		return 0, err
	}

	var done int
	var errs []error
	remote := git.NewRemote(mirror.Storer, &config.RemoteConfig{
		Name: deliveryRemote, URLs: []string{r.Repo},
	})
	for _, p := range todo {
		spec := config.RefSpec(fmt.Sprintf("+%s:%s", p.ref, p.target))
		err := remote.PushContext(ctx, &git.PushOptions{
			RemoteName: deliveryRemote, RefSpecs: []config.RefSpec{spec},
			Force: true, Auth: m.auth(r.Repo),
		})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			errs = append(errs, fmt.Errorf("reflect %s -> %s: %w", p.ref, p.target, err))
			continue
		}
		if err := mirror.Storer.RemoveReference(plumbing.ReferenceName(p.ref)); err != nil {
			errs = append(errs, err)
			continue
		}
		done++
	}
	return done, errors.Join(errs...)
}

// stageRemote is the in-memory remote a stage push goes through, and like deliveryRemote it must
// never become a remote in anybody's `.git/config`.
const stageRemote = "verse-stage"

// reflectAll sweeps every repository this runner serves. Startup, because a pod that died holding
// undelivered work should get it out without waiting for the next task on that repository.
func (r *Runner) reflectAll(ctx context.Context) {
	seen := map[string]bool{}
	for _, env := range r.cfg.Environments {
		for _, repo := range env.Repos {
			key := repo.Repo + "\x00" + repo.Name + "\x00" + repo.Push
			if repo.Push == "" || seen[key] {
				continue
			}
			seen[key] = true
			r.reflect(ctx, repo)
		}
	}
}

// reflect retries one repository's approved-but-undelivered work and says so when it moves any.
func (r *Runner) reflect(ctx context.Context, repo Repo) {
	n, err := r.mirrors.Reflect(ctx, repo)
	if n > 0 {
		fmt.Fprintf(r.log, "coderunner: reflected %d undelivered branch(es) of %s to %s\n",
			n, repo.Name, repo.Repo)
	}
	if err != nil {
		fmt.Fprintf(r.log, "coderunner: reflecting %s: %v\n", repo.Name, err)
	}
}

// StagedWork is one commit this runner is holding: what it was for, why it is still here, and the
// exact command that gets it out.
//
// The recovery instruction is part of the record rather than something to look up, because the
// mirror is a directory named by a HASH of a remote url. Told only "the work is in the mirror", the
// next question is always "which one, and how do I read it", and the answer involves knowing that
// this runner hashes remotes at all.
type StagedWork struct {
	Task  string `tony:"field=task"`
	Repo  string `tony:"field=repo"`
	Why   string `tony:"field=why"` // pending: approved, not landed. held: nobody authorized it to leave.
	Ref   string `tony:"field=ref"`
	Fetch string `tony:"field=fetch"` // paste this to get the commit into a checkout
}

// Staged is everything waiting in one repository's mirror, both namespaces.
//
// It exists because staging made workspaces disposable, and a workspace was the thing an operator
// could `cd` into. Removing that without replacing it would have traded a lost result for an
// unfindable one, which is the same failure wearing a better hat.
func (m *Mirrors) Staged(r Repo) ([]StagedWork, error) {
	dir := m.dirFor(r.Repo)
	lock := m.lockFor(dir)
	lock.Lock()
	defer lock.Unlock()

	mirror, err := git.PlainOpen(dir)
	if err != nil {
		return nil, nil // no mirror: nothing has ever been staged for this repository
	}
	refs, err := mirror.References()
	if err != nil {
		return nil, err
	}
	var out []StagedWork
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().String()
		for _, kind := range []string{stagePending, stageHeld} {
			rest, ok := strings.CutPrefix(name, "refs/"+kind+"/")
			if !ok {
				continue
			}
			taskID, repo, ok := strings.Cut(rest, "/")
			if !ok || repo != safeSeg(r.Name) {
				continue
			}
			out = append(out, StagedWork{
				Task: taskID, Repo: r.Name, Why: whyStaged(kind), Ref: name,
				Fetch: fmt.Sprintf("git fetch %s %s:refs/heads/recovered/%s-%s", dir, name, taskID, repo),
			})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, err
}

func whyStaged(kind string) string {
	if kind == stagePending {
		return "delivery was approved and has not landed — this runner retries it"
	}
	return "nobody authorized this to leave: refused at the gate, or the task never got that far"
}

// staged is every repository this runner serves, for the descriptor.
func (r *Runner) staged() []StagedWork {
	var out []StagedWork
	seen := map[string]bool{}
	for _, env := range r.cfg.Environments {
		for _, repo := range env.Repos {
			key := repo.Repo + "\x00" + repo.Name
			if repo.Push == "" || seen[key] {
				continue
			}
			seen[key] = true
			work, err := r.mirrors.Staged(repo)
			if err != nil {
				fmt.Fprintf(r.log, "coderunner: listing staged work for %s: %v\n", repo.Name, err)
				continue
			}
			out = append(out, work...)
		}
	}
	return out
}
