package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/signadot/verse/task"
)

// Git is go-git, not the git binary, and the difference is worth stating because it decides the
// shape of everything below.
//
// A runner that shells out inherits an ambient world: whichever `git` is on PATH, whatever
// credential helper the machine has configured, whatever `~/.gitconfig` says. All three are
// invisible in the code and none of them is declared anywhere — which is the same complaint
// this example makes about `run_bash` inheriting the server's environment. In particular the
// CREDENTIAL becomes ambient, and a runner whose whole delivery model rests on the task not
// being able to reach the credential should be able to say where the credential comes from.
//
// One correction, since the claim above is broader than the truth: go-git's LOCAL transport shells
// out. `transport/file` runs `git-upload-pack` and `git-receive-pack`, so the mirror→clone step
// below needs those two binaries on the runner's PATH — verified by shimming them, at which point
// the clone fails. What is genuinely go-git is every remote that carries a credential, which is
// where the argument above actually lives: nothing here hands a token to a subprocess, and no
// credential helper or ssh agent is consulted. The git binary is a build dependency of the local
// path, not an ambient authority.
//
// The cost is real and is paid here: go-git has no linked worktrees. `git worktree add` shares
// one object store across many working trees, and go-git offers nothing equivalent (its
// alternates support needs a non-obvious AlternatesFS and a path convention the git binary does
// not share). So a task gets a CLONE off a local mirror rather than a worktree: objects are
// copied per task, and what the mirror still saves is the network fetch, which is the expensive
// part.
//
// One thing gets simpler in exchange, and it is not nothing. With linked worktrees the task
// branches live in the mirror's own ref namespace, so a `fetch --prune` on the next spawn
// deletes the branch of every running task — a silent failure that leaves a worktree's HEAD
// pointing at nothing. With a clone per task, task branches live in the task's own repository
// and the mirror holds upstream refs only, so pruning cannot reach them.

// Workspace is one task's working directory: every repository the environment names, cloned
// side by side under one root, each on the same task branch.
//
// The root is what the agent gets as its working directory, so `verse/task/loop.go` and
// `tony-format/ir/node.go` are both just paths to it, and a change that spans two repositories
// is one piece of work rather than two tasks that have to be coordinated.
type Workspace struct {
	Env    Environment
	Root   string // the directory holding every checkout: the agent's working directory
	Branch string // verse/auto/<task id> — the same in every repository
	Trees  []*Tree

	// mirrors is where work waits on its way out (stage.go). Set by Open and nil in a Reopen:
	// the task process holds neither the mirrors nor the credential, so the side that cannot
	// deliver also cannot stage, which is one fact rather than two rules.
	mirrors *Mirrors
}

// Tree is one repository's checkout inside a workspace.
type Tree struct {
	Repo Repo
	Dir  string // Root/<repo name>
	Head string // the sha it started from, so a diff has something to be against

	// repo is the checkout. There is no credential beside it, on either side of the process
	// boundary: a push carries one handed in for that operation alone (pushAs).
	repo *git.Repository
}

// Primary is the repository a singular question is about — the first one the environment
// names. `task.RepoContext` has one Branch, one Base and one PushTarget, and a workspace has as
// many as it has repositories, so the two do not fit: see RepoContext.
func (w *Workspace) Primary() *Tree {
	if len(w.Trees) == 0 {
		return nil
	}
	return w.Trees[0]
}

