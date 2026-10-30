package github_test

import (
	"context"
	"fmt"
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"github.com/signadot/verse/versed/versedtest"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/io/github"
)

// The out half against the fake: a comment made once, left alone while the text holds, and
// updated in place when it changes; a `to` it cannot read is refused on the status.
func TestTheOutHalfKeepsACommentInStep(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versedtest.Server(t, runnerdtest.Scripted, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	f.repo("signadot/verse").pr(42, "a change", "open", "2026-09-01T00:00:00Z", "abc")
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	places := src.OutRefs()
	if len(places) != 1 || places[0].String() != "github.signadot.verse.out" {
		t.Fatalf("out places %v, want github.signadot.verse.out", places)
	}
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-iod")
	out := verseio.NewOut(src.Sink(), places[0], verse, nil)
	pass := func() {
		t.Helper()
		if err := out.Pass(context.Background()); err != nil {
			t.Fatalf("pass: %v", err)
		}
	}
	ask := func(name string, input entity.Obj) {
		t.Helper()
		entitytest.Set(t, store, entity.RefAt("github", "signadot", "verse", "out", name), input.Payload())
	}
	status := func(name string) entity.Obj {
		e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", name+verseio.StatusSuffix))
		return entity.ToObj(e.Payload)
	}

	ask("trial", entity.Obj{"to": "pr.42", "text": "the trial found nothing new"})
	pass()
	if got := f.commentsOn("signadot/verse", 42); len(got) != 1 || got[0] != "the trial found nothing new" {
		t.Fatalf("comments on #42: %q, want the one asked for", got)
	}
	st := status("trial")
	if st["comment"] == nil || st["url"] == nil || st["posted"] == nil {
		t.Fatalf("the status says %v, want the comment, its url and what it carries", st)
	}

	pass()
	if got := f.commentsOn("signadot/verse", 42); len(got) != 1 || f.editCount() != 0 {
		t.Fatalf("a pass with nothing changed wrote to GitHub: comments %q, edits %d", got, f.editCount())
	}

	ask("trial", entity.Obj{"to": "pr.42", "text": "the trial found one change"})
	pass()
	if got := f.commentsOn("signadot/verse", 42); len(got) != 1 || got[0] != "the trial found one change" || f.editCount() != 1 {
		t.Fatalf("comments on #42 after a change: %q, want the one comment updated", got)
	}

	// DELETED ON GITHUB, and the text changed: the comment is said again, not refused for good as
	// an edit of what is not there (git-issue ne9fce9dh12ks1q4qsn0).
	was := status("trial")["comment"]
	f.mu.Lock()
	f.repos["signadot/verse"].comments = nil
	f.mu.Unlock()
	ask("trial", entity.Obj{"to": "pr.42", "text": "the trial found two changes"})
	pass()
	st = status("trial")
	if got := f.commentsOn("signadot/verse", 42); len(got) != 1 || got[0] != "the trial found two changes" ||
		st["comment"] == was || st["error"] != nil {
		t.Fatalf("a deleted comment: comments %q, status %v, want it said again under a new id", got, st)
	}

	ask("bad", entity.Obj{"to": "branch.main", "text": "x"})
	pass()
	if e, _ := status("bad")["error"].(string); !strings.Contains(e, "pr.<number>") {
		t.Fatalf("a `to` that names no pr or issue: status %v, want the error saying what it takes", status("bad"))
	}
}

// A PULL REQUEST FOR A BRANCH: opened when there is none, kept in step with the input's title and
// body, and ADOPTED where a person opened one already — whose words are then left as they are.
func TestTheOutHalfOpensOrAdoptsAPullRequest(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versedtest.Server(t, runnerdtest.Scripted, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	f.repo("signadot/verse").pr(7, "a person's own", "open", "2026-09-01T00:00:00Z", "abc") // head feature-7
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-iod")
	out := verseio.NewOut(src.Sink(), src.OutRefs()[0], verse, nil)
	pass := func() {
		t.Helper()
		if err := out.Pass(context.Background()); err != nil {
			t.Fatalf("pass: %v", err)
		}
	}
	ask := func(name string, input entity.Obj) {
		t.Helper()
		entitytest.Set(t, store, entity.RefAt("github", "signadot", "verse", "out", name), input.Payload())
	}
	status := func(name string) entity.Obj {
		e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", name+verseio.StatusSuffix))
		return entity.ToObj(e.Payload)
	}
	open := func(title string) entity.Obj { return entity.Obj{"title": title, "body": "proposed by verse"} }

	ask("review-x", entity.Obj{"to": "branch.verse/repo-charter-review-x", "open": open("charters: x")})
	pass()
	st := status("review-x")
	if f.openedCount() != 1 || st["number"] == nil || st["url"] == nil || st["adopted"] != nil {
		t.Fatalf("opening: %d opened, status %v, want one opened and its number on the status", f.openedCount(), st)
	}
	pass()
	if f.openedCount() != 1 || f.editCount() != 0 {
		t.Fatalf("a pass with nothing changed wrote to GitHub: %d opened, %d edits", f.openedCount(), f.editCount())
	}
	ask("review-x", entity.Obj{"to": "branch.verse/repo-charter-review-x", "open": open("charters: x, revised")})
	pass()
	if f.openedCount() != 1 || f.editCount() != 1 {
		t.Fatalf("a changed title: %d opened, %d edits, want the one pull request edited", f.openedCount(), f.editCount())
	}

	// THE IO'S OWN, FOUND AGAIN after its status was lost (git-issue ne9fce9dh12ks1q4qsn0): opened,
	// not adopted, so a changed title still reaches it.
	if _, _, err := store.Write(entity.Write{At: entity.RefAt("github", "signadot", "verse", "out", "review-x"+verseio.StatusSuffix).At()}); err != nil {
		t.Fatal(err)
	}
	pass()
	if st := status("review-x"); f.openedCount() != 1 || st["adopted"] != nil || st["number"] == nil {
		t.Fatalf("the io's own pull request found again: %d opened, status %v, want it recorded as opened", f.openedCount(), st)
	}
	ask("review-x", entity.Obj{"to": "branch.verse/repo-charter-review-x", "open": open("charters: x, revised again")})
	pass()
	if f.editCount() != 2 {
		t.Fatalf("the io's own pull request, found again, was not kept in step: %d edits, want 2", f.editCount())
	}

	ask("person", entity.Obj{"to": "branch.feature-7", "open": open("verse's title")})
	pass()
	st = status("person")
	if f.openedCount() != 1 || st["adopted"] != true || st["number"] != int64(7) {
		t.Fatalf("adopting #7: %d opened, status %v, want #7 adopted and nothing opened", f.openedCount(), st)
	}
	ask("person", entity.Obj{"to": "branch.feature-7", "open": open("verse's title, again")})
	pass()
	if f.editCount() != 2 {
		t.Fatalf("an adopted pull request was rewritten: %d edits", f.editCount())
	}

	ask("no-title", entity.Obj{"to": "branch.y", "open": entity.Obj{"body": "x"}})
	ask("not-a-branch", entity.Obj{"to": "pr.7", "open": open("t")})
	pass()
	for name, says := range map[string]string{"no-title": "title", "not-a-branch": "branch.<name>"} {
		if e, _ := status(name)["error"].(string); !strings.Contains(e, says) {
			t.Errorf("%s: status %v, want an error saying %q", name, status(name), says)
		}
	}
}

// A MERGE AT A SHA: refused while the head is not the sha decided on, made when it is, and made
// once; a pull request merged before the io was asked is recorded with no commit of the io's.
func TestTheOutHalfMergesAtASha(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versedtest.Server(t, runnerdtest.Scripted, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	r := f.repo("signadot/verse")
	r.pr(42, "decided", "open", "2026-09-01T00:00:00Z", "abc")
	r.pr(43, "merged by a person", "closed", "2026-09-01T00:00:00Z", "def")
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-iod")
	out := verseio.NewOut(src.Sink(), src.OutRefs()[0], verse, nil)
	pass := func() {
		t.Helper()
		if err := out.Pass(context.Background()); err != nil {
			t.Fatalf("pass: %v", err)
		}
	}
	ask := func(name string, input entity.Obj) {
		t.Helper()
		entitytest.Set(t, store, entity.RefAt("github", "signadot", "verse", "out", name), input.Payload())
	}
	status := func(name string) entity.Obj {
		e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", name+verseio.StatusSuffix))
		return entity.ToObj(e.Payload)
	}

	ask("stale", entity.Obj{"to": "pr.42", "merge": entity.Obj{"sha": "zzz"}})
	pass()
	if e, _ := status("stale")["error"].(string); f.mergeCount() != 0 || !strings.Contains(e, "head is abc") {
		t.Fatalf("a merge decided at another sha: %d merged, status %v, want refused naming the head", f.mergeCount(), status("stale"))
	}

	// The refused input is withdrawn, so the passes below ask GitHub about nothing else.
	for _, name := range []string{"stale", "stale" + verseio.StatusSuffix} {
		if _, _, err := store.Write(entity.Write{At: entity.RefAt("github", "signadot", "verse", "out", name).At()}); err != nil {
			t.Fatal(err)
		}
	}

	ask("land", entity.Obj{"to": "pr.42", "merge": entity.Obj{"sha": "abc"}})
	pass()
	if st := status("land"); f.mergeCount() != 1 || st["merged"] != true || st["commit"] != "dddd4444" {
		t.Fatalf("merging at the head: %d merged, status %v, want merged with its commit", f.mergeCount(), st)
	}
	// Merged is settled: a later pass asks GitHub nothing about it, which is a read saved per pass.
	before, _ := f.counts()
	pass()
	if after, _ := f.counts(); f.mergeCount() != 1 || after != before {
		t.Fatalf("a merged pull request: %d merged, %d requests on a later pass, want none", f.mergeCount(), after-before)
	}

	ask("around", entity.Obj{"to": "pr.43", "merge": entity.Obj{"sha": "def"}})
	pass()
	if st := status("around"); f.mergeCount() != 1 || st["merged"] != true || st["commit"] != nil {
		t.Fatalf("a pull request merged before the io was asked: %d merged, status %v, want it recorded with no commit of the io's",
			f.mergeCount(), st)
	}
}

// A CLOSE: once, by the kind `to` names, and recorded as it is where somebody closed it first.
func TestTheOutHalfClosesAnIssueOrAPullRequest(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versedtest.Server(t, runnerdtest.Scripted, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	r := f.repo("signadot/verse")
	r.pr(42, "abandoned", "open", "2026-09-01T00:00:00Z", "abc")
	r.pr(43, "closed by a person", "closed", "2026-09-02T00:00:00Z", "def").closedBy = "ana"
	r.issue(7, "an issue", "open", "2026-09-01T00:00:00Z")
	r.issue(8, "done", "open", "2026-09-01T00:00:00Z")
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-iod")
	out := verseio.NewOut(src.Sink(), src.OutRefs()[0], verse, nil)
	pass := func() {
		t.Helper()
		if err := out.Pass(context.Background()); err != nil {
			t.Fatalf("pass: %v", err)
		}
	}
	ask := func(name string, input entity.Obj) {
		t.Helper()
		entitytest.Set(t, store, entity.RefAt("github", "signadot", "verse", "out", name), input.Payload())
	}
	status := func(name string) entity.Obj {
		e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", name+verseio.StatusSuffix))
		return entity.ToObj(e.Payload)
	}

	// The wrong kind is refused, and nothing is closed.
	ask("wrong", entity.Obj{"to": "pr.7", "close": entity.Obj{}})
	pass()
	if e, _ := status("wrong")["error"].(string); f.closeCount() != 0 || !strings.Contains(e, "issue.7") {
		t.Fatalf("pr.7 naming an issue: %d closed, status %v, want refused naming issue.7", f.closeCount(), status("wrong"))
	}

	ask("shut", entity.Obj{"to": "pr.42", "close": entity.Obj{}})
	ask("issue", entity.Obj{"to": "issue.8", "close": entity.Obj{}})
	pass()
	for _, name := range []string{"shut", "issue"} {
		if st := status(name); st["closed"] != true || st["already"] != nil || st["closedBy"] != "verse-bot" || st["closedAt"] == nil {
			t.Errorf("%s: status %v, want closed by the io", name, st)
		}
	}
	if f.closeCount() != 2 || r.find(42).state != "closed" || r.find(8).state != "closed" || r.find(7).state != "open" {
		t.Fatalf("closes %d, #42 %s, #8 %s, #7 %s: want #42 and #8 closed and #7 left open",
			f.closeCount(), r.find(42).state, r.find(8).state, r.find(7).state)
	}
	// Closed is settled: a later pass asks GitHub nothing about it.
	for _, name := range []string{"wrong", "wrong" + verseio.StatusSuffix} {
		if _, _, err := store.Write(entity.Write{At: entity.RefAt("github", "signadot", "verse", "out", name).At()}); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := f.counts()
	pass()
	if after, _ := f.counts(); f.closeCount() != 2 || after != before {
		t.Fatalf("closed items: %d closed, %d requests on a later pass, want none", f.closeCount(), after-before)
	}

	ask("around", entity.Obj{"to": "pr.43", "close": entity.Obj{}})
	pass()
	if st := status("around"); f.closeCount() != 2 || st["closed"] != true || st["already"] != true || st["closedBy"] != "ana" {
		t.Fatalf("a pull request closed before the io was asked: %d closed, status %v, want it recorded as ana's close",
			f.closeCount(), st)
	}
}

// A REVIEW MIRRORING AN ASK: posted, left alone while it says the same, and posted again when it
// changes, since a reviewer's latest review is the one GitHub counts. A pull request the io opened
// cannot be reviewed by it, and GitHub's refusal lands on the status.
func TestTheOutHalfReviewsAPullRequest(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versedtest.Server(t, runnerdtest.Scripted, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	f.repo("signadot/verse").pr(42, "a person's", "open", "2026-09-01T00:00:00Z", "abc")
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-iod")
	out := verseio.NewOut(src.Sink(), src.OutRefs()[0], verse, nil)
	pass := func() {
		t.Helper()
		if err := out.Pass(context.Background()); err != nil {
			t.Fatalf("pass: %v", err)
		}
	}
	ask := func(name string, input entity.Obj) {
		t.Helper()
		entitytest.Set(t, store, entity.RefAt("github", "signadot", "verse", "out", name), input.Payload())
	}
	status := func(name string) entity.Obj {
		e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", name+verseio.StatusSuffix))
		return entity.ToObj(e.Payload)
	}

	ask("ready", entity.Obj{"to": "pr.42", "review": entity.Obj{"event": "request-changes", "body": "ana objects: no tests"}})
	pass()
	pass()
	if got := f.reviewEvents(); len(got) != 1 || got[0] != "REQUEST_CHANGES" {
		t.Fatalf("reviews %v, want one REQUEST_CHANGES however many passes", got)
	}
	if st := status("ready"); st["review"] == nil || st["event"] != "request-changes" {
		t.Fatalf("the status says %v, want the review and its event", st)
	}
	ask("ready", entity.Obj{"to": "pr.42", "review": entity.Obj{"event": "approve", "body": "the ask settled proceed"}})
	pass()
	if got := f.reviewEvents(); len(got) != 2 || got[1] != "APPROVE" {
		t.Fatalf("reviews %v, want an APPROVE after the objection was withdrawn", got)
	}

	ask("self", entity.Obj{"to": "branch.verse/x", "open": entity.Obj{"title": "mine"}})
	pass()
	n := status("self")["number"]
	ask("own", entity.Obj{"to": fmt.Sprintf("pr.%v", n), "review": entity.Obj{"event": "approve"}})
	ask("silent-objection", entity.Obj{"to": "pr.42", "review": entity.Obj{"event": "request-changes"}})
	ask("bad-event", entity.Obj{"to": "pr.42", "review": entity.Obj{"event": "praise"}})
	pass()
	for name, says := range map[string]string{
		"own": "Can not approve your own", "silent-objection": "carries a body", "bad-event": "approve, request-changes, comment or dismiss",
	} {
		if e, _ := status(name)["error"].(string); !strings.Contains(e, says) {
			t.Errorf("%s: status %v, want an error saying %q", name, status(name), says)
		}
	}
}

// A VERDICT AT A HEAD: a review naming a sha is about that commit, so the same verdict at the next
// head is a new review, and a comment after a request for changes dismisses it first, since a
// comment would leave the merge held while the io's latest word says it should not be. A comment
// after an approval dismisses that too, which would otherwise go on counting toward the merge,
// and a review naming no sha leaves none of an earlier one's on the status.
func TestTheOutHalfReviewsAtAHeadAndACommentLiftsItsHold(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versedtest.Server(t, runnerdtest.Scripted, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	f.repo("signadot/verse").pr(42, "a person's", "open", "2026-09-01T00:00:00Z", "aaa")
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-iod")
	out := verseio.NewOut(src.Sink(), src.OutRefs()[0], verse, nil)
	pass := func() {
		t.Helper()
		if err := out.Pass(context.Background()); err != nil {
			t.Fatalf("pass: %v", err)
		}
	}
	ask := func(review entity.Obj) {
		t.Helper()
		entitytest.Set(t, store, entity.RefAt("github", "signadot", "verse", "out", "verdict"),
			entity.Obj{"to": "pr.42", "review": review}.Payload())
	}
	status := func() entity.Obj {
		e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", "verdict"+verseio.StatusSuffix))
		return entity.ToObj(e.Payload)
	}

	ask(entity.Obj{"event": "request-changes", "body": "the doc comment still says it retries", "sha": "aaa"})
	pass()
	pass()
	ask(entity.Obj{"event": "request-changes", "body": "the doc comment still says it retries", "sha": "bbb"})
	pass()
	if got, _ := f.reviewCommits(); len(got) != 2 || got[0] != "aaa" || got[1] != "bbb" {
		t.Fatalf("reviews at %v, want one at aaa and one at bbb: the same verdict at a new head", got)
	}
	if st := status(); st["sha"] != "bbb" {
		t.Fatalf("status %v, want the sha its review is about", st)
	}

	ask(entity.Obj{"event": "comment", "body": "a style note, not a blocker", "sha": "ccc"})
	pass()
	events := f.reviewEvents()
	if _, dismissed := f.reviewCommits(); len(events) != 3 || events[2] != "COMMENT" || dismissed != 2 {
		t.Fatalf("reviews %v and %d dismissed, want a COMMENT after both requests for changes were dismissed",
			events, dismissed)
	}

	ask(entity.Obj{"event": "approve", "body": "fine", "sha": entity.Removed()}) // a Set merges: the earlier sha is taken out
	pass()
	if st := status(); st["event"] != "approve" || st["sha"] != nil {
		t.Fatalf("status %v, want the approval with no sha: it names no commit", st)
	}
	ask(entity.Obj{"event": "comment", "body": "one more note", "sha": "eee"})
	pass()
	pr := f.repos["signadot/verse"].find(42)
	if _, dismissed := f.reviewCommits(); dismissed != 3 || pr.reviews[3].state != "DISMISSED" {
		t.Fatalf("%d dismissed and reviews %+v, want the approval dismissed before the comment", dismissed, pr.reviews)
	}
	if st := status(); st["event"] != "comment" || st["sha"] != "eee" {
		t.Fatalf("status %v, want the comment at eee", st)
	}

	entitytest.Set(t, store, entity.RefAt("github", "signadot", "verse", "out", "silent"),
		entity.Obj{"to": "pr.42", "review": entity.Obj{"event": "comment", "sha": "ddd"}}.Payload())
	pass()
	e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", "silent"+verseio.StatusSuffix))
	if st := entity.ToObj(e.Payload); !strings.Contains(fmt.Sprint(st["error"]), "comments carries a body") {
		t.Errorf("status %v, want a comment without a body refused", st)
	}
}

// GITHUB'S REFUSAL IS PERMANENT, and its rate limit is not (git-issue na9fce9dh12krdydqsn0). A
// review of the io's own pull request is refused with a 422, which asking again cannot change: it
// is asked once. A 403 that is GitHub's rate limit fixes itself: it is asked again.
func TestARefusalIsAskedOnceAndARateLimitAgain(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versedtest.Server(t, runnerdtest.Scripted, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	r := f.repo("signadot/verse")
	r.pr(42, "the io's own", "open", "2026-09-01T00:00:00Z", "abc").selfOpened = true
	r.issue(7, "an issue", "open", "2026-09-01T00:00:00Z")
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-iod")
	out := verseio.NewOut(src.Sink(), src.OutRefs()[0], verse, nil)
	pass := func() {
		t.Helper()
		if err := out.Pass(context.Background()); err != nil {
			t.Fatalf("pass: %v", err)
		}
	}
	status := func(name string) entity.Obj {
		e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", name+verseio.StatusSuffix))
		return entity.ToObj(e.Payload)
	}
	entitytest.Set(t, store, entity.RefAt("github", "signadot", "verse", "out", "own"),
		entity.Obj{"to": "pr.42", "review": entity.Obj{"event": "approve"}}.Payload())
	pass()
	if st := status("own"); st["failed"] != verseio.PermanentFailure || !strings.Contains(fmt.Sprint(st["error"]), "own pull request") {
		t.Fatalf("status %v, want GitHub's refusal, as permanent", st)
	}
	before, _ := f.counts()
	pass()
	pass()
	if after, _ := f.counts(); after != before {
		t.Fatalf("%d requests on two passes after a refusal, want none", after-before)
	}

	entitytest.Set(t, store, entity.RefAt("github", "signadot", "verse", "out", "note"),
		entity.Obj{"to": "issue.7", "text": "hello"}.Payload())
	f.mu.Lock()
	f.failWith = 403 // Forbidden, answered `API rate limit exceeded`
	f.mu.Unlock()
	pass()
	if st := status("note"); st["failed"] != nil || !strings.Contains(fmt.Sprint(st["error"]), "rate limit") {
		t.Fatalf("status %v, want the rate limit, not permanent", st)
	}
	f.mu.Lock()
	f.failWith = 0
	f.mu.Unlock()
	pass()
	if st := status("note"); st["comment"] == nil || st["error"] != nil {
		t.Errorf("status %v, want the comment made once the limit lifted", st)
	}
}

// A pull request from a branch GitHub does not have yet waits for the branch: the io's error is
// retried on the next pass, not recorded as a refusal for good, and the pull request opens once
// the branch is there.
func TestAPullRequestWaitsForItsBranch(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versedtest.Server(t, runnerdtest.Scripted, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	repo := f.repo("signadot/verse")
	repo.missingHeads = map[string]bool{"verse/candidate/aaaa": true}
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-iod")
	out := verseio.NewOut(src.Sink(), src.OutRefs()[0], verse, nil)
	entitytest.Set(t, store, entity.RefAt("github", "signadot", "verse", "out", "candidate"), entity.Obj{
		"to": "branch.verse/candidate/aaaa", "open": entity.Obj{"base": "verse/live", "title": "charters: aaaa"},
	}.Payload())
	if err := out.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", "candidate"+verseio.StatusSuffix))
	st := entity.ToObj(e.Payload)
	if st["error"] == nil || st["failed"] == verseio.PermanentFailure || !strings.Contains(fmt.Sprint(st["error"]), "not on GitHub yet") {
		t.Fatalf("a missing head: status %v, want a retryable error naming the branch", st)
	}
	f.mu.Lock()
	repo.missingHeads = nil
	f.mu.Unlock()
	if err := out.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	e, _, _ = store.Get(entity.RefAt("github", "signadot", "verse", "out", "candidate"+verseio.StatusSuffix))
	if st := entity.ToObj(e.Payload); st["number"] == nil || st["error"] != nil {
		t.Fatalf("once the branch is there: status %v, want the pull request opened", st)
	}
}

// An `open` input carrying `commitStatus` keeps verse's commit status on the pull request's head: posted
// under the context `verse` with the pull request as its link, once per state, description and
// sha, so the page says what verse waits for and then what it decided.
func TestAnOpenInputPutsACommitStatusOnThePullRequestsHead(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versedtest.Server(t, runnerdtest.Scripted, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	f.repo("signadot/verse")
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-iod")
	out := verseio.NewOut(src.Sink(), src.OutRefs()[0], verse, nil)
	ref := entity.RefAt("github", "signadot", "verse", "out", "candidate")
	in := entity.Obj{
		"to": "branch.verse/candidate/aaaa", "open": entity.Obj{"base": "verse/live", "title": "charters: aaaa"},
		"commitStatus": entity.Obj{"verse": entity.Obj{"state": "pending", "description": "waiting for an answer"}},
	}
	entitytest.Set(t, store, ref, in.Payload())
	pass := func() {
		t.Helper()
		if err := out.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	status := func() entity.Obj {
		e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", "candidate"+verseio.StatusSuffix))
		return entity.ToObj(e.Payload)
	}
	pass()
	st := status()
	posted := f.statusesOn("signadot/verse")
	// The fake gives every pull request it opens the head cccc3333, which the check lands on
	// when the input names no sha.
	if len(posted) != 1 || posted[0].state != "pending" || posted[0].sha != "cccc3333" || posted[0].context != "verse" ||
		posted[0].description != "waiting for an answer" || posted[0].target != fmt.Sprint(st["url"]) {
		t.Fatalf("statuses posted = %+v (status %v)", posted, st)
	}
	cs := func(st entity.Obj, context, key string) any {
		m, _ := st["commitStatus"].(map[string]any)
		c, _ := m[context].(map[string]any)
		return c[key]
	}
	if cs(st, "verse", "state") != "pending" || cs(st, "verse", "sha") != "cccc3333" || cs(st, "verse", "posted") == nil || st["error"] != nil {
		t.Fatalf("status after the check = %v", st)
	}
	// The same status again costs nothing.
	pass()
	if n := len(f.statusesOn("signadot/verse")); n != 1 {
		t.Fatalf("a quiet pass posted a status: %d", n)
	}
	// Decided: the state changes, and the new status replaces the old on the same head.
	in["commitStatus"] = entity.Obj{"verse": entity.Obj{"state": "success", "description": "installed", "sha": "cccc3333"}}
	entitytest.Set(t, store, ref, in.Payload())
	pass()
	posted = f.statusesOn("signadot/verse")
	if len(posted) != 2 || posted[1].state != "success" || posted[1].description != "installed" || cs(status(), "verse", "state") != "success" {
		t.Fatalf("after deciding: statuses %+v, status %v", posted, status())
	}
	// A second context stands beside the first: its own status, the first left as it is.
	in["commitStatus"] = entity.Obj{
		"verse":       entity.Obj{"state": "success", "description": "installed", "sha": "cccc3333"},
		"verse/check": entity.Obj{"state": "success", "description": "installs cleanly"},
	}
	entitytest.Set(t, store, ref, in.Payload())
	pass()
	posted = f.statusesOn("signadot/verse")
	if len(posted) != 3 || posted[2].context != "verse/check" || cs(status(), "verse/check", "state") != "success" || cs(status(), "verse", "state") != "success" {
		t.Fatalf("a second context: statuses %+v, status %v", posted, status())
	}
	// A state GitHub has no word for is said beside the rest of the status, once, and the input
	// is not failed.
	in["commitStatus"] = entity.Obj{"verse": entity.Obj{"state": "maybe"}}
	entitytest.Set(t, store, ref, in.Payload())
	pass()
	if st := status(); st["failed"] != nil || st["error"] != nil || !strings.Contains(fmt.Sprint(cs(st, "verse", "error")), "commitStatus.verse.state") {
		t.Fatalf("an unknown state: status %v, want the error under its context, and the input not failed", st)
	}
}

// A COMMIT STATUS GITHUB REFUSES DOES NOT STOP THE REST OF THE INPUT. An App not yet allowed
// `statuses: write` is refused every status: the refusal is said beside the pull request's
// status and not asked again while the same status is asked for, the input is not failed, and
// what else it asks is still done. Once a status goes up, the error goes.
func TestARefusedCommitStatusLeavesTheRestOfTheInput(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versedtest.Server(t, runnerdtest.Scripted, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	repo := f.repo("signadot/verse")
	repo.statusesForbidden = true
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-iod")
	out := verseio.NewOut(src.Sink(), src.OutRefs()[0], verse, nil)
	ref := entity.RefAt("github", "signadot", "verse", "out", "candidate")
	in := entity.Obj{
		"to": "branch.verse/candidate/aaaa", "open": entity.Obj{"base": "verse/live", "title": "charters: aaaa"},
		"commitStatus": entity.Obj{"verse": entity.Obj{"state": "pending", "description": "waiting"}},
	}
	entitytest.Set(t, store, ref, in.Payload())
	pass := func() {
		t.Helper()
		if err := out.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	status := func() entity.Obj {
		e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", "candidate"+verseio.StatusSuffix))
		return entity.ToObj(e.Payload)
	}
	pass()
	st := status()
	under := func(st entity.Obj, key string) any {
		m, _ := st["commitStatus"].(map[string]any)
		c, _ := m["verse"].(map[string]any)
		return c[key]
	}
	if st["number"] == nil || st["failed"] != nil || st["error"] != nil || !strings.Contains(fmt.Sprint(under(st, "error")), "Forbidden") || under(st, "refused") == nil {
		t.Fatalf("a refused status: %v, want the pull request opened and the refusal beside it", st)
	}
	// Quiet passes ask GitHub nothing more, and the input is still served: a changed title is
	// delivered.
	pass()
	pass()
	if repo.statusAsks != 1 {
		t.Fatalf("the refused status was asked %d times, want once", repo.statusAsks)
	}
	in["open"] = entity.Obj{"base": "verse/live", "title": "charters: aaaa, again"}
	entitytest.Set(t, store, ref, in.Payload())
	pass()
	if n := int(status()["number"].(int64)); repo.find(n).title != "charters: aaaa, again" {
		t.Fatalf("the input stopped being served after the refusal: title %q", repo.find(n).title)
	}
	// Allowed now, and a new status asked for: posted, and the error gone.
	repo.statusesForbidden = false
	in["commitStatus"] = entity.Obj{"verse": entity.Obj{"state": "success", "description": "installed"}}
	entitytest.Set(t, store, ref, in.Payload())
	pass()
	if st := status(); under(st, "state") != "success" || under(st, "error") != nil || under(st, "refused") != nil {
		t.Fatalf("after the permission: status %v, want the status posted and the error gone", st)
	}
}

// An `open` input with `to: issue` files an issue, once, and keeps its title and body in step; it
// is how a charter reports what went wrong where the repository's people read, with no number to
// know before the issue exists. `closed: true` then closes it.
func TestAnOpenInputFilesAnIssue(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versedtest.Server(t, runnerdtest.Scripted, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	f.repo("signadot/verse")
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-iod")
	out := verseio.NewOut(src.Sink(), src.OutRefs()[0], verse, nil)
	ref := entity.RefAt("github", "signadot", "verse", "out", "install-failed")
	in := entity.Obj{"to": "issue", "open": entity.Obj{"title": "charters: the install of aaaa failed", "body": "exit status 1"}}
	entitytest.Set(t, store, ref, in.Payload())
	pass := func() {
		t.Helper()
		if err := out.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	status := func() entity.Obj {
		e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", "install-failed"+verseio.StatusSuffix))
		return entity.ToObj(e.Payload)
	}
	pass()
	st := status()
	number, _ := st["number"].(int64)
	if number == 0 || st["url"] == nil || st["posted"] == nil || st["error"] != nil || f.issuesOpenedCount() != 1 {
		t.Fatalf("status after filing = %v, issues opened %d", st, f.issuesOpenedCount())
	}
	if it := f.repos["signadot/verse"].find(int(number)); it == nil || it.pull || it.title != "charters: the install of aaaa failed" || it.body != "exit status 1" {
		t.Fatalf("the issue filed = %+v", it)
	}
	// Quiet when nothing changed; an edit to the body is an edit, not a second issue.
	pass()
	in["open"] = entity.Obj{"title": "charters: the install of aaaa failed", "body": "exit status 1\n\nand the log"}
	entitytest.Set(t, store, ref, in.Payload())
	pass()
	if f.issuesOpenedCount() != 1 || f.editCount() != 1 || f.repos["signadot/verse"].find(int(number)).body != "exit status 1\n\nand the log" {
		t.Fatalf("after an edit: %d issues, %d edits", f.issuesOpenedCount(), f.editCount())
	}
	// Closed once.
	in["closed"] = true
	entitytest.Set(t, store, ref, in.Payload())
	pass()
	if st := status(); st["closed"] != true || f.closeCount() != 1 {
		t.Fatalf("closing: status %v, closes %d", st, f.closeCount())
	}
	// Asked open again, with a new body: the one issue is reopened and says the new thing, and
	// no second issue is filed; then closed again.
	in["closed"] = false
	in["open"] = entity.Obj{"title": "charters: the install of bbbb failed", "body": "exit status 2"}
	entitytest.Set(t, store, ref, in.Payload())
	pass()
	it := f.repos["signadot/verse"].find(int(number))
	if st := status(); st["closed"] != nil || st["reopenedAt"] == nil || it.state != "open" || it.body != "exit status 2" ||
		f.issuesOpenedCount() != 1 || f.reopens != 1 {
		t.Fatalf("reopening: status %v, issue %+v, %d opened, %d reopened", st, it, f.issuesOpenedCount(), f.reopens)
	}
	pass()
	if f.reopens != 1 {
		t.Errorf("reopened twice")
	}
	in["closed"] = true
	entitytest.Set(t, store, ref, in.Payload())
	pass()
	if st := status(); st["closed"] != true || f.closeCount() != 2 || it.state != "closed" {
		t.Fatalf("closing again: status %v, closes %d", st, f.closeCount())
	}
	// A merge asked of an issue is refused for good.
	entitytest.Set(t, store, entity.RefAt("github", "signadot", "verse", "out", "merged-issue"), entity.Obj{
		"to": "issue", "open": entity.Obj{"title": "an issue"}, "merge": entity.Obj{"sha": "abc"},
	}.Payload())
	pass()
	e, _, _ := store.Get(entity.RefAt("github", "signadot", "verse", "out", "merged-issue"+verseio.StatusSuffix))
	if st := entity.ToObj(e.Payload); st["failed"] != verseio.PermanentFailure || !strings.Contains(fmt.Sprint(st["error"]), "not merged") {
		t.Fatalf("a merge on an issue: status %v", st)
	}
}
