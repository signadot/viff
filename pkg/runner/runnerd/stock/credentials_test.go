package stock

import (
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/parse"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/runner/runnerd/claude"
	"github.com/signadot/verse/pkg/runner/runnerd/codex"
	"github.com/signadot/verse/pkg/runner/runnerd/verse"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
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

// agentWith is a runner configuration offering one agent under the ceilings sys.
func agentWith(name string, sys map[string]System) Config {
	return Config{Agents: map[string]Agent{name: {Systems: sys}}}
}

// THE RUNNER'S SYSTEMS ARE THE OPERATOR'S SAY, A DOER AT A TIME: each doer's section may hold a
// ceiling for each system the runner performs; a section for a system there is not, or the runner
// does not perform, or a ceiling its document refuses, is refused as the configuration is loaded.
// The ceilings are published with the doer's offer as written, and the credentials are read from
// fixed variables at start.
func TestTheRunnersSystemsAndCeilings(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		says string
	}{
		{Config{Script: &Script{Systems: map[string]System{"gitlab": {}}}}, "script.systems.gitlab: no system is called that"},
		{agentWith(AgentName, map[string]System{"files": {}}), "agents.verse.systems.files: a section names a ceiling"},
		{Config{Script: &Script{Systems: map[string]System{"git": {Ceiling: systems.Open()}}}}, "does not perform git"},
		{Config{Repos: &Repos{Mirrors: "/m", Host: "ghe.corp"}, Script: &Script{Systems: map[string]System{"github": {Ceiling: systems.Open()}}}}, "does not perform github"},
		{Config{Repos: &Repos{Mirrors: "/m"}, Script: &Script{Systems: map[string]System{"git": {Ceiling: ceiling(t, `{repo: !regexp "^github\\.com/acme/", fly: {}}`)}}}}, `no attribute or verb "fly"`},
		{Config{Repos: &Repos{Mirrors: "/m"}, Script: &Script{Systems: map[string]System{"git": {Ceiling: ceiling(t, `{repo: !regexp "^github\\.com/acme/", push: {ref: main}}`)}}}}, "push[0].ref"},
		// A script is stopped on its questions alone: its commands and files are one action.
		{Config{Script: &Script{Systems: map[string]System{"commands": {Ceiling: systems.Open()}}}}, "script.systems.commands: the runner does not stop script on commands inside its run; of those, its section may name question"},
	} {
		if err := c.cfg.Check(); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%+v: %v, want %q", c.cfg, err, c.says)
		}
	}

	cfg := Config{Repos: &Repos{Mirrors: "/m", Forge: "/forge"},
		Agents: map[string]Agent{AgentName: {Systems: map[string]System{
			"git":          {Ceiling: ceiling(t, `{repo: !regexp "^github\\.com/acme/", fetch: {}, push: {ref: !regexp "^refs/heads/verse/"}}`)},
			"linear-issue": {Ceiling: ceiling(t, `{create: {team: ENG}, comment: {}}`)},
		}}},
		Script: &Script{Systems: map[string]System{
			"git": {Ceiling: ceiling(t, `{repo: "github.com/acme/svc", push: {ref: refs/heads/main}}`)},
		}},
	}
	if err := cfg.Check(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.performs(); !slices.Equal(got, []string{"git", "git-issue", "linear-issue"}) {
		t.Errorf("performs %v", got)
	}
	// Each doer's offer holds every system the runner performs, under that doer's ceiling: the
	// agent's as the agent's section writes it, the scripts' as theirs does, and a system a
	// section does not name closed for that doer.
	d := cfg.Describe(nil)
	agent, script := d.Offers[0], d.Offers[1]
	if git := agent.Systems["git"].Ceiling; len(git) != 1 || systems.Words(git[0].Attributes["repo"]) != `!regexp ^github\.com/acme/` || len(git[0].Verbs["push"]) != 1 {
		t.Errorf("the agent's git ceiling %+v, want it as its section writes it", git)
	}
	if git := script.Systems["git"].Ceiling; len(git) != 1 || systems.Words(git[0].Attributes["repo"]) != "github.com/acme/svc" {
		t.Errorf("the scripts' git ceiling %+v, want it as their section writes it", git)
	}
	if lin, has := script.Systems["linear-issue"]; !has || len(lin.Ceiling) != 0 {
		t.Errorf("the scripts hold linear-issue %+v, want it held and closed: their section names none", lin)
	}
	for _, o := range d.Offers {
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
	// What the channel holds a task to is its doer's ceiling.
	if got := cfg.ceilings(ScriptDoer)["git"]; len(got) != 1 || systems.Words(got[0].Attributes["repo"]) != "github.com/acme/svc" {
		t.Errorf("the channel's git ceiling for a script: %+v", got)
	}
	if got := cfg.doer(&runner.Launch{Prompt: "p"}); got != AgentName {
		t.Errorf("a prompt naming no agent is %q's, want the first agent offered", got)
	}
	t.Setenv(EnvGitHubToken, "gt")
	t.Setenv(EnvLinearAPIKey, "lin")
	tokens := TokensFromEnv(cfg)
	if tokens.Git != "gt" || tokens.Linear != "lin" || tokens.Slack != "" {
		t.Errorf("tokens %+v", tokens)
	}
	if cfg.services(tokens).GitHubAPI != "https://api.github.com" {
		t.Errorf("the GitHub API is %q", cfg.services(tokens).GitHubAPI)
	}
	// github.com itself performs github and github-issue; a directory standing in for it does not.
	if got := (Config{Repos: &Repos{Mirrors: "/m"}}).performs(); !slices.Equal(got, []string{"git", "git-issue", "github", "github-issue"}) {
		t.Errorf("github.com performs %v", got)
	}

	// A GitHub Enterprise Server forge is one line: where its API is.
	ghes := Config{Repos: &Repos{Mirrors: "/m", Host: "ghe.corp", API: "https://ghe.corp/api/v3"}, Script: &Script{}}
	if err := ghes.Check(); err != nil {
		t.Fatal(err)
	}
	if !ghes.atGitHub() || !slices.Contains(ghes.performs(), "github-issue") || ghes.services(Tokens{}).GitHubAPI != "https://ghe.corp/api/v3" {
		t.Errorf("GHES: at GitHub %v, performs %v, api %q", ghes.atGitHub(), ghes.performs(), ghes.services(Tokens{}).GitHubAPI)
	}
	if (Config{Repos: &Repos{Mirrors: "/m", Host: "ghe.corp"}}).atGitHub() {
		t.Error("a forge that is not github.com, with no API named, is a GitHub")
	}
}

// WHAT THE RUNNER CANNOT START WITH IS REFUSED AT START, from the environment it starts in:
// nothing left to offer once the verse agent is closed for its model key, an agent whose program
// is not there. A missing forge token, an agent left to its own login, or claude's API key
// shadowed by an OAuth token, is said and let start.
func TestTheRunnerRefusesToStartWithoutWhatItNeeds(t *testing.T) {
	env := func(set ...string) func(string) string {
		return func(name string) string {
			if slices.Contains(set, name) {
				return "x"
			}
			return ""
		}
	}
	found := func(string) (string, error) { return "/bin/x", nil }
	alone, _ := agentWith(AgentName, nil).Held(env())
	if _, err := alone.CheckStart(env(), found); err == nil || !strings.Contains(err.Error(), "the runner offers nothing it can run") || !strings.Contains(err.Error(), verse.EnvAPIKey) {
		t.Errorf("the verse agent alone, with no model key: %v", err)
	}
	cx := agentWith(codex.Agent, nil)
	gone := func(string) (string, error) { return "", os.ErrNotExist }
	if _, err := cx.CheckStart(env(), gone); err == nil || !strings.Contains(err.Error(), "agents.codex.bin: codex is not here") {
		t.Errorf("codex with no program: %v", err)
	}
	warnings, err := cx.CheckStart(env(), found)
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], codex.EnvAPIKey) {
		t.Errorf("codex with no key: %v %v, want it let start, said", warnings, err)
	}
	cl := agentWith(claude.Agent, nil)
	if warnings, err := cl.CheckStart(env(claude.EnvAPIKey), found); err != nil || len(warnings) != 0 {
		t.Errorf("claude with an API key: %v %v", warnings, err)
	}
	warnings, err = cl.CheckStart(env(claude.EnvAPIKey, claude.EnvOAuthToken), found)
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "claude runs as "+claude.EnvOAuthToken+", and "+claude.EnvAPIKey+" is not used") {
		t.Errorf("claude with both: %v %v, want it let start, said", warnings, err)
	}
	repos := Config{Repos: &Repos{Mirrors: "/m"}, Script: &Script{}}
	if warnings, err := repos.CheckStart(env(), found); err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], EnvGitHubToken) {
		t.Errorf("repos with no forge token: %v %v, want it let start, said", warnings, err)
	}
}

