package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"

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
//
// A PENDING ref carries the target it was approved for as the tail of its own name, and a HELD
// one does not — which is the same distinction the two namespaces already make, written into
// the shape: pending work is going somewhere specific and held work was never told where.
//
// Carrying it beats re-deriving it, and the re-derivation was not merely fragile, it was already
// wrong. Reflect rebuilt the target from the environment's push target by expanding `$task` into
// it, and every real environment writes `refs/heads/verse/auto/$sha/$task` — where `$sha` is the
// commit the work started from, which the mirror does not know and cannot recover. A retry would
// have pushed to a ref with a dollar sign in its name. It also cannot say WHICH destination the
// work was approved for now that a repository may declare several.
func stagedRef(kind, taskID, repo, target string) string {
	ref := fmt.Sprintf("refs/%s/%s/%s", kind, safeSeg(taskID), safeSeg(repo))
	if target == "" {
		return ref
	}
	return ref + "/" + target
}

// splitStaged reads a staged ref back: the task it belongs to, the repository directory it came
// from, and the target it was approved for (empty for held work).
func splitStaged(kind, ref string) (taskID, repo, target string, ok bool) {
	rest, ok := strings.CutPrefix(ref, "refs/"+kind+"/")
	if !ok {
		return "", "", "", false
	}
	taskID, rest, ok = strings.Cut(rest, "/")
	if !ok {
		return "", "", "", false
	}
	repo, target, _ = strings.Cut(rest, "/")
	return taskID, repo, target, repo != ""
}

// Delivery is what became of one workspace's work. The three lists are exhaustive over the
// repositories that had any, which is what makes the workspace decision a lookup rather than a
// guess: Loose is non-empty exactly when the checkout is still the only copy of something.
type Delivery struct {
	Pushed []string // "<repo> -> <target>": it reached the far side
	Staged []string // mirror refs still holding work
	Loose  []string // repos whose work is in NEITHER — do not remove this workspace
	// Refused is what the FAR SIDE said no to, and it is separate from Err because it is not a
	// fault: a remote enforcing its own rules about its own history is an authority verse does
	// not hold, exercising it. See refusal.
	Refused []string
	Err     error
}

// Deliver commits whatever the task produced, stages it in the mirror, pushes it to the ref the
// environment named, and drops the staged copy once the far side has it.
//
// Dropping it on success is the retention rule, and it needs no TTL: what is left under
// refs/pending is exactly the work this runner holds that the world does not.
//
// The destination is asked for BY NAME — `auto` is what the finisher asks for and what `push:`
// has always spelled — so a repository that does not declare it sends nothing, exactly as a
// repository with no push target sends nothing. An empty repo means every tree, which is what
// the finisher's one delivery has always meant.
func (w *Workspace) Deliver(ctx context.Context, l task.Launch, result, repo, dest string) Delivery {
	return w.settle(ctx, l, result, repo, dest, stagePending, true)
}

// Salvage is the same two hops minus the last one: commit what is there and put it in the mirror
// under `held`, for work that was refused at the gate or whose task never got that far.
//
// It exists so that "the task did not deliver" stops meaning "the work is in a directory somebody
// has to know to look in". A refused push is a decision about where work goes, not a decision to
// destroy it.
// It names no destination, and it must not: work that nobody authorized to leave is held for
// every repository that has anywhere to send it, not for one place somebody might have chosen.
func (w *Workspace) Salvage(ctx context.Context, l task.Launch) Delivery {
	return w.settle(ctx, l, "", "", "", stageHeld, false)
}

// Stage is the first hop on its own: put one repository's committed work in the mirror, under
// the target it has just been approved for.
//
// It is the half of Deliver that survives the credential moving into the task process. The task
// commits and the task pushes; what stays here is durability, because the mirror is this side's
// and a checkout is a directory somebody deletes. There is no commit in it — the work is already
// a commit by the time a credential is asked for, which is what makes this a thin pack rather
// than a second opinion about what the task did.
func (w *Workspace) Stage(ctx context.Context, taskID string, t *Tree, target string) Delivery {
	var d Delivery
	ref := stagedRef(stagePending, taskID, t.Repo.Name, target)
	if err := w.mirrors.stage(ctx, t, w.Branch, ref); err != nil {
		d.Err = err
		d.Loose = append(d.Loose, t.Repo.Name)
		return d
	}
	d.Staged = append(d.Staged, ref)
	return d
}

