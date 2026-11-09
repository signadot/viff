// Package cellar keeps the prefixes a runner has installed: one directory per package and
// version, immutable once written, shared across tasks. A prefix is materialised into a task's
// install root as a hardlinked copy under `opt/` and a relative symlink under `bin/` naming the
// command — near-free on one volume, and safe against eviction, because a task's hardlinks hold
// the prefix's inodes until the task ends. Above a bound the operator sets, the least recently
// used prefixes are evicted.
//
// The cellar lays files out and nothing else: what a prefix holds, and whether it may be
// installed at all, are its caller's (the wrapper that resolves and fetches, and the ceiling).
package cellar

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cellar is the store, rooted at one directory on the volume the tasks' install roots share.
type Cellar struct {
	root string
	// collectAbove is the size in bytes past which collect evicts, 0 for never.
	collectAbove int64

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	// sizes is each installed prefix's bytes, walked once: a prefix is immutable from the
	// rename that lands it, so after its first walk a collect is a ReadDir and arithmetic.
	// An evicted prefix's entry goes with it.
	sizes map[string]int64
}

// New opens the cellar at root, making it where it is absent. collectAbove is the size in bytes
// past which the least recently used prefixes are evicted, 0 for never.
func New(root string, collectAbove int64) (*Cellar, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Cellar{root: root, collectAbove: collectAbove, locks: map[string]*sync.Mutex{}, sizes: map[string]int64{}}, nil
}

// lockFor is the one lock of a prefix, per package and version: two installs of one prefix wait
// on each other, and eviction waits on both.
func (c *Cellar) lockFor(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.locks[key] == nil {
		c.locks[key] = &sync.Mutex{}
	}
	return c.locks[key]
}

// CheckName is why a name cannot be a path segment of the cellar's: empty, a dot name, a
// separator in it, or a leading dot (the cellar's own staging and trash names begin with one).
// It is the one copy of the rule: the wire (delivery.ReadInstall) and the configuration check
// the same names with it, since the layout's safety rests on what this refuses.
func CheckName(what, v string) error {
	if v == "" {
		return errors.New(what + " is empty")
	}
	if strings.ContainsAny(v, "/\\") || v == "." || v == ".." || strings.HasPrefix(v, ".") {
		return fmt.Errorf("%s %q: a name, never a path", what, v)
	}
	return nil
}

// Prefix is where a package's version lies, installed or not.
func (c *Cellar) Prefix(name, version string) string {
	return filepath.Join(c.root, name, version)
}

// Has is whether the prefix is installed.
func (c *Cellar) Has(name, version string) bool {
	if CheckName("package", name) != nil || CheckName("version", version) != nil {
		return false
	}
	_, err := os.Stat(c.Prefix(name, version))
	return err == nil
}

// Held is one prefix while its lock is held: what Holding's function works with. Everything a
// Held does happens under the one lock of its package and version, so nothing comes between an
// install and the links a task takes from it — in the gap, an eviction could rename the prefix
// away and the materialise would find the cellar empty-handed.
type Held struct {
	c             *Cellar
	name, version string
	installed     *bool
}

// Holding runs f with the prefix's lock held. Where f installed, the cellar collects after the
// lock is released — eviction takes each prefix's lock, so collecting under one would deadlock.
func (c *Cellar) Holding(name, version string, f func(Held) error) error {
	if err := CheckName("package", name); err != nil {
		return err
	}
	if err := CheckName("version", version); err != nil {
		return err
	}
	lock := c.lockFor(name + "/" + version)
	lock.Lock()
	installed := false
	err := func() error {
		defer lock.Unlock()
		return f(Held{c: c, name: name, version: version, installed: &installed})
	}()
	if err != nil {
		return err
	}
	if installed {
		return c.collect()
	}
	return nil
}

// Install writes the prefix for a package's version where the cellar does not hold it: build
// fills a staging directory, which becomes the prefix whole, by rename — a prefix is never seen
// half-written, and once written it is immutable, so an install of what is held is a touch and
// no build. After an install the cellar collects.
func (c *Cellar) Install(name, version string, build func(stage string) error) error {
	return c.Holding(name, version, func(h Held) error { return h.Install(build) })
}

// Install is Cellar.Install under the held lock.
func (h Held) Install(build func(stage string) error) error {
	prefix := h.c.Prefix(h.name, h.version)
	if _, err := os.Stat(prefix); err == nil {
		return touch(prefix)
	}
	stage, err := os.MkdirTemp(h.c.root, ".stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := build(stage); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
		return err
	}
	if err := os.Rename(stage, prefix); err != nil {
		return err
	}
	*h.installed = true
	return touch(prefix)
}

// Commands are the commands a prefix ships, its bin directory's entries, sorted.
func (c *Cellar) Commands(name, version string) ([]string, error) {
	if !c.Has(name, version) {
		return nil, fmt.Errorf("the cellar has no %s %s", name, version)
	}
	return commands(c.Prefix(name, version))
}

// Commands is Cellar.Commands under the held lock.
func (h Held) Commands() ([]string, error) {
	prefix := h.c.Prefix(h.name, h.version)
	if _, err := os.Stat(prefix); err != nil {
		return nil, fmt.Errorf("the cellar has no %s %s", h.name, h.version)
	}
	return commands(prefix)
}

