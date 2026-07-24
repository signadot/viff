package config

import (
	"os"
	"path/filepath"
	"testing"
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
