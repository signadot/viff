package source

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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
// warehouse: a process needing a diff follows the ref into the real repo (`git show`). Acting
// back on a repo (commit, tag, push) is the separate outbound executor path, not here.
type Git struct {
	mu          sync.Mutex
	repos       map[string]string // repo id -> working dir
	commitDepth int               // how many tip commits to reflect as git:commit:<sha>
}

// NewGit returns an empty git Source; register repositories with AddRepo.
func NewGit() *Git { return &Git{repos: map[string]string{}, commitDepth: 50} }

// AddRepo registers a repository under id (defaults to the dir's base name) and returns the
// id used. Re-adding an id repoints it.
func (g *Git) AddRepo(id, dir string) string {
	if id == "" {
		id = filepath.Base(filepath.Clean(dir))
	}
	g.mu.Lock()
	g.repos[id] = dir
	g.mu.Unlock()
	return id
}

// System is the owned slice.
func (g *Git) System() string { return GitSystem }

// Webhook maps a git-hook delivery to the ref(s) to reconcile. Git has no webhooks; the
// hint is a local reference-transaction hook (see InstallHook) that POSTs ?repo=<id>. Per
// the hint law it carries no authoritative data — it returns the git:head:<repo> marker that
// targets a reconcile of that repo, or nil (full resync) if no repo is named. It writes nothing.
func (g *Git) Webhook(r *http.Request) ([]entity.Ref, error) {
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		return nil, nil // full resync
	}
	return []entity.Ref{{System: GitSystem, Kind: "head", ID: repo}}, nil
}

// hookMarker identifies the hook verse installs, so we only ever overwrite/remove our own.
const hookMarker = "# verse git Source hook"

// InstallHook writes a reference-transaction hook into repo id's git dir that POSTs hookURL
// (the reflector's /hooks/git) on every ref change — the git-native "webhook". It refuses to
// clobber a pre-existing reference-transaction hook that isn't ours. Idempotent for ours.
func (g *Git) InstallHook(id, hookURL string) error {
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
	// committed. It only hints — the reconcile reads truth — so it forwards just the repo id.
	script := "#!/bin/sh\n" + hookMarker + "\n" +
		`[ "$1" = committed ] || exit 0` + "\n" +
		fmt.Sprintf(`curl -fsS -X POST "%s?repo=%s" >/dev/null 2>&1 || true`+"\n",
			hookURL, url.QueryEscape(id))
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
	return d, ok
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

// Reconcile converges git:* with the repositories. A zero ref reconciles every registered
// repo (a full resync); a git:* ref reconciles just the repo it names (the targeted reconcile
// a webhook asks for). Per-repo reconcile is idempotent and content-addressed, so it subsumes
// finer targeting; only actual changes are written, so a quiet repo emits no change events.
func (g *Git) Reconcile(ctx context.Context, store entity.Store, ref entity.Ref) error {
	for id, dir := range g.targets(ref) {
		if err := g.reconcileRepo(ctx, store, id, dir); err != nil {
			return fmt.Errorf("git reflect %s: %w", id, err)
		}
	}
	return nil
}

// targets is the repo id→dir set a reconcile should touch: all repos for a zero ref, else the
// single repo the ref belongs to (its id is the git:head:<repo> id, or the leading segment of
// a git:ref:<repo>/<name> id).
func (g *Git) targets(ref entity.Ref) map[string]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ref.System != GitSystem || ref.ID == "" {
		out := make(map[string]string, len(g.repos))
		for id, dir := range g.repos {
			out[id] = dir
		}
		return out
	}
	id := ref.ID
	if ref.Kind == "ref" {
		id, _, _ = strings.Cut(ref.ID, "/") // <repo>/<name>
	}
	if dir, ok := g.repos[id]; ok {
		return map[string]string{id: dir}
	}
	return nil
}

func (g *Git) reconcileRepo(ctx context.Context, store entity.Store, id, dir string) error {
	desired := map[entity.Ref]entity.Obj{}

	// Refs: refs/heads, refs/tags, refs/remotes — git:ref:<repo>/<short> {repo, name, sha, type}.
	refs, err := gitForEachRef(ctx, dir)
	if err != nil {
		return err
	}
	for name, r := range refs {
		desired[entity.Ref{System: GitSystem, Kind: "ref", ID: id + "/" + name}] = entity.Obj{"repo": id, "name": name, "sha": r.sha, "type": r.typ}
	}

	// HEAD: branch, tip sha, dirty (and ahead/behind when an upstream is set).
	if head := gitHead(ctx, dir); head != nil {
		head["repo"] = id
		desired[entity.Ref{System: GitSystem, Kind: "head", ID: id}] = head
	}

	// Recent commits: a bounded tip window. Scoped per-repo (git:commit:<repo>/<sha>) — a
	// sha is NOT a sound cross-repo key: repos have independent histories, "the same" commit
	// diverges under rebase/amend, and the object format (sha1→sha256) changes hashes. So a
	// commit belongs to its repo; two repos never share one entity.
	for _, c := range gitRecentCommits(ctx, dir, g.commitDepth) {
		desired[entity.Ref{System: GitSystem, Kind: "commit", ID: id + "/" + c.sha}] = entity.Obj{"repo": id, "sha": c.sha, "msg": c.msg, "author": c.author, "parents": c.parents}
	}

	g.converge(store, id, desired)
	return nil
}

// converge writes desired and tombstones this repo's removed refs. Everything is scoped by
// the `repo` field, so one repo's reconcile never touches another's slice. A ref whose sha is
// unchanged is skipped, so re-reflecting a quiet repo fires no triggers. Commits (repo-scoped
// too) are immutable within their repo — Put once when new, never re-Put, and never tombstoned
// (they fall out of the bounded tip window but the history record is kept).
func (g *Git) converge(store entity.Store, repo string, desired map[entity.Ref]entity.Obj) {
	current := map[entity.Ref]entity.Obj{} // this repo's entities (all kinds)
	if l, ok := store.(entity.Lister); ok {
		for _, e := range l.List(func(r entity.Ref) bool { return r.System == GitSystem }) {
			if entity.Str(e.Payload, "repo") == repo {
				current[e.Ref] = entity.ToObj(e.Payload)
			}
		}
	}
	for ref, p := range desired {
		if _, exists := current[ref]; exists {
			if ref.Kind == "commit" || payloadEqual(current[ref], p) {
				continue // immutable commit, or unchanged ref/head
			}
		}
		store.Put(ref, p.Payload())
	}
	for ref := range current {
		if ref.Kind == "commit" {
			continue // keep history
		}
		if _, want := desired[ref]; !want {
			store.Delete(ref) // a deleted branch/tag tombstones its git:ref entity
		}
	}
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

func gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n"), err
}

func nonEmptyLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// payloadEqual compares by stringified value, so a store round-trip that shifts numeric
// types (int↔float) doesn't read as a change.
func payloadEqual(a, b entity.Obj) bool {
	if len(a) != len(b) {
		return false
	}
	for k, bv := range b {
		if av, ok := a[k]; !ok || fmt.Sprint(av) != fmt.Sprint(bv) {
			return false
		}
	}
	return true
}