func commands(prefix string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(prefix, "bin"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out, nil
}

// Materialise gives a task's install root the prefix and a command of it by a name: the prefix
// hardlinked under `inst/opt/<name>-<version>`, kept where a task holds it already, and
// `inst/bin/<as>` a relative symlink to the command, replaced where it points elsewhere — so two
// versions sit side by side, each under its own name. The command must be one the prefix ships.
func (c *Cellar) Materialise(inst, name, version, cmd, as string) error {
	return c.Holding(name, version, func(h Held) error { return h.Materialise(inst, cmd, as) })
}

// Materialise is Cellar.Materialise under the held lock.
func (h Held) Materialise(inst, cmd, as string) error {
	for what, v := range map[string]string{"command": cmd, "name": as} {
		if err := CheckName(what, v); err != nil {
			return err
		}
	}
	c, name, version := h.c, h.name, h.version
	prefix := c.Prefix(name, version)
	if _, err := os.Stat(prefix); err != nil {
		return fmt.Errorf("the cellar has no %s %s", name, version)
	}
	if _, err := os.Stat(filepath.Join(prefix, "bin", cmd)); err != nil {
		return fmt.Errorf("%s %s ships no command %s", name, version, cmd)
	}
	nv := name + "-" + version
	target := filepath.Join(inst, "opt", nv)
	if _, err := os.Stat(target); err != nil {
		if err := os.MkdirAll(filepath.Join(inst, "opt"), 0o755); err != nil {
			return err
		}
		stage, err := os.MkdirTemp(filepath.Join(inst, "opt"), ".opt-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		staged := filepath.Join(stage, nv)
		if err := linkTree(prefix, staged); err != nil {
			return err
		}
		if err := os.Rename(staged, target); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(inst, "bin"), 0o755); err != nil {
		return err
	}
	link := filepath.Join(inst, "bin", as)
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(filepath.Join("..", "opt", nv, "bin", cmd), link); err != nil {
		return err
	}
	return touch(prefix)
}

// linkTree writes dst as src, each regular file a hardlink, each symlink respelled, each
// directory made with its mode: `cp -al`, within one volume.
func linkTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(to, info.Mode().Perm())
		case d.Type()&fs.ModeSymlink != 0:
			dest, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(dest, to)
		default:
			return os.Link(path, to)
		}
	})
}

// touch marks the prefix used now, which eviction orders by.
func touch(prefix string) error {
	now := time.Now()
	return os.Chtimes(prefix, now, now)
}

// collect evicts the least recently used prefixes while the cellar holds more bytes than the
// bound, under each prefix's lock: an eviction renames the prefix out of its place first, so no
// reader sees it half-removed, and a task that materialised it keeps its inodes by its hardlinks.
func (c *Cellar) collect() error {
	if c.collectAbove <= 0 {
		return nil
	}
	type entry struct {
		name, version string
		size          int64
		used          time.Time
	}
	var entries []entry
	var total int64
	names, err := os.ReadDir(c.root)
	if err != nil {
		return err
	}
	for _, n := range names {
		if !n.IsDir() || strings.HasPrefix(n.Name(), ".") {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(c.root, n.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			continue // another collect evicted it since the root was read: its bytes are freed either way
		}
		if err != nil {
			return err
		}
		for _, v := range versions {
			if !v.IsDir() || strings.HasPrefix(v.Name(), ".") {
				continue
			}
			prefix := filepath.Join(c.root, n.Name(), v.Name())
			info, err := os.Stat(prefix)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			size, err := c.size(n.Name(), v.Name())
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			entries = append(entries, entry{n.Name(), v.Name(), size, info.ModTime()})
			total += size
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].used.Before(entries[j].used) })
	for _, e := range entries {
		if total <= c.collectAbove {
			return nil
		}
		lock := c.lockFor(e.name + "/" + e.version)
		lock.Lock()
		err := func() error {
			defer lock.Unlock()
			prefix := c.Prefix(e.name, e.version)
			if _, err := os.Stat(prefix); err != nil {
				return nil // gone already
			}
			trash, err := os.MkdirTemp(c.root, ".rm-")
			if err != nil {
				return err
			}
			if err := os.Rename(prefix, filepath.Join(trash, "x")); err != nil {
				os.RemoveAll(trash)
				return err
			}
			// The size entry goes under the same lock as the rename: past the lock, a
			// reinstall measures its own prefix, never reads this one's.
			c.mu.Lock()
			delete(c.sizes, e.name+"/"+e.version)
			c.mu.Unlock()
			return os.RemoveAll(trash)
		}()
		if err != nil {
			return err
		}
		total -= e.size
	}
	return nil
}

// size is the bytes under an installed prefix, read once and kept: immutable from its rename in.
func (c *Cellar) size(name, version string) (int64, error) {
	key := name + "/" + version
	c.mu.Lock()
	s, ok := c.sizes[key]
	c.mu.Unlock()
	if ok {
		return s, nil
	}
	s, err := sizeOf(c.Prefix(name, version))
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.sizes[key] = s
	c.mu.Unlock()
	return s, nil
}

// sizeOf is the bytes under a prefix: each regular file's once, symlinks and directories free.
func sizeOf(prefix string) (int64, error) {
	var total int64
	err := filepath.WalkDir(prefix, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
