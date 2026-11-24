package workspace

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/runner"
)

// Git is go-git, not the git binary, and the difference is worth stating because it decides the
// shape of everything below.
//
// A runner that shells out inherits an ambient world: whichever `git` is on PATH, whatever
// credential helper the machine has configured, whatever `~/.gitconfig` says. All three are
// invisible in the code and none of them is declared anywhere — which is the same complaint
// viff makes about `run_bash` inheriting the server's environment variables. In particular the
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
// branches live in the mirror's own ref namespace, so a `fetch --prune` on the next launch
// deletes the branch of every running task — a silent failure that leaves a worktree's HEAD
// pointing at nothing. With a clone per task, task branches live in the task's own repository
// and the mirror holds upstream refs only, so pruning cannot reach them.

// Workspace is one task's working directory: every repository the launch names, cloned side by
// side under one root, each on the same task branch.
//
// The root is what the agent gets as its working directory, so `viff/task/loop.go` and
// `tony-format/ir/node.go` are both just paths to it, and a change that spans two repositories
// is one piece of work rather than two tasks that have to be coordinated.
type Workspace struct {
	Repos []Repo // the repositories checked out here, in the order they are named
	Root  string // the directory holding every checkout: the agent's working directory
	// Branch is the task's own branch, viff/auto/<task id>, the same in every repository. It is
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
	// upstream is the mirror's branch tips as the runner last wrote them into the checkout, at
	// open and at each refresh: what a delivery's commits are measured beyond (AuthoredWork).
	// Kept here rather than read back from the checkout's mirror/* refs, which the task can
	// point anywhere.
	upstream []plumbing.Hash

	// repo is the checkout. There is no credential beside it, on either side of the process
	// boundary: the runner pushes from its mirror with its own.
	repo *git.Repository
}

// Mirrors keeps one bare mirror per repository, fetched before a task clones off it — and, since
// staging, the place a task's work waits on its way out (stage.go).
//
// It is per REPOSITORY rather than per task so two tasks on one repo at different
// bases share it — and the lock is per repository too, because two concurrent fetches into one
// directory are not safe.
type Mirrors struct {
	root  string
	auth  func(remote string) transport.AuthMethod
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// ForgeTimeout is how long one request's calls to a forge may take: a fetch, a push, a ref
// lookup, a proposal. They are made holding the mirror's lock, and a forge that stops answering
// would hold every task on that repository with it. Ensure bounds a refetch by it, whoever calls;
// a first clone isn't bounded, and outlives the caller.
const ForgeTimeout = 10 * time.Minute

// NewMirrors is the mirrors under root, which one runner process has to itself.
//
// So a `packed-refs.lock` found in one as the runner starts is nobody's: git holds it while it
// writes `packed-refs`, the runner is the only thing that has git do that (tidy), and no runner
// was running. It is what a collection leaves when it is killed with the runner, and git never
// expires it, so it is removed here, or it refuses every collection in that mirror from then on.
func NewMirrors(root string, auth func(remote string) transport.AuthMethod) *Mirrors {
	stale, _ := filepath.Glob(filepath.Join(root, "*", "packed-refs.lock"))
	for _, lock := range stale {
		_ = os.Remove(lock)
	}
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

// Ensure creates the mirror if it is not there and fetches it either way, with the mirrors' own
// credential for the remote. The fetch happens on every launch, which is the honest place for
// it: a task that starts from a base an hour stale is a task whose work is against a tree nobody
// has.
func (m *Mirrors) Ensure(ctx context.Context, remote string) (string, error) {
	auth := m.auth(remote)
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
		if _, err = r.CreateRemote(&config.RemoteConfig{
			Name: "origin", URLs: []string{remote}, Fetch: mirrorSpecs,
		}); err != nil {
			return "", fmt.Errorf("mirror %s: %w", remote, err)
		}
	} else if err != nil {
		return "", fmt.Errorf("mirror %s: %w", remote, err)
	}

	// Before anything removes a ref: the mirror packs nothing on its own, and what it holds
	// packed carries no peel lines (tidy).
	if err := noAutoGC(r); err != nil {
		return "", fmt.Errorf("mirror %s: %w", remote, err)
	}
	if err := stripPeels(dir); err != nil {
		return "", fmt.Errorf("mirror %s: %w", remote, err)
	}

	// A FIRST clone outlives the caller. A clone of anything real takes minutes, and a task can
	// be cancelled or time out while it waits; a clone under the task's context would be
	// cancelled with it, leaving no mirror and the next launch to start the same clone from
	// nothing. Made under the runner's own context, the clone finishes whatever the caller did —
	// and the caller is then told, below, that it gave up rather than being handed a workspace
	// nobody is waiting for.
	// A refetch is bounded, as every call to the forge made holding the lock is (ForgeTimeout).
	fetchCtx, cancel := context.WithTimeout(ctx, ForgeTimeout)
	defer cancel()
	if fresh {
		fetchCtx = context.WithoutCancel(ctx)
	}
	err = r.FetchContext(fetchCtx, &git.FetchOptions{RemoteName: "origin", RefSpecs: mirrorSpecs,
		Force: true, Prune: true, Auth: auth})
	// Whatever the fetch said, short of leaving no mirror: a mirror used past a failed fetch is as
	// much in need of it. A mirror it could not tidy is still the mirror, fetched, so this is
	// reported beside a fetch that had nothing worse to say, as what it is (Untidy).
	var tidyErr error
	if !fresh || err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		if terr := tidy(ctx, dir); terr != nil {
			tidyErr = &Untidy{Err: fmt.Errorf("mirror %s: %w", remote, terr)}
		}
	}
	if fresh && err == nil && ctx.Err() != nil {
		// The mirror is complete and the task is gone: a workspace made now would start a task
		// nobody is waiting for. Refused with what happened, so the retry — the same launch
		// again, and now fast — is the obvious next act.
		// No dir with it: a dir beside an error reads as "fetch failed, use what the mirror
		// has" one level up, and this is the one refusal that must not be used past.
		return "", fmt.Errorf("the mirror of %s was made, and this task ended while waiting for it (%w) — launch again, and the mirror is there", remote, ctx.Err())
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
		return dir, tidyErr
	case fresh:
		// A first fetch that failed leaves a mirror with nothing in it, and nothing to use past:
		// the repo isn't there, or can't be reached. Removed, so the next launch tries afresh.
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("fetch %s: %w", remote, err)
	case refused(err):
		// The forge said no to the credential. A mirror is shared, and what it holds is not
		// served to a task the forge turned away.
		return "", fmt.Errorf("fetch %s: refused with the runner's credential: %w", remote, err)
	default:
		// Not fatal on its own: the mirror still holds whatever it had, and a task off a
		// slightly stale base beats no task at all when a remote is briefly unreachable. It is
		// reported rather than swallowed, because "the base is older than you think" is exactly
		// the surprise worth naming.
		return dir, fmt.Errorf("fetch %s (using what the mirror already had): %w", remote, err)
	}
}

