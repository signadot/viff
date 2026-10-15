package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
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
	"github.com/go-git/go-git/v5/plumbing/storer"
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
// verse makes about `run_bash` inheriting the server's environment. In particular the
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

// Workspace is one task's working directory: every repository the spawn names, cloned side by
// side under one root, each on the same task branch.
//
// The root is what the agent gets as its working directory, so `verse/task/loop.go` and
// `tony-format/ir/node.go` are both just paths to it, and a change that spans two repositories
// is one piece of work rather than two tasks that have to be coordinated.
type Workspace struct {
	Repos []Repo // the repositories checked out here, in the order they are named
	Root  string // the directory holding every checkout: the agent's working directory
	// Branch is the task's own branch, verse/auto/<task id>, the same in every repository. It is
	// where the work is made, and the branch a push sends when the task names none — a push to
	// anywhere else is the task's to name, within what each repository permits.
	Branch string
	Trees  []*Tree
	// Warnings is what Open could not do and went on without: a fetch that failed while the
	// mirror still had the repository, so a tree may start from a base older than upstream.
	Warnings []string

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
	// Delivered is the sha the last approved push staged from this tree, or empty. It is how
	// Salvage tells work that left from work that did not: a tree whose head is what was
	// delivered has nothing to hold.
	Delivered string

	// repo is the checkout. There is no credential beside it, on either side of the process
	// boundary: the runner pushes from its mirror with its own.
	repo *git.Repository
}

