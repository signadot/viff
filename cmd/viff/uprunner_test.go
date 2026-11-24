package main

import (
	"testing"

	"go.viff.dev/viff/internal/config"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/runner/runnerd/stock"
	viffagent "go.viff.dev/viff/pkg/runner/runnerd/viff"
	"go.viff.dev/viff/pkg/systems"
)

// THE LOCAL RUNNER TAKES A VIRTUAL KEY: a firing whose spawn names one carries it as the
// agent's `viff.portkey/virtual-key`, and `viff up`'s runner offers that annotation, so the
// launch is run on the key and not refused at every firing.
func TestTheLocalRunnerTakesAVirtualKey(t *testing.T) {
	cfg, _ := localRunnerOffers(config.LLMConfig{APIKey: "pk"}, nil)
	if err := cfg.Check(); err != nil {
		t.Fatal(err)
	}
	l := runner.Launch{ID: "t1", Prompt: "hi", Agent: &runner.Agent{Name: stock.AgentName,
		Annotations: map[string]string{viffagent.AnnotationVirtualKey: "vk-team"}},
		Environment: systems.Environment{Systems: systems.Systems{"network": {{Attributes: map[string]string{"mode": runner.NetworkOpen}}}}}}
	if err := runnerd.CheckOffer(&l, cfg.Describe([]string{runner.NetworkOpen})); err != nil {
		t.Errorf("a launch on a virtual key was refused: %v", err)
	}
}
