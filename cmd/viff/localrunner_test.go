package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.viff.dev/viff/internal/config"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/runner/runnerd/stock"
	"go.viff.dev/viff/pkg/task"
)

// THE LOCAL RUNNER'S TOKEN is made on the first run on a data dir, kept there for the user alone,
// and the same token on every run after, so the runner a crashed run left still proves itself.
func TestTheLocalRunnersTokenIsMadeOnceAndKept(t *testing.T) {
	dir := t.TempDir()
	path, first, err := localToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the token file = %v %v, want 0600", fi, err)
	}
	if len(first) != 64 {
		t.Fatalf("the token = %q, want 64 hex digits", first)
	}
	if _, err := runner.StaticSubject(first); err != nil {
		t.Fatalf("the token is no static token: %v", err)
	}
	if _, again, err := localToken(dir); err != nil || again != first {
		t.Fatalf("the second run's token = %q %v, want the first's", again, err)
	}
}

// THE CONFIG `viff up` WRITES is one runnerd loads as it loads any, strictly: where it listens,
// the viff instance it serves, and what it offers. The address it listens on is read back by the next run,
// and moved where the port is taken.
func TestTheLocalRunnersConfigIsOneRunnerdLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), localRunnerConfig)
	c, _ := localRunnerOffers(config.LLMConfig{APIKey: "k", Model: "m"}, task.DefaultShellEnv)
	viff := runnerd.Viff{URL: "http://localhost:9797", Issuer: "https://viff.example.com"}
	if err := writeLocalRunnerConfig(path, "127.0.0.1:9803", viff, c); err != nil {
		t.Fatal(err)
	}
	rc, sc, err := stock.Load(path)
	if err != nil {
		b, _ := os.ReadFile(path)
		t.Fatalf("runnerd refuses the config viff up writes: %v\n%s", err, b)
	}
	if rc.Listen != "127.0.0.1:9803" || rc.Viff == nil || rc.Viff.URL != viff.URL || rc.Viff.Issuer != viff.Issuer {
		t.Fatalf("the door = %+v %+v", rc, rc.Viff)
	}
	if _, ok := sc.Agents[stock.AgentName]; !ok || sc.Script == nil {
		t.Fatalf("what it offers = %+v, want the viff agent and scripts", sc)
	}
	if got := recordedListen(path); got != "127.0.0.1:9803" {
		t.Fatalf("the recorded address = %q", got)
	}
	if err := rewriteListen(path, "127.0.0.1:9804"); err != nil {
		t.Fatal(err)
	}
	if got := recordedListen(path); got != "127.0.0.1:9804" {
		t.Fatalf("after a move the recorded address = %q", got)
	}
	if got := recordedListen(filepath.Join(t.TempDir(), "none.tony")); got != "" {
		t.Fatalf("with no config the recorded address = %q, want none", got)
	}
}

// A VIFF'S DATA DIR IS ITS ADDRESS UNDER ~/.viff, or under $VIFF_HOME where that is set, so a
// bare `viff up` comes back to the same viff.
func TestTheDataDirIsTheAddressUnderTheViffHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIFF_HOME", home)
	for addr, want := range map[string]string{
		"localhost:9797": "localhost_9797",
		":9797":          "localhost_9797",
		"127.0.0.1:8080": "127.0.0.1_8080",
	} {
		got, err := defaultDataDir(addr)
		if err != nil || got != filepath.Join(home, want) {
			t.Errorf("defaultDataDir(%q) = %q %v, want %s", addr, got, err, filepath.Join(home, want))
		}
	}
}

// A runnerd of another build than viff is named, with both builds: a commit apart, or one tree
// with changes and one without, is another build. A build either side does not stamp is not
// checked, since nothing can be said of it.
func TestCompareBuilds(t *testing.T) {
	const (
		rel   = "v0.4.0"
		head  = "v0.0.0-20261103135715-64582230a219"
		dirty = head + "+dirty"
		older = "v0.0.0-20261030182401-2fe12a545d41"
	)
	cases := map[string]struct {
		ours, theirs string
		another      bool
	}{
		"one release":              {ours: rel, theirs: rel},
		"one commit":               {ours: head, theirs: head},
		"one dirty tree":           {ours: dirty, theirs: dirty},
		"a commit apart":           {ours: head, theirs: older, another: true},
		"dirty and clean":          {ours: dirty, theirs: head, another: true},
		"a release and a checkout": {ours: rel, theirs: head, another: true},
		"viff unstamped (go run)":  {ours: "", theirs: head},
		"runnerd unstamped":        {ours: head, theirs: ""},
		"runnerd a devel build":    {ours: head, theirs: "(devel)"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := compareBuilds("/bin/runnerd", c.ours, c.theirs)
			if (err != nil) != c.another {
				t.Fatalf("compareBuilds(%q, %q) = %v, want another build: %v", c.ours, c.theirs, err, c.another)
			}
			if err != nil && (!strings.Contains(err.Error(), c.ours) || !strings.Contains(err.Error(), c.theirs)) {
				t.Errorf("%v: want both builds named", err)
			}
		})
	}
}

// Only a release is a version `go install` can fetch. A build from a checkout is stamped a
// pseudo-version, +dirty or not, and viff up does not try to install one by it.
func TestReleaseOf(t *testing.T) {
	cases := map[string]string{
		"v0.4.0":                                   "v0.4.0",
		"v0.0.0-20261103141526-28c9a666cff9":       "",
		"v0.0.0-20261103141526-28c9a666cff9+dirty": "",
		"v0.4.1-0.20261103141526-28c9a666cff9":     "",
		"v0.4.0+dirty":                             "",
		"":                                         "",
	}
	for version, want := range cases {
		if got := releaseOf(version); got != want {
			t.Errorf("releaseOf(%q) = %q, want %q", version, got, want)
		}
	}
}

// TestCacheComplete: a version's place in the cache counts once both binaries are in it, so an
// install cut short after runnerd is done again rather than trusted.
func TestCacheComplete(t *testing.T) {
	dir := t.TempDir()
	if cacheComplete(dir) {
		t.Fatal("an empty dir is complete")
	}
	if err := os.WriteFile(filepath.Join(dir, "runnerd"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if cacheComplete(dir) {
		t.Fatal("a dir with runnerd and no viff-call-tool is complete")
	}
	if err := os.WriteFile(filepath.Join(dir, "viff-call-tool"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !cacheComplete(dir) {
		t.Fatal("a dir with both is not complete")
	}
}