// Mirrors keeps one bare mirror per repository, fetched before a task clones off it — and, since
// staging, the place a task's work waits on its way out (stage.go).
//
// It is per REPOSITORY rather than per environment so two environments on one repo at different
// bases share it — and the lock is per repository too, because two concurrent fetches into one
// directory are not safe.
type Mirrors struct {
	root  string
	auth  func(remote string) transport.AuthMethod
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewMirrors(root string, auth func(remote string) transport.AuthMethod) *Mirrors {
	return &Mirrors{root: root, auth: auth, locks: map[string]*sync.Mutex{}}
}

func (m *Mirrors) lockFor(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks[key] == nil {
		m.locks[key] = &sync.Mutex{}
	}
	return m.locks[key]
}

// dirFor names a mirror by a hash of its remote rather than by anything readable, because a
// remote is a url or a path and neither survives being a directory name.
func (m *Mirrors) dirFor(repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(m.root, hex.EncodeToString(sum[:8]))
}

// TokenAuth builds the credential from an environment variable, and nothing else does.
//
// Explicit rather than ambient is the whole point of the change: the git binary would have
// found a credential helper, an ssh agent, or a `~/.git-credentials` without this function
// existing, and a reader of the code would have no way to tell which. A runner that pushes on
// behalf of work it did not write should be able to say where its authority came from.
//
// Empty token = no auth, which is right for a local path and is what the tests run on. SSH and
// per-host credentials are the obvious extensions and this is where they go.
func TokenAuth(token string) func(string) transport.AuthMethod {
	return func(remote string) transport.AuthMethod {
		if token == "" || !strings.HasPrefix(remote, "http") {
			return nil
		}
		// The username is ignored by every forge that takes a token this way; the token is
		// the password. Named `x-access-token` because that is what GitHub documents.
		return &http.BasicAuth{Username: "x-access-token", Password: token}
	}
}

// ensure creates the mirror if it is not there and fetches it either way. The fetch happens on
// every spawn, which is the honest place for it: a task that starts from a base an hour stale is
// a task whose work is against a tree nobody has.
func (m *Mirrors) ensure(ctx context.Context, remote string) (string, error) {
	dir := m.dirFor(remote)
	lock := m.lockFor(dir)
	lock.Lock()
	defer lock.Unlock()

	r, err := git.PlainOpen(dir)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if r, err = git.PlainInit(dir, true); err != nil {
			return "", fmt.Errorf("mirror %s: %w", remote, err)
		}
		// `refs/heads/*` here mirrors upstream exactly — task branches live in each task's own
		// clone — so this refspec is unambiguous.
		//
		// It is also what makes the STAGED namespaces safe, and that is now the load-bearing
		// half. go-git's pruneRemotes removes only refs matching the reverse of a configured
		// refspec, so `refs/pending/*` and `refs/held/*` (stage.go) are outside everything this
		// prunes. Put work under `refs/heads/` instead and the next spawn deletes it without a
		// word — which this runner has already done once, to the task branches of every running
		// task.
		if _, err = r.CreateRemote(&config.RemoteConfig{
			Name: "origin", URLs: []string{remote},
			Fetch: []config.RefSpec{"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"},
		}); err != nil {
			return "", fmt.Errorf("mirror %s: %w", remote, err)
		}
	} else if err != nil {
		return "", fmt.Errorf("mirror %s: %w", remote, err)
	}

	err = r.FetchContext(ctx, &git.FetchOptions{RemoteName: "origin", Prune: true, Auth: m.auth(remote)})
	switch {
	case err == nil, errors.Is(err, git.NoErrAlreadyUpToDate):
		// A fresh bare repository's HEAD points at `refs/heads/master`, which is a branch most
		// remotes no longer have — and a dangling HEAD makes every clone off this mirror fail
		// with "reference not found" rather than with anything about HEAD. Pointed at a branch
		// that exists, here, under the lock, because it is a property of the mirror and not of
		// any one task's base.
		if err := fixHead(r); err != nil {
			return dir, fmt.Errorf("mirror %s: %w", remote, err)
		}
		return dir, nil
	default:
		// Not fatal on its own: the mirror still holds whatever it had, and a task off a
		// slightly stale base beats no task at all when a remote is briefly unreachable. It is
		// reported rather than swallowed, because "the base is older than you think" is exactly
		// the surprise worth naming.
		return dir, fmt.Errorf("fetch %s (using what the mirror already had): %w", remote, err)
	}
}

// fixHead points a mirror's HEAD at a branch that exists, preferring the conventional defaults
// and otherwise taking the first in name order so two runs agree. A mirror with no branches at
// all is left alone: there is nothing to point at, and the clone's error is then about the base
// the environment named, which is the more useful complaint.
func fixHead(r *git.Repository) error {
	if _, err := r.Head(); err == nil {
		return nil
	}
	branches, err := r.Branches()
	if err != nil {
		return err
	}
	var names []string
	if err := branches.ForEach(func(ref *plumbing.Reference) error {
		names = append(names, ref.Name().String())
		return nil
	}); err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	pick := names[0]
	for _, want := range []string{"refs/heads/main", "refs/heads/master"} {
		if slices.Contains(names, want) {
			pick = want
			break
		}
	}
	return r.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName(pick)))
}

