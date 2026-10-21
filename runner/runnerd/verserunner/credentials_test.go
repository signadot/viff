package verserunner

import (
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/parse"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/claude"
	"github.com/signadot/verse/runner/runnerd/codex"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// ceiling reads a ceiling as a runner's config writes it.
func ceiling(t *testing.T, text string) systems.Ceiling {
	t.Helper()
	node, err := parse.Parse([]byte(text))
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	var c systems.Ceiling
	if err := gomap.FromTonyIR(node, &c); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return c
}

// THE RUNNER'S SYSTEMS ARE THE OPERATOR'S SAY: each performed system may have a section naming
// the variable its credential is read from and a ceiling of blocks; a section for a system there
// is not, or the runner does not perform, or a ceiling its document refuses, is refused as the
// configuration is loaded. The ceilings are published with every offer as written, and the
// credentials are read from the environment at start.
func TestTheRunnersSystemsAndCeilings(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		says string
	}{
		{Config{Systems: map[string]System{"gitlab": {}}}, "no system is called that"},
		{Config{Systems: map[string]System{"files": {}}}, "a section names a credential, an api or a ceiling"},
		{Config{Systems: map[string]System{"git": {}}}, "does not perform git"},
		{Config{Repos: &Repos{Mirrors: "/m"}, Systems: map[string]System{"linear-issue": {Ceiling: ceiling(t, `{create: {}}`)}}}, "does not perform linear-issue"},
		{Config{Repos: &Repos{Mirrors: "/m", Host: "ghe.corp"}, Systems: map[string]System{"github": {}}}, "does not perform github"},
		{Config{Repos: &Repos{Mirrors: "/m"}, Systems: map[string]System{"git": {Ceiling: ceiling(t, `{repo: "github.com/acme/**", fly: {}}`)}}}, `no attribute or verb "fly"`},
		{Config{Repos: &Repos{Mirrors: "/m"}, Systems: map[string]System{"git": {Ceiling: ceiling(t, `{repo: "github.com/acme/**", push: {ref: main}}`)}}}, "push[0].ref"},
		{Config{Repos: &Repos{Mirrors: "/m"}, Systems: map[string]System{"git": {API: "https://x"}}}, "`api` is where a GitHub forge's API is"},
	} {
		if err := c.cfg.Check(); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%+v: %v, want %q", c.cfg.Systems, err, c.says)
		}
	}

	cfg := Config{Repos: &Repos{Mirrors: "/m", Forge: "/forge"}, Systems: map[string]System{
		"git":          {Credential: "VERSE_TEST_GIT", Ceiling: ceiling(t, `{repo: "github.com/acme/**", fetch: {}, push: {ref: "refs/heads/verse/**"}}`)},
		"linear-issue": {Credential: "VERSE_TEST_LINEAR", Ceiling: ceiling(t, `{create: {team: ENG}, comment: {}}`)},
	}}
	if err := cfg.Check(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.performs(); !slices.Equal(got, []string{"git", "git-issue", "linear-issue"}) {
		t.Errorf("performs %v", got)
	}
	for _, o := range cfg.Describe(Secrets{PortkeyAPIKey: "k"}, nil).Offers {
		git := o.Systems["git"].Ceiling
		if len(git) != 1 || git[0].Attributes["repo"][0] != "github.com/acme/**" || len(git[0].Verbs["push"]) != 1 {
			t.Errorf("%s offers the git ceiling %+v, want it as written", o.Name(), git)
		}
		if lin := o.Systems["linear-issue"].Ceiling; len(lin) != 1 || lin[0].Verbs["create"][0]["team"] != "ENG" {
			t.Errorf("%s offers the linear ceiling %+v, want it as written", o.Name(), lin)
		}
		if gi, has := o.Systems["git-issue"]; !has || len(gi.Ceiling) != 0 {
			t.Errorf("%s offers git-issue %+v, want it held with no ceiling: closed", o.Name(), gi)
		}
		if _, has := o.Systems["github"]; has {
			t.Errorf("%s offers a github ceiling with no GitHub to perform it at", o.Name())
		}
		if net, has := o.Systems["network"]; !has || len(net.Builds) == 0 {
			t.Errorf("%s holds no network with what it builds: %+v", o.Name(), net)
		}
		if _, has := o.Systems["commands"]; has != (o.Agent != nil) {
			t.Errorf("%s holds commands: %v; an agent is stopped on them and a script is not", o.Name(), has)
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

// A CEILING OF ROWS IS NO CEILING: the allow and deny rows a ceiling was are refused as the
// configuration is read, saying what a ceiling block is.
func TestACeilingOfRowsIsRefused(t *testing.T) {
	c := Config{Systems: map[string]System{"commands": {Ceiling: ceiling(t, `[{then: allow}]`)}}}
	err := c.Check()
	if err == nil || !strings.Contains(err.Error(), "systems.commands.ceiling") || !strings.Contains(err.Error(), `"then"`) {
		t.Errorf("a ceiling of rows: %v, want it refused naming the key", err)
	}
}

// A GIT CEILING KEEPS TO THE RUNNER'S FORGE: each block names its repositories, each within
// `<host>/**`, since the runner fetches from nowhere else.
func TestAGitCeilingKeepsToTheForge(t *testing.T) {
	for text, says := range map[string]string{
		`{repo: "github.com/acme/**", fetch: {}}`:           "",
		`{repo: ["acme/app", "github.com/x/*"], fetch: {}}`: "",
		`{repo: "gitlab.com/acme/**", fetch: {}}`:           "beyond the runner's forge (github.com/**)",
		`{repo: "**", fetch: {}}`:                           "beyond the runner's forge",
		`{fetch: {}}`:                                       "names no repo",
	} {
		c := Config{Repos: &Repos{Mirrors: "/m"}, Systems: map[string]System{"git": {Ceiling: ceiling(t, text)}}}
		err := c.Check()
		if says == "" && err != nil || says != "" && (err == nil || !strings.Contains(err.Error(), says)) {
			t.Errorf("%s: %v, want %q", text, err, says)
		}
	}
	ghes := Config{Repos: &Repos{Mirrors: "/m", Host: "ghe.corp"}, Systems: map[string]System{"git": {Ceiling: ceiling(t, `{repo: "github.com/acme/**", fetch: {}}`)}}}
	if err := ghes.Check(); err == nil || !strings.Contains(err.Error(), "ghe.corp/**") {
		t.Errorf("github.com's repositories on a GHES runner: %v", err)
	}
}

// THE SYSTEMS A RUNNER STOPS INSIDE A RUN TAKE A CEILING AND NOTHING ELSE: a `commands` section
// with a ceiling is the operator's line over every task's shell, published on the offers that
// stop commands and held at install, so a spawn whose environment gives a command beyond it is
// refused saying what the ceiling gives; one with no section is closed; a credential on such a
// section is refused, since the runner holds none.
func TestAnInnerSystemsCeilingIsTheOperatorsLine(t *testing.T) {
	c := Config{Systems: map[string]System{"commands": {Ceiling: ceiling(t, `{run: {command: "make *"}}`)}}}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	d := c.Describe(Secrets{PortkeyAPIKey: "k"}, []string{runner.NetworkOpen})
	agent := d.Offers[0]
	if cmds := agent.Systems["commands"]; len(cmds.Ceiling) != 1 || cmds.Ceiling[0].Verbs["run"][0]["command"] != "make *" {
		t.Errorf("the agent's commands ceiling: %+v", cmds)
	}
	if files := agent.Systems["files"]; len(files.Ceiling) != 0 {
		t.Errorf("files, with no section: %+v, want it closed", files)
	}
	launch := func(env systems.Environment) *runner.Launch {
		return &runner.Launch{ID: "x", Prompt: "p", Agent: &runner.Agent{Name: AgentName}, Environment: env,
			Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}}
	}
	cmds := func(shape systems.Shape) systems.Environment {
		e := envOf(netOpen, nil, nil)
		e.Systems["commands"] = []systems.Block{{Verbs: map[string][]systems.Shape{"run": {shape}}}}
		return e
	}
	if err := runnerd.CheckLaunch(launch(cmds(systems.Shape{"command": "make test"})), d, nil); err != nil {
		t.Errorf("a command within the ceiling: %v", err)
	}
	err := runnerd.CheckLaunch(launch(cmds(systems.Shape{})), d, nil)
	if err == nil || !strings.Contains(err.Error(), "beyond this runner's ceiling for commands") || !strings.Contains(err.Error(), "{command: make *}") {
		t.Errorf("any command under a ceiling of make: %v", err)
	}
	if err := runnerd.CheckLaunch(launch(giving(envOf(netOpen, nil, nil), "files")), d, nil); err == nil || !strings.Contains(err.Error(), "the ceiling gives nothing") {
		t.Errorf("files, closed: %v", err)
	}
	bad := Config{Systems: map[string]System{"commands": {Credential: "K"}}}
	if err := bad.Check(); err == nil || !strings.Contains(err.Error(), "no credential") {
		t.Errorf("a credential on commands: %v", err)
	}
}

// OPEN INSIDE IS EVERY SYSTEM A RUN IS STOPPED ON, UNDER AN OPEN CEILING: what `verse up`'s
// runner, whose operator is its tasks' author, is configured with.
func TestOpenInsideOpensWhatARunIsStoppedOn(t *testing.T) {
	open := OpenInside()
	if got := slices.Sorted(maps.Keys(open)); !slices.Equal(got, slices.Sorted(maps.Keys(inner))) {
		t.Fatalf("OpenInside covers %v, want %v", got, slices.Sorted(maps.Keys(inner)))
	}
	c := Config{Systems: open}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	d := c.Describe(Secrets{PortkeyAPIKey: "k"}, []string{runner.NetworkOpen})
	env := giving(envOf(netOpen, nil, nil), "files", "commands", "agent", "question")
	l := &runner.Launch{ID: "x", Prompt: "p", Agent: &runner.Agent{Name: AgentName}, Environment: env, Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}}
	if err := runnerd.CheckLaunch(l, d, nil); err != nil {
		t.Errorf("every verb of what a run is stopped on, under OpenInside: %v", err)
	}
}

// WHAT THE RUNNER DESCRIBES: its version; its agents, the verse agent first (the default), each
// with the settings it takes narrowed by the operator (Config.Agents); and every offer holding the
// network, with what the sandbox builds of it beside the operator's ceiling, as written.
func TestDescribePublishesBuildsAndSettings(t *testing.T) {
	c := Config{
		Models: Models{Model: "claude-sonnet-5"},
		Codex:  &codex.Config{},
		Agents: map[string]map[string][]string{AgentName: {runner.SettingModel: {"claude-*"}}, codex.Agent: {"effort": {"low", "high"}}},
		Systems: map[string]System{
			"network":  {Ceiling: ceiling(t, `{mode: [none, allow], tcp-client: {host: "*.github.com"}}`)},
			"commands": {Ceiling: systems.Open()}, "files": {Ceiling: systems.Open()},
		},
	}
	d := c.Describe(Secrets{PortkeyAPIKey: "k"}, []string{runner.NetworkNone, runner.NetworkOpen, runner.NetworkAllow})
	if d.Version != runner.DescriptionVersion {
		t.Errorf("version %d, want %d", d.Version, runner.DescriptionVersion)
	}
	if err := d.Check(); err != nil {
		t.Fatalf("the description does not check: %v", err)
	}
	if len(d.Offers) != 3 || d.Offers[0].Name() != AgentName || d.Offers[1].Name() != codex.Agent || !d.Offers[2].Script {
		t.Fatalf("offers %v, want verse, codex, scripts", []string{d.Offers[0].Name(), d.Offers[1].Name(), d.Offers[2].Name()})
	}
	verse, cx := d.Offers[0].Agent, d.Offers[1].Agent
	if m := verse.Settings[runner.SettingModel]; strings.Join(m.Values, ",") != "claude-*" || !m.Default {
		t.Errorf("verse's model: %+v, want claude-* with the configured default", m)
	}
	if e := cx.Settings["effort"]; strings.Join(e.Values, ",") != "low,high" || !e.Default {
		t.Errorf("codex's effort: %+v", e)
	}
	if m := cx.Settings[runner.SettingModel]; len(m.Values) != 0 || !m.Default {
		t.Errorf("codex's model, not narrowed: %+v", m)
	}
	for _, o := range d.Offers {
		net := o.Systems["network"]
		if len(net.Builds) != 1 || strings.Join(net.Builds[0].Attributes["mode"], ",") != "none,open,allow" {
			t.Errorf("%s: network builds %+v, want the sandbox's", o.Name(), net.Builds)
		}
		if len(net.Ceiling) != 1 || strings.Join(net.Ceiling[0].Attributes["mode"], ",") != "none,allow" {
			t.Errorf("%s: network ceiling %+v, want it as written", o.Name(), net.Ceiling)
		}
	}
	if m := (Config{}).Describe(Secrets{PortkeyAPIKey: "k"}, nil).Offers[0].Agent.Settings[runner.SettingModel]; m.Default {
		t.Error("a verse agent with no configured model has no default of its own")
	}
	if d := (Config{Codex: &codex.Config{}}).Describe(Secrets{}, nil); d.Offers[0].Name() != codex.Agent {
		t.Errorf("with no model key, codex is the default: %s", d.Offers[0].Name())
	}
}

// THE OPERATOR NARROWS WHAT AN AGENT TAKES, and nothing else: an agent the runner runs, a setting
// it takes, a list that takes something.
func TestTheAgentsSectionIsChecked(t *testing.T) {
	for says, agents := range map[string]map[string]map[string][]string{
		"runs no agent":                  {"gemini": {runner.SettingModel: {"x"}}},
		`takes no setting "temperature"`: {AgentName: {"temperature": {"0.2"}}},
		"an empty list takes nothing":    {codex.Agent: {"effort": {}}},
		`"minimal" is none of low`:       {claude.Agent: {"effort": {"high", "minimal"}}},
	} {
		c := Config{Agents: agents}
		if err := c.Check(); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%v: %v, want it refused saying %q", agents, err, says)
		}
	}
	c := Config{Agents: map[string]map[string][]string{codex.Agent: {"effort": {"high"}, runner.SettingModel: {"gpt-5*"}}}}
	if err := c.Check(); err != nil {
		t.Errorf("a narrowing of codex: %v", err)
	}
	// Claude's efforts narrowed to two of them: the offer takes those two and nothing else.
	cl := Config{Claude: &claude.Config{}, Systems: map[string]System{"commands": {Ceiling: systems.Open()}, "files": {Ceiling: systems.Open()}}, Agents: map[string]map[string][]string{claude.Agent: {"effort": {"low", "high"}}}}
	if err := cl.Check(); err != nil {
		t.Errorf("a narrowing of claude's efforts: %v", err)
	}
	if e := cl.settings(claude.Agent)["effort"]; e.Takes("high") != nil || e.Takes("max") == nil {
		t.Errorf("claude's narrowed effort: %+v", e)
	} // A configured default is held to the narrowing: the offer would refuse it named and run it
	// unnamed.
	off := Config{Models: Models{Model: "gpt-5"}, Agents: map[string]map[string][]string{AgentName: {runner.SettingModel: {"claude-*"}}}}
	if err := off.Check(); err == nil || !strings.Contains(err.Error(), "agents.verse.model: the configured default model is outside it") {
		t.Errorf("a default outside the narrowing: %v", err)
	}
	on := Config{Models: Models{Model: "claude-sonnet-5"}, Agents: map[string]map[string][]string{AgentName: {runner.SettingModel: {"claude-*"}}}}
	if err := on.Check(); err != nil {
		t.Errorf("a default within the narrowing: %v", err)
	}
}

