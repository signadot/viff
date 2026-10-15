package launch

import (
	"reflect"
	"testing"

	"github.com/signadot/verse/task"
)

// A spawn's repos bind the way a charter needs: which repository, which branch it starts from and
// which existing branches it may push come from the firing; which NEW branches it may create do
// not, since a pattern is how far a run may reach.
func TestBindExpandsARepoButNotItsPatterns(t *testing.T) {
	spec := Spec{Repos: []task.Repo{{
		Repo: "$subject.repo", Access: task.AccessWrite, From: "$subject.head",
		Push: &task.RepoPush{New: []string{"$subject.repo/*"}, Existing: []string{"$subject.head"}, Force: true},
	}}}
	got := spec.Bind(Binding{"subject.repo": "acme/app", "subject.head": "fix/typo"}).Repos
	want := []task.Repo{{
		Repo: "acme/app", Access: task.AccessWrite, From: "fix/typo",
		Push: &task.RepoPush{New: []string{"$subject.repo/*"}, Existing: []string{"fix/typo"}, Force: true},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bound repos:\n got %+v %+v\nwant %+v %+v", got[0], *got[0].Push, want[0], *want[0].Push)
	}
	// Pure: the charter's own spec is untouched, and the bound one shares nothing with it.
	if spec.Repos[0].Push.Existing[0] != "$subject.head" || spec.Repos[0].Repo != "$subject.repo" {
		t.Fatalf("Bind changed the charter's spec: %+v", spec.Repos[0])
	}
	got[0].Push.New[0] = "changed"
	if spec.Repos[0].Push.New[0] != "$subject.repo/*" {
		t.Fatal("a bound repo aliases the charter's pattern slice")
	}
}
