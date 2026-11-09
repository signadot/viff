package cellar

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fill writes a prefix of one command and one library with its soname symlink, content bytes big.
func fill(cmd string, content string) func(stage string) error {
	return func(stage string) error {
		for _, dir := range []string{"bin", "lib"} {
			if err := os.MkdirAll(filepath.Join(stage, dir), 0o755); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(stage, "bin", cmd), []byte(content), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(stage, "lib", "libx.so.1.0"), []byte(content), 0o644); err != nil {
			return err
		}
		return os.Symlink("libx.so.1.0", filepath.Join(stage, "lib", "libx.so.1"))
	}
}

// AN INSTALL IS STAGED AND RENAMED WHOLE, AND A PREFIX IS IMMUTABLE ONCE WRITTEN: a second
// install of the same version builds nothing, a build that fails leaves nothing, and the staging
// never shows under the package's name.
func TestAnInstallIsStagedWholeAndImmutable(t *testing.T) {
	c, err := New(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Install("jq", "1.6-2.1+deb12u2", fill("jq", "one")); err != nil {
		t.Fatal(err)
	}
	if !c.Has("jq", "1.6-2.1+deb12u2") || c.Has("jq", "1.7.1-1") {
		t.Fatal("has")
	}
	if err := c.Install("jq", "1.6-2.1+deb12u2", fill("jq", "two")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(c.Prefix("jq", "1.6-2.1+deb12u2"), "bin", "jq")); string(b) != "one" {
		t.Fatalf("the prefix changed: %q", b)
	}
	boom := errors.New("no index")
	if err := c.Install("rg", "14", func(string) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("a failed build: %v", err)
	}
	if c.Has("rg", "14") {
		t.Fatal("a failed build left a prefix")
	}
	if cmds, err := c.Commands("jq", "1.6-2.1+deb12u2"); err != nil || len(cmds) != 1 || cmds[0] != "jq" {
		t.Fatalf("commands: %v %v", cmds, err)
	}
	for _, bad := range [][2]string{{"", "1"}, {"a/b", "1"}, {"jq", "../1"}, {".hidden", "1"}, {"jq", ""}} {
		if err := c.Install(bad[0], bad[1], fill("x", "x")); err == nil {
			t.Errorf("%q %q installed", bad[0], bad[1])
		}
	}
}

// MATERIALISE HARDLINKS THE PREFIX INTO THE TASK'S ROOT AND NAMES THE COMMAND BY A RELATIVE
// SYMLINK, so a link resolves wherever the root is bound, two names share one prefix, and a
// command the prefix does not ship, or a name that is a path, is refused.
func TestMaterialiseHardlinksThePrefixAndNamesTheCommand(t *testing.T) {
	dir := t.TempDir()
	c, err := New(filepath.Join(dir, "cellar"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Install("jq", "1.6", fill("jq", "#!jq")); err != nil {
		t.Fatal(err)
	}
	inst := filepath.Join(dir, "task1", "inst")
	if err := os.MkdirAll(inst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, as := range []string{"jq", "jq-v2"} {
		if err := c.Materialise(inst, "jq", "1.6", "jq", as); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := os.Readlink(filepath.Join(inst, "bin", "jq-v2")); err != nil || got != filepath.Join("..", "opt", "jq-1.6", "bin", "jq") {
		t.Fatalf("the link: %q %v", got, err)
	}
	if b, err := os.ReadFile(filepath.Join(inst, "bin", "jq")); err != nil || string(b) != "#!jq" {
		t.Fatalf("through the link: %q %v", b, err)
	}
	a, err1 := os.Stat(filepath.Join(c.Prefix("jq", "1.6"), "bin", "jq"))
	b, err2 := os.Stat(filepath.Join(inst, "opt", "jq-1.6", "bin", "jq"))
	if err1 != nil || err2 != nil || !os.SameFile(a, b) {
		t.Fatal("the prefix is hardlinked, not copied")
	}
	if got, err := os.Readlink(filepath.Join(inst, "opt", "jq-1.6", "lib", "libx.so.1")); err != nil || got != "libx.so.1.0" {
		t.Fatalf("a soname symlink is respelled: %q %v", got, err)
	}
	if err := c.Materialise(inst, "jq", "1.6", "gojq", "jq"); err == nil {
		t.Fatal("a command the prefix does not ship")
	}
	if err := c.Materialise(inst, "jq", "1.6", "jq", "bin/evil"); err == nil {
		t.Fatal("a name that is a path")
	}
	if err := c.Materialise(inst, "jq", "1.7", "jq", "jq"); err == nil {
		t.Fatal("a version the cellar has not")
	}
}

// EVICTION DROPS THE LEAST RECENTLY USED PREFIX ABOVE THE BOUND, AND A TASK THAT HOLDS IT KEEPS
// IT: the hardlinks of a materialised root hold the inodes after the cellar's entry is gone.
func TestEvictionDropsTheLeastRecentlyUsedAndSparesAHolder(t *testing.T) {
	dir := t.TempDir()
	c, err := New(filepath.Join(dir, "cellar"), 100)
	if err != nil {
		t.Fatal(err)
	}
	big := string(make([]byte, 40)) // bin and lib: 80 bytes a prefix
	if err := c.Install("old", "1", fill("old", big)); err != nil {
		t.Fatal(err)
	}
	inst := filepath.Join(dir, "task1", "inst")
	if err := os.MkdirAll(inst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := c.Materialise(inst, "old", "1", "old", "old"); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(c.Prefix("old", "1"), past, past); err != nil {
		t.Fatal(err)
	}
	if err := c.Install("new", "2", fill("new", big)); err != nil {
		t.Fatal(err)
	}
	if c.Has("old", "1") || !c.Has("new", "2") {
		t.Fatalf("has old %v new %v, want the oldest evicted", c.Has("old", "1"), c.Has("new", "2"))
	}
	if b, err := os.ReadFile(filepath.Join(inst, "bin", "old")); err != nil || string(b) != big {
		t.Fatalf("the holder lost its tool: %d bytes, %v", len(b), err)
	}
}

// A PREFIX'S SIZE IS WALKED ONCE, AND ITS EVICTION DROPS THE ENTRY: a prefix is immutable from
// its rename in, so collect reads the bytes it measured before rather than walking again, and an
// evicted prefix leaves no entry behind to shadow a reinstall's own measure.
func TestAPrefixsSizeIsWalkedOnceAndEvictionDropsIt(t *testing.T) {
	c, err := New(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	big := string(make([]byte, 40)) // bin and lib: 80 bytes a prefix
	if err := c.Install("old", "1", fill("old", big)); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	got := c.sizes["old/1"]
	c.mu.Unlock()
	if got != 80 {
		t.Fatalf("cached size %d, want 80", got)
	}
	// Understate the cached size: a second install then stays under the bound only where
	// collect reads the cache, since a fresh walk sees 160 and evicts.
	c.mu.Lock()
	c.sizes["old/1"] = 1
	c.mu.Unlock()
	if err := c.Install("new", "2", fill("new", big)); err != nil {
		t.Fatal(err)
	}
	if !c.Has("old", "1") || !c.Has("new", "2") {
		t.Fatalf("has old %v new %v: collect walked the prefix it had measured", c.Has("old", "1"), c.Has("new", "2"))
	}
	c.mu.Lock()
	c.sizes["old/1"] = 80
	c.mu.Unlock()
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(c.Prefix("old", "1"), past, past); err != nil {
		t.Fatal(err)
	}
	if err := c.collect(); err != nil {
		t.Fatal(err)
	}
	if c.Has("old", "1") {
		t.Fatal("old survived a collect over the bound")
	}
	c.mu.Lock()
	_, stale := c.sizes["old/1"]
	_, kept := c.sizes["new/2"]
	c.mu.Unlock()
	if stale || !kept {
		t.Fatalf("sizes after eviction: old present %v, new present %v; want the evicted dropped and the kept held", stale, kept)
	}
}

// A HOLD SPANS INSTALL, COMMANDS AND MATERIALISE: Holding takes the prefix's one lock across all
// three, so nothing comes between the build and the links a task takes from it — a concurrent
// install of the same prefix waits out the hold and finds it built, its own build never run, and
// an eviction (which takes the same lock) cannot rename the prefix away in the gap.
func TestHoldingSpansInstallAndMaterialise(t *testing.T) {
	c, err := New(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	inst := t.TempDir()
	installed := make(chan struct{})
	release := make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- c.Holding("jq", "1.6", func(h Held) error {
			if err := h.Install(fill("jq", "j")); err != nil {
				return err
			}
			close(installed)
			<-release
			cmds, err := h.Commands()
			if err != nil {
				return err
			}
			if len(cmds) != 1 || cmds[0] != "jq" {
				t.Errorf("commands %v", cmds)
			}
			return h.Materialise(inst, "jq", "jq-v1")
		})
	}()
	<-installed
	rebuilt := false
	second := make(chan error, 1)
	go func() {
		second <- c.Install("jq", "1.6", func(string) error { rebuilt = true; return nil })
	}()
	close(release)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if rebuilt {
		t.Fatal("the second install rebuilt a prefix the hold had installed")
	}
	if _, err := os.Stat(filepath.Join(inst, "bin", "jq-v1")); err != nil {
		t.Fatalf("the held materialise: %v", err)
	}
}
