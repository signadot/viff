package runnerd_test

import (
	"bufio"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/runner/runnerd"
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
	}, nil, nil)
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
	if _, err := runnerd.NewEgress("t", nil, nil, nil); err == nil {
		t.Error("an empty allowlist was accepted — it permits nothing, which is no network")
	}
	if _, err := runnerd.NewEgress("t", []string{"*"}, nil, nil); err == nil {
		t.Error("`*` was accepted as an allowlist — it is an open network under another name")
	}
	if _, err := runnerd.NewEgress("t", []string{"*.com"}, nil, nil); err == nil {
		t.Error("`*.com` was accepted")
	}
	for _, unread := range []string{"2001:DB8:0:0::1", "[2001:db8:0::1]:8443", "::ffff:10.0.0.1"} {
		if _, err := runnerd.NewEgress("t", []string{unread}, nil, nil); err == nil {
			t.Errorf("%s was accepted, though the proxy never reads a destination so", unread)
		}
	}
	if _, err := runnerd.NewEgress("t", []string{"2001:db8::1", "[2001:db8::1]:8443", "10.0.0.1:8080"}, nil, nil); err != nil {
		t.Errorf("an address as Go writes it was refused: %v", err)
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
	e, err := runnerd.NewEgress("t9", []string{u.Host}, nil, &log)
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

	e, err := runnerd.NewEgress("t10", []string{u.Host}, nil, nil)
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
	for _, want := range []string{"none of the hosts this task's proxy reaches", "not-on-the-list.example:443"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("a refused tunnel does not tell the caller %q: %v", want, err)
		}
	}
}

// A served egress is a socket the runner owns, stopped with the task that had it.
func TestAServedEgressIsASocketThatStopsWithItsTask(t *testing.T) {
	dir := shortTempDir(t)
	eg, err := runnerd.ServeEgress(dir, "t11", []string{"api.anthropic.com"}, nil, io.Discard)
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
	none.Stop() // a task with no allow network has no egress, and nothing to stop
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
	// a unix socket into something curl, go and npm can use. On a port of the test's own: the
	// fixed one is for a network namespace nothing else is in.
	addr, err := runnerd.UseEgress(sock, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return &url.URL{Scheme: "http", Host: addr.String()}
}

// An absolute-form request is checked against the port its scheme defaults to: https:// with no
// port is :443, not :80.
func TestEgressDefaultsAnAbsoluteRequestsPortByScheme(t *testing.T) {
	e, err := runnerd.NewEgress("t12", []string{"allowed.example:80"}, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL := serveEgress(t, e)
	conn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET https://allowed.example/x HTTP/1.1\r\nHost: allowed.example\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || e.Refusals()["allowed.example:443"] != 1 {
		t.Errorf("https://allowed.example answered %d, refusals %v: want it refused as allowed.example:443",
			resp.StatusCode, e.Refusals())
	}
}

// A client that shuts its write side and waits for the answer gets it, through the task's
// loopback bridge and the tunnel both: the half-close reaches the far side as one.
func TestEgressPassesAHalfCloseThrough(t *testing.T) {
	far, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer far.Close()
	go func() {
		c, err := far.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		n, _ := io.Copy(io.Discard, c) // to the client's EOF
		fmt.Fprintf(c, "read %d bytes", n)
	}()
	e, err := runnerd.NewEgress("t13", []string{far.Addr().String()}, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL := serveEgress(t, e)
	conn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %[1]s\r\n\r\n", far.Addr())
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT: %v %v", resp, err)
	}
	fmt.Fprint(conn, "hello")
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("no answer after the half-close: %v (read %q)", err, got)
	}
	if string(got) != "read 5 bytes" {
		t.Errorf("the far side answered %q, want %q", got, "read 5 bytes")
	}
}

// And the other way: a far side that finishes sending first still reads what the client sends
// after, through the bridge and the tunnel both.
func TestEgressKeepsTheClientsSideOpenAfterTheFarSideFinishes(t *testing.T) {
	far, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer far.Close()
	late := make(chan string, 1)
	go func() {
		c, err := far.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		fmt.Fprint(c, "done sending")
		_ = c.(*net.TCPConn).CloseWrite()
		got, _ := io.ReadAll(c)
		late <- string(got)
	}()
	e, err := runnerd.NewEgress("t14", []string{far.Addr().String()}, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL := serveEgress(t, e)
	conn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %[1]s\r\n\r\n", far.Addr())
	br := bufio.NewReader(conn)
	if resp, err := http.ReadResponse(br, nil); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT: %v %v", resp, err)
	}
	if got, err := io.ReadAll(br); err != nil || string(got) != "done sending" {
		t.Fatalf("read %q, %v", got, err)
	}
	fmt.Fprint(conn, "still talking")
	_ = conn.(*net.TCPConn).CloseWrite()
	select {
	case got := <-late:
		if got != "still talking" {
			t.Errorf("the far side read %q after it finished, want %q", got, "still talking")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the far side never read the client's end")
	}
}

// A DESTINATION THAT IS NONE OF THE RUNNER'S OWN HOSTS IS DECIDED: what decide refuses is refused,
// over plain HTTP and in a tunnel alike, naming why; what it admits goes through; and it is asked
// with the host as the shapes spell it, lower case and without a trailing dot.
func TestEgressDecidesWhatIsNotItsOwn(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "reached")
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	var refuse atomic.Bool
	refuse.Store(true)
	var asked atomic.Value
	e, err := runnerd.NewEgress("t15", nil, func(host, port string) error {
		asked.Store(host)
		if refuse.Load() {
			return fmt.Errorf("egress: %s:%s is refused by the autonomy's row not-yet", host, port)
		}
		return nil
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Permits("Example.COM.:443"); err == nil || asked.Load() != "example.com" {
		t.Fatalf("decide asked about %v (%v), want example.com", asked.Load(), err)
	}
	cl := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(serveEgress(t, e))}}
	resp, err := cl.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "not-yet") {
		t.Fatalf("a refused host = %d %q, want 403 naming the row", resp.StatusCode, body)
	}
	if e.Refusals()[u.Host] == 0 {
		t.Errorf("the refusal was not counted: %v", e.Refusals())
	}
	refuse.Store(false)
	resp, err = cl.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "reached" {
		t.Fatalf("an admitted host = %d %q", resp.StatusCode, body)
	}
}
