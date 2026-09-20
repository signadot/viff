package runner_test

import (
	"testing"

	"github.com/signadot/verse/runner"
)

// A runner acts as `runner:` its identity, or its name, and an identity is the id alone.
func TestARunnerActsAsItsKindedIdentity(t *testing.T) {
	e := runner.Entity{Name: "coderunner", URL: "http://coderunner:9802"}
	if got := e.Principal(); got != "runner:coderunner" {
		t.Fatalf("a runner with no identity acts as %q", got)
	}
	e.Identity = "gpu-bot"
	if got := e.Principal(); got != "runner:gpu-bot" || e.Check() != nil {
		t.Fatalf("a runner with an identity acts as %q: %v", got, e.Check())
	}
	for _, id := range []string{"runner:gpu-bot", "user:ana@example.com", "gpu bot"} {
		e.Identity = id
		if err := e.Check(); err == nil {
			t.Errorf("identity %q was taken", id)
		}
	}
}
