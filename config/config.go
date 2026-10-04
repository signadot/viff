// Package config loads verse's layered configuration. Precedence, lowest first:
//
//	/etc/verse.{tony,json,yaml}         system base
//	~/.config/verse.{tony,json,yaml}    per-user — PATCHES the base (a merge, not replace)
//	environment variables               override the file
//	CLI flags                           override env (applied by the commands, not here)
//
// A LAYER'S FORMAT COMES FROM ITS FILE NAME, and the two layers need not agree:
// /etc/verse.json patched by ~/.config/verse.tony merges fine, because both are parsed to
// ir.Node before tony.Patch ever sees them and the dialect is gone by that point. This is
// also the only place the bootstrapping question has a clean answer — a config file cannot
// declare its own format, so the suffix declares it.
//
// The user layer patches per key, so ~/.config need only carry what it changes; everything
// else falls through to /etc. Missing files are absent layers, not errors. ir.Node appears
// only at the parse/merge boundary; the rest of verse consumes the typed Config.
package config

//go:generate go tool tony-codegen -dir .

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/format"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
)

// SystemPath is the system-wide config layer. It is a BASE path — the suffix is probed,
// so /etc/verse.tony, .json and .yaml are all found. A path that already carries a known
// suffix is used as-is, which is what lets a test point at one specific file.
// A var so tests can point it elsewhere.
var SystemPath = "/etc/verse"

// EnvConfigPath names a single config file that REPLACES both layers. It exists so a test
// or a container can be hermetic without reaching for /etc and $HOME.
const EnvConfigPath = "VERSE_CONFIG"

// Config is the resolved configuration (file layers + env). CLI flags override these
// afterward, in the commands.
//
//tony:schemagen=verse-config,notag
type Config struct {
	Addr  string `tony:"field=addr,omitzero"`
	Scope string `tony:"field=scope,omitzero"`
	// User is the principal this CLI acts as: who every request is made by, and so who every
	// write it causes is attributed to and who owns what it creates. An email for a person,
	// which goes on the wire as `user:<email>`; a principal of another kind spelled as one
	// (`runner:coderunner`). Self-asserted, and `--as` overrides it on the commands that take one.
	// A login (`verse account login`) comes before it: the CLI then acts as whoever the login proves.
	// Env: VERSE_USER.
	User string `tony:"field=user,omitzero"`
	// Agent is the operator agent this CLI acts as, when it is one: a harness driving verse
	// through the api is a principal of its own, and not the person whose terminal it runs in.
	// When set, every request is made as `agent:<name>` rather than as User; `--as` still overrides it where a command takes one. Env: VERSE_AGENT.
	Agent string `tony:"field=agent,omitzero"`
	// SystemCharter is the path of a charter file versed installs at start AS THE SYSTEM — the
	// rules the deployment itself stands behind, such as the one that watches policy change.
	// A system-owned charter has to be installed by something, and `--as system` over the api
	// is exactly the self-assertion that stops being fine once writes are refused; so the file
	// the deployment ships is the way. Server-side, read by `verse up`. Env: VERSE_SYSTEM_CHARTER.
	SystemCharter string       `tony:"field=systemCharter,omitzero"`
	Format        FormatConfig `tony:"field=format,omitzero"`
	LLM           LLMConfig    `tony:"field=llm,omitzero"`
	// TripDuration is D for the cycle throttle: how long a firing waits when its causal chain
	// has run past the point where some rule must have fired twice about one subject. Empty
	// means the default; "0" REFUSES the firing instead of pacing it, which stops a runaway
	// dead rather than letting it hum.
	//
	// A duration string ("1s", "500ms"). Server-side, read by `verse up`.
	TripDuration string `tony:"field=tripDuration,omitzero"`
	// Perms is the part of the permission check's policy that is a setting rather than a
	// record. Server-side, read by `verse up`.
	Perms PermsConfig `tony:"field=perms,omitzero"`
	// ShellEnv names the variables a local task's shell (run_bash, under the llm, calls and
	// scripted drivers) passes through from the server's environment. Everything else is unset,
	// the server's model credentials included. Empty means task.DefaultShellEnv. Server-side,
	// read by `verse up`.
	ShellEnv []string `tony:"field=shellEnv,omitzero"`
	// Auth is how the parties that arrive at a verse are authenticated, one block per kind of
	// party. Unset, a request is whoever it says it is.
	Auth AuthConfig `tony:"field=auth,omitzero"`
}

