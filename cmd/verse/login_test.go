package main

import (
	"bytes"
	"github.com/signadot/verse/internal/versed/versedtest"
	"github.com/signadot/verse/pkg/runner/runnerd/runnerdtest"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/internal/config"
	"github.com/signadot/verse/internal/versed"
	"github.com/signadot/verse/internal/versed/idptest"
	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	verseio "github.com/signadot/verse/pkg/io"
)

// `verse account login` proves who is at the keyboard to the verse's identity provider, and from then on
// the CLI's requests carry the token it was given, in place of a name.

const loginClient = "verse-cli"

// serveLogin stands up a verse that takes tokens from a fake IdP named `corp`, and a config that
// names the same provider, so the CLI can log in to it. The account that logs in is ana's.
func serveLogin(t *testing.T, device bool) (entity.Store, *idptest.Logins) {
	return serveLoginWith(t, device, false)
}

// serveLoginWith is serveLogin, with a github provider that names iods beside the issuer when asked.
func serveLoginWith(t *testing.T, device, withIOs bool) (entity.Store, *idptest.Logins) {
	t.Helper()
	idp := idptest.New(t)
	l := idp.Login(loginClient, "sub-ana", map[string]any{"email": "ana@example.com", "email_verified": true}, device)

	store := entitytest.Store(t)
	srv := versedtest.Server(t, runnerdtest.Scripted, store)
	if err := srv.SetAuth(versed.Auth{Mode: versed.AuthOptional, Providers: map[string]versed.AuthProvider{
		"corp": {Issuer: idp.URL, ClientID: loginClient, Domains: []string{"example.com"}},
	}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	cfg := filepath.Join(t.TempDir(), "verse.tony")
	body := "auth:\n  principal:\n    providers:\n      corp:\n        issuer: \"" + idp.URL + "\"\n        clientId: " + loginClient + "\n"
	if withIOs {
		body += "      github:\n        iods: [gh]\n"
	}
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigPath, cfg)
	return store, l
}

// visitor is the browser of a login by authorization code: it opens the first URL the CLI prints
// and follows it back to the CLI's loopback port.
type visitor struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	once sync.Once
}

var printedURL = regexp.MustCompile(`https?://\S+`)

func (v *visitor) Write(p []byte) (int, error) {
	v.mu.Lock()
	v.buf.Write(p)
	found := printedURL.FindString(v.buf.String())
	v.mu.Unlock()
	if found != "" && strings.HasSuffix(strings.TrimRight(v.buf.String(), " "), "\n") {
		v.once.Do(func() {
			go func() {
				if resp, err := http.Get(found); err == nil {
					resp.Body.Close()
				}
			}()
		})
	}
	return len(p), nil
}

func (v *visitor) Close() error { return nil }

// runLogin is runCLI for a login by authorization code, with a browser.
func runLogin(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cc := &cli.Context{
		Out: nopCloser{&out}, Err: &visitor{},
		In: io.NopCloser(strings.NewReader("")), Go: t.Context(),
	}
	cmd := rootCmd()
	g.Addr = testAddr
	sub := cmd.FindSub(cc, args[0])
	err := sub.Run(cc, args[1:])
	return out.String(), err
}

// expireLogin makes the kept login one whose id_token ran out a minute ago, as time would.
func expireLogin(t *testing.T) {
	t.Helper()
	logins = nil
	c, err := loadLogins()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range c.Verses {
		for _, e := range v.Providers {
			e.Expiry = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		}
	}
	if err := saveLogins(c); err != nil {
		t.Fatal(err)
	}
}