// autoBranchPrefix namespaces every branch a task works on, and so every ref a delivery pushes
// from. `verse/auto/` rather than `agent/`, and the reason is a person rather than a machine: a
// developer who pulls and finds a commit they did not write asks what made it, and the first
// segments of a branch name are the cheapest place to answer — which system, and that nothing
// human proposed it. `agent/` said neither, and there is more than one thing in a tree that
// calls itself an agent.
const autoBranchPrefix = "verse/auto/"

// Open builds the workspace for one task: every repository the environment names, checked out
// under one root.
//
// A repository that cannot be opened fails the whole workspace rather than yielding a partial
// one. An agent handed three of the four it was promised does not know that, and will read an
// absent directory as "this code does not exist" rather than "I was short-changed".
func (m *Mirrors) Open(ctx context.Context, id string, env Environment, worktrees string) (*Workspace, error) {
	branch := autoBranchPrefix + safeSeg(id)
	// ABSOLUTE, and here rather than at the flag that supplied it, because this path CROSSES A
	// PROCESS BOUNDARY into a different working directory. It travels to the task process in
	// childConfig, and that process is started with its cwd set to the workspace itself — so a
	// relative root resolves against the runner on this side and against the workspace on that
	// one, and the two are not the same place.
	//
	// What it looks like is a child that exits immediately with `reopen verse in <path>:
	// repository does not exist`, naming a path that is correct from where you are standing and
	// wrong from where it ran. Found by starting the runner with `-data scratchpad/…`, which is
	// the ordinary thing to type; every test passes a t.TempDir(), which is already absolute, so
	// nothing here could have caught it.
	root, err := filepath.Abs(filepath.Join(worktrees, safeSeg(id)))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	w := &Workspace{Env: env, Root: root, Branch: branch, mirrors: m}
	for _, r := range env.Repos {
		t, err := m.open(ctx, r, branch, root)
		if err != nil {
			_ = w.Remove(ctx) // no partial workspaces
			return nil, err
		}
		w.Trees = append(w.Trees, t)
	}
	return w, nil
}

func (m *Mirrors) open(ctx context.Context, r Repo, branch, root string) (*Tree, error) {
	mirror, err := m.ensure(ctx, r.Repo)
	if err != nil && mirror == "" {
		return nil, fmt.Errorf("repo %s: %w", r.Name, err)
	}
	// Resolve the base against the MIRROR, which handles a branch, a tag and a sha uniformly —
	// so `base: main`, `base: v1.2.0` and `base: 9fceb02` all mean what they look like.
	mr, err := git.PlainOpen(mirror)
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", r.Name, err)
	}
	base, err := mr.ResolveRevision(plumbing.Revision(r.Base))
	if err != nil {
		return nil, fmt.Errorf("repo %s: base %q: %w", r.Name, r.Base, err)
	}

	dir := filepath.Join(root, r.Name)
	// Cloned from the LOCAL mirror, so this costs no network. NoCheckout because the checkout
	// below is the one that creates the task branch, and doing it twice would write the tree
	// twice.
	repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL: mirror, RemoteName: "mirror", NoCheckout: true,
	})
	if err != nil {
		return nil, fmt.Errorf("repo %s: clone: %w", r.Name, err)
	}
	// `origin` is the REAL remote, and it is added here rather than being what we cloned from
	// so that a push goes upstream rather than into this runner's cache.
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{r.Repo}}); err != nil {
		return nil, fmt.Errorf("repo %s: %w", r.Name, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", r.Name, err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{
		Hash: *base, Branch: plumbing.NewBranchReferenceName(branch), Create: true,
	}); err != nil {
		return nil, fmt.Errorf("repo %s at %s: %w", r.Name, r.Base, err)
	}
	return &Tree{Repo: r, Dir: dir, Head: base.String(), repo: repo}, nil
}