// mirrorSpecs is what a mirror holds of its remote: every branch and every tag, under their own
// names. `refs/heads/*` here mirrors upstream exactly, since task branches live in each task's
// own clone, so the refspec is unambiguous.
//
// It is also what makes the STAGED namespaces safe. A pruning fetch removes only refs matching the
// reverse of a refspec it was given, so `refs/pending/*` and `refs/held/*` (stage.go) are outside
// everything this prunes. Put work under `refs/heads/` instead and the next launch deletes it
// without a word — which has happened once, to the task branches of every running task.
//
// No `+`, and the fetch forces instead (FetchOptions.Force). go-git reverses a refspec by
// swapping the two sides as written, so a forced one reverses to a destination of
// `+refs/heads/<name>`, which no remote has: prune then removes every ref the refspec covers, and
// the fetch writes them all back.
var mirrorSpecs = []config.RefSpec{"refs/heads/*:refs/heads/*", "refs/tags/*:refs/tags/*"}

// Two programs write a mirror's refs: go-git, in this process, and the git binary, which go-git's
// local transport runs as `git-receive-pack` when a checkout's work is staged (stage.go). They do
// not share a lock on `packed-refs` — git takes `packed-refs.lock`, go-git locks the file itself —
// and go-git removes a ref from it by dropping the ref's line and keeping the `^` line under it,
// the peeled value of an annotated tag. So `packed-refs` is written by git only while the
// mirror's own lock is held, and holds no peel lines when go-git next removes a ref:
//
//   - noAutoGC: git packs refs when it collects garbage, and a receive-pack starts that on its
//     own, detached, to finish after the push that held the lock has returned. A mirror is
//     configured never to.
//   - tidy: the runner collects the garbage instead, in the foreground, under the lock, and then
//     takes the peel lines out of what that packed.
//
// A peel line left without its ref is read by git as the peeled value of whichever ref is now
// above it, or, with none above it, as a file it refuses: `unexpected line in ./packed-refs`, from
// every command in the mirror.

// noAutoGC configures a mirror so no git command in it collects garbage on its own.
func noAutoGC(r *git.Repository) error {
	cfg, err := r.Config()
	if err != nil {
		return err
	}
	want := [][3]string{{"receive", "autogc", "false"}, {"gc", "auto", "0"}}
	set := false
	for _, w := range want {
		if sec := cfg.Raw.Section(w[0]); sec.Option(w[1]) != w[2] {
			sec.SetOption(w[1], w[2])
			set = true
		}
	}
	if !set {
		return nil
	}
	return r.SetConfig(cfg)
}

// Untidy is the error Ensure answers beside a mirror that is fetched and could not be tidied.
// The mirror is as current as a fetch makes it, so a caller that would refuse a mirror whose
// fetch failed has no reason to refuse this one: it says so somewhere a reader will see, and
// goes on.
type Untidy struct{ Err error }

func (u *Untidy) Error() string { return u.Err.Error() }
func (u *Untidy) Unwrap() error { return u.Err }

// TidyTimeout is how long a mirror's garbage collection may take. Its own, and not the forge's
// (ForgeTimeout): a collection reaches no forge, and one that starts after a slow fetch is owed
// more than what the fetch left.
const TidyTimeout = 10 * time.Minute

