package versed

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
)

// Identity bindings: which account in an external system is which principal (git-issues
// 9b4azhtjh12kszwyqsn0, hfh7km3th12ksd6nqsn0). Bindings are kept per PROVIDER: the name a deployment
// gives an issuer, since an issuer's URL is a poor address segment. `github` is the first, and its
// way of proving an account is proof by action: the account says a nonce verse gave out, in a
// comment the github io reflects.
//
// AN ACCOUNT IS AN (iss, sub) PAIR, as OIDC models one: the issuer, and the issuer's own id for
// the account, which for GitHub is the user's number. A login can be renamed and is kept as a hint;
// the number is the key, so a binding survives a rename. A sub is whatever its issuer says — some
// are base64 — and it is one address segment whatever characters it holds.
//
// THE PERSON CLAIMS, THE SYSTEM BINDS. A claim is asked for by the person it would bind, and only
// SystemIdentity writes a binding, from evidence it has read itself. Neither lives on the person's
// own records: what a person owns they may write, and a binding they could write is one they could
// forge. Both are the system's, held out to read:
//
//	verse.identity.<provider>.claim.<nonce>   {principal, iss, login, expires, claimant}       to its principal
//	verse.identity.<provider>.account.<sub>   {principal, iss, sub, login, claimant, proof}   to its principal, and agent:*
//
// A BINDING SAYS HOW ITS CLAIMANT WAS AUTHENTICATED: `claimant` is the provider the caller who
// proved the account was authenticated by, or `asserted` for a caller who was whoever their
// request said. A binding is as good as the name it binds to, so it states which it rests on.
//
// ONE ACCOUNT IS ONE PRINCIPAL, refused at verify when it is somebody else's; a principal may hold
// many. The lookup an io needs — a comment's author to a principal — is a read of the account, so an
// io keeps no table of its own.
//
// A VERIFIED ID_TOKEN IS A SECOND VERIFIER, writing the same record (auth.go): what a binding
// means does not depend on how it was proved, and its `proof` says which way that was.
//
// A COMMENT'S EVIDENCE IS AS TRUSTWORTHY AS THE REFLECTION, which is as trustworthy as an io's
// asserted principal: the comment is read under the roots of the ios the deployment trusts for the
// provider (trustRef), where only those ios write, and under no other io's.
const (
	// githubProvider is the provider GitHub accounts are bound under.
	githubProvider = "github"
	githubIssuer   = "https://github.com"
	// claimantAsserted is the claimant of a binding proved by a caller nobody authenticated.
	claimantAsserted = "asserted"
	// claimTTL is how long a nonce stands to be said.
	claimTTL = time.Hour
	// linkWord is what a person says, with the nonce, to prove an account.
	linkWord = "/verse link"
)

// identityRef is a place under verse.identity.<provider>.
func identityRef(provider string, segs ...string) entity.Ref {
	return entity.VerseRef(append([]string{"identity", provider}, segs...)...)
}

func claimRef(provider, nonce string) entity.Ref { return identityRef(provider, "claim", nonce) }
func accountRef(provider, sub string) entity.Ref { return identityRef(provider, "account", sub) }

// trustRef is where a provider's trusted ios stand: `{ios: [<name>…]}`, the installed ios whose
// reflections count as evidence of the provider's accounts. It is written from the deployment's
// config at start (SetAuth) and by nothing else, and is everyone's to read.
func trustRef(provider string) entity.Ref { return identityRef(provider, "trust") }

// writeTrust records the ios a provider trusts, replacing what stood.
func (s *Server) writeTrust(provider string, ios []string) error {
	ref := trustRef(provider)
	grants := []entity.Grant{{At: ref.Segs(), To: map[entity.Level][]string{entity.Read: {entity.Everyone}}}}
	if err := entity.Establish(s.Store, ref, entity.SystemIdentity, entity.Ref{}, grants); err != nil {
		return err
	}
	names := make([]any, len(ios))
	for i, name := range ios {
		names[i] = name
	}
	_, _, err := entity.As(s.Store, entity.SystemIdentity).Write(entity.Write{
		At: ref.At("ios"), Value: entity.Val(names),
	})
	return err
}

