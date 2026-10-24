package runnerd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/signadot/verse/runner"
)

// The runner's door (git-issue pb6fwh8gh12krmb7vhn0).
//
// A RUNNER BELONGS TO ONE VERSE. Its configuration names the verse it serves by the tokens versed
// signs: versed is an issuer of its own (auth.issuer on the verse side), and presents on every
// call a token it minted for this runner, `iss` versed's issuer name and `aud` this runner's own
// subject. The runner reads versed's keys at the url it reaches versed at, and holds every bearer
// to that issuer, that audience and a live signature; a request that does not verify is refused
// before its body is read, so a launch, with its prompt and its repositories, leaves verse for a
// runner nobody else can drive. Without the block the runner answers whoever reaches its port,
// as every runner did.
//
// THE AUDIENCE IS THE RUNNER'S OWN NAME. A token minted for one runner is good at no other: a
// copy taken at a runner, or sent to a url somebody typed wrong, opens no sibling's door. The
// audience defaults to the subject of the runner's own token, which is what its registration
// names, so a registered runner types nothing; the default runner `verse up -runner` names has
// no registration and takes the audience `runner`, by saying so.
//
// THE DESCRIPTION IS PUBLIC; THE TOKEN IS NOT. GET /describe says what the runner offers, which
// is no secret, so a bare one is answered to anyone, as an operator at the port or a verse with
// no token asks it. A bearer presented with it is still checked, and refused when it does not verify: a
// verse that believes it proves itself and does not finds out at the describe, not at the first
// spawn.
//
// A TOKEN TOWARD A RUNNER OPENS NO DOOR OF VERSE'S. Versed's tokens toward runners are versed's
// own issue, where a runner's token toward versed is the cluster's with the audience `verse`: a
// runner holding what versed presented cannot turn round and present it at versed's door.
//
// THE DESCRIBE IS THE HANDSHAKE. To a caller it verified, and to nobody else, the runner answers
// GET /describe with its own workload token (runner.TokenHeader), from the file VERSE_TOKEN_FILE
// names; versed checks the subject against the runner's registration. Both sides check in the
// same exchange: a spoofed runner cannot receive verse's launches, and verse's runner cannot be
// driven by anyone else.

// VerseAudience is the audience the default runner, which has no registration, takes versed's
// tokens for, by saying `audience: runner` in its block.
const VerseAudience = runner.DefaultAudience

// keySetPath is where an issuer publishes the keys that verify its tokens, in OIDC's well-known
// place: versed serves it under its own url (versed/issuer.go).
const keySetPath = "/.well-known/jwks.json"

// Verse is the verse a runner serves, as its configuration names it (`verse:`): where the runner
// reaches it, and how it holds versed's tokens. A runner with none serves whoever reaches it.
type Verse struct {
	// URL is where the runner reaches its verse: the hello (Announce) and every verb a task
	// performs on verse through the runner go there, with the runner's own token, and versed's
	// keys are read there (keySetPath).
	URL string `tony:"field=url"`
	// Issuer is the `iss` of versed's tokens, `auth.issuer.url` on the verse side: what versed is
	// called where its relying parties are, which a verse behind a proxy may spell differently
	// from URL. Unset is URL.
	Issuer string `tony:"field=issuer,omitzero"`
	// Audience is the `aud` this runner takes versed's tokens for: its own name. Unset is the
	// subject of the runner's own token (NewGuard), which is what its registration names; the
	// default runner, which has no registration, says `runner` (VerseAudience).
	Audience string `tony:"field=audience,omitzero"`
	// CA is the path of the PEM bundle versed's certificate is checked against, for a verse
	// served over TLS under a certificate no public root signed. Unset trusts the system's roots.
	CA string `tony:"field=ca,omitzero"`
}

// Check fills in the defaults it can and refuses a block that names no verse.
func (v *Verse) Check() error {
	if v.URL == "" {
		return fmt.Errorf("verse: `url` names the verse this runner serves, where it reaches it; it is required")
	}
	if u, err := url.Parse(v.URL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("verse.url: %q is not the verse's URL", v.URL)
	}
	if v.Issuer == "" {
		v.Issuer = v.URL
	} else if u, err := url.Parse(v.Issuer); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("verse.issuer: %q is not an issuer's URL", v.Issuer)
	}
	// Versed spells its `iss` without a trailing slash whatever its config says, and a token is
	// held to the name as compared: the two spellings are one issuer here too.
	v.Issuer = strings.TrimRight(v.Issuer, "/")
	if v.Audience != "" {
		if err := runner.CheckSubject(v.Audience); err != nil {
			return fmt.Errorf("verse.audience: %w", err)
		}
	}
	return nil
}

// Guard is a runner's door: it admits the requests of the one verse the runner serves, and
// answers that verse the runner's own token on the describe.
type Guard struct {
	v         Verse
	tokenFile string       // the runner's own workload token, answered on the describe; "" answers none
	client    *http.Client // reaches the issuer where the system's roots do not; nil is the default

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
}

