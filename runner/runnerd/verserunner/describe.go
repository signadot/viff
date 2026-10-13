package verserunner

import "github.com/signadot/verse/runner"

// AgentName is the agent the verse runner offers.
const AgentName = "verse"

// Describe is what the verse runner offers under c and s: verse's own agent, when it has a model
// to reach, and scripts.
//
// It lists only what the runner honours. `effort` and `network` are offered once the runner
// applies them.
func (c Config) Describe(s Secrets) runner.Description {
	var d runner.Description
	if s.PortkeyAPIKey != "" {
		agent := runner.Offer{
			Agent: AgentName,
			With:  map[string]runner.Accepts{"model": runner.AcceptsAny},
			// The agent's write and exec verbs are gated by the harness, so a gate can stop
			// them.
			Intercepts: []string{"files.write", "commands.run"},
		}
		if len(c.Annotations) > 0 {
			agent.Annotations = c.Annotations
		}
		d.Offers = append(d.Offers, agent)
	}
	// A script names what runs it in its own #! line, from whatever the runner's image has, so
	// the offer takes no `with`.
	d.Offers = append(d.Offers, runner.Offer{Script: true})
	return d
}
