package viffd

import (
	"testing"

	"go.viff.dev/viff/pkg/api"
)

// A runner acts as `runner:` its identity, or its name, and an identity is the id alone.
func TestARunnerActsAsItsKindedIdentity(t *testing.T) {
	e := api.Runner{Name: "viff-runner", URL: "http://viff-runner:9802", Subject: "system:serviceaccount:viff:viff-runner"}
	if got := e.Principal(); got != "runner:viff-runner" {
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
