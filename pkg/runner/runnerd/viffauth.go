package runnerd

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/runner"
)

// The runner's door (git-issue pb6fwh8gh12krmb7vhn0).
//
// A RUNNER BELONGS TO ONE VIFF. Its configuration names the viff it serves by the tokens viffd
// signs: viffd is an issuer of its own (auth.issuer on the viff side), and presents on every
// call a token it minted for this runner, `iss` viffd's issuer name, `aud` this runner's own
// subject and `sub` viff (runner.ViffSubject). The runner reads viffd's keys at the url it
// reaches viffd at, and holds every bearer to that issuer, that audience, that subject and a live
// signature; a request that does not verify is refused before its body is read, so a launch, with
// its prompt and its repositories, leaves viff for a runner nobody else can drive. Without the block the runner answers whoever reaches its port,
// as every runner did.
//
// THE AUDIENCE IS THE RUNNER'S OWN NAME. A token minted for one runner is good at no other: a
// copy taken at a runner, or sent to a url somebody typed wrong, opens no sibling's door. The
// audience defaults to the subject of the runner's own token, which is what its registration
// names, so a registered runner types nothing.
//
// THE DESCRIPTION IS PUBLIC; THE TOKEN IS NOT. GET /describe says what the runner offers, which
// is no secret, so a bare one is answered to anyone, as an operator at the port or a viff with
// no token asks it. A bearer presented with it is still checked, and refused when it does not verify: a
// viff that believes it proves itself and does not finds out at the describe, not at the first
// spawn.
//
// A TOKEN TOWARD A RUNNER OPENS NO DOOR OF VIFF'S. Viffd's tokens toward runners are viffd's
// own issue, where a runner's token toward viffd is the cluster's with the audience `viff`: a
// runner holding what viffd presented cannot turn round and present it at viffd's door.
//
// THE DESCRIBE IS THE HANDSHAKE. To a caller it verified, and to nobody else, the runner answers
// GET /describe with its own workload token (runner.TokenHeader), from the file VIFF_TOKEN_FILE
// names; viffd checks the subject against the runner's registration. Both sides check in the
// same exchange: a spoofed runner cannot receive viff's launches, and viff's runner cannot be
// driven by anyone else.

// keySetPath is where an issuer publishes the keys that verify its tokens, in OIDC's well-known
// place: viffd serves it under its own url (internal/viffd/issuer.go).
const keySetPath = "/.well-known/jwks.json"

// Viff is the viff a runner serves, as its configuration names it (`viff:`): where the runner
// reaches it, and how it holds viffd's tokens. A runner with none serves whoever reaches it.
type Viff struct {
	// URL is where the runner reaches its viff: the hello (Announce) and every verb a task
	// performs on viff through the runner go there, with the runner's own token, and viffd's
	// keys are read there (keySetPath).
	URL string `tony:"field=url"`
	// Issuer is the `iss` of viffd's tokens, `auth.issuer.url` on the viff side: what viffd is
	// called where its relying parties are, which a viff behind a proxy may spell differently
	// from URL. Unset is URL.
	Issuer string `tony:"field=issuer,omitzero"`
	// Audience is the `aud` this runner takes viffd's tokens for. Unset is the subject of the
	// runner's own token (NewGuard), which is what its registration names.
	Audience string `tony:"field=audience,omitzero"`
	// CA is the path of the PEM bundle viffd's certificate is checked against, for a viff
	// served over TLS under a certificate no public root signed. Unset trusts the system's roots.
	CA string `tony:"field=ca,omitzero"`
}

// Check fills in the defaults it can and refuses a block that names no viff.
func (v *Viff) Check() error {
	if v.URL == "" {
		return fmt.Errorf("viff: `url` names the viff this runner serves, where it reaches it; it is required")
	}
	if u, err := url.Parse(v.URL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("viff.url: %q is not the viff's URL", v.URL)
	}
	if v.Issuer == "" {
		v.Issuer = v.URL
	} else if u, err := url.Parse(v.Issuer); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("viff.issuer: %q is not an issuer's URL", v.Issuer)
	}
	// Viffd spells its `iss` without a trailing slash whatever its config says, and a token is
	// held to the name as compared: the two spellings are one issuer here too.
	v.Issuer = strings.TrimRight(v.Issuer, "/")
	if v.Audience != "" {
		if err := runner.CheckSubject(v.Audience); err != nil {
			return fmt.Errorf("viff.audience: %w", err)
		}
	}
	return nil
}

