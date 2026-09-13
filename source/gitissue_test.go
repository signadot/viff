package source_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/signadot/tony-format/go-tony/cmd/git-issue/issuelib"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/source"
)

// fileIssue writes a real issue with git-issue's OWN storage layer, in the repository dir, and
// answers with its XIDR.
//
// Upstream's writer rather than a hand-built tree, because that is what makes these tests
// worth running: they pin that verse reads what `git issue` actually writes, so a change to
// the tree layout, to meta.tony's field names or to the ref namespaces fails here rather than
// in somebody's tracker. issuelib.GitStore selects its repository by the process working
// directory and its own doc names t.Chdir as how a test does that — fine for one test, and
// exactly why the source itself does not use it (see GitIssue's doc).
// The description is composed the way `git issue create` composes it — "# <title>" then the
// body — and that detail is the reason the title parsing here is worth a test at all. Create
// takes a title argument but does not persist it: Issue.Title is `tony:"-"`, so it is absent
// from meta.tony, and the FIRST LINE OF description.md is the only place a title is stored.
// GetByRef reads it back exactly that way.
func fileIssue(t *testing.T, dir, title, body string, mutate func(*issuelib.Issue)) string {
	t.Helper()
	t.Chdir(dir)
	s := issuelib.NewGitStoreWithOutput(testLog{t})
	issue, err := s.Create(title, "# "+title+"\n\n"+body)
	if err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	if mutate != nil {
		mutate(issue)
		if err := s.Update(issue, "update", nil); err != nil {
			t.Fatalf("update %q: %v", title, err)
		}
	}
	return issue.ID
}

// commentOn writes a comment the way `git issue comment` does: one file under discussion/,
// named by its instant, an html comment carrying the instant above the text.
func commentOn(t *testing.T, dir, xidr, stamp, text string) {
	t.Helper()
	t.Chdir(dir)
	s := issuelib.NewGitStoreWithOutput(testLog{t})
	issue, _, err := s.Get(xidr)
	if err != nil {
		t.Fatalf("get %s: %v", xidr, err)
	}
	name := "discussion/" + stamp + "-" + xidr[:8] + ".md"
	if err := s.Update(issue, "comment", map[string]string{name: "<!-- " + stamp + " -->\n\n" + text + "\n"}); err != nil {
		t.Fatalf("comment on %s: %v", xidr, err)
	}
}

// closeIssue closes one the way `git issue close` does — by MOVING its ref, which is what
// makes the namespace authoritative for status.
func closeIssue(t *testing.T, dir, xidr string) {
	t.Helper()
	t.Chdir(dir)
	s := issuelib.NewGitStoreWithOutput(testLog{t})
	if err := s.MoveRef(issuelib.RefForXIDR(xidr), issuelib.ClosedRefForXIDR(xidr)); err != nil {
		t.Fatalf("closing %s: %v", xidr, err)
	}
}

type testLog struct{ t *testing.T }

func (w testLog) Write(b []byte) (int, error) { w.t.Logf("git-issue: %s", b); return len(b), nil }

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// reflectIssues runs one pass and answers ref-string → payload object, without a store: the
// tests that are about the ANSWER do not need converge in the way.
func reflectIssues(t *testing.T, gi *source.GitIssue, ref entity.Ref) (map[entity.Ref]entity.Obj, source.Reflection) {
	t.Helper()
	rn, err := gi.Reflect(context.Background(), ref)
	if err != nil {
		t.Fatalf("Reflect(%s): %v", ref, err)
	}
	out := map[entity.Ref]entity.Obj{}
	for _, e := range rn.Entities {
		out[e.Ref] = entity.ToObj(e.Payload)
	}
	return out, rn
}

