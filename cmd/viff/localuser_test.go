package main

import (
	"strings"
	"testing"

	"go.viff.dev/viff/internal/config"
)

// loggedIn sets who the operating system says is logged in, for one test.
func loggedIn(t *testing.T, name string) {
	t.Helper()
	old := login
	login = func() string { return name }
	t.Cleanup(func() { login = old })
}

// at points the CLI at addr for one test, and puts back where it pointed.
func at(t *testing.T, addr string) {
	t.Helper()
	old := g.Addr
	g.Addr = addr
	t.Cleanup(func() { g.Addr = old })
}

func TestLoopbackIsThisMachine(t *testing.T) {
	for addr, want := range map[string]bool{
		"localhost:9797":         true,
		"localhost":              true,
		"127.0.0.1:9797":         true,
		"127.8.0.1:1":            true,
		"[::1]:9797":             true,
		"::1":                    true,
		":9797":                  true,
		"viff:9797":              false,
		"viff.example.com:9797":  false,
		"10.0.0.7:9797":          false,
		"localhost.example:9797": false,
		"[2001:db8::1]:9797":     false,
	} {
		if got := loopback(addr); got != want {
			t.Errorf("loopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// Nobody configured and a viff instance on this machine: the CLI acts as the login, at localhost. The
// same CLI toward a viff instance anywhere else names nobody, which the server refuses for a write.
func TestNobodyConfiguredIsTheLocalUserOnThisMachineOnly(t *testing.T) {
	loggedIn(t, "ana")
	appCfg = &config.Config{}
	asOverride = ""

	at(t, "localhost:9797")
	if p, err := principal(""); err != nil || p != "user:ana@localhost" {
		t.Fatalf("on this machine: principal = (%q, %v), want user:ana@localhost", p, err)
	}
	at(t, "viff.example.com:9797")
	if p, err := principal(""); err != nil || p != "" {
		t.Fatalf("on a shared viff: principal = (%q, %v), want nobody", p, err)
	}
	if _, err := requireAs("", "identity", "a decision names who made it"); err == nil {
		t.Fatal("on a shared viff an answer with no --as was not refused")
	}
}

// The local user is what is left when nothing says who: --as, `agent:` and `user:` each win.
func TestTheLocalUserYieldsToWhoeverIsNamed(t *testing.T) {
	loggedIn(t, "ana")
	at(t, "localhost:9797")
	t.Cleanup(func() { appCfg, asOverride = &config.Config{}, "" })

	appCfg, asOverride = &config.Config{User: "bob@example.com"}, ""
	if p, _ := principal(""); p != "user:bob@example.com" {
		t.Errorf("config user: principal = %q", p)
	}
	appCfg, asOverride = &config.Config{User: "bob@example.com", Agent: "ops"}, ""
	if p, _ := principal(""); p != "agent:ops" {
		t.Errorf("config agent: principal = %q", p)
	}
	appCfg, asOverride = &config.Config{}, ""
	if p, _ := principal("carol@example.com"); p != "user:carol@example.com" {
		t.Errorf("--as: principal = %q", p)
	}
}

// A login the grammar refuses is not guessed at, and one that cannot be read is nobody's: both
// are `local`.
func TestALoginThatIsNoNameIsLocal(t *testing.T) {
	for _, name := range []string{"", "ana maria", "*", `CORP\ana maria`} {
		loggedIn(t, name)
		if got := localUser("localhost:9797"); got != "user:local@localhost" {
			t.Errorf("login %q: local user = %q, want user:local@localhost", name, got)
		}
	}
}

// The quick start's first write, as a new developer makes it: nothing configured, a viff instance just
// started on this machine. It lands, and it is theirs.
func TestAWriteWithNobodyConfiguredLandsAsTheLocalUser(t *testing.T) {
	t.Setenv("VIFF_USER", "")
	loggedIn(t, "ana")
	store := serve(t) // an httptest server, which listens on 127.0.0.1

	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", `{stage: open}`); err != nil {
		t.Fatalf("a write with nobody configured: %v", err)
	}
	if got := payloadOf(t, store, pr1); got["stage"] != "open" {
		t.Fatalf("the write did not land: %+v", got)
	}
	// The place the write brought into being is whose the write was.
	out, err := runCLI(t, "perms", "demo")
	if err != nil {
		t.Fatalf("perms demo: %v", err)
	}
	if !strings.Contains(out, "owner: user:ana@localhost") {
		t.Fatalf("the place is not the local user's:\n%s", out)
	}
}