func TestALoginByDeviceCodeActsAsThePersonItProves(t *testing.T) {
	store, _ := serveLogin(t, true)

	// Before a login, the CLI is the config's user, asserted.
	if out, err := runCLI(t, "whoami"); err != nil || strings.TrimSpace(out) != tester+" (asserted)" {
		t.Fatalf("whoami before a login = %q, %v", out, err)
	}
	out, err := runCLI(t, "account", "login")
	if err != nil || !strings.Contains(out, "as user:ana@example.com (corp)") {
		t.Fatalf("login = %q, %v", out, err)
	}
	st, err := os.Stat(config.TokensPath())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the tokens file: %v, mode %v, want 0600", err, st.Mode().Perm())
	}
	if out, err := runCLI(t, "whoami"); err != nil || strings.TrimSpace(out) != "user:ana@example.com (corp)" {
		t.Fatalf("whoami after a login = %q, %v", out, err)
	}
	// What the CLI writes is the person's, over the config's `user:`.
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", `{stage: open}`); err != nil {
		t.Fatalf("put: %v", err)
	}
	rec, ok, err := store.Get(entity.PermsRef(entity.RefAt("demo")))
	if err != nil || !ok || entity.Str(rec.Payload, entity.OwnerField) != "user:ana@example.com" {
		t.Fatalf("owner of what the logged-in CLI put = %q (ok=%v err=%v)", entity.Str(rec.Payload, entity.OwnerField), ok, err)
	}

	// As an agent, the CLI is the agent, and sends no token beside that name.
	t.Setenv("VERSE_AGENT", "ci")
	if out, err := runCLI(t, "whoami"); err != nil || strings.TrimSpace(out) != "agent:ci (asserted)" {
		t.Errorf("whoami as an agent, logged in = %q, %v", out, err)
	}
	if _, err := runCLI(t, "account", "login"); err == nil || !strings.Contains(err.Error(), "a login is a person's") {
		t.Errorf("a login as an agent: %v", err)
	}
	t.Setenv("VERSE_AGENT", "")

	if out, err := runCLI(t, "account", "logout"); err != nil || !strings.Contains(out, "logged out") {
		t.Fatalf("logout = %q, %v", out, err)
	}
	if out, err := runCLI(t, "whoami"); err != nil || strings.TrimSpace(out) != tester+" (asserted)" {
		t.Errorf("whoami after a logout = %q, %v", out, err)
	}
	if out, err := runCLI(t, "account", "logout"); err != nil || !strings.Contains(out, "not logged in") {
		t.Errorf("a second logout = %q, %v", out, err)
	}
}

// `--as` names somebody else, so the CLI sends that name alone: a token beside it would have the
// verse make the request as the person who logged in, whatever the command said.
func TestAsSendsTheNameAndNotTheToken(t *testing.T) {
	serveLogin(t, true)
	if _, err := runCLI(t, "account", "login"); err != nil {
		t.Fatal(err)
	}
	if token, err := bearer(t.Context()); err != nil || token == "" {
		t.Fatalf("the logged-in CLI's bearer = %q, %v", token, err)
	}
	who, err := principal("bo@example.com") // what a command's --as does
	if err != nil || who != "user:bo@example.com" {
		t.Fatalf("--as bo = %q, %v", who, err)
	}
	if token, err := bearer(t.Context()); err != nil || token != "" {
		t.Fatalf("the bearer beside --as = %q, %v, want none", token, err)
	}
	asOverride = ""
}

func TestALoginByAuthorizationCode(t *testing.T) {
	_, l := serveLogin(t, false)
	out, err := runLogin(t, "account", "login", "corp")
	if err != nil || !strings.Contains(out, "as user:ana@example.com (corp)") {
		t.Fatalf("login = %q, %v", out, err)
	}
	if !strings.Contains(l.Scopes, "openid") || !strings.Contains(l.Scopes, "email") {
		t.Errorf("the login asked for %q, want openid and email", l.Scopes)
	}
	if out, err := runCLI(t, "whoami"); err != nil || strings.TrimSpace(out) != "user:ana@example.com (corp)" {
		t.Fatalf("whoami = %q, %v", out, err)
	}
}

// A provider that names the iods it trusts and no issuer is nobody's to log in to: a bare
// `verse account login` picks the one issuer beside it, and naming it is refused plainly.
func TestOnlyAnIssuerIsAProviderToLogInTo(t *testing.T) {
	serveLoginWith(t, false, true) // corp, the issuer, beside github, which has iods alone
	if out, err := runLogin(t, "account", "login"); err != nil || !strings.Contains(out, "as user:ana@example.com (corp)") {
		t.Fatalf("a bare login with one issuer = %q, %v", out, err)
	}
	if _, err := runLogin(t, "account", "login", "github"); err == nil || !strings.Contains(err.Error(), "nobody's to log in to") {
		t.Errorf("logging in to an iods-only provider: err = %v, want a plain refusal", err)
	}
}

