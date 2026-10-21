package launch

import (
	"reflect"
	"testing"

	"github.com/signadot/verse/systems"
)

// A repository's name and its base, and a tracker's repository, are what a charter has reason
// to build out of the firing. Nothing in the bound spec aliases the charter's.
func TestBindExpandsARepoAndItsTracker(t *testing.T) {
	spec := Spec{Environment: systems.Environment{Systems: systems.Systems{
		"git":       {{Attributes: map[string]string{"repo": "$subject.repo", "from": "$subject.head"}, Verbs: map[string][]systems.Shape{"fetch": {{}}}}},
		"git-issue": {{Attributes: map[string]string{"repo": "$subject.repo"}}},
	}}}
	got := spec.Bind(Binding{"subject.repo": "acme/app", "subject.head": "fix/typo"}).Environment
	want := systems.Environment{Systems: systems.Systems{
		"git":       {{Attributes: map[string]string{"repo": "acme/app", "from": "fix/typo"}, Verbs: map[string][]systems.Shape{"fetch": {{}}}}},
		"git-issue": {{Attributes: map[string]string{"repo": "acme/app"}, Verbs: map[string][]systems.Shape{}}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bound environment:\n got %+v\nwant %+v", got, want)
	}
	if spec.Environment.Systems["git"][0].Attributes["repo"] != "$subject.repo" || spec.Environment.Systems["git-issue"][0].Attributes["repo"] != "$subject.repo" {
		t.Fatalf("Bind changed the charter's spec: %+v", spec.Environment)
	}
	got.Systems["git"][0].Attributes["repo"] = "changed"
	if spec.Environment.Systems["git"][0].Attributes["repo"] != "$subject.repo" {
		t.Fatal("a bound repo aliases the charter's")
	}
}