// trustedIOs is the installed ios a provider trusts as evidence, by name.
func trustedIOs(sys entity.Store, provider string) (map[string]bool, error) {
	rec, found, err := sys.Get(trustRef(provider))
	if err != nil || !found {
		return nil, err
	}
	out := map[string]bool{}
	for _, name := range entity.Strs(rec.Payload, "ios") {
		out[name] = true
	}
	return out, nil
}

// errNoTrustedIO is what a claim or a verify is refused with where no io is trusted as evidence.
var errNoTrustedIO = errors.New("no io is trusted as evidence of an account under this provider on this " +
	"verse, so none can be proved: the deployment names them in its config (auth.principal.providers.<name>.ios)")

// isAccount reports whether ref is a binding's address, under whichever provider.
func isAccount(ref entity.Ref) bool {
	segs := ref.Segs()
	return len(segs) == 5 && segs[1] == "identity" && segs[3] == "account"
}

// accountClaim is what POST /accounts/<provider> carries: for `github`, the login the caller says
// is theirs; for a provider whose io hears the nonce said, nothing.
type accountClaim struct {
	Login string `tony:"field=login,omitzero"`
}

// accountHeard is what POST /accounts/<provider>/heard carries: a nonce an io heard said, the
// account that said it, and the account's login where the system has one.
type accountHeard struct {
	Nonce   string `tony:"field=nonce"`
	Account string `tony:"field=account"`
	Login   string `tony:"field=login,omitzero"`
}

// accountVerify is what POST /accounts/github/verify carries: the nonce the account has said.
type accountVerify struct {
	Nonce string `tony:"field=nonce"`
}

// accountLink is what POST /accounts/<provider>/link carries: an id_token from that provider, as
// evidence that the caller holds the account it names.
type accountLink struct {
	IDToken string `tony:"field=idToken"`
}

