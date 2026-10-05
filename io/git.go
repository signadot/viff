package io

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

	"github.com/signadot/verse/entity"
)

// GitRoot is the entity slice a git Source owns and is the single writer of.
const GitRoot = "git"

// Git reflects one or more git repositories into the git.* slice of the verse — read-only
// reflection, the inbound half of a Source (docs/io.md). It is the single writer of
// git.*, so the slice needs no CAS. Reflection is content-addressed, which makes reconcile
// CURSORLESS: `git for-each-ref` yields a ref→sha map, and comparing shas against the stored
// git.<repo>.ref.* entities IS the level-triggered diff — no since-token, no missed-event window.
//
// It is **multi-repo native**: a repo is registered under an id, and THE ID IS A LEVEL OF THE
// ADDRESS — `git.<repo>.ref.<name>`, `git.<repo>.commit.<sha>`, `git.<repo>.head` — so a
// repository is a place in the tree rather than a prefix packed inside every id. Reconcile is
// per-repo, and a webhook for one repo never tombstones another's refs, because a pass reads
// and covers its own repository's place and nothing beside it. It reflects git-*aware* state (what SDLC flows
// react to — "commit landed on main", "branch deleted", "tag created"), an index not a
// warehouse: a task needing a diff follows the ref into the real repo (`git show`). Acting
// back on a repo is the out half (gitout.go): a tag made, a branch moved, and nothing else.
type Git struct {
	repos       repoSet
	commitDepth int // how many tip commits to reflect as git.<repo>.commit.<sha>
	// trees are the directories whose tree object is reflected on every ref, as
	// `tree: {<name>: <sha>}`: the identity of a directory's contents at that ref, which is what
	// a charter compares when "the same rules" has to mean the same bytes and not the same
	// commit (AddTree). Each is a name and the path it stands for.
	trees []tree
	// root is the slice this source writes, GitRoot unless an operator said otherwise.
	// Set once before serving and read without the lock thereafter, as a Config field would be
	// on the connectors that have one — see SetRoot.
	root string
}

// NewGit returns an empty git Source; register repositories with AddRepo or AddMirror.
func NewGit() *Git { return &Git{repos: newRepoSet(), commitDepth: 50, root: GitRoot} }

// SetRoot points this source at a slice other than `git.*`. Call it before serving; it is read
// on every pass and never written again.
//
// A source is deployed, and more than one of them will be — different hosts, different
// credentials, a partition drawn by whoever runs them. Two deployments sharing `git.*` is the
// ordinary case and needs only disjoint claims; one that wants no coordination at all takes a
// root of its own, and two roots are disjoint on the first segment. See
// docs/io.md, where the argument is made at length for the connector that raised
// it.
//
// The root is the first segment of every address this source writes, so no punctuation of an
// address survives — the same check source.Entity.Check makes, made here so a bad flag fails
// the program rather than the install.
func (g *Git) SetRoot(root string) error {
	if root == "" {
		return nil
	}
	if strings.ContainsAny(root, ":/.") {
		return fmt.Errorf("git: root %q may not contain : / or . — it is the first segment of "+
			"every address this source writes", root)
	}
	g.root = root
	return nil
}

// tree is one directory a Git reflects the tree sha of: under name, for the path.
type tree struct{ name, path string }