// AuthConfig holds one block per kind of party that arrives at a verse. `principal` is the
// people.
//
//tony:schemagen=verse-auth-config,notag
type AuthConfig struct {
	// Principal is how a person is authenticated: the identity providers whose id_tokens this
	// verse accepts. The server reads it to verify a request's bearer, and the CLI reads it to
	// know which providers `verse account login` can log in to.
	Principal PrincipalAuthConfig `tony:"field=principal,omitzero"`
	// Iod is how an io is authenticated: the issuer of the workload tokens the deployment's ios
	// present, and which io each token's subject is. Server-side, read by `verse up`.
	Iod IodAuthConfig `tony:"field=iod,omitzero"`
	// Issuer makes this verse an issuer of its own: it signs a short-lived token for a task whose
	// gate passed, and publishes the keys a system outside verse verifies that token against.
	// Unlike the blocks beside it, this is verse vouching outward, not a party arriving.
	// Server-side, read by `verse up`.
	Issuer IssuerConfig `tony:"field=issuer,omitzero"`
}

// IssuerConfig is what a verse needs to issue tokens: where its relying parties reach it, and
// the key it signs with.
//
//tony:schemagen=verse-issuer-config,notag
type IssuerConfig struct {
	// URL is this verse as a relying party reaches it. It is every token's `iss`, and the
	// discovery document is found at `<url>/.well-known/openid-configuration`.
	URL string `tony:"field=url,omitzero"`
	// Key is the path of the signing key: an RSA private key in PEM, PKCS#1 or PKCS#8.
	Key string `tony:"field=key,omitzero"`
}

// The modes of auth.principal.
const (
	// AuthOff verifies nobody: a request is whoever its Verse-Principal header says.
	AuthOff = "off"
	// AuthOptional uses a verified bearer where a request carries one, and still admits a bare
	// header.
	AuthOptional = "optional"
	// AuthRequired admits a `user:` principal only from a verified bearer.
	AuthRequired = "required"
)

// PrincipalAuthConfig is the OpenID Connect providers a verse accepts people from, and how far
// it insists on them.
//
//tony:schemagen=verse-principal-auth-config,notag
type PrincipalAuthConfig struct {
	// Mode is `off`, `optional` or `required`; unset is `off`. Server-side, read by `verse up`.
	Mode string `tony:"field=mode,omitzero"`
	// Providers are the issuers, each under the name this deployment gives it. The name is an
	// address segment: an account's binding stands at `verse.identity.<name>.account.<sub>`.
	Providers map[string]AuthProviderConfig `tony:"field=providers,omitzero"`
}

// AuthProviderConfig is one OpenID Connect issuer.
//
//tony:schemagen=verse-auth-provider-config,notag
type AuthProviderConfig struct {
	// Issuer is the issuer's URL, which its discovery document is found under and which every
	// token's `iss` equals.
	Issuer string `tony:"field=issuer,omitzero"`
	// ClientID is the OAuth client registered at the issuer for this verse: a token's `aud` holds
	// it, and the CLI logs in as it. It is a public client.
	ClientID string `tony:"field=clientId,omitzero"`
	// ClientSecret is what some issuers hand a public client beside its id and want back at the
	// token endpoint (Google's installed-app and device flows do). It is not a confidential
	// secret: every copy of the CLI carries it, as RFC 8252 §8.5 says of such clients. The CLI
	// sends it where it is set; the server never reads it.
	ClientSecret string `tony:"field=clientSecret,omitzero"`
	// Domains are the email domains a person may arrive from the first time: a token nobody has
	// been bound from before needs a verified email in one of them, and a person an io relays
	// needs an email in one of them. Empty admits nobody new.
	Domains []string `tony:"field=domains,omitzero"`
	// IOs names the installed ios the verse trusts for this provider's accounts, by the name each
	// is installed under: an io whose reflection is evidence of an account (`github`, proved by
	// a comment), and an io that relays a person it identified (`slack`, by the member who
	// typed). A provider has an issuer or ios, not both. Empty trusts none.
	IOs []string `tony:"field=ios,omitzero"`
	// Acts is what a trusted io may do as a person it relays: `control` (unset), the reads and
	// the acts that answer, say and steer, which is all an io does; or `any`.
	Acts string `tony:"field=acts,omitzero"`
}

