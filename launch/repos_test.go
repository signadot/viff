package launch

import (
	"reflect"
	"testing"

	"github.com/signadot/verse/runner"
)

// A repository's name and its base, and a tracker's repository, are what a charter has reason
// to build out of the firing. Nothing in the bound spec aliases the charter's.
func TestBindExpandsARepoAndItsTracker(t *testing.T) {
	spec := Spec{Doer: runner.Doer{With: runner.With{
		Git:      []runner.Repo{{Repo: "$subject.repo", From: "$subject.head"}},
		GitIssue: []runner.Tracker{{Repo: "$subject.repo"}},
	}}}
	got := spec.Bind(Binding{"subject.repo": "acme/app", "subject.head": "fix/typo"}).Doer.With
	want := runner.With{
		Git:      []runner.Repo{{Repo: "acme/app", From: "fix/typo"}},
		GitIssue: []runner.Tracker{{Repo: "acme/app"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bound with:\n got %+v\nwant %+v", got, want)
	}
	if spec.Doer.With.Git[0].Repo != "$subject.repo" || spec.Doer.With.GitIssue[0].Repo != "$subject.repo" {
		t.Fatalf("Bind changed the charter's spec: %+v", spec.Doer.With)
	}
	got.Git[0].Repo = "changed"
	if spec.Doer.With.Git[0].Repo != "$subject.repo" {
		t.Fatal("a bound repo aliases the charter's")
	}
}