// The git-issue Source reflects what `git issue` stores: an issue per ref, status from the ref
// NAMESPACE rather than from meta.tony, labels and commit links carried, and a repo summary
// counting both halves. Multi-repo, like the git source it sits beside.
//
// Driven through the Reflector, which is where converge lives, so this exercises the path a
// Run and a hint take rather than just the source's return value.
func TestGitIssueReconcile(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	store := entitytest.Store(t)

	dirA, dirB := t.TempDir(), t.TempDir()
	initRepo(t, dirA)
	initRepo(t, dirB)
	open := fileIssue(t, dirA, "the wire did not answer", "body of the first", func(i *issuelib.Issue) {
		i.Labels = []string{"agent", "codegen"}
		i.Commits = []string{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}
	})
	shut := fileIssue(t, dirA, "already fixed upstream", "", nil)
	elsewhere := fileIssue(t, dirB, "another repo entirely", "", nil)
	closeIssue(t, dirA, shut)
	commentOn(t, dirA, open, "20260912T213534Z", "underway on a branch")
	commentOn(t, dirA, open, "20260912T213630Z", "landed; closing next")

	gi := source.NewGitIssue()
	gi.AddRepo("A", dirA)
	gi.AddRepo("B", dirB)
	rf := source.NewReflector(gi, store, 0)
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	get := func(segs ...string) entity.Obj {
		ref := entity.RefAt(append([]string{source.GitIssueSystem}, segs...)...)
		e, ok, _ := store.Get(ref)
		if !ok {
			t.Fatalf("missing %s", ref)
		}
		return entity.ToObj(e.Payload)
	}

	o := get("A", "issue", open)
	if o["status"] != "open" || o["title"] != "the wire did not answer" || o["repo"] != "A" {
		t.Fatalf("open issue = %+v", o)
	}
	if o["body"] != "body of the first" {
		t.Errorf("body = %v", o["body"])
	}
	if o["labels"] == nil {
		t.Error("labels are how a charter selects an issue; none carried")
	}
	// The one with nothing to say answers every question anyway — an absent field would fail
	// a charter's reference to it, and "none" is an answer.
	if q := get("A", "issue", shut); q["labels"] == nil || q["commits"] == nil || q["comments"] != int64(0) && q["comments"] != 0 {
		t.Errorf("a bare issue leaves fields absent: labels=%v commits=%v comments=%v", q["labels"], q["commits"], q["comments"])
	} else if _, has := q["discussion"]; !has {
		t.Errorf("a bare issue carries no discussion list: %v", q)
	} else if _, has := q["body"]; !has {
		t.Errorf("a bare issue carries no body: %v", q)
	}
	if o["commits"] == nil {
		t.Error("no commits carried, so `git issue link` is invisible to a charter")
	}
	if o["id"] != open {
		t.Errorf("id = %v, want %s", o["id"], open)
	}
	// The discussion: how many, when the last was, and the text under bodies — the activity a
	// charter otherwise cannot see.
	if o["comments"] != int64(2) && o["comments"] != 2 {
		t.Errorf("comments = %v (%T), want 2", o["comments"], o["comments"])
	}
	if o["lastCommentAt"] != "2026-09-12T21:36:30Z" {
		t.Errorf("lastCommentAt = %v", o["lastCommentAt"])
	}
	e, _, _ := store.Get(entity.RefAt(source.GitIssueSystem, "A", "issue", open))
	discussion := entity.Get(e.Payload, "discussion")
	if discussion == nil || len(discussion.Values) != 2 {
		t.Fatalf("discussion = %v, want two comments", discussion)
	}
	if got := entity.Str(discussion.Values[0], "text"); got != "underway on a branch" {
		t.Errorf("the first comment's text = %q", got)
	}
	if got := entity.Str(discussion.Values[1], "at"); got != "2026-09-12T21:36:30Z" {
		t.Errorf("the second comment's instant = %q", got)
	}

	// Closed: the ref namespace says so, and the entity is KEPT rather than windowed away —
	// the enumeration is local and complete, unlike the GitHub connector's.
	if c := get("A", "issue", shut); c["status"] != "closed" {
		t.Errorf("closed issue status = %v, want closed (from the ref namespace)", c["status"])
	}

	// Multi-repo: B's issue is B's, and A's summary counts only A's two.
	get("B", "issue", elsewhere)
	if _, ok, _ := store.Get(entity.RefAt(source.GitIssueSystem, "A", "issue", elsewhere)); ok {
		t.Error("B's issue leaked into A")
	}
	if s := get("A", "repo"); s["open"] != int64(1) || s["closed"] != int64(1) || s["issues"] != int64(2) {
		t.Errorf("repo:A summary = %+v, want open 1 closed 1 issues 2", s)
	}

	// Deleting a ref tombstones the entity: the cover is complete, so absence is deletion.
	gitRun(t, dirA, "update-ref", "-d", issuelib.ClosedRefForXIDR(shut))
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("reconcile after delete: %v", err)
	}
	if _, ok, _ := store.Get(entity.RefAt(source.GitIssueSystem, "A", "issue", shut)); ok {
		t.Error("a deleted issue ref must tombstone its entity")
	}
	if _, ok, _ := store.Get(entity.RefAt(source.GitIssueSystem, "B", "issue", elsewhere)); !ok {
		t.Error("A's reconcile tombstoned B's issue")
	}
}