// Reopen rebuilds a Workspace over a checkout that already exists — the task process's view of
// what the runner prepared for it (see taskproc.go).
//
// There is no auth on these trees, and that is not the same claim it used to be. A credential
// does reach that side now — but only per operation, only after a gate approved the destination
// it is for, and never as state a tree carries around. See pushAs.
//
// Head is HANDED OVER rather than read here. This side does not need it to diff — that is the
// parent's job — but it does need it to name the push target its delivery proposes, since a
// `$sha` in that target has to expand to the same thing on both sides or a gate matches a ref
// the push never wrote. Reading it from the checkout would agree today and stop agreeing the
// moment a task committed.
func Reopen(root string, env Environment, branch string, bases map[string]string) (*Workspace, error) {
	w := &Workspace{Env: env, Root: root, Branch: branch}
	for _, r := range env.Repos {
		dir := filepath.Join(root, r.Name)
		repo, err := git.PlainOpen(dir)
		if err != nil {
			return nil, fmt.Errorf("reopen %s in %s: %w", r.Name, root, err)
		}
		w.Trees = append(w.Trees, &Tree{Repo: r, Dir: dir, repo: repo, Head: bases[r.Name]})
	}
	return w, nil
}

// Brief tells the agent what its working directory contains, and it is the runner's to say
// rather than the charter's.
//
// The layout is a CONVENTION of this runner — one directory per repository, named by the
// environment, under a root nobody outside chose — so a prompt written against it would be
// guessing, and a prompt written against a single-repo workspace ("look in docs/") is simply
// wrong the moment a second repository appears. An agent dropped into a directory it did not
// create, with no map, spends its first turns discovering one.
func (w *Workspace) Brief() string {
	if len(w.Trees) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Your working directory holds ")
	if len(w.Trees) == 1 {
		b.WriteString("one repository:\n")
	} else {
		fmt.Fprintf(&b, "%d repositories, side by side:\n", len(w.Trees))
	}
	for _, t := range w.Trees {
		fmt.Fprintf(&b, "  %s/ — %s at %s", t.Repo.Name, t.Repo.Repo, t.Repo.Base)
		if len(t.Repo.Dests()) == 0 {
			// Saying so matters: an agent that edits a reference tree has done work that will
			// be thrown away, and nothing downstream would report that it was.
			b.WriteString(" (reference only — changes here are not delivered anywhere)")
		}
		b.WriteString("\n")
	}
	b.WriteString("Every path is relative to that directory, so a file in the first repository " +
		"above is `" + w.Trees[0].Repo.Name + "/…`.\n\n")
	return b.String()
}

// RepoContext is what a gate can match on: the branch this work is on, the base it came from,
// what it has touched, and where it would go. `GateMatch.Branches` and `GateMatch.OnPush` read
// exactly this, so a charter can say `vector: ask` on anything touching a release branch.
//
// It is recomputed per call rather than cached because ChangedFiles is the whole point and it
// changes under the task's own hands.
//
// The awkward part, named rather than hidden: `task.RepoContext` is SINGULAR and a workspace is
// not. Root and ChangedFiles generalize cleanly — the root is the working directory and paths
// are relative to it, so `verse/task/loop.go` says which repository it is in. Branch does too,
// since every repository carries the same task branch. Base and PushTarget do not, so they
// report the PRIMARY repository's, and a gate matching on them is asking about that one. A
// workspace whose repositories have different bases and push targets is exactly the case that
// wants a plural RepoContext, and this is where it would go.
func (w *Workspace) RepoContext() *task.RepoContext {
	rc := &task.RepoContext{Root: w.Root, Branch: w.Branch}
	if p := w.Primary(); p != nil {
		rc.Base = p.Repo.Base
		rc.PushTarget = p.PushTarget(w.Branch)
	}
	for _, t := range w.Trees {
		for _, f := range t.changed() {
			// Repo-relative becomes workspace-relative, which is what the agent sees and what a
			// `paths:` predicate in a charter should be written against.
			rc.ChangedFiles = append(rc.ChangedFiles, filepath.Join(t.Repo.Name, f))
		}
	}
	return rc
}