// EmptyTree is git's tree object with nothing in it, which every repository has without storing
// it: what a directory that is not there reflects as, so that a directory removed reads as a
// change to it, to no files, and installing it is installing nothing.
const EmptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// AddTree reflects the tree object of a directory on every ref of every repository, as
// `tree: {<name>: <object sha>}` on `git.<repo>.ref.<name>`. A ref where the directory is not
// there carries the empty tree (EmptyTree): no files is a state of the directory like any other,
// and removing it is a change to that state. spec is `<name>=<path>`, or a bare path whose last segment is the name: `.verse/charters`
// is reflected as `tree.charters`. The name is one plain word, so a charter can spell it in a
// reference (`$subject.value.tree.charters`), which a path with a dot or a slash in it cannot be.
//
// A tree's sha is the identity of a directory's contents, so two refs that differ only outside
// the directory carry the same one, and a squash or a rebase of the same change keeps it. That
// is what an approval of a repository's charters keys on (git-issue j9vbwvjah12krs9tqsn0).
func (g *Git) AddTree(spec string) error {
	name, path, named := strings.Cut(strings.TrimSpace(spec), "=")
	if !named {
		path, name = name, ""
	}
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return fmt.Errorf("-tree %q: want [<name>=]<path>", spec)
	}
	if name == "" {
		name = path[strings.LastIndex(path, "/")+1:]
	}
	if strings.ContainsAny(name, "./ \t") {
		return fmt.Errorf("-tree %q: the name %q is one plain word, so a charter can reference it; say <name>=<path>", spec, name)
	}
	for _, t := range g.trees {
		if t.name == name {
			return fmt.Errorf("-tree %q: %q is already the name of %s", spec, name, t.path)
		}
	}
	g.trees = append(g.trees, tree{name: name, path: path})
	return nil
}

// AddRepo registers a repository under id (defaults to the dir's base name) and returns the
// id used. Re-adding an id repoints it.
func (g *Git) AddRepo(id, dir string) string { return g.repos.add(id, dir) }

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
	id, err := g.repos.addMirror(ctx, id, remote, dir)
	if err != nil {
		return "", fmt.Errorf("git: %w", err)
	}
	return id, nil
}

// Root is the owned slice.
func (g *Git) Root() string { return g.root }

// Owns is the ownership claim an install checks against the other installed sources: ONE PLACE
// PER REPOSITORY, `git.<repo>`, with no glob in it.
//
// It is wider than a reflection's Covers, and the difference is real rather than sloppy. A
// cover says what one pass is complete for — the mutable kinds, since history is kept — while
// this says what no other source may write, which is the whole of a repo's place, commits
// included. Two sources reflecting one repo would fight over its commits just as surely as over
// its branches.
//
// A literal place is also what the install takes an owner record at (versed's addSource), so
// everything this source writes under a repository is owned through one record rather than one
// per ref.
func (g *Git) Owns() []Own {
	ids := g.repos.ids()
	out := make([]Own, 0, len(ids))
	for _, id := range ids {
		out = append(out, Own{At: g.RepoRef(id).String()})
	}
	return out
}

// RepoRef is the place one repository's entities stand under, in this source's root — what a
// hint names, since a reconcile of it reconciles that repository and a per-repo pass is
// content-addressed and subsumes finer targeting. A hook this source installs has to name it in
// THIS root: a hint at `git.x` reaches nothing when the source writes `mirror.x`.
func (g *Git) RepoRef(id string) entity.Ref {
	return entity.RefAt(g.root, id)
}

// RepoRef is a repository's place in the default root, which is what a hint for a git source
// registered without a root of its own names.
func RepoRef(id string) entity.Ref {
	return entity.RefAt(GitRoot, id)
}

// hookMarker identifies the hook verse installs, so we only ever overwrite/remove our own.
const hookMarker = "# verse git Source hook"

// InstallHook writes a reference-transaction hook into repo id's git dir that POSTs a HINT to
// verse on every ref change — the git-native "webhook". It refuses to clobber a pre-existing
// reference-transaction hook that isn't ours. Idempotent for ours.
//
// hintURL is the registration's hint route, `<verse>/io/<name>/hint`. The ref is
// appended HERE, because which ref targets a repo is git's knowledge and not the route's: a
// reconcile of `git.<id>` is a reconcile of that repo. verse never parses this delivery —
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
		fmt.Sprintf(`curl -fsS -X POST "%s?at=%s" >/dev/null 2>&1 || true`+"\n",
			hintURL, url.QueryEscape(g.RepoRef(id).String()))
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

func (g *Git) dirOf(id string) (string, bool) { return g.repos.dirOf(id) }

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

