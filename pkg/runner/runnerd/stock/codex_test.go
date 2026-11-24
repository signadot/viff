package stock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/task"
)

// THE CONFIGURATION SAYS HOW CODEX RUNS, or the agent is not offered: a `codex:` section is
// checked where it is written, offers the codex agent with a model and an effort, and lets its
// process reach Codex's API whatever the task's network; a runner with none refuses a launch
// for codex rather than guessing how to run it.
func TestTheCodexSectionOffersTheAgent(t *testing.T) {
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "c.tony")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, _, err := Load(write("agents: {codex: {sandbox: everywhere}}\n")); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("a codex section with no such sandbox loaded: %v", err)
	}
	_, c, err := Load(write("agents:\n  codex:\n    settings: {model: {default: m}}\n    systems: {commands: {ceiling: {}}, files: {ceiling: {}}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	var offer *runner.Offer
	for _, o := range c.Describe(nil).Offers {
		if o.Agent != nil && o.Agent.Name == "codex" {
			offer = &o
		}
	}
	if offer == nil {
		t.Fatal("the codex agent is not among the offers")
	}
	if s, ok := offer.Agent.Settings["effort"]; !ok || !s.Default {
		t.Errorf("the codex offer takes no effort, or has no default of its own: %+v", offer.Agent.Settings)
	}
	if s, ok := offer.Agent.Settings[runner.SettingModel]; !ok || !s.Default {
		t.Errorf("the codex offer takes no model, or has no default of its own: %+v", offer.Agent.Settings)
	}
	hosts, err := modelHosts(c, Secrets{})
	if err != nil || !strings.Contains(strings.Join(hosts, ","), "api.openai.com") {
		t.Errorf("model hosts %v (%v), want Codex's API among them", hosts, err)
	}

	r := New(opened(Config{}), Secrets{}, t.TempDir(), os.Stderr)
	tk := r.Build(runner.Launch{ID: "c1", Prompt: "go", Agent: &runner.Agent{Name: "codex"}}, task.NewRegistry())
	if _, err := tk.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "agents.codex") {
		t.Errorf("a runner with no codex section ran codex: %v, want it failed saying why", err)
	}
}
