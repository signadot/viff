// Package stock is the runner verse ships, built on package runnerd: it offers the agents its
// configuration names under `agents:` (verse's own, package verse; codex; claude) and, where it has a `script:`
// section, scripts. `runnerd -config` serves it.
//
// A launch's doer picks which: `agent: verse` is the verse agent, and `script` runs a script.
package stock

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/claude"
	"github.com/signadot/verse/runner/runnerd/codex"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// annotations is every annotation the verse agent knows, with the type of its value. A
// configuration may accept any of them.
var annotations = map[string]string{verse.AnnotationVirtualKey: "string"}

// The environment variables the runner reads the systems' credentials from; each agent's are its
// package's (verse.EnvAPIKey, codex.EnvAPIKey, claude.EnvAPIKey). No configuration names one: a
// variable is read where the configuration offers what needs it.
const (
	// EnvGitHubToken is the runner's token for its forge: what it fetches and pushes with, and
	// what it acts at GitHub with. It stays in the runner; a task's process never has it.
	EnvGitHubToken = "GITHUB_TOKEN"
	// EnvLinearAPIKey is the Authorization header the runner files at Linear with, verbatim: a
	// personal key bare, an OAuth token as `Bearer …`.
	EnvLinearAPIKey = "LINEAR_API_KEY"
	// EnvSlackBotToken is the bot token (`xoxb-…`) the runner posts to Slack with.
	EnvSlackBotToken = "SLACK_BOT_TOKEN"
)

// credentials is every variable the runner reads a credential from, its own and its agents'. No
// `shellEnv` names one: what the runner acts with stays in the runner, and no task's shell, nor
// its process, holds it.
var credentials = []string{
	EnvGitHubToken, EnvLinearAPIKey, EnvSlackBotToken,
	verse.EnvAPIKey, verse.EnvBaseURL, verse.EnvVirtualKey, codex.EnvAPIKey, claude.EnvOAuthToken, claude.EnvAPIKey,
}

// held is the systems a ceiling may name whose credential the runner reads from its environment:
// where it is not set, the runner runs with the system closed (Held).
var held = []struct{ system, env string }{{"linear-issue", EnvLinearAPIKey}, {"slack", EnvSlackBotToken}}

// ScriptDoer is the doer a script is, as a launch says it (runner.Launch.Who) and as the
// systems an inner system is stopped for name it.
const ScriptDoer = "script"

// Config is the verse runner's own configuration, beside runnerd.Config in the same file: the
// agents the process runs, by name, and whether it runs scripts, each doer under its own
// ceilings and its own shell environment. Presence is the switch: an agent the map does not name
// is not offered, and no `script:` offers no scripts.
type Config struct {
	// Repos is where the runner keeps its git mirrors, and the forge it fetches from. Absent,
	// the runner takes no launch that names repos.
	Repos *Repos `tony:"field=repos,omitzero"`
	// Agents is the agents this process runs, by name: verse, codex, claude. Each value is how
	// that program is run and what its tasks get.
	Agents map[string]Agent `tony:"field=agents,omitzero"`
	// Script is the runner's scripts: present, it offers them.
	Script *Script `tony:"field=script,omitzero"`
}

// Agent is one agent the runner runs: its program's own keys, which are the agent's (codex
// `bin`, `sandbox`, `config`; claude `bin`; verse none), and the four keys every agent takes.
type Agent struct {
	// Bin is the program, the agent's name on PATH by default (codex, claude).
	Bin string `tony:"field=bin,omitzero"`
	// Sandbox is codex's own sandbox for approved commands (codex.Config.Sandbox).
	Sandbox string `tony:"field=sandbox,omitzero"`
	// Config is extra codex configuration, as `-c key=value` overrides; the values are TOML.
	Config map[string]string `tony:"field=config,omitzero"`
	// ShellEnv is the variables of the runner's environment the agent's task shell is given, by
	// name. Absent is task.DefaultShellEnv: enough for a shell to find its tools, and no secret.
	ShellEnv []string `tony:"field=shellEnv,omitzero"`
	// Settings is the operator's say over each setting the agent takes (Setting).
	Settings map[string]Setting `tony:"field=settings,omitzero"`
	// Annotations is the annotations a launch of the agent may set (verse only), with the type of
	// their value and the value used where a launch sets none.
	Annotations map[string]Annotation `tony:"field=annotations,omitzero"`
	// Systems is the agent's ceilings, by system (System).
	Systems map[string]System `tony:"field=systems,omitzero"`
}

