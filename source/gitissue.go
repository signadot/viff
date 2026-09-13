package source

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/cmd/git-issue/issuelib"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// GitIssueSystem is the entity slice a git-issue Source owns and is the single writer of.
const GitIssueSystem = "git-issue"

// GitIssue reflects the issues `git issue` keeps INSIDE a git repository into the
// git-issue.* slice — `git-issue.<repo>.issue.<id>` per issue and `git-issue.<repo>.repo` for
// the tracker's counts, the repository a level of the address as it is for git — read-only reflection, the inbound half of a Source (docs/sources.md).
//
// It is a separate source from Git rather than more kinds on it, because the two answer
// different questions about the same directory. Git reflects what the repository IS — refs,
// heads, commits — and git-issue reflects what somebody FILED in it. An operator who wants
// branch state and no issue tracker, or the tracker and not fifty commits a pass, says so by
// running one program and not the other; folding them together would make that a flag.
//
// Storage is git refs, which is what makes this reflectable at all with no service to call:
// `refs/issues/<xidr>` is an open issue and `refs/closed/<xidr>` a closed one, each pointing
// at a commit whose tree holds `meta.tony` (the issue struct) and `description.md` (title on
// the first line, body under it). The ref NAMESPACE is status — closing moves the ref rather
// than rewriting it — so status needs no field read to establish. See issuelib's package doc.
//
// It does not use issuelib.GitStore, and that is deliberate rather than an oversight. That
// store drives git through the process's working directory and holds no path of its own — its
// own doc says callers must not run two against different repositories concurrently — which is
// exactly what a server reflecting several repositories for a concurrent caller would do. The
// plumbing here is the same plumbing git.go already runs, with the repository as an argument.
// What IS taken from issuelib is the part that is pure: the Issue struct, so the schema stays
// upstream's, and the ref-name helpers.
type GitIssue struct {
	repos repoSet
	// bodies reflects description.md's body under the title. Off makes the slice an index of
	// issue STATE — which is what a rule matches on — rather than a copy of their prose.
	bodies bool
	// system is the slice this source writes, GitIssueSystem unless an operator said
	// otherwise. Set once before serving and read without a lock thereafter.
	system string
}

// NewGitIssue returns an empty git-issue Source; register repositories with AddRepo or
// AddMirror.
func NewGitIssue() *GitIssue {
	return &GitIssue{repos: newRepoSet(), bodies: true, system: GitIssueSystem}
}

// SetSystem points this source at a slice other than `git-issue.*`. Call it before serving.
// The system is a segment of the address and of the route that serves the registration, so
// neither punctuation survives.
func (g *GitIssue) SetSystem(system string) error {
	if system == "" {
		return nil
	}
	if strings.ContainsAny(system, ":/") {
		return fmt.Errorf("git-issue: system %q may not contain : or / — it is a segment of the "+
			"address (verse.sources.<system>.<name>) and of the route that serves it", system)
	}
	g.system = system
	return nil
}

// SetBodies chooses whether an issue carries its description body.
func (g *GitIssue) SetBodies(on bool) { g.bodies = on }

// AddRepo registers a repository under id (defaults to the dir's base name) and returns the
// id used. Re-adding an id repoints it.
func (g *GitIssue) AddRepo(id, dir string) string { return g.repos.add(id, dir) }

// AddMirror registers a MIRROR of remote at dir, cloning it if dir does not already hold one.
//
// A mirror clone maps every ref, so it carries `refs/issues/*` and `refs/closed/*` like any
// other — which is what makes the tracker reflectable from a pod that has no repository. It is
// also the ONLY way this source sees a `git issue push` from somebody else's clone: issues
// travel as refs, so a fetch is how they arrive.
func (g *GitIssue) AddMirror(ctx context.Context, id, remote, dir string) (string, error) {
	id, err := g.repos.addMirror(ctx, id, remote, dir)
	if err != nil {
		return "", fmt.Errorf("git-issue: %w", err)
	}
	return id, nil
}

// System is the owned slice.
func (g *GitIssue) System() string { return g.system }

// Owns is the ownership claim an install checks against the other installed sources: one place
// per repository, `git-issue.<repo>`, for the reason git's Owns spells out.
func (g *GitIssue) Owns() []Own {
	ids := g.repos.ids()
	out := make([]Own, 0, len(ids))
	for _, id := range ids {
		out = append(out, Own{At: entity.RefAt(g.system, id).String()})
	}
	return out
}

