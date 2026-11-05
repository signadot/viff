package task

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/systems"
)

// AN AUTONOMY WITHIN ANOTHER lets through no call the other holds back: each row's mode is at
// most what the other decides for the calls the row matches, over the calls the environment
// gives, and the default no looser on what the rows above it leave.
func TestAnAutonomyWithinAnother(t *testing.T) {
	outer := Autonomy{
		{Match: &Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"ref": systems.Glob("refs/heads/verse/auto/*")}}, Then: Then{Mode: ThenAllow}},
		{Match: &Match{System: "git", Verb: "push"}, Then: Then{Mode: ThenDeny}},
		{Match: &Match{System: "commands"}, Then: Then{Mode: ThenAsk}},
		{Then: Then{Mode: ThenAsk}},
	}
	// What the inner task reaches: git whole, commands, and the network.
	env := systems.Environment{Systems: systems.Systems{
		"git":      {{Attributes: map[string]string{"repo": "github.com/signadot/verse", "from": "main"}, Verbs: map[string][]systems.Shape{"fetch": {{}}, "push": {{}}}}},
		"commands": {{Verbs: map[string][]systems.Shape{"run": {{}}}}},
		"network":  {{Attributes: map[string]string{"mode": "open"}}},
	}}
	// And one that reaches no git: a row of outer's on git then bounds nothing of it.
	noGit := systems.Environment{Systems: systems.Systems{
		"commands": {{Verbs: map[string][]systems.Shape{"run": {{}}}}},
		"network":  {{Attributes: map[string]string{"mode": "open"}}},
	}}
	for name, c := range map[string]struct {
		inner Autonomy
		env   systems.Environment
		says  string // "" for within
	}{
		"the same": {outer, env, ""},
		"stricter": {Autonomy{{Match: &Match{System: "commands"}, Then: Then{Mode: ThenDeny}}, {Then: Then{Mode: ThenDeny}}}, env, ""},
		"the allowed push, the rest denied as the outer denies it": {Autonomy{
			{Match: &Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"ref": systems.Glob("refs/heads/verse/auto/*")}}, Then: Then{Mode: ThenAllow}},
			{Match: &Match{System: "git", Verb: "push"}, Then: Then{Mode: ThenDeny}},
			{Then: Then{Mode: ThenAsk}}}, env, ""},
		"none, which asks, where the outer denies a push": {nil, env, "`then: ask` on every call, where the autonomy it must lie within says deny on git.push"},
		"none, which asks, reaching no git":               {nil, noGit, ""},
		"a looser default":                                {Autonomy{{Then: Then{Mode: ThenAllow}}}, env, "`then: allow` on every call, where the autonomy it must lie within says deny on git.push"},
		"a looser default, reaching no git":               {Autonomy{{Then: Then{Mode: ThenAllow}}}, noGit, "`then: allow` on every call, where the autonomy it must lie within says ask on commands"},
		"allowing what the outer asks":                    {Autonomy{{Match: &Match{System: "commands"}, Then: Then{Mode: ThenAllow}}, {Then: Then{Mode: ThenAsk}}}, env, "`then: allow` on commands, where the autonomy it must lie within says ask on commands"},
		"asking what the outer denies":                    {Autonomy{{Match: &Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"ref": systems.Glob("refs/heads/release/*")}}, Then: Then{Mode: ThenAsk}}, {Match: &Match{System: "git", Verb: "push"}, Then: Then{Mode: ThenDeny}}, {Then: Then{Mode: ThenAsk}}}, env, "`then: ask` on git.push ref=!glob refs/heads/release/*, where the autonomy it must lie within says deny on git.push"},
		"a wider push":                                    {Autonomy{{Match: &Match{System: "git", Verb: "push"}, Then: Then{Mode: ThenAllow}}, {Then: Then{Mode: ThenAsk}}}, env, "`then: allow` on git.push, where the autonomy it must lie within says deny on git.push"},
		// The default row covers every call the rows above leave, so it is held to the outer's rows on those too.
		"a default that allows what a row of the outer denies": {Autonomy{{Match: &Match{System: "network"}, Then: Then{Mode: ThenDeny}}, {Then: Then{Mode: ThenAllow}}}, env, "`then: allow` on every call, where the autonomy it must lie within says deny on git.push"},
		// A `null` in the inner row is any value: wider than the outer's pattern, not a call its narrower row misses.
		"a wildcard over a denied pattern": {Autonomy{{Match: &Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"ref": ir.Null()}}, Then: Then{Mode: ThenAllow}}, {Then: Then{Mode: ThenAsk}}}, env, "`then: allow` on git.push ref=null, where the autonomy it must lie within says deny on git.push"},
	} {
		err := c.inner.Within(outer, c.env)
		switch {
		case c.says == "" && err != nil:
			t.Errorf("%s: %v, want within", name, err)
		case c.says != "" && err == nil:
			t.Errorf("%s: within, want refused for %q", name, c.says)
		case c.says != "" && !strings.Contains(err.Error(), c.says):
			t.Errorf("%s: refused for another reason: %v", name, err)
		}
	}
}
