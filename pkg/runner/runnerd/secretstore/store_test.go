package secretstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A SECRET'S VALUE IS ITS FILE'S, READ AT EACH USE: one changed between two reads is the second
// read's, and the newline a file ends with is not the value's, whichever kind it is.
func TestAValueIsReadAtEachUse(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	for _, c := range []struct{ file, want string }{
		{"xoxb-1", "xoxb-1"},
		{"xoxb-2\n", "xoxb-2"},
		{"xoxb-3\r\n", "xoxb-3"},
		{"two\nlines\n", "two\nlines"},
		{"ends twice\n\n", "ends twice\n"},
	} {
		write(t, dir, "slack-bot", c.file)
		if got, err := s.Get("slack-bot"); err != nil || got != c.want {
			t.Errorf("the file %q reads %q, %v, want %q", c.file, got, err, c.want)
		}
	}
	if !s.Has("slack-bot") || s.Has("linear") {
		t.Error("Has does not say what Get would give")
	}
}

// AN ABSENT SECRET IS ONE WHOSE FILE IS NOT THERE, IS EMPTY, OR DOES NOT READ, and the error says
// which, naming the secret. No error holds any of a value.
func TestAnAbsentSecretSaysWhy(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	write(t, dir, "empty", "")
	write(t, dir, "newline", "\n")
	write(t, dir, "big", strings.Repeat("k", maxValue+1))
	write(t, dir, "full", strings.Repeat("k", maxValue))
	if err := os.Mkdir(filepath.Join(dir, "a-directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"missing":     "the secret missing: it is not in the store",
		"empty":       "the secret empty: its file is empty",
		"newline":     "the secret newline: its file is empty",
		"big":         "the secret big: its file is over",
		"a-directory": "the secret a-directory: it does not read",
	} {
		v, err := s.Get(name)
		if err == nil || v != "" || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %q, %v, want an error saying %q", name, v, err, want)
		}
		if err != nil && (strings.Contains(err.Error(), "kkkk") || strings.Contains(err.Error(), dir)) {
			t.Errorf("%s: the error holds a value or the store's path: %v", name, err)
		}
		if s.Has(name) {
			t.Errorf("%s: Has says it is there", name)
		}
	}
	if v, err := s.Get("full"); err != nil || len(v) != maxValue {
		t.Errorf("a value of the most a file may hold: %d bytes, %v", len(v), err)
	}
	// A file the runner cannot read. Root reads any file, so the test has nothing to see there.
	if os.Getuid() != 0 {
		write(t, dir, "locked", "xoxb-1")
		if err := os.Chmod(filepath.Join(dir, "locked"), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get("locked"); err == nil || !strings.Contains(err.Error(), "the secret locked: it does not read") {
			t.Errorf("a file that cannot be read: %v", err)
		}
	}
	for _, none := range []*Store{nil, Open("")} {
		if _, err := none.Get("slack-bot"); err == nil || !strings.Contains(err.Error(), "no secret store") {
			t.Errorf("a runner with no store: %v", err)
		}
	}
}

// A NAME IS A FILE'S NAME AND NOTHING ELSE, so none reaches out of the store, and none is a name
// a Kubernetes Secret volume keeps for itself.
func TestANameIsAFilesName(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "outside-"+filepath.Base(dir))
	if err := os.WriteFile(outside, []byte("not the store's"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })
	s := Open(dir)
	for _, name := range []string{"", ".", "..", "../" + filepath.Base(outside), "a/b", `a\b`, "..data", ".hidden",
		"SLACK_BOT_TOKEN", "gitToken", "slack bot", "slack\x00bot", "clé"} {
		if err := CheckName(name); err == nil {
			t.Errorf("%q is taken as a secret's name", name)
		}
		if v, err := s.Get(name); err == nil {
			t.Errorf("%q reads %q", name, v)
		}
	}
	for _, name := range []string{"slack-bot", "linear", "mcp.linear_2", "a"} {
		if err := CheckName(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
}

// A KUBERNETES SECRET VOLUME READS: each name is a link into `..data`, itself a link to the
// directory of the day, and a rotation moves `..data` alone.
func TestAKubernetesSecretVolumeReads(t *testing.T) {
	dir := t.TempDir()
	day := func(name, value string) {
		t.Helper()
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, name), "slack-bot", value)
	}
	day("..2026_11_06_10_00", "xoxb-1")
	if err := os.Symlink("..2026_11_06_10_00", filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", "slack-bot"), filepath.Join(dir, "slack-bot")); err != nil {
		t.Fatal(err)
	}
	s := Open(dir)
	if v, err := s.Get("slack-bot"); err != nil || v != "xoxb-1" {
		t.Fatalf("a secret in the volume's layout: %q, %v", v, err)
	}
	// The rotation: a new directory, and `..data` renamed over to it.
	day("..2026_11_06_11_00", "xoxb-2")
	if err := os.Symlink("..2026_11_06_11_00", filepath.Join(dir, "..data_tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "..data_tmp"), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get("slack-bot"); err != nil || v != "xoxb-2" {
		t.Errorf("after the rotation: %q, %v", v, err)
	}
}
