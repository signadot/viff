package verserunner

import (
	"testing"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// AN UPDATE KEEPS THE BASE IT DOES NOT NAME: a proposal opens into the branch the checkout started
// from, but an update that says nothing about the base leaves the pull request's where it is, and
// a policy sees a base only when the update moves it.
func TestAnUpdateKeepsTheBaseItDoesNotName(t *testing.T) {
	ws := &workspace.Workspace{Branch: "verse/auto/t1",
		Trees: []*workspace.Tree{{Repo: workspace.Repo{Repo: runner.Repo{Repo: "acme/app", From: "main"}}}}}
	names := []string{ws.Trees[0].Repo.Name()}
	create, update := prTool(nil, ws, names, createPRTool), prTool(nil, ws, names, updatePRTool)
	if f, _ := create.Fields(map[string]any{}); f["base"] != "main" {
		t.Errorf("create-pr with no base names %q, want the checkout's start, main", f["base"])
	}
	if f, _ := update.Fields(map[string]any{"title": "x"}); f["base"] != "" {
		t.Errorf("update-pr with no base names %q, want none: the pull request keeps its base", f["base"])
	}
	if f, _ := update.Fields(map[string]any{"base": "release"}); f["base"] != "release" {
		t.Errorf("update-pr moving the base names %q, want release", f["base"])
	}
}

// A PROPOSAL'S BASE IS A BRANCH'S NAME however the spawn wrote where the checkout starts: a row
// on `base: main` decides a proposal from a checkout of `refs/heads/main`.
func TestAProposalsBaseIsABranchsName(t *testing.T) {
	ws := &workspace.Workspace{Branch: "verse/auto/t1",
		Trees: []*workspace.Tree{{Repo: workspace.Repo{Repo: runner.Repo{Repo: "acme/app", From: "refs/heads/main"}}}}}
	create := prTool(nil, ws, []string{ws.Trees[0].Repo.Name()}, createPRTool)
	if f, _ := create.Fields(map[string]any{}); f["base"] != "main" {
		t.Errorf("create-pr from refs/heads/main names the base %q, want main", f["base"])
	}
}

// A FETCH IS A STEP, named by its repository, so a row on git.fetch decides it as written.
func TestAFetchIsAStep(t *testing.T) {
	ws := &workspace.Workspace{Trees: []*workspace.Tree{{Repo: workspace.Repo{Repo: runner.Repo{Repo: "acme/app"}}}}}
	fetch := fetchToolFor(nil, ws, []string{"app"})
	if fetch.Step != task.StepGitFetch {
		t.Fatalf("git-fetch is the step %q, want %s", fetch.Step, task.StepGitFetch)
	}
	if f, ok := fetch.Fields(map[string]any{"repo": "app"}); !ok || f["repo"] != "github.com/acme/app" {
		t.Errorf("git-fetch names %v, want the repository", f)
	}
}
