// Package verserunner is the `verse` runner: it offers verse's own agent (package agent) and
// scripts. `runnerd verse` serves it.
//
// A launch's doer picks which: `agent: verse` is the verse agent, and `script` runs a script.
package verserunner

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/codex"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// AnnotationVirtualKey lets a launch pick the Portkey virtual key its run uses: a routing label
// (route, budget), not a secret. The runner holds the Portkey account key.
const AnnotationVirtualKey = "verse.portkey/virtual-key"

// annotations is every annotation the verse runner knows, with the type of its value. A
// configuration may accept any of them.
var annotations = map[string]string{AnnotationVirtualKey: "string"}

// The environment variables the runner reads its secrets from.
const (
	EnvPortkeyAPIKey     = "PORTKEY_API_KEY"
	EnvPortkeyBaseURL    = "PORTKEY_BASE_URL"
	EnvPortkeyVirtualKey = "PORTKEY_VIRTUAL_KEY"
	// EnvGitHubToken is where the runner's token for its forge is read from when `systems.git`
	// names no variable: what it fetches and pushes with. It stays in the runner; a task's
	// process never has it.
	EnvGitHubToken = "GITHUB_TOKEN"
)

// Config is the verse runner's own configuration, beside runnerd.Config in the same file.
type Config struct {
	Models Models `tony:"field=models,omitzero"`
	// Agents is the operator's narrowing of each agent's settings, by agent and setting: the
	// values a launch may set, as patterns (`verse: {model: ["claude-sonnet-*"]}`). A setting
	// the agent does not take is refused; one not named here takes any value of its form.
	Agents map[string]map[string][]string `tony:"field=agents,omitzero"`
	// Annotations is the annotations a launch may set, with the type of their value.
	Annotations map[string]string `tony:"field=annotations,omitzero"`
	// ShellEnv is the variables of the runner's environment a task's shell is given, by name.
	// Absent is task.DefaultShellEnv: enough for a shell to find its tools, and no secret.
	ShellEnv []string `tony:"field=shellEnv,omitzero"`
	// Repos is where the runner keeps its git mirrors, and the forge it fetches from. Absent,
	// the runner takes no launch that names repos.
	Repos *Repos `tony:"field=repos,omitzero"`
	// Codex is how this runner runs the codex agent (package codex). Absent, the agent is not
	// offered: a runner that has not said how to run Codex does not guess.
	Codex *codex.Config `tony:"field=codex,omitzero"`
	// Systems is the operator's say over each system the runner holds for its tasks, by name
	// (System): the credential it acts with, and the ceiling over what any task may do. `git`
	// and `git-issue` come with `repos`, `github` and `github-issue` where the forge is GitHub,
	// and `linear-issue` and `slack` where their credential is named; a section for one of those
	// the runner does not perform is refused. `files`, `commands` and `agent`, which the runner
	// stops inside a run, take a ceiling and nothing else.
	Systems map[string]System `tony:"field=systems,omitzero"`
}

// System is one system's section under `systems:`: how the runner reaches it and how far a
// task may go with it.
type System struct {
	// Credential is the NAME of the variable holding the credential the runner acts with: for
	// `git`, its forge token (absent is GITHUB_TOKEN); for `linear-issue`, the Authorization header
	// verbatim (a personal key bare, an OAuth token as `Bearer …`); for `slack`, the bot token
	// (`xoxb-…`). For those two it is what makes the system offered at all. The runner reads it;
	// a task never has it. The file carries no secret.
	Credential string `tony:"field=credential,omitzero"`
	// API is where the forge's API is, for `github` and `github-issue`: absent is github.com's,
	// and a GitHub Enterprise Server's is `https://<host>/api/v3`.
	API string `tony:"field=api,omitzero"`
	// Ceiling is the operator's bound on the system (systems.Ceiling): blocks in the
	// environment's form, attributes as patterns, closed, so absent is nothing of the system (the
	// network apart, whose absent ceiling bounds nothing beyond what the sandbox builds). It is
	// published with the runner's offers, holds a launch's environment at install and at launch
	// (every block within it), and is read again at each act.
	Ceiling systems.Ceiling `tony:"field=ceiling,omitzero"`
}

// Repos is how the runner checks out a launch's repos.
type Repos struct {
	// Mirrors is the directory the runner keeps one bare mirror per repo in, shared by its tasks.
	Mirrors string `tony:"field=mirrors"`
	// Host is the forge the runner's repos are on, as a launch's repo names it (`host/owner/name`,
	// or `owner/name` for github.com). Absent is github.com.
	Host string `tony:"field=host,omitzero"`
	// Forge is where the host's `owner/name` is fetched from: absent is https://<host>, and a
	// directory holding `owner/name` repositories serves a runner with no network.
	Forge string `tony:"field=forge,omitzero"`
}