// tidy collects a mirror's garbage when git judges there is enough of it, as a receive-pack
// would have, and leaves `packed-refs` without peel lines. The caller holds the mirror's lock.
//
// This is the one place the runner runs the git binary itself. It is given no credential and
// reaches no remote, and of the runner's environment it is given PATH alone, so it reads the
// mirror's configuration and no other: not the machine's, not the account's, and none carried in
// a variable.
//
// A collection outlives the caller, as a first clone does: a task that ends while git is
// repacking has no claim on the mirror every other task uses. Where it runs past its own time it
// is asked to stop and given a moment to, since git removes its lock files on a signal it can
// catch and leaves them, for good, on one it cannot. A `packed-refs.lock` left behind refuses
// every collection after it.
func tidy(ctx context.Context, dir string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TidyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir,
		"-c", "gc.auto=6700", "-c", "gc.autoDetach=false", "gc", "--auto", "--quiet")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	out, gcErr := cmd.CombinedOutput()
	if gcErr != nil {
		gcErr = fmt.Errorf("git gc: %w: %s", gcErr, strings.TrimSpace(string(out)))
	}
	// Whether or not it ran to the end: it may have packed the refs before it stopped.
	return errors.Join(gcErr, stripPeels(dir))
}

// stripPeels rewrites a bare repository's `packed-refs` without its peel lines, and without the
// header that claims them: no traits, so git peels an annotated tag as it needs, as in a checkout
// (packTags). A file with none is left as it is.
func stripPeels(dir string) error {
	path := filepath.Join(dir, "packed-refs")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var keep []string
	peeled := false
	for line := range strings.Lines(string(b)) {
		switch {
		case strings.HasPrefix(line, "^"):
			peeled = true
		case strings.HasPrefix(line, "#"):
		default:
			keep = append(keep, line)
		}
	}
	if !peeled {
		return nil
	}
	tmp := path + ".tidy"
	if err := os.WriteFile(tmp, []byte(strings.Join(keep, "")), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// refused reports whether a fetch failed because the forge refused its credentials, as against
// failing to reach the forge at all. A forge that hides a private repo from a caller says it
// isn't there.
func refused(err error) bool {
	return errors.Is(err, transport.ErrAuthenticationRequired) ||
		errors.Is(err, transport.ErrAuthorizationFailed) ||
		errors.Is(err, transport.ErrRepositoryNotFound)
}

// fixHead points a mirror's HEAD at a branch that exists, preferring the conventional defaults
// and otherwise taking the first in name order so two runs agree. A mirror with no branches at
// all is left alone: there is nothing to point at, and the clone's error is then about the base
// the launch named, which is the more useful complaint.
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
// from. `viff/auto/` rather than `agent/`, and the reason is a person rather than a machine: a
// developer who pulls and finds a commit they did not write asks what made it, and the first
// segments of a branch name are the cheapest place to answer — which system, and that nothing
// human proposed it. `agent/` said neither, and there is more than one thing in a tree that
// calls itself an agent.
const AutoBranchPrefix = "viff/auto/"

// Author is the git identity a workspace's commits carry: what `git commit` in the checkout
// writes as author and committer unless the command says otherwise.
type Author struct{ Name, Email string }

// RunnerAuthor is the identity when no person's can be derived: a charter firing, whose
// principal is an agent or nobody, a rule acting alone or for a pair whose asker is not a person,
// and the runner's own commits.
var RunnerAuthor = Author{Name: "viff runner", Email: "runner@invalid"}

// AuthorFor derives the identity from the launch's principal and the doer that works for it.
//
// A person's launch commits as that person, at a tagged address: `user:u@h` becomes
// `u+viff-<doer>-runner@h`, with the principal and the doer as the name. The tag says in
// the address what the Viff-Principal trailer says in the message — that viff made this on
// their behalf — so a forge's view of the commit and the trailer agree, and the person's mail
// is where a reply to the commit goes. Any other principal is the runner's identity: an agent
// has no mailbox to tag, and the trailer already names it.
//
// A pair (`pair:<asker> via:<rule>`, entity.Pair: a rule's task acting for whoever wrote the
// record) commits as its asker, read once with entity.PairOf, as mirror.ActsFor reads it: the
// person on whose authority the work was done is on the commit, as their own launch would put
// them, and the rule is said by the Viff-Principal trailer, which carries the whole pair. A pair
// whose asker is no person is the runner's.
func AuthorFor(principal, doer string) Author {
	if asker, _, ok := entity.PairOf(principal); ok {
		principal = asker
	}
	addr, ok := strings.CutPrefix(principal, "user:")
	user, host, at := strings.Cut(addr, "@")
	if !ok || !at || user == "" || host == "" {
		return RunnerAuthor
	}
	if doer == "" {
		doer = "code"
	}
	return Author{
		Name:  addr + " (viff " + doer + ")",
		Email: user + "+viff-" + doer + "-runner@" + host,
	}
}

// IdentityFor is the identity a launch's work carries: what its checkout is given to commit as,
// and what a delivery of that work is held to (AuthoredWork). One derivation for both, so the
// two cannot disagree; the check reads it from the launch, never back from the checkout's
// config, which the task can rewrite.
func IdentityFor(l *runner.Launch) Author {
	return AuthorFor(l.Provenance.Principal, cmp.Or(l.Who(), "agent"))
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
	// A relative root would show as a child that exits immediately with `reopen viff in
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
	mirror, err := m.Ensure(ctx, r.Remote)
	if err != nil && mirror == "" {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name(), err)
	}
	if err != nil {
		warning = fmt.Sprintf("repo %s: %v", r.Name(), err)
	}
	// Resolve the base against the MIRROR, which handles a branch, a tag and a sha uniformly —
	// so `base: main`, `base: v1.2.0` and `base: 9fceb02` all mean what they look like. No base
	// is the repository's default branch, which is where the mirror's HEAD points (fixHead), and
	// the name is written back so the tree says where it started rather than that it was not told.
	mr, err := git.PlainOpen(mirror)
	if err != nil {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name(), err)
	}
	if r.From == "" {
		head, err := mr.Reference(plumbing.HEAD, false)
		if err != nil || head.Type() != plumbing.SymbolicReference {
			return nil, "", fmt.Errorf("repo %s: no branch named and the repository has no default branch", r.Name())
		}
		r.From = head.Target().Short()
	}
	base, err := mr.ResolveRevision(plumbing.Revision(r.From))
	if err != nil {
		return nil, "", fmt.Errorf("repo %s: base %q: %w", r.Name(), r.From, err)
	}

	dir := filepath.Join(root, r.Name())
	// The checkout's objects ARE the mirror's, linked rather than cloned, and the reason is
	// what a launch costs against a repository of any size. A clone from the local mirror is no
	// network, but it is go-git re-packing every object into the workspace: 6s for a 217MB
	// monorepo on a laptop, and more than 30s on a staging node, before
	// the task could begin. A hardlink per object file is the
	// clone git itself makes of a local repository, costs a directory walk, and can never go
	// stale: a pack is immutable, and a fetch that brings more objects writes a new one beside
	// it rather than into it. (Not `objects/info/alternates`, which go-git honours by
	// re-opening the shared store on every object it does not have: 41s to check out the same
	// tree, and 12s to commit one file.)
	//
	// The refs a clone would have made are copied by hand, because they are the other half of
	// what a clone is: every head of the mirror as `refs/remotes/mirror/<name>`, which is what
	// a task's shell reads a sibling's delivery through, and every tag of the mirror as
	// `refs/tags/<name>`, so `git describe --tags` and `git log <tag>..` answer in a checkout as
	// they do in a clone. The objects behind a tag are linked in with the rest; a tag ref nobody
	// copied would leave every annotated tag object in the store with nothing naming it.
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		return nil, "", fmt.Errorf("repo %s: init: %w", r.Name(), err)
	}
	if err := linkObjects(mirror, dir); err != nil {
		return nil, "", fmt.Errorf("repo %s: sharing the mirror's objects: %w", r.Name(), err)
	}
	if err := packTags(mr, dir); err != nil {
		return nil, "", fmt.Errorf("repo %s: the mirror's tags: %w", r.Name(), err)
	}
	// Reopened so the storage sees the objects and the tags it now has.
	if repo, err = git.PlainOpen(dir); err != nil {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name(), err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "mirror", URLs: []string{mirror},
		Fetch: []config.RefSpec{"+refs/heads/*:refs/remotes/mirror/*", "+refs/tags/*:refs/tags/*"}}); err != nil {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name(), err)
	}
	_, upstream, err := syncMirrorRefs(mr, repo)
	if err != nil {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name(), err)
	}
	// The checkout knows who commits in it, so an agent's `git commit` is attributed without the
	// agent being asked and without it choosing: a clone with no identity makes git refuse the
	// first commit, and an agent that meets the refusal invents a name.
	cfg, err := repo.Config()
	if err != nil {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name(), err)
	}
	cfg.User.Name, cfg.User.Email = author.Name, author.Email
	// Where the checkout started, said in the checkout: the task's process reopens it knowing
	// the launch, which names no base when it is the default branch (Reopen).
	cfg.Raw.Section(baseSection).SetOption(baseOption, r.From)
	if err := repo.SetConfig(cfg); err != nil {
		return nil, "", fmt.Errorf("repo %s: author: %w", r.Name(), err)
	}
	// No `origin`: the checkout has one remote, the mirror, and the runner is the only thing
	// that pushes upstream (delivery). A remote naming the real repository would be a route out
	// of the workspace that passes no gate — for a repository on this filesystem, a working one.
	wt, err := repo.Worktree()
	if err != nil {
		return nil, "", fmt.Errorf("repo %s: %w", r.Name(), err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{
		Hash: *base, Branch: plumbing.NewBranchReferenceName(branch), Create: true,
	}); err != nil {
		return nil, "", fmt.Errorf("repo %s at %s: %w", r.Name(), r.From, err)
	}
	return &Tree{Repo: r, Dir: dir, Head: base.String(), repo: repo, upstream: upstream}, warning, nil
}