// A NETWORK CEILING THE SANDBOX CAN'T MEET IS REFUSED WHEN THE RUNNER STARTS, not at each spawn:
// an unconfined sandbox builds only the open network.
func TestARunnerRefusesACeilingItsSandboxCantBuild(t *testing.T) {
	c := Config{Systems: map[string]System{"network": {Ceiling: ceiling(t, `{mode: [none, allow]}`)}}}
	_, err := Standalone(c, Secrets{}, Tokens{}, runnerd.Unconfined{}, []string{os.Args[0]}, t.TempDir(), t.TempDir(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "mode: none is no network this runner's sandbox builds (it builds: open)") {
		t.Errorf("a ceiling of none and allow on an unconfined sandbox: %v", err)
	}
	if _, err := Standalone(Config{Systems: map[string]System{"network": {Ceiling: ceiling(t, `{mode: open}`)}}}, Secrets{}, Tokens{},
		runnerd.Unconfined{}, []string{os.Args[0]}, t.TempDir(), t.TempDir(), io.Discard); err != nil {
		t.Errorf("a ceiling of open on an unconfined sandbox: %v", err)
	}
}

// CODEX'S AND CLAUDE'S OFFERS SAY WHAT EACH CAN'T RUN WITHOUT, and a runner whose ceilings can't
// give it does not start: no spawn of that agent could ever be written there.
func TestCodexAndClaudeRequireTheirShellAndEdits(t *testing.T) {
	open := map[string]System{"commands": {Ceiling: systems.Open()}, "files": {Ceiling: systems.Open()}}
	c := Config{Codex: &codex.Config{}, Systems: open}
	if err := c.Check(); err != nil {
		t.Fatalf("a codex runner whose ceilings give both: %v", err)
	}
	d := c.Describe(Secrets{}, []string{"open"})
	if got := d.Offers[0].Requires; d.Offers[0].Name() != codex.Agent || !slices.Equal(got, []string{task.StepCommandsRun, task.StepFilesWrite}) {
		t.Errorf("codex's offer requires %v", got)
	}
	if err := d.Check(); err != nil {
		t.Errorf("the description: %v", err)
	}
	closed := Config{Codex: &codex.Config{}, Systems: map[string]System{"files": {Ceiling: systems.Open()}}}
	if err := closed.Check(); err == nil || !strings.Contains(err.Error(), "systems.commands.ceiling: codex can't run without commands.run") {
		t.Errorf("a codex runner whose commands ceiling is closed: %v", err)
	}
	// Claude's are the same steps, held the same way.
	closed = Config{Claude: &claude.Config{}, Systems: map[string]System{"files": {Ceiling: systems.Open()}}}
	if err := closed.Check(); err == nil || !strings.Contains(err.Error(), "systems.commands.ceiling: claude can't run without commands.run") {
		t.Errorf("a claude runner whose commands ceiling is closed: %v", err)
	}
}