// Tokens is what the runner holds that a task's process never has, read from its environment:
// its git token for the forge, its Linear credential and its Slack bot token.
type Tokens struct {
	Git    string
	Linear string
	Slack  string
	// LinearAPI, GitHubAPI and SlackAPI move the systems' APIs, for a test; empty is each one's
	// own, or the one `systems.github.api` names.
	LinearAPI, GitHubAPI, SlackAPI string
}

// services is what the delivery channel performs the acts beyond git's with.
func (c Config) services(t Tokens) delivery.Services {
	api := cmp.Or(t.GitHubAPI, c.Systems["github"].API, c.Systems["github-issue"].API, delivery.GitHubAPI)
	return delivery.Services{LinearAuth: t.Linear, LinearAPI: t.LinearAPI, GitHubAPI: api, SlackToken: t.Slack, SlackAPI: t.SlackAPI}
}

// TokensFromEnv reads the runner's tokens from its environment, and names the variables that
// aren't set.
func TokensFromEnv(c Config) (Tokens, []string) {
	var t Tokens
	var unset []string
	if env := c.Systems["linear-issue"].Credential; env != "" {
		if t.Linear = os.Getenv(env); t.Linear == "" {
			unset = append(unset, env+" (linear-issue)")
		}
	}
	if env := c.Systems["slack"].Credential; env != "" {
		if t.Slack = os.Getenv(env); t.Slack == "" {
			unset = append(unset, env+" (slack)")
		}
	}
	if c.Repos == nil {
		return t, unset
	}
	env := cmp.Or(c.Systems["git"].Credential, EnvGitHubToken)
	if t.Git = os.Getenv(env); t.Git == "" {
		unset = append(unset, env+" (git)")
	}
	return t, unset
}

// forge is the forge the runner's repos are on, its host, and where it fetches them from (url).
func (r *Repos) forge() (host, url string) {
	host = strings.ToLower(cmp.Or(r.Host, runner.DefaultHost)) // a host name is case-insensitive
	url = cmp.Or(r.Forge, "https://"+host)
	return host, url
}

// atGitHub reports whether the runner's forge is a GitHub: github.com itself, or one whose API the
// configuration names (`systems.github.api`). A directory standing in for a forge is neither:
// it has no pull requests and no issues.
func (c Config) atGitHub() bool {
	if c.Repos == nil {
		return false
	}
	if c.Systems["github"].API != "" {
		return true
	}
	host, url := c.Repos.forge()
	return host == runner.DefaultHost && url == "https://"+runner.DefaultHost
}

// performs is the systems the runner performs for its tasks, from its configuration: the ones
// its offers list, with their ceilings.
func (c Config) performs() []string {
	var out []string
	if c.Repos != nil {
		out = append(out, "git", "git-issue")
		if c.atGitHub() {
			out = append(out, "github", "github-issue")
		}
	}
	for _, name := range []string{"linear-issue", "slack"} {
		if c.Systems[name].Credential != "" {
			out = append(out, name)
		}
	}
	return out
}

// inner is the systems the runner stops inside a run rather than performs over the channel,
// and which doers it stops them for: the verse agent all four, codex what it proposes, a
// script its questions alone, since its commands and files are one action and no step.
var inner = map[string][]string{
	"files":    {AgentName, codex.Agent},
	"commands": {AgentName, codex.Agent},
	"agent":    {AgentName},
	"question": {AgentName, codex.Agent, "script"},
}

// OpenInside is the systems the runner stops inside a run (files, commands, agent, question),
// each under an open ceiling (systems.Open): the configuration of a runner whose operator is the
// author of its tasks, `verse up`'s, which says so rather than leaving them closed.
func OpenInside() map[string]System {
	out := map[string]System{}
	for name := range inner {
		out[name] = System{Ceiling: systems.Open()}
	}
	return out
}

// ceilings is each performed system's ceiling as the channel reads it at each act, as written:
// a `git` ceiling keeps to the runner's forge (Check).
func (c Config) ceilings() map[string]systems.Ceiling {
	out := map[string]systems.Ceiling{}
	for _, name := range c.performs() {
		out[name] = c.Systems[name].Ceiling
	}
	return out
}

