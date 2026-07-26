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
	"os"
	"path/filepath"
	"strings"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/format"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

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
	Addr   string       `tony:"field=addr,omitzero"`
	Scope  string       `tony:"field=scope,omitzero"`
	Format FormatConfig `tony:"field=format,omitzero"`
	LLM    LLMConfig    `tony:"field=llm,omitzero"`
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

// LLMConfig configures the llm process driver. When creds are absent the driver is
// unavailable and an llm spawn fails (that spawn, not the engine).
//
//tony:schemagen=verse-llm,notag
type LLMConfig struct {
	BaseURL    string `tony:"field=baseURL,omitzero"`
	APIKey     string `tony:"field=apiKey,omitzero"`
	VirtualKey string `tony:"field=virtualKey,omitzero"`
	Model      string `tony:"field=model,omitzero"`
}

// Available reports whether the llm driver can be built — an API key is configured.
// The virtual key is optional (Portkey routing); requiring it would wrongly fall back
// to scripted for a direct-key setup.
func (l LLMConfig) Available() bool { return l.APIKey != "" }

// Status is a secrets-safe summary for /status: whether the llm driver is usable, the model
// and base URL (not secret), and MASKED key fragments — enough to tell which key is set (and
// catch a stale/typo'd one) without exposing it. The full keys never leave the process.
func (l LLMConfig) Status() map[string]any {
	m := map[string]any{
		"available":  l.Available(),
		"apiKey":     maskSecret(l.APIKey),
		"virtualKey": maskSecret(l.VirtualKey),
	}
	if l.Model != "" {
		m["model"] = l.Model
	}
	if l.BaseURL != "" {
		m["baseURL"] = l.BaseURL
	}
	return m
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
// getting JSON is worse than being told.
func (c *Config) applyEnv() error {
	c.Addr = envOr("VERSE_ADDR", c.Addr)
	c.Scope = envOr("VERSE_SCOPE", c.Scope)
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
		f, err := codec.Parse(v)
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
