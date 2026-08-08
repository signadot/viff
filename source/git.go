package source

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/signadot/verse/entity"
)

// GitSystem is the entity slice a git Source owns and is the single writer of.
const GitSystem = "git"

// Git reflects one or more git repositories into the git:* slice of the verse — read-only
// reflection, the inbound half of a Source (docs/sources.md). It is the single writer of
// git:*, so the slice needs no CAS. Reflection is content-addressed, which makes reconcile
// CURSORLESS: `git for-each-ref` yields a ref→sha map, and comparing shas against the stored
// git:ref:* entities IS the level-triggered diff — no since-token, no missed-event window.
//
// It is **multi-repo native**: a repo is registered under an id, every ref/head entity carries
// that id (as the `repo` field and as an id prefix), and reconcile is per-repo — so a webhook
// for one repo never tombstones another's refs. It reflects git-*aware* state (what SDLC flows
// react to — "commit landed on main", "branch deleted", "tag created"), an index not a
// warehouse: a task needing a diff follows the ref into the real repo (`git show`). Acting
// back on a repo (commit, tag, push) is the separate outbound executor path, not here.
type Git struct {
	mu          sync.Mutex
	repos       map[string]repo
	commitDepth int // how many tip commits to reflect as git:commit:<sha>
	// system is the slice this source writes, GitSystem unless an operator said otherwise.
	// Set once before serving and read without the lock thereafter, as a Config field would be
	// on the connectors that have one — see SetSystem.
	system string
}

// repo is one registered repository: where it is, and — for a mirror — what it is a mirror OF.
//
// A remote makes the read a fetch, and that is the difference between a source that sits next
// to its truth (a build host, a laptop) and one deployed away from it (a pod, which has no
// repository until it makes one). Both are the same reflection afterwards; only how the local
// copy comes to be current differs.
type repo struct {
	dir    string
	remote string // when set, `git fetch` before reading — this dir is a mirror of it
}

// NewGit returns an empty git Source; register repositories with AddRepo or AddMirror.
func NewGit() *Git { return &Git{repos: map[string]repo{}, commitDepth: 50, system: GitSystem} }

// SetSystem points this source at a slice other than `git:*`. Call it before serving; it is read
// on every pass and never written again.
//
// A source is deployed, and more than one of them will be — different hosts, different
// credentials, a partition drawn by whoever runs them. Two deployments sharing `git:*` is the
// ordinary case and needs only disjoint claims; one that wants no coordination at all takes a
// system of its own, and two systems are disjoint on the first segment. See
// docs/sources.md, where the argument is made at length for the connector that raised
// it.
//
// The system is a segment of the address and of the route that serves the registration, so
// neither punctuation survives — the same check source.Entity.Check makes, made here so a bad
// flag fails the program rather than the install.
func (g *Git) SetSystem(system string) error {
	if system == "" {
		return nil
	}
	if strings.ContainsAny(system, ":/") {
		return fmt.Errorf("git: system %q may not contain : or / — it is a segment of the "+
			"address (sources:<system>:<name>) and of the route that serves it", system)
	}
	g.system = system
	return nil
}

// AddRepo registers a repository under id (defaults to the dir's base name) and returns the
// id used. Re-adding an id repoints it.
func (g *Git) AddRepo(id, dir string) string {
	if id == "" {
		id = filepath.Base(filepath.Clean(dir))
	}
	g.mu.Lock()
	g.repos[id] = repo{dir: dir}
	g.mu.Unlock()
	return id
}

