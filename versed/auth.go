package versed

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// Who a request is from, verified (git-issue hfh7km3th12ksd6nqsn0, step 1).
//
// VERSE IS A RELYING PARTY. A deployment names the OpenID Connect providers its people sign in
// with (Auth), and a request carrying `Authorization: Bearer <id_token>` from one of them is
// verified at the door, ahead of the Verse-Principal header: the signature against the provider's
// published keys, that the token was issued for this verse's client (`aud`), and that it is in
// date (`exp`, `nbf`). It is the id_token and not an access token because an id_token's audience
// is the verse's own client, and a provider's access token is often opaque.
//
// THE PRINCIPAL IS THE BINDING, NOT THE CLAIM. A verified token says which account the caller
// holds, `(iss, sub)`, and the binding at `verse.identity.<provider>.account.<sub>` says which
// principal that account is (identity.go). So a person whose email changes at the provider keeps
// their principal, and an account linked to a principal acts as it whatever the token says.
//
// FIRST SIGHT. An account with no binding is bound to `user:<email>` when its token carries an
// email the provider has verified, in one of the domains the deployment admits for that provider.
// The binding is written by system:identity like any other, with the token's `iss`, `sub` and
// `iat` as its evidence and never the token.
//
// A BEARER THAT FAILS IS REFUSED, and never falls back to the header beside it: a write let
// through on a failed proof would name somebody verse had just failed to verify. A bearer sent to
// a verse that verifies none is refused too, since serving it as nobody would answer with what
// looks like an empty verse. And a verified bearer beside a header naming somebody else is
// refused, because acting as another is delegation, which a header does not confer.
//
// HOW THE CALLER WAS AUTHENTICATED travels with the request in its context (authenticatedBy),
// where a client cannot put it: what a binding records as its claimant is read from there.

// The modes of authenticating people.
const (
	// AuthOff verifies nobody: a request is whoever its header says.
	AuthOff = "off"
	// AuthOptional uses a verified bearer where a request carries one, and admits a bare header.
	AuthOptional = "optional"
	// AuthRequired admits a `user:` principal only from a verified bearer.
	AuthRequired = "required"
)

// Auth is how a deployment has people authenticated: the mode, and its identity providers by the
// names it gives them.
type Auth struct {
	Mode      string
	Providers map[string]AuthProvider
}

// AuthProvider is one OpenID Connect issuer a verse accepts id_tokens from.
type AuthProvider struct {
	// Issuer is the issuer's URL: where its discovery document is, and every token's `iss`.
	Issuer string
	// ClientID is this verse's client at the issuer, which a token's `aud` holds.
	ClientID string
	// Domains are the email domains a first sight is admitted from.
	Domains []string
	// IOs are the installed ios trusted as evidence of the provider's accounts: `github`'s, whose
	// accounts are proved by a comment an io reflects.
	IOs []string
}

const (
	// discoveryTimeout bounds reading a provider's discovery document.
	discoveryTimeout = 10 * time.Second
	// discoveryRetry is how long a failed discovery stands before it is asked again, so a provider
	// that is down costs the requests that name it one wait and not one each.
	discoveryRetry = 5 * time.Second
	// clockSkew is how far past its expiry a token is still taken, for a clock that runs behind
	// the provider's.
	clockSkew = 30 * time.Second
	// googleIssuer is the one issuer whose tokens may spell `iss` without its scheme.
	googleIssuer         = "https://accounts.google.com"
	googleIssuerNoScheme = "accounts.google.com"
)

// authn is a server's providers as the door asks for them: by the issuer a token names.
type authn struct {
	mode     string
	byIssuer map[string]*authProvider
}

// authProvider is one provider and its verifier, which is built the first time a token names it:
// a provider that is down when versed starts stops nothing but the requests that need it.
type authProvider struct {
	name string
	AuthProvider

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
	failed   time.Time
	failure  error
}