// A SYSTEM WHOSE CREDENTIAL IS NOT SET IS CLOSED, NOT REFUSED: the runner starts with Linear
// left out of every doer's section, saying so; no offer holds it, so a launch that needs it is
// refused where it is written; and with the variable set, the configuration is as written.
func TestASystemWithNoCredentialIsClosed(t *testing.T) {
	lin := Config{
		Agents: map[string]Agent{AgentName: {Systems: map[string]System{"linear-issue": {Ceiling: systems.Open()}, "question": {Ceiling: systems.Open()}}}},
		Script: &Script{Systems: map[string]System{"linear-issue": {Ceiling: systems.Open()}}},
	}
	// The verse agent's model key is set: only Linear's is missing.
	none := func(name string) string {
		if name == verse.EnvAPIKey {
			return "k"
		}
		return ""
	}
	got, warnings := lin.Held(none)
	if len(warnings) != 1 || !strings.Contains(warnings[0], EnvLinearAPIKey+" is not set: linear-issue is closed") ||
		!strings.Contains(warnings[0], "agents.verse.systems.linear-issue, script.systems.linear-issue") {
		t.Errorf("warnings %v", warnings)
	}
	if err := got.Check(); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(got.performs(), "linear-issue") || TokensFromEnv(got).Linear != "" {
		t.Errorf("performs %v", got.performs())
	}
	if _, ok := got.Agents[AgentName].Systems["question"]; !ok {
		t.Error("Held closed more than linear-issue")
	}
	if _, ok := lin.Script.Systems["linear-issue"]; !ok || !slices.Contains(lin.performs(), "linear-issue") {
		t.Error("Held changed the configuration it was given")
	}
	d := got.Describe(nil)
	for _, o := range d.Offers {
		if _, ok := o.Systems["linear-issue"]; ok {
			t.Errorf("the offer %s holds linear-issue", o.Name())
		}
	}
	l := &runner.Launch{ID: "s", Script: "true", Environment: systems.Environment{Systems: systems.Systems{"linear-issue": {{}}}}}
	if err := runnerd.CheckLaunch(l, d, nil); err == nil || !strings.Contains(err.Error(), "linear-issue") {
		t.Errorf("a launch needing linear-issue: %v, want it refused", err)
	}
	set := func(name string) string {
		if name == EnvLinearAPIKey || name == verse.EnvAPIKey {
			return "k"
		}
		return ""
	}
	if same, warnings := lin.Held(set); len(warnings) != 0 || !slices.Contains(same.performs(), "linear-issue") {
		t.Errorf("with the key set: %v, performs %v", warnings, same.performs())
	}
}