// Primary is the repository a singular question is about — the first one the spawn names.
// `task.RepoContext` has one Branch and one Base, and a workspace has as many as it has
// repositories, so the two do not fit: see RepoContext.
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
func (m *Mirrors) Dir(repo string) string {
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
func (m *Mirrors) Ensure(ctx context.Context, remote string) (string, error) {
	dir := m.Dir(remote)
	lock := m.lockFor(dir)
	lock.Lock()
	defer lock.Unlock()

	r, err := git.PlainOpen(dir)
	fresh := errors.Is(err, git.ErrRepositoryNotExists)
	if fresh {
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
		// word — which has happened once, to the task branches of every running task.
		if _, err = r.CreateRemote(&config.RemoteConfig{
			Name: "origin", URLs: []string{remote},
			Fetch: []config.RefSpec{"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"},
		}); err != nil {
			return "", fmt.Errorf("mirror %s: %w", remote, err)
		}
	} else if err != nil {
		return "", fmt.Errorf("mirror %s: %w", remote, err)
	}

	// A FIRST clone outlives the caller. The spawn that asks for it is bounded by versed's
	// control deadline (client.ControlTimeout, 30s), and a clone of anything real is longer than
	// that; a clone under the request's context would be cancelled with it, leaving no mirror
	// and the next spawn to start the same race from nothing. Made under the runner's own
	// context, the clone finishes whatever the caller did — and the caller is then told, below,
	// that it ran out of time rather than being handed a workspace nobody is waiting for.
	fetchCtx := ctx
	if fresh {
		fetchCtx = context.WithoutCancel(ctx)
	}
	err = r.FetchContext(fetchCtx, &git.FetchOptions{RemoteName: "origin", Prune: true, Auth: m.auth(remote)})
	if fresh && err == nil && ctx.Err() != nil {
		// The mirror is complete and the spawn is not: a workspace made now would start a task
		// against a spawn its caller already recorded as failed. Refused with what happened,
		// so the retry — which is the same spawn again, and now fast — is the obvious next act.
		// No dir with it: a dir beside an error reads as "fetch failed, use what the mirror
		// has" one level up, and this is the one refusal that must not be used past.
		return "", fmt.Errorf("the mirror of %s was made, and this spawn ran out of time waiting for it (%w) — spawn again, and the mirror is there", remote, ctx.Err())
	}
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

// AutoBranchPrefix namespaces every branch a task works on, and so every ref a delivery pushes
// from. `verse/auto/` rather than `agent/`, and the reason is a person rather than a machine: a
// developer who pulls and finds a commit they did not write asks what made it, and the first
// segments of a branch name are the cheapest place to answer — which system, and that nothing
// human proposed it. `agent/` said neither, and there is more than one thing in a tree that
// calls itself an agent.
const AutoBranchPrefix = "verse/auto/"

// Author is the git identity a workspace's commits carry: what `git commit` in the checkout
// writes as author and committer unless the command says otherwise.
type Author struct{ Name, Email string }

// RunnerAuthor is the identity when no person's can be derived: a charter firing, whose
// principal is an agent or nobody, and the runner's own commits.
var RunnerAuthor = Author{Name: "verse coderunner", Email: "coderunner@invalid"}

// AuthorFor derives the identity from the launch's principal and the driver that works for it.
//
// A person's launch commits as that person, at a tagged address: `user:u@h` becomes
// `u+verse-<driver>-runner@h`, with the principal and the driver as the name. The tag says in
// the address what the Verse-Principal trailer says in the message — that verse made this on
// their behalf — so a forge's view of the commit and the trailer agree, and the person's mail
// is where a reply to the commit goes. Any other principal is the runner's identity: an agent
// has no mailbox to tag, and the trailer already names it.
func AuthorFor(principal, driver string) Author {
	addr, ok := strings.CutPrefix(principal, "user:")
	user, host, at := strings.Cut(addr, "@")
	if !ok || !at || user == "" || host == "" {
		return RunnerAuthor
	}
	if driver == "" {
		driver = "code"
	}
	return Author{
		Name:  addr + " (verse " + driver + ")",
		Email: user + "+verse-" + driver + "-runner@" + host,
	}
}

// Open builds the workspace for one task: every repository named, checked out under one root.
//
// A repository that cannot be opened fails the whole workspace rather than yielding a partial
// one. An agent handed three of the four it was promised does not know that, and will read an
// absent directory as "this code does not exist" rather than "I was short-changed".
func (m *Mirrors) Open(ctx context.Context, id string, repos []Repo, worktrees string, author Author) (*Workspace, error) {
	branch := AutoBranchPrefix + SafeSeg(id)
	// ABSOLUTE, and here rather than at the flag that supplied it, because this path CROSSES A
	// PROCESS BOUNDARY into a different working directory. It travels to the task process in
	// its configuration, and that process is started with its cwd set to the workspace itself — so a
	// relative root resolves against the runner on this side and against the workspace on that
	// one, and the two are not the same place.
	//
	// A relative root would show as a child that exits immediately with `reopen verse in
	// <path>: repository does not exist`, naming a path that is correct from where you are
	// standing and wrong from where it ran. Tests pass a t.TempDir(), which is already absolute,
	// so this is the case `-data scratchpad/…` on a command line meets and they do not.
	root, err := filepath.Abs(filepath.Join(worktrees, SafeSeg(id)))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	w := &Workspace{Repos: repos, Root: root, Branch: branch, mirrors: m}
	for _, r := range repos {
		t, warning, err := m.open(ctx, r, branch, root, author)
		if err != nil {
			_ = w.Remove(ctx) // no partial workspaces
			return nil, err
		}
		if warning != "" {
			w.Warnings = append(w.Warnings, warning)
		}
		w.Trees = append(w.Trees, t)
	}
	return w, nil
}

// open checks one repository out. The warning is a fetch that failed while the mirror still had
// the repository: the tree is usable and its base may be older than upstream, which the caller
// says somewhere a reader of the task will see.
func (m *Mirrors) open(ctx context.Context, r Repo, branch, root string, author Author) (t *Tree, warning string, err error) {
	mirror, err := m.Ensure(ctx, r.Repo)
	if err != nil && mirror == "" {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name, err)
	}
	if err != nil {
		warning = fmt.Sprintf("repo %s: %v", r.Name, err)
	}
	// Resolve the base against the MIRROR, which handles a branch, a tag and a sha uniformly —
	// so `base: main`, `base: v1.2.0` and `base: 9fceb02` all mean what they look like. No base
	// is the repository's default branch, which is where the mirror's HEAD points (fixHead), and
	// the name is written back so the tree says where it started rather than that it was not told.
	mr, err := git.PlainOpen(mirror)
	if err != nil {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name, err)
	}
	if r.Base == "" {
		head, err := mr.Reference(plumbing.HEAD, false)
		if err != nil || head.Type() != plumbing.SymbolicReference {
			return nil, "", fmt.Errorf("repo %s: no branch named and the repository has no default branch", r.Name)
		}
		r.Base = head.Target().Short()
	}
	base, err := mr.ResolveRevision(plumbing.Revision(r.Base))
	if err != nil {
		return nil, "", fmt.Errorf("repo %s: base %q: %w", r.Name, r.Base, err)
	}

	dir := filepath.Join(root, r.Name)
	// The checkout's objects ARE the mirror's, linked rather than cloned, and the reason is
	// what a spawn costs against a repository of any size. A clone from the local mirror is no
	// network, but it is go-git re-packing every object into the workspace: 6s for a 217MB
	// monorepo on a laptop, and past versed's 30s control deadline on the node that runs this,
	// so the spawn was cancelled before the task began. A hardlink per object file is the
	// clone git itself makes of a local repository, costs a directory walk, and can never go
	// stale: a pack is immutable, and a fetch that brings more objects writes a new one beside
	// it rather than into it. (Not `objects/info/alternates`, which go-git honours by
	// re-opening the shared store on every object it does not have: 41s to check out the same
	// tree, and 12s to commit one file.)
	//
	// The refs a clone would have made are copied by hand, because they are the other half of
	// what a clone is: every head of the mirror as `refs/remotes/mirror/<name>`, which is what
	// a task's shell reads a sibling's delivery through.
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		return nil, "", fmt.Errorf("repo %s: init: %w", r.Name, err)
	}
	if err := linkObjects(mirror, dir); err != nil {
		return nil, "", fmt.Errorf("repo %s: sharing the mirror's objects: %w", r.Name, err)
	}
	// Reopened so the storage sees the objects it now has.
	if repo, err = git.PlainOpen(dir); err != nil {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name, err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "mirror", URLs: []string{mirror},
		Fetch: []config.RefSpec{"+refs/heads/*:refs/remotes/mirror/*"}}); err != nil {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name, err)
	}
	if err := mirrorRefs(mr, repo); err != nil {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name, err)
	}
	// The checkout knows who commits in it, so an agent's `git commit` is attributed without the
	// agent being asked and without it choosing: a clone with no identity makes git refuse the
	// first commit, and an agent that meets the refusal invents a name.
	cfg, err := repo.Config()
	if err != nil {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name, err)
	}
	cfg.User.Name, cfg.User.Email = author.Name, author.Email
	if err := repo.SetConfig(cfg); err != nil {
		return nil, "", fmt.Errorf("repo %s: author: %w", r.Name, err)
	}
	// No `origin`: the checkout has one remote, the mirror, and the runner is the only thing
	// that pushes upstream (delivery). A remote naming the real repository would be a route out
	// of the workspace that passes no gate — for a repository on this filesystem, a working one.
	wt, err := repo.Worktree()
	if err != nil {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name, err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{
		Hash: *base, Branch: plumbing.NewBranchReferenceName(branch), Create: true,
	}); err != nil {
		return nil, "", fmt.Errorf("repo %s at %s: %w", r.Name, r.Base, err)
	}
	return &Tree{Repo: r, Dir: dir, Head: base.String(), repo: repo}, warning, nil
}

