package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// A task's checkout is a directory somebody deletes, so a copy of the work that lives only
// there is a result one `os.RemoveAll` away from nothing. Delivery is therefore two hops:
// commit, push into the runner's mirror, then push the mirror to the remote. The first hop is
// local, costs a thin pack against a repository that already has every base object, and cannot
// fail for any reason the network owns; once it is done the checkout may go.
//
// Staged ALWAYS rather than only when something goes wrong. Salvage-only code is code that runs
// exclusively when things are already broken, which is the worst moment for it to be the path
// nothing exercises. Here the recovery path is the ordinary path minus its last step.
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
	StagePending = "pending" // approved, not landed: retryable, as a fast-forward
	// StagePendingForce is pending work whose approval let it rewrite its target, so the retry
	// may too. Recorded in the namespace because the retry runs without the spawn that decided
	// it — at startup, over whatever the mirrors hold.
	StagePendingForce = "pending-force"
	StageHeld         = "held" // not authorized to leave, or never got that far
)

// stagedRef is where one repository's work waits in the mirror.
//
// A PENDING ref carries the target it was approved for as the tail of its own name, and a HELD
// one does not — which is the same distinction the two namespaces already make, written into
// the shape: pending work is going somewhere specific and held work was never told where.
//
// Carried rather than re-derived, because the target is the TASK's choice: a push names its
// branch, within what the repository permits, and nothing but the request that was approved can
// say which one it was.
func StagedRef(kind, taskID, repo, target string) string {
	ref := fmt.Sprintf("refs/%s/%s/%s", kind, SafeSeg(taskID), SafeSeg(repo))
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

// Delivery is what became of one workspace's work. The two lists are exhaustive over the
// repositories that had any, which is what makes the workspace decision a lookup rather than a
// guess: Loose is non-empty exactly when the checkout is still the only copy of something.
//
// There is no `Pushed`. Whether work reached the far side is the remote's to say, and the sweep
// (Reflect) asks it; a field here claiming it would be this process asserting something only the
// remote knows. What it can assert is what it holds, which is what these two say.
type Delivery struct {
	Staged []string // mirror refs still holding work
	Loose  []string // repos whose work is in NEITHER — do not remove this workspace
	Err    error
}

// Salvage commits what is there and puts it in the mirror under `held`, for work that was refused
// at the gate or whose task never got that far.
//
// It exists so that "the task did not deliver" stops meaning "the work is in a directory somebody
// has to know to look in". A refused push is a decision about where work goes, not a decision to
// destroy it.
//
// It names no branch, and it must not: work that nobody authorized to leave is held for every
// repository that may send work anywhere, not for one place somebody might have chosen.
func (w *Workspace) Salvage(ctx context.Context, l task.Launch) Delivery {
	var d Delivery
	var errs []error
	for _, t := range w.Trees {
		// A read-only repository is a reference tree: nothing leaves and nothing is staged,
		// because there is nowhere to hold work FOR.
		if !t.Repo.Writes() {
			continue
		}
		work, err := t.CommitWork(l, "")
		if err != nil {
			errs = append(errs, err)
			d.Loose = append(d.Loose, t.Repo.Name)
			continue
		}
		if !work {
			continue // genuinely untouched
		}
		if head, err := t.repo.Head(); err == nil && head.Hash().String() == t.Delivered {
			continue // what is here is what an approved push staged: it left
		}
		ref := StagedRef(StageHeld, l.ID, t.Repo.Name, "")
		if err := w.mirrors.stage(ctx, t, w.Branch, ref); err != nil {
			// Staging is the local hop, so this failing means the disk or the git binary, not
			// the network. The workspace is the only copy and must be kept.
			errs = append(errs, err)
			d.Loose = append(d.Loose, t.Repo.Name)
			continue
		}
		d.Staged = append(d.Staged, ref)
	}
	d.Err = errors.Join(errs...)
	return d
}

// Stage is the other half: put one repository's already-committed work in the mirror, under the
// target it has just been approved for, from where the runner pushes it (Mirrors.Deliver).
//
// It is durability before any network is tried: the mirror is this side's and a checkout is a
// directory somebody deletes. There is no commit in it — the work is already a commit by the
// time a push is asked for, which is what makes this a thin pack rather than a second opinion
// about what the task did.
func (w *Workspace) Stage(ctx context.Context, taskID string, t *Tree, target string, force bool) Delivery {
	var d Delivery
	kind := StagePending
	if force {
		kind = StagePendingForce
	}
	ref := StagedRef(kind, taskID, t.Repo.Name, target)
	if err := w.mirrors.stage(ctx, t, w.Branch, ref); err != nil {
		d.Err = err
		d.Loose = append(d.Loose, t.Repo.Name)
		return d
	}
	if head, err := t.repo.Head(); err == nil {
		t.Delivered = head.Hash().String()
	}
	d.Staged = append(d.Staged, ref)
	return d
}

// stage pushes a task's branch into the mirror under ref.
func (m *Mirrors) stage(ctx context.Context, t *Tree, branch, ref string) error {
	dir := m.Dir(t.Repo.Repo)
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
	dir := m.Dir(repo)
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

// Reflect pushes every PENDING ref in one remote's mirror to the target its task named, and drops
// the ones that land. This is the retry, and it is the reason `pending` and `held` are different
// namespaces: everything it touches has already been through a gate that said yes.
//
// Called after a mirror is fetched (so a spawn against a repository sweeps that repository's
// backlog), once at startup over every mirror there is, and once when a task ends. No timer and
// no loop of its own: the work is bounded by what is there, and a runner nobody is using has
// nothing to retry for.
//
// It is also how a pending ref SETTLES. The remote is the authority on what landed and this
// process is not it, so the three answers arrive here: already-up-to-date means it landed and
// the ref goes, an error means it did not and the ref stays, and a REFUSAL means no retry will
// ever change the answer and the ref moves to `held`. Without that last one a protected branch
// would be re-attempted on every spawn, forever, in the log.
//
// Whether a retry may rewrite its target was decided when the push was approved, and is in the
// namespace the work waits under (StagePendingForce): a retry runs without the spawn that decided
// it. auth is the runner's own credential, the one every push is made with.
func (m *Mirrors) Reflect(ctx context.Context, remote string, auth transport.AuthMethod) (int, error) {
	dir := m.Dir(remote)
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
	type pending struct {
		ref, task, repo, target string
		force                   bool
	}
	var todo []pending
	if err := refs.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().String()
		for _, kind := range []string{StagePending, StagePendingForce} {
			taskID, repo, target, ok := splitStaged(kind, name)
			if !ok || target == "" {
				continue // held work, or a shape nothing here wrote
			}
			todo = append(todo, pending{ref: name, task: taskID, repo: repo, target: target, force: kind == StagePendingForce})
		}
		return nil
	}); err != nil {
		return 0, err
	}

	var done int
	var errs []error
	for _, p := range todo {
		refusal, err := m.deliver(ctx, mirror, remote, p.ref, p.target, p.force, auth)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("reflect %s -> %s: %w", p.ref, p.target, err))
		case refusal != "":
			errs = append(errs, fmt.Errorf("%s was refused by the remote (%s), so it is held "+
				"rather than retried", p.target, refusal))
		default:
			done++
		}
	}
	return done, errors.Join(errs...)
}