// THE VERSE AGENT WITH NO MODEL KEY IS CLOSED, NOT REFUSED: the runner starts offering what
// else it offers, saying so, and a prompt on it is refused where it is written. Codex and claude
// with no key stay offered, since a login may still authenticate them.
func TestTheVerseAgentWithNoKeyIsClosed(t *testing.T) {
	c := Config{
		Agents: map[string]Agent{AgentName: {Systems: OpenInside(AgentName)}, codex.Agent: {Systems: OpenInside(codex.Agent)}},
		Script: &Script{},
	}
	none := func(string) string { return "" }
	got, warnings := c.Held(none)
	if len(warnings) != 1 || !strings.Contains(warnings[0], verse.EnvAPIKey+" is not set: agents.verse is closed") {
		t.Errorf("warnings %v", warnings)
	}
	if _, ok := got.Agents[AgentName]; ok {
		t.Error("the verse agent is still run")
	}
	if _, ok := got.Agents[codex.Agent]; !ok {
		t.Error("codex was closed with it")
	}
	if _, ok := c.Agents[AgentName]; !ok {
		t.Error("Held changed the configuration it was given")
	}
	if _, err := got.CheckStart(none, func(string) (string, error) { return "/bin/x", nil }); err != nil {
		t.Errorf("scripts and codex left: %v", err)
	}
	if s := SecretsFromEnv(got); s.PortkeyAPIKey != "" {
		t.Errorf("secrets %+v", s)
	}
	d := got.Describe(nil)
	for _, o := range d.Offers {
		if o.Agent != nil && o.Agent.Name == AgentName {
			t.Error("an offer holds the verse agent")
		}
	}
	l := &runner.Launch{ID: "p", Prompt: "go", Agent: &runner.Agent{Name: AgentName}}
	if err := runnerd.CheckLaunch(l, d, nil); err == nil {
		t.Error("a prompt to the closed verse agent is taken")
	}
	key := func(name string) string {
		if name == verse.EnvAPIKey {
			return "k"
		}
		return ""
	}
	if same, warnings := c.Held(key); len(warnings) != 0 || len(same.Agents) != 2 {
		t.Errorf("with the key set: %v, agents %v", warnings, same.Agents)
	}
}