// linkObjects hardlinks every file under the mirror's object store into the checkout's, and
// copies the one it cannot link — a workspace root on a different filesystem from the mirrors,
// which no deployment here has but a test's temp dirs might.
func linkObjects(mirror, dir string) error {
	src, dst := filepath.Join(mirror, "objects"), filepath.Join(dir, ".git", "objects")
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.Link(p, target); err == nil || errors.Is(err, fs.ErrExist) {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// mirrorRefs writes the mirror's heads into the checkout as `refs/remotes/mirror/<name>`.
func mirrorRefs(mirror, repo *git.Repository) error {
	_, err := syncMirrorRefs(mirror, repo)
	return err
}

// syncMirrorRefs sets `refs/remotes/mirror/<branch>` in the checkout to each branch of the mirror
// and reports the ones that moved, as `mirror/<branch> <was>..<now>` (`<was>` is `new` for a
// branch the checkout had not seen). Branches the mirror no longer has are left: a ref that
// vanishes under a running agent is a surprise, and a stale remote-tracking ref is not.
func syncMirrorRefs(mirror, repo *git.Repository) ([]string, error) {
	refs, err := mirror.References()
	if err != nil {
		return nil, err
	}
	var moved []string
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if !ref.Name().IsBranch() {
			return nil
		}
		short := strings.TrimPrefix(ref.Name().String(), "refs/heads/")
		name := plumbing.NewRemoteReferenceName("mirror", short)
		was := "new"
		if old, err := repo.Storer.Reference(name); err == nil {
			if old.Hash() == ref.Hash() {
				return nil
			}
			was = ShortSha(old.Hash().String())
		}
		if err := repo.Storer.SetReference(plumbing.NewHashReference(name, ref.Hash())); err != nil {
			return err
		}
		moved = append(moved, "mirror/"+short+" "+was+".."+ShortSha(ref.Hash().String()))
		return nil
	})
	sort.Strings(moved)
	return moved, err
}

