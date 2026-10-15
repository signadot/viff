package runnerd_test

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
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/runner/runnerd"
)

// The allowlist decides by host AND port, and says what it measured against.
//
// Each case here is a decision somebody has to be able to defend later, which is why they are
// written out rather than left to the reader of parseAllow.
func TestEgressAllowlist(t *testing.T) {
	e, err := runnerd.NewEgress("t", []string{
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
		err := e.Permits(tc.dest)
		if tc.ok && err != nil {
			t.Errorf("%s: %s was refused: %v", tc.why, tc.dest, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: %s was allowed", tc.why, tc.dest)
		}
	}
	// A refusal names the destination and the list, because a refusal nobody can read is one
	// somebody will turn off.
	err = e.Permits("evil.com:443")
	for _, want := range []string{"evil.com:443", "api.anthropic.com", "*.githubusercontent.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}

// Two allowlists that are not allowlists, refused where they are written rather than where they
// would have surprised somebody.
func TestEgressRefusesANonList(t *testing.T) {
	if _, err := runnerd.NewEgress("t", nil, nil); err == nil {
		t.Error("an empty allowlist was accepted — it permits nothing, which is no network")
	}
	if _, err := runnerd.NewEgress("t", []string{"*"}, nil); err == nil {
		t.Error("`*` was accepted as an allowlist — it is an open network under another name")
	}
	if _, err := runnerd.NewEgress("t", []string{"*.com"}, nil); err == nil {
		t.Error("`*.com` was accepted")
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
	e, err := runnerd.NewEgress("t9", []string{u.Host}, &log)
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

	e, err := runnerd.NewEgress("t10", []string{u.Host}, nil)
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
	// CONNECT and returns errors.New(reasonPhrase) — dropping the status code and the body.
	for _, want := range []string{"egress allowlist", "not-on-the-list.example:443"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("a refused tunnel does not tell the caller %q: %v", want, err)
		}
	}
}

// A served egress is a socket the runner owns, stopped with the task that had it.
func TestAServedEgressIsASocketThatStopsWithItsTask(t *testing.T) {
	dir := shortTempDir(t)
	eg, err := runnerd.ServeEgress(dir, "t11", []string{"api.anthropic.com"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(eg.Sock()) != dir {
		t.Errorf("the socket %s is not in the directory given", eg.Sock())
	}
	if _, err := os.Stat(eg.Sock()); err != nil {
		t.Fatalf("the socket is not there: %v", err)
	}
	eg.Stop()
	if _, err := os.Stat(eg.Sock()); !os.IsNotExist(err) {
		t.Error("the socket outlived its egress")
	}
	var none *runnerd.EgressServer
	none.Stop() // a task with no restricted network has no egress, and nothing to stop
	if none.Sock() != "" {
		t.Error("no egress has a socket")
	}
}

// serveEgress runs the proxy on a unix socket and puts the task process's own loopback forwarder
// in front of it, returning the proxy URL a task would be given.
func serveEgress(t *testing.T, e *runnerd.Egress) *url.URL {
	t.Helper()
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "e.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: e}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	// The forwarder the task process runs, exercised here rather than assumed: it is what turns
	// a unix socket into something curl, go and npm can use. t.Setenv first, so UseEgress's own
	// os.Setenv is undone when the test ends.
	for _, n := range runnerd.ProxyEnv {
		t.Setenv(n, "")
	}
	names, err := runnerd.UseEgress(sock)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != len(runnerd.ProxyEnv) {
		t.Fatalf("UseEgress named %v, want every proxy variable", names)
	}
	u, err := url.Parse(os.Getenv("HTTPS_PROXY"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}