// Guard is a runner's door: it admits the requests of the one viff the runner serves, and
// answers that viff the runner's own token on the describe.
type Guard struct {
	v         Viff
	tokenFile string       // the runner's own workload token, answered on the describe; "" answers none
	client    *http.Client // reaches the issuer where the system's roots do not; nil is the default

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
}

// guardClockSkew is how far past its expiry a token is still taken, for a clock that runs behind
// the viff's.
const guardClockSkew = 30 * time.Second

// NewGuard is the door for v. tokenFile names the runner's own workload token, which the door
// answers on the describe to the viff it verified, and whose subject is the audience the door
// takes where the block names none; "" answers none, and the block then names the audience. The
// file is read at each use, since the runtime rotates the token under the process, and once
// here, so a path that holds nothing is refused where it was named rather than guessed at from
// the viff side.
func NewGuard(v Viff, tokenFile string) (*Guard, error) {
	if err := v.Check(); err != nil {
		return nil, err
	}
	g := &Guard{v: v, tokenFile: tokenFile}
	if tokenFile != "" {
		tok, err := g.ownToken()
		if err != nil {
			return nil, err
		}
		if g.v.Audience == "" {
			sub, err := runner.SubjectOf(tok)
			if err != nil {
				return nil, fmt.Errorf("viff.audience is unset and this runner's own token does not say whose it is: %w", err)
			}
			g.v.Audience = sub
		}
	}
	if g.v.Audience == "" {
		return nil, fmt.Errorf("viff.audience: a runner with no token of its own names the audience it takes its viff's tokens for")
	}
	if v.CA != "" {
		pem, err := os.ReadFile(v.CA)
		if err != nil {
			return nil, fmt.Errorf("viff.ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("viff.ca: %s holds no certificate", v.CA)
		}
		g.client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	}
	return g, nil
}

// Viff is the block the door was made from, with its defaults filled in: the issuer the tokens
// are held to and the audience they are taken for.
func (g *Guard) Viff() Viff { return g.v }

// ownToken is the runner's own workload token, as the file holds it now.
func (g *Guard) ownToken() (string, error) {
	tok, err := runner.ReadTokenFile(g.tokenFile)
	if err != nil {
		return "", fmt.Errorf("this runner's %w", err)
	}
	return tok, nil
}

// Wrap is h behind the door. A request is served when its bearer verifies to the viff, and
// refused 401 otherwise, except a bare GET /describe, which is answered to anyone: the
// description is public. The describe answers the
// runner's own token to the caller it verified, and to nobody else.
func (g *Guard) Wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		describe := r.Method == http.MethodGet && r.URL.Path == "/describe"
		presented := r.Header.Get("Authorization") != ""
		if presented || !describe {
			if code, why := g.admit(r); code != 0 {
				if code == http.StatusUnauthorized {
					w.Header().Set("WWW-Authenticate", "Bearer")
				}
				http.Error(w, why, code)
				return
			}
		}
		if describe && presented && g.tokenFile != "" {
			tok, err := g.ownToken()
			if err != nil {
				// Named at start and gone since: said to the one caller who would otherwise
				// guess at it, and not served without, since the handshake is what it asked for.
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set(runner.TokenHeader, tok)
		}
		h.ServeHTTP(w, r)
	})
}

// admit is whether r is from the runner's viff: 0 when it is, else the status and the reason.
func (g *Guard) admit(r *http.Request) (int, string) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return http.StatusUnauthorized, fmt.Sprintf("this runner serves one viff, %s, and the request "+
			"presents no bearer: viffd presents a token it signs for this runner (auth.issuer)", g.v.Issuer)
	}
	id, err := g.verifierFor().Verify(r.Context(), token)
	if err != nil {
		if keysUnread(err) {
			return http.StatusServiceUnavailable, fmt.Sprintf("the viff's keys at %s%s could not be read, so the "+
				"bearer cannot be verified: %v", g.v.URL, keySetPath, err)
		}
		return http.StatusUnauthorized, fmt.Sprintf("the bearer did not verify as %s's token for %s: %v", g.v.Issuer, g.v.Audience, err)
	}
	// THE VIFF'S OWN TOKEN, NOT ANYTHING ITS ISSUER SIGNED. The issuer signs a gate credential
	// for whatever audience a charter declares, this runner's name included; what tells the
	// viff's token from one is its subject (runner.ViffSubject).
	if id.Subject != runner.ViffSubject {
		return http.StatusUnauthorized, fmt.Sprintf("the bearer is %s's token for %s, and not the viff's own: "+
			"its subject is %q, where the viff's is %q", g.v.Issuer, g.v.Audience, id.Subject, runner.ViffSubject)
	}
	return 0, ""
}

