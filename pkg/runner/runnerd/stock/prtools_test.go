package stock

import (
	"slices"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/verse"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
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

// THE PULL REQUEST READS ARE OFFERED WHERE THE FORGE IS A GITHUB, as the writes are, and only
// where the environment gives them: an environment giving create-pr alone leaves a task with no
// read. Each is the step its verb is, on the fields the runner fills — the repository, and the
// number as one spelling however the model wrote it, so an ask row's answer meets the act.
func TestThePullRequestReadsAreOfferedAsTheirSteps(t *testing.T) {
	ws := &workspace.Workspace{Branch: "verse/auto/t1",
		Trees: []*workspace.Tree{{Repo: workspace.Repo{Repo: runner.Repo{Repo: "acme/app", From: "main"}}}}}
	names := func(tools []verse.Tool) (out []string) {
		for _, t := range tools {
			out = append(out, t.Name)
		}
		return out
	}
	all := names(repoTools(nil, ws, true))
	for _, want := range []string{listPRsTool, readPRTool} {
		if !slices.Contains(all, want) {
			t.Errorf("a GitHub forge offers %v, want %s among them", all, want)
		}
	}
	if forgeless := names(repoTools(nil, ws, false)); slices.Contains(forgeless, listPRsTool) || slices.Contains(forgeless, readPRTool) {
		t.Errorf("a forge that is not a GitHub offers %v", forgeless)
	}
	writesOnly := systems.Environment{Systems: systems.Systems{"github": {{
		Attributes: map[string]string{"repo": "github.com/acme/app"},
		Verbs:      map[string][]systems.Shape{"create-pr": {{}}}}}}}
	if left := names(offered(repoTools(nil, ws, true), writesOnly)); slices.Contains(left, listPRsTool) || slices.Contains(left, readPRTool) || !slices.Contains(left, createPRTool) {
		t.Errorf("an environment giving create-pr alone leaves %v", left)
	}

	read := readPRToolFor(nil, ws, []string{"app"})
	if read.Step != task.StepGitHubReadPR {
		t.Fatalf("github-read-pr is the step %q", read.Step)
	}
	for _, pr := range []any{7, 7.0, "#7", " 7"} {
		if f, ok := read.Fields(map[string]any{"pr": pr}); !ok || f["repo"] != "github.com/acme/app" || f["pr"] != "7" {
			t.Errorf("pr %#v: fields %v, %v; want the repository and pr 7", pr, f, ok)
		}
	}
	if _, ok := read.Fields(map[string]any{"pr": "seven"}); ok {
		t.Error("a pr that is not a number has fields")
	}
	list := listPRsToolFor(nil, ws, []string{"app"})
	if f, ok := list.Fields(map[string]any{}); list.Step != task.StepGitHubListPRs || !ok || len(f) != 1 || f["repo"] != "github.com/acme/app" {
		t.Errorf("github-list-prs is the step %q with %v", list.Step, f)
	}
}

// A DRAFT IS A FIELD OF THE PROPOSAL, so an environment can give drafts only: a shape naming
// `draft: true` allows a create-pr asking for a draft, however the model spelled it, and refuses
// one opening ready. An update says nothing about it.
func TestAnEnvironmentCanGiveDraftsOnly(t *testing.T) {
	ws := &workspace.Workspace{Branch: "verse/auto/t1",
		Trees: []*workspace.Tree{{Repo: workspace.Repo{Repo: runner.Repo{Repo: "acme/app", From: "main"}}}}}
	names := []string{ws.Trees[0].Repo.Name()}
	create, update := prTool(nil, ws, names, createPRTool), prTool(nil, ws, names, updatePRTool)
	draftsOnly := systems.Environment{Systems: systems.Systems{"github": {{
		Attributes: map[string]string{"repo": "github.com/acme/app"},
		Verbs:      map[string][]systems.Shape{"create-pr": {{"draft": ir.FromBool(true)}}}}}}}
	for _, d := range []any{true, "true"} {
		f, _ := create.Fields(map[string]any{"draft": d})
		if f["draft"] != "true" || !draftsOnly.Allows(task.StepGitHubCreatePR, systems.FieldsDoc(task.StepGitHubCreatePR, f)) {
			t.Errorf("draft %#v: fields %v, want a draft the environment allows", d, f)
		}
	}
	for _, in := range []map[string]any{{}, {"draft": false}} {
		f, _ := create.Fields(in)
		if f["draft"] != "false" || draftsOnly.Allows(task.StepGitHubCreatePR, systems.FieldsDoc(task.StepGitHubCreatePR, f)) {
			t.Errorf("%v: fields %v, want a ready pull request the environment refuses", in, f)
		}
	}
	if f, _ := update.Fields(map[string]any{"draft": true}); f["draft"] != "" {
		t.Errorf("update-pr names draft %q, want nothing", f["draft"])
	}
}