// RepoRef is the place one repository's tracker stands under: a reconcile of it reconciles
// that repository's whole tracker, since the pass enumerates refs and is complete. It is what a
// hint names.
//
// This source installs no git hook, unlike Git, and the reason is that there is only one
// `reference-transaction` hook per repository — so a source that claimed it would be taking it
// from whoever had it, which for the ordinary deployment (both sources over one repository) is
// the git source. Convergence is therefore the resync, and an operator who wants it faster
// adds a line to their OWN hook posting to the hint route with this ref. docs/reference/
// sourced.md carries the line.
func (g *GitIssue) RepoRef(id string) entity.Ref {
	return entity.RefAt(g.system, id)
}

var _ Source = (*GitIssue)(nil)

// Reflect reads the registered repositories' trackers and returns what git-issue.* should
// hold. A zero ref reads every repository; a ref at or under `git-issue.<repo>` reads just that
// one.
func (g *GitIssue) Reflect(ctx context.Context, ref entity.Ref) (Reflection, error) {
	var out Reflection
	targets := g.targets(ref)
	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Strings(ids) // one order, so two passes over the same repositories answer the same way
	for _, id := range ids {
		rn, err := g.reflectRepo(ctx, id, targets[id])
		if err != nil {
			return Reflection{}, fmt.Errorf("git-issue reflect %s: %w", id, err)
		}
		out.Covers = append(out.Covers, rn.Covers...)
		out.Entities = append(out.Entities, rn.Entities...)
	}
	return out, nil
}

// targets is the repo id→repo set a reconcile should touch: all of them for a ref that names
// none, else the single repository whose place the ref is at or under.
func (g *GitIssue) targets(ref entity.Ref) map[string]repo {
	segs := ref.Segs()
	if len(segs) < 2 || segs[0] != g.system {
		return g.repos.all()
	}
	id := segs[1]
	if r, ok := g.repos.get(id); ok {
		return map[string]repo{id: r}
	}
	return nil
}