// guardClockSkew is how far past its expiry a token is still taken, for a clock that runs behind
// the verse's.
const guardClockSkew = 30 * time.Second

// NewGuard is the door for v. tokenFile names the runner's own workload token, which the door
// answers on the describe to the verse it verified, and whose subject is the audience the door
// takes where the block names none; "" answers none, and the block then names the audience. The
// file is read at each use, since the runtime rotates the token under the process, and once
// here, so a path that holds nothing is refused where it was named rather than guessed at from
// the verse side.
func NewGuard(v Verse, tokenFile string) (*Guard, error) {
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
			sub, err := runner.TokenSubject(tok)
			if err != nil {
				return nil, fmt.Errorf("verse.audience is unset and this runner's own token does not say whose it is: %w", err)
			}
			g.v.Audience = sub
		}
	}
	if g.v.Audience == "" {
		return nil, fmt.Errorf("verse.audience: a runner with no token of its own names the audience it takes its verse's tokens for (`runner` for the default runner)")
	}
	if v.CA != "" {
		pem, err := os.ReadFile(v.CA)
		if err != nil {
			return nil, fmt.Errorf("verse.ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("verse.ca: %s holds no certificate", v.CA)
		}
		g.client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	}
	return g, nil
}

// Verse is the block the door was made from, with its defaults filled in: the issuer the tokens
// are held to and the audience they are taken for.
func (g *Guard) Verse() Verse { return g.v }

// ownToken is the runner's own workload token, as the file holds it now.
func (g *Guard) ownToken() (string, error) {
	tok, err := runner.ReadTokenFile(g.tokenFile)
	if err != nil {
		return "", fmt.Errorf("this runner's %w", err)
	}
	return tok, nil
}

// Wrap is h behind the door. A request is served when its bearer verifies to the verse, and
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

// admit is whether r is from the runner's verse: 0 when it is, else the status and the reason.
func (g *Guard) admit(r *http.Request) (int, string) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return http.StatusUnauthorized, fmt.Sprintf("this runner serves one verse, %s, and the request "+
			"presents no bearer: versed presents a token it signs for this runner (auth.issuer)", g.v.Issuer)
	}
	if _, err := g.verifierFor().Verify(r.Context(), token); err != nil {
		if keysUnread(err) {
			return http.StatusServiceUnavailable, fmt.Sprintf("the verse's keys at %s%s could not be read, so the "+
				"bearer cannot be verified: %v", g.v.URL, keySetPath, err)
		}
		return http.StatusUnauthorized, fmt.Sprintf("the bearer did not verify as %s's token for %s: %v", g.v.Issuer, g.v.Audience, err)
	}
	return 0, ""
}

// verifierFor is the verifier of the verse's tokens, made once. The keys are read where the
// runner reaches its verse, as a token asks for them, and the tokens are held to the issuer name
// the block says: a verse called one thing outside and another in-cluster is one verse, and no
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

// keysUnread is whether verifying failed for want of the verse's keys rather than on the token:
// the fetch did not reach the verse, or the verse did not answer it with keys. go-oidc wraps the
// transport's error and spells a refused fetch; nothing else it says is about the fetch.
func keysUnread(err error) bool {
	var u *url.Error
	return errors.As(err, &u) || strings.Contains(err.Error(), "get keys failed")
}

// The hello: how long the runner waits between tries, from the first to the longest. Variables,
// so a test need not wait a second to see the second try.
var (
	helloFirstWait = time.Second
	helloMaxWait   = 30 * time.Second
)

// Announce is the runner saying to its verse that it is here: POST /runners/hello with its own
// token, until the verse answers 2xx or ctx ends. Versed finds the registration by the token's
// subject and describes the runner then and there, so a pod that comes up after its
// registration, or comes back after a restart, is verified within seconds and without anyone
// running `verse runner check`. A verse that is down and a registration that does not exist yet
// are one case here: keep asking, with backoff, and say why once per change of answer. Nothing
// to announce with (no token of its own) returns at once; the runner then waits to be asked.
//
// The loop is for the door's refusals (401), never for a verb verse's permissions refuse (403):
// a refused install is not a lost identity, and must not send a runner with tasks on it back to
// announcing itself.
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

// hello is one try: what the verse said, and whether the announcing is over.
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error(), false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 == 2 {
		return "the verse describes this runner now: " + strings.TrimSpace(string(body)), true
	}
	return fmt.Sprintf("%s: %s", resp.Status, strings.TrimSpace(string(body))), false
}

// WithReady is h with GET /ready beside it, for a readiness probe: 200 as soon as the runner
// serves, with or without a door. Readiness is the server's and nothing else's: whether a verse
// has heard this runner's hello, and whether its describe verified the runner, is the
// registration's to say (`verified`, `unverified`), where `verse runner` shows it. A readiness
// that waited on verse would keep the pod out of its Service until versed had reached it, and
// versed reaches it through that Service.
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
