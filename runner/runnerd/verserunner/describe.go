package verserunner

import (
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/codex"
)

// AgentName is the agent the verse runner offers.
const AgentName = "verse"

// Describe is what the verse runner offers under c and s: verse's own agent, which holds a
// conversation, when it has a model to reach; Codex, when the configuration says how to run it;
// and scripts always.
//
// Each offer lists the systems it intercepts (Config.intercepted): the ones the runner performs
// for every doer, with their ceilings, and the ones it stops inside a run for that doer; and,
// under `with.network`, the networks its sandbox builds (networks). `effort` is offered once
// the runner applies it.
func (c Config) Describe(s Secrets, networks []string) runner.Description {
	var d runner.Description
	if s.PortkeyAPIKey != "" {
		d.Offers = append(d.Offers, runner.Offer{
			Agent:       AgentName,
			With:        map[string]runner.Accepts{"model": runner.AcceptsAny},
			Annotations: c.Annotations,
			Systems:     c.intercepted(AgentName),
			Converse:    true,
		})
	}
	if c.Codex != nil {
		// Codex takes a model and a reasoning effort on its turns; its commands and patches pass
		// the loop's gates, as do the fetches a command asks to make.
		d.Offers = append(d.Offers, runner.Offer{
			Agent:   codex.Agent,
			With:    map[string]runner.Accepts{"model": runner.AcceptsAny, "effort": runner.AcceptsAny},
			Systems: c.intercepted(codex.Agent),
		})
	}
	d.Offers = append(d.Offers, runner.Offer{Script: true, Systems: c.intercepted("script")})
	if len(networks) > 0 {
		for i := range d.Offers {
			if d.Offers[i].With == nil {
				d.Offers[i].With = map[string]runner.Accepts{}
			}
			d.Offers[i].With["network"] = runner.AcceptsOneOf(networks...)
		}
	}
	return d
}
