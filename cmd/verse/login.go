package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/scott-cotton/cli"
	"golang.org/x/oauth2"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/config"
)

// Logging in: the CLI proves who is at the keyboard to an identity provider the verse names, and
// from then on sends the id_token it was given with every request, in place of a name (git-issue
// hfh7km3th12ksd6nqsn0 §1.4).
//
// THE PROVIDERS ARE THE CONFIG'S (`auth.principal.providers`), the same block the server reads:
// the CLI logs in as the client the verse accepts tokens for. It is a public client. Nothing the
// CLI holds is a secret of the client's, and the login is proved by PKCE or by the device's code.
//
// TWO WAYS IN. Where the provider's discovery names a device authorization endpoint, the CLI
// prints a URL and a code to enter there, and waits (RFC 8628), which works on a machine with no
// browser. Otherwise it listens on a loopback port, prints the provider's authorization URL, and
// takes the code the browser is sent back with (authorization code + PKCE).
//
// THE TOKENS ARE KEPT IN A FILE beside the config, readable by its owner alone, one entry per
// verse address and provider (config.TokensPath). A file and not a keychain, because it behaves
// the same on every machine the CLI runs on, a container and CI included.
//
// THE BEARER IS SENT ONLY WHEN THE CLI ACTS AS THE PERSON WHO LOGGED IN. With `agent:` in the
// config, or `--as` on the command, it acts as somebody else, and a token beside another name is
// one the server refuses: it sends the name alone, asserted as it always was.

// refreshMargin is how long before its expiry an id_token is renewed rather than sent.
const refreshMargin = 10 * time.Second

// providerWait is how long the CLI waits on an identity provider that is not answering: for its
// discovery document, and for a renewal. A provider that has gone quiet fails the command, saying
// so, where an unbounded wait would leave it hanging on a request that has nothing to do with
// what was asked. A var so a test need not sit it out.
var providerWait = 30 * time.Second

// loginEntry is one login: the tokens a provider gave, and who the verse said they prove.
type loginEntry struct {
	// Principal is who the verse made the request as when it was shown the token.
	Principal string `tony:"field=principal,omitzero"`
	// IDToken is what is sent as the bearer. Expiry is its `exp`.
	IDToken string `tony:"field=idToken"`
	Expiry  string `tony:"field=expiry"`
	// RefreshToken renews the id_token when the provider gave one.
	RefreshToken string `tony:"field=refreshToken,omitzero"`
}

// verseLogins is the logins toward one verse: one per provider, and the provider in use.
type verseLogins struct {
	Current   string                 `tony:"field=current,omitzero"`
	Providers map[string]*loginEntry `tony:"field=providers,omitzero"`
}

// tokenCache is the tokens file: the logins, by the address of the verse they are toward.
type tokenCache struct {
	Verses map[string]*verseLogins `tony:"field=verses,omitzero"`
}

// logins is the tokens file as this invocation read it, read once.
var logins *tokenCache

