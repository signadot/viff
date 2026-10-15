package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
)

// What a spawn says about its repositories is checked when the charter is installed, with its
// author present: a push on a repository that may only read is refused there, and a well-formed
// list installs and is carried as written.
func TestASpawnsReposAreCheckedAtInstall(t *testing.T) {
	rule := func(repos ...task.Repo) Trigger {
		return Trigger{
			Name: "fix", Condition: Condition{Subject: prSubject("open")},
			Action: Action{Patch: entity.Obj{"stage": "fixing"}.Payload(),
				Spawn: &launch.Spec{Prompt: "p", Repos: repos}},
		}
	}
	eng := New(entitytest.Store(t), task.AutoResolver{})
	err := eng.AddTrigger(rule(task.Repo{Repo: "acme/app", Push: &task.RepoPush{New: []string{"verse/*"}}}))
	if err == nil || !strings.Contains(err.Error(), "`push` needs `access: write`") {
		t.Fatalf("a push on a read-only repository installed: %v", err)
	}
	eng = New(entitytest.Store(t), task.AutoResolver{})
	if err := eng.AddTrigger(rule(
		task.Repo{Repo: "$subject.repo", Access: task.AccessWrite, From: "$subject.head",
			Push: &task.RepoPush{Existing: []string{"$subject.head"}, Force: true}},
		task.Repo{Repo: "acme/docs"},
	)); err != nil {
		t.Fatalf("a well-formed repos list is refused: %v", err)
	}
}
