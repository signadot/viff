package runnerd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type ownConfig struct {
	Models struct {
		Model string `tony:"field=model,omitzero"`
	} `tony:"field=models,omitzero"`
	Tags map[string]string `tony:"field=tags,omitzero"`
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "runner.tony")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadReadsBothHalves(t *testing.T) {
	var own ownConfig
	c, err := Load(writeConfig(t, "listen: \":9902\"\nmodels: {model: m1}\ntags: {any-key: x}\n"), &own)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9902" {
		t.Fatalf("config %+v", c)
	}
	if own.Models.Model != "m1" || own.Tags["any-key"] != "x" {
		t.Fatalf("own %+v", own)
	}
}

func TestLoadRefusesWhatItWouldNotApply(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"listne: x\n", `no setting "listne"`},
		{"models: {modle: m1}\n", `no setting "models.modle"`},
		{"concurrency: 4\n", `no setting "concurrency"`},
		{"hold: 3m\n", `no setting "hold"`},
		{"verse: {url: \"http://verse:9797\", audiance: x}\n", `no setting "verse.audiance"`},
		{"verse: {issuer: \"https://verse.example\"}\n", "verse: `url`"},
	} {
		var own ownConfig
		_, err := Load(writeConfig(t, tc.body), &own)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got %v, want an error with %q", tc.body, err, tc.want)
		}
	}
}

func TestListenDefaults(t *testing.T) {
	var c Config
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	if c.Listen != DefaultListen {
		t.Fatalf("listen %q", c.Listen)
	}
}