// reflectRepo reads one repository's tracker whole. Everything it returns stands under the
// repository's place — covers included — so one repository's pass never tombstones another's.
//
// ONE COVER PER KIND, complete, and the contrast with the GitHub connector is the decision
// worth stating. GitHub covers issues twice — the open set complete, the closed set as a
// window — because the closed back catalogue is unbounded and every page of it costs a request
// against a budget. Here the enumeration is `git for-each-ref` over a local directory: it lists
// every issue the repository has, open and closed, for one process and no network. So the
// answer really is complete for the whole slice, a closed issue keeps its entity for as long as
// its ref exists, and one whose ref is deleted is tombstoned because it is genuinely gone.
func (g *GitIssue) reflectRepo(ctx context.Context, id string, rp repo) (Reflection, error) {
	dir := rp.dir
	// A mirror's read starts with a fetch, and a FAILED one fails the pass rather than
	// reflecting what was fetched last time — stale-and-silent being the worse failure, as it
	// is for git: a source whose credential expired last Tuesday would otherwise look exactly
	// like a tracker where nobody has filed anything.
	if rp.remote != "" {
		if _, err := gitCmd(ctx, dir, "fetch", "--prune"); err != nil {
			return Reflection{}, fmt.Errorf("fetching %s: %w", rp.remote, err)
		}
	}
	// Declared BEFORE the read and unconditionally: a repository whose every issue was deleted
	// must still cover `git-issue.<repo>.issue.*`, or the ones it no longer has would stand
	// forever.
	rn := Reflection{Covers: []entity.Pattern{
		Cover(g.system, id, "repo"),
		Cover(g.system, id, "issue", "*"),
	}}

	refs, err := gitIssueRefs(ctx, dir)
	if err != nil {
		return Reflection{}, err
	}

	// Every blob this pass needs, read in ONE `git cat-file --batch`. The shape that suggests
	// itself — two `git show`s per issue — is two processes per issue, so a tracker with two
	// hundred issues would fork four hundred times a pass to read a few kilobytes.
	specs := make([]string, 0, 3*len(refs))
	for _, ref := range refs {
		specs = append(specs, ref+":meta.tony", ref+":description.md", ref+":discussion")
	}
	blobs, err := gitCatFile(ctx, dir, specs)
	if err != nil {
		return Reflection{}, err
	}
	// THE DISCUSSION, which is where the activity a charter cannot see any other way lives: an
	// issue written already triaged, a decision taken in a comment, a "this is underway". The
	// `discussion` tree came back in the batch above as a tree object; its entries name the
	// comment files, and one more batch reads those. Two round trips per pass however many
	// issues carry comments, rather than a process per issue.
	comments := map[string][]string{}
	var commentSpecs []string
	for _, ref := range refs {
		for _, name := range treeEntries(blobs[ref+":discussion"]) {
			if !strings.HasSuffix(name, ".md") {
				continue
			}
			comments[ref] = append(comments[ref], name)
			commentSpecs = append(commentSpecs, ref+":discussion/"+name)
		}
	}
	commentBlobs, err := gitCatFile(ctx, dir, commentSpecs)
	if err != nil {
		return Reflection{}, err
	}

	var open, closed int
	for _, ref := range refs {
		xidr, err := issuelib.XIDRFromRef(ref)
		if err != nil {
			continue // not an issue ref; for-each-ref was asked for two prefixes, so unreachable
		}
		meta, ok := blobs[ref+":meta.tony"]
		if !ok {
			// A ref with no meta.tony is not an issue this source can describe. Skipped rather
			// than failed: one malformed ref should not cost the pass, and the covers above
			// mean a skipped issue is tombstoned rather than frozen — which is the honest
			// answer, since nothing here can say what it holds.
			continue
		}
		// meta.tony is ENRICHMENT, and this is where that shows. The ref already gives the id
		// and the status, and description.md the title, so an issue whose meta will not decode
		// is still a real issue that a charter can see — while failing the pass over it would
		// take the whole slice out until somebody fixed one old file.
		//
		// Which is not hypothetical: `git issue`'s first iteration numbered issues, and those
		// refs carry `id: 5` where the struct wants a string. issuelib's doc says such issues
		// "remain reachable on read" until `git issue migrate` rewrites them, and upstream's
		// own reader does not manage it — GetByRef errors and List silently returns 172 of 173.
		// So verse reflects what it can rather than inheriting that.
		var issue issuelib.Issue
		metaErr := codec.Unmarshal(meta, &issue, codec.Tony)
		// The ref namespace is authoritative for status, per issuelib: closing MOVES the ref,
		// and meta.tony's own status field is a copy that a hand-edited tree could disagree
		// with.
		status := issuelib.StatusFromRef(ref)
		if status == "closed" {
			closed++
		} else {
			open++
		}
		title, body := splitDescription(blobs[ref+":description.md"])
		p := entity.Obj{
			"repo":   id,
			"id":     xidr,
			"title":  title,
			"status": status,
		}
		if metaErr != nil {
			// Said on the entity rather than swallowed: a thin issue with no explanation is
			// indistinguishable from one nobody labelled, and this is the field that tells an
			// operator which — and that `git issue migrate` is the fix.
			p["metaError"] = metaErr.Error()
		}
		addTime(p, "createdAt", issue.Created)
		addTime(p, "updatedAt", issue.Updated)
		addStrings(p, "labels", issue.Labels)
		addStrings(p, "commits", issue.Commits)
		addStrings(p, "branches", issue.Branches)
		addStrings(p, "related", issue.RelatedIssues)
		addStrings(p, "blocks", issue.Blocks)
		addStrings(p, "blockedBy", issue.BlockedBy)
		addStrings(p, "duplicates", issue.Duplicates)
		// closedBy only where it can be true. It comes from meta.tony, which KEEPS the last
		// closure across a reopen, so an open issue that was once closed would otherwise read
		// `status: open` beside the sha that closed it — a record disagreeing with itself,
		// and the same fact reached two ways is exactly what made the duplicate-ref bug hard
		// to see (the entities that were wrong were wrong in only one of their two fields).
		if status == "closed" && issue.ClosedBy != nil && *issue.ClosedBy != "" {
			p["closedBy"] = *issue.ClosedBy
		}
		if g.bodies {
			p["body"] = body // "" for an issue that is its title alone, for addStrings' reason
		}
		// The comments, as `git issue comment` writes them: one file each under discussion/,
		// named by its instant. The count and the last instant ride whether or not bodies do,
		// since "somebody said something since" is state and not prose; the text is prose.
		names := comments[ref]
		sort.Strings(names)
		p["comments"] = len(names)
		if len(names) > 0 {
			if at, ok := commentInstant(names[len(names)-1]); ok {
				p["lastCommentAt"] = at
			}
		}
		if g.bodies {
			discussion := &ir.Node{Type: ir.ArrayType}
			for _, name := range names {
				c := entity.Obj{}
				if at, ok := commentInstant(name); ok {
					c["at"] = at
				}
				c["text"] = commentText(commentBlobs[ref+":discussion/"+name])
				discussion.Values = append(discussion.Values, c.Payload())
			}
			p["discussion"] = discussion
		}
		rn.Add(entity.RefAt(g.system, id, "issue", xidr), p)
	}

	rn.Add(entity.RefAt(g.system, id, "repo"), entity.Obj{
		"repo": id, "open": open, "closed": closed, "issues": open + closed,
	})
	return rn, nil
}

