// Package idptest is an OpenID Connect identity provider for tests: discovery, a JWKS, and
// id_tokens minted with whatever claims a test asks for, signed with a key it generates.
//
// It is the IdP a test points a verse at (versed's `auth.principal` providers) and logs a CLI in to,
// and the relying party's side of the same documents is what a test of versed as an issuer reads.
// Everything it serves is OIDC's wire format, which is JSON by specification, and the JSON stops
// here: a test holds a token as a string and claims as a map.
package idptest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// IdP is one issuer, served over HTTP for as long as its test runs.
type IdP struct {
	// URL is the issuer: what a provider's `issuer` is configured as, and every token's `iss`.
	URL string

	t   testing.TB
	srv *httptest.Server

	mu  sync.Mutex
	key *rsa.PrivateKey
	kid string
	// jwksFetches counts the reads of the key set, so a test can see a refetch happen.
	jwksFetches int
	// handlers are the endpoints beyond discovery and keys that a test of a login flow adds.
	handlers map[string]http.HandlerFunc
	// discovery is what the discovery document says beyond the issuer and the key set.
	discovery map[string]any
}

// New starts an IdP and stops it when the test ends.
func New(t testing.TB) *IdP {
	t.Helper()
	p := &IdP{t: t, handlers: map[string]http.HandlerFunc{}, discovery: map[string]any{}}
	p.Rotate()
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	p.URL = p.srv.URL
	return p
}

// NewTLS is New over TLS, under a certificate no public root signed: an issuer like a cluster's
// own API server, which a verifier reaches only trusting the CA it hands out (CAFile).
func NewTLS(t testing.TB) *IdP {
	t.Helper()
	p := &IdP{t: t, handlers: map[string]http.HandlerFunc{}, discovery: map[string]any{}}
	p.Rotate()
	p.srv = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	p.URL = p.srv.URL
	return p
}

// CAFile is the issuer's certificate as a PEM bundle in a file, for an IdP started with NewTLS.
func (p *IdP) CAFile() string {
	p.t.Helper()
	cert := p.srv.Certificate()
	if cert == nil {
		p.t.Fatal("CAFile: the IdP is not served over TLS")
	}
	path := filepath.Join(p.t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o644); err != nil {
		p.t.Fatal(err)
	}
	return path
}

// Handle serves path with h, and names it in the discovery document under key when key is not
// empty: `Handle("token_endpoint", "/token", h)`.
func (p *IdP) Handle(key, path string, h http.HandlerFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handlers[path] = h
	if key != "" {
		p.discovery[key] = p.URL + path
	}
}

// Rotate replaces the signing key: tokens minted from here on carry a new `kid`, and the key set
// serves only the new key, as an IdP's does once the old one has aged out.
func (p *IdP) Rotate() {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		p.t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.key = key
	sum := sha256.Sum256(key.PublicKey.N.Bytes())
	p.kid = base64.RawURLEncoding.EncodeToString(sum[:8])
}

// JWKSFetches is how many times the key set has been read.
func (p *IdP) JWKSFetches() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jwksFetches
}

// Token mints an id_token for the client `aud`, about `sub`, good for an hour. claims are laid
// over the standard ones, so a test says only what its case is about: an `email`, an `exp` in the
// past, another `iss`. A claim set to nil is left out.
func (p *IdP) Token(aud, sub string, claims map[string]any) string {
	p.mu.Lock()
	key, kid := p.key, p.kid
	p.mu.Unlock()
	return p.sign(key, map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}, p.claims(aud, sub, claims))
}

// Forged mints a token as Token does, signed with a key the IdP does not publish, under the `kid`
// of the one it does.
func (p *IdP) Forged(aud, sub string, claims map[string]any) string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		p.t.Fatal(err)
	}
	p.mu.Lock()
	kid := p.kid
	p.mu.Unlock()
	return p.sign(key, map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}, p.claims(aud, sub, claims))
}

