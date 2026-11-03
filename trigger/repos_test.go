package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// What a spawn says about its repositories is checked when the charter is installed, with its
// author present: a repository that is not `owner/name` is refused there, and a well-formed list
// installs and is carried as written.
func TestASpawnsReposAreCheckedAtInstall(t *testing.T) {
	rule := func(repos ...runner.Repo) Trigger {
		var git []systems.Block
		for _, r := range repos {
			attrs := map[string]string{"repo": r.Repo}
			if r.From != "" {
				attrs["from"] = r.From
			}
			git = append(git, systems.Block{Attributes: attrs, Verbs: map[string][]systems.Shape{"fetch": {{}}}})
		}
		return Trigger{
			Name: "fix", Condition: Condition{Subject: prSubject("open")},
			Action: Action{Patch: entity.Obj{"stage": "fixing"}.Payload(),
				Spawn: &launch.Spec{Prompt: "p", Agent: &runner.Agent{Name: "verse"},
					Environment: systems.Environment{Systems: systems.Systems{"git": git}}}},
		}
	}
	eng := New(entitytest.Store(t), task.AutoResolver{})
	err := eng.AddTrigger(rule(runner.Repo{Repo: "not a repo"}))
	if err == nil || !strings.Contains(err.Error(), "spawn.environment.systems.git") {
		t.Fatalf("a malformed repository installed: %v", err)
	}
	eng = New(entitytest.Store(t), task.AutoResolver{})
	if err := eng.AddTrigger(rule(
		runner.Repo{Repo: "$[subject.value.repo]", From: "$[subject.value.head]"},
		runner.Repo{Repo: "acme/docs"},
	)); err != nil {
		t.Fatalf("a well-formed repos list is refused: %v", err)
	}
}