// linkObjects hardlinks every file under the mirror's object store into the checkout's, and
// copies the one it cannot link — a workspace root on a different filesystem from the mirrors,
// which no deployment here has but a test's temp dirs might.
//
// It reads the store without the mirror's lock, so a collection may run under it (tidy), and a
// collection replaces files: the loose objects and packs it read go, and one pack holding the
// same objects arrives. A file gone between being listed and being linked is therefore not a
// failure, and not the end either, since what replaced it may not have been listed. The store is
// walked again, linking what is new, until a walk finds everything it listed.
func linkObjects(mirror, dir string) error {
	for range 4 {
		gone, err := linkObjectsOnce(mirror, dir)
		if err != nil || !gone {
			return err
		}
	}
	return fmt.Errorf("the object store of %s kept changing while it was read", mirror)
}

// linkObjectsOnce is one walk of linkObjects, and reports whether a file it listed was gone.
func linkObjectsOnce(mirror, dir string) (gone bool, err error) {
	src, dst := filepath.Join(mirror, "objects"), filepath.Join(dir, ".git", "objects")
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			gone = true
			return nil
		}
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
		err = os.Link(p, target)
		if err == nil || errors.Is(err, fs.ErrExist) {
			return nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			gone = true
			return nil
		}
		in, err := os.Open(p)
		if errors.Is(err, fs.ErrNotExist) {
			gone = true
			return nil
		}
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
	return gone, err
}

