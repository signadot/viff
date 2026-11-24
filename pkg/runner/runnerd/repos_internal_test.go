package runnerd

import (
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/runner/runnerd/workspace"
)

// A push is made with the runner's own token: to a forge over the network, refused when the runner
// has none, rather than reaching it as somebody else's "authentication required"; to one on this
// filesystem, with none.
func TestTheRunnersTokenIsWhatAPushIsMadeWith(t *testing.T) {
	github := workspace.Repo{Remote: "https://github.com/acme/app"}
	if got, err := NewRepos(t.TempDir(), "", "the-runners-own").credential(github); err != nil || got != "the-runners-own" {
		t.Errorf("a forge got %q (%v), want the runner's token", got, err)
	}
	if _, err := NewRepos(t.TempDir(), "", "").credential(github); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("a runner with no token pushed to a forge over the network: %v", err)
	}
	local := workspace.Repo{Remote: "/srv/forge/acme/app"}
	if got, err := NewRepos(t.TempDir(), "/srv/forge", "").credential(local); err != nil || got != "" {
		t.Errorf("a forge on this filesystem was refused a push for want of a token: %q %v", got, err)
	}
	if r := NewRepos(t.TempDir(), "", ""); r.Forge != DefaultForge {
		t.Errorf("no forge named fetches from %q, want %s", r.Forge, DefaultForge)
	}
}