// Refresh brings one checkout's view of upstream up to the mirror, which the caller has just
// fetched: the mirror's new objects are linked in as they were at open, and `refs/remotes/mirror/*`
// move to where the mirror's branches are. The task's own branch and working tree are untouched;
// what the agent does with a newer `mirror/main` is its own git. It returns what moved.
//
// This is the parent writing into a checkout a task is using, and it is safe for the same reason
// a fetch into a repository someone is working in is: objects are only ever added, and the refs
// written are the remote-tracking ones nothing in the checkout holds a lock on.
func (t *Tree) Refresh(mirror string) ([]string, error) {
	if err := linkObjects(mirror, t.Dir); err != nil {
		return nil, fmt.Errorf("repo %s: sharing the mirror's objects: %w", t.Repo.Name, err)
	}
	mr, err := git.PlainOpen(mirror)
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", t.Repo.Name, err)
	}
	moved, err := syncMirrorRefs(mr, t.repo)
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", t.Repo.Name, err)
	}
	return moved, nil
}

// Tree is the checkout of the repository named, or nil.
func (w *Workspace) Tree(name string) *Tree {
	for _, t := range w.Trees {
		if t.Repo.Name == name {
			return t
		}
	}
	return nil
}

// Reopen rebuilds a Workspace over a checkout that already exists — the task process's view of
// what the runner prepared for it.
//
// There is no auth on these trees, and no mirrors: the task process asks for a push over the
// delivery channel, and the runner makes it from its mirror with its own credential.
//
// Head is HANDED OVER rather than read here: it is the commit the work started from, which is
// what a delivery is measured against (AuthoredWork), and reading it from the checkout would
// agree today and stop agreeing the moment a task committed.
func Reopen(root string, repos []Repo, branch string, bases map[string]string) (*Workspace, error) {
	w := &Workspace{Repos: repos, Root: root, Branch: branch}
	for _, r := range repos {
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
// The layout is a CONVENTION of the runner — one directory per repository, named by the
// repository, under a root nobody outside chose — so a prompt written against it would be
// guessing, and a prompt written against a single-repo workspace ("look in docs/") is simply
// wrong the moment a second repository appears. An agent dropped into a directory it did not
// create, with no map, spends its first turns discovering one. What each repository permits is
// said beside it, since an agent that edits a read-only tree has done work that will be thrown
// away, and nothing downstream would report that it was.
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
		fmt.Fprintf(&b, "  %s/ — %s at %s (%s)\n", t.Repo.Name, t.Repo.Repo, t.Repo.Base, t.Repo.Allowance())
	}
	fmt.Fprintf(&b, "Every path is relative to that directory, so a file in the first repository "+
		"above is `%s/…`. Each checkout is on your branch, `%s`.\n\n", w.Trees[0].Repo.Name, w.Branch)
	return b.String()
}

// RepoContext is what a gate can match on: the branch this work is on, the base it came from,
// and what it has touched. `GateMatch.Branches` reads exactly this, so a charter can say
// `vector: ask` on anything touching a release branch.
//
// It is recomputed per call rather than cached because ChangedFiles is the whole point and it
// changes under the task's own hands.
//
// The awkward part, named rather than hidden: `task.RepoContext` is SINGULAR and a workspace is
// not. Root and ChangedFiles generalize cleanly — the root is the working directory and paths
// are relative to it, so `verse/task/loop.go` says which repository it is in. Branch does too,
// since every repository carries the same task branch. Base does not, so it reports the PRIMARY
// repository's, and a gate matching on it is asking about that one. A workspace whose
// repositories have different bases is exactly the case that wants a plural RepoContext, and
// this is where it would go. Where work goes is not here at all: a push names its branch, and
// the effect it derives is what a gate matches on.
func (w *Workspace) RepoContext() *task.RepoContext {
	rc := &task.RepoContext{Root: w.Root, Branch: w.Branch}
	if p := w.Primary(); p != nil {
		rc.Base = p.Repo.Base
	}
	for _, t := range w.Trees {
		for _, f := range t.Changed() {
			// Repo-relative becomes workspace-relative, which is what the agent sees and what a
			// `paths:` predicate in a charter should be written against.
			rc.ChangedFiles = append(rc.ChangedFiles, filepath.Join(t.Repo.Name, f))
		}
	}
	return rc
}