// SetAuth says how this verse authenticates people, and writes down which ios each provider
// trusts as evidence (trustRef). It is called before the server serves.
//
// THE TRUSTED IOS COME FROM HERE AND NOWHERE ELSE. Which ios' reflections count as evidence
// decides what a binding guarantees, so it is set by the one input that does not come through the
// door: the deployment's own config. It is written to the store as system:identity, where it is
// read like any record, and written again at every start, so the store says what the config does.
func (s *Server) SetAuth(a Auth) error {
	switch a.Mode {
	case "", AuthOff, AuthOptional, AuthRequired:
	default:
		return fmt.Errorf("auth.principal.mode: %q is not a mode: %s, %s or %s", a.Mode, AuthOff, AuthOptional, AuthRequired)
	}
	n := &authn{mode: a.Mode, byIssuer: map[string]*authProvider{}}
	if n.mode == "" {
		n.mode = AuthOff
	}
	for name, p := range a.Providers {
		if name == githubProvider {
			if p.Issuer != "" || p.ClientID != "" || len(p.Domains) > 0 {
				return fmt.Errorf("auth.principal.providers.%s: that name is the provider GitHub accounts "+
					"are bound under, by proof by action: its entry takes `ios` alone", name)
			}
			continue
		}
		switch {
		case name == "" || p.Issuer == "" || p.ClientID == "":
			return fmt.Errorf("auth.principal.providers.%s: a provider has a name, an issuer and a clientId", name)
		case len(p.IOs) > 0:
			return fmt.Errorf("auth.principal.providers.%s.ios: an io's reflection is evidence of a %s "+
				"account only", name, githubProvider)
		}
		if other, dup := n.byIssuer[p.Issuer]; dup {
			return fmt.Errorf("auth.principal.providers.%s: %s is %s's issuer too, and a token's "+
				"issuer names one provider", name, p.Issuer, other.name)
		}
		n.byIssuer[p.Issuer] = &authProvider{name: name, AuthProvider: p}
	}
	if n.mode == AuthRequired && len(n.byIssuer) == 0 {
		return fmt.Errorf("auth.principal.mode: %s admits a person only from an identity provider's "+
			"token, and auth.principal.providers names no issuer", AuthRequired)
	}
	if err := s.writeTrust(githubProvider, a.Providers[githubProvider].IOs); err != nil {
		return fmt.Errorf("auth.principal.providers.%s.ios: %w", githubProvider, err)
	}
	s.auth = n
	return nil
}

// verifiesPeople reports whether this verse admits a `user:` only from a verified bearer.
func (s *Server) verifiesPeople() bool {
	return s.auth != nil && s.auth.mode == AuthRequired
}

