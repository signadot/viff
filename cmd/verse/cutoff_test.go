package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/scott-cotton/cli"
	logd "github.com/signadot/tony-format/go-tony/system/logd/server"

	"github.com/signadot/verse/config"
)

// parseCutoff is a copy of logd's duration syntax, kept out of the binary's imports. A value
// it takes that logd refuses would start a store that dies on its config, and one it refuses
// that logd takes would refuse a cutoff that works; either is drift, and this is where it shows.
// The one intended difference: zero and negatives, which logd reads as its own 1h default, are
// refused here.
func TestParseCutoffIsLogds(t *testing.T) {
	for _, s := range []string{"1d", "36h", "90m", "1d12h", "1.5d", "2w", "1y", "1h30m"} {
		got, err := parseCutoff(s)
		if err != nil {
			t.Errorf("parseCutoff(%q) = %v", s, err)
			continue
		}
		want, err := logd.ParseDuration(s)
		if err != nil {
			t.Fatalf("logd.ParseDuration(%q) = %v", s, err)
		}
		if got != want {
			t.Errorf("parseCutoff(%q) = %v, logd reads %v", s, got, want)
		}
	}
	for _, s := range []string{"", "d", "1x", "one day", "0", "0d", "-1h"} {
		if d, err := parseCutoff(s); err == nil {
			t.Errorf("parseCutoff(%q) = %v, want it refused", s, d)
		}
	}
}

// The cutoff comes from the flag, else the variable, else the config's field, else the default,
// and the file names which, so that whoever reads it knows where to change it.
func TestResolveCutoff(t *testing.T) {
	cases := map[string]struct {
		flag, env, field string
		want             cutoff
	}{
		"default":           {want: cutoff{"1d", "verse up's default"}},
		"the config":        {field: "36h", want: cutoff{"36h", "compactionCutoff in the verse config"}},
		"the variable":      {env: "2d", field: "36h", want: cutoff{"2d", "VERSE_COMPACTION_CUTOFF"}},
		"the flag over all": {flag: "3d", env: "2d", field: "36h", want: cutoff{"3d", "verse up --compaction-cutoff"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("VERSE_COMPACTION_CUTOFF", c.env)
			got, err := resolveCutoff(c.flag, &config.Config{CompactionCutoff: c.field})
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("resolveCutoff = %+v, want %+v", got, c.want)
			}
		})
	}
	t.Run("a bad config value names its source", func(t *testing.T) {
		t.Setenv("VERSE_COMPACTION_CUTOFF", "")
		_, err := resolveCutoff("", &config.Config{CompactionCutoff: "a day"})
		if err == nil || !strings.Contains(err.Error(), "compactionCutoff in the verse config") {
			t.Errorf("resolveCutoff = %v, want an error naming the config", err)
		}
	})
}

// The file verse writes is one logd reads, with the cutoff asked for, and says where it came from.
func TestLogdConfigIsReadByLogd(t *testing.T) {
	dir := t.TempDir()
	path, err := resolveLogdConfig("", dir, cutoff{"1d", "verse up's default"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := logd.LoadConfig(path)
	if err != nil {
		t.Fatalf("logd.LoadConfig: %v", err)
	}
	if cfg.Compaction == nil || time.Duration(cfg.Compaction.Cutoff) != 24*time.Hour {
		t.Fatalf("logd reads compaction %+v, want a 24h cutoff", cfg.Compaction)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "from verse up's default") {
		t.Errorf("the file does not say where its cutoff came from:\n%s", b)
	}
}

func TestValidateCompactionCutoff(t *testing.T) {
	cases := map[string]upConfig{
		"with --docd-addr":   {DocdAddr: "docd:7071", CompactionCutoff: "1d"},
		"with --logd-config": {LogdConfig: "logd.tony", CompactionCutoff: "1d"},
		"not a duration":     {CompactionCutoff: "a day"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := cfg.validate(); !errors.Is(err, cli.ErrUsage) {
				t.Errorf("validate() = %v, want a usage error", err)
			}
		})
	}
	ok := upConfig{CompactionCutoff: "36h"}
	if err := ok.validate(); err != nil {
		t.Errorf("validate() = %v, want nil", err)
	}
}