// loadLogins reads the tokens file. A file that is not there is no logins.
func loadLogins() (*tokenCache, error) {
	if logins != nil {
		return logins, nil
	}
	c := &tokenCache{}
	path := config.TokensPath()
	if path == "" {
		logins = c
		return c, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		logins = c
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := codec.Unmarshal(data, c, codec.Tony); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	logins = c
	return c, nil
}

// saveLogins writes the tokens file whole, for its owner alone.
func saveLogins(c *tokenCache) error {
	path := config.TokensPath()
	if path == "" {
		return fmt.Errorf("there is nowhere to keep a login: no home directory, and no %s", config.EnvConfigPath)
	}
	data, err := codec.Marshal(c, codec.Tony)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	logins = c
	return os.Rename(tmp.Name(), path)
}

// currentLogin is the login in use toward the verse this invocation talks to, and its provider;
// nil where there is none.
func currentLogin() (*loginEntry, string, error) {
	c, err := loadLogins()
	if err != nil {
		return nil, "", err
	}
	v := c.Verses[g.Addr]
	if v == nil || v.Current == "" {
		return nil, "", nil
	}
	return v.Providers[v.Current], v.Current, nil
}

// actsAsLogin reports whether the CLI acts as the person who logged in: not with `--as`, which
// names somebody else, and not as the config's `agent:`.
func actsAsLogin() bool {
	return asOverride == "" && (appCfg == nil || appCfg.Agent == "")
}

// loginPrincipal is who the login in use proves, "" where the CLI is not acting as a login.
func loginPrincipal() string {
	if !actsAsLogin() {
		return ""
	}
	l, _, err := currentLogin()
	if err != nil || l == nil {
		return ""
	}
	return l.Principal
}

// bearer is the id_token a request carries: the login's, renewed first where it has expired, and
// "" where the CLI has no login toward this verse or acts as somebody else.
//
// THE TOKENS FILE IS READ AGAIN for each request, since a process can outlive what it read
// (`verse mcp` lives as long as its host): a login, a logout or a renewal made since, by another
// process, is the one this request goes by.
func bearer(ctx context.Context) (string, error) {
	if !actsAsLogin() {
		return "", nil
	}
	logins = nil
	l, provider, err := currentLogin()
	if err != nil {
		return "", fmt.Errorf("reading the login: %w", err)
	}
	if l == nil {
		return "", nil
	}
	if exp, err := time.Parse(time.RFC3339, l.Expiry); err == nil && time.Until(exp) > refreshMargin {
		return l.IDToken, nil
	}
	again := fmt.Sprintf("log in again with `verse account login %s`, or drop the login with `verse account logout`", provider)
	if l.RefreshToken == "" {
		return "", fmt.Errorf("the login to %s through %s has expired, and %s gave nothing to renew it with: %s",
			g.Addr, provider, provider, again)
	}
	ctx, cancel := context.WithTimeout(ctx, providerWait)
	defer cancel()
	p, err := loginSetup(ctx, provider)
	if err != nil {
		return "", fmt.Errorf("renewing the login to %s: %w", g.Addr, quiet(ctx, provider, err))
	}
	tok, err := p.conf.TokenSource(ctx, &oauth2.Token{RefreshToken: l.RefreshToken}).Token()
	if err != nil {
		return "", fmt.Errorf("the login to %s through %s has expired and was not renewed (%v): %s", g.Addr, provider, quiet(ctx, provider, err), again)
	}
	raw, expiry, err := p.idToken(ctx, tok)
	if err != nil {
		return "", fmt.Errorf("the login to %s through %s has expired, and renewing it %v: %s", g.Addr, provider, err, again)
	}
	// Kept in the file as it stands now, not as it stood before the provider answered: the file is
	// written whole, and what another process wrote in the meantime stays. A login dropped in the
	// meantime is not put back.
	logins = nil
	c, err := loadLogins()
	if err != nil {
		return "", fmt.Errorf("reading the login: %w", err)
	}
	v := c.Verses[g.Addr]
	if v == nil || v.Providers[provider] == nil {
		return raw, nil
	}
	l = v.Providers[provider]
	l.IDToken, l.Expiry = raw, expiry.UTC().Format(time.RFC3339)
	if tok.RefreshToken != "" {
		l.RefreshToken = tok.RefreshToken // a provider that rotates them gives the next one
	}
	if err := saveLogins(c); err != nil {
		return "", fmt.Errorf("keeping the renewed login: %w", err)
	}
	return raw, nil
}

// quiet says, of an error from a provider that ran out the wait, that the provider did not answer:
// the error underneath is a cancelled request, which says nothing of why.
func quiet(ctx context.Context, provider string, err error) error {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s did not answer within %s", provider, providerWait)
	}
	return err
}