// The acts a trusted io may do as a person it relays.
const (
	// ActsControl is the reads, and the acts that answer a question, say and steer. Unset means it.
	ActsControl = "control"
	// ActsAny is every act.
	ActsAny = "any"
)

// IodProvider is the name the ios' accounts stand under (`verse.identity.iod`), which no
// provider of people may take.
const IodProvider = "iod"

// GitHubProvider is the provider GitHub accounts are bound under. GitHub issues no id_token for
// a person, so it is never an OpenID Connect issuer: its entry names the ios whose reflected
// comments are evidence of its accounts.
const GitHubProvider = "github"

// IodAuthConfig is how the deployment's ios are authenticated at the door.
//
//tony:schemagen=verse-iod-auth-config,notag
type IodAuthConfig struct {
	// Issuer is the URL of the issuer of the ios' workload tokens: on Kubernetes the cluster's
	// own, which signs a pod's projected service-account token (on EKS,
	// `https://oidc.eks.<region>.amazonaws.com/id/<id>`). Its discovery document is under it.
	Issuer string `tony:"field=issuer,omitzero"`
	// Audience is what a token's `aud` holds, which is what the pod's projected token was asked
	// for. Unset is `verse`.
	Audience string `tony:"field=audience,omitzero"`
	// Accounts maps a token's subject (`system:serviceaccount:<namespace>:<name>`) to the io it
	// is. Written at start, never at first sight.
	Accounts map[string]IodAccountConfig `tony:"field=accounts,omitzero"`
	// Static is the ios of a deployment with no issuer: each presents the token in a file, and
	// the verse keeps the token's hash. Read at start.
	Static []IodStaticConfig `tony:"field=static,omitzero"`
}

// IodAccountConfig is one io, as a workload token names it.
//
//tony:schemagen=verse-iod-account-config,notag
type IodAccountConfig struct {
	// Principal is the `agent:` the io acts as.
	Principal string `tony:"field=principal"`
	// IO is the name the io is installed under, which is how a provider's `ios` names it. An io
	// that relays nobody needs none.
	IO string `tony:"field=io,omitzero"`
}

// IodStaticConfig is one io that presents a fixed token.
//
//tony:schemagen=verse-iod-static-config,notag
type IodStaticConfig struct {
	// File is the path of the token.
	File string `tony:"field=file"`
	// Principal is the `agent:` the io acts as.
	Principal string `tony:"field=principal"`
	// IO is the name the io is installed under.
	IO string `tony:"field=io,omitzero"`
}

