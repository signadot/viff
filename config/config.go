// Package config loads verse's layered configuration. Precedence, lowest first:
//
//	/etc/verse.tony            system base
//	~/.config/verse.tony       per-user — PATCHES the base (a tony merge, not replace)
//	environment variables      override the file
//	CLI flags                  override env (applied by the commands, not here)
//
// The two files are tony documents merged with tony.Patch, so ~/.config need only
// carry the keys it wants to change; everything else falls through to /etc. Missing
// files are simply absent layers (no error). ir.Node appears only at the parse/merge
// boundary; the rest of verse consumes the typed Config.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/parse"
)

// SystemPath is the system-wide config file; a var so tests can point it elsewhere.
// change
var SystemPath = "/etc/verse.tony"

// Config is the resolved configuration (file layers + env). CLI flags override these
// afterward, in the commands.
type Config struct {
	_     struct{}  `tony:"schemagen=verse-config,notag"`
	Addr  string    `tony:"field=addr,omitzero"`
	Scope string    `tony:"field=scope,omitzero"`
	LLM   LLMConfig `tony:"field=llm,omitzero"`
}

// LLMConfig configures the llm process driver. When creds are absent the driver is
// unavailable and an llm spawn fails (that spawn, not the engine).
type LLMConfig struct {
	_          struct{} `tony:"schemagen=verse-llm,notag"`
	BaseURL    string   `tony:"field=baseURL,omitzero"`
	APIKey     string   `tony:"field=apiKey,omitzero"`
	VirtualKey string   `tony:"field=virtualKey,omitzero"`
	Model      string   `tony:"field=model,omitzero"`
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

// UserPath is ~/.config/verse.tony (honoring XDG_CONFIG_HOME), or "" if no home.
func UserPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "verse.tony")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "verse.tony")
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
	c.applyEnv() // env always wins over the file — and applies even if the file broke
	if err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

// loadMerged parses /etc then patches it with ~/.config (each optional).
func loadMerged() (*ir.Node, error) {
	base, err := readNode(SystemPath)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", SystemPath, err)
	}
	over, err := readNode(UserPath())
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", UserPath(), err)
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

// readNode parses a tony file into a node, treating a missing or empty file as no
// layer (nil, nil) rather than an error.
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
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	return parse.Parse(data, parse.ParseTony())
}

// applyEnv overlays environment variables on top of the file config.
func (c *Config) applyEnv() {
	c.Addr = envOr("VERSE_ADDR", c.Addr)
	c.Scope = envOr("VERSE_SCOPE", c.Scope)
	c.LLM.BaseURL = envOr("PORTKEY_BASE_URL", c.LLM.BaseURL)
	c.LLM.APIKey = envOr("PORTKEY_API_KEY", c.LLM.APIKey)
	c.LLM.VirtualKey = envOr("PORTKEY_VIRTUAL_KEY", c.LLM.VirtualKey)
	c.LLM.Model = envOr("OPENAI_MODEL", c.LLM.Model)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