// Reflect reads the repositories and returns what git.* should hold. A zero ref reads every
// registered repo (a full resync); a ref at or under `git.<repo>` reads just that repo (the
// targeted pass a hint asks for). Per-repo reflection is content-addressed, so it subsumes finer
// targeting — there is no cursor to keep and no missed-event window.
//
// It writes nothing, and the answer is scoped by what it Covers: `git.<repo>.ref.*` and
// `git.<repo>.head` for each repo it actually read. Commits are carried and covered by NOTHING,
// which is how "keep history" is said — they fall out of the bounded tip window and stay
// standing.
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

// targets is the repo id→repo set a reconcile should touch: all repos for a ref that names none,
// else the single repo whose place the ref is at or under — the second level of the address.
func (g *Git) targets(ref entity.Ref) map[string]repo {
	segs := ref.Segs()
	if len(segs) < 2 || segs[0] != g.root {
		return g.repos.all()
	}
	id := segs[1]
	if r, ok := g.repos.get(id); ok {
		return map[string]repo{id: r}
	}
	return nil
}

// reflectRepo reads one repository. Everything it returns stands under the repository's place —
// covers included — so one repo's pass never tombstones another's refs.
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
	// What this answer is COMPLETE for: the two mutable kinds, at this repo's place. Declared
	// BEFORE the read, and unconditionally: a repo whose every branch was deleted must still
	// cover `git.<repo>.ref.*`, or the refs it no longer has would stand forever.
	//
	// `git.<repo>.commit.*` is deliberately absent, and that absence IS the history rule. Commits
	// are carried in Entities and covered by nothing, so one that has fallen out of the bounded
	// tip window is simply not mentioned — and an unmentioned, uncovered entity stays.
	rn := Reflection{Covers: []entity.Pattern{
		Cover(g.root, id, "ref", "*"),
		Cover(g.root, id, "head"),
	}}

	// Refs: refs/heads, refs/tags, refs/remotes — git.<repo>.ref.<short> {repo, name, sha, type}.
	// That list is enforced in gitForEachRef rather than assumed; a mirror holds far more.
	refs, err := gitForEachRef(ctx, dir)
	if err != nil {
		return Reflection{}, err
	}
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	trees := gitTrees(ctx, dir, g.trees, refs)
	for _, name := range names {
		r := refs[name]
		p := entity.Obj{"repo": id, "name": name, "sha": r.sha, "type": r.typ}
		if t := trees[r.sha]; len(t) > 0 {
			p["tree"] = t
		}
		rn.Add(entity.RefAt(g.root, id, "ref", name), p)
	}

	// HEAD: branch, tip sha, dirty (and ahead/behind when an upstream is set).
	if head := gitHead(ctx, dir); head != nil {
		head["repo"] = id
		rn.Add(entity.RefAt(g.root, id, "head"), head)
	}

	// Recent commits: a bounded tip window. Scoped per-repo (git.<repo>.commit.<sha>) — a
	// sha is NOT a sound cross-repo key: repos have independent histories, "the same" commit
	// diverges under rebase/amend, and the object format (sha1→sha256) changes hashes. So a
	// commit belongs to its repo; two repos never share one entity.
	for _, c := range gitRecentCommits(ctx, dir, g.commitDepth) {
		p := entity.Obj{"repo": id, "sha": c.sha, "msg": c.msg, "author": c.author, "parents": c.parents}
		// `parents` is written even when it is empty, unlike the trailers below: a root commit
		// HAS no parents, and that is a fact about it rather than a field nobody filled in. It
		// also keeps the field one shape, so a predicate over it never has to ask which.
		//
		// Trailers are the one part of a commit BODY that reaches the verse, and only when
		// there are any — an empty map on every commit is noise in every payload.
		if len(c.trailers) > 0 {
			p["trailers"] = c.trailers
		}
		rn.Add(entity.RefAt(g.root, id, "commit", c.sha), p)
	}
	return rn, nil
}

// ── git plumbing (all shape-agnostic; run with the repo dir as cwd) ──────────

type refInfo struct{ sha, typ string }