// Validate refuses an auth block that names no usable provider or mode. It runs at load, where
// the refusal is a line in a config file, and the server runs it again on what it is handed, so
// the rules live here once.
func (a AuthConfig) Validate() error {
	if (a.Issuer.URL == "") != (a.Issuer.Key == "") {
		return fmt.Errorf("auth.issuer: an issuer has both a url and a key")
	}
	if a.Issuer.URL != "" {
		if u, err := url.Parse(a.Issuer.URL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("auth.issuer.url: %q is not a URL a relying party can fetch from", a.Issuer.URL)
		}
	}
	p := a.Principal
	switch p.Mode {
	case "", AuthOff, AuthOptional, AuthRequired:
	default:
		return fmt.Errorf("auth.principal.mode: %q is not a mode: %s, %s or %s",
			p.Mode, AuthOff, AuthOptional, AuthRequired)
	}
	issuers := map[string]string{}
	for name, prov := range p.Providers {
		at := "auth.principal.providers." + name
		if strings.TrimSpace(name) != name || name == "" || strings.ContainsAny(name, ".*/ ") {
			return fmt.Errorf("%s: a provider's name is one plain address segment", at)
		}
		if name == IodProvider {
			return fmt.Errorf("%s: %q is the ios' own, whose accounts are auth.iod's; give the provider another name", at, name)
		}
		byIssuer, byIOs := prov.Issuer != "" || prov.ClientID != "", len(prov.IOs) > 0
		switch {
		case byIssuer && byIOs:
			return fmt.Errorf("%s: a provider has an issuer, whose token is its evidence, or ios, "+
				"whose word is; not both", at)
		case !byIssuer && !byIOs:
			return fmt.Errorf("%s: a provider has an issuer and a clientId, or names the ios it "+
				"trusts", at)
		case name == GitHubProvider && byIssuer:
			return fmt.Errorf("%s: %q is the provider GitHub accounts are bound under, by proof "+
				"by action, and GitHub is no OpenID Connect issuer for people: its entry takes "+
				"`ios`; give an issuer another name", at, name)
		case byIssuer && prov.Acts != "":
			return fmt.Errorf("%s.acts: what a trusted io may do goes with `ios`", at)
		}
		switch prov.Acts {
		case "", ActsControl, ActsAny:
		default:
			return fmt.Errorf("%s.acts: %q is not %s or %s", at, prov.Acts, ActsControl, ActsAny)
		}
		if byIOs {
			continue
		}
		if prov.ClientID == "" {
			return fmt.Errorf("%s.clientId is required: a token is accepted for one client", at)
		}
		u, err := url.Parse(prov.Issuer)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("%s.issuer: %q is not an issuer's URL", at, prov.Issuer)
		}
		if other, dup := issuers[prov.Issuer]; dup {
			return fmt.Errorf("%s.issuer: %s is %s's issuer too, and a token's issuer names one "+
				"provider", at, prov.Issuer, other)
		}
		issuers[prov.Issuer] = name
	}
	return a.Iod.validate(issuers)
}

// validate refuses an iod block that names no usable issuer or io.
func (c IodAuthConfig) validate(issuers map[string]string) error {
	if c.Issuer != "" {
		u, err := url.Parse(c.Issuer)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("auth.iod.issuer: %q is not an issuer's URL", c.Issuer)
		}
		if other, dup := issuers[c.Issuer]; dup {
			return fmt.Errorf("auth.iod.issuer: %s is %s's issuer, and a token's issuer names one "+
				"party", c.Issuer, other)
		}
	}
	if len(c.Accounts) > 0 && c.Issuer == "" {
		return fmt.Errorf("auth.iod.accounts: an account is a token's subject, and no issuer of " +
			"those tokens is named (auth.iod.issuer)")
	}
	for sub, acct := range c.Accounts {
		if sub == "" {
			return fmt.Errorf("auth.iod.accounts: an account is keyed by the token's subject")
		}
		if err := ioPrincipal(acct.Principal); err != nil {
			return fmt.Errorf("auth.iod.accounts.%q.principal: %w", sub, err)
		}
	}
	for i, st := range c.Static {
		if strings.TrimSpace(st.File) == "" {
			return fmt.Errorf("auth.iod.static[%d].file: the path of the token is required", i)
		}
		if err := ioPrincipal(st.Principal); err != nil {
			return fmt.Errorf("auth.iod.static[%d].principal: %w", i, err)
		}
	}
	return nil
}

// ioPrincipal refuses a principal that is not an io's: an io is an `agent:`.
func ioPrincipal(p string) error {
	kind, id, ok := strings.Cut(p, ":")
	if !ok || kind != "agent" || id == "" || strings.ContainsAny(id, "*/ ") {
		return fmt.Errorf("%q is not an io's principal, which is `agent:<name>`", p)
	}
	return nil
}