// EACH DOER'S SHELL IS GIVEN ITS OWN VARIABLES, AND ITS TASK'S PROCESS NOTHING MORE: what the
// scripts' section names, no agent's shell sees, nor the process an agent's task runs in, whose
// children could read its environment; and a launch's `with.env` may not name what its own doer's
// shell is given, and may name what another's is.
func TestEachDoersShellEnvIsItsOwn(t *testing.T) {
	c := Config{
		Agents: map[string]Agent{AgentName: {ShellEnv: []string{"PATH", "HOME"}}},
		Script: &Script{ShellEnv: []string{"PATH", "MYSQL_PWD"}},
	}
	agent := &runner.Launch{ID: "a", Prompt: "p", Agent: &runner.Agent{Name: AgentName}}
	script := &runner.Launch{ID: "s", Script: "true"}
	if got := c.envOf(agent); slices.Contains(got, "MYSQL_PWD") || !slices.Contains(got, "HOME") {
		t.Errorf("the agent's shell: %v", got)
	}
	if got := c.envOf(script); !slices.Contains(got, "MYSQL_PWD") || slices.Contains(got, "HOME") {
		t.Errorf("a script's shell: %v", got)
	}
	if got := c.envOf(&runner.Launch{ID: "d", Prompt: "p"}); slices.Contains(got, "MYSQL_PWD") {
		t.Errorf("a prompt naming no agent is the default agent's: %v", got)
	}
	if got := (Config{Script: &Script{}}).shellEnv(ScriptDoer); !slices.Contains(got, task.DefaultShellEnv[0]) {
		t.Errorf("no shellEnv is the default: %v", got)
	}

	// The task's process: the environment the runner starts it with is its doer's alone.
	t.Setenv("MYSQL_PWD", "hunter2")
	h, err := Standalone(c, Secrets{}, Tokens{}, runnerd.Unconfined{}, []string{os.Args[0]}, t.TempDir(), t.TempDir(), nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if env := strings.Join(h.Env(agent), " "); strings.Contains(env, "MYSQL_PWD") {
		t.Errorf("an agent's task process holds the scripts' variable: %s", env)
	}
	if env := strings.Join(h.Env(script), " "); !strings.Contains(env, "MYSQL_PWD=hunter2") {
		t.Errorf("a script's task process lacks its own variable: %s", env)
	}

	// `with.env` is held to the launching doer's list.
	vars := func(l *runner.Launch) *runner.Launch {
		l.Environment = envOf(netOpen, nil, nil)
		l.Environment.Vars = map[string]string{"MYSQL_PWD": "x"}
		return l
	}
	if err := runnerd.CheckLaunch(vars(script), runner.Description{}, c.envOf(script)); err == nil || !strings.Contains(err.Error(), "MYSQL_PWD is the runner's to set") {
		t.Errorf("a script setting its own doer's variable: %v", err)
	}
	if err := runnerd.CheckLaunch(vars(agent), runner.Description{}, c.envOf(agent)); err != nil {
		t.Errorf("an agent setting a variable only the scripts' shells are given: %v", err)
	}
}

// THE FILE SAYS WHAT THE PROCESS OFFERS, AND IS READ AS WRITTEN: `agents:` keyed by agent name,
// `script:`, nothing else at the root of the runner's own; a key the shape has no place for is
// refused at any depth, the keys of an earlier shape among them; and a runner offering nothing is
// refused.
func TestTheConfigShapeIsChecked(t *testing.T) {
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "c.tony")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for body, says := range map[string]string{
		"listen: \":9802\"\n":                           "the runner offers nothing",
		"script: {}\nmodels: {model: m}\n":              `no setting "models"`,
		"script: {}\nshellEnv: [PATH]\n":                `no setting "shellEnv"`,
		"codex: {sandbox: read-only}\n":                 `no setting "codex"`,
		"script: {systems: {slack: {credential: K}}}\n": `no setting "script.systems.slack.credential"`,
		"agents: {claude: {modle: x}}\n":                `no setting "agents.claude.modle"`,
		"agents: {claude: {sandbox: read-only}}\n":      "agents.claude.sandbox: claude takes no sandbox (its program's keys: bin",
		"agents: {verse: {bin: /x}}\n":                  "agents.verse.bin: verse takes no bin (its program's keys: none",
		"agents: {gemini: {}}\n":                        `agents.gemini: this runner runs no agent "gemini" (it runs: verse, codex, claude)`,
		"agents: {verse: {settings: {model: {values: [claude-*], default: gpt-5}}}}\n": "agents.verse.settings.model.default: outside the values taken",
		"script: {shellEnv: []}\n":                            "script.shellEnv: an empty list gives the shell nothing; leave it out for the default (PATH",
		"script: {shellEnv: [PATH, GITHUB_TOKEN]}\n":          "script.shellEnv: GITHUB_TOKEN is a credential the runner acts with",
		"agents: {claude: {shellEnv: [ANTHROPIC_API_KEY]}}\n": "agents.claude.shellEnv: ANTHROPIC_API_KEY is a credential",
	} {
		if _, _, err := Load(write(body)); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%q: %v, want %q", body, err, says)
		}
	}
	_, c, err := Load(write("script: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d := c.Describe(nil); len(d.Offers) != 1 || !d.Offers[0].Script || len(d.Offers[0].Systems) != 2 {
		t.Errorf("`script: {}` is scripts with their questions and the network alone: %+v", d.Offers)
	}
}

