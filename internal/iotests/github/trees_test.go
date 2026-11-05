package github_test

import (
	"context"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	verseio "github.com/signadot/verse/pkg/io"
	"github.com/signadot/verse/pkg/io/github"
)

// AN OPEN PULL REQUEST CARRIES THE TREES IT CHANGES: the sha of each configured directory at the
// commit it proposes, where that differs from the directory at the head's merge base, read once
// per head and kept. A pull request branched before the base's copy changed, still carrying the
// older copy, changes nothing; one that removes the directory changes it to the empty tree; one
// in a repository that never had the directory changes nothing; and a closed pull request is not
// asked about.
func TestAnOpenPullRequestCarriesTheTreesItChanges(t *testing.T) {
	f := newFake()
	r := f.repo("signadot/verse")
	r.pr(1, "changes the charters", "open", "2026-10-01T00:00:00Z", "head1")
	r.pr(2, "removes the charters", "open", "2026-10-01T00:00:00Z", "head2")
	r.pr(5, "changes something else", "open", "2026-10-01T00:00:00Z", "head5")
	r.pr(6, "never had charters", "open", "2026-10-01T00:00:00Z", "head6")
	r.pr(3, "merged long ago", "closed", "2026-09-01T00:00:00Z", "head3")
	r.pr(4, "branched before the charters last changed", "open", "2026-10-01T00:00:00Z", "head4")
	r.trees = map[string][]fakeEntry{
		"head1":  {{".verse", "tree", "verse1"}, {"README", "blob", "r1"}},
		"verse1": {{"charters", "tree", "charters1"}},
		"head2":  {{"README", "blob", "r2"}},
		"head3":  {{".verse", "tree", "verse1"}},
		"head4":  {{".verse", "tree", "verse0"}},
		"base0":  {{".verse", "tree", "verse0"}},
		"verse0": {{"charters", "tree", "charters0"}},
		"head5":  {{".verse", "tree", "verse0"}, {"README", "blob", "r5"}},
		"head6":  {{"README", "blob", "r6"}},
		"base6":  {{"README", "blob", "r0"}},
	}
	// Every open head branched from base0, whose charters are charters0; main has since moved on.
	r.mergeBases = map[string]string{"head1": "base0", "head2": "base0", "head4": "base0", "head5": "base0", "head6": "base6"}
	tree, err := github.ParseTree(".verse/charters")
	if err != nil || tree.Name != "charters" || tree.Path != ".verse/charters" {
		t.Fatalf("ParseTree = %+v, %v", tree, err)
	}
	src := newSource(t, f, github.Config{Repos: []string{"signadot/verse"}, Trees: []github.Tree{tree}})
	reflect := func() map[string]entity.Obj {
		t.Helper()
		rn, err := src.Reflect(context.Background(), entity.Ref{})
		if err != nil {
			t.Fatal(err)
		}
		return payloads(rn)
	}
	got := reflect()
	pr := func(n int) entity.Obj { return got[src.ItemRef("pr", "signadot/verse", n).String()] }
	if tr, _ := pr(1)["changes"].(map[string]any); tr["charters"] != "charters1" {
		t.Fatalf("pull request 1's changes = %v, want charters1", pr(1)["changes"])
	}
	if tr, _ := pr(2)["changes"].(map[string]any); tr["charters"] != verseio.EmptyTree {
		t.Errorf("a pull request removing the directory changes = %v, want the empty tree", pr(2)["changes"])
	}
	for _, n := range []int{5, 6} {
		if pr(n)["changes"] != nil {
			t.Errorf("pull request %d leaves the directory alone and carries a change: %v", n, pr(n)["changes"])
		}
	}
	if pr(3)["changes"] != nil {
		t.Errorf("a closed pull request carries a change: %v", pr(3)["changes"])
	}
	if pr(4)["changes"] != nil {
		t.Errorf("a pull request carrying its merge base's charters changes them: %v", pr(4)["changes"])
	}
	asked, compared := r.treeAsks, r.compareAsks
	got = reflect()
	if r.treeAsks != asked || r.compareAsks != compared {
		t.Errorf("a head already read was asked about again: %d and %d requests, then %d and %d", asked, compared, r.treeAsks, r.compareAsks)
	}
	if tr, _ := pr(1)["changes"].(map[string]any); tr["charters"] != "charters1" {
		t.Errorf("the second pass lost the change: %v", pr(1)["changes"])
	}
	for _, bad := range []string{"", "a/b.c", "x=", "na me=dir"} {
		if _, err := github.ParseTree(bad); err == nil {
			t.Errorf("ParseTree(%q) was taken", bad)
		}
	}
	if tr, err := github.ParseTree("ch=.verse/charters"); err != nil || tr.Name != "ch" {
		t.Errorf("a named tree = %+v, %v", tr, err)
	}
}
