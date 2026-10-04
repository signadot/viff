package versed

import (
	"fmt"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/versed/idptest"
)

// A request carrying an id_token from a provider the deployment names is whoever that account is
// bound to, verified at the door, ahead of the header.

const (
	authClient   = "verse-cli"
	authProvName = "corp"
	authDomain   = "example.com"
)

// authServer is a server that takes id_tokens from one fake IdP, named `corp`, admitting first
// sights from example.com.
func authServer(t *testing.T, mode string) (*Server, *httptest.Server, *idptest.IdP) {
	t.Helper()
	idp := idptest.New(t)
	srv := testServer(t)
	if err := srv.SetAuth(Auth{Mode: mode, Providers: map[string]AuthProvider{
		authProvName: {Issuer: idp.URL, ClientID: authClient, Domains: []string{authDomain}},
	}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, idp
}

// person is the claims of a token whose account has a verified email.
func person(email string) map[string]any {
	return map[string]any{"email": email, "email_verified": true}
}

// bearer makes one request carrying a token, and a principal header too where as is not empty.
// It answers the status and the body.
func bearer(t *testing.T, method, url, token, as, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/tony")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if as != "" {
		req.Header.Set(api.PrincipalHeader, as)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := stdio.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

// entityPut is the body of a raw put of one entity.
func entityPut(t *testing.T, at string) string {
	t.Helper()
	b, err := codec.Marshal(api.PutEntity{At: at, Payload: entity.Obj{"stage": "open"}.Payload()}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAFirstSightBindsAndTheWriteIsThePersons(t *testing.T) {
	srv, ts, idp := authServer(t, AuthOptional)
	token := idp.Token(authClient, "sub-ana", person("Ana@Example.com"))

	if code, body := bearer(t, http.MethodPost, ts.URL+"/entity", token, "", entityPut(t, "demo.pr.1")); code/100 != 2 {
		t.Fatalf("put with a bearer = %d %s", code, body)
	}
	if got := owner(t, srv, entity.RefAt("demo")); got != idAna {
		t.Fatalf("owner of what the bearer wrote = %q, want %s", got, idAna)
	}
	if _, ok, err := srv.Store.Get(entity.UserRef("ana@example.com")); err != nil || !ok {
		t.Errorf("ana is not on the list of users (ok=%v err=%v)", ok, err)
	}

	// The binding is the system's, says which provider vouched for its claimant, and keeps the
	// token's iss, sub and iat as its evidence and nothing else of the token.
	rec, found, err := entity.As(srv.Store, entity.SystemIdentity).Get(accountRef(authProvName, "sub-ana"))
	if err != nil || !found {
		t.Fatalf("no binding after a first sight (found=%v err=%v)", found, err)
	}
	proof := entity.Get(rec.Payload, "proof")
	if entity.Str(rec.Payload, "principal") != idAna || entity.Str(rec.Payload, "iss") != idp.URL ||
		entity.Str(rec.Payload, "claimant") != authProvName || entity.Str(proof, "method") != "oidc" ||
		entity.Str(proof, "sub") != "sub-ana" || entity.Str(proof, "iat") == "" {
		t.Errorf("binding = %v", rec.Payload)
	}
	rendered, err := codec.Marshal(rec.Payload, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), token) || strings.Contains(string(rendered), strings.Split(token, ".")[2]) {
		t.Error("the binding holds the token")
	}
	if err := write(srv.Store, idAna, accountRef(authProvName, "sub-ana"), entity.Obj{"principal": idBo}); err == nil {
		t.Error("a person rewrote the binding a first sight made")
	}

	// Their email changes at the provider, to one no domain admits: the account still finds them.
	moved := idp.Token(authClient, "sub-ana", person("ana@elsewhere.org"))
	if code, body := bearer(t, http.MethodPost, ts.URL+"/entity", moved, "", entityPut(t, "demo.pr.2")); code/100 != 2 {
		t.Fatalf("put after the email changed = %d %s", code, body)
	}
	if ok, err := entity.Holds(entity.As(srv.Store, entity.SystemDefault), idAna, entity.OwnerLevel, entity.RefAt("demo", "pr", "2")); err != nil || !ok {
		t.Errorf("what the account wrote after its email changed is not ana's: %v %v", ok, err)
	}
	code, body := bearer(t, http.MethodGet, ts.URL+"/accounts?format=tony", moved, "", "")
	var rows []map[string]any
	if err := codec.Unmarshal([]byte(body), &rows, codec.Tony); err != nil || code != http.StatusOK {
		t.Fatalf("accounts = %d %s (%v)", code, body, err)
	}
	if len(rows) != 1 || rows[0]["principal"] != idAna || rows[0]["provider"] != authProvName || rows[0]["claimant"] != authProvName {
		t.Errorf("ana's accounts = %v", rows)
	}
}

func TestABearerThatDoesNotProveItsCallerIsRefused(t *testing.T) {
	srv, ts, idp := authServer(t, AuthOptional)
	other := idptest.New(t)
	hour := time.Hour

	for _, c := range []struct {
		what  string
		token string
		want  string
	}{
		{"an email the provider has not verified",
			idp.Token(authClient, "s1", map[string]any{"email": "ana@example.com", "email_verified": false}), "has not verified"},
		{"no email",
			idp.Token(authClient, "s2", nil), "carries no email"},
		{"a domain outside the provider's",
			idp.Token(authClient, "s3", person("ana@elsewhere.org")), "in none of them"},
		{"another client's token",
			idp.Token("somebody-else", "s4", person("ana@example.com")), "audience"},
		{"an expired token",
			idp.Token(authClient, "s5", map[string]any{"email": "ana@example.com", "email_verified": true,
				"iat": time.Now().Add(-2 * hour).Unix(), "exp": time.Now().Add(-hour).Unix()}), "expired"},
		{"a token not yet in date",
			idp.Token(authClient, "s6", map[string]any{"email": "ana@example.com", "email_verified": true,
				"nbf": time.Now().Add(hour).Unix()}), "nbf"},
		{"a signature by a key the provider does not publish",
			idp.Forged(authClient, "s7", person("ana@example.com")), "did not verify"},
		{"an issuer this verse does not name",
			other.Token(authClient, "s8", person("ana@example.com")), "no identity provider of this verse"},
		{"alg none",
			idp.Unsigned(authClient, "s9", person("ana@example.com")), "did not verify"},
		{"no account",
			idp.Token(authClient, "", person("ana@example.com")), "no account"},
		{"something that is no token",
			"not-a-jwt", "not an id_token"},
	} {
		code, body := bearer(t, http.MethodPost, ts.URL+"/entity", c.token, "", entityPut(t, "demo.pr.1"))
		if code != http.StatusUnauthorized || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %q, want 401 saying %q", c.what, code, strings.TrimSpace(body), c.want)
		}
		// And it does not fall back to a header beside it.
		if code, _ := bearer(t, http.MethodPost, ts.URL+"/entity", c.token, idAna, entityPut(t, "demo.pr.1")); code != http.StatusUnauthorized {
			t.Errorf("%s, with a header beside it: %d, want 401", c.what, code)
		}
	}
	if _, ok, _ := srv.Store.Get(entity.RefAt("demo", "pr", "1")); ok {
		t.Error("a refused bearer's write stands")
	}
	ents, err := entity.BelowErr(entity.As(srv.Store, entity.SystemIdentity), isAccount, 3, entity.VerseRoot, "identity")
	if err != nil || len(ents) != 0 {
		t.Errorf("refused bearers left bindings: %v (%v)", ents, err)
	}
}

func TestABearerBesideAnotherNameIsRefused(t *testing.T) {
	_, ts, idp := authServer(t, AuthOptional)
	token := idp.Token(authClient, "sub-ana", person("ana@example.com"))

	if code, body := bearer(t, http.MethodPost, ts.URL+"/entity", token, idBo, entityPut(t, "demo.pr.1")); code != http.StatusForbidden {
		t.Fatalf("ana's bearer beside bo's name = %d %s, want 403", code, body)
	}
	// Naming the same principal is not naming another, however it is spelled.
	for _, as := range []string{idAna, "ana@example.com"} {
		if code, body := bearer(t, http.MethodPost, ts.URL+"/entity", token, as, entityPut(t, "demo.pr.1")); code/100 != 2 {
			t.Errorf("ana's bearer beside %q = %d %s", as, code, body)
		}
	}
}

func TestWithNoBearerTheHeaderIsAdmittedUnderOptional(t *testing.T) {
	srv, ts, _ := authServer(t, AuthOptional)
	if code, body := bearer(t, http.MethodPost, ts.URL+"/entity", "", idBo, entityPut(t, "demo.pr.1")); code/100 != 2 {
		t.Fatalf("put with a header and no bearer = %d %s", code, body)
	}
	if got := owner(t, srv, entity.RefAt("demo")); got != idBo {
		t.Errorf("owner = %q, want %s", got, idBo)
	}
}

// A verse that names no provider verifies no bearer, and says so: served as nobody, the request
// would be answered with what an empty verse answers.
func TestABearerAtAVerseThatVerifiesNoneIsRefused(t *testing.T) {
	idp := idptest.New(t)
	token := idp.Token(authClient, "sub-ana", person("ana@example.com"))

	bare := testServer(t)
	off := testServer(t)
	if err := off.SetAuth(Auth{Mode: AuthOff, Providers: map[string]AuthProvider{
		authProvName: {Issuer: idp.URL, ClientID: authClient, Domains: []string{authDomain}},
	}}); err != nil {
		t.Fatal(err)
	}
	for what, srv := range map[string]*Server{"no auth": bare, "mode off": off} {
		ts := httptest.NewServer(srv.Handler())
		for _, as := range []string{"", idAna} {
			if code, body := bearer(t, http.MethodGet, ts.URL+"/accounts", token, as, ""); code != http.StatusUnauthorized || !strings.Contains(body, "verifies no bearer") {
				t.Errorf("%s, header %q: %d %q, want 401", what, as, code, strings.TrimSpace(body))
			}
		}
		ts.Close()
	}
}

// A provider's keys rotate: a token signed with a key the verifier has not seen is checked against
// the key set read again.
func TestARotatedKeyIsRefetched(t *testing.T) {
	_, ts, idp := authServer(t, AuthOptional)
	before := idp.Token(authClient, "sub-ana", person("ana@example.com"))
	if code, body := bearer(t, http.MethodGet, ts.URL+"/accounts", before, "", ""); code != http.StatusOK {
		t.Fatalf("before the rotation = %d %s", code, body)
	}
	fetched := idp.JWKSFetches()

	idp.Rotate()
	after := idp.Token(authClient, "sub-ana", person("ana@example.com"))
	if code, body := bearer(t, http.MethodGet, ts.URL+"/accounts", after, "", ""); code != http.StatusOK {
		t.Fatalf("a token signed with the rotated key = %d %s", code, body)
	}
	if idp.JWKSFetches() <= fetched {
		t.Error("the key set was not read again for a kid nobody had seen")
	}
	// The key that was rotated out no longer verifies.
	if code, _ := bearer(t, http.MethodGet, ts.URL+"/accounts", before, "", ""); code != http.StatusUnauthorized {
		t.Errorf("a token signed with the key rotated out = %d, want 401", code)
	}
}

// A provider is discovered when a token first names it, so one that is down when versed starts
// stops only the requests that need it, and is found once it is back.
func TestAProviderThatIsDownStopsOnlyItsOwnTokens(t *testing.T) {
	idp := idptest.New(t)
	// A second issuer whose discovery answers 500 until it is told otherwise.
	up := false
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/.well-known/openid-configuration" {
			idptest.WriteJSON(w, http.StatusOK, map[string]any{
				"issuer": "http://" + r.Host, "jwks_uri": idp.URL + "/keys",
				"authorization_endpoint": idp.URL + "/authorize", "token_endpoint": idp.URL + "/token",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer gate.Close()

	srv := testServer(t)
	if err := srv.SetAuth(Auth{Mode: AuthOptional, Providers: map[string]AuthProvider{
		authProvName: {Issuer: gate.URL, ClientID: authClient, Domains: []string{authDomain}},
	}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	slow := idp.Token(authClient, "sub-ana", map[string]any{"iss": gate.URL, "email": "ana@example.com", "email_verified": true})
	if code, body := bearer(t, http.MethodGet, ts.URL+"/accounts", slow, "", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("a token of a provider that is down = %d %s, want 503", code, body)
	}
	// A request that carries no bearer is served meanwhile.
	if code, _ := bearer(t, http.MethodGet, ts.URL+"/accounts", "", idBo, ""); code != http.StatusOK {
		t.Errorf("a request with no bearer while the provider is down = %d", code)
	}
	up = true
	srv.auth.provider(gate.URL).failed = time.Time{} // the wait before asking again, skipped
	if code, body := bearer(t, http.MethodGet, ts.URL+"/accounts", slow, "", ""); code != http.StatusOK {
		t.Fatalf("once the provider is back = %d %s", code, body)
	}
}

// A binding is of one issuer's account. A provider's name pointed at another issuer does not
// inherit the accounts bound under the name before.
func TestABindingOfAnotherIssuerIsNotTheTokens(t *testing.T) {
	srv, ts, idp := authServer(t, AuthOptional)
	sys := entity.As(srv.Store, entity.SystemIdentity)
	old := entity.Obj{"principal": idBo, "iss": "https://old.example.com", "sub": "sub-1", "claimant": authProvName}
	if code, err := srv.bind(sys, authProvName, old, idBo, "sub-1"); err != nil || code != http.StatusCreated {
		t.Fatalf("bind = %d %v", code, err)
	}
	token := idp.Token(authClient, "sub-1", person("ana@example.com"))
	if code, body := bearer(t, http.MethodGet, ts.URL+"/accounts", token, "", ""); code != http.StatusUnauthorized || !strings.Contains(body, "https://old.example.com") {
		t.Errorf("a token whose sub is bound under another issuer = %d %q, want 401", code, strings.TrimSpace(body))
	}
}

func TestAuthRefusesWhatItCannotServe(t *testing.T) {
	srv := testServer(t)
	p := AuthProvider{Issuer: "https://idp.example.com", ClientID: "c"}
	for what, a := range map[string]Auth{
		"an unknown mode":             {Mode: "sometimes"},
		"an issuer named github":      {Mode: AuthOptional, Providers: map[string]AuthProvider{githubProvider: p}},
		"ios on an issuer":            {Mode: AuthOptional, Providers: map[string]AuthProvider{"a": {Issuer: p.Issuer, ClientID: "c", IOs: []string{"gh"}}}},
		"required with no issuer":     {Mode: AuthRequired, Providers: map[string]AuthProvider{githubProvider: {IOs: []string{"gh"}}}},
		"a provider with no client":   {Mode: AuthOptional, Providers: map[string]AuthProvider{"a": {Issuer: p.Issuer}}},
		"two providers of one issuer": {Mode: AuthOptional, Providers: map[string]AuthProvider{"a": p, "b": p}},
	} {
		if err := srv.SetAuth(a); err == nil {
			t.Errorf("%s was taken", what)
		}
	}
}

// GET /whoami says who the request is from and how versed knows: the provider that verified them,
// `asserted` for a name taken as given, and neither for a request that names nobody.
func TestWhoamiSaysHowTheCallerIsKnown(t *testing.T) {
	_, ts, idp := authServer(t, AuthOptional)
	token := idp.Token(authClient, "sub-ana", person("ana@example.com"))
	for _, c := range []struct {
		what, token, as string
		want            map[string]any
	}{
		{"a verified bearer", token, "", map[string]any{"principal": idAna, "by": authProvName}},
		{"a name", "", idBo, map[string]any{"principal": idBo, "by": claimantAsserted}},
		{"nobody", "", "", map[string]any{}},
	} {
		code, body := bearer(t, http.MethodGet, ts.URL+"/whoami?format=tony", c.token, c.as, "")
		got := map[string]any{}
		if err := codec.Unmarshal([]byte(body), &got, codec.Tony); err != nil || code != http.StatusOK {
			t.Fatalf("%s: %d %s (%v)", c.what, code, body, err)
		}
		if len(got) != len(c.want) || got["principal"] != c.want["principal"] || got["by"] != c.want["by"] {
			t.Errorf("%s: whoami = %v, want %v", c.what, got, c.want)
		}
	}
}

// linkServer is a server that takes tokens from two fake IdPs, `corp` and `okta`, with a github io
// registered, so a caller verified by one can prove accounts elsewhere. Only corp admits a first
// sight.
func linkServer(t *testing.T) (*Server, *httptest.Server, *idptest.IdP, *idptest.IdP) {
	t.Helper()
	corp, okta := idptest.New(t), idptest.New(t)
	srv, ts := identityServer(t)
	if err := srv.SetAuth(Auth{Mode: AuthOptional, Providers: map[string]AuthProvider{
		authProvName:   {Issuer: corp.URL, ClientID: authClient, Domains: []string{authDomain}},
		"okta":         {Issuer: okta.URL, ClientID: authClient},
		githubProvider: {IOs: []string{"gh"}},
	}}); err != nil {
		t.Fatal(err)
	}
	return srv, ts, corp, okta
}

// tonyOf reads a response's body as one document.
func tonyOf(t *testing.T, body string) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := codec.Unmarshal([]byte(body), &out, codec.Tony); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return out
}

func TestAnAuthenticatedCallerLinksASecondProvidersAccount(t *testing.T) {
	srv, ts, corp, okta := linkServer(t)
	ana := corp.Token(authClient, "sub-ana", person("ana@example.com"))
	bo := corp.Token(authClient, "sub-bo", person("bo@example.com"))
	// The okta account has no email verse would admit at first sight: on its own it is nobody.
	evidence := okta.Token(authClient, "okta-7", nil)
	link := ts.URL + "/accounts/okta/link?format=tony"
	body := `{idToken: "` + evidence + `"}`

	if code, _ := bearer(t, http.MethodGet, ts.URL+"/whoami", evidence, "", ""); code != http.StatusUnauthorized {
		t.Fatalf("the okta token before it is linked = %d, want 401", code)
	}
	// A caller nobody verified links nothing: the binding would let the token act as a name
	// nobody proved.
	if code, out := bearer(t, http.MethodPost, link, "", idAna, body); code != http.StatusForbidden {
		t.Fatalf("a link by an asserted caller = %d %s, want 403", code, out)
	}
	code, out := bearer(t, http.MethodPost, link, ana, "", body)
	row := tonyOf(t, out)
	if code != http.StatusCreated || row["principal"] != idAna || row["sub"] != "okta-7" || row["claimant"] != authProvName {
		t.Fatalf("link = %d %v, want 201 binding okta-7 to ana, claimant %s", code, row, authProvName)
	}
	if strings.Contains(out, evidence) {
		t.Error("the link answered with the token")
	}
	// From then on the okta token is ana.
	code, out = bearer(t, http.MethodGet, ts.URL+"/whoami?format=tony", evidence, "", "")
	if who := tonyOf(t, out); code != http.StatusOK || who["principal"] != idAna || who["by"] != "okta" {
		t.Errorf("whoami with the linked account's token = %d %v", code, who)
	}
	// Linked again by the same person it is the same binding; by somebody else it is refused.
	if code, _ := bearer(t, http.MethodPost, link, ana, "", body); code != http.StatusOK {
		t.Errorf("the same link again = %d, want 200", code)
	}
	if code, _ := bearer(t, http.MethodPost, link, bo, "", body); code != http.StatusConflict {
		t.Errorf("a second principal's link of the account = %d, want 409", code)
	}
	rec, _, _ := entity.As(srv.Store, entity.SystemIdentity).Get(accountRef("okta", "okta-7"))
	if entity.Str(rec.Payload, "principal") != idAna {
		t.Errorf("the account is bound to %q after a refused link", entity.Str(rec.Payload, "principal"))
	}

	// The evidence is a token of the provider the route names, and one that verifies.
	for what, c := range map[string]struct{ url, token string }{
		"a token of another provider":  {ts.URL + "/accounts/okta/link", ana},
		"a token that does not verify": {link, okta.Forged(authClient, "okta-8", nil)},
		"no token":                     {link, ""},
	} {
		if code, out := bearer(t, http.MethodPost, c.url, ana, "", `{idToken: "`+c.token+`"}`); code != http.StatusBadRequest {
			t.Errorf("evidence that is %s = %d %s, want 400", what, code, out)
		}
	}
}

// A GitHub account is proved by action whoever proves it, and its binding says how that caller was
// authenticated. An asserted binding proved again by a verified caller becomes the provider's; a
// verified one proved again by an asserted caller stays as it is.
func TestProvingABindingAgainAsAVerifiedCallerChangesItsClaimant(t *testing.T) {
	srv, ts, corp, _ := linkServer(t)
	ana := corp.Token(authClient, "sub-ana", person("ana@example.com"))
	sys := entity.As(srv.Store, entity.SystemIdentity)
	claimant := func() string {
		t.Helper()
		rec, found, err := sys.Get(accountRef(githubProvider, anaSub))
		if err != nil || !found {
			t.Fatalf("no binding (found=%v err=%v)", found, err)
		}
		return entity.Str(rec.Payload, "claimant")
	}
	// prove claims and verifies ana-gh as ana, with her token or by her say-so.
	n := 0
	prove := func(token string) int {
		t.Helper()
		as := ""
		if token == "" {
			as = idAna
		}
		code, out := bearer(t, http.MethodPost, ts.URL+"/accounts/github?format=tony", token, as, `{login: ana-gh}`)
		if code != http.StatusCreated {
			t.Fatalf("claim = %d %s", code, out)
		}
		nonce := tonyOf(t, out)["nonce"].(string)
		n++
		reflectComment(t, srv, "c"+string(rune('0'+n)), "ana-gh", 101, "/verse link "+nonce)
		code, _ = bearer(t, http.MethodPost, ts.URL+"/accounts/github/verify?format=tony", token, as, `{nonce: "`+nonce+`"}`)
		return code
	}

	if code := prove(""); code != http.StatusCreated || claimant() != claimantAsserted {
		t.Fatalf("an asserted caller's verify = %d, claimant %q", code, claimant())
	}
	if code := prove(ana); code != http.StatusOK || claimant() != authProvName {
		t.Fatalf("proved again by a verified caller = %d, claimant %q, want 200 and %s", code, claimant(), authProvName)
	}
	if code := prove(""); code != http.StatusOK || claimant() != authProvName {
		t.Errorf("proved again by an asserted caller = %d, claimant %q, want it left as %s", code, claimant(), authProvName)
	}
}

// Where people are admitted only verified, a binding a caller nobody verified proved does not say
// who an account is: the lookup an io makes skips it until a verified caller proves it again. Its
// own principal still sees it listed, with the claimant that says why it does not count.
func TestAnAssertedBindingDoesNotCountWherePeopleAreVerified(t *testing.T) {
	srv, ts, corp, okta := linkServer(t)
	n := claim(t, ts, idAna, "ana-gh")
	reflectComment(t, srv, "1", "ana-gh", 101, "/verse link "+n)
	if code, _ := verify(t, ts, idAna, n); code != http.StatusCreated {
		t.Fatalf("verify = %d", code)
	}
	who := ts.URL + "/accounts/github/who?sub=" + anaSub
	if code, _ := call(t, http.MethodGet, idIO, who, ""); code != http.StatusOK {
		t.Fatalf("who under optional = %d, want the asserted binding answered", code)
	}

	if err := srv.SetAuth(Auth{Mode: AuthRequired, Providers: map[string]AuthProvider{
		authProvName:   {Issuer: corp.URL, ClientID: authClient, Domains: []string{authDomain}},
		"okta":         {Issuer: okta.URL, ClientID: authClient},
		githubProvider: {IOs: []string{"gh"}},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"sub=" + anaSub, "login=ana-gh"} {
		if code, _ := call(t, http.MethodGet, idIO, ts.URL+"/accounts/github/who?"+q, ""); code != http.StatusNotFound {
			t.Errorf("who?%s of an asserted binding where people are verified = %d, want 404", q, code)
		}
	}
	ana := corp.Token(authClient, "sub-ana", person("ana@example.com"))
	code, out := bearer(t, http.MethodGet, ts.URL+"/accounts?format=tony", ana, "", "")
	var rows []map[string]any
	if err := codec.Unmarshal([]byte(out), &rows, codec.Tony); err != nil || code != http.StatusOK {
		t.Fatalf("accounts = %d %s (%v)", code, out, err)
	}
	asserted := 0
	for _, row := range rows {
		if row["provider"] == githubProvider && row["claimant"] == claimantAsserted {
			asserted++
		}
	}
	if asserted != 1 {
		t.Errorf("ana's accounts = %v, want the GitHub binding listed as asserted", rows)
	}

	// Proved again by ana verified, it counts.
	code, out = bearer(t, http.MethodPost, ts.URL+"/accounts/github?format=tony", ana, "", `{login: ana-gh}`)
	if code != http.StatusCreated {
		t.Fatalf("claim = %d %s", code, out)
	}
	nonce := tonyOf(t, out)["nonce"].(string)
	reflectComment(t, srv, "2", "ana-gh", 101, "/verse link "+nonce)
	if code, out := bearer(t, http.MethodPost, ts.URL+"/accounts/github/verify", ana, "", `{nonce: "`+nonce+`"}`); code != http.StatusOK {
		t.Fatalf("verify as a verified caller = %d %s", code, out)
	}
	if code, out := call(t, http.MethodGet, idIO, who, ""); code != http.StatusOK || out["principal"] != idAna || out["claimant"] != authProvName {
		t.Errorf("who after a verified proof = %d %v", code, out)
	}
}

// Under `required` a person is admitted only from a verified token. A request that names a person
// and proves nobody is refused, and told to log in; an agent and a runner are whoever they say;
// and a request that names nobody still reads what everyone may.
func TestRequiredAdmitsAPersonOnlyFromAToken(t *testing.T) {
	srv, ts, idp := authServer(t, AuthRequired)
	token := idp.Token(authClient, "sub-ana", person("ana@example.com"))

	if code, body := bearer(t, http.MethodPost, ts.URL+"/entity", token, "", entityPut(t, "demo.pr.1")); code/100 != 2 {
		t.Fatalf("a put with a token = %d %s", code, body)
	}
	for _, as := range []string{idBo, "bo@example.com"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			url, body := ts.URL+"/whoami", ""
			if method == http.MethodPost {
				url, body = ts.URL+"/entity", entityPut(t, "other.pr.1")
			}
			code, out := bearer(t, method, url, "", as, body)
			if code != http.StatusUnauthorized || !strings.Contains(out, "verse login") {
				t.Errorf("%s as %q with no token = %d %q, want 401 saying to log in", method, as, code, strings.TrimSpace(out))
			}
		}
	}
	if _, ok, _ := srv.Store.Get(entity.RefAt("other", "pr", "1")); ok {
		t.Error("a refused request's write stands")
	}
	if _, ok, _ := srv.Store.Get(entity.UserRef("bo@example.com")); ok {
		t.Error("a name nobody proved is on the list of users")
	}
	// An agent and a runner are not verified by anything, and are admitted as they were.
	for _, as := range []string{"agent:ci", "runner:w1"} {
		code, out := bearer(t, http.MethodGet, ts.URL+"/whoami?format=tony", "", as, "")
		if who := tonyOf(t, out); code != http.StatusOK || who["principal"] != as || who["by"] != claimantAsserted {
			t.Errorf("whoami as %s = %d %v", as, code, who)
		}
	}
	// A request that names nobody reads what everyone may, and writes nothing, as before.
	if code, _ := bearer(t, http.MethodGet, ts.URL+"/entity?at=demo.pr.1", "", "", ""); code != http.StatusOK {
		t.Errorf("a read naming nobody = %d, want what * holds", code)
	}
	if code, _ := bearer(t, http.MethodPost, ts.URL+"/entity", "", "", entityPut(t, "anon.pr.1")); code != http.StatusBadRequest {
		t.Errorf("a write naming nobody = %d, want 400", code)
	}
}

// Proof by action reads a comment only under the ios the deployment trusts for GitHub. A comment
// an untrusted io reflects is no evidence, and where none is trusted nothing is claimed at all.
func TestOnlyATrustedIosReflectionIsEvidence(t *testing.T) {
	srv, ts := identityServer(t)
	resp := installIO(t, ts, idBo, map[string]any{
		"name": "mirror", "root": "mirror", "url": "http://mirror-io:9801",
		"owns": []any{map[string]any{"at": "mirror.signadot"}},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s", resp.Status)
	}
	// Bo's io reflects a comment that says ana's nonce, under its own root.
	nonce := claim(t, ts, idAna, "ana-gh")
	forged := entity.RefAt("mirror", "signadot", "verse", "comment", "42", "1")
	if _, _, err := srv.Store.Write(entity.Write{At: forged.At(), Value: entity.Obj{
		"author": "ana-gh", "authorId": 101, "body": "/verse link " + nonce,
		"url": "https://github.com/signadot/verse/pull/42#issuecomment-1",
	}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if code, _ := verify(t, ts, idAna, nonce); code != http.StatusNotFound {
		t.Fatalf("verify on an untrusted io's reflection = %d, want 404", code)
	}
	// The trusted set is the store's to show, to anybody.
	code, out := call(t, http.MethodGet, idBo, ts.URL+"/entity?at="+trustRef(githubProvider).String(), "")
	if code != http.StatusOK || !strings.Contains(fmt.Sprint(out), "gh") {
		t.Errorf("the trusted ios as a person reads them = %d %v", code, out)
	}

	// Trusting both, the same comment is evidence; the config at the next start says which.
	if err := srv.SetAuth(Auth{Providers: map[string]AuthProvider{githubProvider: {IOs: []string{"gh", "mirror"}}}}); err != nil {
		t.Fatal(err)
	}
	if code, _ := verify(t, ts, idAna, nonce); code != http.StatusCreated {
		t.Fatalf("verify once the io is trusted = %d, want 201", code)
	}

	// A config that names none trusts none, whatever stood before: nothing is claimed or verified.
	if err := srv.SetAuth(Auth{}); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, http.MethodPost, idBo, ts.URL+"/accounts/github", `{"login": "bo-gh"}`); code != http.StatusConflict {
		t.Errorf("a claim where no io is trusted = %d, want 409", code)
	}
}
