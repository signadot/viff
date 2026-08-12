package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The egress proxy: what makes `network.egress: restricted` a thing a runner can build rather
// than a word in a vocabulary.
//
// It matters more than it looks, and the reason is the secret story. Once it was settled that
// what the TASK needs — the model key, a registry token — goes into the sandbox, the question
// stopped being whether the value is present and became **what the sandbox can reach**. A secret
// inside a box that can only talk to the model API is bounded; the same secret inside a box that
// can talk to anything is not. So one mechanism bounds every secret, rather than one proxy per
// secret, and this is that mechanism.
//
// It is also the rung bwrap cannot build by itself: `--unshare-net` takes ingress and egress
// together and offers nothing in between. The shape that works is a namespace with NO network
// plus one channel out of it that this side controls — so the sandbox's only route to anything is
// through an allowlist, by construction rather than by configuration the task could change.
//
// What it is NOT is a security boundary against a task that can reach the network another way.
// Under a confiner that does not unshare the network it is advice: the proxy is there, the task
// is told to use it, and nothing stops it opening its own socket. That is why hostConfiner
// refuses `restricted` rather than claiming it — the same refuse-rather-than-degrade rule the
// rest of the containment follows, and the reason this cannot be exercised end to end on a
// machine without bwrap.

// egressPath is where the proxy's socket appears INSIDE the sandbox. A fixed path because the
// confiner binds it and the task process dials it, and those are two different programs agreeing
// about one name.
const egressPath = "/run/verse-egress"

// Egress is an allowlisting HTTP proxy for one task.
//
// Per task rather than per runner, because the allowlist is the environment's and the refusals
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
// that permits nothing: an environment that declared `restricted` and listed nothing has said
// something contradictory, and a task that cannot reach anything is a task that should have asked
// for `none`.
func NewEgress(id string, allow []string, log io.Writer) (*Egress, error) {
	if len(allow) == 0 {
		return nil, fmt.Errorf("an egress allowlist with no entries permits nothing — " +
			"an environment that means that wants `network.egress: none`")
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

func parseAllow(entry string) (rule, error) {
	e := strings.ToLower(strings.TrimSpace(entry))
	if e == "" {
		return rule{}, fmt.Errorf("egress: an empty allowlist entry")
	}
	if e == "*" {
		// Refused by name rather than accepted as a convenience. An allowlist of everything is
		// `network.egress: full`, which is a different claim, comparable differently, and
		// visible as itself in a descriptor.
		return rule{}, fmt.Errorf("egress: `*` is not an allowlist — an environment that means " +
			"it wants `network.egress: full`, which says so where anything can read it")
	}
	r := rule{host: e}
	if h, p, err := net.SplitHostPort(e); err == nil {
		r.host, r.port = h, p
	}
	if after, ok := strings.CutPrefix(r.host, "*."); ok {
		r.wildcard, r.host = true, after
		if r.host == "" || !strings.Contains(r.host, ".") {
			return rule{}, fmt.Errorf("egress: %q matches too much — a wildcard needs a domain "+
				"under it", entry)
		}
	}
	return r, nil
}

// permits reports whether a destination is on the allowlist, and says what it was measured
// against when it is not — a refusal nobody can read is a refusal somebody will disable.
func (e *Egress) permits(hostport string) error {
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
	return fmt.Errorf("egress: %s is not on this environment's allowlist (%s)",
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
		dest = net.JoinHostPort(dest, "80")
	}
	if perr := e.permits(dest); perr != nil {
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
	if perr := e.permits(dest); perr != nil {
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
	go func() {
		defer up.Close()
		defer down.Close()
		_, _ = io.Copy(up, buf) // whatever the client already sent, then the rest
	}()
	go func() {
		defer up.Close()
		defer down.Close()
		_, _ = io.Copy(down, up)
	}()
}

// refuseTunnel says no to a CONNECT in the only channel a client will actually read.
//
// A refused CONNECT is answered rather than dropped, so an agent can say what happened instead of
// reporting a network problem — and so can everybody reading after it. But the obvious way to
// answer, `http.Error(w, why, 403)`, loses the message: Go's own transport reads a failed CONNECT
// response and returns `errors.New(reasonPhrase)`, discarding the status code AND the body. What
// the agent saw was the single word "Forbidden".
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
	fmt.Fprintf(e.log, "coderunner: %s: egress REFUSED %s — %v\n", e.id, dest, err)
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