// reflectedRefs is the ref space this source reflects: branches, tags, and remote-tracking
// branches. Everything else in a repository is somebody else's namespace.
//
// NAMED EXPLICITLY, because `for-each-ref` with no pattern is EVERY ref, and a mirror clone
// maps every ref the remote has (`+refs/*:refs/*`, see gitrepos.go). On GitHub that means
// `refs/pull/<n>/head` and `refs/pull/<n>/merge` — 4739 of them on one repo we mirror — plus
// `refs/git-issues/*`, which is git-issue's namespace and reflected properly by that source.
//
// Two costs, and the second is the one that showed. The slice grew by thousands of entities
// nobody asked for the moment a busy remote was added. And it never settled: GitHub RECREATES
// `refs/pull/<n>/merge` whenever the base branch moves, so every fetch after any push to main
// changed a batch of shas, and the store took writes on every pass forever — with no activity
// anybody watching the repository would call activity.
var reflectedRefs = []string{"refs/heads", "refs/tags", "refs/remotes"}

func gitForEachRef(ctx context.Context, dir string) (map[string]refInfo, error) {
	args := append([]string{"for-each-ref",
		"--format=%(refname:short) %(objectname) %(objecttype)"}, reflectedRefs...)
	out, err := gitCmd(ctx, dir, args...)
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

// gitTrees is the tree object of each path at each ref's sha, by sha then path, for the paths
// that exist there. One `cat-file --batch-check` for all of them: a mirror has hundreds of refs,
// and a process per ref per path is what a pass every minute cannot afford. A ref that is an
// annotated tag peels to its commit, as `<sha>:<path>` does.
func gitTrees(ctx context.Context, dir string, paths []tree, refs map[string]refInfo) map[string]map[string]string {
	if len(paths) == 0 || len(refs) == 0 {
		return nil
	}
	// ONE ORDER, held in a slice: the answers come back in the order asked, and the asking and
	// the reading must walk the same sequence, which two walks of a map do not.
	seen := map[string]bool{}
	var shas []string
	for _, r := range refs {
		if !seen[r.sha] {
			seen[r.sha] = true
			shas = append(shas, r.sha)
		}
	}
	sort.Strings(shas)
	var in strings.Builder
	for _, sha := range shas {
		for _, p := range paths {
			fmt.Fprintf(&in, "%s:%s\n", sha, p.path)
		}
	}
	out, err := gitCmdIn(ctx, dir, in.String(), "cat-file", "--batch-check=%(objectname) %(objecttype) %(rest)")
	if err != nil {
		return nil // a read that fails leaves the field off, as a path that is not there does
	}
	trees := map[string]map[string]string{}
	// A line for an object that is not there reads `<sha>:<path> missing`, and nothing of the
	// format above: the directory is not at that ref, which is the empty tree. A path that is
	// something other than a directory is left off.
	lines := nonEmptyLines(out)
	i := 0
	for _, sha := range shas {
		for _, p := range paths {
			if i >= len(lines) {
				return trees
			}
			f := strings.Fields(lines[i])
			i++
			obj := ""
			switch {
			case len(f) >= 2 && f[1] == "tree":
				obj = f[0]
			case len(f) >= 2 && f[len(f)-1] == "missing":
				obj = EmptyTree
			default:
				continue
			}
			if trees[sha] == nil {
				trees[sha] = map[string]string{}
			}
			trees[sha][p.name] = obj
		}
	}
	return trees
}

// gitHead returns the git.<repo>.head payload, or nil for an unborn repo (no HEAD sha yet).
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

type commitInfo struct {
	sha, msg, author string
	// A LIST, exploded from git's own space-separated `%P`. A merge commit has two of them and
	// a root commit none, so the joined string was a set written as text: `!key()` — the
	// operator that says "this element is in this list", in a match and in a patch alike —
	// cannot read it, and the workarounds are worse than the shape. `!glob "*<sha>*"` is a
	// substring test that would match a sha it was never given, and comparing the whole field
	// works only while there is exactly one parent, which is to say until the first merge.
	parents  []string
	trailers map[string]string
}

func gitRecentCommits(ctx context.Context, dir string, depth int) []commitInfo {
	// Unit-separated fields (0x1f) so subjects with spaces survive the split, and NUL-separated
	// RECORDS (-z) so the raw message can be one of them.
	//
	// The record separator is what changed when trailers arrived, and it had to. `%B` is
	// multi-line, and a line-per-commit scan turns three commits into seventy-nine lines and
	// then drops all of them for having the wrong field count. `-z` costs nothing and the
	// alternative — asking git for named trailers with `%(trailers:key=…)` — cannot serve
	// "whatever this commit declared", because it makes the caller name the keys first.
	out, err := gitCmd(ctx, dir, "log", "-z", "-n", strconv.Itoa(depth),
		"--format=%H%x1f%s%x1f%an%x1f%P%x1f%B")
	if err != nil {
		return nil // no commits
	}
	var cs []commitInfo
	for _, rec := range strings.Split(out, "\x00") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		// SplitN with the raw message LAST, so a message containing 0x1f cannot shift the
		// fields before it — a commit body is arbitrary text and one of them will.
		f := strings.SplitN(rec, "\x1f", 5)
		if len(f) != 5 {
			continue
		}
		// Fields rather than Split, so a root commit's empty `%P` is an empty list rather
		// than a list holding one empty string.
		cs = append(cs, commitInfo{sha: f[0], msg: f[1], author: f[2],
			parents: strings.Fields(f[3]), trailers: parseTrailers(f[4])})
	}
	return cs
}

