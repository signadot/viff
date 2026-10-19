package runnerd

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/signadot/verse/runner"
)

// The egress proxy: what makes an allow network a thing a runner can build rather than a
// word in a vocabulary.
//
// It matters more than it looks, and the reason is the secret story. Once it is settled that what
// the TASK needs — the model key, a registry token — goes into the sandbox, the question stops
// being whether the value is present and becomes **what the sandbox can reach**. A secret inside
// a box that can only talk to the model API is bounded; the same secret inside a box that can
// talk to anything is not. So one mechanism bounds every secret, rather than one proxy per
// secret, and this is that mechanism.
//
// It is also the rung bwrap cannot build by itself: `--unshare-net` takes everything away and
// offers nothing in between. The shape that works is a namespace with NO network plus one channel
// out of it that the runner controls — so the sandbox's only route to anything is through an
// allowlist, by construction rather than by configuration the task could change.
//
// What it is NOT is a security boundary against a task that can reach the network another way.
// Under a sandbox that does not unshare the network it is advice: the proxy is there, the task is
// told to use it, and nothing stops it opening its own socket. That is why Unconfined refuses
// NetworkAllow rather than claiming it.

// Egress is an allowlisting HTTP proxy for one task.
//
// Per task rather than per runner, because the allowlist is the launch's and the refusals
// are worth attributing. A refusal is the interesting event here: it is the only place a runner
// finds out that a task tried to reach something nobody said it could, which is a signal about
// the work — a dependency nobody declared, or an agent doing something surprising — as much as
// about security.
type Egress struct {
	id    string
	rules []rule
	log   io.Writer

	dial net.Dialer

	mu      sync.Mutex
	refused map[string]int // destination → times, so a log does not repeat forever
}

// rule is one allowlist entry.
type rule struct {
	host     string // lowercased, no port
	wildcard bool   // the entry was `*.example.com`: match any subdomain, not the bare domain
	port     string // "" = the web ports only, see permits
}