// A CEILING OF ROWS IS NO CEILING: the allow and deny rows a ceiling was are refused as the
// configuration is read, saying what a ceiling block is.
func TestACeilingOfRowsIsRefused(t *testing.T) {
	c := agentWith(AgentName, map[string]System{"commands": {Ceiling: ceiling(t, `[{then: allow}]`)}})
	err := c.Check()
	if err == nil || !strings.Contains(err.Error(), "agents.verse.systems.commands.ceiling") || !strings.Contains(err.Error(), `"then"`) {
		t.Errorf("a ceiling of rows: %v, want it refused naming the key", err)
	}
}

// A GIT CEILING KEEPS TO THE RUNNER'S FORGE: each block names its repositories, each within
// `<host>/**`, since the runner fetches from nowhere else.
func TestAGitCeilingKeepsToTheForge(t *testing.T) {
	for text, says := range map[string]string{
		`{repo: !regexp "^github\\.com/acme/", fetch: {}}`:          "",
		`{repo: !or [acme/app, !glob "github.com/x/*"], fetch: {}}`: "",
		`{repo: !regexp "^gitlab\\.com/acme/", fetch: {}}`:          "is beyond the runner's forge (!regexp ^github\\.com/)",
		`{repo: null, fetch: {}}`:                                   "beyond the runner's forge",
		`{fetch: {}}`:                                               "names no repo",
	} {
		c := Config{Repos: &Repos{Mirrors: "/m"}, Script: &Script{Systems: map[string]System{"git": {Ceiling: ceiling(t, text)}}}}
		err := c.Check()
		if says == "" && err != nil || says != "" && (err == nil || !strings.Contains(err.Error(), says)) {
			t.Errorf("%s: %v, want %q", text, err, says)
		}
	}
	ghes := Config{Repos: &Repos{Mirrors: "/m", Host: "ghe.corp"}, Script: &Script{Systems: map[string]System{"git": {Ceiling: ceiling(t, `{repo: !regexp "^github\\.com/acme/", fetch: {}}`)}}}}
	if err := ghes.Check(); err == nil || !strings.Contains(err.Error(), `!regexp ^ghe\.corp/`) {
		t.Errorf("github.com's repositories on a GHES runner: %v", err)
	}
}

