package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
)

// The user file patches the system file (per-key merge, not replace), and env
// overrides both.
func TestLayering(t *testing.T) {
	etc := filepath.Join(t.TempDir(), "verse.tony")
	if err := os.WriteFile(etc, []byte(`addr: "sys:1"
scope: "baseline"
llm: { model: "gpt-4o", apiKey: "sys-key", virtualKey: "sys-vk" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	xdg := t.TempDir()
	if err := os.WriteFile(filepath.Join(xdg, "verse.tony"), []byte(`addr: "user:2"
llm: { apiKey: "user-key" }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	withLayers(t, etc, xdg)

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// user patches: addr overridden, llm.apiKey overridden; system model/scope/vk kept.
	if c.Addr != "user:2" {
		t.Errorf("addr = %q, want user:2 (user patches system)", c.Addr)
	}
	if c.Scope != "baseline" {
		t.Errorf("scope = %q, want baseline (from system, not patched)", c.Scope)
	}
	if c.LLM.APIKey != "user-key" {
		t.Errorf("llm.apiKey = %q, want user-key (patched)", c.LLM.APIKey)
	}
	if c.LLM.Model != "gpt-4o" || c.LLM.VirtualKey != "sys-vk" {
		t.Errorf("llm model/vk = %q/%q, want gpt-4o/sys-vk (from system)", c.LLM.Model, c.LLM.VirtualKey)
	}
	if !c.LLM.Available() {
		t.Error("llm should be available (apiKey + virtualKey set)")
	}

	// env overrides the merged file value.
	t.Setenv("VERSE_ADDR", "env:3")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "env:3" {
		t.Errorf("addr = %q, want env:3 (env overrides file)", c.Addr)
	}
}

// Missing files are absent layers, not errors: with neither of them to read, Load answers
// the zero config rather than failing.
func TestNoFiles(t *testing.T) {
	withLayers(t, filepath.Join(t.TempDir(), "nope.tony"), t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatalf("load with no files: %v", err)
	}
	if c.Addr != "" || c.LLM.Available() {
		t.Errorf("expected empty config, got %+v", c)
	}
}

// A layer's format comes from its file name, and the layers need not agree: both are
// ir.Node by the time they merge, so the dialect is gone before tony.Patch sees them.
func TestLayersMayBeDifferentDialects(t *testing.T) {
	etcDir, xdg := t.TempDir(), t.TempDir()
	// system layer in JSON…
	write(t, filepath.Join(etcDir, "verse.json"), `{"addr": "sys:1", "scope": "baseline",
		"llm": {"model": "gpt-4o", "apiKey": "sys-key"}}`)
	// …patched by a user layer in YAML.
	write(t, filepath.Join(xdg, "verse.yaml"), "addr: user:2\nllm:\n  apiKey: user-key\n")

	withLayers(t, filepath.Join(etcDir, "verse"), xdg)

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "user:2" {
		t.Errorf("addr = %q, want user:2 (the YAML layer patches the JSON one)", c.Addr)
	}
	if c.Scope != "baseline" || c.LLM.Model != "gpt-4o" {
		t.Errorf("values from the JSON layer were lost: %+v", c)
	}
	if c.LLM.APIKey != "user-key" {
		t.Errorf("llm.apiKey = %q, want user-key (patched by the YAML layer)", c.LLM.APIKey)
	}
}

// Two files for one layer is an error, not a precedence rule — editing the one that is
// being silently ignored is a bad afternoon.
func TestAmbiguousLayerIsAnError(t *testing.T) {
	etcDir := t.TempDir()
	write(t, filepath.Join(etcDir, "verse.tony"), "addr: a\n")
	write(t, filepath.Join(etcDir, "verse.json"), `{"addr": "b"}`)
	withLayers(t, filepath.Join(etcDir, "verse"), t.TempDir())

	_, err := Load()
	if err == nil {
		t.Fatal("two config files for one layer loaded without complaint")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error does not name the problem: %v", err)
	}
}

// VERSE_CONFIG replaces both layers, so a test or a container can be hermetic.
func TestExplicitConfigPath(t *testing.T) {
	p := filepath.Join(t.TempDir(), "custom.yaml")
	write(t, p, "addr: explicit:9\n")
	withLayers(t, filepath.Join(t.TempDir(), "verse"), t.TempDir())
	t.Setenv(EnvConfigPath, p)

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "explicit:9" {
		t.Errorf("addr = %q, want explicit:9 (VERSE_CONFIG replaces both layers)", c.Addr)
	}
}

func TestFormatConfig(t *testing.T) {
	etcDir := t.TempDir()
	write(t, filepath.Join(etcDir, "verse.tony"), "format:\n  default: yaml\n  output: tony\n")
	withLayers(t, filepath.Join(etcDir, "verse"), t.TempDir())

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Format.ResolveDefault(); got != codec.YAML {
		t.Errorf("default = %v, want yaml", got)
	}
	if got := c.Format.ResolveOutput(); got != codec.Tony {
		t.Errorf("output = %v, want tony (explicitly set)", got)
	}
	if got := c.Format.ResolveWire(); got != codec.YAML {
		t.Errorf("wire = %v, want yaml (falls back to default)", got)
	}

	// env overrides the file, per knob.
	t.Setenv("VERSE_WIRE_FORMAT", "tony")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Format.ResolveWire(); got != codec.Tony {
		t.Errorf("wire = %v, want tony (env overrides the file)", got)
	}
}

// json names a rendering verse does not produce, and it is refused at BOTH doors — the
// env var and the config file. The file half is the one that needs saying: it decodes
// straight into go-tony's own format type, which knows nothing of verse's rules, so
// without FormatConfig.validate the setting would sit there until a response failed to
// encode with no line number attached.
func TestFormatConfigRefusesJSONOutput(t *testing.T) {
	t.Run("env", func(t *testing.T) {
		withLayers(t, filepath.Join(t.TempDir(), "verse"), t.TempDir())
		t.Setenv("VERSE_FORMAT", "json")
		_, err := Load()
		if err == nil {
			t.Fatal("VERSE_FORMAT=json was accepted")
		}
		if !strings.Contains(err.Error(), "VERSE_FORMAT") {
			t.Errorf("error does not name the variable: %v", err)
		}
	})
	t.Run("file", func(t *testing.T) {
		etcDir := t.TempDir()
		write(t, filepath.Join(etcDir, "verse.tony"), "format:\n  output: json\n")
		withLayers(t, filepath.Join(etcDir, "verse"), t.TempDir())
		_, err := Load()
		if err == nil {
			t.Fatal("format.output: json was accepted from a config file")
		}
		if !strings.Contains(err.Error(), "format.output") {
			t.Errorf("error does not name the setting: %v", err)
		}
	})
}

// An unset format falls through to the baseline, and a malformed one is reported rather
// than silently becoming the baseline.
func TestFormatFallbackAndBadValue(t *testing.T) {
	withLayers(t, filepath.Join(t.TempDir(), "verse"), t.TempDir())

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Format.ResolveDefault(); got != codec.Baseline {
		t.Errorf("unset default = %v, want the baseline %v", got, codec.Baseline)
	}

	t.Setenv("VERSE_FORMAT", "tny")
	c, err = Load()
	if err == nil {
		t.Fatal("a malformed VERSE_FORMAT was accepted")
	}
	if !strings.Contains(err.Error(), "VERSE_FORMAT") {
		t.Errorf("error does not name the variable: %v", err)
	}
	// Still usable: Load's contract is that it always returns a config.
	if got := c.Format.ResolveDefault(); got != codec.Baseline {
		t.Errorf("after a bad value, default = %v, want the baseline", got)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withLayers points both config layers at test directories and clears the environment
// verse reads, so a developer's own shell cannot change the result. It also restores
// SystemPath, which is a package variable every later test in the binary would otherwise
// inherit.
//
// The list is every variable Load consults and has to stay that way, because one missing
// from it turns a test of the loader into a question about the machine running it.
// PORTKEY_VIRTUAL_KEY is the one that bites: anybody who has pointed a verse at a real LLM
// has it exported, so every config they load has an LLM configured and `Available()` is true
// with no file anywhere saying so.
func withLayers(t *testing.T, systemBase, xdgDir string) {
	t.Helper()
	old := SystemPath
	SystemPath = systemBase
	t.Cleanup(func() { SystemPath = old })
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	for _, k := range []string{
		EnvConfigPath, "VERSE_ADDR", "VERSE_SCOPE", "VERSE_USER",
		"VERSE_FORMAT", "VERSE_OUTPUT_FORMAT", "VERSE_WIRE_FORMAT",
		"OPENAI_MODEL", "PORTKEY_API_KEY", "PORTKEY_VIRTUAL_KEY", "PORTKEY_BASE_URL",
	} {
		t.Setenv(k, "")
	}
}

// `user:` is who the CLI acts as, and VERSE_USER overrides the file the way the other env
// knobs do.
func TestUserFromFileAndEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "verse.tony")
	write(t, p, "user: walter@example.com\n")
	withLayers(t, filepath.Join(t.TempDir(), "verse"), t.TempDir())
	t.Setenv(EnvConfigPath, p)

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.User != "walter@example.com" {
		t.Errorf("user = %q, want the file's", c.User)
	}
	t.Setenv("VERSE_USER", "verse-runner-bot")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.User != "verse-runner-bot" {
		t.Errorf("user = %q, want VERSE_USER over the file", c.User)
	}
}

// The repositories whose charters verse reconciles are read from the file, and an entry that
// names no address is refused at load, where the refusal is a line in a file.
func TestRepoChartersFromFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "verse.tony")
	write(t, p, "repoCharters:\n- repo: verse\n  github: signadot/verse\n  at: repos.verse\n  answerers: [\"user:*\"]\n")
	withLayers(t, filepath.Join(t.TempDir(), "verse"), t.TempDir())
	t.Setenv(EnvConfigPath, p)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.RepoCharters) != 1 || c.RepoCharters[0].Repo != "verse" || c.RepoCharters[0].GitHub != "signadot/verse" ||
		c.RepoCharters[0].At != "repos.verse" || len(c.RepoCharters[0].Answerers) != 1 || c.RepoCharters[0].Answerers[0] != "user:*" {
		t.Fatalf("repoCharters = %+v", c.RepoCharters)
	}
	write(t, p, "repoCharters:\n- repo: verse\n  github: signadot/verse\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "repoCharters verse") {
		t.Fatalf("an entry with no address loaded: %v", err)
	}
}