// providerNames is the configured providers' names, for a refusal that says what would be taken.
func (n *authn) providerNames() string {
	names := make([]string, 0, len(n.byIssuer))
	for _, p := range n.byIssuer {
		names = append(names, p.name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// provider is the provider a token's `iss` names, nil where it names none of them.
func (n *authn) provider(iss string) *authProvider {
	if iss == googleIssuerNoScheme {
		iss = googleIssuer
	}
	return n.byIssuer[iss]
}

// verifierFor is the provider's verifier, discovering the provider if nothing has yet.
func (p *authProvider) verifierFor() (*oidc.IDTokenVerifier, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.verifier != nil {
		return p.verifier, nil
	}
	if p.failure != nil && time.Since(p.failed) < discoveryRetry {
		return nil, p.failure
	}
	// Not the request's context: the discovery outlives the request that happened to need it.
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()
	prov, err := oidc.NewProvider(ctx, p.Issuer)
	if err != nil {
		p.failed, p.failure = time.Now(), err
		return nil, err
	}
	p.failure = nil
	p.verifier = prov.Verifier(&oidc.Config{
		ClientID: p.ClientID,
		Now:      func() time.Time { return time.Now().Add(-clockSkew) },
	})
	return p.verifier, nil
}

// admits reports whether email is in one of the provider's domains.
func (p *authProvider) admits(email string) bool {
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return false
	}
	for _, d := range p.Domains {
		if strings.EqualFold(email[at+1:], d) {
			return true
		}
	}
	return false
}

// bearerOf is the token a request carries as `Authorization: Bearer`, and "" where it carries none.
func bearerOf(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// tokenIssuer reads `iss` from a token nobody has verified yet, to know whose keys to verify it
// against. Nothing else is taken from it.
func tokenIssuer(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("it is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("it is not a JWT: %w", err)
	}
	var claims map[string]any
	if err := codec.Unmarshal(payload, &claims, codec.Tony); err != nil {
		return "", fmt.Errorf("its claims do not read: %w", err)
	}
	iss, _ := claims["iss"].(string)
	if iss == "" {
		return "", fmt.Errorf("it names no issuer")
	}
	return iss, nil
}

// claimBool reads a claim that is a boolean, which some providers send as the string "true".
type claimBool bool

func (b *claimBool) UnmarshalJSON(data []byte) error {
	*b = string(data) == "true" || string(data) == `"true"`
	return nil
}

// turnedAway is a request turned away at the door: the status, and the reason it is told.
type turnedAway struct {
	code int
	msg  string
}

func turnAway(code int, format string, args ...any) *turnedAway {
	return &turnedAway{code: code, msg: fmt.Sprintf(format, args...)}
}

// verified is an id_token that checked out: the provider it is from, and what it says.
type verified struct {
	provider *authProvider
	sub      string
	issuedAt time.Time
	email    string
	// emailVerified is the provider saying the email is the account holder's.
	emailVerified bool
}

// verifyToken checks an id_token against the provider its issuer names.
func (s *Server) verifyToken(ctx context.Context, token string) (*verified, *turnedAway) {
	n := s.auth
	if n == nil || n.mode == AuthOff || len(n.byIssuer) == 0 {
		return nil, turnAway(http.StatusUnauthorized, "this verse verifies no bearer: it names no "+
			"identity provider (auth.principal in its config), so a request says who makes it in %s",
			api.PrincipalHeader)
	}
	iss, err := tokenIssuer(token)
	if err != nil {
		return nil, turnAway(http.StatusUnauthorized, "the bearer is not an id_token: %v", err)
	}
	p := n.provider(iss)
	if p == nil {
		return nil, turnAway(http.StatusUnauthorized, "the bearer was issued by %s, which is no identity "+
			"provider of this verse (it takes: %s)", iss, n.providerNames())
	}
	verifier, err := p.verifierFor()
	if err != nil {
		return nil, turnAway(http.StatusServiceUnavailable, "the identity provider %s could not be "+
			"read, so its tokens cannot be verified: %v", p.name, err)
	}
	tok, err := verifier.Verify(ctx, token)
	if err != nil {
		return nil, turnAway(http.StatusUnauthorized, "the bearer did not verify against %s: %v", p.name, err)
	}
	if tok.Subject == "" {
		return nil, turnAway(http.StatusUnauthorized, "the bearer names no account (it has no sub)")
	}
	var claims struct {
		Email         string    `json:"email"`
		EmailVerified claimBool `json:"email_verified"`
	}
	if err := tok.Claims(&claims); err != nil {
		return nil, turnAway(http.StatusUnauthorized, "the bearer's claims do not read: %v", err)
	}
	return &verified{
		provider: p, sub: tok.Subject, issuedAt: tok.IssuedAt,
		email: claims.Email, emailVerified: bool(claims.EmailVerified),
	}, nil
}

// authenticate is who a request carrying a bearer is from: the principal its account is bound to,
// bound at first sight where it is not yet, and the provider that vouched for it. presented is
// what the request's header says, which names the same principal or nobody.
func (s *Server) authenticate(ctx context.Context, token, presented string) (principal, provider string, _ *turnedAway) {
	v, ref := s.verifyToken(ctx, token)
	if ref != nil {
		return "", "", ref
	}
	p := v.provider
	sys := entity.As(s.Store, entity.SystemIdentity)
	cur, found, err := sys.Get(accountRef(p.name, v.sub))
	if err != nil {
		return "", "", turnAway(http.StatusBadGateway, "%v", err)
	}
	if found {
		if iss := entity.Str(cur.Payload, "iss"); iss != p.Issuer {
			return "", "", turnAway(http.StatusUnauthorized, "the %s account %s is bound as an account "+
				"of %s, and this token is from %s", p.name, v.sub, iss, p.Issuer)
		}
		principal = entity.Str(cur.Payload, "principal")
	} else {
		who, ref := s.firstSight(sys, v)
		if ref != nil {
			return "", "", ref
		}
		principal = who
	}
	if presented != "" {
		if g, err := entity.ParsePrincipal(presented); err != nil || g.String() != principal {
			return "", "", turnAway(http.StatusForbidden, "the bearer is %s and %s names %s: a request "+
				"is made as whoever it proves it is from", principal, api.PrincipalHeader, presented)
		}
	}
	return principal, p.name, nil
}

// firstSight binds an account nobody has been bound from to `user:<email>`, where its token
// carries a verified email in one of the provider's domains.
func (s *Server) firstSight(sys entity.Store, v *verified) (string, *turnedAway) {
	p := v.provider
	const unbound = "no principal is bound to this %s account, and a first sight binds one only from " +
		"a verified email in the provider's domains: "
	switch {
	case v.email == "":
		return "", turnAway(http.StatusUnauthorized, unbound+"the token carries no email (log in asking for the `email` scope)", p.name)
	case !v.emailVerified:
		return "", turnAway(http.StatusUnauthorized, unbound+"%s has not verified %s", p.name, p.name, v.email)
	case !p.admits(v.email):
		return "", turnAway(http.StatusUnauthorized, unbound+"%s is in none of them", p.name, v.email)
	}
	// An address's case carries nothing, and a grant is spelled by somebody typing it.
	g, err := entity.ParsePrincipal(entity.UserKind + ":" + strings.ToLower(v.email))
	if err != nil || g.Kind != entity.UserKind {
		return "", turnAway(http.StatusUnauthorized, unbound+"%s is no name for a person", p.name, v.email)
	}
	who := g.String()
	if code, err := s.bind(sys, p.name, oidcBinding(v, who, p.name), who, v.sub); err != nil {
		return "", turnAway(code, "%v", err)
	}
	return who, nil
}

// whoami answers who the request is from and how versed knows it: `by` is the provider that
// verified them, or `asserted` for a caller who is whoever their request said. A request that
// names nobody is answered with neither.
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	if p := principalOf(r); p != "" {
		out["principal"], out["by"] = p, authenticatedBy(r)
	}
	writeOut(w, r, http.StatusOK, out)
}

// authnKey is the context key of how a request's caller was authenticated.
type authnKey struct{}

// authenticated marks r as from a caller the named provider vouched for.
func authenticated(r *http.Request, provider string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authnKey{}, provider))
}

// authenticatedBy is how a request's caller was authenticated: the provider that vouched for
// them, or claimantAsserted for a caller who is whoever their request said.
func authenticatedBy(r *http.Request) string {
	if provider, ok := r.Context().Value(authnKey{}).(string); ok && provider != "" {
		return provider
	}
	return claimantAsserted
}