func TestAnExpiredLoginIsRenewed(t *testing.T) {
	_, l := serveLogin(t, true)
	if _, err := runCLI(t, "account", "login"); err != nil {
		t.Fatal(err)
	}
	expireLogin(t)
	if out, err := runCLI(t, "whoami"); err != nil || strings.TrimSpace(out) != "user:ana@example.com (corp)" {
		t.Fatalf("whoami on an expired token = %q, %v", out, err)
	}
	if l.Refreshes != 1 {
		t.Fatalf("the login was renewed %d times, want once", l.Refreshes)
	}
	// The renewed token is kept: the next command sends it and renews nothing.
	if _, err := runCLI(t, "whoami"); err != nil || l.Refreshes != 1 {
		t.Errorf("a second command: %v, %d renewals", err, l.Refreshes)
	}
}

// A provider that answers a refresh with no id_token cannot renew a login, and the CLI says so
// rather than sending the request as the config's user.
func TestALoginThatCannotBeRenewedFailsTheRequest(t *testing.T) {
	for what, set := range map[string]func(*idptest.Logins){
		"a refresh that gives no id_token": func(l *idptest.Logins) { l.NoIDTokenOnRefresh = true },
		"no refresh token":                 func(l *idptest.Logins) { l.NoRefreshToken = true },
	} {
		_, l := serveLogin(t, true)
		set(l)
		if _, err := runCLI(t, "account", "login"); err != nil {
			t.Fatalf("%s: login: %v", what, err)
		}
		expireLogin(t)
		out, err := runCLI(t, "whoami")
		if err == nil || !strings.Contains(err.Error(), "verse account login") {
			t.Errorf("%s: whoami = %q, %v, want a failure that says to log in again", what, out, err)
		}
		// Logging out is how to go on as the config's user.
		if _, err := runCLI(t, "account", "logout"); err != nil {
			t.Errorf("%s: logout: %v", what, err)
		}
		if out, err := runCLI(t, "whoami"); err != nil || strings.TrimSpace(out) != tester+" (asserted)" {
			t.Errorf("%s: whoami after a logout = %q, %v", what, out, err)
		}
	}
}

// A token the verse does not take is no login: nothing is kept of it.
func TestALoginTheVerseRefusesIsNotKept(t *testing.T) {
	_, l := serveLogin(t, true)
	l.Claims = map[string]any{"email": "ana@elsewhere.org", "email_verified": true}
	if _, err := runCLI(t, "account", "login"); err == nil || !strings.Contains(err.Error(), "in none of them") {
		t.Fatalf("a login from a domain the verse does not admit: %v", err)
	}
	if out, err := runCLI(t, "whoami"); err != nil || strings.TrimSpace(out) != tester+" (asserted)" {
		t.Errorf("whoami after a refused login = %q, %v", out, err)
	}
}

func TestLoginNamesItsProvider(t *testing.T) {
	serveLogin(t, true)
	if _, err := runCLI(t, "account", "login", "okta"); err == nil || !strings.Contains(err.Error(), "corp") {
		t.Errorf("a login to a provider the config does not name: %v", err)
	}
	t.Setenv(config.EnvConfigPath, filepath.Join(t.TempDir(), "none.tony"))
	if _, err := runCLI(t, "account", "login"); err == nil || !strings.Contains(err.Error(), "names no identity provider") {
		t.Errorf("a login with no provider configured: %v", err)
	}
}