// NewEgress builds a proxy over an allowlist. An empty allowlist is an error rather than a proxy
// that permits nothing: a launch that asked for an allow network and listed nothing has
// said something contradictory, and a task that cannot reach anything is a task that should have
// asked for none.
func NewEgress(id string, allow []string, log io.Writer) (*Egress, error) {
	if len(allow) == 0 {
		return nil, fmt.Errorf("an egress allowlist with no entries permits nothing — " +
			"a launch that means that wants network none")
	}
	rules := make([]rule, 0, len(allow))
	for _, entry := range allow {
		r, err := parseAllow(entry)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return &Egress{
		id: id, rules: rules, log: log,
		dial:    net.Dialer{Timeout: 20 * time.Second},
		refused: map[string]int{},
	}, nil
}

// parseAllow reads an entry runner.CheckAllow takes. `*` is refused there by name rather than
// accepted as a convenience: an allowlist of everything is an open network, which is a
// different claim, and visible as itself where anything can read it.
func parseAllow(entry string) (rule, error) {
	if err := runner.CheckAllow(entry); err != nil {
		return rule{}, fmt.Errorf("egress: %w", err)
	}
	e := strings.ToLower(strings.TrimSpace(entry))
	r := rule{host: e}
	if h, p, err := net.SplitHostPort(e); err == nil {
		r.host, r.port = h, p
	}
	if after, ok := strings.CutPrefix(r.host, "*."); ok {
		r.wildcard, r.host = true, after
	}
	return r, nil
}

// Permits reports whether a destination (host:port) is on the allowlist, and says what it was
// measured against when it is not — a refusal nobody can read is a refusal somebody will disable.
func (e *Egress) Permits(hostport string) error {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return fmt.Errorf("egress: %q is not a host and port", hostport)
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, r := range e.rules {
		if !r.matchHost(host) {
			continue
		}
		if r.port == "" {
			// An entry that named no port means the web ports and not "anything on this host".
			// The narrower reading is the right default: an allowlist is written to name a
			// service, and a host with a database on it is the case that would silently widen.
			if port == "80" || port == "443" {
				return nil
			}
			continue
		}
		if r.port == port {
			return nil
		}
	}
	return fmt.Errorf("egress: %s is not on this task's allowlist (%s)",
		hostport, strings.Join(e.allowed(), ", "))
}

func (r rule) matchHost(host string) bool {
	if r.wildcard {
		// `*.example.com` matches sub.example.com and not example.com. Saying which is the whole
		// value of a wildcard: the two are different names with different owners often enough.
		return strings.HasSuffix(host, "."+r.host)
	}
	return host == r.host
}

func (e *Egress) allowed() []string {
	out := make([]string, 0, len(e.rules))
	for _, r := range e.rules {
		h := r.host
		if r.wildcard {
			h = "*." + h
		}
		if r.port != "" {
			h = net.JoinHostPort(h, r.port)
		}
		out = append(out, h)
	}
	return out
}

// ServeHTTP is the proxy. CONNECT is the one that matters — everything the task does over TLS
// arrives as one — and absolute-form requests are handled so that plain HTTP is refused by the
// same list rather than by not working.
func (e *Egress) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		e.connect(w, r)
		return
	}
	if !r.URL.IsAbs() {
		// Not a proxy request at all. Worth its own message: it is what a task gets for pointing
		// something at this socket directly instead of through the proxy variables.
		http.Error(w, "egress: this is a proxy, not an origin server — use it as one",
			http.StatusBadRequest)
		return
	}
	dest := r.URL.Host
	if r.URL.Port() == "" {
		port := "80"
		if strings.EqualFold(r.URL.Scheme, "https") {
			port = "443"
		}
		dest = net.JoinHostPort(r.URL.Hostname(), port)
	}
	if perr := e.Permits(dest); perr != nil {
		e.note(dest, perr)
		http.Error(w, perr.Error(), http.StatusForbidden)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	resp, rerr := http.DefaultTransport.RoundTrip(out)
	if rerr != nil {
		http.Error(w, fmt.Sprintf("egress: %v", rerr), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// connect is the tunnel: check the destination, dial it, and splice. Nothing inside is inspected,
// which is the honest limit of a destination allowlist — it says where, never what.
func (e *Egress) connect(w http.ResponseWriter, r *http.Request) {
	dest := r.Host
	if _, _, err := net.SplitHostPort(dest); err != nil {
		dest = net.JoinHostPort(dest, "443")
	}
	if perr := e.Permits(dest); perr != nil {
		e.note(dest, perr)
		e.refuseTunnel(w, dest)
		return
	}
	up, derr := e.dial.DialContext(r.Context(), "tcp", dest)
	if derr != nil {
		http.Error(w, fmt.Sprintf("egress: %v", derr), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "egress: cannot tunnel here", http.StatusInternalServerError)
		return
	}
	down, buf, herr := hj.Hijack()
	if herr != nil {
		up.Close()
		return
	}
	if _, werr := down.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); werr != nil {
		up.Close()
		down.Close()
		return
	}
	go splice(down, buf, up) // buf: whatever the client already sent, then the rest
}

// refuseTunnel says no to a CONNECT in the only channel a client will actually read.
//
// A refused CONNECT is answered rather than dropped, so an agent can say what happened instead of
// reporting a network problem — and so can everybody reading after it. But the obvious way to
// answer, `http.Error(w, why, 403)`, loses the message: Go's own transport reads a failed CONNECT
// response and returns `errors.New(reasonPhrase)`, discarding the status code AND the body. What
// the agent sees is then the single word "Forbidden".
//
// So the explanation goes in the REASON PHRASE, which is the one part that survives. ASCII and one
// line, because that is what a status line is; the long version is in the body for a client that
// does read it, and on the runner's log either way.
func (e *Egress) refuseTunnel(w http.ResponseWriter, dest string) {
	why := fmt.Sprintf("Forbidden: %s is not on this task's egress allowlist (%s)",
		dest, strings.Join(e.allowed(), " "))
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, why, http.StatusForbidden)
		return
	}
	down, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer down.Close()
	body := why + "\n"
	fmt.Fprintf(down, "HTTP/1.1 403 %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n"+
		"Connection: close\r\n\r\n%s", sanitizeReason(why), len(body), body)
}

// sanitizeReason keeps a status line a status line: one line, no control characters. The
// destination comes from the task, so this is not hygiene, it is the boundary — a host with a
// CRLF in it would otherwise write headers of its own choosing into this response.
func sanitizeReason(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			r = ' '
		}
		out = append(out, r)
	}
	return strings.TrimSpace(string(out))
}

// note records a refusal, once per destination, on the runner's log. Once, because a build tool
// that retries turns one refused host into a hundred lines and the hundredth says nothing the
// first did not.
func (e *Egress) note(dest string, err error) {
	e.mu.Lock()
	n := e.refused[dest]
	e.refused[dest]++
	e.mu.Unlock()
	if n > 0 || e.log == nil {
		return
	}
	fmt.Fprintf(e.log, "runnerd: %s: egress REFUSED %s — %v\n", e.id, dest, err)
}