// treeEntries is the names in a tree object as `git cat-file --batch` hands one back: entries
// of `<mode> <name>\0<20-byte id>`, in tree order. Nil for anything that is not a tree.
func treeEntries(tree []byte) []string {
	var names []string
	for len(tree) > 0 {
		nul := bytes.IndexByte(tree, 0)
		if nul < 0 || nul+21 > len(tree) {
			return names
		}
		_, name, ok := bytes.Cut(tree[:nul], []byte(" "))
		if !ok {
			return names
		}
		names = append(names, string(name))
		tree = tree[nul+21:]
	}
	return names
}

// commentInstant is when a comment was made, from its file's name — `20260912T213534Z-<hash>.md`
// — spelled the way every other instant here is.
func commentInstant(name string) (string, bool) {
	stamp, _, _ := strings.Cut(strings.TrimSuffix(name, ".md"), "-")
	t, err := time.Parse("20060102T150405Z", stamp)
	if err != nil {
		return "", false
	}
	return t.UTC().Format(time.RFC3339), true
}

// commentText is a comment's body without the `<!-- instant -->` line `git issue comment`
// writes above it.
func commentText(b []byte) string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	if strings.HasPrefix(s, "<!--") {
		if _, rest, ok := strings.Cut(s, "-->"); ok {
			s = rest
		}
	}
	return strings.TrimSpace(s)
}

// splitDescription reads description.md as git-issue writes it: the title is the first line
// with any leading `# ` stripped, and the body is whatever follows.
func splitDescription(b []byte) (title, body string) {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	first, rest, _ := strings.Cut(s, "\n")
	title = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(first), "# "))
	return title, strings.TrimSpace(rest)
}

// addTime writes an RFC3339 string, and nothing at all for a zero time — the spelling the
// other connectors' timestamps already have, since theirs arrive as strings from an API.
func addTime(p entity.Obj, key string, t time.Time) {
	if !t.IsZero() {
		p[key] = t.UTC().Format(time.RFC3339)
	}
}

// addStrings writes a list field, EMPTY INCLUDED. A charter copies an issue's facts onto a row
// of its own by reference, and an absent reference fails the firing (docs/trigger-model.md), so
// an issue with no labels has `labels: []` rather than nothing where labels would be: every
// issue answers every question the tracker's shape can ask, and "none" is an answer.
func addStrings(p entity.Obj, key string, v []string) {
	if v == nil {
		v = []string{}
	}
	p[key] = v
}

// ── git plumbing ─────────────────────────────────────────────────────────────

// gitIssueRefs lists every issue ref in the repository, open and closed, sorted — one per
// issue, which is the part that takes work.
//
// `git issue` MOVES a ref to change status: closing takes refs/issues/<id> to
// refs/closed/<id>, reopening takes it back. In the repository somebody types in, an issue
// therefore has exactly one ref and its namespace IS its status.
//
// A MIRROR is not that repository. `git issue push` writes the new ref and does not delete
// the one it moved from, so a mirror accumulates both — 12 of 17 closed issues on the verse
// tracker carry a stale refs/issues alongside their refs/closed. Read naively that is one
// issue reflected twice, once open and once closed, with nothing deciding which lands: nine
// closed issues were reading `status: open` on staging, and one entity could say `status:
// open` while carrying the `closedBy` sha of the commit that closed it.
//
// Which of the two is current cannot be answered by the namespaces, because the ambiguity is
// symmetric: a stale close and a live REOPEN look identical — both leave the id in both
// namespaces. Preferring closed would fix today's nine and pin every reopened issue closed
// forever.
//
// The refs answer it themselves. They are commits on one chain — create, then close, then
// reopen, each built on the last — so the DESCENDANT is the current state, exactly, in either
// direction and with no clock involved. That is what this picks.
func gitIssueRefs(ctx context.Context, dir string) ([]string, error) {
	out, err := gitCmd(ctx, dir, "for-each-ref", "--format=%(refname)",
		"refs/issues/", "refs/closed/")
	if err != nil {
		return nil, err
	}
	refs := nonEmptyLines(out)
	sort.Strings(refs)
	return currentRefs(ctx, dir, refs), nil
}

