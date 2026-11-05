package versed

import (
	"cmp"
	"context"
	"encoding/base64"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/signadot/verse/internal/config"
	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/runner"
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
// names it gives them. It is config.AuthConfig as the server takes it, and config's validator
// is the one that judges it (SetAuth), so a rule about a provider lives in one place.
type Auth struct {
	Mode      string
	Providers map[string]AuthProvider
	// Iod is how the deployment's iods are authenticated (iod.go).
	Iod AuthIod
}

// AuthProvider is one OpenID Connect issuer a verse accepts id_tokens from.
type AuthProvider struct {
	// Issuer is the issuer's URL: where its discovery document is, and every token's `iss`.
	Issuer string
	// ClientID is this verse's client at the issuer, which a token's `aud` holds.
	ClientID string
	// Domains are the email domains a first sight is admitted from.
	Domains []string
	// IOs are the installed iods the verse trusts for the provider's accounts, by the name each is
	// installed under: `github`'s, whose accounts are proved by a comment an io reflects, and
	// `slack`'s, whose io relays the member who typed (iod.go). A provider has an issuer or iods.
	IOs []string
	// Acts is what a trusted io may do as a person it relays: actsControl (""), or actsAny.
	Acts string
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

// config is this Auth as the config package spells it, for its validator.
func (a Auth) config() config.AuthConfig {
	c := config.AuthConfig{Principal: config.PrincipalAuthConfig{Mode: a.Mode}}
	if len(a.Providers) > 0 {
		c.Principal.Providers = map[string]config.AuthProviderConfig{}
	}
	for name, p := range a.Providers {
		c.Principal.Providers[name] = config.AuthProviderConfig{
			Issuer: p.Issuer, ClientID: p.ClientID, Domains: p.Domains, IOs: p.IOs,
			Acts: p.Acts,
		}
	}
	c.Iod = config.IodAuthConfig{Issuer: a.Iod.Issuer, Audience: a.Iod.Audience, CA: a.Iod.CA}
	if len(a.Iod.Accounts) > 0 {
		c.Iod.Accounts = map[string]config.IodAccountConfig{}
	}
	for sub, acct := range a.Iod.Accounts {
		c.Iod.Accounts[sub] = config.IodAccountConfig{Principal: acct.Principal, IO: acct.IO}
	}
	for _, st := range a.Iod.Static {
		c.Iod.Static = append(c.Iod.Static, config.IodStaticConfig{File: st.File, Principal: st.Principal, IO: st.IO})
	}
	return c
}

// authn is a server's providers as the door asks for them: by the issuer a token names, and by
// name, for the provider a relayed account is under; and the iods (iod.go).
type authn struct {
	mode     string
	byIssuer map[string]*authProvider
	byName   map[string]*authProvider
	iod      *iodAuth
}

// authProvider is one provider and its verifier, which is built the first time a token names it:
// a provider that is down when versed starts stops nothing but the requests that need it. The
// iods' issuer is one too (iod), whose tokens name an io rather than a person.
type authProvider struct {
	name string
	iod  bool
	AuthProvider
	// client reaches the issuer where the system's roots do not: the iods' issuer behind a
	// cluster's own CA (AuthIod.CA). nil is the default client.
	client *http.Client

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
	failed   time.Time
	failure  error
}

// SetAuth says how this verse authenticates people, and writes down which iods each provider
// trusts as evidence (trustRef). It is called before the server serves.
//
// THE TRUSTED IOS COME FROM HERE AND NOWHERE ELSE. Which iods' reflections count as evidence
// decides what a binding guarantees, so it is set by the one input that does not come through the
// door: the deployment's own config. It is written to the store as system:identity, where it is
// read like any record, and written again at every start, so the store says what the config does.
func (s *Server) SetAuth(a Auth) error {
	if err := a.config().Validate(); err != nil {
		return err
	}
	n := &authn{mode: a.Mode, byIssuer: map[string]*authProvider{}, byName: map[string]*authProvider{}}
	if n.mode == "" {
		n.mode = AuthOff
	}
	for name, p := range a.Providers {
		prov := &authProvider{name: name, AuthProvider: p}
		n.byName[name] = prov
		if p.Issuer != "" {
			n.byIssuer[p.Issuer] = prov
		}
	}
	if n.mode == AuthRequired && len(n.byIssuer) == 0 {
		return fmt.Errorf("auth.principal.mode: %s admits a person only from an identity provider's "+
			"token, and auth.principal.providers names no issuer", AuthRequired)
	}
	iodProv, iod, err := s.setIod(a.Iod)
	if err != nil {
		return err
	}
	if iodProv != nil {
		n.byIssuer[iodProv.Issuer] = iodProv
	}
	n.iod = iod
	if err := s.writeTrusts(a.Providers); err != nil {
		return err
	}
	s.auth = n
	return nil
}

// writeTrusts records each provider's trusted iods, and clears a provider's that the config no
// longer names, so the store says what the config does.
func (s *Server) writeTrusts(providers map[string]AuthProvider) error {
	sys := entity.As(s.Store, entity.SystemIdentity)
	standing, err := entity.BelowErr(sys, func(ref entity.Ref) bool {
		segs := ref.Segs()
		return len(segs) == 4 && segs[3] == "trust"
	}, 2, entity.VerseRoot, "identity")
	if err != nil {
		return err
	}
	written := map[string]bool{}
	for name, p := range providers {
		if len(p.IOs) > 0 || name == githubProvider {
			if err := s.writeTrust(name, p.IOs); err != nil {
				return fmt.Errorf("auth.principal.providers.%s.iods: %w", name, err)
			}
			written[name] = true
		}
	}
	// A trust node the config no longer speaks for — its provider gone, or kept and now an
	// issuer — is cleared, so nothing reads yesterday's iods as trusted.
	for _, e := range standing {
		if name := e.Ref.Segs()[2]; !written[name] && len(entity.Strs(e.Payload, "iods")) > 0 {
			if err := s.writeTrust(name, nil); err != nil {
				return err
			}
			written[name] = true
		}
	}
	if !written[githubProvider] {
		return s.writeTrust(githubProvider, nil)
	}
	return nil
}

// verifiesPeople reports whether this verse admits a `user:` only from a verified bearer.
func (s *Server) verifiesPeople() bool {
	return s.auth != nil && s.auth.mode == AuthRequired
}

// providerNames is the names of the providers people's tokens may come from, for a refusal that
// says what would be taken. The iods' issuer is not one of them.
func (n *authn) providerNames() string {
	names := make([]string, 0, len(n.byIssuer))
	for _, p := range n.byIssuer {
		if !p.iod {
			names = append(names, p.name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// peoplesIssuers reports whether any provider issues tokens for people.
func (n *authn) peoplesIssuers() bool {
	for _, p := range n.byIssuer {
		if !p.iod {
			return true
		}
	}
	return false
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
	if p.client != nil {
		ctx = oidc.ClientContext(ctx, p.client) // the key set keeps this client too
	}
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
	if n == nil || len(n.byIssuer) == 0 {
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
		if !n.peoplesIssuers() {
			return nil, turnAway(http.StatusUnauthorized, "this verse verifies no person's bearer: it "+
				"names no identity provider (auth.principal in its config), so a request says who makes "+
				"it in %s", api.PrincipalHeader)
		}
		return nil, turnAway(http.StatusUnauthorized, "the bearer was issued by %s, which is no identity "+
			"provider of this verse (it takes: %s)", iss, n.providerNames())
	}
	if n.mode == AuthOff && !p.iod {
		// People are not verified here, and a token is not served as nobody: an answer with
		// nothing in it reads as an empty verse.
		return nil, turnAway(http.StatusUnauthorized, "this verse verifies no bearer of a person "+
			"(auth.principal.mode is %s), so a request says who makes it in %s", AuthOff, api.PrincipalHeader)
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
// what the request's header says, which names the same principal or nobody — or, for a bearer
// that proves an io, a person the io relays (iod.go), in which case via is the io. account is the
// relayed person's account (api.ForPrincipalHeader), task the task a runner's request is made
// for (api.TaskHeader), and a is what the request does. Only a runner's bearer names a task.
func (s *Server) authenticate(ctx context.Context, token, presented, account, task string, a act) (principal, provider, via string, _ *turnedAway) {
	if io, ok := s.auth.staticIo(token); ok {
		if task != "" {
			return "", "", "", notARunnersTask(io.Principal, task)
		}
		return s.asIo(io, presented, account, a)
	}
	// A runner proved by a static token, which its registration names by its hash (runnerauth.go).
	if e, ok, err := s.staticRunner(token); err != nil {
		return "", "", "", turnAway(http.StatusServiceUnavailable, "whether the bearer is a runner's is not known: %v", err)
	} else if ok {
		return s.asRunner(e, presented, account, task, a)
	}
	// A bearer that is no JWT is a static token, an io's or a runner's, and is neither: said so,
	// rather than read as an id_token from a provider it never claimed to be from.
	if _, static := runner.StaticSubjectOf(token); static {
		return "", "", "", turnAway(http.StatusUnauthorized, "the bearer is a static token that is no "+
			"io's (auth.iod.static) and no registered runner's: a runner's registration names its "+
			"token by its hash (`verse runner add -token-file`), and none names this one")
	}
	v, ref := s.verifyToken(ctx, token)
	if ref != nil {
		return "", "", "", ref
	}
	p := v.provider
	if task != "" && !p.iod {
		return "", "", "", turnAway(http.StatusForbidden, "%s names task %s, and the bearer is a %s "+
			"account's: only the runner hosting a task makes a request for it", api.TaskHeader, task, p.name)
	}
	if p.iod {
		if io, ok := s.auth.iod.accounts[v.sub]; ok {
			if task != "" {
				return "", "", "", notARunnersTask(io.Principal, task)
			}
			return s.asIo(io, presented, account, a)
		}
		// A runner, by the subject its registration names (runnerauth.go). Its own requests are
		// made as itself: a name beside its token is its own or nobody's.
		e, ok, err := s.runnerBySubject(v.sub)
		if err != nil {
			return "", "", "", turnAway(http.StatusServiceUnavailable, "whether the bearer is a runner's is not known: %v", err)
		}
		if ok {
			return s.asRunner(e, presented, account, task, a)
		}
		return "", "", "", turnAway(http.StatusUnauthorized, "the bearer is a workload token whose "+
			"subject %s is no io of this verse (auth.iod.accounts) and no registered runner's "+
			"(`verse runner add -subject`)", v.sub)
	}
	sys := entity.As(s.Store, entity.SystemIdentity)
	cur, found, err := sys.Get(accountRef(p.name, v.sub))
	if err != nil {
		return "", "", "", turnAway(http.StatusBadGateway, "%v", err)
	}
	if found {
		if iss := entity.Str(cur.Payload, "iss"); iss != p.Issuer {
			return "", "", "", turnAway(http.StatusUnauthorized, "the %s account %s is bound as an account "+
				"of %s, and this token is from %s", p.name, v.sub, iss, p.Issuer)
		}
		principal = entity.Str(cur.Payload, "principal")
	} else {
		who, ref := s.firstSight(sys, v)
		if ref != nil {
			return "", "", "", ref
		}
		principal = who
	}
	if presented != "" {
		if g, err := entity.ParsePrincipal(presented); err != nil || g.String() != principal {
			return "", "", "", turnAway(http.StatusForbidden, "the bearer is %s and %s names %s: a request "+
				"is made as whoever it proves it is from", principal, api.PrincipalHeader, presented)
		}
	}
	return principal, p.name, "", nil
}

// asIo is a request a verified io makes: as itself, or relaying a person.
func (s *Server) asIo(io IodAccount, presented, account string, a act) (principal, provider, via string, _ *turnedAway) {
	if presented == "" || presented == io.Principal {
		return io.Principal, iodProvider, "", nil
	}
	return s.relay(io, presented, account, a)
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
// verified them, or `asserted` for a caller who is whoever their request said, and `via` the io
// that relayed them where one did. A request that names nobody is answered with none.
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	var out api.WhoAmI
	if p := principalOf(r); p != "" {
		out = api.WhoAmI{Principal: p, By: authenticatedBy(r), Via: relayedBy(r)}
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

// asRunner is a request whose bearer proved the runner e: its own, made as itself, or one it
// carries for a task it hosts (task), admitted through the task's record (relayTask). A name
// beside its token with no task is its own or nobody's.
//
// A RUNNER RELAYS A TASK, NEVER AN ACCOUNT. An account to relay beside a runner's token
// (api.ForPrincipalHeader) is refused, rather than carried out as the runner with the account
// unread: what a runner carries is a task's verb, and the task's record is the binding.
//
// A RUNNER CARRIES WHAT A TASK'S TOOLS DO ON VERSE, AND NOTHING ELSE (relayedActs): what follows a
// task's ask is held within the task where the ask is raised, and a request the tools never make
// would be the owner's act unheld.
func (s *Server) asRunner(e api.Runner, presented, account, task string, a act) (principal, provider, via string, _ *turnedAway) {
	if account != "" {
		return "", "", "", turnAway(http.StatusForbidden, "the bearer is runner %s and %s names an account "+
			"to relay: a runner relays a task it hosts (%s), never an account, and the request is not "+
			"carried out as the runner instead", e.Principal(), api.ForPrincipalHeader, api.TaskHeader)
	}
	if task != "" {
		if !relayedActs[a.pattern] {
			return "", "", "", turnAway(http.StatusForbidden, "runner %s carries a request for task %s to "+
				"%s, and a runner carries only what a task's tools do on verse: %s", e.Principal(), task,
				cmp.Or(a.pattern, a.method+" on a route verse does not have"), strings.Join(slices.Sorted(maps.Keys(relayedActs)), ", "))
		}
		return s.relayTask(e, presented, task)
	}
	if presented != "" && presented != e.Principal() {
		return "", "", "", turnAway(http.StatusForbidden, "the bearer is runner %s and %s names %s: "+
			"a runner's own request is made as itself, and one it carries for a task names the task (%s)",
			e.Principal(), api.PrincipalHeader, presented, api.TaskHeader)
	}
	return e.Principal(), runnerProvider, "", nil
}

// relayedActs are the routes a runner carries a task's request to, the verse system's verbs:
// raising an ask (`verse-ask-create`), reading the owner's asks (`verse-ask-get`,
// `verse-ask-list`), reading, writing and removing the verse at an address (`verse-data-get`,
// `-put`, `-rm`), reading the charters, asking whether some would install, installing and
// removing them (`verse-charter-get`, `-check`, `-install`, `-remove`; what a task installs is
// held within it, chartertask.go), and saying whom a request is made as.
var relayedActs = map[string]bool{
	"POST /asks": true, "GET /asks": true,
	"GET /state": true, "POST /entity": true, "DELETE /entity": true,
	"GET /charter": true, "POST /charter/check": true, "POST /charter": true, "DELETE /charter": true,
	"GET /whoami": true,
}

// notARunnersTask refuses a task named beside a bearer that proves an io: only a runner hosting a
// task carries a request for it.
func notARunnersTask(io, task string) *turnedAway {
	return turnAway(http.StatusForbidden, "%s names task %s, and the bearer is io %s's: only the "+
		"runner hosting a task makes a request for it", api.TaskHeader, task, io)
}