// changed is this tree's dirty paths, relative to itself, sorted so two readers see one order
// (go-git returns a map).
func (t *Tree) Changed() []string {
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

// shortSha is the branch-name form of a commit: enough to be unique in a repository this size
// and short enough that a ref stays readable next to a task id that is already long.
func ShortSha(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// commitWork commits whatever the task produced and reports whether there is anything to deliver.
//
// It is the first half of a delivery, apart from the push, which is what lets the work be
// STAGED before it is sent (stage.go): the commit is local and always safe, the push is neither.
//
// The trailers are the attribution channel. A push made under a shared bot credential says nothing
// at the far end about who it was for, so the only externally checkable link back into the verse is
// what this writes down — and it is checkable, by anyone who can read both the repository and the
// verse.
func (t *Tree) CommitWork(l task.Launch, result string) (bool, error) {
	// What counts as work is "the branch moved OR the tree is dirty", not "the tree is dirty".
	//
	// The difference is not pedantry: an agent that commits its own edits — which a coding
	// agent will do, told not to or otherwise — leaves a CLEAN tree with its work sitting in
	// commits. A test for dirt alone reads that as "nothing happened", delivers nothing, and
	// lets the workspace go with the only copy of the result in it.
	dirty := len(t.Changed()) > 0
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
		// trailers are the runner's only attribution channel out of the verse, and a commit
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
	// The runner made this commit itself, for a program with no agent to commit; it says so,
	// and who it was for is a trailer.
	sig := &object.Signature{Name: RunnerAuthor.Name, Email: RunnerAuthor.Email, When: time.Now()}
	if _, err := wt.Commit(CommitMessage(l, result), &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		return false, fmt.Errorf("repo %s: commit: %w", t.Repo.Name, err)
	}
	return true, nil
}

// Refusal reads a failed push and reports what the FAR SIDE said when it declined, or "" when
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
func Refusal(err error) string {
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

// deliveryRemote is the in-memory remote a push is made through. It is not a remote in any
// `.git/config` and must not become one: the name exists only so go-git's own check that the
// options and the remote agree has something to compare.
const deliveryRemote = "verse-delivery"

// commitMessage carries the provenance the runner was given. Every trailer is conditional: an
// absent one means verse did not know, and writing it out empty would claim to have asked.
func CommitMessage(l task.Launch, result string) string {
	subject, body := firstLine(result), ""
	if isDocument(result) {
		// A CONTRACT-shaped answer is not a sentence, and a subject line is. A task with an
		// `answer:` contract returns a document — the whole point of the field — so its first
		// line is `{"files": 1, "fixed": 1, "findings": [{"symbol": …` truncated at 72
		// characters, which is what `git log --oneline` then shows forever. One is on main.
		//
		// So the document becomes the BODY, where it is worth reading, and the subject is
		// derived from what verse knows about the run. The alternative — digging a `report` or
		// `summary` field out of the answer — would make the runner know a charter's answer
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
	return b.String()
}

// isDocument reports whether a result is a structured answer rather than prose — the shape an
// `answer:` contract asks for. JSON and tony both start a document the same way, and a sentence
// does not start with either.
//
// A FENCE COUNTS. A model asked for a document commonly returns it fenced, and the fence line is
// not the document: a subject line taken from it would be ```` ```json ````, on every commit an
// agent delivers, in the one place a reader looks to see what an agent changed.
//
// Deliberately shallow: it decides where the text GOES and never what it means, so being wrong
// costs a commit whose subject is derived rather than quoted, which is the safer of the two
// mistakes. Parsing it to be sure would make the runner a reader of somebody's answer shape.
func isDocument(result string) bool {
	s := strings.TrimSpace(result)
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") || strings.HasPrefix(s, "```")
}

// describe is the subject line for work that did not come with a sentence: what fired, about
// what. `comment-drift on drift.head.head-moved@git.head.verse` says more to somebody reading
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

// Deliverable reports whether any repository here may send work somewhere. False means a
// read-only workspace — nothing was ever going to leave it — which is the only case where
// removing it after delivering nothing is not destroying something.
func (w *Workspace) Deliverable() bool {
	for _, t := range w.Trees {
		if t.Repo.Writes() {
			return true
		}
	}
	return false
}

// Remove drops the whole workspace. The runner calls it when a task ends, unless it was told to
// keep workspaces, because the alternative is a disk that fills at the rate work arrives — an
// argument about SUCCESSFUL runs; Delivery.Loose is the case where it does not apply.
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
// chosen by whoever spawns — including a client posting straight at the runner — so it is
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
func SafeSeg(s string) string {
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

// requiredTrailers is the provenance every commit an agent delivers must carry: the same lines
// commitWork writes when the runner makes the commit. They are the only externally checkable
// link from a commit on the forge back into the verse, so a delivery without them is refused
// rather than quietly repaired — repairing would rewrite the agent's commits, and the agent
// would no longer know the SHAs it delivered.
func RequiredTrailers(l task.Launch) []string {
	lines := []string{"Verse-Task: " + l.ID}
	if p := l.Provenance.Principal; p != "" {
		lines = append(lines, "Verse-Principal: "+p)
	}
	return lines
}

// authoredWorkMax bounds how far back a delivery is read: a branch whose base is no longer in
// its history (the agent rebased onto something else) is refused rather than walked to the root.
const authoredWorkMax = 200

// authoredWork is the delivery an AGENT decides: the commits it made on this branch beyond the
// base, each carrying the required trailers. ok reports whether there is anything to deliver at
// all; refusal is why the push must not go, in words the agent can act on, or "" when it may.
// Uncommitted changes are not looked at: what is not committed is not delivered.
func (t *Tree) AuthoredWork(l task.Launch) (ok bool, refusal string, err error) {
	head, err := t.repo.Head()
	if err != nil {
		return false, "", fmt.Errorf("repo %s: %w", t.Repo.Name, err)
	}
	if head.Hash().String() == t.Head {
		return false, "", nil
	}
	iter, err := t.repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		return false, "", fmt.Errorf("repo %s: %w", t.Repo.Name, err)
	}
	defer iter.Close()
	var bad []string
	n, reachedBase := 0, false
	err = iter.ForEach(func(c *object.Commit) error {
		if c.Hash.String() == t.Head {
			reachedBase = true
			return storer.ErrStop
		}
		n++
		if n > authoredWorkMax {
			return storer.ErrStop
		}
		if why := MissingTrailers(c.Message, l); why != "" {
			bad = append(bad, ShortSha(c.Hash.String())+" ("+why+")")
		}
		return nil
	})
	if err != nil {
		return false, "", fmt.Errorf("repo %s: %w", t.Repo.Name, err)
	}
	if !reachedBase {
		return true, fmt.Sprintf("not pushed: the branch no longer descends from the base this task "+
			"started at (%s); deliver work on top of it", ShortSha(t.Head)), nil
	}
	if len(bad) > 0 {
		return true, fmt.Sprintf("not pushed: every commit delivered must end with these trailers, as "+
			"the last paragraph of its message:\n  %s\nThese commits do not: %s. Amend or re-commit "+
			"with them, then push again.", strings.Join(RequiredTrailers(l), "\n  "), strings.Join(bad, "; ")), nil
	}
	return true, "", nil
}

// missingTrailers says which required trailer a commit message lacks, reading them the way git
// does: the last paragraph, every line a `Key: value`. "" when all are there.
func MissingTrailers(msg string, l task.Launch) string {
	paras := strings.Split(strings.TrimRight(msg, "\n"), "\n\n")
	last := paras[len(paras)-1]
	have := map[string]string{}
	for _, line := range strings.Split(last, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		have[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	var missing []string
	for _, want := range RequiredTrailers(l) {
		k, v, _ := strings.Cut(want, ":")
		if have[k] != strings.TrimSpace(v) {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "missing or wrong: " + strings.Join(missing, ", ")
}