// Unsigned mints a token whose header says `alg: none` and whose signature is empty.
func (p *IdP) Unsigned(aud, sub string, claims map[string]any) string {
	return p.sign(nil, map[string]any{"alg": "none", "typ": "JWT"}, p.claims(aud, sub, claims))
}

func (p *IdP) claims(aud, sub string, over map[string]any) map[string]any {
	now := time.Now()
	c := map[string]any{
		"iss": p.URL, "aud": aud, "sub": sub,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
	for k, v := range over {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	return c
}

func (p *IdP) sign(key *rsa.PrivateKey, header, claims map[string]any) string {
	enc := base64.RawURLEncoding
	h, err := json.Marshal(header)
	if err != nil {
		p.t.Fatal(err)
	}
	c, err := json.Marshal(claims)
	if err != nil {
		p.t.Fatal(err)
	}
	signed := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	if key == nil {
		return signed + "."
	}
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return signed + "." + enc.EncodeToString(sig)
}

func (p *IdP) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		p.mu.Lock()
		doc := map[string]any{
			"issuer":                                p.URL,
			"jwks_uri":                              p.URL + "/keys",
			"authorization_endpoint":                p.URL + "/authorize",
			"token_endpoint":                        p.URL + "/token",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		for k, v := range p.discovery {
			doc[k] = v
		}
		p.mu.Unlock()
		WriteJSON(w, http.StatusOK, doc)
	case "/keys":
		p.mu.Lock()
		p.jwksFetches++
		pub, kid := p.key.PublicKey, p.kid
		p.mu.Unlock()
		enc := base64.RawURLEncoding
		WriteJSON(w, http.StatusOK, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
			"n": enc.EncodeToString(pub.N.Bytes()),
			"e": enc.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	default:
		p.mu.Lock()
		h := p.handlers[r.URL.Path]
		p.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}
}

// WriteJSON answers with v as JSON, which is how every OIDC and OAuth endpoint answers.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Logins is the endpoints a CLI logs in through, serving one account that approves whatever is
// asked of it: the authorization endpoint (code + PKCE), the token endpoint (a code, a device
// code, a refresh token), and the device authorization endpoint where the IdP offers one.
type Logins struct {
	idp      *IdP
	clientID string
	sub      string

	mu sync.Mutex
	// Claims are the claims of every id_token the endpoints give out.
	Claims map[string]any
	// Life is how long an id_token they give out is good for; an hour unless a test says.
	Life time.Duration
	// NoRefreshToken has the token endpoint give no refresh token, as an IdP does for a client
	// that did not ask to stay logged in.
	NoRefreshToken bool
	// NoIDTokenOnRefresh has a refresh answer with an access token alone, as some IdPs do.
	NoIDTokenOnRefresh bool
	// Stalled has the token endpoint take a request and sit on it, as a provider that has gone
	// quiet does: until the caller gives up, or for stallFor, after which it answers 503 so that
	// a caller with no patience of its own fails its test and does not hang it.
	Stalled bool
	// Refreshes counts the refresh grants served.
	Refreshes int
	// Scopes is what the last login asked for.
	Scopes string
	// Secret, when set, is the client secret the token endpoint wants beside the client id, as
	// Google's installed-app flows do; a token request without it is refused as invalid_client.
	Secret string

	codes     map[string]string // authorization code → PKCE challenge
	devices   map[string]bool   // device code → given out
	refreshes map[string]bool   // refresh token → good
}

// stallFor is how long a stalled token endpoint sits on a request before it answers 503.
const stallFor = 3 * time.Second

// Login turns the login endpoints on for clientID, logging in the account sub. With device, the
// discovery document names a device authorization endpoint.
func (p *IdP) Login(clientID, sub string, claims map[string]any, device bool) *Logins {
	l := &Logins{
		idp: p, clientID: clientID, sub: sub, Claims: claims, Life: time.Hour,
		codes: map[string]string{}, devices: map[string]bool{}, refreshes: map[string]bool{},
	}
	p.Handle("authorization_endpoint", "/authorize", l.authorize)
	p.Handle("token_endpoint", "/token", l.token)
	if device {
		p.Handle("device_authorization_endpoint", "/device", l.device)
	}
	p.mu.Lock()
	p.discovery["scopes_supported"] = []string{"openid", "email", "offline_access"}
	p.mu.Unlock()
	return l
}

// next is a fresh opaque value: a code, a device code, a refresh token.
func (l *Logins) next(kind string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return kind + "-" + base64.RawURLEncoding.EncodeToString(b)
}

// authorize is the browser's stop: it approves at once, and sends the browser back to the
// client's redirect with a code.
func (l *Logins) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != l.clientID || q.Get("response_type") != "code" ||
		q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "authorize: wants this client, a code, and a PKCE S256 challenge", http.StatusBadRequest)
		return
	}
	l.mu.Lock()
	code := l.next("code")
	l.codes[code] = q.Get("code_challenge")
	l.Scopes = q.Get("scope")
	l.mu.Unlock()
	http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+q.Get("state"), http.StatusFound)
}

