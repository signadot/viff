package verserunner

import (
	"strings"
	"testing"

	"github.com/signadot/verse/runner/runnerd/verserunner/agent"
	"github.com/signadot/verse/task"
)

// THE BRIEF NAMES EXACTLY THE TOOLS THE TASK WAS LEFT WITH, by system, in the documents' order,
// with the two facts that hold for all of them; a tool the policy pruned is not named, and a
// task with no performed tools gets no paragraph.
func TestTheBriefNamesTheToolsOffered(t *testing.T) {
	all := []agent.Tool{
		{Name: "slack-post"}, {Name: "git-push"}, {Name: "github-issue-create"}, {Name: "git-fetch"},
		{Name: "github-issue-close"}, {Name: "read_file"}, // a runtime tool, no system's
	}
	allow := task.Then{Mode: task.ThenAllow}
	p := task.Policy{
		{Match: &task.Match{System: "git"}, Then: allow},
		{Match: &task.Match{System: "github-issue", Verb: "create"}, Then: allow},
		{Then: allow},
	}
	got := toolsBrief(offered(all, p))
	want := "What reaches the world from here is these tools, and nothing else: `git-push`, `git-fetch` (git); `github-issue-create` (github-issue). There is no command-line client"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("the brief:\n%s\nwant it to begin:\n%s", got, want)
	}
	for _, absent := range []string{"slack-post", "github-issue-close", "read_file"} {
		if strings.Contains(got, absent) {
			t.Errorf("the brief names %s, which the task does not have", absent)
		}
	}
	for _, fact := range []string{"nothing you install there can reach them", "A call the policy refuses is a decision"} {
		if !strings.Contains(got, fact) {
			t.Errorf("the brief lacks %q", fact)
		}
	}
	if got := toolsBrief(offered(all, task.Policy{{Then: allow}})); got != "" {
		t.Errorf("a task with no performed tools got a brief: %q", got)
	}
	if got := toolsBrief([]agent.Tool{{Name: "read_file"}}); got != "" {
		t.Errorf("a runtime tool alone got a brief: %q", got)
	}
}
