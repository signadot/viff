package versed

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/internal/versed/idptest"
	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/mirror"
)

// An io proves it is the io with its workload token, and a trusted io relays a person through the
// person's account binding (iod.go).

const (
	ioSub       = "system:serviceaccount:verse:slack-iod"
	ioAgent     = "agent:slack-iod"
	ioName      = "workspace"
	slackProv   = "slack"
	staticToken = "s3cret-static-token-for-the-git-iod"
	staticIo    = "agent:git-iod"
)

// iodServer is a server in mode with people from `corp`, iods from a cluster issuer (the second fake
// IdP), one io `workspace` acting as agent:slack-iod, a static io, and slack trusting `workspace`
// as the provider slack says.
func iodServer(t *testing.T, mode string, slack AuthProvider) (*Server, *httptest.Server, *idptest.IdP, *idptest.IdP) {
	t.Helper()
	people, cluster := idptest.New(t), idptest.New(t)
	file := filepath.Join(t.TempDir(), "git-iod.token")
	if err := os.WriteFile(file, []byte(staticToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t)
	srv.Start(t.Context())
	providers := map[string]AuthProvider{
		authProvName: {Issuer: people.URL, ClientID: authClient, Domains: []string{authDomain}},
	}
	if len(slack.IOs) > 0 {
		providers[slackProv] = slack
	}
	if err := srv.SetAuth(Auth{Mode: mode, Providers: providers, Iod: AuthIod{
		Issuer:   cluster.URL,
		Accounts: map[string]IodAccount{ioSub: {Principal: ioAgent, IO: ioName}},
		Static:   []IodStatic{{File: file, Principal: staticIo}},
	}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, people, cluster
}

// slackTrusting is the slack provider trusting the io named, admitting first sights from example.com.
func slackTrusting(io string) AuthProvider {
	return AuthProvider{IOs: []string{io}, Domains: []string{authDomain}}
}

// relay makes one request as an io presenting a person: the io's bearer, the person's name and
// their account.
func relay(t *testing.T, method, url, token, person, account, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/tony")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if person != "" {
		req.Header.Set(api.PrincipalHeader, person)
	}
	if account != "" {
		req.Header.Set(api.ForPrincipalHeader, account)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

func TestAnIoProvesItselfWithItsWorkloadToken(t *testing.T) {
	srv, ts, _, cluster := iodServer(t, AuthRequired, slackTrusting(ioName))
	whoami := ts.URL + "/whoami?format=tony"

	token := cluster.Token(iodAudience, ioSub, nil)
	code, body := relay(t, http.MethodGet, whoami, token, "", "", "")
	if who := tonyOf(t, body); code != http.StatusOK || who["principal"] != ioAgent || who["by"] != iodProvider {
		t.Fatalf("whoami with the workload token = %d %s", code, body)
	}
	// Naming itself beside its token is fine; naming anyone else is a relay (below).
	if code, body := relay(t, http.MethodGet, whoami, token, ioAgent, "", ""); code != http.StatusOK {
		t.Errorf("the io naming itself = %d %s", code, body)
	}

	for what, bad := range map[string]string{
		"a token for another audience": cluster.Token("other", ioSub, nil),
		"a subject that is no io":      cluster.Token(iodAudience, "system:serviceaccount:verse:stranger", nil),
		"a forged token":               cluster.Forged(iodAudience, ioSub, nil),
	} {
		if code, body := relay(t, http.MethodGet, whoami, bad, "", "", ""); code != http.StatusUnauthorized {
			t.Errorf("%s = %d %s, want 401", what, code, body)
		}
	}
	if code, body := relay(t, http.MethodGet, whoami, cluster.Token(iodAudience, "system:serviceaccount:verse:stranger", nil), "", "", ""); !strings.Contains(body, "no io of this verse") {
		t.Errorf("an unknown subject's refusal does not say so: %d %s", code, body)
	}

	// A static token is its io; one byte off is nobody's.
	code, body = relay(t, http.MethodGet, whoami, staticToken, "", "", "")
	if who := tonyOf(t, body); code != http.StatusOK || who["principal"] != staticIo || who["by"] != iodProvider {
		t.Errorf("whoami with the static token = %d %s", code, body)
	}
	if code, _ := relay(t, http.MethodGet, whoami, staticToken+"x", "", "", ""); code != http.StatusUnauthorized {
		t.Errorf("a static token one byte off = %d, want 401", code)
	}

	// The record says what the door was told, and holds no token.
	rec, found, err := srv.Store.Get(accountRef(iodProvider, ioSub))
	if err != nil || !found || entity.Str(rec.Payload, "principal") != ioAgent || entity.Str(rec.Payload, "io") != ioName {
		t.Errorf("the io's account record = %v (found=%v err=%v)", rec.Payload, found, err)
	}
	statics := entity.Below(srv.Store, nil, 1, identityRef(iodProvider, "static").Segs()...)
	if len(statics) != 1 || entity.Str(statics[0].Payload, "principal") != staticIo || strings.Contains(statics[0].Ref.String(), staticToken) {
		t.Errorf("the static io's record = %v", statics)
	}

	// Iods are verified whatever the people's mode: a verse that verifies no person still knows its iods.
	off, tsOff, _, clusterOff := iodServer(t, AuthOff, AuthProvider{})
	_ = off
	if code, body := relay(t, http.MethodGet, tsOff.URL+"/whoami?format=tony", clusterOff.Token(iodAudience, ioSub, nil), "", "", ""); code != http.StatusOK || tonyOf(t, body)["principal"] != ioAgent {
		t.Errorf("under mode off, the io's token = %d %s", code, body)
	}
}

// The iods' issuer may sit behind a CA no public root signed, a cluster's own API server: named a
// bundle, the door reaches it; not named one, it cannot, and says so rather than admitting.
func TestAnIssuerBehindTheClustersCAIsReachedThroughIt(t *testing.T) {
	cluster := idptest.NewTLS(t)
	accounts := map[string]IodAccount{ioSub: {Principal: ioAgent, IO: ioName}}
	trusting := testServer(t)
	if err := trusting.SetAuth(Auth{Iod: AuthIod{Issuer: cluster.URL, Accounts: accounts, CA: cluster.CAFile()}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(trusting.Handler())
	defer ts.Close()
	token := cluster.Token(iodAudience, ioSub, nil)
	if code, body := relay(t, http.MethodGet, ts.URL+"/whoami?format=tony", token, "", "", ""); code != http.StatusOK || tonyOf(t, body)["principal"] != ioAgent {
		t.Errorf("through the bundle: whoami = %d %s", code, body)
	}

	blind := testServer(t)
	if err := blind.SetAuth(Auth{Iod: AuthIod{Issuer: cluster.URL, Accounts: accounts}}); err != nil {
		t.Fatal(err)
	}
	ts2 := httptest.NewServer(blind.Handler())
	defer ts2.Close()
	if code, body := relay(t, http.MethodGet, ts2.URL+"/whoami", token, "", "", ""); code != http.StatusServiceUnavailable || !strings.Contains(body, "could not be read") {
		t.Errorf("without the bundle: whoami = %d %q, want 503 saying the issuer could not be read", code, strings.TrimSpace(body))
	}
	if err := testServer(t).SetAuth(Auth{Iod: AuthIod{Issuer: cluster.URL, CA: filepath.Join(t.TempDir(), "none.crt")}}); err == nil {
		t.Error("a bundle that is not there was accepted")
	}
}

func TestATrustedIoRelaysAPersonThroughABinding(t *testing.T) {
	srv, ts, people, cluster := iodServer(t, AuthRequired, slackTrusting(ioName))
	io := cluster.Token(iodAudience, ioSub, nil)
	const account = slackProv + ":T1/U1"

	// Ana has never been seen. The io relays her from Slack; her first sight binds the Slack
	// account, and the request is hers, with how verse knows it.
	code, body := relay(t, http.MethodGet, ts.URL+"/whoami?format=tony", io, idAna, account, "")
	if who := tonyOf(t, body); code != http.StatusOK || who["principal"] != idAna || who["by"] != slackProv || who["via"] != ioAgent {
		t.Fatalf("whoami relayed = %d %s", code, body)
	}
	rec, found, err := entity.As(srv.Store, entity.SystemIdentity).Get(accountRef(slackProv, "T1/U1"))
	if err != nil || !found {
		t.Fatalf("no binding after a relayed first sight (found=%v err=%v)", found, err)
	}
	proof := entity.Get(rec.Payload, "proof")
	if entity.Str(rec.Payload, "principal") != idAna || entity.Str(rec.Payload, "claimant") != slackProv ||
		entity.Str(rec.Payload, "via") != ioAgent || entity.Str(proof, "method") != "relay" ||
		entity.Str(proof, "io") != ioAgent || entity.Str(proof, "account") != "T1/U1" {
		t.Errorf("binding = %v", rec.Payload)
	}

	// Spelled with capitals by the io, the person is the same person, under the name grants use.
	if code, body := relay(t, http.MethodGet, ts.URL+"/whoami?format=tony", io, "user:Ana@Example.com", account, ""); code != http.StatusOK || tonyOf(t, body)["principal"] != idAna {
		t.Errorf("relayed with capitals = %d %s", code, body)
	}

	// A write as the relayed person is more than a trusted io does by default.
	if code, body := relay(t, http.MethodPost, ts.URL+"/entity", io, idAna, account, entityPut(t, "demo.pr.9")); code != http.StatusForbidden || !strings.Contains(body, "acts: any") {
		t.Errorf("a write relayed under control acts = %d %s, want 403", code, body)
	}

	// A gate of ana's own rule, answered from Slack: the ballot is hers, and says it came through
	// the io, vouched for by slack.
	ana := people.Token(authClient, "sub-ana", person("ana@example.com"))
	if code, out := bearer(t, http.MethodPost, ts.URL+"/charter", ana, "",
		deployRule(`[{name: ok, mode: ask}]`, `script: "true"`)); code != http.StatusCreated {
		t.Fatalf("install = %d %s", code, out)
	}
	ready, err := codec.Marshal(api.PutEntity{At: "demo.pr.1", Payload: entity.Obj{"stage": "ready"}.Payload()}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if code, out := bearer(t, http.MethodPost, ts.URL+"/entity", ana, "", string(ready)); code/100 != 2 {
		t.Fatalf("put = %d %s", code, out)
	}
	qid := waitRuleQuestion(t, srv, "deploy", "demo.pr.\"1\"")
	if code, out := relay(t, http.MethodPost, ts.URL+answerPath(qid), io, idAna, account, `{choice: proceed}`); code/100 != 2 {
		t.Fatalf("answer relayed = %d %s", code, out)
	}
	q, found, err := srv.Store.Get(mirror.QuestionRef(qid))
	if err != nil || !found {
		t.Fatalf("the question's record: found=%v err=%v", found, err)
	}
	ballot := entity.Get(entity.Get(q.Payload, "ballots"), idAna)
	if entity.Str(ballot, "choice") != "proceed" || entity.Str(ballot, "authenticated") != slackProv || entity.Str(ballot, "via") != ioAgent {
		t.Errorf("ana's ballot = %v, want proceed, authenticated slack, via the io", ballot)
	}

	// The same account, relayed again, is the same person without another first sight; and the
	// io's lookup finds her.
	if code, body := relay(t, http.MethodGet, ts.URL+"/whoami?format=tony", io, idAna, account, ""); code != http.StatusOK || tonyOf(t, body)["principal"] != idAna {
		t.Errorf("relayed again = %d %s", code, body)
	}
	if code, body := relay(t, http.MethodGet, ts.URL+"/accounts/"+slackProv+"/who?sub=T1%2FU1&format=tony", io, "", "", ""); code != http.StatusOK || tonyOf(t, body)["principal"] != idAna {
		t.Errorf("the io's lookup of the account = %d %s", code, body)
	}
}

func TestATrustedIoMayBeLetDoAnything(t *testing.T) {
	any := slackTrusting(ioName)
	any.Acts = actsAny
	srv, ts, _, cluster := iodServer(t, AuthRequired, any)
	io := cluster.Token(iodAudience, ioSub, nil)
	if code, body := relay(t, http.MethodPost, ts.URL+"/entity", io, idAna, slackProv+":T1/U1", entityPut(t, "demo.pr.1")); code/100 != 2 {
		t.Fatalf("a write relayed under acts: any = %d %s", code, body)
	}
	if got := owner(t, srv, entity.RefAt("demo")); got != idAna {
		t.Errorf("owner of what the relayed person wrote = %q, want %s", got, idAna)
	}
}

func TestARelayIsRefusedWhereItMustBe(t *testing.T) {
	srv, ts, people, cluster := iodServer(t, AuthRequired, slackTrusting(ioName))
	io := cluster.Token(iodAudience, ioSub, nil)
	whoami := ts.URL + "/whoami?format=tony"

	// An account bound to somebody else, however it got there.
	if _, err := srv.bind(entity.As(srv.Store, entity.SystemIdentity), slackProv,
		entity.Obj{"principal": idBo, "sub": "T1/U9", "claimant": slackProv}, idBo, "T1/U9"); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		what, person, account, want string
		code                        int
	}{
		{"no account named", idAna, "", "names none", http.StatusForbidden},
		{"an account of a provider that trusts no iods", idAna, authProvName + ":sub-ana", "trusts iods", http.StatusForbidden},
		{"an account of no provider", idAna, "nowhere:1", "no provider of this verse", http.StatusForbidden},
		{"a person who is no person", "agent:bot", slackProv + ":T1/U2", "relays a person", http.StatusForbidden},
		{"a runner", "runner:gpu", slackProv + ":T1/U2", "relays a person", http.StatusForbidden},
		{"an account bound to another", idAna, slackProv + ":T1/U9", "another principal", http.StatusConflict},
		{"an email outside the domains", "user:eve@elsewhere.org", slackProv + ":T1/U3", "none of the domains", http.StatusForbidden},
	} {
		code, body := relay(t, http.MethodGet, whoami, io, c.person, c.account, "")
		if code != c.code || !strings.Contains(body, c.want) {
			t.Errorf("%s = %d %q, want %d saying %q", c.what, code, strings.TrimSpace(body), c.code, c.want)
		}
	}
	// A write by a never-seen person is refused as an act, and the refusal writes no binding.
	if code, body := relay(t, http.MethodPost, ts.URL+"/entity", io, idAna, slackProv+":T1/U7", entityPut(t, "demo.pr.7")); code != http.StatusForbidden || !strings.Contains(body, "acts: any") {
		t.Errorf("a write by a never-seen person = %d %q, want 403 on the act", code, strings.TrimSpace(body))
	}
	if ents := entity.Below(srv.Store, nil, 1, identityRef(slackProv, "account").Segs()...); len(ents) != 1 {
		t.Errorf("refusals bound somebody: %d bindings, want bo's alone", len(ents))
	}
	// A request matching no route is the router's to answer, not the relay's.
	if code, _ := relay(t, http.MethodPost, ts.URL+"/whoami", io, idAna, slackProv+":T1/U1", ""); code != http.StatusMethodNotAllowed {
		t.Errorf("a relayed POST to a GET-only route = %d, want 405", code)
	}
	// A write on an account bound to another is the conflict, not the act: the binding is
	// checked first, as documented.
	if code, body := relay(t, http.MethodPost, ts.URL+"/entity", io, idAna, slackProv+":T1/U9", entityPut(t, "demo.pr.1")); code != http.StatusConflict {
		t.Errorf("a write for another's account = %d %q, want 409", code, strings.TrimSpace(body))
	}

	// A binding proved by a caller nobody verified does not admit a relay where people are
	// verified: it is proved again by the relay, which makes it the provider's.
	if _, err := srv.bind(entity.As(srv.Store, entity.SystemIdentity), slackProv,
		entity.Obj{"principal": idAna, "sub": "T1/U4", "claimant": claimantAsserted}, idAna, "T1/U4"); err != nil {
		t.Fatal(err)
	}
	if code, body := relay(t, http.MethodGet, whoami, io, idAna, slackProv+":T1/U4", ""); code != http.StatusOK {
		t.Fatalf("ana relayed on her asserted binding = %d %q", code, strings.TrimSpace(body))
	}
	rec, _, _ := entity.As(srv.Store, entity.SystemIdentity).Get(accountRef(slackProv, "T1/U4"))
	if entity.Str(rec.Payload, "claimant") != slackProv {
		t.Errorf("the asserted binding was not re-proved by the relay: %v", rec.Payload)
	}
	if code, _ := relay(t, http.MethodGet, whoami, io, idBo, slackProv+":T1/U4", ""); code != http.StatusConflict {
		t.Errorf("bo relayed on ana's account = %d, want 409", code)
	}

	// An io the provider does not trust, and a provider with no domains: a binding or nothing.
	for what, slack := range map[string]AuthProvider{
		"an io the provider does not trust": slackTrusting("other"),
		"a provider with no domains":        {IOs: []string{ioName}},
	} {
		_, ts2, _, cluster2 := iodServer(t, AuthRequired, slack)
		code, body := relay(t, http.MethodGet, ts2.URL+"/whoami?format=tony", cluster2.Token(iodAudience, ioSub, nil), idAna, slackProv+":T1/U1", "")
		if code != http.StatusForbidden {
			t.Errorf("%s = %d %q, want 403", what, code, strings.TrimSpace(body))
		}
	}

	// A person's bearer beside another person's name is still refused: this is not delegation.
	ana := people.Token(authClient, "sub-ana", person("ana@example.com"))
	if code, _ := relay(t, http.MethodGet, whoami, ana, idBo, slackProv+":T1/U1", ""); code != http.StatusForbidden {
		t.Errorf("a person relaying another = %d, want 403", code)
	}
	// And a static io with no name is trusted for nobody's accounts.
	if code, body := relay(t, http.MethodGet, whoami, staticToken, idAna, slackProv+":T1/U1", ""); code != http.StatusForbidden || !strings.Contains(body, "not an io this verse trusts") {
		t.Errorf("a nameless io relaying = %d %q", code, strings.TrimSpace(body))
	}
}

func TestATrustedIoReportsANonceHeardAndTheAccountIsBound(t *testing.T) {
	// Slack trusts the io, admits no first sight: the relay takes bound accounts only.
	srv, ts, people, cluster := iodServer(t, AuthRequired, AuthProvider{IOs: []string{ioName}})
	io := cluster.Token(iodAudience, ioSub, nil)
	ana := people.Token(authClient, "sub-ana", person("ana@example.com"))
	const account = slackProv + ":T1/U1"
	whoami := ts.URL + "/whoami?format=tony"
	heard := ts.URL + "/accounts/" + slackProv + "/heard?format=tony"

	// Unbound, the relay turns ana away: no domain admits a first sight.
	if code, body := relay(t, http.MethodGet, whoami, io, idAna, account, ""); code != http.StatusForbidden || !strings.Contains(body, "names no domains") {
		t.Fatalf("relay of an unbound account without first sight = %d %s, want 403", code, body)
	}

	// Ana, logged in, claims a Slack account: no login to name, a nonce to say.
	code, body := bearer(t, http.MethodPost, ts.URL+"/accounts/"+slackProv+"?format=tony", ana, "", `{}`)
	claim := tonyOf(t, body)
	nonce, _ := claim["nonce"].(string)
	if code != http.StatusCreated || nonce == "" || claim["say"] != "/verse link "+nonce {
		t.Fatalf("claim under slack = %d %s", code, body)
	}
	if _, has := claim["login"]; has {
		t.Errorf("a slack claim names a login: %v", claim)
	}

	// Heard by somebody other than a trusted io for slack: refused, the claim stands.
	said := `{nonce: "` + nonce + `", account: "T1/U1"}`
	if code, body := bearer(t, http.MethodPost, heard, ana, "", said); code != http.StatusForbidden {
		t.Errorf("a person reporting a nonce heard = %d %s, want 403", code, body)
	}
	if code, body := relay(t, http.MethodPost, heard, staticToken, "", "", said); code != http.StatusForbidden || !strings.Contains(body, "not an io this verse trusts for slack") {
		t.Errorf("an io slack does not trust reporting = %d %s, want 403", code, body)
	}
	if code, body := relay(t, http.MethodPost, heard, io, "", "", `{nonce: "nobody-claimed-this", account: "T1/U1"}`); code != http.StatusNotFound {
		t.Errorf("a nonce nobody claimed = %d %s, want 404", code, body)
	}

	// The trusted io heard the account say it: bound, with the claimant ana was authenticated as.
	code, body = relay(t, http.MethodPost, heard, io, "", "", said)
	if code != http.StatusCreated {
		t.Fatalf("heard by the trusted io = %d %s", code, body)
	}
	rec, found, err := entity.As(srv.Store, entity.SystemIdentity).Get(accountRef(slackProv, "T1/U1"))
	if err != nil || !found {
		t.Fatalf("no binding after heard (found=%v err=%v)", found, err)
	}
	proof := entity.Get(rec.Payload, "proof")
	if entity.Str(rec.Payload, "principal") != idAna || entity.Str(rec.Payload, "claimant") != authProvName ||
		entity.Str(rec.Payload, "via") != ioAgent || entity.Str(proof, "method") != "heard" ||
		entity.Str(proof, "io") != ioAgent || entity.Str(proof, "account") != "T1/U1" {
		t.Errorf("binding = %v", rec.Payload)
	}
	// The nonce is spent.
	if code, body := relay(t, http.MethodPost, heard, io, "", "", said); code != http.StatusNotFound {
		t.Errorf("the same nonce heard twice = %d %s, want 404", code, body)
	}

	// Now the relay admits the account as ana, and a ballot is hers, authenticated by slack.
	if code, body := relay(t, http.MethodGet, whoami, io, idAna, account, ""); code != http.StatusOK || tonyOf(t, body)["principal"] != idAna || tonyOf(t, body)["by"] != slackProv {
		t.Errorf("relay after heard = %d %s", code, body)
	}
}

func TestAClaimIsUnderAProviderAnIoProves(t *testing.T) {
	_, ts, people, _ := iodServer(t, AuthRequired, AuthProvider{IOs: []string{ioName}})
	ana := people.Token(authClient, "sub-ana", person("ana@example.com"))
	// The people's issuer proves its own accounts; nothing is claimed under it.
	if code, body := bearer(t, http.MethodPost, ts.URL+"/accounts/"+authProvName, ana, "", `{}`); code != http.StatusNotFound {
		t.Errorf("a claim under an issuer = %d %s, want 404", code, body)
	}
	if code, body := bearer(t, http.MethodPost, ts.URL+"/accounts/nowhere", ana, "", `{}`); code != http.StatusNotFound {
		t.Errorf("a claim under no provider = %d %s, want 404", code, body)
	}
}

func TestANonceHeardFromAnotherLoginBindsNothing(t *testing.T) {
	srv, ts, people, cluster := iodServer(t, AuthRequired, AuthProvider{IOs: []string{ioName}})
	// The same io, trusted for github too.
	if err := srv.SetAuth(Auth{Mode: AuthRequired, Providers: map[string]AuthProvider{
		authProvName:   {Issuer: people.URL, ClientID: authClient, Domains: []string{authDomain}},
		githubProvider: {IOs: []string{ioName}},
	}, Iod: AuthIod{Issuer: cluster.URL, Accounts: map[string]IodAccount{ioSub: {Principal: ioAgent, IO: ioName}}}}); err != nil {
		t.Fatal(err)
	}
	io := cluster.Token(iodAudience, ioSub, nil)
	ana := people.Token(authClient, "sub-ana", person("ana@example.com"))
	heard := ts.URL + "/accounts/github/heard?format=tony"

	code, body := bearer(t, http.MethodPost, ts.URL+"/accounts/github?format=tony", ana, "", `{login: "@Ana-GH"}`)
	nonce, _ := tonyOf(t, body)["nonce"].(string)
	if code != http.StatusCreated || nonce == "" {
		t.Fatalf("claim = %d %s", code, body)
	}
	for what, said := range map[string]string{
		"another login":  `{nonce: "` + nonce + `", account: "github:666", login: mallory}`,
		"no login named": `{nonce: "` + nonce + `", account: "github:101"}`,
	} {
		if code, body := relay(t, http.MethodPost, heard, io, "", "", said); code != http.StatusForbidden || !strings.Contains(body, "said from the account it claims") {
			t.Errorf("%s = %d %s, want 403", what, code, body)
		}
	}
	// The claim stands, and the login itself, however cased, binds.
	code, body = relay(t, http.MethodPost, heard, io, "", "", `{nonce: "`+nonce+`", account: "github:101", login: "ana-gh"}`)
	if code != http.StatusCreated {
		t.Fatalf("heard from the claimed login = %d %s", code, body)
	}
	rec, found, err := entity.As(srv.Store, entity.SystemIdentity).Get(accountRef(githubProvider, "101"))
	if err != nil || !found || entity.Str(rec.Payload, "principal") != idAna || entity.Str(rec.Payload, "login") != "ana-gh" {
		t.Errorf("binding = %v (found=%v err=%v)", rec.Payload, found, err)
	}
}