// device gives out a device code, which the account has approved by the time it is polled for.
func (l *Logins) device(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.PostForm.Get("client_id") != l.clientID {
		oauthError(w, "invalid_client")
		return
	}
	l.mu.Lock()
	code := l.next("device")
	l.devices[code] = true
	l.Scopes = r.PostForm.Get("scope")
	l.mu.Unlock()
	WriteJSON(w, http.StatusOK, map[string]any{
		"device_code": code, "user_code": "ABCD-EFGH",
		"verification_uri":          l.idp.URL + "/device/approve",
		"verification_uri_complete": l.idp.URL + "/device/approve?user_code=ABCD-EFGH",
		"expires_in":                300, "interval": 1,
	})
}

// token serves the three grants a CLI uses.
func (l *Logins) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, "invalid_request")
		return
	}
	f := r.PostForm
	l.mu.Lock()
	stalled := l.Stalled
	l.mu.Unlock()
	if stalled {
		select {
		case <-r.Context().Done():
		case <-time.After(stallFor):
			http.Error(w, "stalled", http.StatusServiceUnavailable)
		}
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Secret != "" && f.Get("client_secret") != l.Secret {
		oauthError(w, "invalid_client")
		return
	}
	withID := true
	switch f.Get("grant_type") {
	case "authorization_code":
		challenge, ok := l.codes[f.Get("code")]
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			oauthError(w, "invalid_grant")
			return
		}
		delete(l.codes, f.Get("code"))
	case "urn:ietf:params:oauth:grant-type:device_code":
		if !l.devices[f.Get("device_code")] {
			oauthError(w, "invalid_grant")
			return
		}
		delete(l.devices, f.Get("device_code"))
	case "refresh_token":
		if !l.refreshes[f.Get("refresh_token")] {
			oauthError(w, "invalid_grant")
			return
		}
		l.Refreshes++
		withID = !l.NoIDTokenOnRefresh
	default:
		oauthError(w, "unsupported_grant_type")
		return
	}
	out := map[string]any{"access_token": l.next("access"), "token_type": "Bearer", "expires_in": 3600}
	if withID {
		claims := map[string]any{"exp": time.Now().Add(l.Life).Unix()}
		for k, v := range l.Claims {
			claims[k] = v
		}
		out["id_token"] = l.idp.Token(l.clientID, l.sub, claims)
	}
	if !l.NoRefreshToken && f.Get("grant_type") != "refresh_token" {
		rt := l.next("refresh")
		l.refreshes[rt] = true
		out["refresh_token"] = rt
	}
	WriteJSON(w, http.StatusOK, out)
}

// oauthError answers as a token endpoint refuses: 400 with the error's code.
func oauthError(w http.ResponseWriter, code string) {
	WriteJSON(w, http.StatusBadRequest, map[string]any{"error": code})
}