// Destination resolves a name a task asked for: which repository declares it, and the ref it
// expands to. A nil tree means no repository here declares that name, which is the refusal — a
// task names a destination and never a ref, so the set of names is the set of places work can go.
//
// One repository, because ONE PUSH ACTION IS ONE REPOSITORY, and an environment that could make
// that ambiguous is refused when the config is read (checkDelivering).
func (w *Workspace) Destination(name string) (*Tree, string) {
	for _, t := range w.Trees {
		if ref := t.DestRef(name, w.Branch); ref != "" {
			return t, ref
		}
	}
	return nil, ""
}

// Retirable resolves a destination that DELETES: the repository that declares it, and the ref
// the task asked to remove, once that ref has been checked against what the environment permits.
//
// The check is the whole of the safety, and it is done HERE and again in the parent, because the
// task supplies the ref. Every other destination is resolved from the environment alone and a
// task can only pick a name; this one takes an argument, so the argument is validated against
// the glob the environment wrote rather than trusted.
//
// A ref outside the pattern, or a destination that does not delete, resolves to nothing — which
// is the same answer an undeclared name gets, and refuses it before anything is gated.
func (w *Workspace) Retirable(name, ref string) (*Tree, string) {
	if ref == "" {
		return nil, ""
	}
	for _, t := range w.Trees {
		d, ok := t.Repo.Dest(name)
		if !ok || !d.Delete {
			continue
		}
		full := ref
		if !strings.HasPrefix(full, "refs/") {
			// A person and an agent both say `verse/auto/x/y`; the pattern and the refspec are
			// both full refs. Normalised in one place so the glob is matched against one shape.
			full = "refs/heads/" + full
		}
		if ok, err := filepath.Match(d.Ref, full); err == nil && ok {
			return t, full
		}
	}
	return nil, ""
}

// DestinationNames is every name a task here may ask for, in the order the environment wrote
// them. It is what the push tool offers the model as an enumeration, so the vocabulary a runtime
// will accept is the vocabulary the model can write rather than something it finds out by being
// refused.
func (w *Workspace) DestinationNames() []string {
	var out []string
	for _, t := range w.Trees {
		for _, d := range t.Repo.Dests() {
			out = append(out, d.Name)
		}
	}
	return out
}

// changed is this tree's dirty paths, relative to itself, sorted so two readers see one order
// (go-git returns a map).
func (t *Tree) changed() []string {
	wt, err := t.repo.Worktree()
	if err != nil {
		return nil
	}
	st, err := wt.Status()
	if err != nil {
		return nil
	}
	files := make([]string, 0, len(st))
	for path := range st {
		files = append(files, path)
	}
	sort.Strings(files)
	return files
}

// DestRef is where a NAMED destination of this repository goes, with $task and $sha expanded.
// Empty means this repository declares no such destination — which is how a name the
// environment never wrote is refused, before anything is gated and before any credential is
// anywhere near it.
func (t *Tree) DestRef(name, branch string) string {
	d, ok := t.Repo.Dest(name)
	if !ok {
		return ""
	}
	out := strings.ReplaceAll(d.Ref, "$task", strings.TrimPrefix(branch, autoBranchPrefix))
	return strings.ReplaceAll(out, "$sha", shortSha(t.Head))
}

// PushTarget is where this repository's own work goes: the destination named `auto`, which is
// what `push:` has always meant. Empty means nowhere.
//
// `$sha` is the commit the work started from, and it exists because `$task` alone is not unique
// over time. A charter's task id is a function of (chain, subject), so a rule whose subject is
// one long-lived entity — main's tip, a repository, a queue — mints the SAME id for every firing
// and every fix lands on one ref, each overwriting the last. That is invisible while the
// overwritten branch was merged or redundant, and it is lost work the first time it was not.
//
// With the base sha in it, two reviews of two commits are two branches and two reviews of ONE
// commit are one, which is the right identity in both directions: a re-run of the same review
// should land where the first one did.
//
// Both sides compute this — the parent to push, the child to declare the effect its delivery
// gate matches on — so the sha travels in the child's config rather than being re-derived there.
// Re-deriving it from the checkout would agree today, because a task in these environments has
// no way to commit, and would diverge the moment one did: the gate would name a ref the push
// never wrote.
func (t *Tree) PushTarget(branch string) string { return t.DestRef(autoDestination, branch) }

