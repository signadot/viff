package verserunner

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

// A bad allowlist entry fails when the RUNNER starts, not when the first task tries to reach
// something — otherwise it reads as the network being down.
func TestABadAllowlistEntryIsRefusedWhereItIsWritten(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.tony")
	if err := os.WriteFile(bad, []byte("environments:\n- name: e\n  repos:\n  - {name: r, repo: \".\", base: main}\n  egress: [\"*\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("a bad allowlist entry loaded: %v", err)
	}
}

// End to end: a launch into an environment whose egress this runner cannot restrict is refused on
// the wire, with the reason.
//
// On a machine without bwrap that is every such launch, which is the honest state of this feature
// here — the proxy and the forwarder are exercised for real (runnerd's tests), and the combination
// that makes them a floor is not, because the mechanism that makes it one does not exist on macOS.
func TestALaunchNeedingRestrictedEgressIsRefusedHere(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Driver: driverLLM, Egress: []string{"api.anthropic.com"},
		}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{ID: "e1", Repos: ro(origin), Prompt: "x"})
	e := h.Err()
	if e == "" {
		t.Fatal("a launch asking for containment this runner cannot build was accepted")
	}
	for _, want := range []string{"refused", "network"} {
		if !strings.Contains(e, want) {
			t.Errorf("the refusal does not say %q: %s", want, e)
		}
	}
	if r.running() != 0 {
		t.Error("a refused launch started a process anyway")
	}
}

// The shipped sample's hardened environment loads and asks for what it says it asks for.
func TestShippedHardenedEnvironmentAsksForRestrictedEgress(t *testing.T) {
	_, c, err := Load(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	env, err := c.Environment("verse-dev-hardened")
	if err != nil {
		t.Fatalf("the sample's hardened environment: %v", err)
	}
	if n := networkFor(env); n.Mode != runnerd.NetworkRestricted {
		t.Errorf("it declares an allowlist but asks for a network %q", n.Mode)
	}
	if _, err := runnerd.NewEgress("x", env.Egress, nil); err != nil {
		t.Errorf("its allowlist does not build a proxy: %v", err)
	}
}

// A missing model credential is refused at the spawn, naming the variable.
//
// It used to be discovered at the first model call and reported as the vendor's opinion —
// `Portkey Error: Invalid API Key` — which says nothing about which variable, or that it is read
// in the RUNNER's environment rather than verse's. Found by driving it.
func TestAMissingModelCredentialIsRefusedNotDiscovered(t *testing.T) {
	origin := initRepo(t)
	cfg := &Config{
		Default: "e",
		Environments: []Environment{{
			Name: "e", Driver: driverLLM,
		}},
	}
	r := testRunnerWith(t, cfg, Secrets{})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{ID: "k1", Repos: ro(origin), Prompt: "x"})
	e := h.Err()
	for _, want := range []string{"refused", EnvPortkeyAPIKey, "runner's environment"} {
		if !strings.Contains(e, want) {
			t.Errorf("the refusal does not say %q: %s", want, e)
		}
	}
	if r.running() != 0 {
		t.Error("a task that cannot possibly finish was started anyway")
	}
}