// loginProvider is a provider as the CLI logs in to it: discovered, with the client's OAuth
// config and a verifier for what it gives back.
type loginProvider struct {
	name     string
	conf     oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// loginSetup discovers the named provider.
func loginSetup(ctx context.Context, name string) (*loginProvider, error) {
	p, ok := appCfg.Auth.Principal.Providers[name]
	if !ok {
		return nil, fmt.Errorf("%q is no identity provider in this config (auth.principal.providers has: %s)",
			name, providerNames())
	}
	if p.Issuer == "" {
		return nil, fmt.Errorf("%q names the iods it trusts and no issuer, so it is nobody's to log in to "+
			"(the providers to log in to: %s)", name, providerNames())
	}
	prov, err := oidc.NewProvider(ctx, p.Issuer)
	if err != nil {
		return nil, fmt.Errorf("reading %s's discovery document: %w", name, err)
	}
	var doc struct {
		Scopes []string `json:"scopes_supported"`
	}
	_ = prov.Claims(&doc)
	scopes := []string{oidc.ScopeOpenID, "email"}
	// What renews an id_token is a refresh token, which a provider gives a client that asks to
	// stay logged in. The standard way to ask is this scope, where the provider has it.
	if slices.Contains(doc.Scopes, oidc.ScopeOfflineAccess) {
		scopes = append(scopes, oidc.ScopeOfflineAccess)
	}
	return &loginProvider{
		name:     name,
		conf:     oauth2.Config{ClientID: p.ClientID, ClientSecret: p.ClientSecret, Endpoint: prov.Endpoint(), Scopes: scopes},
		verifier: prov.Verifier(&oidc.Config{ClientID: p.ClientID}),
	}, nil
}

// discover is loginSetup for a login, which waits on a person afterwards: only the discovery is
// held to the provider's wait.
func discover(ctx context.Context, name string) (*loginProvider, error) {
	ctx, cancel := context.WithTimeout(ctx, providerWait)
	defer cancel()
	p, err := loginSetup(ctx, name)
	return p, quiet(ctx, name, err)
}

// idToken is the id_token in a token endpoint's answer, checked as the server will check it, and
// when it expires.
func (p *loginProvider) idToken(ctx context.Context, tok *oauth2.Token) (string, time.Time, error) {
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return "", time.Time{}, fmt.Errorf("gave no id_token")
	}
	id, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("gave an id_token that does not verify: %w", err)
	}
	return raw, id.Expiry, nil
}

// providerNames is the config's providers, for a message that says what could be named.
func providerNames() string {
	names := make([]string, 0, len(appCfg.Auth.Principal.Providers))
	for name, p := range appCfg.Auth.Principal.Providers {
		if p.Issuer != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// pickProvider is the provider a command names, or the config's one provider when it names none.
func pickProvider(args []string, usage string) (string, error) {
	switch {
	case len(args) > 1:
		return "", fmt.Errorf("%w: %s", cli.ErrUsage, usage)
	case len(args) == 1:
		return args[0], nil
	}
	// The providers a person logs in to are the issuers. One that names the iods it trusts
	// (`github`, `slack`) issues no tokens, and is nobody's to log in to.
	provs := map[string]config.AuthProviderConfig{}
	for name, p := range appCfg.Auth.Principal.Providers {
		if p.Issuer != "" {
			provs[name] = p
		}
	}
	switch len(provs) {
	case 0:
		return "", fmt.Errorf("this config names no identity provider to log in to: set " +
			"auth.principal.providers, as the verse's own config has it")
	case 1:
		for name := range provs {
			return name, nil
		}
	}
	return "", fmt.Errorf("%w: this config names several identity providers (%s): %s", cli.ErrUsage, providerNames(), usage)
}

// obtain logs in to a provider and answers what its token endpoint gave: by the device grant
// where the provider offers one, and by authorization code with PKCE on a loopback port where it
// does not.
func (p *loginProvider) obtain(cc *cli.Context) (*oauth2.Token, error) {
	ctx := cc.Go
	if p.conf.Endpoint.DeviceAuthURL != "" {
		da, err := p.conf.DeviceAuth(ctx)
		if err != nil {
			return nil, fmt.Errorf("asking %s for a device code: %w", p.name, err)
		}
		if da.VerificationURIComplete != "" {
			fmt.Fprintf(cc.Err, "To log in, open %s\nand confirm the code %s.\n", da.VerificationURIComplete, da.UserCode)
		} else {
			fmt.Fprintf(cc.Err, "To log in, open %s\nand enter the code %s.\n", da.VerificationURI, da.UserCode)
		}
		return p.conf.DeviceAccessToken(ctx, da)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening for the browser's return: %w", err)
	}
	defer ln.Close()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	state, verifier := hex.EncodeToString(b[:]), oauth2.GenerateVerifier()
	conf := p.conf
	conf.RedirectURL = "http://" + ln.Addr().String() + "/callback"

	type result struct {
		code string
		err  error
	}
	got := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = fmt.Errorf("the browser came back with a state this login did not give out")
		case q.Get("error") != "":
			res.err = fmt.Errorf("%s refused the login: %s %s", p.name, q.Get("error"), q.Get("error_description"))
		case q.Get("code") == "":
			res.err = fmt.Errorf("the browser came back with no code")
		default:
			res.code = q.Get("code")
		}
		if res.err != nil {
			http.Error(w, "verse account login: "+res.err.Error(), http.StatusBadRequest)
		} else {
			fmt.Fprintln(w, "verse account login: logged in. You can close this window.")
		}
		select {
		case got <- res:
		default:
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	fmt.Fprintf(cc.Err, "To log in, open\n%s\n", conf.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)))
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-got:
		if res.err != nil {
			return nil, res.err
		}
		return conf.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	}
}

