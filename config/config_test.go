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

	old := SystemPath
	SystemPath = etc
	t.Cleanup(func() { SystemPath = old })
	t.Setenv("XDG_CONFIG_HOME", xdg)
	// Ensure env doesn't leak in from the runner for the values we assert on file merge.
	t.Setenv("VERSE_ADDR", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("PORTKEY_API_KEY", "")
	t.Setenv("PORTKEY_VIRTUAL_KEY", "")

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

// Missing files are absent layers, not errors; env still applies.
func TestNoFiles(t *testing.T) {
	SystemPath = filepath.Join(t.TempDir(), "nope.tony")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("VERSE_ADDR", "")
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
	write(t, filepath.Join(etcDir, "verse.tony"), "format:\n  default: yaml\n  output: json\n")
	withLayers(t, filepath.Join(etcDir, "verse"), t.TempDir())

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Format.ResolveDefault(); got != codec.YAML {
		t.Errorf("default = %v, want yaml", got)
	}
	if got := c.Format.ResolveOutput(); got != codec.JSON {
		t.Errorf("output = %v, want json (explicitly set)", got)
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
// verse reads, so a developer's own shell cannot change the result.
func withLayers(t *testing.T, systemBase, xdgDir string) {
	t.Helper()
	old := SystemPath
	SystemPath = systemBase
	t.Cleanup(func() { SystemPath = old })
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	for _, k := range []string{
		EnvConfigPath, "VERSE_ADDR", "VERSE_SCOPE",
		"VERSE_FORMAT", "VERSE_OUTPUT_FORMAT", "VERSE_WIRE_FORMAT",
		"OPENAI_MODEL", "PORTKEY_API_KEY", "PORTKEY_VIRTUAL_KEY", "PORTKEY_BASE_URL",
	} {
		t.Setenv(k, "")
	}
}