// PermsConfig is what a deployment sets about permissions that no record can say: the root's
// default. Every other default is a record (`verse perms <path> --child-grants`).
//
//tony:schemagen=verse-perms-config,notag
type PermsConfig struct {
	// ChildGrants is what a node created with no default above it holds out — grantees by level,
	// `{read: ["*"]}` — the open verse's when left unset, and `{}` for a verse where what
	// anybody creates is theirs and their grantees' alone.
	ChildGrants map[string][]string `tony:"field=child-grants,omitzero"`
	// Admins are who may take a top-level place: grantees (`scott@example.com`, `agent:ops`),
	// seeded into the `admin` group and held `owner` beneath the root. Unset is the open verse
	// — anybody takes one — which is what a dogfood deployment runs as. Env:
	// VERSE_PERMS_ADMINS, comma-separated, which is how a cluster with no verse.tony in the pod
	// says it (and how it is changed live, with `kubectl set env`).
	//
	// It is a SETTING rather than a record because it is the one policy that has to exist
	// before anybody can be refused: a verse whose top is administered cannot be administered
	// into existence from inside itself.
	Admins []string `tony:"field=admins,omitzero"`
}

// TripPace resolves TripDuration: the duration, whether it was SET at all, and any parse error.
//
// The default is not decided here. An unset value reports set=false and the engine keeps its own
// default (trigger.DefaultTripPace), so the number lives with the behaviour it paces rather than
// being duplicated into a config package that would then have to be kept in step with it.
//
// A malformed value is an ERROR rather than a silent fallback, because a server that quietly ran
// at a pace nobody asked for is how a charter comes to hammer a store with an operator believing
// otherwise.
func (c Config) TripPace() (d time.Duration, set bool, err error) {
	if c.TripDuration == "" {
		return 0, false, nil
	}
	d, err = time.ParseDuration(c.TripDuration)
	if err != nil {
		return 0, false, fmt.Errorf("tripDuration %q: %w", c.TripDuration, err)
	}
	if d < 0 {
		return 0, false, fmt.Errorf("tripDuration %q: must not be negative", c.TripDuration)
	}
	return d, true, nil
}

// FormatConfig selects the representation. Three knobs rather than one because wanting tony
// on the wire and JSON on stdout — to pipe into jq — is an entirely reasonable thing to
// want, and a single knob makes you give up both.
//
// The fields are POINTERS because format.Format's zero value IS tony, so a plain value
// cannot distinguish "set to tony" from "not set". Typing them as codec.Format rather than
// string also means a typo in verse.tony is a load error rather than a silent fallback to
// the default, which is the entire point of having written it down.
//
//tony:schemagen=verse-format,notag
type FormatConfig struct {
	// Spelled format.Format, not codec.Format: they are the SAME type (codec.Format is
	// an alias) but tony-codegen cannot resolve an alias across a package boundary
	// (tony-format#f69agjyeh12ksa25cnn0 item 14). Everything else keeps saying
	// codec.Format — this is spelling, not semantics.
	Default *format.Format `tony:"field=default,omitzero"` // everything not separately set
	Output  *format.Format `tony:"field=output,omitzero"`  // CLI stdout
	Wire    *format.Format `tony:"field=wire,omitzero"`    // HTTP bodies
}

// validate rejects a format the file layer set to something verse cannot render.
//
// The env path gets this from codec.ParseOutput, but a config FILE decodes straight into
// *format.Format through go-tony's own text unmarshaler, which knows nothing of verse's
// rules — so `format: {output: json}` would otherwise sit there until the first response
// failed to encode. Named at load, it is a line number in a config file instead.
func (f FormatConfig) validate() error {
	for _, k := range []struct {
		field string
		v     *format.Format
	}{{"default", f.Default}, {"output", f.Output}, {"wire", f.Wire}} {
		if k.v != nil && !codec.Writable(*k.v) {
			return fmt.Errorf("format.%s: %s", k.field, codec.ErrNotWritable)
		}
	}
	return nil
}

// ResolveDefault is the format for everything not separately overridden.
func (f FormatConfig) ResolveDefault() codec.Format {
	if f.Default != nil {
		return *f.Default
	}
	return codec.Baseline
}

// ResolveOutput is the format for CLI output, falling back to the default.
func (f FormatConfig) ResolveOutput() codec.Format {
	if f.Output != nil {
		return *f.Output
	}
	return f.ResolveDefault()
}

// ResolveWire is the format for HTTP bodies, falling back to the default.
func (f FormatConfig) ResolveWire() codec.Format {
	if f.Wire != nil {
		return *f.Wire
	}
	return f.ResolveDefault()
}

