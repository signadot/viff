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
	Branch string // agent/<task id> — the same in every repository
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

	repo *git.Repository
	auth transport.AuthMethod
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

// Open builds the workspace for one task: every repository the environment names, checked out
// under one root.
//
// A repository that cannot be opened fails the whole workspace rather than yielding a partial
// one. An agent handed three of the four it was promised does not know that, and will read an
// absent directory as "this code does not exist" rather than "I was short-changed".
func (m *Mirrors) Open(ctx context.Context, id string, env Environment, worktrees string) (*Workspace, error) {
	branch := "agent/" + safeSeg(id)
	root := filepath.Join(worktrees, safeSeg(id))
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
	return &Tree{Repo: r, Dir: dir, Head: base.String(), repo: repo, auth: m.auth(r.Repo)}, nil
}

// Reopen rebuilds a Workspace over a checkout that already exists — the task process's view of
// what the runner prepared for it (see taskproc.go).
//
// Two absences are the design rather than an omission. There is no auth on these trees, because
// nothing on that side of the process boundary may reach a remote; the credential stays in the
// parent and delivery is the parent's act. And Head is empty, because the sha the work started
// from is what a DIFF is taken against, which is also the parent's job — this side only needs to
// know what the working tree currently says, which it reads from the working tree.
func Reopen(root string, env Environment, branch string) (*Workspace, error) {
	w := &Workspace{Env: env, Root: root, Branch: branch}
	for _, r := range env.Repos {
		dir := filepath.Join(root, r.Name)
		repo, err := git.PlainOpen(dir)
		if err != nil {
			return nil, fmt.Errorf("reopen %s in %s: %w", r.Name, root, err)
		}
		w.Trees = append(w.Trees, &Tree{Repo: r, Dir: dir, repo: repo})
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
		if t.Repo.Push == "" {
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

// PushTarget is where this repository's branch goes, with $task expanded. Empty means nowhere.
func (t *Tree) PushTarget(branch string) string {
	if t.Repo.Push == "" {
		return ""
	}
	return strings.ReplaceAll(t.Repo.Push, "$task", strings.TrimPrefix(branch, "agent/"))
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

// push sends this repository's task branch to the ref the environment named.
func (t *Tree) push(ctx context.Context, branch, target string) error {
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
		RemoteName: deliveryRemote, RefSpecs: []config.RefSpec{spec}, Force: true, Auth: t.auth,
	}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("repo %s: push %s: %w", t.Repo.Name, target, err)
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
	subject := firstLine(result)
	if subject == "" {
		subject = "work by " + l.ID
	}
	var b strings.Builder
	b.WriteString(subject)
	b.WriteString("\n\n")
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

// Deliverable reports whether any repository here was supposed to send work somewhere. False
// means a read-only workspace — nothing was ever going to leave it — which is the only case
// where removing it after delivering nothing is not destroying something.
func (w *Workspace) Deliverable() bool {
	for _, t := range w.Trees {
		if t.Repo.Push != "" {
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