// A pass covers each repository's own issues and nothing else, so one repository's reconcile
// never tombstones another's — and an EMPTY tracker still covers, or deleting the last issue
// would leave it standing forever.
func TestGitIssueCoversPerRepoEvenWhenEmpty(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	initRepo(t, dir)

	gi := source.NewGitIssue()
	gi.AddRepo("A", dir)

	_, rn := reflectIssues(t, gi, entity.Ref{})
	if len(rn.Covers) != 2 {
		t.Fatalf("an empty tracker must still cover its slice; got %+v", rn.Covers)
	}
	// Each cover is at the repository's place, which is what keeps it from tombstoning another
	// repository's issues.
	for _, c := range rn.Covers {
		if !c.MatchRef(entity.RefAt(source.GitIssueSystem, "A", "repo")) &&
			!c.MatchRef(entity.RefAt(source.GitIssueSystem, "A", "issue", "x")) {
			t.Errorf("cover %+v is not at A's place", c.Path)
		}
		if c.MatchRef(entity.RefAt(source.GitIssueSystem, "B", "repo")) ||
			c.MatchRef(entity.RefAt(source.GitIssueSystem, "B", "issue", "x")) {
			t.Errorf("cover %+v reaches B, so it would tombstone another repository", c.Path)
		}
	}
	// The summary is the one entity a tracker with no issues has.
	if len(rn.Entities) != 1 || rn.Entities[0].Ref != entity.RefAt(source.GitIssueSystem, "A", "repo") {
		t.Fatalf("want just the repo summary, got %+v", rn.Entities)
	}
}

// A targeted reconcile reads only the repository the ref names — both spellings, since a hint
// may name the repo summary or an individual issue.
func TestGitIssueTargetedReconcile(t *testing.T) {
	needGit(t)
	dirA, dirB := t.TempDir(), t.TempDir()
	initRepo(t, dirA)
	initRepo(t, dirB)
	a := fileIssue(t, dirA, "in A", "", nil)
	fileIssue(t, dirB, "in B", "", nil)

	gi := source.NewGitIssue()
	gi.AddRepo("A", dirA)
	gi.AddRepo("B", dirB)

	for _, ref := range []entity.Ref{
		gi.RepoRef("A"),
		entity.RefAt(source.GitIssueSystem, "A", "repo"),
		entity.RefAt(source.GitIssueSystem, "A", "issue", a),
	} {
		got, rn := reflectIssues(t, gi, ref)
		for name, p := range got {
			if p["repo"] != "A" {
				t.Errorf("Reflect(%s) reached outside A: %s", ref, name)
			}
		}
		if len(rn.Covers) != 2 {
			t.Errorf("Reflect(%s) covered %d patterns, want just A's two", ref, len(rn.Covers))
		}
	}
}