// LLMConfig configures the llm task driver. When creds are absent the driver is
// unavailable and an llm spawn fails (that spawn, not the engine).
//
//tony:schemagen=verse-llm,notag
type LLMConfig struct {
	BaseURL    string `tony:"field=baseURL,omitzero"`
	APIKey     string `tony:"field=apiKey,omitzero"`
	VirtualKey string `tony:"field=virtualKey,omitzero"`
	Model      string `tony:"field=model,omitzero"`
}

// VirtualKeyOr is the credential a task runs under: the one its spawn named, else this verse's
// own. A spawn's is already the resolved key — versed read it from the address the spawn named,
// as whoever named it — so there is nothing to look up here and no list to keep.
func (l LLMConfig) VirtualKeyOr(named string) string {
	if named != "" {
		return named
	}
	return l.VirtualKey
}

// Available reports whether the llm driver can be built — an API key is configured.
// The virtual key is optional (Portkey routing); requiring it would wrongly fall back
// to scripted for a direct-key setup.
func (l LLMConfig) Available() bool { return l.APIKey != "" }

// Status is a secrets-safe summary for /status: whether the llm driver is usable, the model
// and base URL (not secret), and MASKED key fragments — enough to tell which key is set (and
// catch a stale/typo'd one) without exposing it. The full keys never leave the task.
func (l LLMConfig) Status() *api.LLM {
	return &api.LLM{
		Available:  l.Available(),
		APIKey:     maskSecret(l.APIKey),
		VirtualKey: maskSecret(l.VirtualKey),
		Model:      l.Model,
		BaseURL:    l.BaseURL,
	}
}

// maskSecret elides a secret to "unset", "set" (too short to mask safely), or a
// first-4…last-4 fragment.
func maskSecret(s string) string {
	switch {
	case s == "":
		return "unset"
	case len(s) <= 12:
		return "set"
	default:
		return s[:4] + "…" + s[len(s)-4:]
	}
}

// UserPath is the per-user config layer, ~/.config/verse (honoring XDG_CONFIG_HOME), or ""
// if there is no home. Like SystemPath it is a BASE path — the suffix is probed.
func UserPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "verse")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "verse")
}

// TokensPath is where the CLI keeps what `verse account login` was given: `verse-tokens.tony` beside the
// config, which is beside the file VERSE_CONFIG names when it names one and beside the per-user
// layer otherwise. "" where there is no home to keep it in.
func TokensPath() string {
	const name = "verse-tokens.tony"
	if explicit := os.Getenv(EnvConfigPath); explicit != "" {
		return filepath.Join(filepath.Dir(explicit), name)
	}
	if u := UserPath(); u != "" {
		return filepath.Join(filepath.Dir(u), name)
	}
	return ""
}

// resolveLayer finds the one file a layer's base path names, probing the known suffixes.
// A base that already carries a recognized suffix is taken literally.
//
// TWO FILES FOR ONE LAYER IS AN ERROR, not a precedence rule. Silently preferring
// verse.tony over verse.json is the kind of thing you debug at 2am after editing the one
// that is being ignored; saying so costs a line and a puzzled moment now.
func resolveLayer(base string) (string, error) {
	if base == "" {
		return "", nil
	}
	if _, named := codec.ForPathStrict(base); named {
		if exists(base) {
			return base, nil
		}
		return "", nil
	}
	var found []string
	for _, suffix := range codec.Suffixes() {
		if p := base + suffix; exists(p) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("ambiguous config layer: %s — keep one",
			strings.Join(found, ", "))
	}
}

