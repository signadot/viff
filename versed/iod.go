package versed

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
)

// iods at the door, and a person relayed by a trusted io (git-issue hfh7km3th12ksd6nqsn0 §1.7;
// the plan is hqh7km3th12ks1qxqsn0, step 3).
//
// AN IO IS THE DEPLOYMENT'S OWN PROGRAM, and it proves it is the io with the token its runtime
// gives it: on Kubernetes the pod's projected service-account token, whose issuer is the
// cluster's; on a machine with no cluster, a token in a file the deployment wrote. The door
// verifies the first with the verifier it uses for a person's provider, checking `aud` against
// the audience the token was asked for, and the second by its hash. Which io a token is, is
// config (AuthIod.Accounts), written at start and never at first sight: `system:serviceaccount:
// verse:slack-iod` is `agent:slack-iod`, installed as `workspace`. A verified io is its principal
// under every mode.
//
// A PERSON RELAYED BY A TRUSTED IO. An io that acts for a person — the Slack member who typed an
// answer, the GitHub login who commented — does not act on its own authority. It relays a person
// the external system identified. The one exception to "a bearer beside a header naming another
// is refused" (auth.go) admits exactly that, when all of these hold, in order:
//
//  1. the bearer verifies to an io (above);
//  2. that io is in the provider's `iods`: the deployment said this io's word counts for that
//     provider's accounts, and for nobody else's;
//  3. the request names the account (api.ForPrincipalHeader, `<provider>:<id>`), which is what the io
//     saw, beside the person it concluded (api.PrincipalHeader);
//  4. the account resolves to the presented person through its binding, or on first sight, where
//     the provider has domains and the person's email is in one, in which case the binding is
//     written with the provider as its claimant and the io as `via`;
//  5. the act is one a trusted io does for a person — a read, or answering, saying and steering
//     — unless the provider's entry says `acts: any`.
//
// The request then proceeds as the person: the permission check, the write and the record are
// theirs, and a ballot says `authenticated: <provider>` and `via: <the io>`. What is trusted is
// then stated exactly: verse checked the io is the io; the external system checked the person;
// the deployment said the io counts for these accounts. It is not delegation between people: a
// person's bearer beside another person's name stays refused.
const iodProvider = "iod"

// AuthIod is how the deployment's iods are authenticated: the issuer of their workload tokens and
// which io each token's subject is, or a token in a file for a deployment with no issuer.
type AuthIod struct {
	Issuer   string
	Audience string
	// CA is the path of the PEM bundle the issuer's certificate is checked against, for an
	// issuer that is not behind a public one: a cluster's own API server. "" is the system's roots.
	CA       string
	Accounts map[string]IodAccount
	Static   []IodStatic
}

// IodAccount is one io: the principal it acts as, and the name it is installed under, which is
// how a provider's `iods` names it. An io that relays nobody needs no name.
type IodAccount struct {
	Principal string
	IO        string
}

// IodStatic is one io that presents a fixed token, kept in a file.
type IodStatic struct {
	File      string
	Principal string
	IO        string
}

// iodAudience is what a workload token's `aud` holds unless the config says otherwise.
const iodAudience = "verse"

// What a trusted io may do as a person it relays (AuthProvider.Acts).
const (
	// actsControl is the reads, and answering, saying and steering: all an io does. Unset means it.
	actsControl = "control"
	// actsAny is every act.
	actsAny = "any"
)

// iodAuth is the iods as the door asks for them: by a token's subject, and by a static token's hash.
type iodAuth struct {
	accounts map[string]IodAccount
	static   map[[sha256.Size]byte]IodAccount
}