// A LEGACY issue does not take the pass down with it.
//
// `git issue`'s first iteration numbered issues six digits and wrote `id: 5` — a Number where
// the current struct wants a string — and issuelib's doc says such issues stay readable until
// `git issue migrate` rewrites them. Upstream's own reader does not manage it (GetByRef errors,
// List silently returns one fewer than it found), and one of these is sitting in a real
// tracker today. Failing the pass over it would take the whole slice out until somebody fixed
// an old file, so what is knowable without meta.tony — the id and status from the ref, the
// title from description.md — is reflected anyway, and metaError says what was missed.
func TestGitIssueLegacyIssueDoesNotFailThePass(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	fine := fileIssue(t, dir, "an ordinary issue", "", nil)
	victim := fileIssue(t, dir, "will become legacy", "", nil)

	// Rewrite one into the old shape, at the old six-digit ref.
	t.Chdir(dir)
	s := issuelib.NewGitStoreWithOutput(testLog{t})
	legacy := "id: 5\nstatus: open\nlabels: []\ncommits: []\nbranches: []\n" +
		"created: \"2025-12-19T12:29:27+01:00\"\nupdated: \"2025-12-19T12:29:27+01:00\"\n"
	err := s.ReplaceTree(issuelib.RefForXIDR(victim), "make it legacy", map[string][]byte{
		"meta.tony":      []byte(legacy),
		"description.md": []byte("# a legacy title\n\nand its body"),
	})
	if err != nil {
		t.Fatalf("ReplaceTree: %v", err)
	}
	if err := s.MoveRef(issuelib.RefForXIDR(victim), "refs/issues/000005"); err != nil {
		t.Fatalf("MoveRef: %v", err)
	}

	gi := source.NewGitIssue()
	gi.AddRepo("A", dir)
	got, _ := reflectIssues(t, gi, entity.Ref{})

	p := got[entity.RefAt(source.GitIssueSystem, "A", "issue", "000005")]
	if p == nil {
		t.Fatalf("the legacy issue was dropped; got %v", len(got))
	}
	if p["status"] != "open" {
		t.Errorf("legacy status = %v, want open (from the ref)", p["status"])
	}
	if p["title"] != "a legacy title" {
		t.Errorf("legacy title = %v (from description.md)", p["title"])
	}
	if p["id"] != "000005" {
		t.Errorf("legacy id = %v, want the ref's 000005", p["id"])
	}
	if p["metaError"] == nil {
		t.Error("no metaError: a thin issue with no explanation reads as an unlabelled one")
	}
	// And the healthy issue beside it is untouched — the point of not failing the pass.
	if q := got[entity.RefAt(source.GitIssueSystem, "A", "issue", fine)]; q == nil || q["metaError"] != nil {
		t.Errorf("the ordinary issue was collateral: %+v", q)
	}
	if sm := got[entity.RefAt(source.GitIssueSystem, "A", "repo")]; sm["issues"] != int64(2) {
		t.Errorf("summary = %+v, want both issues counted", sm)
	}
}

// A mirror carries the tracker. `git clone --mirror` maps every ref, so refs/issues and
// refs/closed arrive with everything else — which is the whole deployed story, since a pod has
// no repository and somebody else's `git issue push` reaches it only as a fetch.
func TestGitIssueMirrorCarriesTheTracker(t *testing.T) {
	needGit(t)
	ctx := context.Background()

	origin := t.TempDir()
	initRepo(t, origin)
	commit(t, origin, "a.txt", "one", "first") // a mirror of an unborn repo has nothing to clone
	open := fileIssue(t, origin, "filed upstream", "and pushed", nil)
	shut := fileIssue(t, origin, "settled upstream", "", nil)
	closeIssue(t, origin, shut)

	into := filepath.Join(t.TempDir(), "mirrors", "origin")
	gi := source.NewGitIssue()
	if _, err := gi.AddMirror(ctx, "up", origin, into); err != nil {
		t.Fatalf("AddMirror: %v", err)
	}
	got, _ := reflectIssues(t, gi, entity.Ref{})
	if p := got[entity.RefAt(source.GitIssueSystem, "up", "issue", open)]; p == nil || p["status"] != "open" {
		t.Errorf("mirror lost the open issue: %+v", p)
	}
	if p := got[entity.RefAt(source.GitIssueSystem, "up", "issue", shut)]; p == nil || p["status"] != "closed" {
		t.Errorf("mirror lost the closed issue: %+v", p)
	}

	// A pass FETCHES, so an issue filed after the clone arrives without a restart.
	later := fileIssue(t, origin, "filed after the clone", "", nil)
	got, _ = reflectIssues(t, gi, entity.Ref{})
	if got[entity.RefAt(source.GitIssueSystem, "up", "issue", later)] == nil {
		t.Errorf("a later issue did not arrive on the next pass; got %d issues", len(got)-1)
	}
}

