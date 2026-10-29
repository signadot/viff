package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/signadot/verse/config"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/stock"
	"github.com/signadot/verse/task"
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

// THE CONFIG `verse up` WRITES is one runnerd loads as it loads any, strictly: where it listens,
// the verse it serves, and what it offers. The address it listens on is read back by the next run,
// and moved where the port is taken.
func TestTheLocalRunnersConfigIsOneRunnerdLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), localRunnerConfig)
	c, _ := embeddedRunner(config.LLMConfig{APIKey: "k", Model: "m"}, task.DefaultShellEnv)
	verse := runnerd.Verse{URL: "http://localhost:9797", Issuer: "https://verse.example.com"}
	if err := writeLocalRunnerConfig(path, "127.0.0.1:9803", verse, c); err != nil {
		t.Fatal(err)
	}
	rc, sc, err := stock.Load(path)
	if err != nil {
		b, _ := os.ReadFile(path)
		t.Fatalf("runnerd refuses the config verse up writes: %v\n%s", err, b)
	}
	if rc.Listen != "127.0.0.1:9803" || rc.Verse == nil || rc.Verse.URL != verse.URL || rc.Verse.Issuer != verse.Issuer {
		t.Fatalf("the door = %+v %+v", rc, rc.Verse)
	}
	if _, ok := sc.Agents[stock.AgentName]; !ok || sc.Script == nil {
		t.Fatalf("what it offers = %+v, want the verse agent and scripts", sc)
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

// A VERSE'S DATA DIR IS ITS ADDRESS UNDER ~/.verse, or under $VERSE_HOME where that is set, so a
// bare `verse up` comes back to the same verse.
func TestTheDataDirIsTheAddressUnderTheVerseHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VERSE_HOME", home)
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