// setIod reads the iods' block: the issuer's verifier, the accounts, and the static tokens' hashes.
// It writes the accounts to the store as system:identity, so the store says what the config does.
func (s *Server) setIod(a AuthIod) (*authProvider, *iodAuth, error) {
	out := &iodAuth{accounts: map[string]IodAccount{}, static: map[[sha256.Size]byte]IodAccount{}}
	if len(a.Accounts) > 0 && a.Issuer == "" {
		return nil, nil, fmt.Errorf("auth.iod.accounts: an account is a token's subject, and no issuer of those tokens is named (auth.iod.issuer)")
	}
	for sub, acct := range a.Accounts {
		if err := ioPrincipal(acct.Principal); err != nil {
			return nil, nil, fmt.Errorf("auth.iod.accounts.%q: %w", sub, err)
		}
		out.accounts[sub] = acct
	}
	for i, st := range a.Static {
		if err := ioPrincipal(st.Principal); err != nil {
			return nil, nil, fmt.Errorf("auth.iod.static[%d]: %w", i, err)
		}
		data, err := os.ReadFile(st.File)
		if err != nil {
			return nil, nil, fmt.Errorf("auth.iod.static[%d]: %w", i, err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return nil, nil, fmt.Errorf("auth.iod.static[%d]: %s holds no token", i, st.File)
		}
		out.static[sha256.Sum256([]byte(token))] = IodAccount{Principal: st.Principal, IO: st.IO}
	}
	if err := s.writeIodAccounts(a, out); err != nil {
		return nil, nil, err
	}
	var prov *authProvider
	if a.Issuer != "" {
		aud := a.Audience
		if aud == "" {
			aud = iodAudience
		}
		prov = &authProvider{name: iodProvider, iod: true, AuthProvider: AuthProvider{Issuer: a.Issuer, ClientID: aud}}
		if a.CA != "" {
			client, err := clientTrusting(a.CA)
			if err != nil {
				return nil, nil, fmt.Errorf("auth.iod.ca: %w", err)
			}
			prov.client = client
		}
	}
	return prov, out, nil
}

// clientTrusting is an HTTP client that trusts the certificates in the PEM bundle at path, and
// nothing else: how the door reaches an issuer whose certificate no public root signed, which
// on a cluster is the API server's, under the cluster's own CA.
func clientTrusting(path string) (*http.Client, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s holds no certificate", path)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}, nil
}

// ioPrincipal refuses a principal that is not an io's: an io is an `agent:`.
func ioPrincipal(p string) error {
	g, err := entity.ParsePrincipal(p)
	if err != nil || g.Kind != entity.AgentKind {
		return fmt.Errorf("%q is not an io's principal, which is `agent:<name>`", p)
	}
	return nil
}

// writeIodAccounts records the iods at `verse.identity.iod.account.<sub>` and
// `verse.identity.iod.static.<hash>`, everyone's to read, replacing what stood: the door answers
// from memory, and the record is how an operator sees what the door was told.
func (s *Server) writeIodAccounts(a AuthIod, io *iodAuth) error {
	sys := entity.As(s.Store, entity.SystemIdentity)
	for _, kind := range []string{"account", "static"} {
		old, err := entity.BelowErr(sys, nil, 1, identityRef(iodProvider, kind).Segs()...)
		if err != nil {
			return err
		}
		for _, e := range old {
			if _, _, err := sys.Write(entity.Write{At: e.Ref.At()}); err != nil {
				return err
			}
		}
	}
	write := func(ref entity.Ref, acct IodAccount, more entity.Obj) error {
		grants := []entity.Grant{{At: ref.Segs(), To: map[entity.Level][]string{entity.Read: {entity.Everyone}}}}
		if err := entity.Establish(s.Store, ref, entity.SystemIdentity, entity.Ref{}, grants); err != nil {
			return err
		}
		rec := entity.Obj{"principal": acct.Principal}
		if acct.IO != "" {
			rec["io"] = acct.IO
		}
		for k, v := range more {
			rec[k] = v
		}
		_, _, err := sys.Write(entity.Write{At: ref.At(), Value: rec.Payload()})
		return err
	}
	for sub, acct := range io.accounts {
		if err := write(accountRef(iodProvider, sub), acct, entity.Obj{"iss": a.Issuer, "sub": sub}); err != nil {
			return fmt.Errorf("auth.iod.accounts.%q: %w", sub, err)
		}
	}
	for h, acct := range io.static {
		if err := write(identityRef(iodProvider, "static", hex.EncodeToString(h[:])), acct, nil); err != nil {
			return fmt.Errorf("auth.iod.static: %w", err)
		}
	}
	return nil
}

// providerNamed is the provider the door knows by name, or nil.
func (s *Server) providerNamed(name string) *authProvider {
	if s.auth == nil {
		return nil
	}
	return s.auth.byName[name]
}

// ioOf is the io acting as principal, by the accounts the deployment configured, or nobody.
func (s *Server) ioOf(principal string) (IodAccount, bool) {
	if s.auth == nil || s.auth.iod == nil {
		return IodAccount{}, false
	}
	for _, acct := range s.auth.iod.accounts {
		if acct.Principal == principal {
			return acct, true
		}
	}
	for _, acct := range s.auth.iod.static {
		if acct.Principal == principal {
			return acct, true
		}
	}
	return IodAccount{}, false
}

// staticIo is the io a token that is not a JWT belongs to, if a static token hashes to it.
func (n *authn) staticIo(token string) (IodAccount, bool) {
	if n == nil || n.iod == nil || len(n.iod.static) == 0 {
		return IodAccount{}, false
	}
	acct, ok := n.iod.static[sha256.Sum256([]byte(token))]
	return acct, ok
}

// act is one request's act, as the door sees it: its method, the route it matched, and whether
// that route is one a trusted io does for a person — a read, or a route registered as a control
// act (router.handleControl: answering a question, saying on it, steering). A request that
// matched no route is left to the router, which answers it.
type act struct {
	method, pattern string
	control         bool
}

// relay is a verified io presenting a person: who the request is from, the provider that vouches
// for the account, and the io it came through. See the conditions at the top of this file.
func (s *Server) relay(io IodAccount, presented, account string, a act) (principal, provider, via string, _ *turnedAway) {
	n := s.auth
	g, err := entity.ParsePrincipal(presented)
	if err != nil || g.Kind != entity.UserKind {
		return "", "", "", turnAway(http.StatusForbidden, "the bearer is %s and %s names %s: an io acts "+
			"as itself, or relays a person (`user:`) it identified, and nobody else", io.Principal,
			api.PrincipalHeader, presented)
	}
	// An address's case carries nothing, as at a first sight through a token.
	g.ID = strings.ToLower(g.ID)
	who := g.String()
	pname, id, ok := strings.Cut(account, ":")
	if !ok || pname == "" || id == "" {
		return "", "", "", turnAway(http.StatusForbidden, "an io relaying a person names the account "+
			"the person is in %s, `<provider>:<id>`, and this request %s", api.ForPrincipalHeader,
			map[bool]string{true: "names none", false: fmt.Sprintf("names %q", account)}[account == ""])
	}
	p := n.byName[pname]
	if p == nil || len(p.IOs) == 0 {
		return "", "", "", turnAway(http.StatusForbidden, "%s is no provider of this verse that "+
			"trusts iods (auth.principal.providers.<name>.iods)", pname)
	}
	trusted := false
	for _, name := range p.IOs {
		if io.IO != "" && name == io.IO {
			trusted = true
		}
	}
	if !trusted {
		return "", "", "", turnAway(http.StatusForbidden, "%s is not an io this verse trusts for %s "+
			"accounts (auth.principal.providers.%s.iods names %s)", io.Principal, pname, pname,
			strings.Join(p.IOs, ", "))
	}
	// Nothing below writes until the act is one the io may do: a refused request leaves no
	// binding behind. The binding is read first, so that a conflict is reported as one.
	sys := entity.As(s.Store, entity.SystemIdentity)
	ref := accountRef(pname, id)
	cur, found, err := sys.Get(ref)
	if err != nil {
		return "", "", "", turnAway(http.StatusBadGateway, "%v", err)
	}
	// A binding proved by a caller nobody verified does not say who an account is where people
	// are verified, as the io's own lookup already holds; it is proved again here, by this relay,
	// which is what the first-sight path below does with it.
	if found && s.verifiesPeople() && entity.Str(cur.Payload, "claimant") == claimantAsserted {
		found = false
	}
	switch {
	case found && entity.Str(cur.Payload, "principal") == who:
	case found:
		return "", "", "", turnAway(http.StatusConflict, "%s account %s is bound to another principal: "+
			"one account is one principal, so that binding is revoked first", pname, id)
	case len(p.Domains) == 0:
		return "", "", "", turnAway(http.StatusForbidden, "no principal is bound to %s account %s, and "+
			"%s admits a relayed person only through a binding: it names no domains a first sight "+
			"is admitted from", pname, id, pname)
	case !p.admits(g.ID):
		return "", "", "", turnAway(http.StatusForbidden, "no principal is bound to %s account %s, and "+
			"%s is in none of the domains %s admits a first sight from", pname, id, g.ID, pname)
	}
	if !a.control && p.Acts != actsAny {
		return "", "", "", turnAway(http.StatusForbidden, "a trusted io acts for a person to read, and "+
			"to answer, say and steer; %s %s is more than that, and %s's entry does not say `acts: any`",
			a.method, a.pattern, pname)
	}
	if !found {
		binding := entity.Obj{
			"principal": who, "sub": id, "login": strings.ToLower(g.ID),
			"claimant": pname, "via": io.Principal,
			"proof": entity.Obj{
				"method": "relay", "io": io.Principal, "account": id,
				"at": s.now().UTC().Format(time.RFC3339),
			},
		}
		if code, err := s.bind(sys, pname, binding, who, id); err != nil {
			return "", "", "", turnAway(code, "%v", err)
		}
	}
	return who, pname, io.Principal, nil
}

// relayKey is the context key of the io a request's person was relayed by.
type relayKey struct{}

// relayed marks r as from a person a trusted io relayed.
func relayed(r *http.Request, io string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), relayKey{}, io))
}

// relayedBy is the io a request's person was relayed by, "" where the person asked themselves.
func relayedBy(r *http.Request) string {
	io, _ := r.Context().Value(relayKey{}).(string)
	return io
}
