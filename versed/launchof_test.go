package versed

import (
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// The spec's envelope reaches the launch: the doer and what it works with, the autonomy, the
// gates as the runner reads them (their step half), the answer contract, and the provenance.
func TestLaunchOfCarriesTheSpec(t *testing.T) {
	l, err := launchOf("rev-1", launch.Spec{
		Prompt:  "review it",
		Doer:    runner.Doer{Agent: "verse", With: runner.With{Git: []runner.Repo{{Repo: "acme/app"}}}},
		Timeout: "1h",
		Policy:  task.Policy{{Name: "release", Match: &task.Match{System: "git", Verb: "push"}, Then: task.Then{Mode: task.ThenAsk, Ask: &task.Ask{Need: 2, Within: "4h", Default: task.Deny, Labels: map[string]string{"slack": "C1"}}}}, {Then: task.Then{Mode: task.ThenAllow}}},
		Subject: `demo.pr."1"`, Chain: "review",
	}, "verse.charters.review")
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "rev-1" || l.Prompt != "review it" || l.Doer.Agent != "verse" || l.Policy.Default() != task.ThenAllow || l.Timeout != "1h" {
		t.Errorf("launch = %+v", l)
	}
	if len(l.Doer.With.Git) != 1 || l.Doer.With.Git[0].Repo != "acme/app" {
		t.Errorf("repos = %+v, want the spawn's", l.Doer.With.Git)
	}
	if len(l.Policy) != 2 || l.Policy[0].Name != "release" || l.Policy[0].Then.Ask.Need != 2 || l.Policy[0].Then.Ask.Within != "4h" ||
		l.Policy[0].Then.Ask.Default != task.Deny || l.Policy[0].Match.System != "git" || l.Policy[0].Match.Verb != "push" {
		t.Errorf("policy = %+v, want the spawn's whole", l.Policy)
	}
	if l.Provenance.Principal != "verse.charters.review" || l.Provenance.Subject != `demo.pr."1"` || l.Provenance.Chain != "review" {
		t.Errorf("provenance = %+v", l.Provenance)
	}
}

// A spec that names no policy produces none, so the runner's default stands: resolving
// "unstated" to a value would change every existing charter.
func TestLaunchOfLeavesUnstatedAxesUnset(t *testing.T) {
	l, err := launchOf("p1", launch.Spec{Prompt: "go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Policy) != 0 || l.Doer.Annotations != nil {
		t.Fatalf("unstated axes resolved to %+v, want them unset", l)
	}
}

// The model credential verse resolved rides as the doer's annotation, the key's name, beside
// whatever annotations the charter wrote, which are left as they were.
func TestLaunchOfPutsTheCredentialOnTheDoer(t *testing.T) {
	spec := launch.Spec{Prompt: "go", VirtualKey: "review-key", Doer: runner.Doer{Annotations: map[string]string{"x": "y"}}}
	l, err := launchOf("p1", spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if l.Doer.Annotations[AnnotationVirtualKey] != "review-key" || l.Doer.Annotations["x"] != "y" {
		t.Errorf("annotations = %v", l.Doer.Annotations)
	}
	if _, in := spec.Doer.Annotations[AnnotationVirtualKey]; in {
		t.Error("launchOf wrote into the charter's annotations")
	}
}

// A contract that cannot be meant fails the firing, before a run is paid for.
func TestLaunchOfRefusesAContractItCannotParse(t *testing.T) {
	var spec launch.Spec
	if err := codec.Unmarshal([]byte("prompt: go\nanswer:\n  define: {x: 1}\n  accept: {x: 1}\n"), &spec, codec.Tony); err != nil {
		t.Fatal(err)
	}
	if _, err := launchOf("x", spec, ""); err == nil || !strings.Contains(err.Error(), "answer contract") {
		t.Fatalf("err = %v, want the contract refused", err)
	}
}