// whoIs asks the verse who a request is from, and how it knows.
func whoIs() (principal, by string, err error) {
	resp, err := httpGet("/whoami", nil)
	if err != nil {
		return "", "", fmt.Errorf("whoami: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", statusError(resp, "whoami")
	}
	var out struct {
		Principal string `tony:"field=principal"`
		By        string `tony:"field=by"`
	}
	decodeClose(resp, &out)
	return out.Principal, out.By, nil
}

// accountConfig is `verse account`: who you are to this verse, and the accounts in other systems
// that are you. Bare, it lists your bindings; `-link` binds one more; `login` and `logout` are
// its verbs.
type accountConfig struct {
	*cli.Command
	Link    string `cli:"name=link desc='bind your account at this provider to who you are: a login there, or, at a provider whose accounts an io proves (slack, github), a nonce to say from the account'"`
	Account string `cli:"name=account desc='with -link, the account you claim at a provider whose accounts an io proves (a GitHub login); Slack needs none'"`
}

func accountCmd() *cli.Command {
	cfg := &accountConfig{}
	return cli.NewCommandAt(&cfg.Command, "account").
		WithAliases("accounts").
		WithSynopsis("account | account login [<provider>] | account logout [<provider>] | account -link <provider> [-account <login>] — "+
			"who you are to this verse: bare, the accounts in other systems bound to you; login proves who you are to an "+
			"identity provider the verse names; -link binds an account in another system to who you are").
		WithOpts(mustOpts(cfg)...).
		WithSubs(loginCmd(), logoutCmd()).
		WithRun(cfg.run)
}

func (cfg *accountConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if handled, err := dispatch(cfg.Command, cc, args); handled {
		return err
	}
	if cfg.Link != "" {
		if len(args) > 0 {
			return fmt.Errorf("%w: account -link <provider> names the one provider", cli.ErrUsage)
		}
		if appCfg.Agent != "" {
			return fmt.Errorf("account -link: this CLI acts as the agent %q (`agent:` in its config), and an account is a person's", appCfg.Agent)
		}
		if p, ok := appCfg.Auth.Principal.Providers[cfg.Link]; ok && p.Issuer == "" && len(p.IOs) > 0 {
			return claimThrough(cc, cfg.Link, cfg.Account)
		}
		if cfg.Account != "" {
			return fmt.Errorf("%w: -account goes with a -link provider whose accounts an io proves", cli.ErrUsage)
		}
		return link(cc, cfg.Link)
	}
	if cfg.Account != "" {
		return fmt.Errorf("%w: -account goes with -link", cli.ErrUsage)
	}
	if err := noArgs(cfg.Command, "account", args); err != nil {
		return err
	}
	resp, err := httpGet("/accounts", nil)
	if err != nil {
		return fmt.Errorf("account: %w", err)
	}
	return printBody(cc, resp)
}

type loginConfig struct{ *cli.Command }

func loginCmd() *cli.Command {
	cfg := &loginConfig{}
	return cli.NewCommandAt(&cfg.Command, "login").
		WithSynopsis("login [<provider>] — prove who you are to an identity provider this verse names, " +
			"and act as that person from then on").
		WithRun(cfg.run)
}

func (cfg *loginConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if appCfg.Agent != "" {
		return fmt.Errorf("login: this CLI acts as the agent %q (`agent:` in its config), and a login is a person's", appCfg.Agent)
	}
	name, err := pickProvider(args, "account login [<provider>]")
	if err != nil {
		return err
	}
	p, err := discover(cc.Go, name)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	tok, err := p.obtain(cc)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	raw, expiry, err := p.idToken(cc.Go, tok)
	if err != nil {
		return fmt.Errorf("login: %s %w", name, err)
	}

	c, err := loadLogins()
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	prev := c.Verses[g.Addr].clone()
	entry := &loginEntry{IDToken: raw, Expiry: expiry.UTC().Format(time.RFC3339), RefreshToken: tok.RefreshToken}
	c.use(g.Addr, name, entry)
	if err := saveLogins(c); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	// The verse has the last word on who the token proves: its binding, or the first sight it
	// makes now. A token it refuses is no login, so the one before it is put back.
	who, by, err := whoIs()
	if err == nil && by != name {
		err = fmt.Errorf("the verse at %s did not take the token: it answers as %q", g.Addr, who)
	}
	if err != nil {
		c.Verses[g.Addr] = prev
		if c.Verses[g.Addr] == nil {
			delete(c.Verses, g.Addr)
		}
		_ = saveLogins(c)
		return fmt.Errorf("login: %w", err)
	}
	entry.Principal = who
	if err := saveLogins(c); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	fmt.Fprintf(cc.Out, "logged in to %s as %s (%s)\n", g.Addr, who, by)
	return nil
}

// link binds the caller's account at a second provider to who they are logged in as: it logs in
// there, and presents the id_token it is given to the verse as evidence of the account. The
// login in use stays the one it was.
func link(cc *cli.Context, name string) error {
	l, current, err := currentLogin()
	if err != nil {
		return fmt.Errorf("account -link: %w", err)
	}
	if l == nil {
		return fmt.Errorf("account -link: an account at an identity provider is linked to who you are logged in "+
			"as, and this CLI is not logged in to %s: `verse account login` first", g.Addr)
	}
	if name == current {
		return fmt.Errorf("account -link: %s is the provider you are logged in through", name)
	}
	p, err := discover(cc.Go, name)
	if err != nil {
		return fmt.Errorf("account -link: %w", err)
	}
	tok, err := p.obtain(cc)
	if err != nil {
		return fmt.Errorf("account -link: %w", err)
	}
	raw, _, err := p.idToken(cc.Go, tok)
	if err != nil {
		return fmt.Errorf("account -link: %s %w", name, err)
	}
	resp, err := httpPost("/accounts/"+url.PathEscape(name)+"/link", map[string]any{"idToken": raw})
	if err != nil {
		return fmt.Errorf("account -link: %w", err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return statusError(resp, "account -link")
	}
	var row struct {
		Principal string `tony:"field=principal"`
		Sub       string `tony:"field=sub"`
	}
	decodeClose(resp, &row)
	fmt.Fprintf(cc.Out, "linked the %s account %s to %s\n", name, row.Sub, row.Principal)
	return nil
}

// claimThrough claims the caller's account at a provider whose accounts an io proves, and prints
// the words to say from it. The verse binds the account when a trusted io hears them said, so
// there is nothing to wait on here; and the claim is whoever the CLI acts as, logged in or not,
// since a verse that takes names as asserted takes this binding the same way (the binding's
// `claimant` says which).
func claimThrough(cc *cli.Context, name, account string) error {
	body := map[string]any{}
	if account != "" {
		body["login"] = strings.TrimPrefix(account, "@")
	}
	resp, err := httpPost("/accounts/"+url.PathEscape(name), body)
	if err != nil {
		return fmt.Errorf("account -link: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return statusError(resp, "account -link")
	}
	var claim struct {
		Say     string `tony:"field=say"`
		Expires string `tony:"field=expires"`
	}
	decodeClose(resp, &claim)
	iods := strings.Join(appCfg.Auth.Principal.Providers[name].IOs, ", ")
	fmt.Fprintf(cc.Out, "say this from your %s account, where the io it trusts (%s) hears you, "+
		"on a line of its own:\n\n    %s\n\nin Slack, a message to the app; on GitHub, a comment on "+
		"a pull request the io reflects. The verse binds the account when the io reports it, "+
		"and the words stand until %s.\n", name, iods, claim.Say, claim.Expires)
	return nil
}

// use makes entry the login in use toward addr, through provider.
func (c *tokenCache) use(addr, provider string, entry *loginEntry) {
	if c.Verses == nil {
		c.Verses = map[string]*verseLogins{}
	}
	v := c.Verses[addr]
	if v == nil {
		v = &verseLogins{}
		c.Verses[addr] = v
	}
	if v.Providers == nil {
		v.Providers = map[string]*loginEntry{}
	}
	v.Providers[provider], v.Current = entry, provider
}

// clone copies a verse's logins, so a login that fails can put back what stood before it.
func (v *verseLogins) clone() *verseLogins {
	if v == nil {
		return nil
	}
	out := &verseLogins{Current: v.Current, Providers: map[string]*loginEntry{}}
	for name, e := range v.Providers {
		cp := *e
		out.Providers[name] = &cp
	}
	return out
}

type logoutConfig struct{ *cli.Command }

func logoutCmd() *cli.Command {
	cfg := &logoutConfig{}
	return cli.NewCommandAt(&cfg.Command, "logout").
		WithSynopsis("logout [<provider>] — drop the login toward this verse: every provider's, or the one named").
		WithRun(cfg.run)
}

func (cfg *logoutConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return fmt.Errorf("%w: logout [<provider>]", cli.ErrUsage)
	}
	c, err := loadLogins()
	if err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	v := c.Verses[g.Addr]
	if v == nil || len(v.Providers) == 0 {
		fmt.Fprintf(cc.Out, "not logged in to %s\n", g.Addr)
		return nil
	}
	if len(args) == 1 {
		if _, ok := v.Providers[args[0]]; !ok {
			return fmt.Errorf("logout: not logged in to %s through %s", g.Addr, args[0])
		}
		delete(v.Providers, args[0])
		if v.Current == args[0] {
			v.Current = ""
			for name := range v.Providers {
				if v.Current == "" || name < v.Current {
					v.Current = name
				}
			}
		}
	}
	if len(args) == 0 || len(v.Providers) == 0 {
		delete(c.Verses, g.Addr)
	}
	if err := saveLogins(c); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	fmt.Fprintf(cc.Out, "logged out of %s\n", g.Addr)
	return nil
}

func whoamiCmd() *cli.Command {
	return leaf("whoami", "whoami — who this verse takes you for, and how it knows: the identity provider that verified you, or `asserted`",
		func(cc *cli.Context, args []string) error {
			who, by, err := whoIs()
			if err != nil {
				return err
			}
			if who == "" {
				fmt.Fprintln(cc.Out, "nobody: this CLI names no user toward "+g.Addr+" (`verse account login`, or `user:` in its config)")
				return nil
			}
			fmt.Fprintf(cc.Out, "%s (%s)\n", who, by)
			return nil
		})
}