// claimAccount gives the caller a nonce to say from the account they claim, and keeps the claim
// until it is verified or expires. The claim is under a provider whose accounts are proved by an
// io's word: `github`, where the login is named and the proof is a reflected comment (verify), or
// any provider whose io reports the nonce said from an account (heard).
func (s *Server) claimAccount(w http.ResponseWriter, r *http.Request) {
	var body accountClaim
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	who, ok := actor(w, s.scopedStore(r), "")
	if !ok {
		return
	}
	provider := r.PathValue("provider")
	login := strings.TrimPrefix(strings.TrimSpace(body.Login), "@")
	if login == "" && provider == githubProvider {
		http.Error(w, "a claim names the GitHub login it claims", http.StatusBadRequest)
		return
	}
	// A provider whose accounts an io proves has a trust record, the ios it names (writeTrust);
	// an issuer has none, and its accounts are proved by its tokens. Refused before a nonce is
	// given out, so nobody says one where no io's word would be taken. (The verify route below is
	// `github`'s alone; another provider's nonce is reported heard.)
	trusted, err := trustedIOs(entity.As(s.Store, entity.SystemIdentity), provider)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if trusted == nil {
		http.Error(w, fmt.Sprintf("%q is no provider whose accounts an io proves on this verse "+
			"(auth.principal.providers.<name>.ios)", provider), http.StatusNotFound)
		return
	}
	if len(trusted) == 0 {
		http.Error(w, errNoTrustedIO.Error(), http.StatusConflict)
		return
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nonce := hex.EncodeToString(b[:])
	expires := time.Now().UTC().Add(claimTTL).Format(time.RFC3339)
	ref := claimRef(provider, nonce)
	grants := []entity.Grant{{At: ref.Segs(), To: map[entity.Level][]string{entity.Read: {who}}}}
	if err := entity.Establish(s.Store, ref, entity.SystemIdentity, entity.Ref{}, grants); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	rec := entity.Obj{"principal": who, "expires": expires, "claimant": authenticatedBy(r)}
	if provider == githubProvider {
		rec["iss"], rec["login"] = githubIssuer, login
	} else if login != "" {
		rec["login"] = login
	}
	if _, _, err := entity.As(s.Store, entity.SystemIdentity).Write(entity.Write{
		At: ref.At(), Value: rec.Payload(), When: []entity.Require{entity.Created()},
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out := map[string]any{
		"nonce": nonce, "expires": expires,
		"say": fmt.Sprintf("%s %s", linkWord, nonce),
	}
	if login != "" {
		out["login"] = login
	}
	writeOut(w, r, http.StatusCreated, out)
}

// heardAccount binds a claimed account from an io's word that the account said the nonce: the
// Slack io hearing `/verse link <nonce>` in a message from a member, the github io reading it in
// a comment it reflected. The io is verified by its own token and trusted for the provider, which
// is the same word the relay takes for a person (iod.go); the binding's claimant is whoever
// claimed, as they were authenticated then. A claim that names a login (github) is said from
// that login or not at all: somebody else saying the nonce binds nothing, and the claim stands.
// A claim that names none (slack) is bound to whoever says it, since the nonce is the only
// thing that was given out: it is said from one's own account, as any credential is used. A
// nonce nobody claimed is not confirmed to whoever says it.
func (s *Server) heardAccount(w http.ResponseWriter, r *http.Request) {
	var body accountHeard
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	provider := r.PathValue("provider")
	io, ok := s.ioOf(principalOf(r))
	if authenticatedBy(r) != iodProvider || !ok {
		http.Error(w, "a nonce heard is reported by an io the verse verified (auth.iod), as itself", http.StatusForbidden)
		return
	}
	p := s.providerNamed(provider)
	if p == nil || len(p.IOs) == 0 {
		http.Error(w, fmt.Sprintf("%q is no provider whose accounts an io proves on this verse "+
			"(auth.principal.providers.<name>.ios)", provider), http.StatusNotFound)
		return
	}
	if io.IO == "" || !slices.Contains(p.IOs, io.IO) {
		http.Error(w, fmt.Sprintf("%s is not an io this verse trusts for %s accounts "+
			"(auth.principal.providers.%s.ios names %s)", io.Principal, provider, provider,
			strings.Join(p.IOs, ", ")), http.StatusForbidden)
		return
	}
	account := strings.TrimSpace(strings.TrimPrefix(body.Account, provider+":"))
	if account == "" || strings.TrimSpace(body.Nonce) == "" {
		http.Error(w, "a nonce heard names the nonce and the account that said it", http.StatusBadRequest)
		return
	}
	sys := entity.As(s.Store, entity.SystemIdentity)
	cref := claimRef(provider, strings.TrimSpace(body.Nonce))
	claim, found, err := sys.Get(cref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !found {
		http.Error(w, "no claim has that nonce", http.StatusNotFound)
		return
	}
	if exp, err := time.Parse(time.RFC3339, entity.Str(claim.Payload, "expires")); err != nil || time.Now().After(exp) {
		_, _, _ = sys.Write(entity.Write{At: cref.At()})
		http.Error(w, "the claim has expired: claim the account again for a new nonce", http.StatusGone)
		return
	}
	if claimed := entity.Str(claim.Payload, "login"); claimed != "" && !strings.EqualFold(claimed, strings.TrimPrefix(body.Login, "@")) {
		http.Error(w, fmt.Sprintf("the claim names the %s login %s, and the nonce was said from %q: a "+
			"nonce is said from the account it claims", provider, claimed, body.Login), http.StatusForbidden)
		return
	}
	who := entity.Str(claim.Payload, "principal")
	binding := entity.Obj{
		"principal": who, "sub": account, "claimant": entity.Str(claim.Payload, "claimant"),
		"via": io.Principal,
		"proof": entity.Obj{
			"method": "heard", "io": io.Principal, "account": account,
			"at": time.Now().UTC().Format(time.RFC3339),
		},
	}
	if login := entity.Str(claim.Payload, "login"); login != "" {
		binding["login"] = strings.ToLower(login) // a hint, kept as the relay keeps one
	}
	if iss := entity.Str(claim.Payload, "iss"); iss != "" {
		binding["iss"] = iss
	}
	code, err := s.bind(sys, provider, binding, who, account)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	_, _, _ = sys.Write(entity.Write{At: cref.At()})
	writeOut(w, r, code, accountRow(provider, binding.Payload()))
}

// verifyAccount looks for the claimed account having said the nonce, and binds the account to the
// claim's principal when it has. The claim is spent either way but one: a comment not reflected
// yet leaves it standing, to be asked again.
func (s *Server) verifyAccount(w http.ResponseWriter, r *http.Request) {
	var body accountVerify
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	who, ok := actor(w, s.scopedStore(r), "")
	if !ok {
		return
	}
	sys := entity.As(s.Store, entity.SystemIdentity)
	// A claim somebody else made is not found, as a claim nobody made is not: a nonce is not
	// confirmed to whoever guesses it.
	cref := claimRef(githubProvider, strings.TrimSpace(body.Nonce))
	claim, found, err := sys.Get(cref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !found || entity.Str(claim.Payload, "principal") != who {
		http.Error(w, "no claim of yours has that nonce", http.StatusNotFound)
		return
	}
	if exp, err := time.Parse(time.RFC3339, entity.Str(claim.Payload, "expires")); err != nil || time.Now().After(exp) {
		_, _, _ = sys.Write(entity.Write{At: cref.At()})
		http.Error(w, "the claim has expired: claim the account again for a new nonce", http.StatusGone)
		return
	}
	login := entity.Str(claim.Payload, "login")
	evidence, err := s.linkComment(sys, login, cref.Name())
	if errors.Is(err, errNoTrustedIO) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if evidence == nil {
		http.Error(w, fmt.Sprintf("no comment by %s saying %q is reflected yet: say it on a "+
			"repository the github io reflects, and verify again once it has been", login,
			linkWord+" "+cref.Name()), http.StatusNotFound)
		return
	}
	sub := strconv.FormatInt(*ir.Uncomment(entity.Get(evidence.Payload, "authorId")).Int64, 10)
	binding := entity.Obj{
		"principal": who, "iss": githubIssuer, "sub": sub,
		"login":    entity.Str(evidence.Payload, "author"),
		"claimant": authenticatedBy(r),
		"proof": entity.Obj{
			"method": "comment", "evidence": evidence.Ref.String(),
			"at": time.Now().UTC().Format(time.RFC3339),
		},
	}
	code, err := s.bind(sys, githubProvider, binding, who, sub)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	_, _, _ = sys.Write(entity.Write{At: cref.At()})
	writeOut(w, r, code, accountRow(githubProvider, binding.Payload()))
}

// bind writes an account's binding to who, unless it is bound already: to who, which is the same
// binding proved again, or to somebody else, which is refused.
//
// PROVED AGAIN BY A CALLER A PROVIDER VERIFIED, AN ASSERTED BINDING BECOMES THAT PROVIDER'S: the
// record takes the new claimant and proof. The other way round changes nothing, since a caller
// nobody verified proves nothing a verified one had not.
//
// THE VALUE BEFORE THE RECORD. A binding with no record of its own is the system's and nobody
// else's to read, so the value is written first under an absence precondition, and the record that
// holds it out goes to whichever writer's value landed.
func (s *Server) bind(sys entity.Store, provider string, binding entity.Obj, who, sub string) (int, error) {
	ref := accountRef(provider, sub)
	committed, _, err := sys.Write(entity.Write{
		At: ref.At(), Value: binding.Payload(), When: []entity.Require{entity.Created()},
	})
	if err != nil {
		return http.StatusBadGateway, err
	}
	if !committed {
		cur, found, err := sys.Get(ref)
		if err != nil {
			return http.StatusBadGateway, err
		}
		if found && entity.Str(cur.Payload, "principal") != who {
			return http.StatusConflict, fmt.Errorf("%s account %s is bound to another principal: "+
				"one account is one principal, so that binding is revoked first", provider, sub)
		}
		if found && entity.Str(cur.Payload, "claimant") == claimantAsserted && binding["claimant"] != claimantAsserted {
			if _, _, err := sys.Write(entity.Write{
				At: ref.At(), Value: binding.Payload(), When: []entity.Require{{Pattern: entity.Unchanged(cur.Payload)}},
			}); err != nil {
				return http.StatusBadGateway, err
			}
		}
		return http.StatusOK, nil
	}
	grants := []entity.Grant{{At: ref.Segs(), To: map[entity.Level][]string{entity.Read: {who, "agent:*"}}}}
	if err := entity.Establish(s.Store, ref, entity.SystemIdentity, entity.Ref{}, grants); err != nil {
		_, _, _ = sys.Write(entity.Write{At: ref.At()})
		return http.StatusBadGateway, err
	}
	return http.StatusCreated, nil
}

// linkAccount binds a second provider's account to the caller, from an id_token of that provider
// the caller presents as evidence. It needs no nonce: the caller is somebody a provider has
// verified, and the token proves they hold the account. A caller who is only whoever their
// request said links nothing this way, since the binding would let the account's token act as a
// name nobody proved.
func (s *Server) linkAccount(w http.ResponseWriter, r *http.Request) {
	var body accountLink
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	who, ok := actor(w, s.scopedStore(r), "")
	if !ok {
		return
	}
	by := authenticatedBy(r)
	if by == claimantAsserted {
		http.Error(w, "an account is linked by a caller an identity provider has verified: log in "+
			"(`verse login`), then link", http.StatusForbidden)
		return
	}
	v, refused := s.verifyToken(r.Context(), strings.TrimSpace(body.IDToken))
	if refused != nil {
		// The request was admitted; what failed is the evidence in its body.
		code := refused.code
		if code == http.StatusUnauthorized {
			code = http.StatusBadRequest
		}
		http.Error(w, "the evidence: "+refused.msg, code)
		return
	}
	provider := r.PathValue("provider")
	if v.provider.iod {
		// A workload token names an io, which is config and never a person's account (iod.go).
		http.Error(w, "the evidence is a workload token of the ios' issuer, which binds no person's account", http.StatusBadRequest)
		return
	}
	if v.provider.name != provider {
		http.Error(w, fmt.Sprintf("the evidence is a token of %s, not of %s", v.provider.name, provider), http.StatusBadRequest)
		return
	}
	binding := oidcBinding(v, who, by)
	code, err := s.bind(entity.As(s.Store, entity.SystemIdentity), provider, binding, who, v.sub)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	writeOut(w, r, code, accountRow(provider, binding.Payload()))
}

// oidcBinding is the binding of the account a verified id_token names to who, proved by that
// token: its iss, sub and iat are kept as the evidence, and never the token.
func oidcBinding(v *verified, who, claimant string) entity.Obj {
	p := v.provider
	b := entity.Obj{
		"principal": who, "iss": p.Issuer, "sub": v.sub, "claimant": claimant,
		"proof": entity.Obj{
			"method": "oidc", "iss": p.Issuer, "sub": v.sub,
			"iat": v.issuedAt.UTC().Format(time.RFC3339),
			"at":  time.Now().UTC().Format(time.RFC3339),
		},
	}
	if v.email != "" {
		b["login"] = strings.ToLower(v.email)
	}
	return b
}

// linkComment is the reflected comment in which login says the nonce, nil where none is. It is
// read under the roots of the ios the provider trusts (trustRef) and no others, at the github io's
// comment places, `<root>.<org>.<name>.comment.<number>.<id>`, and counts only a comment that
// carries its author's number, since that number is what the binding keys on.
func (s *Server) linkComment(sys entity.Store, login, nonce string) (*entity.Entity, error) {
	trusted, err := trustedIOs(sys, githubProvider)
	if err != nil {
		return nil, err
	}
	if len(trusted) == 0 {
		return nil, errNoTrustedIO
	}
	ios, err := verseio.ListErr(sys)
	if err != nil {
		return nil, err
	}
	said := linkWord + " " + nonce
	for _, io := range ios {
		if io.Root == "" || !trusted[io.Name] {
			continue
		}
		ents, err := entity.BelowErr(sys, func(ref entity.Ref) bool {
			segs := ref.Segs()
			return len(segs) == 6 && segs[3] == "comment"
		}, 5, io.Root)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			author := entity.Str(e.Payload, "author")
			id := ir.Uncomment(entity.Get(e.Payload, "authorId"))
			if !strings.EqualFold(author, login) || id == nil || id.Int64 == nil || *id.Int64 <= 0 {
				continue
			}
			if !strings.HasPrefix(entity.Str(e.Payload, "url"), githubIssuer+"/") {
				continue // a comment that is not GitHub's is no evidence of a GitHub account
			}
			if saysWord(entity.Str(e.Payload, "body"), said) {
				return &e, nil
			}
		}
	}
	return nil, nil
}

// saysWord reports whether body says said as a line of its own, so a nonce quoted inside other
// words — somebody pasting another person's instructions — is not the account saying it.
func saysWord(body, said string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == said {
			return true
		}
	}
	return false
}

// listAccounts is the caller's bindings, under every provider.
func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	who := entity.PrincipalOf(store)
	ents, err := entity.BelowErr(store, isAccount, 3, entity.VerseRoot, "identity")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out := make([]map[string]any, 0)
	for _, e := range ents {
		if entity.Str(e.Payload, "principal") == who {
			out = append(out, accountRow(e.Ref.Segs()[2], e.Payload))
		}
	}
	writeOut(w, r, http.StatusOK, out)
}

// whoIsAccount answers which principal a provider's account is, by the issuer's id for it (`sub`,
// GitHub's number) or its login, as far as the caller may read the binding: its principal and
// agent:*, so an io answers as the person who wrote a comment without keeping a table of its own.
// Under the mode that admits people only verified, an `asserted` binding is not answered.
func (s *Server) whoIsAccount(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	provider := r.PathValue("provider")
	sub, login := r.URL.Query().Get("sub"), r.URL.Query().Get("login")
	if (sub == "") == (login == "") {
		http.Error(w, "ask by sub or by login, one of the two", http.StatusBadRequest)
		return
	}
	var ent *entity.Entity
	if sub != "" {
		e, found, err := store.Get(accountRef(provider, sub))
		if errors.Is(err, entity.ErrForbidden) {
			found, err = false, nil
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if found {
			ent = &e
		}
	} else {
		ents, err := entity.BelowErr(store, nil, 1, identityRef(provider, "account").Segs()...)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for i := range ents {
			if strings.EqualFold(entity.Str(ents[i].Payload, "login"), strings.TrimPrefix(login, "@")) {
				ent = &ents[i]
				break
			}
		}
	}
	// Where people are verified, a binding proved by a caller nobody verified does not say who an
	// account is: it is skipped, as if it were not there, until a verified caller proves it again.
	if ent != nil && s.verifiesPeople() && entity.Str(ent.Payload, "claimant") == claimantAsserted {
		ent = nil
	}
	if ent == nil {
		http.Error(w, "no binding of that account is yours to read", http.StatusNotFound)
		return
	}
	writeOut(w, r, http.StatusOK, accountRow(provider, ent.Payload))
}

// revokeAccount removes the caller's own binding of an account.
func (s *Server) revokeAccount(w http.ResponseWriter, r *http.Request) {
	who, ok := actor(w, s.scopedStore(r), "")
	if !ok {
		return
	}
	sys := entity.As(s.Store, entity.SystemIdentity)
	ref := accountRef(r.PathValue("provider"), r.PathValue("sub"))
	cur, found, err := sys.Get(ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !found || entity.Str(cur.Payload, "principal") != who {
		http.Error(w, "no binding of yours is that account", http.StatusNotFound)
		return
	}
	if _, _, err := sys.Write(entity.Write{
		At: ref.At(), When: []entity.Require{{Pattern: entity.Unchanged(cur.Payload)}},
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// accountRow is a binding as the routes answer it, with the provider its address names.
func accountRow(provider string, p entity.Payload) map[string]any {
	row := map[string]any{"provider": provider}
	for _, k := range []string{"principal", "iss", "sub", "login", "claimant", "via"} {
		if v := entity.Str(p, k); v != "" {
			row[k] = v
		}
	}
	// A proof holds what its method kept: a comment's address, a token's iss, sub and iat, or the
	// io and account a person was relayed from or heard by.
	if proof := entity.Get(p, "proof"); proof != nil {
		shown := map[string]any{}
		for _, k := range []string{"method", "evidence", "iss", "sub", "iat", "io", "account", "at"} {
			if v := entity.Str(proof, k); v != "" {
				shown[k] = v
			}
		}
		row["proof"] = shown
	}
	return row
}
