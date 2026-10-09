package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// The allowlist decides by host AND port, and says what it measured against.
//
// Each case here is a decision somebody has to be able to defend later, which is why they are
// written out rather than left to the reader of parseAllow.
func TestEgressAllowlist(t *testing.T) {
	e, err := NewEgress("t", []string{
		"api.anthropic.com",
		"*.githubusercontent.com",
		"registry.internal:5000",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		dest string
		ok   bool
		why  string
	}{
		{"api.anthropic.com:443", true, "the exact host on a web port"},
		{"api.anthropic.com:80", true, "the other web port"},
		{"API.Anthropic.com:443", true, "host matching is case-insensitive"},
		{"api.anthropic.com.:443", true, "a trailing dot is the same name"},
		{"api.anthropic.com:22", false, "a host on the list is not every port on that host"},
		{"evil.com:443", false, "not on the list at all"},
		{"raw.githubusercontent.com:443", true, "a wildcard covers its subdomains"},
		{"a.b.githubusercontent.com:443", true, "and covers them at any depth"},
		{"githubusercontent.com:443", false, "a wildcard does NOT cover the bare domain — " +
			"different name, often different owner"},
		{"notgithubusercontent.com:443", false, "and does not match by string suffix"},
		{"registry.internal:5000", true, "an explicit port is honoured"},
		{"registry.internal:443", false, "and is the ONLY port for that entry"},
	} {
		err := e.permits(tc.dest)
		if tc.ok && err != nil {
			t.Errorf("%s: %s was refused: %v", tc.why, tc.dest, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: %s was allowed", tc.why, tc.dest)
		}
	}
	// A refusal names the destination and the list, because a refusal nobody can read is one
	// somebody will turn off.
	err = e.permits("evil.com:443")
	for _, want := range []string{"evil.com:443", "api.anthropic.com", "*.githubusercontent.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}

// Two allowlists that are not allowlists, refused where they are written rather than where they
// would have surprised somebody.
func TestEgressRefusesANonList(t *testing.T) {
	if _, err := NewEgress("t", nil, nil); err == nil {
		t.Error("an empty allowlist was accepted — it permits nothing, which is `egress: none`")
	}
	if _, err := NewEgress("t", []string{"*"}, nil); err == nil {
		t.Error("`*` was accepted as an allowlist — it is `egress: full` under another name")
	}
	if _, err := NewEgress("t", []string{"*.com"}, nil); err == nil {
		t.Error("`*.com` was accepted")
	}
	// And a bad entry fails when the RUNNER starts, not when the first task tries to reach
	// something — otherwise it reads as the network being down.
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.tony")
	if err := os.WriteFile(bad, []byte("environments:\n- name: e\n  repos:\n  - {name: r, repo: \".\", base: main}\n  egress: [\"*\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bad); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("a bad allowlist entry loaded: %v", err)
	}
}

// The proxy really proxies: an allowed destination is reached, a refused one is refused with
// something the caller can read, and the refusal is counted.
//
// Over a unix socket and through the loopback forwarder the task process runs, because that is
// the whole path — a test against the handler alone would skip the two hops that exist precisely
// because a sandbox with no network cannot dial an address.
func TestEgressProxiesWhatIsAllowedAndRefusesTheRest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "upstream says hello")
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)

	var log bytes.Buffer
	// The allowlist names the upstream by host:port, since httptest binds an ephemeral one.
	e, err := NewEgress("t9", []string{u.Host}, &log)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL := serveEgress(t, e)

	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	cl := &http.Client{Transport: tr}

	// Allowed, over plain HTTP (absolute-form), which is the shape a proxy sees for http://.
	resp, err := cl.Get(upstream.URL)
	if err != nil {
		t.Fatalf("an allowed destination was not reachable through the proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "upstream says hello") {
		t.Errorf("proxied body = %q", body)
	}

	// Refused, and the refusal reaches the caller as a refusal rather than as a hang.
	resp, err = cl.Get("http://not-on-the-list.example:80/")
	if err != nil {
		t.Fatalf("a refused destination produced a transport error rather than a response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("refused destination answered %d, want 403", resp.StatusCode)
	}
	if n := e.Refusals()["not-on-the-list.example:80"]; n != 1 {
		t.Errorf("the refusal was not counted: %v", e.Refusals())
	}
	if !strings.Contains(log.String(), "egress REFUSED not-on-the-list.example:80") {
		t.Errorf("the refusal is not on the runner's log:\n%s", log.String())
	}
}

// CONNECT is the one that matters — everything over TLS arrives as one — so the tunnel is driven
// end to end against a real TLS server rather than asserted about.
func TestEgressTunnelsTLSAndRefusesByName(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "secure hello")
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)

	e, err := NewEgress("t10", []string{u.Host}, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL := serveEgress(t, e)

	cl := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // httptest's own cert
	}}
	resp, err := cl.Get(upstream.URL)
	if err != nil {
		t.Fatalf("CONNECT to an allowed destination failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "secure hello") {
		t.Errorf("tunnelled body = %q", body)
	}

	// And a CONNECT to somewhere else does not open. The client reports the proxy's 403, which is
	// the point of answering rather than dropping: an agent can say what happened.
	_, err = cl.Get("https://not-on-the-list.example/")
	if err == nil {
		t.Fatal("CONNECT to a destination outside the allowlist succeeded")
	}
	// The explanation has to be in the reason phrase, because Go's transport reads a failed
	// CONNECT and returns errors.New(reasonPhrase) — dropping the status code and the body. The
	// first version of this answered with http.Error and the agent saw the word "Forbidden".
	for _, want := range []string{"egress allowlist", "not-on-the-list.example:443"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("a refused tunnel does not tell the caller %q: %v", want, err)
		}
	}
}

// A confiner that cannot unshare the network REFUSES restricted egress rather than running the
// task with a proxy it is free to ignore.
//
// This is the rule the whole containment design rests on, and egress is where it bites hardest:
// the proxy would work under hostConfiner — it is a socket and a loopback port — and it would be
// advice, because nothing stops the task opening its own connection. A floor that can be stepped
// around is not one.
func TestRestrictedEgressIsRefusedWhereItCannotBeEnforced(t *testing.T) {
	env := Environment{Name: "e", Egress: []string{"api.anthropic.com"}}
	p := pointFor(env)
	if p["network.egress"] != "restricted" {
		t.Fatalf("an environment with an allowlist did not ask for restricted egress: %v", p)
	}
	if p["network.ingress"] != "none" {
		t.Errorf("restricted egress arrived without `ingress: none`, which is how it is built: %v", p)
	}
	if err := (hostConfiner{}).Provides(p); err == nil {
		t.Fatal("hostConfiner claimed restricted egress, which it cannot enforce")
	}
	// bwrap can, and says so.
	if err := (bwrapConfiner{bin: "/usr/bin/bwrap"}).Provides(p); err != nil {
		t.Fatalf("bwrap refuses the point it builds with --unshare-net plus a bound socket: %v", err)
	}
	// But not with a way in as well: there is no route in once the network is unshared, so
	// asking for one is asking for a different mechanism.
	both := Point{"network.egress": "restricted", "network.ingress": "full"}
	if err := (bwrapConfiner{bin: "/usr/bin/bwrap"}).Provides(both); err == nil {
		t.Error("bwrap claimed restricted egress alongside full ingress")
	}
}

// The confiner binds the socket in and takes the network away, and the task is told where it
// landed. Three things that have to agree about one name.
func TestBwrapBindsTheEgressSocket(t *testing.T) {
	b := bwrapConfiner{bin: "/usr/bin/bwrap"}
	cmd := exec.Command("coderunner", "-task")
	p := Point{"network.egress": "restricted", "network.ingress": "none"}
	if err := b.Confine(cmd, p, Sandbox{Dir: "/w", Egress: "/ctl/e1.sock"}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "--unshare-net") {
		t.Error("restricted egress did not unshare the network, so the allowlist is optional")
	}
	if !strings.Contains(joined, "--bind /ctl/e1.sock "+egressPath) {
		t.Errorf("the egress socket is not bound into the sandbox:\n%s", joined)
	}
	if got := b.EgressAt("/ctl/e1.sock"); got != egressPath {
		t.Errorf("the task would be told to dial %q, which is not where it was bound", got)
	}
	// And restricted egress with no socket to bind is a refusal rather than a sandbox with no
	// way out that nobody asked for.
	if err := b.Confine(exec.Command("x"), p, Sandbox{Dir: "/w"}); err == nil {
		t.Error("a restricted point with no egress proxy was confined anyway")
	}
}

// serveEgress runs the proxy on a unix socket and puts the task process's own loopback forwarder
// in front of it, returning the proxy URL a task would be given.
func serveEgress(t *testing.T, e *Egress) *url.URL {
	t.Helper()
	dir, err := os.MkdirTemp("", "eg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "e.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: e}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	// The forwarder the task process runs, exercised here rather than assumed: it is what turns
	// a unix socket into something curl, go and npm can use. t.Setenv first, so useEgress's own
	// os.Setenv is undone when the test ends.
	for _, n := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(n, "")
	}
	cc := childConfig{Egress: sock}
	if err := useEgress(&cc); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("HTTPS_PROXY"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// End to end: a launch into an environment whose egress this runner cannot restrict is refused on
// the wire, with the reason.
//
// On a machine without bwrap that is every such launch, which is the honest state of this feature
// here — the proxy and the forwarder above are exercised for real, and the combination that makes
// them a floor is not, because the mechanism that makes it one does not exist on macOS.
func TestALaunchNeedingRestrictedEgressIsRefusedHere(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name: "e", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}},
			Driver: driverLLM, Egress: []string{"api.anthropic.com"},
		}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := runner.HTTP(srv.URL).Spawn(task.Launch{ID: "e1", Prompt: "x"})
	e := h.Err()
	if e == "" {
		t.Fatal("a launch asking for containment this runner cannot build was accepted")
	}
	for _, want := range []string{"refused", "network.egress"} {
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
	c, err := LoadConfig("coderunner.tony")
	if err != nil {
		t.Fatal(err)
	}
	env, err := c.Environment("verse-dev-hardened")
	if err != nil {
		t.Fatalf("the sample's hardened environment: %v", err)
	}
	if p := pointFor(env); p["network.egress"] != "restricted" {
		t.Errorf("it declares an allowlist but asks for egress %q", p["network.egress"])
	}
	if _, err := NewEgress("x", env.Egress, nil); err != nil {
		t.Errorf("its allowlist does not build a proxy: %v", err)
	}
}