// Refusals reports what was turned away and how often, for a caller that wants to say so
// somewhere better than a log.
func (e *Egress) Refusals() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]int, len(e.refused))
	for k, v := range e.refused {
		out[k] = v
	}
	return out
}

// EgressServer is one task's way out: its Egress proxy, served on a unix socket the runner owns
// and the sandbox binds in.
type EgressServer struct {
	Egress *Egress
	sock   string
	srv    *http.Server
}

// ServeEgress serves a proxy over allow for one task, on a socket in dir: a directory no sandbox
// binds, so the path is the runner's; the sandbox binds it in at EgressPath.
func ServeEgress(dir, id string, allow []string, log io.Writer) (*EgressServer, error) {
	eg, err := NewEgress(id, allow, log)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "e*.sock")
	if err != nil {
		return nil, fmt.Errorf("egress socket: %w", err)
	}
	sock := f.Name()
	f.Close()
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("egress socket: %w", err)
	}
	// 0600 on the socket is not the boundary — the boundary is that the only thing that can
	// reach this path is whatever the sandbox bound it into. It is here because a runner that
	// shares a host with anything else should not widen by default.
	_ = os.Chmod(sock, 0o600)
	srv := &http.Server{Handler: eg}
	go func() { _ = srv.Serve(ln) }()
	return &EgressServer{Egress: eg, sock: sock, srv: srv}, nil
}

// Sock is the socket's path, on the runner's side.
func (e *EgressServer) Sock() string {
	if e == nil {
		return ""
	}
	return e.sock
}

// Stop stops serving and removes the socket. Nil-safe: a task with no allow network has none.
func (e *EgressServer) Stop() {
	if e == nil {
		return
	}
	_ = e.srv.Close()
	_ = os.Remove(e.sock)
}

// ProxyEnv is the environment variables a proxy is announced in, both spellings of each.
var ProxyEnv = []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}

// splice joins a client to its far side until both directions have ended, then closes both: what
// the client sends (read from in) goes to far, and what far sends to the client. Each direction's
// end is passed on as a half-close, so a client that shuts its write side still reads the answer,
// and a far side that finishes first still reads what the client sends.
func splice(client net.Conn, in io.Reader, far net.Conn) {
	var both sync.WaitGroup
	both.Add(2)
	go func() {
		defer both.Done()
		_, _ = io.Copy(far, in)
		closeWrite(far)
	}()
	go func() {
		defer both.Done()
		_, _ = io.Copy(client, far)
		closeWrite(client)
	}()
	both.Wait()
	client.Close()
	far.Close()
}

// closeWrite half-closes c, where it can be.
func closeWrite(c net.Conn) {
	if hc, ok := c.(interface{ CloseWrite() error }); ok {
		_ = hc.CloseWrite()
	}
}

// UseEgress, in a task's process, makes the runner's proxy at sock usable as a proxy: it sets the
// proxy variables on this process, and returns their names for the caller to pass on to the
// task's subprocesses.
//
// The proxy is on the other side of a unix socket, and nothing speaks "HTTP proxy over a unix
// socket" — not Go's transport, not curl, not go, not npm. So this process listens on its OWN
// loopback and pipes each connection down the socket, which turns the socket into an ordinary
// `http://127.0.0.1:<port>` proxy that everything already knows how to use.
//
// Loopback is safe to expose here for the reason the socket is: with the network unshared this is
// a network namespace of one process tree, so 127.0.0.1 is reachable by the task and by nothing
// else on the machine. Without the unshare it would be the host's loopback and this would be a
// proxy anything could use — which is one more reason a sandbox that cannot unshare refuses a
// network of allow rather than approximating it.
func UseEgress(sock string) (names []string, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("egress: no loopback to put the proxy on (%w) — with the network "+
			"unshared this is the one interface there should be", err)
	}
	go func() {
		for {
			down, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				up, err := net.Dial("unix", sock)
				if err != nil {
					down.Close()
					return
				}
				splice(down, down, up)
			}()
		}
	}()
	proxy := "http://" + ln.Addr().String()
	for _, n := range ProxyEnv {
		// Set on this process before anything makes a request: net/http reads the proxy
		// environment ONCE, on the first use, and caches it for the life of the program.
		if err := os.Setenv(n, proxy); err != nil {
			return nil, err
		}
	}
	return append([]string(nil), ProxyEnv...), nil
}
