package viffd

import (
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/launch"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// The spec's envelope reaches the launch: the agent, the environment, the autonomy, the
// gates as the runner reads them (their step half), the answer contract, and the provenance.
func TestLaunchOfCarriesTheSpec(t *testing.T) {
	l, err := launchOf("rev-1", launch.Spec{
		Prompt: "review it",
		Agent:  &runner.Agent{Name: "viff"},
		Environment: systems.Environment{Systems: systems.Systems{"git": {{Attributes: map[string]string{"repo": "acme/app"},
			Verbs: map[string][]systems.Shape{"fetch": {{}}}}}}},
		Timeout:  "1h",
		Autonomy: task.Autonomy{{Name: "release", Match: &task.Match{System: "git", Verb: "push"}, Then: task.Then{Mode: task.ThenAsk, Ask: &task.Ask{Need: 2, Within: "4h", Default: task.Deny, Labels: map[string]string{"slack": "C1"}}}}, {Then: task.Then{Mode: task.ThenAllow}}},
		Subject:  `demo.pr."1"`, Chain: "review",
	}, "mirror.charters.review")
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "rev-1" || l.Prompt != "review it" || l.Who() != "viff" || l.Autonomy.Default() != task.ThenAllow || l.Timeout != "1h" {
		t.Errorf("launch = %+v", l)
	}
	if repos := l.Repos(); len(repos) != 1 || repos[0].Repo != "acme/app" {
		t.Errorf("repos = %+v, want the spawn's", repos)
	}
	if len(l.Autonomy) != 2 || l.Autonomy[0].Name != "release" || l.Autonomy[0].Then.Ask.Need != 2 || l.Autonomy[0].Then.Ask.Within != "4h" ||
		l.Autonomy[0].Then.Ask.Default != task.Deny || l.Autonomy[0].Match.System != "git" || l.Autonomy[0].Match.Verb != "push" {
		t.Errorf("autonomy = %+v, want the spawn's whole", l.Autonomy)
	}
	if l.Provenance.Principal != "mirror.charters.review" || l.Provenance.Subject != `demo.pr."1"` || l.Provenance.Chain != "review" {
		t.Errorf("provenance = %+v", l.Provenance)
	}
}

// A spec that names no autonomy and no agent produces none, so the runner's default stands: resolving
// "unstated" to a value would change every existing charter.
func TestLaunchOfLeavesUnstatedAxesUnset(t *testing.T) {
	l, err := launchOf("p1", launch.Spec{Prompt: "go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Autonomy) != 0 || l.Agent != nil {
		t.Fatalf("unstated axes resolved to %+v, want them unset", l)
	}
}

// The model credential viff resolved rides as the agent's annotation, the key's name, beside
// whatever annotations the charter wrote, which are left as they were.
func TestLaunchOfPutsTheCredentialOnTheAgent(t *testing.T) {
	spec := launch.Spec{Prompt: "go", VirtualKey: "review-key", Agent: &runner.Agent{Name: "viff", Annotations: map[string]string{"x": "y"}}}
	l, err := launchOf("p1", spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if a := l.AgentSettings(); a.Name != "viff" || a.Annotations[AnnotationVirtualKey] != "review-key" || a.Annotations["x"] != "y" {
		t.Errorf("agent = %+v", a)
	}
	if _, in := spec.Agent.Annotations[AnnotationVirtualKey]; in {
		t.Error("launchOf wrote into the charter's annotations")
	}
}

// A PROMPT FOR THE DEFAULT AGENT SPENDS ITS CREDENTIAL TOO: the key rides an agent that names
// none, which is the runner's default with that setting, and the launch is one the runner takes.
func TestLaunchOfPutsTheCredentialOnTheDefaultAgent(t *testing.T) {
	l, err := launchOf("p1", launch.Spec{Prompt: "go", VirtualKey: "review-key"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a := l.AgentSettings(); a.Name != "" || a.Annotations[AnnotationVirtualKey] != "review-key" {
		t.Errorf("agent = %+v, want the default agent carrying the key", a)
	}
	if l.Who() != "" {
		t.Errorf("who = %q, want the runner's default", l.Who())
	}
	if err := l.Check(); err != nil {
		t.Errorf("a prompt-only launch with a credential: %v", err)
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