// syncMirrorRefs sets `refs/remotes/mirror/<branch>` in the checkout to each branch of the mirror
// and `refs/tags/<tag>` to each of its tags, and reports the ones that moved, as
// `mirror/<branch> <was>..<now>` and `tags/<tag> <was>..<now>` (`<was>` is `new` for a ref the
// checkout had not seen), and the mirror's branch tips, as read from the mirror. Refs the mirror
// no longer has are left: a ref that vanishes under a running agent is a surprise, and a stale
// remote-tracking ref or tag is not.
func syncMirrorRefs(mirror, repo *git.Repository) (moved []string, tips []plumbing.Hash, err error) {
	// What the checkout has, read once: a lookup per ref reads packed-refs from the top each
	// time, and a checkout of a repository with thousands of tags has them all packed (packTags).
	have := map[plumbing.ReferenceName]plumbing.Hash{}
	own, err := repo.Storer.IterReferences()
	if err != nil {
		return nil, nil, err
	}
	if err := own.ForEach(func(ref *plumbing.Reference) error {
		have[ref.Name()] = ref.Hash()
		return nil
	}); err != nil {
		return nil, nil, err
	}
	refs, err := mirror.References()
	if err != nil {
		return nil, nil, err
	}
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		var name plumbing.ReferenceName
		var short string
		switch {
		case ref.Name().IsBranch():
			tips = append(tips, ref.Hash())
			name = plumbing.NewRemoteReferenceName("mirror", ref.Name().Short())
			short = "mirror/" + ref.Name().Short()
		case ref.Name().IsTag():
			// A tag is a tag in the checkout too, under its own name: there is no remote-tracking
			// place for tags, and git reads `refs/tags/` wherever a tag is named. An annotated
			// tag's ref is its tag object, as in the mirror.
			name = ref.Name()
			short = "tags/" + ref.Name().Short()
		default:
			return nil
		}
		was := "new"
		if old, ok := have[name]; ok {
			if old == ref.Hash() {
				return nil
			}
			was = ShortSha(old.String())
		}
		if err := repo.Storer.SetReference(plumbing.NewHashReference(name, ref.Hash())); err != nil {
			return err
		}
		moved = append(moved, short+" "+was+".."+ShortSha(ref.Hash().String()))
		return nil
	})
	sort.Strings(moved)
	return moved, tips, err
}

// packTags writes every tag of the mirror into a fresh checkout's packed-refs, in one write, as
// a clone does. A ref file per tag is a write per tag: 0.4s for 450 tags on a laptop, at every
// launch, where a packed-refs file of the same tags is one. syncMirrorRefs then finds each tag in
// place and writes none; a tag that moves later is written loose over its packed line, which is
// how git itself keeps the two. No traits are claimed, so git peels an annotated tag as it needs.
func packTags(mirror *git.Repository, dir string) error {
	refs, err := mirror.References()
	if err != nil {
		return err
	}
	var lines []string
	if err := refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsTag() && ref.Type() == plumbing.HashReference {
			lines = append(lines, ref.Hash().String()+" "+ref.Name().String()+"\n")
		}
		return nil
	}); err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}
	sort.Strings(lines)
	return os.WriteFile(filepath.Join(dir, ".git", "packed-refs"), []byte(strings.Join(lines, "")), 0o644)
}

