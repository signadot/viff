package github_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/io/github"
	"github.com/signadot/verse/versed"
)

// The out half against the fake: a comment made once, left alone while the text holds, and
// updated in place when it changes; a `to` it cannot read is refused on the status.
func TestTheOutHalfKeepsACommentInStep(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versed.NewWithStore(versed.ScriptedBuilder, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	f.repo("signadot/verse").pr(42, "a change", "open", "2026-09-01T00:00:00Z", "abc")
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	places := src.OutRefs()
	if len(places) != 1 || places[0].String() != "github.signadot.verse.out" {
		t.Fatalf("out places %v, want github.signadot.verse.out", places)
	}
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-io")
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
	ts := httptest.NewServer(versed.NewWithStore(versed.ScriptedBuilder, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	f.repo("signadot/verse").pr(7, "a person's own", "open", "2026-09-01T00:00:00Z", "abc") // head feature-7
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}})
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:github-io")
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

	ask("person", entity.Obj{"to": "branch.feature-7", "open": open("verse's title")})
	pass()
	st = status("person")
	if f.openedCount() != 1 || st["adopted"] != true || st["number"] != int64(7) {
		t.Fatalf("adopting #7: %d opened, status %v, want #7 adopted and nothing opened", f.openedCount(), st)
	}
	ask("person", entity.Obj{"to": "branch.feature-7", "open": open("verse's title, again")})
	pass()
	if f.editCount() != 1 {
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
