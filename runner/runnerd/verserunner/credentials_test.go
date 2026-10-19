package verserunner

import (
	"slices"
	"strings"
	"testing"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

// THE RUNNER'S SYSTEMS ARE THE OPERATOR'S SAY: each performed system may have a section naming
// the variable its credential is read from and a ceiling of allow and deny rows; a section for a
// system there is not, or the runner does not perform, or a ceiling that asks, is refused as the
// configuration is loaded. The ceilings are published with every offer, completed with `allow`
// for the rest, and the credentials are read from the environment at start.
func TestTheRunnersSystemsAndCeilings(t *testing.T) {
	allow, deny, ask := task.Then{Mode: task.ThenAllow}, task.Then{Mode: task.ThenDeny}, task.Then{Mode: task.ThenAsk}
	for _, c := range []struct {
		cfg  Config
		says string
	}{
		{Config{Systems: map[string]System{"gitlab": {}}}, "no system is called that"},
		{Config{Systems: map[string]System{"git": {}}}, "does not perform git"},
		{Config{Repos: &Repos{Mirrors: "/m"}, Systems: map[string]System{"linear-issue": {Ceiling: task.Policy{{Then: allow}}}}}, "does not perform linear-issue"},
		{Config{Repos: &Repos{Mirrors: "/m", Host: "ghe.corp"}, Systems: map[string]System{"github": {}}}, "does not perform github"},
		{Config{Repos: &Repos{Mirrors: "/m"}, Systems: map[string]System{"git": {Ceiling: task.Policy{{Match: &task.Match{Verb: "push"}, Then: ask}}}}}, "a ceiling allows or denies"},
		{Config{Repos: &Repos{Mirrors: "/m"}, Systems: map[string]System{"git": {Ceiling: task.Policy{{Match: &task.Match{System: "github"}, Then: deny}}}}}, "names the system github"},
		{Config{Repos: &Repos{Mirrors: "/m"}, Systems: map[string]System{"git": {Ceiling: task.Policy{{Match: &task.Match{Verb: "fly"}, Then: deny}}}}}, "no verb"},
		{Config{Repos: &Repos{Mirrors: "/m"}, Systems: map[string]System{"git": {API: "https://x"}}}, "`api` is where a GitHub forge's API is"},
	} {
		if err := c.cfg.Check(); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%+v: %v, want %q", c.cfg.Systems, err, c.says)
		}
	}

	cfg := Config{Repos: &Repos{Mirrors: "/m", Forge: "/forge"}, Systems: map[string]System{
		"git":          {Credential: "VERSE_TEST_GIT", Ceiling: task.Policy{{Name: "no-release", Match: &task.Match{Verb: "push", Params: map[string]string{"ref": "refs/heads/release/**"}}, Then: deny}}},
		"linear-issue": {Credential: "VERSE_TEST_LINEAR", Ceiling: task.Policy{{Match: &task.Match{Verb: "create", Params: map[string]string{"team": "ENG"}}, Then: allow}, {Then: deny}}},
	}}
	if err := cfg.Check(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.performs(); !slices.Equal(got, []string{"git", "git-issue", "linear-issue"}) {
		t.Errorf("performs %v", got)
	}
	for _, o := range cfg.Describe(Secrets{PortkeyAPIKey: "k"}, nil).Offers {
		git := o.Systems["git"].Ceiling
		if len(git) != 3 || git[0].Name != "no-release" || git[0].Match.System != "git" ||
			git[1].Match.Params["repo"] != "github.com/**" || git[1].Then.Mode != task.ThenAllow || git[2].Then.Mode != task.ThenDeny {
			t.Errorf("%s offers the git ceiling %+v, want the row with its system, allow within the forge, and deny for the rest", o.Agent, git)
		}
		if lin := o.Systems["linear-issue"].Ceiling; len(lin) != 2 || lin[1].Then.Mode != task.ThenDeny {
			t.Errorf("%s offers the linear ceiling %+v, want it as written", o.Agent, lin)
		}
		if _, has := o.Systems["github"]; has {
			t.Errorf("%s offers a github ceiling with no GitHub to perform it at", o.Agent)
		}
		if _, has := o.Systems["network"]; has {
			t.Errorf("%s offers network as a system: it is the run's fact, under with", o.Agent)
		}
		if _, has := o.Systems["commands"]; has != (o.Agent != "") {
			t.Errorf("%s/%v holds commands: %v; an agent is stopped on them and a script is not", o.Agent, o.Script, has)
		}
	}
	t.Setenv("VERSE_TEST_GIT", "gt")
	tokens, unset := TokensFromEnv(cfg)
	if tokens.Git != "gt" || tokens.Linear != "" {
		t.Errorf("tokens %+v", tokens)
	}
	if len(unset) != 1 || !strings.Contains(unset[0], "VERSE_TEST_LINEAR (linear-issue)") {
		t.Errorf("unset %v, want the linear variable named", unset)
	}
	if cfg.services(tokens).GitHubAPI != "https://api.github.com" {
		t.Errorf("the GitHub API is %q", cfg.services(tokens).GitHubAPI)
	}
	// github.com itself performs github and github-issue; a directory standing in for it does not.
	if got := (Config{Repos: &Repos{Mirrors: "/m"}}).performs(); !slices.Equal(got, []string{"git", "git-issue", "github", "github-issue"}) {
		t.Errorf("github.com performs %v", got)
	}

	// A GitHub Enterprise Server forge is one line: where its API is.
	ghes := Config{Repos: &Repos{Mirrors: "/m", Host: "ghe.corp"}, Systems: map[string]System{"github": {API: "https://ghe.corp/api/v3"}}}
	if err := ghes.Check(); err != nil {
		t.Fatal(err)
	}
	if !ghes.atGitHub() || !slices.Contains(ghes.performs(), "github-issue") || ghes.services(Tokens{}).GitHubAPI != "https://ghe.corp/api/v3" {
		t.Errorf("GHES: at GitHub %v, performs %v, api %q", ghes.atGitHub(), ghes.performs(), ghes.services(Tokens{}).GitHubAPI)
	}
	if (Config{Repos: &Repos{Mirrors: "/m", Host: "ghe.corp"}}).atGitHub() {
		t.Error("a forge that is not github.com, with no API named, is a GitHub")
	}
	_ = runner.DefaultHost
}

// THE SYSTEMS A RUNNER STOPS INSIDE A RUN TAKE A CEILING AND NOTHING ELSE: a `commands` section
// with a ceiling is the operator's line over every task's shell, published on the offers that
// stop commands and held at install, so a spawn that would ask about a command is refused
// naming both rows; a credential on such a section is refused, since the runner holds none.
func TestAnInnerSystemsCeilingIsTheOperatorsLine(t *testing.T) {
	deny := task.Then{Mode: task.ThenDeny}
	c := Config{Systems: map[string]System{"commands": {Ceiling: task.Policy{{Then: deny}}}}}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	d := c.Describe(Secrets{PortkeyAPIKey: "k"}, []string{runner.NetworkOpen})
	agent := d.Offers[0]
	if cmds := agent.Systems["commands"]; len(cmds.Ceiling) != 1 || cmds.Ceiling[0].Then.Mode != task.ThenDeny {
		t.Errorf("the agent's commands ceiling: %+v", cmds)
	}
	if files := agent.Systems["files"]; len(files.Ceiling) != 1 || files.Ceiling[0].Then.Mode != task.ThenAllow {
		t.Errorf("files, with no section: %+v, want allow", files)
	}
	l := &runner.Launch{ID: "x", Prompt: "p", Doer: runner.Doer{Agent: AgentName, With: runner.With{Network: netOpen}}, Policy: task.Policy{
		{Name: "ship", Match: &task.Match{System: "commands", Verb: "run", Params: map[string]string{"command": "make ship*"}}, Then: task.Then{Mode: task.ThenAsk}},
		{Then: task.Then{Mode: task.ThenAllow}}}}
	err := runnerd.CheckLaunch(l, d, nil)
	if err == nil || !strings.Contains(err.Error(), "policy[0]: this runner's ceiling refuses what the row would ask") || !strings.Contains(err.Error(), "policy[0]: deny everything") {
		t.Errorf("a spawn asking about a command under a ceiling that denies them: %v", err)
	}
	bad := Config{Systems: map[string]System{"commands": {Credential: "K"}}}
	if err := bad.Check(); err == nil || !strings.Contains(err.Error(), "no credential") {
		t.Errorf("a credential on commands: %v", err)
	}
}
