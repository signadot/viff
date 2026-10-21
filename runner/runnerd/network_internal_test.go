package runnerd

import (
	"slices"
	"testing"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/systems"
)

// A TASK'S NETWORK IS ITS LAUNCH'S, absent being none; an agent reaches its model under any of
// them, through a proxy whose own hosts are the model's. What else the proxy lets through is the
// environment's to say, connection by connection (connections), not a list of the runner's.
func TestATasksNetworkIsItsLaunchsAndAnAgentsModel(t *testing.T) {
	h := &SubprocessHandler{SubprocessConfig: SubprocessConfig{AgentHosts: []string{"api.portkey.ai"}}}
	none, open := netEnv(runner.NetworkNone), netEnv(runner.NetworkOpen)
	allow := netEnv(runner.NetworkAllow, "pypi.org")
	for _, c := range []struct {
		name   string
		script bool
		env    systems.Environment
		mode   string
		own    []string
	}{
		{"a script, no network", true, systems.Environment{}, runner.NetworkNone, nil},
		{"a script, none", true, none, runner.NetworkNone, nil},
		{"a script, allow", true, allow, runner.NetworkAllow, nil},
		{"an agent, no network", false, systems.Environment{}, runner.NetworkAllow, []string{"api.portkey.ai"}},
		{"an agent, open", false, open, runner.NetworkOpen, nil},
		{"an agent, none", false, none, runner.NetworkAllow, []string{"api.portkey.ai"}},
		{"an agent, allow", false, allow, runner.NetworkAllow, []string{"api.portkey.ai"}},
	} {
		l := &runner.Launch{Prompt: "p", Environment: c.env}
		if c.script {
			l = &runner.Launch{Script: "x", Environment: c.env}
		}
		n, got := h.network(l)
		if n.Mode != c.mode || !slices.Equal(got, c.own) {
			t.Errorf("%s: %s %v, want %s %v", c.name, n.Mode, got, c.mode, c.own)
		}
	}
	if shapes := allow.Systems["network"][0].Verbs["tcp-client"]; len(shapes) != 1 {
		t.Errorf("the launch's tcp-client shapes were changed: %v", shapes)
	}
}