// `verse account -link` binds a second provider's account to who the CLI is logged in as, and
// leaves the login in use as it was.
func TestLoginLinkBindsASecondProvidersAccount(t *testing.T) {
	corp, okta := idptest.New(t), idptest.New(t)
	corp.Login(loginClient, "sub-ana", map[string]any{"email": "ana@example.com", "email_verified": true}, true)
	okta.Login(loginClient, "okta-7", nil, false)

	store := entitytest.Store(t)
	srv := versedtest.Server(t, runnerdtest.Scripted, store)
	if err := srv.SetAuth(versed.Auth{Mode: versed.AuthOptional, Providers: map[string]versed.AuthProvider{
		"corp": {Issuer: corp.URL, ClientID: loginClient, Domains: []string{"example.com"}},
		"okta": {Issuer: okta.URL, ClientID: loginClient},
	}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	cfg := filepath.Join(t.TempDir(), "verse.tony")
	body := "auth: {principal: {providers: {corp: {issuer: \"" + corp.URL + "\", clientId: " + loginClient + "}, " +
		"okta: {issuer: \"" + okta.URL + "\", clientId: " + loginClient + "}}}}\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigPath, cfg)

	// With nobody logged in there is nobody to link to.
	if _, err := runCLI(t, "account", "--link", "okta"); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("a link with no login: %v", err)
	}
	// With several providers, a login names the one.
	if _, err := runCLI(t, "account", "login"); err == nil || !strings.Contains(err.Error(), "several identity providers") {
		t.Fatalf("a login naming none of two providers: %v", err)
	}
	if _, err := runCLI(t, "account", "login", "corp"); err != nil {
		t.Fatal(err)
	}
	out, err := runLogin(t, "account", "--link", "okta")
	if err != nil || !strings.Contains(out, "linked the okta account okta-7 to user:ana@example.com") {
		t.Fatalf("account -link = %q, %v", out, err)
	}
	rec, ok, err := entity.As(store, entity.SystemIdentity).Get(entity.VerseRef("identity", "okta", "account", "okta-7"))
	if err != nil || !ok || entity.Str(rec.Payload, "principal") != "user:ana@example.com" || entity.Str(rec.Payload, "claimant") != "corp" {
		t.Fatalf("the linked binding = %v (ok=%v err=%v)", rec.Payload, ok, err)
	}
	if out, err := runCLI(t, "whoami"); err != nil || strings.TrimSpace(out) != "user:ana@example.com (corp)" {
		t.Errorf("whoami after a link = %q, %v, want the login in use unchanged", out, err)
	}
}

// A provider that has gone quiet fails the command, saying so: the CLI does not wait on it without
// end, on a request that has nothing to do with what was asked.
func TestAQuietProviderFailsTheRenewalAndDoesNotHangIt(t *testing.T) {
	_, l := serveLogin(t, true)
	if _, err := runCLI(t, "account", "login"); err != nil {
		t.Fatal(err)
	}
	expireLogin(t)
	l.Stalled = true
	old := providerWait
	providerWait = 300 * time.Millisecond
	defer func() { providerWait = old }()

	done := make(chan error, 1)
	go func() {
		_, err := runCLI(t, "whoami")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "did not answer") || !strings.Contains(err.Error(), "verse account login") {
			t.Fatalf("whoami against a quiet provider: %v, want a failure saying it did not answer", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("whoami hung on a provider that does not answer")
	}
}

// `verse account -link` at a provider whose accounts an io proves claims the account and prints the
// words to say; the verse binds it when the io hears them, so no login is needed first.
func TestLoginLinkClaimsAnAccountAnIoProves(t *testing.T) {
	store := entitytest.Store(t)
	srv := versedtest.Server(t, runnerdtest.Scripted, store)
	if err := srv.SetAuth(versed.Auth{Mode: versed.AuthOptional, Providers: map[string]versed.AuthProvider{
		"slack":  {IOs: []string{"workspace"}},
		"github": {IOs: []string{"gh"}},
	}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	cfg := filepath.Join(t.TempDir(), "verse.tony")
	body := "auth: {principal: {providers: {slack: {iods: [workspace]}, github: {iods: [gh]}}}}\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigPath, cfg)

	out, err := runLogin(t, "account", "--link", "slack")
	if err != nil || !strings.Contains(out, "/verse link ") || !strings.Contains(out, "(workspace)") || strings.Contains(out, "GitHub") {
		t.Fatalf("account -link slack = %q, %v", out, err)
	}
	nonce := strings.Fields(out[strings.Index(out, "/verse link "):])[2]
	rec, ok, err := entity.As(store, entity.SystemIdentity).Get(entity.VerseRef("identity", "slack", "claim", nonce))
	if err != nil || !ok || entity.Str(rec.Payload, "principal") != tester || entity.Str(rec.Payload, "claimant") != "asserted" {
		t.Fatalf("the claim = principal %q claimant %q, want %q asserted (ok=%v err=%v)", entity.Str(rec.Payload, "principal"), entity.Str(rec.Payload, "claimant"), tester, ok, err)
	}
	// GitHub names the login; without one the verse says so.
	if _, err := runLogin(t, "account", "--link", "github"); err == nil || !strings.Contains(err.Error(), "names the GitHub login") {
		t.Errorf("account -link github without --account: %v", err)
	}
	// A GitHub link waits for verse to read the words in a comment on any issue or pull request a
	// trusted github io reflects, and binds the account from the comment itself: the io's own
	// report hears them only in a reply on a pull request it is asking a question on
	// (git-issue zr5vx3z2h12kr6dnvhn0).
	entitytest.Set(t, store, verseio.Ref("gh"), entity.Obj{
		"name": "gh", "root": "github", "url": "http://gh:9801",
		"owns": []any{entity.Obj{"at": "github.signadot.verse"}},
	}.Payload())
	defer func(d time.Duration) { verifyEvery = d }(verifyEvery)
	verifyEvery = 50 * time.Millisecond
	sys := entity.As(store, entity.SystemIdentity)
	// says has login say the words of its claim, once the CLI has claimed, in a comment on an
	// issue the io reflects, as the reflection writes one; and then does whatever else the case
	// needs done with the claim.
	says := func(login string, authorID int64, then func(nonce string)) {
		go func() {
			nonce := awaitClaim(t, sys, login)
			ref := entity.RefAt("github", "signadot", "verse", "comment", "7", strconv.FormatInt(authorID, 10))
			if _, _, err := store.Write(entity.Write{At: ref.At(), Value: entity.Obj{
				"repo": "signadot/verse", "on": 7, "kind": "conversation", "author": login, "authorId": authorID,
				"body": "/verse link " + nonce, "url": "https://github.com/signadot/verse/issues/7#issuecomment-1",
			}.Payload()}); err != nil {
				t.Error(err)
			}
			if then != nil {
				then(nonce)
			}
		}()
	}
	says("ana-gh", 101, nil)
	out, err = runLogin(t, "account", "--link", "github", "--account", "@ana-gh")
	if err != nil || !strings.Contains(out, "any issue or pull request in signadot/verse") ||
		!strings.Contains(out, "linked the github account ana-gh (101) to "+tester) {
		t.Fatalf("account -link github --account = %q, %v", out, err)
	}
	// Linked already, it says so and claims nothing.
	if out, err := runLogin(t, "account", "--link", "github", "--account", "ana-gh"); err != nil || !strings.Contains(out, "already linked") {
		t.Errorf("account -link github of a linked account = %q, %v", out, err)
	}
	// The claim spent by another route while this waits, as the io's report spends it, ends the
	// wait at the binding it made. The CLI's own verify can get to the comment first, in which
	// case this one finds the account bound (200) and the wait ended on its own.
	verifyEvery = time.Second
	says("bo-gh", 102, func(nonce string) {
		resp, err := httpPost("/accounts/github/verify", api.AccountVerify{Nonce: nonce})
		if err != nil || (resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK) {
			t.Errorf("binding bo-gh beside the CLI: %v %v", resp, err)
		}
		if err == nil {
			resp.Body.Close()
		}
	})
	if out, err := runLogin(t, "account", "--link", "github", "--account", "bo-gh"); err != nil || !strings.Contains(out, "linked the github account bo-gh (102)") {
		t.Errorf("account -link github, bound by another route = %q, %v", out, err)
	}
	// A claim that expires before the words are read ends the wait, saying so.
	verifyEvery = 50 * time.Millisecond
	go func() {
		nonce := awaitClaim(t, sys, "cy-gh")
		ref := entity.VerseRef("identity", "github", "claim", nonce)
		rec, _, _ := sys.Get(ref)
		o := entity.ToObj(rec.Payload)
		o["expires"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		if _, _, err := sys.Write(entity.Write{At: ref.At(), Value: o.Payload()}); err != nil {
			t.Error(err)
		}
	}()
	if _, err := runLogin(t, "account", "--link", "github", "--account", "cy-gh"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("account -link github past its claim: %v", err)
	}
	// Ctrl-c ends the wait, saying the login is not linked. The CLI installs its handler before
	// it claims, so a claim the store holds is one the wait answers the signal for.
	go func() {
		awaitClaim(t, sys, "dy-gh")
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Error(err)
		}
	}()
	if _, err := runLogin(t, "account", "--link", "github", "--account", "dy-gh"); err == nil || !strings.Contains(err.Error(), "stopped waiting; dy-gh is not linked") {
		t.Errorf("account -link github, interrupted: %v", err)
	}
	// Nothing to log in to: the provider is nobody's issuer.
	if _, err := runLogin(t, "account", "login", "slack"); err == nil || !strings.Contains(err.Error(), "no issuer") {
		t.Errorf("login at an iods provider: %v", err)
	}
}

// An issuer that wants a client secret at its token endpoint gets it where the config carries
// one, and refuses a login without it in its own words.
func TestALoginSendsTheClientSecretTheIssuerWants(t *testing.T) {
	idp := idptest.New(t)
	l := idp.Login(loginClient, "sub-ana", map[string]any{"email": "ana@example.com", "email_verified": true}, true)
	l.Secret = "installed-app-secret"
	store := entitytest.Store(t)
	srv := versedtest.Server(t, runnerdtest.Scripted, store)
	if err := srv.SetAuth(versed.Auth{Mode: versed.AuthOptional, Providers: map[string]versed.AuthProvider{
		"google": {Issuer: idp.URL, ClientID: loginClient, Domains: []string{"example.com"}},
	}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	cfg := filepath.Join(t.TempDir(), "verse.tony")
	write := func(secret string) {
		t.Helper()
		body := "auth: {principal: {providers: {google: {issuer: \"" + idp.URL + "\", clientId: " + loginClient + secret + "}}}}\n"
		if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(config.EnvConfigPath, cfg)
	write("")
	if _, err := runCLI(t, "account", "login"); err == nil || !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("a login with no secret to an issuer that wants one: %v, want its refusal", err)
	}
	write(", clientSecret: installed-app-secret")
	if out, err := runCLI(t, "account", "login"); err != nil || !strings.Contains(out, "as user:ana@example.com (google)") {
		t.Fatalf("a login with the secret = %q, %v", out, err)
	}
}

// A LONG-LIVED PROCESS KEEPS NOBODY ELSE'S LOGINS OUT: what another process wrote to the tokens
// file after this one read it — a login toward another verse — survives this one renewing its own,
// since the file is read again at the request and again before the renewal is written.
func TestARenewalKeepsWhatAnotherProcessWrote(t *testing.T) {
	_, l := serveLogin(t, true)
	if _, err := runCLI(t, "account", "login"); err != nil {
		t.Fatal(err)
	}
	expireLogin(t) // and read: what this process holds is the expired login alone
	other, err := os.ReadFile(config.TokensPath())
	if err != nil {
		t.Fatal(err)
	}
	c := &tokenCache{}
	if err := codec.Unmarshal(other, c, codec.Tony); err != nil {
		t.Fatal(err)
	}
	c.Verses["elsewhere:1"] = &verseLogins{Current: "corp", Providers: map[string]*loginEntry{
		"corp": {Principal: "user:ana@example.com", IDToken: "theirs", Expiry: "2099-01-01T00:00:00Z"}}}
	b, err := codec.Marshal(c, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.TokensPath(), b, 0o600); err != nil { // another process's write
		t.Fatal(err)
	}
	if token, err := bearer(t.Context()); err != nil || token == "" || l.Refreshes != 1 {
		t.Fatalf("the renewal: %q, %v, %d renewals", token, err, l.Refreshes)
	}
	logins = nil
	after, err := loadLogins()
	if err != nil {
		t.Fatal(err)
	}
	if e := after.Verses["elsewhere:1"]; e == nil || e.Providers["corp"].IDToken != "theirs" {
		t.Fatalf("the other process's login is gone: %+v", after.Verses)
	}
}

// awaitClaim is the nonce of the GitHub claim of login, once the CLI has made it.
func awaitClaim(t *testing.T, sys entity.Store, login string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		claims, _ := entity.BelowErr(sys, func(entity.Ref) bool { return true }, 1, "verse", "identity", "github", "claim")
		for _, c := range claims {
			if strings.EqualFold(entity.Str(c.Payload, "login"), login) {
				return c.Ref.Name()
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("no claim of %s was made", login)
	return ""
}
