package verserunner

import (
	"slices"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/claude"
	"github.com/signadot/verse/runner/runnerd/codex"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// AgentName is the agent the verse runner offers.
const AgentName = "verse"

// Describe is what the verse runner offers under c and s: verse's own agent when it has a model
// to reach; Codex and Claude, each when the configuration says how to run it; and scripts always.
// The first is the runner's default agent (defaultAgent). Every agent holds a conversation: each
// driver takes words between turns as its next turn (task.Conversation); a script has no turn.
//
// Each offer holds the systems it intercepts (Config.intercepted), each under its ceiling, and
// the network, with what the sandbox builds of it (networks) beside the operator's ceiling. An
// agent's settings are the ones it takes, narrowed by the operator (Config.Agents).
func (c Config) Describe(s Secrets, networks []string) runner.Description {
	d := runner.Description{Version: runner.DescriptionVersion}
	for _, name := range c.agents(s) {
		o := runner.Offer{Agent: &runner.AgentOffer{Name: name, Settings: c.settings(name)}, Systems: c.intercepted(name), Converse: true}
		if name == AgentName {
			o.Agent.Annotations = c.Annotations
		}
		o.Requires = requires[name]
		d.Offers = append(d.Offers, o)
	}
	d.Offers = append(d.Offers, runner.Offer{Script: true, Systems: c.intercepted("script")})
	for i := range d.Offers {
		if d.Offers[i].Systems == nil {
			d.Offers[i].Systems = map[string]runner.System{}
		}
		d.Offers[i].Systems["network"] = runner.System{Builds: networkBuilds(networks), Ceiling: c.Systems["network"].Ceiling}
	}
	return d
}

// networkBuilds is what a sandbox building networks builds of the network: those modes, and,
// where allow is one, a connection to any host through its proxy. The network is apart from the
// empty block (systems.Block.Given), so the connection is said.
func networkBuilds(networks []string) systems.Ceiling {
	b := systems.CeilingBlock{Attributes: map[string][]string{"mode": networks}}
	if slices.Contains(networks, runner.NetworkAllow) {
		b.Verbs = map[string][]systems.Shape{"tcp-client": {{}}}
	}
	return systems.Ceiling{b}
}

// agents is the agents the runner offers, its default first: verse's own where it has a model
// key, then Codex and Claude, each where the configuration says how to run it.
func (c Config) agents(s Secrets) []string {
	var out []string
	if s.PortkeyAPIKey != "" {
		out = append(out, AgentName)
	}
	if c.Codex != nil {
		out = append(out, codex.Agent)
	}
	if c.Claude != nil {
		out = append(out, claude.Agent)
	}
	return out
}

// requires is the steps each agent can't run without: Codex's shell and its edits are its own,
// and Claude's.
var requires = map[string][]string{
	codex.Agent:  {task.StepCommandsRun, task.StepFilesWrite},
	claude.Agent: {task.StepCommandsRun, task.StepFilesWrite},
}

// takes is the settings each agent takes, as the runner applies them: the verse agent its model,
// over the configured one; Codex its model and the reasoning effort of its turns, each over
// Codex's own; Claude its session's model and one of its efforts, each over Claude's own.
var takes = map[string]map[string]runner.Setting{
	AgentName:    {runner.SettingModel: {}},
	codex.Agent:  {runner.SettingModel: {Default: true}, "effort": {Default: true}},
	claude.Agent: {runner.SettingModel: {Default: true}, "effort": {Default: true, Values: claude.Efforts}},
}

// settings is what agent takes, the operator's narrowing applied (Config.Agents); the verse
// agent has a default model where the configuration names one.
func (c Config) settings(agent string) map[string]runner.Setting {
	out := map[string]runner.Setting{}
	for name, s := range takes[agent] {
		if values, narrowed := c.Agents[agent][name]; narrowed {
			s.Values = values
		}
		if agent == AgentName && name == runner.SettingModel {
			s.Default = c.Models.Model != ""
		}
		out[name] = s
	}
	return out
}