func (w *Workspace) settle(ctx context.Context, l task.Launch, result, repo, dest, kind string, send bool) Delivery {
	var d Delivery
	var errs []error
	for _, t := range w.Trees {
		if repo != "" && t.Repo.Name != repo {
			continue
		}
		// A repository with nowhere to push is a reference tree: nothing leaves and nothing is
		// staged, because there is no target to hold work FOR. When a destination is named, a
		// repository that does not declare THAT one is in the same position.
		var target string
		if send {
			if target = t.DestRef(dest, w.Branch); target == "" {
				continue
			}
		} else if len(t.Repo.Dests()) == 0 {
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
		ref := stagedRef(kind, l.ID, t.Repo.Name, target)
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
		if err := t.push(ctx, w.Branch, target); err != nil {
			if why := refusal(err); why != "" {
				// The far side said no, which is a DECISION and not a fault. It moves to
				// `held`, because `pending` means "a retry may complete this" and a retry
				// cannot: whatever rule refused it will refuse it again on every spawn against
				// this repository, forever, in the log. Held work is exactly work nobody with
				// authority over where it goes has agreed to take.
				held := stagedRef(stageHeld, l.ID, t.Repo.Name, "")
				if err := w.mirrors.stage(ctx, t, w.Branch, held); err != nil {
					errs = append(errs, err)
					d.Loose = append(d.Loose, t.Repo.Name)
					continue
				}
				if err := w.mirrors.unstage(ctx, t.Repo.Repo, ref); err != nil {
					errs = append(errs, fmt.Errorf("dropping %s after a refusal: %w", ref, err))
				}
				d.Refused = append(d.Refused, t.Repo.Name+" -> "+target+": "+why)
				d.Staged = append(d.Staged, held)
				continue
			}
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
// backlog), once at startup, and once when a task ends. No timer and no loop of its own: the work
// is bounded by what is there, and a runner nobody is using has nothing to retry for.
//
// It is also how a pending ref SETTLES now that the task does the pushing. This side stages the
// work when it releases a credential and then never learns what became of it, which is the honest
// position — the remote is the authority and this process is not it — so the three answers arrive
// here instead: already-up-to-date means it landed and the ref goes, an error means it did not and
// the ref stays, and a REFUSAL means no retry will ever change the answer and the ref moves to
// `held`. Without that last one a protected branch would be re-attempted on every spawn, forever,
// in the log.
//
// auth is the credential to retry under. It is handed in rather than taken from the mirror's own
// because a destination may name one of its own, and a retry has to use the same authority the
// approval released.
func (m *Mirrors) Reflect(ctx context.Context, r Repo, auth transport.AuthMethod) (int, error) {
	if len(r.Dests()) == 0 {
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
	type pending struct{ ref, task, target string }
	var todo []pending
	if err := refs.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().String()
		taskID, repo, target, ok := splitStaged(stagePending, name)
		if !ok || repo != safeSeg(r.Name) || target == "" {
			return nil // another repository's staged work, or a shape nothing here wrote
		}
		todo = append(todo, pending{ref: name, task: taskID, target: target})
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
			Force: true, Auth: auth,
		})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			if why := refusal(err); why != "" {
				if err := m.hold(mirror, p.ref, stagedRef(stageHeld, p.task, r.Name, "")); err != nil {
					errs = append(errs, err)
					continue
				}
				errs = append(errs, fmt.Errorf("%s was refused by the remote (%s), so it is held "+
					"rather than retried", p.target, why))
				continue
			}
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

// hold moves a staged ref out of the retry namespace, keeping the commit exactly where it is. A
// local ref rename in a repository this process owns — no transport, and nothing copied.
func (m *Mirrors) hold(mirror *git.Repository, from, to string) error {
	ref, err := mirror.Reference(plumbing.ReferenceName(from), false)
	if err != nil {
		return err
	}
	if err := mirror.Storer.SetReference(
		plumbing.NewHashReference(plumbing.ReferenceName(to), ref.Hash())); err != nil {
		return err
	}
	return mirror.Storer.RemoveReference(plumbing.ReferenceName(from))
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
			key := repo.Repo + "\x00" + repo.Name
			if len(repo.Dests()) == 0 || seen[key] {
				continue
			}
			seen[key] = true
			r.reflect(ctx, env, repo)
		}
	}
}

// reflect retries one repository's approved-but-undelivered work and says so when it moves any.
//
// The credential is the FIRST one any of this repository's destinations names, and the runner's
// own token when none does. That is exact in every configuration there can be — a PAT scopes per
// repository, and an environment may declare only one repository that delivers — and it is worth
// saying rather than inferring, because the alternative is a retry that silently pushes with the
// wrong authority.
func (r *Runner) reflect(ctx context.Context, env Environment, repo Repo) {
	tok := r.token
	for _, d := range repo.Dests() {
		if name := env.CredentialEnv(d); name != "" {
			tok = os.Getenv(name)
			break
		}
	}
	n, err := r.mirrors.Reflect(ctx, repo, TokenAuth(tok)(repo.Repo))
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
	Task string `tony:"field=task"`
	Repo string `tony:"field=repo"`
	Why  string `tony:"field=why"` // pending: approved, not landed. held: nobody authorized it to leave.
	Ref  string `tony:"field=ref"`
	// To is the ref this was approved to go to, and it is empty for held work — which is the
	// honest rendering of the difference, since nobody ever said where held work should land.
	To    string `tony:"field=to,omitzero"`
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
			taskID, repo, target, ok := splitStaged(kind, name)
			if !ok || repo != safeSeg(r.Name) {
				continue
			}
			out = append(out, StagedWork{
				Task: taskID, Repo: r.Name, Why: whyStaged(kind), Ref: name, To: target,
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
			if len(repo.Dests()) == 0 || seen[key] {
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
