package main

import (
	"testing"

	"github.com/signadot/verse/config"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/verserunner"
)

// THE EMBEDDED RUNNER TAKES A VIRTUAL KEY: a firing whose spawn names one carries it as the
// doer's `verse.portkey/virtual-key`, and `verse up`'s runner offers that annotation, so the
// launch is run on the key and not refused at every firing.
func TestTheEmbeddedRunnerTakesAVirtualKey(t *testing.T) {
	cfg, secrets := embeddedRunner(config.LLMConfig{APIKey: "pk"}, nil)
	if err := cfg.Check(); err != nil {
		t.Fatal(err)
	}
	l := runner.Launch{ID: "t1", Prompt: "hi", Doer: runner.Doer{Agent: verserunner.AgentName,
		With:        runner.With{Network: &runner.Network{Mode: runner.NetworkOpen}},
		Annotations: map[string]string{verserunner.AnnotationVirtualKey: "vk-team"}}}
	if err := runnerd.CheckOffer(&l, cfg.Describe(secrets, []string{runner.NetworkOpen})); err != nil {
		t.Errorf("a launch on a virtual key was refused: %v", err)
	}
}