// AddMirror registers a MIRROR of remote at dir, cloning it if dir does not already hold one,
// and returns the id used (defaulting to the remote's base name, less any `.git`).
//
// This is what makes a git source deployable away from the repository it reflects. A pod has
// no repositories, and the two obvious answers are both worse than this one: a hostPath mounts
// somebody's laptop into the cluster, and a shared volume makes the reflection depend on
// whatever else writes to it. A mirror is the source's own copy of a remote it can reach,
// which is the ordinary shape of a read-only consumer of a repository.
//
// FETCHING IS THE READ, and that is why it does not violate "a source never acts on the
// external system": `git fetch` takes nothing from the remote but what `git ls-remote` would,
// and writes only into this source's private copy. Pushing, committing and tagging remain the
// outbound executor path, and none of them is here.
//
// The clone happens HERE, at startup, rather than lazily inside a reflect: an unreachable
// remote or a bad credential is then a task that does not start, in front of the operator
// who just deployed it, instead of a registration that installs clean and reports a failure
// into lastError once a minute forever.
func (g *Git) AddMirror(ctx context.Context, id, remote, dir string) (string, error) {
	if remote == "" {
		return "", fmt.Errorf("git: a mirror needs a remote to mirror")
	}
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(strings.TrimRight(remote, "/")), ".git")
	}
	// A mirror is a bare repository, so HEAD is the file that says one is already there. Not
	// re-cloned when it is: the volume outliving the pod is the point of putting it on one.
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		parent := filepath.Dir(dir)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return "", fmt.Errorf("git: %s: %w", parent, err)
		}
		if _, err := gitCmd(ctx, parent, "clone", "--mirror", remote, dir); err != nil {
			return "", fmt.Errorf("git: cloning %s into %s: %w", remote, dir, err)
		}
	}
	g.mu.Lock()
	g.repos[id] = repo{dir: dir, remote: remote}
	g.mu.Unlock()
	return id, nil
}

// System is the owned slice.
func (g *Git) System() string { return g.system }

// Owns is the ownership claim an install checks against the other installed sources, as ref
// globs (see own.go).
//
// It is KIND-LESS where a reflection's Covers names a kind, and the difference is real rather
// than sloppy. A cover says what one pass is complete for — the mutable kinds, since history
// is kept — while this says what no other source may write, which is the whole of a repo's
// slice, commits included. Two sources reflecting one repo would fight over its commits just
// as surely as over its branches.
//
// TWO ENTRIES PER REPO, and the shortcut is worth naming because it is tempting. Refs and
// commits are `<repo>/<name>`, but a head is the bare `<repo>` — so the claim is
// `git:*:<id>/*` AND `git:head:<id>`. The single glob `<id>*` covers both, and also claims
// `<id>-extras/main`: not a false alarm but a real overclaim, and one that would make two
// perfectly disjoint repos refuse to install alongside each other.
func (g *Git) Owns() []Own {
	g.mu.Lock()
	defer g.mu.Unlock()
	ids := make([]string, 0, len(g.repos))
	for id := range g.repos {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Own, 0, 2*len(ids))
	for _, id := range ids {
		out = append(out, Own{Ref: g.system + ":*:" + id + "/*"}, Own{Ref: g.system + ":head:" + id})
	}
	return out
}

// HeadRef is the ref that targets one repo: a reconcile of it reconciles that repo, since a
// per-repo pass is content-addressed and subsumes finer targeting. It is what a hint names.
func HeadRef(id string) entity.Ref {
	return entity.Ref{System: GitSystem, Kind: "head", ID: id}
}

// HeadRef is the same ref in THIS source's system, which is what a hook this source installs
// has to name: a hint at `git:head:x` reaches nothing when the source writes `mirror:head:x`.
func (g *Git) HeadRef(id string) entity.Ref {
	return entity.Ref{System: g.system, Kind: "head", ID: id}
}

// hookMarker identifies the hook verse installs, so we only ever overwrite/remove our own.
const hookMarker = "# verse git Source hook"

// InstallHook writes a reference-transaction hook into repo id's git dir that POSTs a HINT to
// verse on every ref change — the git-native "webhook". It refuses to clobber a pre-existing
// reference-transaction hook that isn't ours. Idempotent for ours.
//
// hintURL is the registration's hint route, `<verse>/sources/git/<name>/hint`. The ref is
// appended HERE, because which ref targets a repo is git's knowledge and not the route's: a
// reconcile of `git:head:<id>` is a reconcile of that repo. verse never parses this delivery —
// there is nothing to parse, which is the point.
func (g *Git) InstallHook(id, hintURL string) error {
	dir, ok := g.dirOf(id)
	if !ok {
		return fmt.Errorf("git: no repo %q", id)
	}
	hooks, err := gitHooksDir(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		return err
	}
	path := filepath.Join(hooks, "reference-transaction")
	if b, err := os.ReadFile(path); err == nil && !strings.Contains(string(b), hookMarker) {
		return fmt.Errorf("git: %s already has a reference-transaction hook; not overwriting", id)
	}
	// git runs this three times per transaction (prepared|committed|aborted); act on
	// committed. It carries no data at all — just which slice went stale — which is what lets
	// the route it posts to stay unauthenticated in a wide-open verse.
	script := "#!/bin/sh\n" + hookMarker + "\n" +
		`[ "$1" = committed ] || exit 0` + "\n" +
		fmt.Sprintf(`curl -fsS -X POST "%s?ref=%s" >/dev/null 2>&1 || true`+"\n",
			hintURL, url.QueryEscape(g.HeadRef(id).String()))
	return os.WriteFile(path, []byte(script), 0o755)
}