// Refresh brings one checkout's view of upstream up to the mirror, which the caller has just
// fetched: the mirror's new objects are linked in as they were at open, and `refs/remotes/mirror/*`
// and `refs/tags/*` move to where the mirror's branches and tags are. The task's own branch and
// working tree are untouched; what the agent does with a newer `mirror/main` is its own git. It
// returns what moved.
//
// This is the parent writing into a checkout a task is using, and it is safe for the same reason
// a fetch into a repository someone is working in is: objects are only ever added, and the refs
// written are the remote-tracking ones nothing in the checkout holds a lock on.
func (t *Tree) Refresh(mirror string) ([]string, error) {
	if err := linkObjects(mirror, t.Dir); err != nil {
		return nil, fmt.Errorf("repo %s: sharing the mirror's objects: %w", t.Repo.Name(), err)
	}
	// Reopened, so this side sees the packs just linked in: go-git reads a repository's list of
	// packs once.
	repo, err := git.PlainOpen(t.Dir)
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	t.repo = repo
	mr, err := git.PlainOpen(mirror)
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	moved, upstream, err := syncMirrorRefs(mr, t.repo)
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	t.upstream = upstream
	return moved, nil
}

// Tree is the checkout of the repository named, or nil.
func (w *Workspace) Tree(name string) *Tree {
	for _, t := range w.Trees {
		if t.Repo.Name() == name {
			return t
		}
	}
	return nil
}

// The checkout's git config says where it started: `viff.base`.
const baseSection, baseOption = "viff", "base"

// Reopen rebuilds a Workspace over a checkout that already exists — the task process's view of
// what the runner prepared for it. A repo with no From gets the base its checkout records.
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
		dir := filepath.Join(root, r.Name())
		repo, err := git.PlainOpen(dir)
		if err != nil {
			return nil, fmt.Errorf("reopen %s in %s: %w", r.Name(), root, err)
		}
		if r.From == "" {
			cfg, err := repo.Config()
			if err != nil {
				return nil, fmt.Errorf("reopen %s in %s: %w", r.Name(), root, err)
			}
			r.From = cfg.Raw.Section(baseSection).Option(baseOption)
		}
		w.Trees = append(w.Trees, &Tree{Repo: r, Dir: dir, repo: repo, Head: bases[r.Name()]})
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
		fmt.Fprintf(&b, "  %s/ — %s at %s\n", t.Repo.Name(), t.Repo.Repo.Repo, t.Repo.From)
	}
	fmt.Fprintf(&b, "Every path is relative to that directory, so a file in the first repository "+
		"above is `%s/…`. Each checkout is on your branch, `%s`.\n\n", w.Trees[0].Repo.Name(), w.Branch)
	return b.String()
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
// at the far end about who it was for, so the only externally checkable link back into the viff instance is
// what this writes down — and it is checkable, by anyone who can read both the repository and the
// viff.
func (t *Tree) CommitWork(l *runner.Launch, result string) (bool, error) {
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
		// trailers are the runner's only attribution channel out of the viff instance, and a commit
		// the agent wrote carries none of them. Work preserved without provenance beats work
		// destroyed, which is the trade being made here and not a preference.
		return true, nil
	}
	wt, err := t.repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return false, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	// The runner made this commit itself, for a program with no agent to commit; it says so,
	// and who it was for is a trailer.
	sig := &object.Signature{Name: RunnerAuthor.Name, Email: RunnerAuthor.Email, When: time.Now()}
	if _, err := wt.Commit(CommitMessage(l, result), &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		return false, fmt.Errorf("repo %s: commit: %w", t.Repo.Name(), err)
	}
	return true, nil
}

// Refusal reads a failed push and reports what the FAR SIDE said when it declined, or "" when
// the failure was not a decision at all.
//
// The distinction is the whole of how a push is reported, and getting it backwards fills the
// record with alarm about the thing working as intended. A remote that will not fast-forward, a
// protected branch, a ruleset, a hook: those are an authority OUTSIDE viff exercising rules
// about its own history — which is precisely the authority this design deliberately does not
// take — docs/reference/examples/README.md is blunt that viff should not compete with git
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
const deliveryRemote = "viff-delivery"