// shortSha is the branch-name form of a commit: enough to be unique in a repository this size
// and short enough that a ref stays readable next to a task id that is already long.
func shortSha(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// commitWork commits whatever the task produced and reports whether there is anything to deliver.
//
// It is the first half of what used to be one `deliver`, and splitting it is what lets the work be
// STAGED before it is sent (stage.go): the commit is local and always safe, the push is neither.
//
// The trailers are the attribution channel. A push made under a shared bot credential says nothing
// at the far end about who it was for, so the only externally checkable link back into the verse is
// what this writes down — and it is checkable, by anyone who can read both the repository and the
// verse. See docs/working/attribution.md.
func (t *Tree) commitWork(l task.Launch, result string) (bool, error) {
	// What counts as work is "the branch moved OR the tree is dirty", not "the tree is dirty".
	//
	// The difference is not pedantry: an agent that commits its own edits — which a coding
	// agent will do, told not to or otherwise — leaves a CLEAN tree with its work sitting in
	// commits. Testing only for dirt read that as "nothing happened", delivered nothing, and
	// (before the workspace rule learned better) removed the workspace, taking the only copy
	// with it. That is the exact shape of the first real result this runner produced and lost.
	dirty := len(t.changed()) > 0
	moved := false
	if head, err := t.repo.Head(); err == nil {
		moved = head.Hash().String() != t.Head // Head is the sha this checkout started from
	}
	if !dirty && !moved {
		return false, nil // genuinely untouched; an empty commit would be a lie about work
	}
	if !dirty {
		// Already committed by whatever did the work: take it as it stands rather than putting
		// an empty commit on top purely to carry trailers.
		//
		// The cost is real and is the reason a prompt should still say "do not commit": the
		// trailers are this runner's only attribution channel out of the verse, and a commit
		// the agent wrote carries none of them. Work preserved without provenance beats work
		// destroyed, which is the trade being made here and not a preference.
		return true, nil
	}
	wt, err := t.repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("repo %s: %w", t.Repo.Name, err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return false, fmt.Errorf("repo %s: %w", t.Repo.Name, err)
	}
	// The committer identity is the RUNNER's, and it says so. It is not the principal's: the
	// runner made this commit on somebody's behalf, and writing their name into the author
	// field would assert something no forge can check. Who it was for is a trailer.
	sig := &object.Signature{Name: "verse coderunner", Email: "coderunner@invalid", When: time.Now()}
	if _, err := wt.Commit(commitMessage(l, result), &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		return false, fmt.Errorf("repo %s: commit: %w", t.Repo.Name, err)
	}
	return true, nil
}

// pushAs sends this repository's task branch to a ref, under a credential handed in FOR THIS ONE
// OPERATION.
//
// Per-operation is the whole of it, and it is why no Tree carries a credential any more. The
// secret arrives with the gate's approval that released it, is used where it arrived, and is
// stored nowhere — and no `.git/config`, credential helper or ssh agent is consulted, which is
// the same argument TokenAuth exists for, one process over.
func (t *Tree) pushAs(ctx context.Context, branch, target string, auth transport.AuthMethod) error {
	// Force, and the reason is that the branch is namespaced by the TASK ID: this runner is its
	// only writer by construction, so the only thing a force can overwrite is an earlier attempt
	// of the same task, which is what a retried spawn means.
	spec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:%s", branch, target))

	// The remote is PINNED to the url the environment configured, held in memory — and
	// deliberately not the `origin` in the task repository's own `.git/config`.
	//
	// That distinction is the whole of it. `.git/config` lives inside the workspace, which is
	// the one place the agent can write, and go-git resolves a RemoteName through
	// Repository.Config() at push time — it re-reads the file. So pushing by NAME lets a task
	// redirect its own delivery by editing a file in its working directory, and this runner's
	// credential travels to wherever it points. That is not hypothetical: an agent that
	// rewrites the origin url receives the push, verified against a scratch remote.
	//
	// Pinning removes the path that needs nothing but the workspace. It does not make delivery
	// safe on its own — at `filesystem.scope: shared` the agent can reach the mirror and this
	// runner's own files too — which is what the containment classes are for, and why the
	// confiner refuses anything it cannot actually build.
	remote := git.NewRemote(t.repo.Storer, &config.RemoteConfig{
		Name: deliveryRemote, URLs: []string{t.Repo.Repo},
	})
	if err := remote.PushContext(ctx, &git.PushOptions{
		RemoteName: deliveryRemote, RefSpecs: []config.RefSpec{spec}, Force: true, Auth: auth,
	}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("repo %s: push %s: %w", t.Repo.Name, target, err)
	}
	return nil
}

// refusal reads a failed push and reports what the FAR SIDE said when it declined, or "" when
// the failure was not a decision at all.
//
// The distinction is the whole of how a push is reported, and getting it backwards fills the
// record with alarm about the thing working as intended. A remote that will not fast-forward, a
// protected branch, a ruleset, a hook: those are an authority OUTSIDE verse exercising rules
// about its own history — which is precisely the authority this design deliberately does not
// take — docs/reference/examples/README.md is blunt that verse should not compete with git
// server configuration. The task tried, correctly, and the answer is no. A missing credential or an
// unreachable host is not an answer at all.
//
// It is a string match, and that is what the transport gives: go-git renders receive-pack's `ng`
// line as `command error on <ref>: <status>` and its own pre-flight check as `non-fast-forward
// update: <ref>`, and neither is reachable as a typed error from PushContext. Auth is typed, so
// auth is matched typed — and it is matched FIRST, because an authorization failure at the
// transport is infrastructure however much the word looks like a refusal.
func refusal(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, transport.ErrAuthenticationRequired) ||
		errors.Is(err, transport.ErrAuthorizationFailed) {
		return ""
	}
	msg := err.Error()
	for _, mark := range []string{"non-fast-forward", "command error on "} {
		if strings.Contains(msg, mark) {
			return msg
		}
	}
	return ""
}