// RemoveHook removes the reference-transaction hook, but only if it is ours.
func (g *Git) RemoveHook(id string) error {
	dir, ok := g.dirOf(id)
	if !ok {
		return nil
	}
	hooks, err := gitHooksDir(dir)
	if err != nil {
		return err
	}
	path := filepath.Join(hooks, "reference-transaction")
	if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), hookMarker) {
		return os.Remove(path)
	}
	return nil
}

func (g *Git) dirOf(id string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	d, ok := g.repos[id]
	return d.dir, ok
}

// gitHooksDir resolves a repo's hooks directory (handles worktrees/bare via git rev-parse).
func gitHooksDir(dir string) (string, error) {
	out, err := gitCmd(context.Background(), dir, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(dir, out)
	}
	return out, nil
}

var _ Source = (*Git)(nil)

// Reflect reads the repositories and returns what git:* should hold. A zero ref reads every
// registered repo (a full resync); a git:* ref reads just the repo it names (the targeted
// pass a hint asks for). Per-repo reflection is content-addressed, so it subsumes finer
// targeting — there is no cursor to keep and no missed-event window.
//
// It writes nothing, and the answer is scoped by what it Covers: `git:ref:*` and `git:head:*`
// for each repo it actually read. Commits are carried and covered by NOTHING, which is how
// "keep history" is said — they fall out of the bounded tip window and stay standing.
func (g *Git) Reflect(ctx context.Context, ref entity.Ref) (Reflection, error) {
	var out Reflection
	targets := g.targets(ref)
	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Strings(ids) // one order, so two passes over the same repos answer the same way
	for _, id := range ids {
		rn, err := g.reflectRepo(ctx, id, targets[id])
		if err != nil {
			return Reflection{}, fmt.Errorf("git reflect %s: %w", id, err)
		}
		out.Covers = append(out.Covers, rn.Covers...)
		out.Entities = append(out.Entities, rn.Entities...)
	}
	return out, nil
}

// targets is the repo id→repo set a reconcile should touch: all repos for a zero ref, else the
// single repo the ref belongs to (its id is the git:head:<repo> id, or the leading segment of
// a git:ref:<repo>/<name> id).
func (g *Git) targets(ref entity.Ref) map[string]repo {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ref.System != g.system || ref.ID == "" {
		out := make(map[string]repo, len(g.repos))
		for id, r := range g.repos {
			out[id] = r
		}
		return out
	}
	id := ref.ID
	if ref.Kind == "ref" {
		id, _, _ = strings.Cut(ref.ID, "/") // <repo>/<name>
	}
	if r, ok := g.repos[id]; ok {
		return map[string]repo{id: r}
	}
	return nil
}