// currentRefs reduces the ref list to one ref per issue, keeping the descendant when an id
// appears in both namespaces. Order is preserved, so the result is still sorted.
//
// A pair whose histories are UNRELATED keeps both — deliberately. It should not happen (they
// share a create commit), and if it does, the two refs are describing different objects that
// merely share an id; dropping one would be this function inventing an answer where it has
// none. Reflected twice, the later Add wins and the situation is at least visible.
func currentRefs(ctx context.Context, dir string, refs []string) []string {
	byID := map[string][]string{}
	for _, ref := range refs {
		xidr, err := issuelib.XIDRFromRef(ref)
		if err != nil {
			continue
		}
		byID[xidr] = append(byID[xidr], ref)
	}
	drop := map[string]bool{}
	for _, group := range byID {
		if len(group) != 2 {
			continue
		}
		a, b := group[0], group[1]
		// Identical commits carry no ordering, and every commit is its own ancestor — so
		// asking would answer "yes" both ways and the first test would win by accident.
		// It means somebody COPIED a ref rather than moving it (a real close commits its
		// metadata first, which is what makes the two differ), and nothing in the refs can
		// say which namespace is meant. Both stand, and the duplicate stays visible.
		if sameCommit(ctx, dir, a, b) {
			continue
		}
		switch {
		case isAncestor(ctx, dir, a, b):
			drop[a] = true // a is what b was built on: b is current
		case isAncestor(ctx, dir, b, a):
			drop[b] = true
		}
	}
	if len(drop) == 0 {
		return refs
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if !drop[ref] {
			out = append(out, ref)
		}
	}
	return out
}

// sameCommit reports whether two refs name one commit. A git failure answers false, which
// leaves the pair to the ancestry test below rather than deciding here.
func sameCommit(ctx context.Context, dir, a, b string) bool {
	x, err := gitCmd(ctx, dir, "rev-parse", a)
	if err != nil {
		return false
	}
	y, err := gitCmd(ctx, dir, "rev-parse", b)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(x)) == strings.TrimSpace(string(y))
}

// isAncestor reports whether a is an ancestor of b. A git failure answers false, which leaves
// both refs standing rather than guessing — the same choice unrelated histories get.
func isAncestor(ctx context.Context, dir, a, b string) bool {
	_, err := gitCmd(ctx, dir, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// gitCatFile reads several objects in ONE git invocation and answers spec→contents, leaving
// out whatever git reported missing.
//
// `git cat-file --batch` answers each requested spec in the order it was asked, as a header
// line — `<sha> <type> <size>`, or `<spec> missing` — followed by exactly size bytes and a
// newline. Nothing in the reply identifies WHICH spec a found object answers (the header
// carries the object's sha, not the name it was asked for), so the association is positional,
// which is why this reads one response per spec rather than parsing to exhaustion.
func gitCatFile(ctx context.Context, dir string, specs []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(specs))
	if len(specs) == 0 {
		return out, nil
	}
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(strings.Join(specs, "\n") + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	r := bufio.NewReader(bytes.NewReader(stdout))
	for _, spec := range specs {
		header, err := r.ReadString('\n')
		if err != nil {
			// git stopped answering early. Everything read so far is still good, and the
			// caller treats a spec it did not get back as absent.
			return out, nil
		}
		fields := strings.Fields(strings.TrimRight(header, "\n"))
		if len(fields) != 3 {
			continue // "<spec> missing", or "<spec> ambiguous" — no payload follows either
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(r, buf); err != nil {
			return out, nil
		}
		// The contents are followed by a newline that is not part of them.
		if _, err := r.Discard(1); err != nil {
			out[spec] = buf
			return out, nil
		}
		out[spec] = buf
	}
	return out, nil
}
