package runnerd

import (
	"slices"
	"testing"

	"github.com/signadot/verse/runner"
)

// A TASK'S NETWORK IS ITS LAUNCH'S, absent being none; an agent reaches its model under any of
// them, through a proxy that allows it beside what the launch allows.
func TestATasksNetworkIsItsLaunchsAndAnAgentsModel(t *testing.T) {
	h := &SubprocessHandler{SubprocessConfig: SubprocessConfig{AgentHosts: []string{"api.portkey.ai"}}}
	script, agent := runner.Doer{Script: "x"}, runner.Doer{Agent: "verse"}
	none := &runner.Network{Mode: runner.NetworkNone}
	allow := &runner.Network{Mode: runner.NetworkAllow, Allow: []string{"pypi.org"}}
	for _, c := range []struct {
		name  string
		doer  runner.Doer
		net   *runner.Network
		mode  string
		allow []string
	}{
		{"a script, no network", script, nil, runner.NetworkNone, nil},
		{"a script, none", script, none, runner.NetworkNone, nil},
		{"a script, allow", script, allow, runner.NetworkAllow, []string{"pypi.org"}},
		{"an agent, no network", agent, nil, runner.NetworkAllow, []string{"api.portkey.ai"}},
		{"an agent, open", agent, &runner.Network{Mode: runner.NetworkOpen}, runner.NetworkOpen, nil},
		{"an agent, none", agent, none, runner.NetworkAllow, []string{"api.portkey.ai"}},
		{"an agent, allow", agent, allow, runner.NetworkAllow, []string{"pypi.org", "api.portkey.ai"}},
	} {
		d := c.doer
		d.With.Network = c.net
		n, got := h.network(&runner.Launch{Doer: d})
		if n.Mode != c.mode || !slices.Equal(got, c.allow) {
			t.Errorf("%s: %s %v, want %s %v", c.name, n.Mode, got, c.mode, c.allow)
		}
	}
	if len(allow.Allow) != 1 {
		t.Errorf("the launch's allow list was changed: %v", allow.Allow)
	}
}