// verifierFor is the verifier of the viff's tokens, made once. The keys are read where the
// runner reaches its viff, as a token asks for them, and the tokens are held to the issuer name
// the block says: a viff called one thing outside and another in-cluster is one viff, and no
// discovery document is needed to say so. The key set keeps its context for the refetches a
// rotated key asks for, so it is the process's, not one request's.
func (g *Guard) verifierFor() *oidc.IDTokenVerifier {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.verifier == nil {
		ctx := context.Background()
		if g.client != nil {
			ctx = oidc.ClientContext(ctx, g.client)
		}
		keys := oidc.NewRemoteKeySet(ctx, strings.TrimRight(g.v.URL, "/")+keySetPath)
		g.verifier = oidc.NewVerifier(g.v.Issuer, keys, &oidc.Config{
			ClientID: g.v.Audience,
			Now:      func() time.Time { return time.Now().Add(-guardClockSkew) },
		})
	}
	return g.verifier
}

// keysUnread is whether verifying failed for want of the viff's keys rather than on the token:
// the fetch did not reach the viff, the viff did not answer it with keys, or what it answered
// did not decode. go-oidc's verifier wraps the key set's error with %v, so no error type survives
// to be matched, and the one thing every failed fetch says is the key set's own wording
// (keysFetchFailed); TestKeysUnreadMatchesGoOIDC holds go-oidc to it.
func keysUnread(err error) bool {
	return strings.Contains(err.Error(), keysFetchFailed)
}

// keysFetchFailed is how go-oidc's remote key set begins the error of every fetch that failed.
const keysFetchFailed = "fetching keys"

// The hello: how long the runner waits between tries, from the first to the longest. Variables,
// so a test need not wait a second to see the second try.
var (
	helloFirstWait = time.Second
	helloMaxWait   = 30 * time.Second
)

// Announce is the runner saying to its viff that it is here: POST /runners/hello with its own
// token, until the viff answers 2xx or ctx ends. Viffd finds the registration by the token's
// subject and describes the runner then and there, so a pod that comes up after its
// registration, or comes back after a restart, is verified within seconds and without anyone
// running `viff runner check`. A viff that is down and a registration that does not exist yet
// are one case here: keep asking, with backoff, and say why once per change of answer. Nothing
// to announce with (no token of its own) returns at once; the runner then waits to be asked.
//
// Every answer but a 2xx is tried again: the door's 401 (a token the viff cannot verify yet),
// the hello's 404 (no registration names this runner yet) and its 503 (the viff could not reach
// the runner back) are each one a later try can find changed.
func (g *Guard) Announce(ctx context.Context, logf func(string, ...any)) {
	if g.tokenFile == "" {
		return
	}
	target := strings.TrimRight(g.v.URL, "/") + "/runners/hello"
	wait := helloFirstWait
	last := ""
	for {
		said, done := g.hello(ctx, target)
		if done {
			logf("announced to %s: %s", g.v.URL, said)
			return
		}
		if said != last {
			logf("announcing to %s: %s; trying again", g.v.URL, said)
			last = said
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, helloMaxWait)
	}
}