// THE SYSTEMS A RUNNER STOPS INSIDE A RUN TAKE A CEILING: a `commands` section with a ceiling is
// the operator's line over the doer's shell, published on its offer and held at install, so a
// spawn whose environment gives a command beyond it is refused saying what the ceiling gives; one
// with no section is closed.
func TestAnInnerSystemsCeilingIsTheOperatorsLine(t *testing.T) {
	c := agentWith(AgentName, map[string]System{"commands": {Ceiling: ceiling(t, `{run: {command: !regexp "^make "}}`)}})
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	d := c.Describe([]string{runner.NetworkOpen})
	agent := d.Offers[0]
	if cmds := agent.Systems["commands"]; len(cmds.Ceiling) != 1 || systems.Words(cmds.Ceiling[0].Verbs["run"][0]["command"]) != `!regexp "^make "` {
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
	if err := runnerd.CheckLaunch(launch(cmds(systems.Shape{"command": ir.FromString("make test")})), d, nil); err != nil {
		t.Errorf("a command within the ceiling: %v", err)
	}
	err := runnerd.CheckLaunch(launch(cmds(systems.Shape{})), d, nil)
	if err == nil || !strings.Contains(err.Error(), "beyond this runner's ceiling for commands") || !strings.Contains(err.Error(), `{command: !regexp "^make "}`) {
		t.Errorf("any command under a ceiling of make: %v", err)
	}
	if err := runnerd.CheckLaunch(launch(giving(envOf(netOpen, nil, nil), "files")), d, nil); err == nil || !strings.Contains(err.Error(), "the ceiling gives nothing") {
		t.Errorf("files, closed: %v", err)
	}
}

// VERSE'S SECTION IS A CEILING, taken whether or not the runner reaches a verse: the file is
// checked before that is known, and verse's verbs go with the runner's own token.
func TestVersesSectionIsTakenBeforeTheRunnerReachesAVerse(t *testing.T) {
	c := Config{Script: &Script{Systems: map[string]System{"verse": {Ceiling: systems.Open()}}}}
	if err := c.Check(); err != nil {
		t.Errorf("a ceiling for verse: %v", err)
	}
}

// OPEN INSIDE IS EVERY SYSTEM A RUN OF THE DOER IS STOPPED ON, UNDER AN OPEN CEILING: what `verse
// up`'s runner, whose operator is its tasks' author, is configured with (Open).
func TestOpenInsideOpensWhatARunIsStoppedOn(t *testing.T) {
	if got := slices.Sorted(maps.Keys(OpenInside(AgentName))); !slices.Equal(got, slices.Sorted(maps.Keys(inner))) {
		t.Fatalf("OpenInside(verse) covers %v, want %v", got, slices.Sorted(maps.Keys(inner)))
	}
	if got := slices.Sorted(maps.Keys(OpenInside(ScriptDoer))); !slices.Equal(got, []string{"question"}) {
		t.Fatalf("OpenInside(script) covers %v, want question alone", got)
	}
	c := Open(AgentName)
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	d := c.Describe([]string{runner.NetworkOpen})
	env := giving(envOf(netOpen, nil, nil), "files", "commands", "agent", "question")
	l := &runner.Launch{ID: "x", Prompt: "p", Agent: &runner.Agent{Name: AgentName}, Environment: env, Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}}
	if err := runnerd.CheckLaunch(l, d, nil); err != nil {
		t.Errorf("every verb of what a run is stopped on, under OpenInside: %v", err)
	}
}