// reflectRepo reads one repository. Everything it returns is scoped by the `repo` field —
// including the covers — so one repo's pass never tombstones another's refs.
func (g *Git) reflectRepo(ctx context.Context, id string, rp repo) (Reflection, error) {
	dir := rp.dir
	// A mirror's read starts with a fetch, and a FAILED one fails the pass rather than
	// reflecting what was fetched last time. Stale-and-silent is the worse failure of the two:
	// converge would write nothing, the registration would carry no error, and a source whose
	// credential expired last Tuesday would look exactly like a repository where nothing has
	// happened. An error lands in lastError, where somebody can see it.
	if rp.remote != "" {
		if _, err := gitCmd(ctx, dir, "fetch", "--prune"); err != nil {
			return Reflection{}, fmt.Errorf("fetching %s: %w", rp.remote, err)
		}
	}
	// What this answer is COMPLETE for: the two mutable kinds, narrowed to this repo by the
	// field the payloads below already carry. Declared BEFORE the read, and unconditionally:
	// a repo whose every branch was deleted must still cover `git:ref:{repo: id}`, or the
	// refs it no longer has would stand forever.
	//
	// `git:commit:*` is deliberately absent, and that absence IS the history rule. Commits are
	// carried in Entities and covered by nothing, so one that has fallen out of the bounded
	// tip window is simply not mentioned — and an unmentioned, uncovered entity stays.
	mine := entity.Obj{"repo": id}.Payload()
	rn := Reflection{Covers: []entity.Pattern{
		{System: g.system, Kind: "ref", Predicate: mine},
		{System: g.system, Kind: "head", Predicate: mine},
	}}

	// Refs: refs/heads, refs/tags, refs/remotes — git:ref:<repo>/<short> {repo, name, sha, type}.
	refs, err := gitForEachRef(ctx, dir)
	if err != nil {
		return Reflection{}, err
	}
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r := refs[name]
		rn.Add(entity.Ref{System: g.system, Kind: "ref", ID: id + "/" + name},
			entity.Obj{"repo": id, "name": name, "sha": r.sha, "type": r.typ})
	}

	// HEAD: branch, tip sha, dirty (and ahead/behind when an upstream is set).
	if head := gitHead(ctx, dir); head != nil {
		head["repo"] = id
		rn.Add(entity.Ref{System: g.system, Kind: "head", ID: id}, head)
	}

	// Recent commits: a bounded tip window. Scoped per-repo (git:commit:<repo>/<sha>) — a
	// sha is NOT a sound cross-repo key: repos have independent histories, "the same" commit
	// diverges under rebase/amend, and the object format (sha1→sha256) changes hashes. So a
	// commit belongs to its repo; two repos never share one entity.
	for _, c := range gitRecentCommits(ctx, dir, g.commitDepth) {
		rn.Add(entity.Ref{System: g.system, Kind: "commit", ID: id + "/" + c.sha},
			entity.Obj{"repo": id, "sha": c.sha, "msg": c.msg, "author": c.author, "parents": c.parents})
	}
	return rn, nil
}

// ── git plumbing (all shape-agnostic; run with the repo dir as cwd) ──────────

type refInfo struct{ sha, typ string }

func gitForEachRef(ctx context.Context, dir string) (map[string]refInfo, error) {
	out, err := gitCmd(ctx, dir, "for-each-ref",
		"--format=%(refname:short) %(objectname) %(objecttype)")
	if err != nil {
		return nil, err
	}
	refs := map[string]refInfo{}
	for _, line := range nonEmptyLines(out) {
		if f := strings.Fields(line); len(f) >= 3 {
			refs[f[0]] = refInfo{sha: f[1], typ: f[2]}
		}
	}
	return refs, nil
}

// gitHead returns the git:head payload, or nil for an unborn repo (no HEAD sha yet).
func gitHead(ctx context.Context, dir string) entity.Obj {
	sha, err := gitCmd(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return nil // no commits yet
	}
	p := entity.Obj{"sha": sha}
	if branch, err := gitCmd(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && branch != "" {
		p["ref"] = branch
	}
	porcelain, _ := gitCmd(ctx, dir, "status", "--porcelain")
	p["dirty"] = porcelain != ""
	if lr, err := gitCmd(ctx, dir, "rev-list", "--count", "--left-right", "@{upstream}...HEAD"); err == nil {
		if f := strings.Fields(lr); len(f) == 2 {
			p["behind"], _ = strconv.Atoi(f[0])
			p["ahead"], _ = strconv.Atoi(f[1])
		}
	}
	return p
}

type commitInfo struct{ sha, msg, author, parents string }

func gitRecentCommits(ctx context.Context, dir string, depth int) []commitInfo {
	// Unit-separated fields (0x1f) so subjects with spaces survive the split.
	out, err := gitCmd(ctx, dir, "log", "-n", strconv.Itoa(depth), "--format=%H%x1f%s%x1f%an%x1f%P")
	if err != nil {
		return nil // no commits
	}
	var cs []commitInfo
	for _, line := range nonEmptyLines(out) {
		if f := strings.Split(line, "\x1f"); len(f) == 4 {
			cs = append(cs, commitInfo{sha: f[0], msg: f[1], author: f[2], parents: f[3]})
		}
	}
	return cs
}

// gitCmd runs one git command in dir and returns its stdout, with git's own stderr folded into
// the error.
//
// The folding is what makes a deployed mirror debuggable: an ExitError renders as "exit status
// 128", and that is the whole of what a failed clone of a private remote would otherwise write
// into the registration's lastError. (cmd.Output fills ExitError.Stderr only while Stderr is
// nil, and setting it — which is what captures the message — is exactly what empties that.)
func gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
	}
	return strings.TrimRight(string(out), "\n"), err
}

func nonEmptyLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