// hello is one try: what the viff said, and whether the announcing is over.
func (g *Guard) hello(ctx context.Context, target string) (said string, done bool) {
	tok, err := g.ownToken()
	if err != nil {
		return err.Error(), false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return err.Error(), false
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := g.helloClient().Do(req)
	if err != nil {
		return err.Error(), false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 == 2 {
		return "the viff describes this runner now: " + strings.TrimSpace(string(body)), true
	}
	return fmt.Sprintf("%s: %s", resp.Status, strings.TrimSpace(string(body))), false
}

// helloTimeout bounds one try of the hello: a viff that takes the connection and never answers
// is a try that failed, and the backoff tries again.
var helloTimeout = 30 * time.Second

// helloClient is how the hello reaches the viff: as the door reads its keys, through the roots
// `viff.ca` names where it names any, and within helloTimeout.
func (g *Guard) helloClient() *http.Client {
	c := &http.Client{Timeout: helloTimeout}
	if g.client != nil {
		c.Transport = g.client.Transport
	}
	return c
}

// carryTimeout bounds one request Carry makes for a task. It is longer than viff's own longest
// wait in front of an act, the charter lock's (30s), and the act behind it: a runner that stopped
// waiting first would tell the task an install had not been made that viff then made. A request
// it does stop waiting on may have been done, and the task is told so (delivery).
var carryTimeout = 2 * time.Minute

// carryClient is how Carry reaches the viff: through the roots the hello uses, within
// carryTimeout.
func (g *Guard) carryClient() *http.Client {
	c := &http.Client{Timeout: carryTimeout}
	if g.client != nil {
		c.Transport = g.client.Transport
	}
	return c
}

// carryCap is the most of viff's answer Carry hands a task.
const carryCap = 4 << 20

// Carry makes a task's request to the viff this runner serves, a verb of the viff system: method
// on path, with the runner's own token and the task's id (api.TaskHeader), body a tony document
// where there is one, and the answer asked for in tony. Viff admits it as the task's owner
// through the task's record, which names this runner, so the task holds nothing and names nobody,
// and only on the routes its door carries for a task. It answers the viff's status and what it
// said; an error is a request that got no answer. A runner with no token of its own has nothing to
// carry it with, and Carrier is then nil.
func (g *Guard) Carry(ctx context.Context, taskID, method, path string, body []byte) (int, string, error) {
	tok, err := g.ownToken()
	if err != nil {
		return 0, "", err
	}
	u := strings.TrimRight(g.v.URL, "/") + path
	if strings.Contains(path, "?") {
		u += "&format=tony"
	} else {
		u += "?format=tony"
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/tony")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set(api.TaskHeader, taskID)
	resp, err := g.carryClient().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	// A read of the owner's asks is a listing, so the bound is a listing's. One byte past it tells
	// an answer that is over it from one that fits, and an answer over it is not handed on cut.
	got, err := io.ReadAll(io.LimitReader(resp.Body, carryCap+1))
	if err != nil {
		return 0, "", fmt.Errorf("reading viff's answer: %w", err)
	}
	if len(got) > carryCap {
		return 0, "", fmt.Errorf("viff's answer is over %d bytes, the most the runner carries to a task: read less at once", carryCap)
	}
	said := strings.TrimSpace(string(got))
	// A REFUSAL OF THE RUNNER, NOT OF THE ASK: viff's door says so with WWW-Authenticate (RFC
	// 6750), a token it did not take or a task it would not carry this runner's request for.
	// That is this runner's fault to fix, and an error to the task rather than a no.
	if resp.Header.Get("WWW-Authenticate") != "" {
		return 0, "", fmt.Errorf("viff refused this runner's request for the task (%d): %s", resp.StatusCode, said)
	}
	return resp.StatusCode, said, nil
}

// Carrier is Carry where the runner holds a token of its own to carry a task's request with, and
// nil where it holds none.
func (g *Guard) Carrier() func(ctx context.Context, taskID, method, path string, body []byte) (int, string, error) {
	if g.tokenFile == "" {
		return nil
	}
	return g.Carry
}

// WithReady is h with GET /ready beside it, for a readiness probe: 200 as soon as the runner
// serves, with or without a door. Readiness is the server's and nothing else's: whether a viff
// has heard this runner's hello, and whether its describe verified the runner, is the
// registration's to say (`described`), where `viff runner` shows it. A readiness
// that waited on viff would keep the pod out of its Service until viffd had reached it, and
// viffd reaches it through that Service.
func WithReady(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ready")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// StopPath is where a runner's viff asks it to stop: POST, on the control face, behind the door,
// so only the viff it serves may. The runner answers 202 and stops as a signal would stop it,
// its tasks with it. It is how a viff stops a runner it did not start, or no longer holds the
// process of: its local runner after the viff itself was restarted.
const StopPath = "/stop"

// WithStop is h with POST StopPath beside it, calling stop once the answer is written. Wrap it in
// the guard (Guard.Wrap), so the route is the viff's alone.
func WithStop(h http.Handler, stop func()) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != StopPath {
			h.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "POST "+StopPath+" stops this runner", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintln(w, "stopping")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go stop()
	})
}