// taskCeilings is every system's ceiling, inner and performed, for the supervisor inside a
// run: a step beyond one is refused there, before anyone is asked about it.
func (c Config) taskCeilings() map[string]systems.Ceiling {
	out := c.ceilings()
	for name := range inner {
		out[name] = c.Systems[name].Ceiling
	}
	return out
}

// intercepted is the systems an offer intercepts for a doer, as the offer publishes them: the
// performed systems for every doer, and the inner systems the doer is stopped on.
func (c Config) intercepted(doer string) map[string]runner.System {
	out := map[string]runner.System{}
	for name, ceiling := range c.ceilings() {
		out[name] = runner.System{Ceiling: ceiling}
	}
	for name, doers := range inner {
		if slices.Contains(doers, doer) {
			out[name] = runner.System{Ceiling: c.Systems[name].Ceiling}
		}
	}
	return out
}

// networks is the networks sb builds a task, as the offers say them under `with.network`.
func networks(sb runnerd.Sandbox) []string {
	var out []string
	for _, mode := range runnerd.NetworkModes {
		if sb.Provides(runnerd.Network{Mode: mode}) == nil {
			out = append(out, mode)
		}
	}
	return out
}

// shellEnv is the variables a task's shell is given, and the proxy's: a task whose network
// allows hosts reaches them through the proxy its process announces (runnerd.UseEgress).
func (c Config) shellEnv() []string {
	names := c.ShellEnv
	if len(names) == 0 {
		names = task.DefaultShellEnv
	}
	return append(slices.Clone(names), runnerd.ProxyEnv...)
}

// Models is how the verse agent reaches its model.
type Models struct {
	Portkey Portkey `tony:"field=portkey,omitzero"`
	// Model is the agent's model when a launch doesn't pick one.
	Model string `tony:"field=model,omitzero"`
}

// Portkey is the Portkey route. Its account key is read from PORTKEY_API_KEY.
type Portkey struct {
	// VirtualKey is used when a launch doesn't pick one, ahead of PORTKEY_VIRTUAL_KEY.
	VirtualKey string `tony:"field=virtualKey,omitzero"`
}

// Load reads the verse runner's configuration file, and its secrets from the environment.
func Load(path string) (runnerd.Config, Config, error) {
	var c Config
	rc, err := runnerd.Load(path, &c)
	if err != nil {
		return rc, c, err
	}
	if err := c.Check(); err != nil {
		return rc, c, fmt.Errorf("%s: %w", path, err)
	}
	return rc, c, nil
}

// checkForge refuses a `git` ceiling block reaching past the runner's forge: it fetches from
// nowhere else, so each block names its repositories, each within `<host>/**`.
func (c *Config) checkForge() error {
	ceiling := c.Systems["git"].Ceiling
	if len(ceiling) == 0 || c.Repos == nil {
		return nil
	}
	host, _ := c.Repos.forge()
	forge := strings.ToLower(host) + "/**"
	for i, b := range ceiling {
		repos := b.Attributes["repo"]
		if len(repos) == 0 {
			return fmt.Errorf("systems.git.ceiling[%d]: names no repo; a git ceiling names the repositories it is for, on the forge (%s)", i, forge)
		}
		for _, p := range repos {
			if p = systems.Canonical(systems.FormRepo, p); !systems.PatternContains(forge, p, false) {
				return fmt.Errorf("systems.git.ceiling[%d].repo: %s is beyond the runner's forge (%s): it fetches from nowhere else", i, p, forge)
			}
		}
	}
	return nil
}

// checkBuilds refuses a network ceiling naming a mode the sandbox can't build: the runner would
// publish a ceiling no launch could meet, and every spawn would be refused for it. Said at start.
func (c Config) checkBuilds(networks []string) error {
	for i, b := range c.Systems["network"].Ceiling {
		for _, mode := range b.Attributes["mode"] {
			if !slices.ContainsFunc(networks, func(n string) bool { return systems.Match(mode, n, false) }) {
				return fmt.Errorf("systems.network.ceiling[%d].mode: %s is no network this runner's sandbox builds (it builds: %s)",
					i, mode, strings.Join(networks, ", "))
			}
		}
	}
	return nil
}

