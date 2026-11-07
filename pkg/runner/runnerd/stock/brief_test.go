package stock

import (
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/runner/runnerd/verse"
	"github.com/signadot/verse/pkg/systems"
)

// THE BRIEF NAMES EXACTLY THE TOOLS THE TASK WAS LEFT WITH, by system, in the documents' order,
// with the two facts that hold for all of them; a tool the environment does not give is not named, and a
// task with no performed tools gets no paragraph. A tool is its step's, so one whose name is no
// verb's (an MCP server's tool) is offered and named by its step, and dropped where it is not
// given.
func TestTheBriefNamesTheToolsOffered(t *testing.T) {
	all := []verse.Tool{
		{Name: "slack-post"}, {Name: "git-push"}, {Name: "github-issue-create"}, {Name: "git-fetch"},
		{Name: "github-issue-close"}, {Name: "read_file"}, // a runtime tool, no system's
		{Name: "mcp-linear-get_issue", Step: "mcp.call"}, {Name: "mcp-search-query", Step: "mcp.call"},
	}
	env := systems.Environment{Systems: systems.Systems{
		"git":          {{Attributes: map[string]string{"repo": "acme/app"}, Verbs: map[string][]systems.Shape{"push": {{}}, "fetch": {{}}}}},
		"github-issue": {{Attributes: map[string]string{"repo": "acme/app"}, Verbs: map[string][]systems.Shape{"create": {{}}}}},
		"mcp":          {{Attributes: map[string]string{"server": "linear"}, Verbs: map[string][]systems.Shape{"call": {{}}}}},
	}}
	got := toolsBrief(offered(all, env), env)
	want := "What reaches the world from here is these tools, and nothing else: `git-push`, `git-fetch` (git); `github-issue-create` (github-issue); `mcp-linear-get_issue`, `mcp-search-query` (mcp). There is no command-line client"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("the brief:\n%s\nwant it to begin:\n%s", got, want)
	}
	for _, absent := range []string{"slack-post", "github-issue-close", "read_file"} {
		if strings.Contains(got, absent) {
			t.Errorf("the brief names %s, which the task does not have", absent)
		}
	}
	delete(env.Systems, "mcp")
	if got := toolsBrief(offered(all, env), env); strings.Contains(got, "mcp-") {
		t.Errorf("an MCP tool is named where the environment does not give mcp.call: %s", got)
	}
	for _, fact := range []string{"nothing you install there can reach them", "A call the autonomy refuses is a decision"} {
		if !strings.Contains(got, fact) {
			t.Errorf("the brief lacks %q", fact)
		}
	}
	if got := toolsBrief(offered(all, systems.Environment{}), systems.Environment{}); got != "" {
		t.Errorf("a task with no performed tools got a brief: %q", got)
	}
	if got := toolsBrief([]verse.Tool{{Name: "read_file"}}, systems.Environment{}); got != "" {
		t.Errorf("a runtime tool alone got a brief: %q", got)
	}
}

// THE BRIEF NAMES THE SECRETS THE ENVIRONMENT GIVES, with the variable each arrives in, because
// `secret-exec` takes its secret as an argument where every other system's target is part of the
// tool's name: a task that is not told them guesses, and the variable is the wrong guess nearest
// to hand. The sentence is left out where the task has no `secret` tool, and where no block names
// a secret.
func TestTheBriefNamesTheSecretsGiven(t *testing.T) {
	tools := []verse.Tool{{Name: "secret-exec"}}
	one := systems.Environment{Systems: systems.Systems{
		"secret": {{Attributes: map[string]string{"name": "signadot-prod-api-key", "as": "SIGNADOT_API_KEY"},
			Verbs: map[string][]systems.Shape{"exec": {{}}}}},
	}}
	got := toolsBrief(offered(tools, one), one)
	for _, want := range []string{
		"The one secret `secret-exec` may name is `signadot-prod-api-key`, in `SIGNADOT_API_KEY`",
		"its `name` is the secret's own name, not the variable's",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the brief lacks %q:\n%s", want, got)
		}
	}

	// Several: named in order, and the sentence reads as a plural.
	two := systems.Environment{Systems: systems.Systems{
		"secret": {
			{Attributes: map[string]string{"name": "npm-token", "as": "NPM_TOKEN"},
				Verbs: map[string][]systems.Shape{"exec": {{}}}},
			{Attributes: map[string]string{"name": "aws-key", "as": "AWS_SECRET_ACCESS_KEY"},
				Verbs: map[string][]systems.Shape{"exec": {{}}}},
		},
	}}
	got = toolsBrief(offered(tools, two), two)
	want := "The secrets `secret-exec` may name are `aws-key`, in `AWS_SECRET_ACCESS_KEY`; `npm-token`, in `NPM_TOKEN`:"
	if !strings.Contains(got, want) {
		t.Errorf("the brief lacks %q:\n%s", want, got)
	}

	// No `secret` tool: no sentence, even where the environment has a block.
	got = toolsBrief([]verse.Tool{{Name: "git-push"}}, systems.Environment{Systems: systems.Systems{
		"git":    {{Attributes: map[string]string{"repo": "acme/app"}, Verbs: map[string][]systems.Shape{"push": {{}}}}},
		"secret": {{Attributes: map[string]string{"name": "npm-token", "as": "NPM_TOKEN"}}},
	}})
	if strings.Contains(got, "npm-token") {
		t.Errorf("the brief names a secret to a task with no secret tool:\n%s", got)
	}
}