// deleteRef removes a ref at the remote — a push with no source, which is how git spells a
// delete and why this needs no commit, no branch and nothing staged.
//
// Not forced, and the difference matters here in a way it does not for a delivery. `+` on a
// refspec overrides the receiving end's rules, and the rule this one would override is
// `receive.denyDeletes` — the operator's own statement that a ref pattern is not to be removed.
// A sweep that forced past it would be verse claiming an authority over somebody's repository
// that the whole design says belongs to the receiving end.
func (t *Tree) deleteRef(ctx context.Context, ref string, auth transport.AuthMethod) error {
	remote := git.NewRemote(t.repo.Storer, &config.RemoteConfig{
		Name: deliveryRemote, URLs: []string{t.Repo.Repo},
	})
	if err := remote.PushContext(ctx, &git.PushOptions{
		RemoteName: deliveryRemote, RefSpecs: []config.RefSpec{config.RefSpec(":" + ref)}, Auth: auth,
	}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("repo %s: delete %s: %w", t.Repo.Name, ref, err)
	}
	return nil
}

// deliveryRemote is the in-memory remote a push is made through. It is not a remote in any
// `.git/config` and must not become one: the name exists only so go-git's own check that the
// options and the remote agree has something to compare.
const deliveryRemote = "verse-delivery"

// commitMessage carries the provenance the runner was given. Every trailer is conditional: an
// absent one means verse did not know, and writing it out empty would claim to have asked.
func commitMessage(l task.Launch, result string) string {
	subject, body := firstLine(result), ""
	if isDocument(result) {
		// A CONTRACT-shaped answer is not a sentence, and a subject line is. A task with an
		// `answer:` contract returns a document — the whole point of the field — so its first
		// line is `{"files": 1, "fixed": 1, "findings": [{"symbol": …` truncated at 72
		// characters, which is what `git log --oneline` then shows forever. One is on main.
		//
		// So the document becomes the BODY, where it is worth reading, and the subject is
		// derived from what verse knows about the run. The alternative — digging a `report` or
		// `summary` field out of the answer — would make this runner know a charter's answer
		// shape, which is the one thing about a contract that is deliberately the charter's.
		subject, body = describe(l), result
	}
	if subject == "" {
		subject = describe(l)
	}
	var b strings.Builder
	b.WriteString(subject)
	b.WriteString("\n\n")
	if body != "" {
		// Before the trailers and separated from them, because git reads trailers from the LAST
		// paragraph and takes it only if every line in it is one. A document pasted after them
		// would silently un-declare every Verse-* line below it.
		b.WriteString(strings.TrimRight(body, "\n"))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "Verse-Task: %s\n", l.ID)
	p := l.Provenance
	if p.Principal != "" {
		fmt.Fprintf(&b, "Verse-Principal: %s\n", p.Principal)
	}
	if p.Subject != "" {
		fmt.Fprintf(&b, "Verse-Subject: %s\n", p.Subject)
	}
	if p.Chain != "" {
		fmt.Fprintf(&b, "Verse-Chain: %s\n", p.Chain)
	}
	if l.Environment != "" {
		fmt.Fprintf(&b, "Verse-Environment: %s\n", l.Environment)
	}
	return b.String()
}