// Deliver pushes one staged ref from the mirror to its target, under a credential handed in for
// this one operation. It is the push itself, made by the side that holds the mirror and the
// credential: the task's checkout never pushes anything, so no credential is ever in a process
// the model drives. On success the staged ref goes; a REFUSAL by the remote — not a fast-forward,
// a protected branch, a rule — moves it to `held` and comes back as words, since an authority
// outside verse deciding about its own history is an answer and not a failure; any other error
// leaves the ref where it is, for the sweep.
func (m *Mirrors) Deliver(ctx context.Context, remote, ref, target string, force bool, auth transport.AuthMethod) (refusal string, err error) {
	dir := m.Dir(remote)
	lock := m.lockFor(dir)
	lock.Lock()
	defer lock.Unlock()

	mirror, err := git.PlainOpen(dir)
	if err != nil {
		return "", fmt.Errorf("mirror of %s: %w", remote, err)
	}
	return m.deliver(ctx, mirror, remote, ref, target, force, auth)
}

// deliver is Deliver under the mirror's lock, for Reflect as well.
func (m *Mirrors) deliver(ctx context.Context, mirror *git.Repository, remote, ref, target string, force bool, auth transport.AuthMethod) (string, error) {
	spec := config.RefSpec(fmt.Sprintf("%s:%s", ref, target))
	if force {
		spec = "+" + spec
	}
	// The remote is PINNED to the url the spawn named, held in memory — never a name looked up
	// in any `.git/config`, which is a file in a directory somebody else writes.
	rem := git.NewRemote(mirror.Storer, &config.RemoteConfig{
		Name: deliveryRemote, URLs: []string{remote},
	})
	err := rem.PushContext(ctx, &git.PushOptions{
		RemoteName: deliveryRemote, RefSpecs: []config.RefSpec{spec}, Force: force, Auth: auth,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		if why := Refusal(err); why != "" {
			taskID, repo, _, _ := splitStaged(kindOf(ref), ref)
			if err := m.hold(mirror, ref, StagedRef(StageHeld, taskID, repo, "")); err != nil {
				return "", err
			}
			return why, nil
		}
		return "", err
	}
	return "", mirror.Storer.RemoveReference(plumbing.ReferenceName(ref))
}

// kindOf is the namespace a staged ref is under.
func kindOf(ref string) string {
	for _, kind := range []string{StagePending, StagePendingForce, StageHeld} {
		if strings.HasPrefix(ref, "refs/"+kind+"/") {
			return kind
		}
	}
	return ""
}

// DeleteRef removes a ref at the remote — a push with no source, which is how git spells a
// delete and why this needs no commit and nothing staged — made from the mirror by the side that
// holds the credential.
//
// Not forced, and the difference matters here in a way it does not for a delivery. `+` on a
// refspec overrides the receiving end's rules, and the rule this one would override is
// `receive.denyDeletes` — the operator's own statement that a ref pattern is not to be removed.
// A sweep that forced past it would be verse claiming an authority over somebody's repository
// that the whole design says belongs to the receiving end.
func (m *Mirrors) DeleteRef(ctx context.Context, remote, target string, auth transport.AuthMethod) error {
	dir := m.Dir(remote)
	lock := m.lockFor(dir)
	lock.Lock()
	defer lock.Unlock()

	mirror, err := git.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("mirror of %s: %w", remote, err)
	}
	rem := git.NewRemote(mirror.Storer, &config.RemoteConfig{
		Name: deliveryRemote, URLs: []string{remote},
	})
	if err := rem.PushContext(ctx, &git.PushOptions{
		RemoteName: deliveryRemote, RefSpecs: []config.RefSpec{config.RefSpec(":" + target)}, Auth: auth,
	}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("delete %s at %s: %w", target, remote, err)
	}
	return nil
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

// StagedWork is one commit the runner is holding: what it was for, why it is still here, and the
// exact command that gets it out.
//
// The recovery instruction is part of the record rather than something to look up, because the
// mirror is a directory named by a HASH of a remote url. Told only "the work is in the mirror", the
// next question is always "which one, and how do I read it", and the answer involves knowing that
// the runner hashes remotes at all.
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

// Staged is everything waiting in one remote's mirror, every namespace.
//
// It exists because staging made workspaces disposable, and a workspace was the thing an operator
// could `cd` into. Removing that without replacing it would have traded a lost result for an
// unfindable one, which is the same failure wearing a better hat.
func (m *Mirrors) Staged(remote string) ([]StagedWork, error) {
	dir := m.Dir(remote)
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
		for _, kind := range []string{StagePending, StagePendingForce, StageHeld} {
			taskID, repo, target, ok := splitStaged(kind, name)
			if !ok {
				continue
			}
			out = append(out, StagedWork{
				Task: taskID, Repo: repo, Why: whyStaged(kind), Ref: name, To: target,
				Fetch: fmt.Sprintf("git fetch %s %s:refs/heads/recovered/%s-%s", dir, name, taskID, repo),
			})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, err
}

func whyStaged(kind string) string {
	if kind == StageHeld {
		return "nobody authorized this to leave: refused at the gate, or the task never got that far"
	}
	return "delivery was approved and has not landed — the runner retries it"
}

// Remotes is every remote this runner has a mirror of: what a sweep at startup covers, when no
// spawn is here to say which repositories there are. A mirror records its remote as `origin`.
func (m *Mirrors) Remotes() ([]string, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := git.PlainOpen(filepath.Join(m.root, e.Name()))
		if err != nil {
			continue // not a mirror this runner made
		}
		origin, err := r.Remote("origin")
		if err != nil || len(origin.Config().URLs) == 0 {
			continue
		}
		out = append(out, origin.Config().URLs[0])
	}
	sort.Strings(out)
	return out, nil
}