func exists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// Load reads and merges the file layers, then folds in environment overrides. It
// ALWAYS returns a usable config with env applied, even when the file layer fails: a
// broken or partial ~/.config/verse.tony must never silently drop env-provided values
// (addr, scope, llm creds) and, say, strand you on the scripted driver. On a file
// error it returns that config plus the error (for the caller to warn about).
func Load() (*Config, error) {
	c := &Config{}
	merged, err := loadMerged()
	if err == nil && merged != nil {
		if err = gomap.FromTonyIR(merged, c); err != nil {
			c = &Config{} // bad file: discard the partial parse, fall back to env below
		}
	}
	envErr := c.applyEnv() // env always wins over the file — and applies even if the file broke
	if err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if envErr != nil {
		return c, fmt.Errorf("config: %w", envErr)
	}
	if err := c.Format.validate(); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if err := c.Auth.Validate(); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

// loadMerged parses the system layer then patches it with the user layer (each optional).
// VERSE_CONFIG, when set, replaces both.
//
// The layers are merged as ir.Node, so they may be written in different dialects — the
// format is gone by the time tony.Patch sees them.
func loadMerged() (*ir.Node, error) {
	if explicit := os.Getenv(EnvConfigPath); explicit != "" {
		n, err := readNode(explicit)
		if err != nil {
			return nil, fmt.Errorf("config %s (%s): %w", explicit, EnvConfigPath, err)
		}
		return n, nil
	}

	basePath, err := resolveLayer(SystemPath)
	if err != nil {
		return nil, err
	}
	overPath, err := resolveLayer(UserPath())
	if err != nil {
		return nil, err
	}

	base, err := readNode(basePath)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", basePath, err)
	}
	over, err := readNode(overPath)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", overPath, err)
	}
	switch {
	case base == nil:
		return over, nil
	case over == nil:
		return base, nil
	default:
		return tony.Patch(base, over)
	}
}

// readNode parses a config file into a node, reading it in the dialect its name declares.
// A missing or empty file is no layer (nil, nil) rather than an error — an absent layer
// must not assert null over the layer beneath it, which is why this uses ParseNodeOrNil.
func readNode(path string) (*ir.Node, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return codec.ParseNodeOrNil(data, codec.ForPath(path))
}

// applyEnv overlays environment variables on top of the file config. A malformed format
// name is reported rather than silently ignored — writing VERSE_FORMAT=tny and quietly
// getting something else is worse than being told.
//
// All three knobs name a format to WRITE in (Default is what Output and Wire fall back
// to), so all three go through codec.ParseOutput and all three refuse "json". Reading
// JSON needs no setting at all — it is parsed as tony wherever it turns up.
func (c *Config) applyEnv() error {
	c.Addr = envOr("VERSE_ADDR", c.Addr)
	c.Scope = envOr("VERSE_SCOPE", c.Scope)
	c.User = envOr("VERSE_USER", c.User)
	c.Agent = envOr("VERSE_AGENT", c.Agent)
	c.SystemCharter = envOr("VERSE_SYSTEM_CHARTER", c.SystemCharter)
	// Comma-separated, because an env var is one string and a deployment sets this one without
	// a config file: `kubectl set env deploy/versed VERSE_PERMS_ADMINS=…` is how a cluster says
	// who may take a top-level place, and there is no verse.tony in the pod to say it in.
	if v := os.Getenv("VERSE_PERMS_ADMINS"); v != "" {
		c.Perms.Admins = nil
		for _, a := range strings.Split(v, ",") {
			if a = strings.TrimSpace(a); a != "" {
				c.Perms.Admins = append(c.Perms.Admins, a)
			}
		}
	}
	c.LLM.BaseURL = envOr("PORTKEY_BASE_URL", c.LLM.BaseURL)
	c.LLM.APIKey = envOr("PORTKEY_API_KEY", c.LLM.APIKey)
	c.LLM.VirtualKey = envOr("PORTKEY_VIRTUAL_KEY", c.LLM.VirtualKey)
	c.LLM.Model = envOr("OPENAI_MODEL", c.LLM.Model)

	var errs []error
	for _, e := range []struct {
		name string
		dst  **codec.Format
	}{
		{"VERSE_FORMAT", &c.Format.Default},
		{"VERSE_OUTPUT_FORMAT", &c.Format.Output},
		{"VERSE_WIRE_FORMAT", &c.Format.Wire},
	} {
		v := os.Getenv(e.name)
		if v == "" {
			continue
		}
		f, err := codec.ParseOutput(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.name, err))
			continue
		}
		*e.dst = &f
	}
	return errors.Join(errs...)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