// commitMessage carries the provenance the runner was given. Every trailer is conditional: an
// absent one means viff did not know, and writing it out empty would claim to have asked.
func CommitMessage(l *runner.Launch, result string) string {
	subject, body := firstLine(result), ""
	if isDocument(result) {
		// A CONTRACT-shaped answer is not a sentence, and a subject line is. A task with an
		// `answer:` contract returns a document — the whole point of the field — so its first
		// line is `{"files": 1, "fixed": 1, "findings": [{"symbol": …` truncated at 72
		// characters, which is what `git log --oneline` then shows forever. One is on main.
		//
		// So the document becomes the BODY, where it is worth reading, and the subject is
		// derived from what viff knows about the run. The alternative — digging a `report` or
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
		// would silently un-declare every Viff-* line below it.
		b.WriteString(strings.TrimRight(body, "\n"))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "Viff-Task: %s\n", l.ID)
	p := l.Provenance
	if p.Principal != "" {
		fmt.Fprintf(&b, "Viff-Principal: %s\n", p.Principal)
	}
	if p.Subject != "" {
		fmt.Fprintf(&b, "Viff-Subject: %s\n", p.Subject)
	}
	if p.Chain != "" {
		fmt.Fprintf(&b, "Viff-Chain: %s\n", p.Chain)
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
// what. `comment-drift on drift.head.head-moved@git.head.viff` says more to somebody reading
// `git log` than the task id does, and both are recorded in the trailers below anyway.
func describe(l *runner.Launch) string {
	p := l.Provenance
	switch {
	case p.Chain != "" && p.Subject != "":
		return firstLine(p.Chain + " on " + p.Subject)
	case p.Chain != "":
		return firstLine(p.Chain)
	}
	return "work by " + l.ID
}

// Deliverable reports whether work here may be sent somewhere: there is a repository, and the
// launch's environment gives a push to one. False means a read-only workspace — nothing was
// ever going to leave it — which is the only case where removing it after delivering nothing
// is not destroying something.
//
// THE ENVIRONMENT IS THE AUTHORITY on what may leave, here and in Salvage: a push is the run's
// when a block gives one. The act itself is still decided by the autonomy's rows alone until
// presence in the environment is the reach (the next step of the environment plan), so a run
// whose rows allow a push its environment does not give could push and still be judged
// read-only here; written spawns keep the two aligned until then.
func (w *Workspace) Deliverable(l *runner.Launch) bool {
	return len(w.Trees) > 0 && l.Pushes("")
}

// Remove drops the whole workspace. The runner calls it when a task ends, unless it was told to
// keep workspaces, because the alternative is a disk that fills at the rate work arrives — an
// argument about SUCCESSFUL runs; Delivery.Loose is the case where it does not apply.
//
// One directory tree and nothing else — no worktree to unregister and no branch to delete out
// of a shared mirror, because a clone per task keeps every task's refs inside the directory
// being removed.
func (w *Workspace) Remove(_ context.Context) error { return RemoveTree(w.Root) }

// RemoveTree removes a directory a task wrote in, whatever modes the task left on it. A task's
// tools make read-only trees as a matter of course — Go's module cache is one, every directory
// of it 0555 — and `os.RemoveAll` cannot unlink out of a directory it may not write, so the
// modes a task chose would decide whether its directory goes. Where the plain removal is
// refused, every directory is made the owner's to read, write and search, and it is removed
// again.
//
// Directories only, and never through a symlink: a file's own mode does not hold it in place,
// and a link is removed as a name, wherever it points.
func RemoveTree(root string) error {
	err := os.RemoveAll(root)
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(root)
}

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
// chosen by whoever launches — including a client posting straight at the runner — so it is
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
// viff's trigger.spawnID, which disambiguates its own safeSeg the same way and for the same reason.
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
// link from a commit on the forge back into the viff instance, so a delivery without them is refused
// rather than quietly repaired — repairing would rewrite the agent's commits, and the agent
// would no longer know the SHAs it delivered. The commit's identity (IdentityFor) is held the
// same way, for the same reason.
func RequiredTrailers(l *runner.Launch) []string {
	lines := []string{"Viff-Task: " + l.ID}
	if p := l.Provenance.Principal; p != "" {
		lines = append(lines, "Viff-Principal: "+p)
	}
	return lines
}

// authoredWorkMax bounds how far back a delivery is read: a branch whose base is no longer in
// its history (the agent rebased onto something else) is refused rather than walked to the root.
const authoredWorkMax = 200

// authoredWork is the delivery an AGENT decides: the commits it made on this branch beyond the
// base, each carrying the required trailers and authored and committed as the launch's identity
// (IdentityFor). ok reports whether there is anything to deliver at all; refusal is why the push
// must not go, in words the agent can act on, or "" when it may. Uncommitted changes are not
// looked at: what is not committed is not delivered.
//
// The identity is the launch's, not the checkout's config: GIT_AUTHOR_*/GIT_COMMITTER_*, `git -c
// user.*` and `git config user.*` all override the config, and a commit is what it says, so the
// identity on a delivered commit is evidence of who made it only if it is held here.
func (t *Tree) AuthoredWork(l *runner.Launch) (ok bool, refusal string, head plumbing.Hash, err error) {
	// Opened afresh: the task's own git may have fetched objects into packs this side's open
	// repository has never listed (go-git reads the list once).
	repo, err := git.PlainOpen(t.Dir)
	if err != nil {
		return false, "", head, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	ref, err := repo.Head()
	if err != nil {
		return false, "", head, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	head = ref.Hash()
	if head.String() == t.Head {
		return false, "", head, nil
	}
	upstream, err := upstreamOf(repo, t.upstream, t.Head)
	if err != nil {
		return false, "", head, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	// Every commit reachable from the head and not beyond a boundary — the base, or a commit
	// already upstream (one a rebase or a merge of mirror/* put beneath the task's work) — is the
	// task's. Each path stops at its own boundary, so a merge's other side is read too.
	want := IdentityFor(l)
	var bad []string
	n, offBase, unsigned, misidentified, authorOnly := 0, false, false, false, false
	err = walk(repo, head, func(h plumbing.Hash) bool {
		return h.String() == t.Head || upstream[h]
	}, func(c *object.Commit) bool {
		n++
		if n > authoredWorkMax || c.NumParents() == 0 {
			offBase = true // a history that doesn't come back to the base, or too far to read
			return false
		}
		var whys []string
		if why := MissingTrailers(c.Message, l); why != "" {
			whys, unsigned = append(whys, why), true
		}
		author := c.Author.Name != want.Name || c.Author.Email != want.Email
		committer := c.Committer.Name != want.Name || c.Committer.Email != want.Email
		if author {
			whys = append(whys, "author "+c.Author.Name+" <"+c.Author.Email+">")
		}
		if committer {
			whys = append(whys, "committer "+c.Committer.Name+" <"+c.Committer.Email+">")
		}
		misidentified = misidentified || author || committer
		authorOnly = authorOnly || author && !committer
		if len(whys) > 0 {
			bad = append(bad, ShortSha(c.Hash.String())+" ("+strings.Join(whys, "; ")+")")
		}
		return true
	})
	if err != nil {
		return false, "", head, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	if n == 0 {
		return false, "", head, nil // on upstream, with nothing of its own on top
	}
	if offBase {
		return true, fmt.Sprintf("not pushed: the branch no longer descends from the base this task "+
			"started at (%s); deliver work on top of it", ShortSha(t.Head)), head, nil
	}
	if len(bad) > 0 {
		var b strings.Builder
		b.WriteString("not pushed: ")
		if unsigned {
			fmt.Fprintf(&b, "every commit delivered must end with these trailers, as the last paragraph "+
				"of its message:\n  %s\n", strings.Join(RequiredTrailers(l), "\n  "))
		}
		if misidentified {
			fmt.Fprintf(&b, "every commit delivered must carry, as its author and its committer, the "+
				"name and email this checkout was given:\n  %s <%s>\nDo not override them with "+
				"GIT_AUTHOR_*/GIT_COMMITTER_*, `git -c user.*` or `git config user.*`.\n", want.Name, want.Email)
		}
		fmt.Fprintf(&b, "These commits do not: %s. ", strings.Join(bad, "; "))
		if authorOnly {
			fmt.Fprintf(&b, "For another's patch, re-commit each such commit with --reset-author and "+
				"credit its author with a `Co-Authored-By: Name <email>` trailer: `git commit --amend "+
				"--reset-author` when it is the tip, or, when the branch holds only your own commits "+
				"since the base, `git rebase -x 'git commit --amend --no-edit --reset-author' %s`; "+
				"otherwise amend each such commit by hand. ", ShortSha(t.Head))
		}
		b.WriteString("Amend or re-commit with them, then push again.")
		return true, b.String(), head, nil
	}
	return true, "", head, nil
}

// Tip is the commit the task has checked out now (its HEAD), read afresh.
func (t *Tree) Tip() (plumbing.Hash, error) {
	repo, err := git.PlainOpen(t.Dir)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	ref, err := repo.Head()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("repo %s: %w", t.Repo.Name(), err)
	}
	return ref.Hash(), nil
}

// Made reports whether commit h is this task's own: in the checkout, with the trailers every
// commit the task delivers carries. A branch whose tip it is was made by the task, however it
// reached the forge (its own push, or the sweep retrying one).
func (t *Tree) Made(h plumbing.Hash, l *runner.Launch) bool {
	repo, err := git.PlainOpen(t.Dir)
	if err != nil {
		return false
	}
	c, err := repo.CommitObject(h)
	return err == nil && MissingTrailers(c.Message, l) == ""
}

// upstreamMax bounds how many commits are read from the mirror's branches for the ones already
// upstream: a branch that forked before the base this task started at is read no further. Past
// it, an upstream commit beneath the task's work reads as the task's, and the delivery is refused
// naming it (or as no longer descending from the base): safe, and unlikely in a task's life.
const upstreamMax = 5000

// upstreamOf is the commits already upstream: those reachable from tips, the mirror's branches as
// the runner last wrote them into the checkout, back to base, at most upstreamMax of them. A
// delivery's commits are the ones beyond them. The tips are the runner's record, never the
// checkout's mirror/* refs: those are the task's to move, and one pointed at an unsigned commit
// would pass it as upstream.
func upstreamOf(repo *git.Repository, tips []plumbing.Hash, base string) (map[plumbing.Hash]bool, error) {
	seen := map[plumbing.Hash]bool{}
	for _, tip := range tips {
		err := walk(repo, tip, func(h plumbing.Hash) bool {
			return h.String() == base || seen[h]
		}, func(c *object.Commit) bool {
			seen[c.Hash] = true
			return len(seen) < upstreamMax
		})
		if err != nil {
			return nil, err
		}
	}
	return seen, nil
}

// walk visits the commits reachable from from, each once, newest first along each path, not
// past a commit stop says is a boundary. visit reports whether to read on beyond the commit.
func walk(repo *git.Repository, from plumbing.Hash, stop func(plumbing.Hash) bool, visit func(*object.Commit) bool) error {
	queue, done := []plumbing.Hash{from}, map[plumbing.Hash]bool{}
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if done[h] || stop(h) {
			continue
		}
		done[h] = true
		c, err := repo.CommitObject(h)
		if err != nil {
			return err
		}
		if visit(c) {
			queue = append(queue, c.ParentHashes...)
		}
	}
	return nil
}

// missingTrailers says which required trailer a commit message lacks, reading them the way git
// does: the last paragraph, every line a `Key: value`. "" when all are there.
func MissingTrailers(msg string, l *runner.Launch) string {
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