// A SPAWN'S CREDENTIAL IS THE ONE IT CARRIES, and this verse's only when it carries none.
//
// There is nothing to look up: what a spawn carries is already the key, read for it from the
// address it named by whoever named it. This verse's own is the default, which is every spawn
// written before credentials had addresses.
func TestASpawnsCredentialBeatsTheVersesOwn(t *testing.T) {
	llm := LLMConfig{APIKey: "pk", VirtualKey: "vk-general"}

	if got := llm.VirtualKeyOr("vk-review"); got != "vk-review" {
		t.Errorf("carried → %q, want the spawn's own", got)
	}
	if got := llm.VirtualKeyOr(""); got != "vk-general" {
		t.Errorf("unnamed → %q, want this verse's", got)
	}
	// A verse with none, asked by a spawn with none: no virtual key at all, which is the
	// account key alone and the state every verse was in before this.
	if got := (LLMConfig{APIKey: "pk"}).VirtualKeyOr(""); got != "" {
		t.Errorf("neither → %q, want no virtual key", got)
	}
}

// The auth block names the identity providers people arrive from, and is refused at load where it
// names one that could not be used.
func TestAuthPrincipalIsReadAndChecked(t *testing.T) {
	load := func(body string) (*Config, error) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "verse.tony")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv(EnvConfigPath, path)
		return Load()
	}
	c, err := load(`auth:
  principal:
    mode: optional
    providers:
      google:
        issuer: https://accounts.google.com
        clientId: 1234.apps.googleusercontent.com
        domains: [example.com]
      okta:
        issuer: https://example.okta.com
        clientId: "0oa1"
      github:
        iods: [gh]
`)
	if err != nil {
		t.Fatal(err)
	}
	p := c.Auth.Principal
	if gh := p.Providers[GitHubProvider]; len(gh.IOs) != 1 || gh.IOs[0] != "gh" {
		t.Errorf("github = %+v, want the io it trusts", gh)
	}
	if p.Mode != AuthOptional || len(p.Providers) != 3 {
		t.Fatalf("auth.principal = %+v", p)
	}
	if g := p.Providers["google"]; g.Issuer != "https://accounts.google.com" ||
		g.ClientID != "1234.apps.googleusercontent.com" || len(g.Domains) != 1 || g.Domains[0] != "example.com" {
		t.Errorf("google = %+v", g)
	}

	// A verse with no auth block is one that verifies nobody.
	if c, err := load("addr: \"x:1\"\n"); err != nil || c.Auth.Principal.Mode != "" || len(c.Auth.Principal.Providers) != 0 {
		t.Errorf("no auth block = %+v, %v", c.Auth, err)
	}

	// The issuer is a url and a key file, both or neither.
	if c, err := load("auth: {issuer: {url: \"https://verse.example.com\", key: /etc/verse/issuer.pem}}\n"); err != nil ||
		c.Auth.Issuer.URL != "https://verse.example.com" || c.Auth.Issuer.Key != "/etc/verse/issuer.pem" {
		t.Errorf("auth.issuer = %+v, %v", c.Auth.Issuer, err)
	}

	// Verse's own issuer is no provider's and not the workload tokens': a token it signs toward a
	// runner opens nothing at its own door.
	if _, err := load("auth: {issuer: {url: \"https://login.example.com\", key: /k.pem}, principal: {providers: " +
		"{corp: {issuer: \"https://login.example.com\", clientId: verse-cli}}}}\n"); err == nil || !strings.Contains(err.Error(), "auth.issuer.url") {
		t.Errorf("an issuer url that is a provider's = %v", err)
	}
	if _, err := load("auth: {issuer: {url: \"https://oidc.example/id/1/\", key: /k.pem}, iod: {issuer: \"https://oidc.example/id/1\"}}\n"); err == nil ||
		!strings.Contains(err.Error(), "auth.iod.issuer") {
		t.Errorf("an issuer url that is the workload issuer = %v", err)
	}

	// Iods: an issuer-less provider trusting an io, with what it may do, and the iods' own block.
	if c, err := load("auth: {principal: {providers: {slack: {iods: [workspace], domains: [example.com], acts: any}, " +
		"corp: {issuer: \"https://login.example.com\", clientId: verse-cli}, github: {iods: [gh]}}}, " +
		"iod: {issuer: \"https://oidc.eks.us-west-2.amazonaws.com/id/1\", audience: verse, " +
		"accounts: {\"system:serviceaccount:verse:slack-iod\": {principal: \"agent:slack-iod\", iod: workspace}}, " +
		"static: [{file: /run/verse/git-iod.token, principal: \"agent:git-iod\"}]}}\n"); err != nil {
		t.Errorf("iods block: %v", err)
	} else {
		if sl := c.Auth.Principal.Providers["slack"]; sl.Acts != ActsAny || len(sl.IOs) != 1 || sl.IOs[0] != "workspace" {
			t.Errorf("slack = %+v", sl)
		}
		iod := c.Auth.Iod
		if iod.Audience != "verse" || iod.Accounts["system:serviceaccount:verse:slack-iod"].IO != "workspace" ||
			len(iod.Static) != 1 || iod.Static[0].Principal != "agent:git-iod" {
			t.Errorf("iod = %+v", iod)
		}
	}

	for _, bad := range []struct{ what, body, want string }{
		{"an issuer with no key", "auth: {issuer: {url: \"https://verse.example.com\"}}", "both a url and a key"},
		{"an issuer whose url is none", "auth: {issuer: {url: verse, key: /k.pem}}", "relying party"},
		{"an unknown mode", "auth: {principal: {mode: sometimes}}", "not a mode"},
		{"a provider with no client", "auth: {principal: {providers: {google: {issuer: \"https://accounts.google.com\"}}}}", "clientId is required"},
		{"a provider with no issuer", "auth: {principal: {providers: {google: {clientId: a}}}}", "not an issuer's URL"},
		{"an issuer that is no URL", "auth: {principal: {providers: {google: {issuer: accounts, clientId: a}}}}", "not an issuer's URL"},
		{"an issuer named github", "auth: {principal: {providers: {github: {issuer: \"https://github.com\", clientId: a}}}}", "its entry takes `iods`"},
		{"iods on an issuer", "auth: {principal: {providers: {google: {issuer: \"https://accounts.google.com\", clientId: a, iods: [gh]}}}}", "not both"},
		{"a provider with neither", "auth: {principal: {providers: {slack: {domains: [example.com]}}}}", "or names the iods"},
		{"a provider named iod", "auth: {principal: {providers: {iod: {iods: [x]}}}}", "iods' own"},
		{"acts that are neither word", "auth: {principal: {providers: {slack: {iods: [workspace], acts: some}}}}", "not control or any"},
		{"acts on an issuer", "auth: {principal: {providers: {google: {issuer: \"https://accounts.google.com\", clientId: a, acts: any}}}}", "goes with `iods`"},
		{"iod accounts with no issuer", "auth: {iod: {accounts: {\"system:serviceaccount:verse:slack-iod\": {principal: \"agent:slack-iod\"}}}}", "no issuer of those tokens"},
		{"an iod account that is no agent", "auth: {iod: {issuer: \"https://oidc.example.com/id/1\", accounts: {\"system:serviceaccount:verse:slack-iod\": {principal: \"user:ana@example.com\"}}}}", "not an io's principal"},
		{"a static io with no file", "auth: {iod: {static: [{principal: \"agent:slack-iod\"}]}}", "path of the token"},
		{"a bundle with no issuer", "auth: {iod: {ca: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt}}", "no issuer is named"},
		{"an iod issuer that is a provider's", "auth: {principal: {providers: {a: {issuer: \"https://a.example.com\", clientId: a}}}, iod: {issuer: \"https://a.example.com\"}}", "names one party"},
		{"a name that is not one segment", "auth: {principal: {providers: {\"a.b\": {issuer: \"https://a.example.com\", clientId: a}}}}", "one plain address segment"},
		{"two providers of one issuer", "auth: {principal: {providers: {a: {issuer: \"https://a.example.com\", clientId: a}, b: {issuer: \"https://a.example.com\", clientId: b}}}}", "names one"},
	} {
		if _, err := load(bad.body); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: err = %v, want one saying %q", bad.what, err, bad.want)
		}
	}
}