// Script is the runner's scripts: their ceilings and their shell environment.
type Script struct {
	// ShellEnv is the variables of the runner's environment a script's shell is given, by name;
	// absent is task.DefaultShellEnv. No agent sees them.
	ShellEnv []string `tony:"field=shellEnv,omitzero"`
	// Systems is the scripts' ceilings, by system (System).
	Systems map[string]System `tony:"field=systems,omitzero"`
}

// Setting is the operator's say over one setting of an agent's.
type Setting struct {
	// Values are the values a launch may set, as patterns (`claude-sonnet-*`); absent, any of
	// the setting's form.
	Values []string `tony:"field=values,omitzero"`
	// Default is the value the runner uses where a launch sets none; absent leaves it to the
	// agent's own, where it has one.
	Default string `tony:"field=default,omitzero"`
}

// Annotation is one annotation an agent takes.
type Annotation struct {
	// Type is its value's type: `string`.
	Type string `tony:"field=type"`
	// Default is the value the runner uses where a launch sets none.
	Default string `tony:"field=default,omitzero"`
}

// System is one system's section under a doer's `systems:`: how far its tasks may go with it.
type System struct {
	// Ceiling is the operator's bound on the system (systems.Ceiling): blocks in the
	// environment's form, attributes as patterns, closed, so absent is nothing of the system (the
	// network apart, whose absent ceiling bounds nothing beyond what the sandbox builds). It is
	// published with the doer's offer, holds a launch's environment at install and at launch
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
	// API is where the forge's GitHub API is: absent is github.com's where the forge is
	// github.com, and a GitHub Enterprise Server's is `https://<host>/api/v3`.
	API string `tony:"field=api,omitzero"`
}

// agentOrder is the agents this runner runs, in the order it offers them: its default first.
var agentOrder = []string{AgentName, codex.Agent, claude.Agent}

// program is each agent's own keys under `agents.<name>`, beside the four every agent takes.
var program = map[string][]string{
	AgentName:    nil,
	codex.Agent:  {"bin", "sandbox", "config"},
	claude.Agent: {"bin"},
}