// A missing model credential is refused at the spawn, naming the variable.
//
// It used to be discovered at the first model call and reported as the vendor's opinion —
// `Portkey Error: Invalid API Key` — which says nothing about which variable, or that it is read
// in the RUNNER's environment rather than verse's. Found by driving it.
func TestAMissingModelCredentialIsRefusedNotDiscovered(t *testing.T) {
	fastLifecycle(t)
	t.Setenv("DRIVE_KEY", "")
	origin := initRepo(t)
	cfg := &Config{
		Default: "e",
		Environments: []Environment{{
			Name: "e", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}}, Driver: driverLLM,
		}},
		LLM: LLM{APIKeyEnv: "DRIVE_KEY", Model: "m"},
	}
	if why := cfg.MissingLLMCreds(); !strings.Contains(why, "DRIVE_KEY") {
		t.Fatalf("the complaint does not name the variable: %q", why)
	}
	r := testRunner(t, cfg)
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := runner.HTTP(srv.URL).Spawn(task.Launch{ID: "k1", Prompt: "x"})
	e := h.Err()
	for _, want := range []string{"refused", "DRIVE_KEY", "runner's environment"} {
		if !strings.Contains(e, want) {
			t.Errorf("the refusal does not say %q: %s", want, e)
		}
	}
	if r.running() != 0 {
		t.Error("a task that cannot possibly finish was started anyway")
	}
	// With the variable set it is not refused — the check is about the credential, not about
	// the driver.
	t.Setenv("DRIVE_KEY", "sk-whatever")
	if why := cfg.MissingLLMCreds(); why != "" {
		t.Errorf("a set variable still reads as missing: %q", why)
	}
	// And a config that names no variable at all says THAT, rather than naming an empty one.
	none := &Config{Environments: cfg.Environments}
	if why := none.MissingLLMCreds(); !strings.Contains(why, "apiKeyEnv") {
		t.Errorf("a config with no llm block does not say so: %q", why)
	}
}