// parseTrailers extracts a commit's trailer block — the `Key: value` lines git's own
// `interpret-trailers` reads, which is where `Tier:`, `Co-Authored-By:` and this tree's own
// `Breaks:` live.
//
// It is a deliberate SUBSET of git's heuristic, and stricter in one way: git accepts a final
// paragraph in which only some lines parse, and this requires all of them. That trades a few
// real trailer blocks — one stray prose line at the end of an otherwise-trailer paragraph — for
// not lifting arbitrary prose into an entity payload.
//
// It cannot be made exact, and that is a property of trailers rather than of this function. A
// trailer block is a heuristic over the last paragraph, so a commit whose body simply ENDS with
// something shaped like `Status: proposal` yields `Status` as a trailer, in git's parser too.
// Which is the argument for the convention living in the key NAMESPACE rather than in a curated
// allowlist here: a charter matching `Verse-Task` is safe, one matching bare `Status` never was.
// This source has no opinion about which keys are meaningful, because it cannot have a better
// one than the repository it is reflecting.
func parseTrailers(msg string) map[string]string {
	lines := strings.Split(strings.TrimRight(msg, "\n"), "\n")
	// The last paragraph: everything after the final blank line.
	start := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			start = i + 1
			break
		}
	}
	if start == 0 {
		return nil // a one-paragraph message is a subject, not a trailer block
	}
	out := map[string]string{}
	last := ""
	for _, line := range lines[start:] {
		// A continuation line — leading whitespace — belongs to the trailer above it.
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if last == "" {
				return nil
			}
			out[last] += " " + strings.TrimSpace(line)
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || !trailerKey(key) {
			return nil // one line that is not a trailer, and this paragraph is prose
		}
		if _, seen := out[key]; seen {
			// A repeated key — Co-Authored-By is the common one — joined with a space, which
			// is what git's own `%(trailers:…,separator=…)` does and what makes the result a
			// scalar a charter can glob rather than a list it has to reach into.
			out[key] += " "
		}
		out[key] += strings.TrimSpace(value)
		last = key
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// trailerKey reports whether s can be a trailer key: a token, no spaces. Git is looser about
// what it accepts; the point here is only to reject the prose that a bare `Cut` on ":" admits —
// "So Op stays set" has a colon in it somewhere often enough to matter.
func trailerKey(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// gitCmd runs one git command in dir and returns its stdout, with git's own stderr folded into
// the error.
//
// The folding is what makes a deployed mirror debuggable: an ExitError renders as "exit status
// 128", and that is the whole of what a failed clone of a private remote would otherwise write
// into the registration's lastError. (cmd.Output fills ExitError.Stderr only while Stderr is
// nil, and setting it — which is what captures the message — is exactly what empties that.)
func gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	return gitCmdIn(ctx, dir, "", args...)
}

// gitCmdIn is gitCmd with stdin, for the plumbing that takes its questions there.
func gitCmdIn(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
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