// WHAT THE RUNNER DESCRIBES: its version; its agents, the verse agent first (the default), each
// with the settings it takes narrowed by the operator and holding a conversation, a default where
// the agent has its own or the configuration names one; and every offer holding the network, with
// what the sandbox builds of it beside the doer's ceiling, as written.
func TestDescribePublishesBuildsAndSettings(t *testing.T) {
	net := map[string]System{"network": {Ceiling: ceiling(t, `{mode: !or [none, allow], tcp-client: {host: !glob "*.github.com"}}`)}}
	c := Config{
		Agents: map[string]Agent{
			AgentName: {Settings: map[string]Setting{runner.SettingModel: {Values: []string{"claude-*"}, Default: "claude-sonnet-5"}}, Systems: net},
			codex.Agent: {Settings: map[string]Setting{"effort": {Values: []string{"low", "high"}}},
				Systems: map[string]System{"network": net["network"], "commands": {Ceiling: systems.Open()}, "files": {Ceiling: systems.Open()}}},
		},
		Script: &Script{Systems: net},
	}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	d := c.Describe([]string{runner.NetworkNone, runner.NetworkOpen, runner.NetworkAllow})
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
		if o.Converse != (o.Agent != nil) {
			t.Errorf("%s: converse %v, want every agent to hold a conversation and a script none", o.Name(), o.Converse)
		}
		net := o.Systems["network"]
		if len(net.Builds) != 1 || systems.Words(net.Builds[0].Attributes["mode"]) != "!or [none, open, allow]" {
			t.Errorf("%s: network builds %+v (mode %s), want the sandbox's", o.Name(), net.Builds, systems.Words(net.Builds[0].Attributes["mode"]))
		}
		if len(net.Ceiling) != 1 || systems.Words(net.Ceiling[0].Attributes["mode"]) != "!or [none, allow]" {
			t.Errorf("%s: network ceiling %+v (mode %s), want it as written", o.Name(), net.Ceiling, systems.Words(net.Ceiling[0].Attributes["mode"]))
		}
	}
	if m := agentWith(AgentName, nil).Describe(nil).Offers[0].Agent.Settings[runner.SettingModel]; m.Default {
		t.Error("a verse agent with no configured model has no default of its own")
	}
	if d := agentWith(codex.Agent, nil).Describe(nil); d.Offers[0].Name() != codex.Agent {
		t.Errorf("with no verse agent, codex is the default: %s", d.Offers[0].Name())
	}
	// A launch naming no setting runs the configured default, and the record says so.
	l := c.withDefaults(runner.Launch{Prompt: "p"}, AgentName)
	if l.AgentSettings().Name != AgentName || l.AgentSettings().Model() != "claude-sonnet-5" {
		t.Errorf("the default filled in: %+v", l.Agent)
	}
	named := runner.Launch{Prompt: "p", Agent: &runner.Agent{Name: AgentName, Settings: map[string]string{runner.SettingModel: "claude-opus"}}}
	if l := c.withDefaults(named, AgentName); l.AgentSettings().Model() != "claude-opus" {
		t.Errorf("a named model is the launch's: %+v", l.Agent)
	}
}