// agents is the agents the runner offers, in agentOrder.
func (c Config) agents() []string {
	var out []string
	for _, name := range agentOrder {
		if _, ok := c.Agents[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// doers is everything the runner offers: its agents, then scripts where it has them.
func (c Config) doers() []string {
	out := c.agents()
	if c.Script != nil {
		out = append(out, ScriptDoer)
	}
	return out
}

// systemsOf is a doer's ceilings, as its section writes them; nil for a doer not offered.
func (c Config) systemsOf(doer string) map[string]System {
	if doer == ScriptDoer {
		if c.Script == nil {
			return nil
		}
		return c.Script.Systems
	}
	return c.Agents[doer].Systems
}

// codexConfig is how this runner runs codex, nil where it runs none.
func (c Config) codexConfig() *codex.Config {
	a, ok := c.Agents[codex.Agent]
	if !ok {
		return nil
	}
	return &codex.Config{Bin: a.Bin, Sandbox: a.Sandbox, Config: a.Config}
}

// claudeConfig is how this runner runs claude, nil where it runs none.
func (c Config) claudeConfig() *claude.Config {
	a, ok := c.Agents[claude.Agent]
	if !ok {
		return nil
	}
	return &claude.Config{Bin: a.Bin}
}

// Tokens is what the runner holds that a task's process never has, read from its environment:
// its git token for the forge, its Linear credential and its Slack bot token.
type Tokens struct {
	Git    string
	Linear string
	Slack  string
	// LinearAPI, GitHubAPI and SlackAPI move the systems' APIs, for a test; empty is each one's
	// own, or the one `repos.api` names.
	LinearAPI, GitHubAPI, SlackAPI string
}

// services is what the delivery channel performs the acts beyond git's with.
func (c Config) services(t Tokens) delivery.Services {
	api := delivery.GitHubAPI
	if c.Repos != nil {
		api = cmp.Or(c.Repos.API, api)
	}
	api = cmp.Or(t.GitHubAPI, api)
	return delivery.Services{LinearAuth: t.Linear, LinearAPI: t.LinearAPI, GitHubAPI: api, SlackToken: t.Slack, SlackAPI: t.SlackAPI}
}

// TokensFromEnv reads the runner's tokens from its environment, each where the runner performs
// what needs it.
func TokensFromEnv(c Config) Tokens {
	var t Tokens
	performs := c.performs()
	if slices.Contains(performs, "linear-issue") {
		t.Linear = os.Getenv(EnvLinearAPIKey)
	}
	if slices.Contains(performs, "slack") {
		t.Slack = os.Getenv(EnvSlackBotToken)
	}
	if c.Repos != nil {
		t.Git = os.Getenv(EnvGitHubToken)
	}
	return t
}

// Held is the configuration the runner runs with in the environment it starts in (getenv): c,
// with each system whose credential is not set (held) left out of every doer's section, and the
// verse agent left out where its model key is not set. The runner then does not perform the
// system, or run the agent, and no offer holds it, so a spawn that needs it is refused at
// install, where it is written, rather than the runner not starting or the act failing at the
// service. It says what it closed. Codex and claude are not closed: without a key, a login may
// still authenticate them (CheckStart).
func (c Config) Held(getenv func(string) string) (Config, []string) {
	var warnings []string
	if _, ok := c.Agents[AgentName]; ok && getenv(verse.EnvAPIKey) == "" {
		agents := maps.Clone(c.Agents)
		delete(agents, AgentName)
		c.Agents = agents
		warnings = append(warnings, fmt.Sprintf("%s is not set: agents.%s is closed, since the verse agent calls its model with it; a spawn that prompts it is refused at install",
			verse.EnvAPIKey, AgentName))
	}
	for _, s := range held {
		naming := c.naming(s.system)
		if len(naming) == 0 || getenv(s.env) != "" {
			continue
		}
		c = c.without(s.system)
		warnings = append(warnings, fmt.Sprintf("%s is not set: %s is closed to every doer, though %s names it; a spawn that needs it is refused at install",
			s.env, s.system, strings.Join(naming, ", ")))
	}
	return c, warnings
}

// without is c with no doer's section naming system. c's maps are not changed.
func (c Config) without(system string) Config {
	if c.Agents != nil {
		agents := make(map[string]Agent, len(c.Agents))
		for name, a := range c.Agents {
			a.Systems = maps.Clone(a.Systems)
			delete(a.Systems, system)
			agents[name] = a
		}
		c.Agents = agents
	}
	if c.Script != nil {
		script := *c.Script
		script.Systems = maps.Clone(script.Systems)
		delete(script.Systems, system)
		c.Script = &script
	}
	return c
}

// CheckStart refuses what the runner cannot start with, from the environment it starts in
// (getenv) and the programs it finds (lookPath), given the configuration Held left it: nothing
// left to offer, an agent whose program is not there. It returns what it lets start and is worth
// saying: a forge token missing, which a forge on this filesystem does without and a fetch over
// the network finds, an agent left to its own login, and claude's API key shadowed by an OAuth
// token. A system whose credential is not set, or the verse agent with no model key, is not
// refused here; the runner runs with it closed (Held).
func (c Config) CheckStart(getenv func(string) string, lookPath func(string) (string, error)) (warnings []string, err error) {
	if c.Repos != nil && getenv(EnvGitHubToken) == "" {
		warnings = append(warnings, EnvGitHubToken+" is not set: a fetch or a push over the network is refused where it reaches for it")
	}
	if len(c.Agents) == 0 && c.Script == nil {
		return nil, fmt.Errorf("the runner offers nothing it can run: agents.%s is closed, %s not being set, and nothing else is offered", AgentName, verse.EnvAPIKey)
	}
	if cc := c.codexConfig(); cc != nil {
		if _, err := lookPath(cc.Program()); err != nil {
			return nil, fmt.Errorf("agents.%s.bin: %s is not here: %w", codex.Agent, cc.Program(), err)
		}
		if why := codex.MissingKey(getenv); why != "" {
			warnings = append(warnings, why+". Codex will have to be logged in under the HOME its environment passes through, or its turns will fail")
		}
	}
	if cc := c.claudeConfig(); cc != nil {
		if _, err := lookPath(cc.Program()); err != nil {
			return nil, fmt.Errorf("agents.%s.bin: %s is not here: %w", claude.Agent, cc.Program(), err)
		}
		if why := claude.MissingKey(getenv); why != "" {
			warnings = append(warnings, why+". Claude will have to be logged in under the HOME its environment passes through, or its turns will fail")
		}
		if why := claude.Shadowed(getenv); why != "" {
			warnings = append(warnings, why)
		}
	}
	return warnings, nil
}

// naming is the sections whose ceilings name system, as a message says where.
func (c Config) naming(system string) []string {
	var out []string
	for _, doer := range c.doers() {
		if _, ok := c.systemsOf(doer)[system]; ok {
			out = append(out, sectionOf(doer)+".systems."+system)
		}
	}
	return out
}

// sectionOf is where a doer's section is in the file.
func sectionOf(doer string) string {
	if doer == ScriptDoer {
		return "script"
	}
	return "agents." + doer
}

// forge is the forge the runner's repos are on, its host, and where it fetches them from (url).
func (r *Repos) forge() (host, url string) {
	host = strings.ToLower(cmp.Or(r.Host, runner.DefaultHost)) // a host name is case-insensitive
	url = cmp.Or(r.Forge, "https://"+host)
	return host, url
}

// atGitHub reports whether the runner's forge is a GitHub: github.com itself, or one whose API the
// configuration names (`repos.api`). A directory standing in for a forge is neither: it has no
// pull requests and no issues.
func (c Config) atGitHub() bool {
	if c.Repos == nil {
		return false
	}
	if c.Repos.API != "" {
		return true
	}
	host, url := c.Repos.forge()
	return host == runner.DefaultHost && url == "https://"+runner.DefaultHost
}

// performs is the systems the runner performs for its tasks: `git` and `git-issue` where it
// checks out repos, `github` and `github-issue` where its forge is a GitHub, and `linear-issue`
// and `slack` where a doer's ceiling names them (and their credential is set: Held). Every doer's
// offer lists them, each under that doer's ceiling.
func (c Config) performs() []string {
	var out []string
	if c.Repos != nil {
		out = append(out, "git", "git-issue")
		if c.atGitHub() {
			out = append(out, "github", "github-issue")
		}
	}
	for _, name := range []string{"linear-issue", "slack"} {
		if len(c.naming(name)) > 0 {
			out = append(out, name)
		}
	}
	return out
}

// inner is the systems the runner stops inside a run rather than performs over the channel,
// and which doers it stops them for: the verse agent all four, codex and claude what each
// proposes, a script its questions alone, since its commands and files are one action and no
// step.
var inner = map[string][]string{
	"files":    {AgentName, codex.Agent, claude.Agent},
	"commands": {AgentName, codex.Agent, claude.Agent},
	"agent":    {AgentName},
	"question": {AgentName, codex.Agent, claude.Agent, ScriptDoer},
}

// OpenInside is the systems the runner stops inside a run for doer, each under an open ceiling
// (systems.Open): the ceilings of a runner whose operator is the author of its tasks, `verse
// up`'s, which says so rather than leaving them closed.
func OpenInside(doer string) map[string]System {
	out := map[string]System{}
	for name, doers := range inner {
		if slices.Contains(doers, doer) {
			out[name] = System{Ceiling: systems.Open()}
		}
	}
	return out
}

// Open is the configuration of a runner whose operator is the author of its tasks: scripts, and
// each agent named, each with what it is stopped on inside a run open (OpenInside).
func Open(agents ...string) Config {
	c := Config{Script: &Script{Systems: OpenInside(ScriptDoer)}}
	for _, name := range agents {
		if c.Agents == nil {
			c.Agents = map[string]Agent{}
		}
		c.Agents[name] = Agent{Systems: OpenInside(name)}
	}
	return c
}

// ceilings is each performed system's ceiling for doer, as the channel reads it at each act, as
// written: a `git` ceiling keeps to the runner's forge (Check). A performed system the doer's
// section does not name is there, closed.
func (c Config) ceilings(doer string) map[string]systems.Ceiling {
	out := map[string]systems.Ceiling{}
	for _, name := range c.performs() {
		out[name] = c.systemsOf(doer)[name].Ceiling
	}
	return out
}

// taskCeilings is every system's ceiling for doer, inner and performed, for the supervisor
// inside a run: a step beyond one is refused there, before anyone is asked about it.
func (c Config) taskCeilings(doer string) map[string]systems.Ceiling {
	out := c.ceilings(doer)
	for name := range inner {
		out[name] = c.systemsOf(doer)[name].Ceiling
	}
	return out
}

// intercepted is the systems an offer intercepts for a doer, as the offer publishes them: the
// performed systems, and the inner systems the doer is stopped on, each under the doer's ceiling.
func (c Config) intercepted(doer string) map[string]runner.System {
	out := map[string]runner.System{}
	for name, ceiling := range c.ceilings(doer) {
		out[name] = runner.System{Ceiling: ceiling}
	}
	for name, doers := range inner {
		if slices.Contains(doers, doer) {
			out[name] = runner.System{Ceiling: c.systemsOf(doer)[name].Ceiling}
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

// shellEnv is the variables a doer's task shell is given, and the proxy's: a task whose network
// allows hosts reaches them through the proxy its process announces (runnerd.UseEgress).
func (c Config) shellEnv(doer string) []string {
	var names []string
	if doer == ScriptDoer {
		if c.Script != nil {
			names = c.Script.ShellEnv
		}
	} else {
		names = c.Agents[doer].ShellEnv
	}
	if len(names) == 0 {
		names = task.DefaultShellEnv
	}
	return append(slices.Clone(names), runnerd.ProxyEnv...)
}

// envOf is the variables l's doer's shell is given (shellEnv): what its task's process holds of
// the runner's environment, and what its `with.env` may not name.
func (c Config) envOf(l *runner.Launch) []string { return c.shellEnv(c.doer(l)) }

// Load reads the verse runner's configuration file.
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
func (c *Config) checkForge(at string, sections map[string]System) error {
	ceiling := sections["git"].Ceiling
	if len(ceiling) == 0 || c.Repos == nil {
		return nil
	}
	host, _ := c.Repos.forge()
	forge := strings.ToLower(host) + "/**"
	for i, b := range ceiling {
		repos := b.Attributes["repo"]
		if len(repos) == 0 {
			return fmt.Errorf("%s.git.ceiling[%d]: names no repo; a git ceiling names the repositories it is for, on the forge (%s)", at, i, forge)
		}
		for _, p := range repos {
			if p = systems.Canonical(systems.FormRepo, p); !systems.PatternContains(forge, p, false) {
				return fmt.Errorf("%s.git.ceiling[%d].repo: %s is beyond the runner's forge (%s): it fetches from nowhere else", at, i, p, forge)
			}
		}
	}
	return nil
}

// checkBuilds refuses a network ceiling naming a mode the sandbox can't build: the runner would
// publish a ceiling no launch could meet, and every spawn would be refused for it. Said at start.
func (c Config) checkBuilds(networks []string) error {
	for _, doer := range c.doers() {
		for i, b := range c.systemsOf(doer)["network"].Ceiling {
			for _, mode := range b.Attributes["mode"] {
				if !slices.ContainsFunc(networks, func(n string) bool { return systems.Match(mode, n, false) }) {
					return fmt.Errorf("%s.systems.network.ceiling[%d].mode: %s is no network this runner's sandbox builds (it builds: %s)",
						sectionOf(doer), i, mode, strings.Join(networks, ", "))
				}
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
	if len(c.Agents) == 0 && c.Script == nil {
		return fmt.Errorf("the runner offers nothing: `agents:` names the agents it runs, `script:` says it runs scripts (`script: {}` for scripts with their questions and the network alone)")
	}
	for _, name := range slices.Sorted(maps.Keys(c.Agents)) {
		if err := c.checkAgent(name, c.Agents[name]); err != nil {
			return err
		}
	}
	if c.Script != nil {
		if err := checkShellEnv("script", c.Script.ShellEnv); err != nil {
			return err
		}
		if err := c.checkSystems(ScriptDoer, c.Script.Systems); err != nil {
			return err
		}
	}
	return nil
}

// checkShellEnv checks a doer's `shellEnv`: a list that names something, and no credential.
func checkShellEnv(at string, names []string) error {
	if names != nil && len(names) == 0 {
		return fmt.Errorf("%s.shellEnv: an empty list gives the shell nothing; leave it out for the default (%s)",
			at, strings.Join(task.DefaultShellEnv, ", "))
	}
	for _, name := range names {
		if slices.Contains(credentials, name) {
			return fmt.Errorf("%s.shellEnv: %s is a credential the runner acts with, and no task's shell holds one", at, name)
		}
	}
	return nil
}

// checkAgent checks one agent's section.
func (c *Config) checkAgent(name string, a Agent) error {
	at := "agents." + name
	takes, ok := takes[name]
	if !ok {
		return fmt.Errorf("%s: this runner runs no agent %q (it runs: %s)", at, name, strings.Join(agentOrder, ", "))
	}
	for key, set := range map[string]bool{"bin": a.Bin != "", "sandbox": a.Sandbox != "", "config": a.Config != nil} {
		if set && !slices.Contains(program[name], key) {
			own := cmp.Or(strings.Join(program[name], ", "), "none")
			return fmt.Errorf("%s.%s: %s takes no %s (its program's keys: %s; every agent's: shellEnv, settings, annotations, systems)",
				at, key, name, key, own)
		}
	}
	if err := checkShellEnv(at, a.ShellEnv); err != nil {
		return err
	}
	if name == codex.Agent {
		if err := c.codexConfig().Validate(); err != nil {
			return fmt.Errorf("%s: %w", at, err)
		}
	}
	for _, setting := range slices.Sorted(maps.Keys(a.Settings)) {
		own, ok := takes[setting]
		if !ok {
			return fmt.Errorf("%s.settings.%s: %s takes no setting %q (it takes: %s)", at, setting, name, setting,
				strings.Join(slices.Sorted(maps.Keys(takes)), ", "))
		}
		s := a.Settings[setting]
		if s.Values != nil && len(s.Values) == 0 {
			return fmt.Errorf("%s.settings.%s.values: an empty list takes nothing; leave it out for any", at, setting)
		}
		// A narrowing narrows: where the agent lists the values it takes (Claude's efforts), the
		// operator's are among them, or the offer would take a value the agent refuses when the
		// task runs.
		if len(own.Values) > 0 {
			for _, v := range s.Values {
				if err := own.Takes(v); err != nil {
					return fmt.Errorf("%s.settings.%s.values: %s does not take it: %w", at, setting, name, err)
				}
			}
		}
		// A default is held to the operator's own narrowing, and to the agent's: the offer would
		// refuse it named and run it unnamed.
		if s.Default != "" {
			for _, values := range [][]string{s.Values, own.Values} {
				if err := (runner.Setting{Values: values}).Takes(s.Default); err != nil {
					return fmt.Errorf("%s.settings.%s.default: outside the values taken: %w", at, setting, err)
				}
			}
		}
	}
	for _, ann := range slices.Sorted(maps.Keys(a.Annotations)) {
		want, ok := annotations[ann]
		if name != AgentName || !ok {
			return fmt.Errorf("%s.annotations.%s: %s takes no annotation %q (the verse agent takes: %s)", at, ann, name, ann,
				strings.Join(slices.Sorted(maps.Keys(annotations)), ", "))
		}
		if typ := a.Annotations[ann].Type; typ != want {
			return fmt.Errorf("%s.annotations.%s.type: %s takes a %s, not %q", at, ann, ann, want, typ)
		}
	}
	if err := c.checkSystems(name, a.Systems); err != nil {
		return err
	}
	// What an offered agent can't run without, its ceilings give: else no spawn of it could
	// ever be written here, and each would be refused for its environment when the runner is
	// at fault.
	for _, step := range requires[name] {
		system, verb, _ := systems.Split(step)
		if !a.Systems[system].Ceiling.Gives(step) {
			return fmt.Errorf("%s.systems.%s.ceiling: %s can't run without %s, which the ceiling does not give: write `%s: {ceiling: {}}` or `%s: {ceiling: {%s: {}}}`",
				at, system, name, step, system, system, verb)
		}
	}
	return nil
}

// checkSystems checks one doer's ceilings.
func (c *Config) checkSystems(doer string, sections map[string]System) error {
	at := sectionOf(doer) + ".systems"
	if err := c.checkForge(at, sections); err != nil {
		return err
	}
	performs := c.performs()
	for _, name := range slices.Sorted(maps.Keys(sections)) {
		sys := sections[name]
		if _, ok := systems.Get(name); !ok {
			return fmt.Errorf("%s.%s: no system is called that", at, name)
		}
		if doers, isInner := inner[name]; isInner {
			if !slices.Contains(doers, doer) {
				return fmt.Errorf("%s.%s: the runner does not stop %s on %s inside its run; of those, its section may name %s",
					at, name, doer, name, strings.Join(innerFor(doer), ", "))
			}
		} else if name != "network" && !slices.Contains(performs, name) {
			return fmt.Errorf("%s.%s: this runner does not perform %s (it performs: %s); %s", at, name, name,
				cmp.Or(strings.Join(performs, ", "), "nothing"), needsFor(name))
		}
		// A section saying nothing is the system closed, as no section is; refused, so that
		// `name: {}` is never read beside `name: {ceiling: {}}`, which opens it.
		if len(sys.Ceiling) == 0 {
			return fmt.Errorf("%s.%s: a section names a ceiling; with none it closes %s, as leaving it out does (`ceiling: {}` opens it)",
				at, name, name)
		}
		if err := sys.Ceiling.Check(name); err != nil {
			return fmt.Errorf("%s.%s.ceiling%w", at, name, err)
		}
	}
	return nil
}

// innerFor is the inner systems doer is stopped on, by name.
func innerFor(doer string) []string {
	var out []string
	for name, doers := range inner {
		if slices.Contains(doers, doer) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// needsFor says what a system's section needs of the configuration to be performed.
func needsFor(system string) string {
	switch system {
	case "git", "git-issue":
		return "a `repos` section is what checks out repos"
	case "github", "github-issue":
		return "a `repos` section whose forge is github.com, or `repos.api` naming the forge's GitHub API"
	}
	return "the runner performs none of its verbs"
}