// Check validates the configuration.
func (c *Config) Check() error {
	if c.Repos != nil && c.Repos.Mirrors == "" {
		return fmt.Errorf("repos: `mirrors` names the directory the runner keeps its mirrors in")
	}
	if err := c.checkForge(); err != nil {
		return err
	}
	performs := c.performs()
	for _, name := range slices.Sorted(maps.Keys(c.Systems)) {
		sys := c.Systems[name]
		if _, ok := systems.Get(name); !ok {
			return fmt.Errorf("systems.%s: no system is called that", name)
		}
		if _, isInner := inner[name]; isInner || name == "network" {
			if sys.Credential != "" || sys.API != "" {
				return fmt.Errorf("systems.%s: the runner stops %s inside a run with no credential; its section is a ceiling alone", name, name)
			}
		} else if !slices.Contains(performs, name) {
			return fmt.Errorf("systems.%s: this runner does not perform %s (it performs: %s); %s", name, name,
				cmp.Or(strings.Join(performs, ", "), "nothing"), needsFor(name))
		}
		// A section saying nothing is the system closed, as no section is; refused, so that
		// `name: {}` is never read beside `name: {ceiling: {}}`, which opens it.
		if sys.Credential == "" && sys.API == "" && len(sys.Ceiling) == 0 {
			return fmt.Errorf("systems.%s: a section names a credential, an api or a ceiling; with none it closes "+
				"%s, as leaving it out does (`ceiling: {}` opens it)", name, name)
		}
		if sys.API != "" && name != "github" && name != "github-issue" {
			return fmt.Errorf("systems.%s: `api` is where a GitHub forge's API is, for github and github-issue", name)
		}
		if err := sys.Ceiling.Check(name); err != nil {
			return fmt.Errorf("systems.%s.ceiling%w", name, err)
		}
	}
	for _, agent := range slices.Sorted(maps.Keys(c.Agents)) {
		takes, ok := takes[agent]
		if !ok {
			return fmt.Errorf("agents.%s: the verse runner runs no agent %q (it runs: %s, %s)", agent, agent, AgentName, codex.Agent)
		}
		for _, setting := range slices.Sorted(maps.Keys(c.Agents[agent])) {
			if _, ok := takes[setting]; !ok {
				return fmt.Errorf("agents.%s.%s: %s takes no setting %q (it takes: %s)", agent, setting, agent, setting,
					strings.Join(slices.Sorted(maps.Keys(takes)), ", "))
			}
			if len(c.Agents[agent][setting]) == 0 {
				return fmt.Errorf("agents.%s.%s: an empty list takes nothing; leave the setting out for any", agent, setting)
			}
		}
	}
	// A default the operator configured is held to the operator's own narrowing: the offer
	// would refuse it named and run it unnamed.
	defaults := map[string]string{AgentName: c.Models.Model}
	if c.Codex != nil {
		defaults[codex.Agent] = c.Codex.Model
	}
	for _, agent := range slices.Sorted(maps.Keys(defaults)) {
		model := defaults[agent]
		if model == "" {
			continue
		}
		if err := (runner.Setting{Values: c.Agents[agent][runner.SettingModel]}).Takes(model); err != nil {
			return fmt.Errorf("agents.%s.model: the configured default model is outside it: %w", agent, err)
		}
	}
	for name, typ := range c.Annotations {
		want, ok := annotations[name]
		if !ok {
			return fmt.Errorf("annotations: the verse runner has no annotation %q (it has: %s)",
				name, strings.Join(slices.Sorted(maps.Keys(annotations)), ", "))
		}
		if typ != want {
			return fmt.Errorf("annotations: %s takes a %s, not a %s", name, want, typ)
		}
	}
	if c.Codex != nil {
		if err := c.Codex.Validate(); err != nil {
			return err
		}
		// What Codex can't run without, its ceilings give: else no Codex spawn could ever be
		// written here, and each would be refused for its environment when the runner is at fault.
		for _, step := range requires[codex.Agent] {
			system, verb, _ := systems.Split(step)
			if !c.Systems[system].Ceiling.Gives(step) {
				return fmt.Errorf("systems.%s.ceiling: codex can't run without %s, which the ceiling does not give: write `%s: {ceiling: {}}` or `%s: {ceiling: {%s: {}}}`",
					system, step, system, system, verb)
			}
		}
	}
	return nil
}

// needsFor says what a system's section needs of the configuration to be performed.
func needsFor(system string) string {
	switch system {
	case "git", "git-issue":
		return "a `repos` section is what checks out repos"
	case "github", "github-issue":
		return "a `repos` section whose forge is github.com, or `api` naming the forge's GitHub API"
	case "linear-issue":
		return "`credential` names the variable the Linear credential is read from"
	case "slack":
		return "`credential` names the variable the Slack bot token is read from"
	}
	return "the runner performs none of its verbs"
}