// isDocument reports whether a result is a structured answer rather than prose — the shape an
// `answer:` contract asks for. JSON and tony both start a document the same way, and a sentence
// does not start with either.
//
// A FENCE COUNTS. A model asked for a document commonly returns it fenced, and the fence line is
// not the document — so `firstLine` took ```` ```json ```` as the subject and every branch the
// drift flow delivered carried it. `git log --oneline` on main read as a wall of ```` ```json ````
// and `git shortlog` grouped them as one subject, which is the one place a reader looks to see
// what an agent changed (git-issue qwbacp11h12krq1tgsn0).
//
// Deliberately shallow: it decides where the text GOES and never what it means, so being wrong
// costs a commit whose subject is derived rather than quoted, which is the safer of the two
// mistakes. Parsing it to be sure would make this runner a reader of somebody's answer shape.
func isDocument(result string) bool {
	s := strings.TrimSpace(result)
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") || strings.HasPrefix(s, "```")
}

// describe is the subject line for work that did not come with a sentence: what fired, about
// what. `comment-drift on drift:head:head-moved@git:head:verse` says more to somebody reading
// `git log` than the task id does, and both are recorded in the trailers below anyway.
func describe(l task.Launch) string {
	p := l.Provenance
	switch {
	case p.Chain != "" && p.Subject != "":
		return firstLine(p.Chain + " on " + p.Subject)
	case p.Chain != "":
		return firstLine(p.Chain)
	}
	return "work by " + l.ID
}

// Deliverable reports whether any repository here was supposed to send work somewhere. False
// means a read-only workspace — nothing was ever going to leave it — which is the only case
// where removing it after delivering nothing is not destroying something.
func (w *Workspace) Deliverable() bool {
	for _, t := range w.Trees {
		if len(t.Repo.Dests()) > 0 {
			return true
		}
	}
	return false
}

// Remove drops the whole workspace. Called on terminal unless -keep, because the alternative is
// a disk that fills at the rate work arrives — an argument about SUCCESSFUL runs, and see
// Runner.finish for the cases where it does not apply.
//
// One directory tree and nothing else — no worktree to unregister and no branch to delete out
// of a shared mirror, because a clone per task keeps every task's refs inside the directory
// being removed.
func (w *Workspace) Remove(_ context.Context) error { return os.RemoveAll(w.Root) }

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 72 {
		s = s[:72]
	}
	return s
}

// safeSeg makes a task id usable as ONE directory name and ONE git ref component. A task id is
// chosen by whoever spawns — including a client posting straight at this runner — so it is
// untrusted input that becomes a path and a ref.
//
// Dots are mapped out along with everything else, which is not fussiness. `.` and `..` are
// legal-looking ids that are lethal as path segments: `filepath.Join(root, "..")` is the parent
// of every workspace, and Remove does `os.RemoveAll` on what this returns. They are invalid in a
// ref component too — git rejects `..` in a ref name, and a leading dot, and a `.lock` suffix —
// so allowing them buys nothing and costs a directory traversal.
//
// It is lossy, so a rewrite gets a hash of the ORIGINAL appended: without it `a.b` and `a_b`
// would share a workspace, which is two tasks in one checkout. Same discipline as
// trigger.spawnID, which disambiguates its own safeSeg the same way and for the same reason.
func safeSeg(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	seg := b.String()
	if seg == s && seg != "" {
		return seg
	}
	if seg == "" {
		seg = "task"
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%s-%08x", seg, h.Sum32())
}
