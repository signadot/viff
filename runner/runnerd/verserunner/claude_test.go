package verserunner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// THE CONFIGURATION SAYS HOW CLAUDE RUNS, or the agent is not offered: a `claude:` section
// offers the claude agent with a model and an effort, intercepting its files, commands and
// questions, and lets its process reach Claude's API whatever the task's network; a runner with
// none refuses a launch for claude rather than guessing how to run it.
func TestTheClaudeSectionOffersTheAgent(t *testing.T) {
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "c.tony")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	_, c, err := Load(write("agents:\n  claude:\n    settings: {model: {default: m}}\n    systems: {commands: {ceiling: {}}, files: {ceiling: {}}, question: {ceiling: {}}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	var offer *runner.Offer
	for _, o := range c.Describe(nil).Offers {
		if o.Agent != nil && o.Agent.Name == "claude" {
			offer = &o
		}
	}
	if offer == nil {
		t.Fatal("the claude agent is not among the offers")
	}
	if s, ok := offer.Agent.Settings["effort"]; !ok || !s.Default || s.Takes("max") != nil || s.Takes("minimal") == nil {
		t.Errorf("the claude offer does not take Claude's efforts, and only those, with a default of its own: %+v", offer.Agent.Settings)
	}
	if s, ok := offer.Agent.Settings[runner.SettingModel]; !ok || !s.Default {
		t.Errorf("the claude offer takes no model, or has no default of its own: %+v", offer.Agent.Settings)
	}
	for _, sys := range []string{"files", "commands", "question"} {
		if _, ok := offer.Systems[sys]; !ok {
			t.Errorf("the claude offer does not intercept %s: %v", sys, offer.Systems)
		}
	}
	hosts, err := modelHosts(c, Secrets{})
	if err != nil || !strings.Contains(strings.Join(hosts, ","), "api.anthropic.com") {
		t.Errorf("model hosts %v (%v), want Claude's API among them", hosts, err)
	}

	r := New(opened(Config{}), Secrets{}, t.TempDir(), os.Stderr)
	tk := r.Build(runner.Launch{ID: "c1", Prompt: "go", Agent: &runner.Agent{Name: "claude"}}, task.NewRegistry())
	if _, err := tk.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "agents.claude") {
		t.Errorf("a runner with no claude section ran claude: %v, want it failed saying why", err)
	}
}