// A RELATIVE -state clones where it was told to.
//
// It did not: the clone runs with the target's parent as its working directory, so git
// resolved a relative path against that parent a second time and put the mirror in
// <state>/<state>/<id>. Every pass then failed with "chdir <state>/<id>: no such file or
// directory", which reads like the remote's fault and is not. Only the default -state being
// absolute kept it hidden, in this source and in the git source it is shared with.
func TestGitMirrorHonoursARelativeState(t *testing.T) {
	needGit(t)
	ctx := context.Background()

	origin := t.TempDir()
	initRepo(t, origin)
	commit(t, origin, "a.txt", "one", "first")
	id := fileIssue(t, origin, "reachable through a relative state dir", "", nil)

	// A relative path is only meaningful against a known cwd, which is what t.Chdir gives.
	work := t.TempDir()
	t.Chdir(work)
	into := filepath.Join("state", "up")

	gi := source.NewGitIssue()
	if _, err := gi.AddMirror(ctx, "up", origin, into); err != nil {
		t.Fatalf("AddMirror: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "state", "up", "HEAD")); err != nil {
		t.Fatalf("mirror is not at the relative path it was given: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "state", "state")); err == nil {
		t.Error("the state dir was resolved twice: mirror landed in <state>/<state>/<id>")
	}
	got, _ := reflectIssues(t, gi, entity.Ref{})
	if got[entity.RefAt(source.GitIssueSystem, "up", "issue", id)] == nil {
		t.Errorf("reflect through a relative -state found nothing; got %+v", got)
	}
}

// Ownership is one place per repository, with no glob in it: the place is what the install
// takes an owner record at.
func TestGitIssueOwns(t *testing.T) {
	gi := source.NewGitIssue()
	gi.AddRepo("A", t.TempDir())
	want := map[string]bool{"git-issue.A": true}
	got := gi.Owns()
	if len(got) != len(want) {
		t.Fatalf("Owns() = %+v, want %v", got, want)
	}
	for _, o := range got {
		if !want[o.At] {
			t.Errorf("unexpected claim %q", o.At)
		}
	}
}

// -system moves the whole slice, including what Owns claims and what RepoRef names — a hint at
// git-issue.x reaches nothing when the source writes tracker.x.
func TestGitIssueSystemMovesTheSlice(t *testing.T) {
	gi := source.NewGitIssue()
	if err := gi.SetSystem("tracker"); err != nil {
		t.Fatal(err)
	}
	gi.AddRepo("A", t.TempDir())
	if gi.System() != "tracker" {
		t.Errorf("System() = %q", gi.System())
	}
	if got := gi.RepoRef("A").String(); got != "tracker.A" {
		t.Errorf("RepoRef = %q", got)
	}
	for _, o := range gi.Owns() {
		if !hasPrefix(o.At, "tracker.") {
			t.Errorf("claim %q is not in the moved slice", o.At)
		}
	}
	if err := gi.SetSystem("a:b"); err == nil {
		t.Error("a system with a colon must be refused — it is a segment of the address")
	}
}

// Bodies are separable: an operator who wants an index of issue STATE rather than a copy of
// everyone's prose turns them off, and nothing else about the entity changes.
func TestGitIssueBodiesAreSeparable(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	id := fileIssue(t, dir, "a title", "a body nobody wants reflected", nil)

	gi := source.NewGitIssue()
	gi.SetBodies(false)
	gi.AddRepo("A", dir)
	got, _ := reflectIssues(t, gi, entity.Ref{})
	p := got[entity.RefAt(source.GitIssueSystem, "A", "issue", id)]
	if p == nil {
		t.Fatalf("missing the issue; got %+v", got)
	}
	if _, ok := p["body"]; ok {
		t.Errorf("body reflected with -bodies=false: %+v", p)
	}
	if p["title"] != "a title" {
		t.Errorf("title = %v; the title is not a body and stays", p["title"])
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