// A SPAWN'S OWN CREDENTIAL BEATS THIS RUNNER'S, and a spawn without one gets the runner's.
//
// What a spawn carries IS the key — verse read it from the address the spawn named, as whoever
// named it, and refused there if that read was not theirs — so this side has nothing to look up
// and no list to keep in step with the store. The whole permission question was answered before
// the launch arrived.
func TestASpawnsOwnCredentialBeatsTheRunners(t *testing.T) {
	t.Setenv("RUNNER_VK", "vk-runner")
	cfg := &Config{LLM: LLM{APIKeyEnv: "DRIVE_KEY", VirtualKeyEnv: "RUNNER_VK", Model: "m"}}

	if got := cfg.VirtualKeyOr("vk-review"); got != "vk-review" {
		t.Errorf("carried → %q, want the spawn's own", got)
	}
	if got := cfg.VirtualKeyOr(""); got != "vk-runner" {
		t.Errorf("unnamed → %q, want this runner's", got)
	}
	// A runner naming no variable, asked by a spawn carrying nothing: the account key alone,
	// which is what every runner did before credentials had addresses.
	none := &Config{LLM: LLM{APIKeyEnv: "DRIVE_KEY"}}
	if got := none.VirtualKeyOr(""); got != "" {
		t.Errorf("neither → %q, want no virtual key", got)
	}
}