// THE OPERATOR NARROWS WHAT AN AGENT TAKES, and nothing else: an agent the runner runs, a setting
// it takes, a list that takes something, a default within both.
func TestTheAgentsSettingsAreChecked(t *testing.T) {
	open := map[string]System{"commands": {Ceiling: systems.Open()}, "files": {Ceiling: systems.Open()}}
	with := func(agent string, settings map[string]Setting) Config {
		return Config{Agents: map[string]Agent{agent: {Settings: settings, Systems: open}}}
	}
	for says, c := range map[string]Config{
		`takes no setting "temperature"`:    with(AgentName, map[string]Setting{"temperature": {Values: []string{"0.2"}}}),
		"an empty list takes nothing":       with(codex.Agent, map[string]Setting{"effort": {Values: []string{}}}),
		`"minimal" is none of low`:          with(claude.Agent, map[string]Setting{"effort": {Values: []string{"high", "minimal"}}}),
		"default: outside the values taken": with(claude.Agent, map[string]Setting{"effort": {Default: "extreme"}}),
		"model.default: outside the values": with(AgentName, map[string]Setting{runner.SettingModel: {Values: []string{"claude-*"}, Default: "gpt-5"}}),
	} {
		if err := c.Check(); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%+v: %v, want it refused saying %q", c.Agents, err, says)
		}
	}
	cx := with(codex.Agent, map[string]Setting{"effort": {Values: []string{"high"}}, runner.SettingModel: {Values: []string{"gpt-5*"}}})
	if err := cx.Check(); err != nil {
		t.Errorf("a narrowing of codex: %v", err)
	}
	// Claude's efforts narrowed to two of them: the offer takes those two and nothing else.
	cl := with(claude.Agent, map[string]Setting{"effort": {Values: []string{"low", "high"}, Default: "high"}})
	if err := cl.Check(); err != nil {
		t.Errorf("a narrowing of claude's efforts: %v", err)
	}
	if e := cl.settings(claude.Agent)["effort"]; e.Takes("high") != nil || e.Takes("max") == nil {
		t.Errorf("claude's narrowed effort: %+v", e)
	}
}

// A NETWORK CEILING THE SANDBOX CAN'T MEET IS REFUSED WHEN THE RUNNER STARTS, not at each spawn:
// an unconfined sandbox builds only the open network.
func TestARunnerRefusesACeilingItsSandboxCantBuild(t *testing.T) {
	c := Config{Script: &Script{Systems: map[string]System{"network": {Ceiling: ceiling(t, `{mode: !or [none, allow]}`)}}}}
	_, err := Standalone(c, Secrets{}, Tokens{}, runnerd.Unconfined{}, []string{os.Args[0]}, t.TempDir(), t.TempDir(), nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "script.systems.network.ceiling[0].mode: none is no network this runner's sandbox builds (it builds: open)") {
		t.Errorf("a ceiling of none and allow on an unconfined sandbox: %v", err)
	}
	if _, err := Standalone(Config{Script: &Script{Systems: map[string]System{"network": {Ceiling: ceiling(t, `{mode: open}`)}}}}, Secrets{}, Tokens{},
		runnerd.Unconfined{}, []string{os.Args[0]}, t.TempDir(), t.TempDir(), nil, io.Discard); err != nil {
		t.Errorf("a ceiling of open on an unconfined sandbox: %v", err)
	}
}

// CODEX'S AND CLAUDE'S OFFERS SAY WHAT EACH CAN'T RUN WITHOUT, and a runner whose ceilings can't
// give it does not start: no spawn of that agent could ever be written there.
func TestCodexAndClaudeRequireTheirShellAndEdits(t *testing.T) {
	open := map[string]System{"commands": {Ceiling: systems.Open()}, "files": {Ceiling: systems.Open()}}
	c := agentWith(codex.Agent, open)
	if err := c.Check(); err != nil {
		t.Fatalf("a codex runner whose ceilings give both: %v", err)
	}
	d := c.Describe([]string{"open"})
	if got := d.Offers[0].Requires; d.Offers[0].Name() != codex.Agent || !slices.Equal(got, []string{task.StepCommandsRun, task.StepFilesWrite}) {
		t.Errorf("codex's offer requires %v", got)
	}
	if err := d.Check(); err != nil {
		t.Errorf("the description: %v", err)
	}
	closed := agentWith(codex.Agent, map[string]System{"files": {Ceiling: systems.Open()}})
	if err := closed.Check(); err == nil || !strings.Contains(err.Error(), "agents.codex.systems.commands.ceiling: codex can't run without commands.run") {
		t.Errorf("a codex runner whose commands ceiling is closed: %v", err)
	}
	// Claude's are the same steps, held the same way.
	closed = agentWith(claude.Agent, map[string]System{"files": {Ceiling: systems.Open()}})
	if err := closed.Check(); err == nil || !strings.Contains(err.Error(), "agents.claude.systems.commands.ceiling: claude can't run without commands.run") {
		t.Errorf("a claude runner whose commands ceiling is closed: %v", err)
	}
}
