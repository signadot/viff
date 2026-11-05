package stock

import (
	"slices"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/claude"
	"github.com/signadot/verse/pkg/runner/runnerd/codex"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// AgentName is verse's own agent.
const AgentName = "verse"

// Describe is what the runner offers under c: one offer for each agent `agents:` names, in
// agentOrder; and scripts where c has a `script:` section. Every agent holds a conversation: each driver takes words between turns as
// its next turn (task.Conversation); a script has no turn.
//
// Each offer holds the systems it intercepts (Config.intercepted), each under its doer's ceiling,
// and the network, with what the sandbox builds of it (networks) beside the doer's ceiling. An
// agent's settings are the ones it takes, narrowed by the operator (Agent.Settings).
func (c Config) Describe(networks []string) runner.Description {
	d := runner.Description{Version: runner.DescriptionVersion}
	for _, name := range c.agents() {
		o := runner.Offer{Agent: &runner.AgentOffer{Name: name, Settings: c.settings(name)}, Systems: c.intercepted(name), Converse: true}
		for ann, a := range c.Agents[name].Annotations {
			if o.Agent.Annotations == nil {
				o.Agent.Annotations = map[string]string{}
			}
			o.Agent.Annotations[ann] = a.Type
		}
		o.Requires = requires[name]
		d.Offers = append(d.Offers, o)
	}
	if c.Script != nil {
		d.Offers = append(d.Offers, runner.Offer{Script: true, Systems: c.intercepted(ScriptDoer)})
	}
	for i := range d.Offers {
		if d.Offers[i].Systems == nil {
			d.Offers[i].Systems = map[string]runner.System{}
		}
		doer := d.Offers[i].Name()
		d.Offers[i].Systems["network"] = runner.System{Builds: networkBuilds(networks), Ceiling: c.systemsOf(doer)["network"].Ceiling}
	}
	return d
}

// networkBuilds is what a sandbox building networks builds of the network: those modes, and,
// where allow is one, a connection to any host through its proxy. The network is apart from the
// empty block (systems.Block.Given), so the connection is said.
func networkBuilds(networks []string) systems.Ceiling {
	b := systems.CeilingBlock{Attributes: map[string]*ir.Node{"mode": systems.OneOf(networks...)}}
	if slices.Contains(networks, runner.NetworkAllow) {
		b.Verbs = map[string][]systems.Shape{"tcp-client": {{}}}
	}
	return systems.Ceiling{b}
}

// requires is the steps each agent can't run without: Codex's shell and its edits are its own,
// and Claude's.
var requires = map[string][]string{
	codex.Agent:  {task.StepCommandsRun, task.StepFilesWrite},
	claude.Agent: {task.StepCommandsRun, task.StepFilesWrite},
}

// takes is the settings each agent takes, as the runner applies them: the verse agent its model;
// Codex its model and the reasoning effort of its turns, each over Codex's own; Claude its
// session's model and one of its efforts, each over Claude's own.
var takes = map[string]map[string]runner.Setting{
	AgentName:    {runner.SettingModel: {}},
	codex.Agent:  {runner.SettingModel: {Default: true}, "effort": {Default: true}},
	claude.Agent: {runner.SettingModel: {Default: true}, "effort": {Default: true, Values: claude.Efforts}},
}

// settings is what agent takes, the operator's narrowing applied (Agent.Settings): a setting
// has a default where the agent has its own, or the configuration names one.
func (c Config) settings(agent string) map[string]runner.Setting {
	out := map[string]runner.Setting{}
	for name, s := range takes[agent] {
		own := c.Agents[agent].Settings[name]
		if own.Values != nil {
			s.Values = own.Values
		}
		s.Default = s.Default || own.Default != ""
		out[name] = s
	}
	return out
}

// withDefaults is l with the configured defaults of its agent's settings and annotations filled
// in where l sets none: what the agent runs with, and what the record says it ran with.
func (c Config) withDefaults(l runner.Launch, agent string) runner.Launch {
	a, ok := c.Agents[agent]
	if !ok {
		return l
	}
	out := l.AgentSettings()
	out.Name = agent
	out.Settings = fill(out.Settings, a.Settings, func(s Setting) string { return s.Default })
	out.Annotations = fill(out.Annotations, a.Annotations, func(a Annotation) string { return a.Default })
	l.Agent = &out
	return l
}

// fill is set, with each default of from's that set has no value for; set is not changed.
func fill[T any](set map[string]string, from map[string]T, def func(T) string) map[string]string {
	var out map[string]string
	for name, v := range from {
		d := def(v)
		if _, has := set[name]; has || d == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(set)+1)
			for k, v := range set {
				out[k] = v
			}
		}
		out[name] = d
	}
	if out == nil {
		return set
	}
	return out
}
