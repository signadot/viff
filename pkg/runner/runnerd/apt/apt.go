// Package apt wraps the Debian package manager as the runner's install wrapper: it resolves a
// package's dependency closure against the signed index of the repositories the operator trusts,
// reads the fetch plan, fetches each package by the index's SHA256, extracts it with none of its
// own scripts run, and assembles a self-contained prefix for the cellar under the runpath
// discipline — `$ORIGIN/../lib` on each executable, `$ORIGIN` on each bundled library, so the
// prefix runs with no LD_LIBRARY_PATH and collides with nothing.
//
// Everything runs non-root. The state apt needs (lists, archives) is redirected to a private
// writable directory; the base-provides set is the runner image's own dpkg status, so a closure
// is only the delta past the image, and libc is never bundled. Integrity rides apt's own chain:
// `update` verifies the Release signature with the image's keyrings, and each package is fetched
// by the SHA256 the verified index carries, never the plan's MD5.
package apt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Exec runs one command of the install toolchain and answers its stdout. It exists so the
// wrapper's reading of apt is tested where apt is not: nil is the host's commands.
type Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

// Apt is the wrapper over one set of repositories.
type Apt struct {
	// State is the wrapper's own writable directory: the sources.list it writes and the lists
	// `update` fetches, both bounded by the repositories named. Fetched packages live with the
	// build that fetched them, never here.
	State string
	// Repos are the sources.list lines the operator trusts
	// (`deb http://deb.debian.org/debian bookworm main`).
	Repos []string
	// Status is the dpkg status file naming what the image provides, which a closure is resolved
	// past; empty is /var/lib/dpkg/status.
	Status string
	// Exec runs the toolchain's commands; nil is the host's.
	Exec Exec
	// Client fetches packages; nil is a client of its own.
	Client *http.Client
	// MaxIndexAge is how old the lists may grow before a resolve refreshes them; 0 is a day.
	MaxIndexAge time.Duration

	mu      sync.Mutex
	updated time.Time
	// updating single-flights the index refresh: two resolves finding the index stale at once
	// would both run `apt-get update` into one state directory, and the loser would fail on
	// apt's lists lock.
	updating sync.Mutex
}

// Pkg is one package of a closure: what to fetch, from where, and the hash the verified index
// gives it.
type Pkg struct {
	Name, Version string
	URL           string
	SHA256        string
}

// Resolution is what installing a package means here: the version that lands, and the closure to
// fetch — empty with Provided set where the image already provides it, so there is nothing to
// install and nothing to bundle.
type Resolution struct {
	Name, Version string
	Closure       []Pkg
	Provided      bool
}

func (a *Apt) status() string {
	if a.Status != "" {
		return a.Status
	}
	return "/var/lib/dpkg/status"
}

func (a *Apt) maxAge() time.Duration {
	if a.MaxIndexAge > 0 {
		return a.MaxIndexAge
	}
	return 24 * time.Hour
}

func (a *Apt) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if a.Exec != nil {
		return a.Exec(ctx, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(errs.String()))
	}
	return out.Bytes(), nil
}

// opts is how every apt command is pointed away from the system and at the operator's repos: the
// state and cache in the wrapper's own directory, the sources it writes, the image's dpkg status
// as what is installed, and no privilege drop (there is no _apt user to drop to).
func (a *Apt) opts() []string {
	s := a.State
	return []string{
		"-q",
		"-o", "Dir::State::Lists=" + filepath.Join(s, "lists"),
		"-o", "Dir::Cache=" + filepath.Join(s, "cache"),
		"-o", "Dir::Cache::Archives=" + filepath.Join(s, "archives"),
		"-o", "Dir::Etc::SourceList=" + filepath.Join(s, "sources.list"),
		"-o", "Dir::Etc::SourceParts=/dev/null",
		"-o", "Dir::State::status=" + a.status(),
		"-o", "APT::Sandbox::User=",
	}
}

// Update fetches the repositories' indices, signatures verified by apt against the image's
// keyrings, into the wrapper's own state.
func (a *Apt) Update(ctx context.Context) error {
	for _, dir := range []string{"lists/partial", "cache"} {
		if err := os.MkdirAll(filepath.Join(a.State, dir), 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(a.State, "sources.list"), []byte(strings.Join(a.Repos, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	if _, err := a.run(ctx, "apt-get", append(a.opts(), "update")...); err != nil {
		return fmt.Errorf("updating the index: %w", err)
	}
	a.mu.Lock()
	a.updated = time.Now()
	a.mu.Unlock()
	return nil
}

// ensureIndex updates where the index was never fetched by this process or has grown older than
// MaxIndexAge — the `brew update` of the system, implicit. One update runs at a time: a second
// caller finding the index stale waits on the first and reads the clock again, so concurrent
// resolves never race `apt-get update` against itself.
func (a *Apt) ensureIndex(ctx context.Context) error {
	a.updating.Lock()
	defer a.updating.Unlock()
	a.mu.Lock()
	fresh := !a.updated.IsZero() && time.Since(a.updated) < a.maxAge()
	a.mu.Unlock()
	if fresh {
		return nil
	}
	return a.Update(ctx)
}

// instLine reads one of `apt-get install -s`'s plan lines: the package, and the version that
// would land (`Inst jq (1.6-2.1+deb12u2 Debian:12.5/stable [amd64])`, `[old]` before the
// parenthesis on an upgrade).
var instLine = regexp.MustCompile(`(?m)^Inst (\S+)(?: \[\S+\])? \((\S+) `)

// uriLine reads one of `--print-uris`'s plan lines: the url and the pool file it lands as. The
// hash it prints is MD5 and is not read; integrity is the index's SHA256.
var uriLine = regexp.MustCompile(`(?m)^'([^']+)' (\S+) \d+ `)

// Resolve is the closure installing a package means, past what the image provides: resolved
// against the index (refreshed when stale), the named version where one is given, the
// repositories' candidate where none. A package the image already provides resolves to no
// closure, Provided, at the version the image holds.
func (a *Apt) Resolve(ctx context.Context, name, version string) (Resolution, error) {
	if err := a.ensureIndex(ctx); err != nil {
		return Resolution{}, err
	}
	spec := name
	if version != "" {
		spec = name + "=" + version
	}
	sim, err := a.run(ctx, "apt-get", append(a.opts(), "install", "-s", "--no-install-recommends", spec)...)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolving %s: %w", spec, err)
	}
	matches := instLine.FindAllStringSubmatch(string(sim), -1)
	if len(matches) == 0 {
		installed, err := a.installedVersion(ctx, name)
		if err != nil {
			return Resolution{}, err
		}
		if version != "" && installed != version {
			return Resolution{}, fmt.Errorf("the image provides %s %s, and %s is not installable beside it", name, installed, version)
		}
		return Resolution{Name: name, Version: installed, Provided: true}, nil
	}
	uris, err := a.run(ctx, "apt-get", append(a.opts(), "install", "-y", "--no-install-recommends", "--print-uris", spec)...)
	if err != nil {
		return Resolution{}, fmt.Errorf("reading the fetch plan for %s: %w", spec, err)
	}
	urlOf := map[string]string{} // pool file -> url
	for _, m := range uriLine.FindAllStringSubmatch(string(uris), -1) {
		urlOf[m[2]] = m[1]
	}
	r := Resolution{Name: name}
	for _, m := range matches {
		p := Pkg{Name: m[1], Version: m[2]}
		file, sha, err := a.indexEntry(ctx, p.Name, p.Version)
		if err != nil {
			return Resolution{}, err
		}
		p.SHA256 = sha
		p.URL = urlOf[filepath.Base(file)]
		if p.URL == "" {
			return Resolution{}, fmt.Errorf("the fetch plan names no url for %s %s (%s)", p.Name, p.Version, file)
		}
		if p.Name == name {
			r.Version = p.Version
		}
		r.Closure = append(r.Closure, p)
	}
	if r.Version == "" {
		return Resolution{}, fmt.Errorf("the plan for %s installs %d packages and %s is not among them", spec, len(r.Closure), name)
	}
	return r, nil
}

// indexEntry is a package's pool file and SHA256 as the verified index carries them
// (`apt-cache show`), at the one version the closure names.
func (a *Apt) indexEntry(ctx context.Context, name, version string) (file, sha string, err error) {
	out, err := a.run(ctx, "apt-cache", append(a.opts(), "show", name+"="+version)...)
	if err != nil {
		return "", "", fmt.Errorf("reading the index for %s=%s: %w", name, version, err)
	}
	for _, paragraph := range strings.Split(string(out), "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(paragraph, "\n") {
			if k, v, ok := strings.Cut(line, ": "); ok && !strings.HasPrefix(line, " ") {
				fields[k] = strings.TrimSpace(v)
			}
		}
		if fields["Package"] == name && fields["Version"] == version && fields["SHA256"] != "" && fields["Filename"] != "" {
			if !hexSHA256(fields["SHA256"]) {
				return "", "", fmt.Errorf("the index's SHA256 for %s %s is no sha256: %q", name, version, fields["SHA256"])
			}
			return fields["Filename"], fields["SHA256"], nil
		}
	}
	return "", "", fmt.Errorf("the index carries no SHA256 for %s %s", name, version)
}

// hexSHA256 is whether s is a sha256 as the index spells one, 64 hex digits: everything the
// wrapper does with a hash — slicing a cache name from it, comparing a fetch against it — holds
// only for one, and a malformed entry is refused by name rather than trusted.
func hexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// installedVersion is the version the image provides (`apt-cache policy`'s Installed line).
func (a *Apt) installedVersion(ctx context.Context, name string) (string, error) {
	out, err := a.run(ctx, "apt-cache", append(a.opts(), "policy", name)...)
	if err != nil {
		return "", fmt.Errorf("reading %s's versions: %w", name, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Installed: "); ok && v != "(none)" {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s resolves to nothing to install, and the image does not provide it", name)
}

// Build is the cellar's build for a resolution: fetch each package of the closure by the index's
// hash, refuse one that carries an install script beyond the stock ldconfig stanza (nothing of a
// package's runs here, and runpaths replace ldconfig), extract, and assemble the prefix — the
// named package's commands under bin/ and its share/ beside them, every package's libraries flat
// under lib/, runpaths set on each ELF.
func (a *Apt) Build(ctx context.Context, r Resolution) func(stage string) error {
	return func(stage string) error {
		trees := filepath.Join(stage, ".x")
		debs := filepath.Join(stage, ".debs")
		var named string
		for _, p := range r.Closure {
			deb, err := a.fetch(ctx, debs, p)
			if err != nil {
				return err
			}
			if err := a.checkScripts(ctx, p, deb, trees); err != nil {
				return err
			}
			tree := filepath.Join(trees, p.Name)
			if err := os.MkdirAll(tree, 0o755); err != nil {
				return err
			}
			if _, err := a.run(ctx, "dpkg-deb", "-x", deb, tree); err != nil {
				return fmt.Errorf("extracting %s: %w", p.Name, err)
			}
			if p.Name == r.Name {
				named = tree
			}
		}
		if named == "" {
			return fmt.Errorf("the closure holds no %s", r.Name)
		}
		if err := a.assemble(ctx, stage, named, trees); err != nil {
			return err
		}
		if err := os.RemoveAll(trees); err != nil {
			return err
		}
		return os.RemoveAll(debs)
	}
}

// fetch brings one package into dir, a directory of the build's own, and answers its path: by
// the url the plan gave, held to the SHA256 the index gave, a file already there reverified
// before it is reused. The fetched files live and die with the build — the cellar's prefixes are
// the cache, so nothing accumulates where no bound governs it.
func (a *Apt) fetch(ctx context.Context, dir string, p Pkg) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, p.Name+"_"+p.SHA256[:16]+".deb")
	if ok, _ := hashes(dest, p.SHA256); ok {
		return dest, nil
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", p.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: %s", p.Name, resp.Status)
	}
	tmp, err := os.CreateTemp(dir, ".fetch-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		return "", fmt.Errorf("fetching %s: %w", p.Name, err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != p.SHA256 {
		return "", fmt.Errorf("%s %s does not match the index: fetched sha256 %s, want %s", p.Name, p.Version, got, p.SHA256)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return "", err
	}
	return dest, nil
}

// hashes is whether the file is present with the given SHA256.
func hashes(path, want string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == want, nil
}

// checkScripts refuses a package whose install scripts do more than the stock ldconfig stanza.
// No script of a package's runs here, so a package that needs one would land half-configured:
// better refused, naming the script, than installed broken. The one shape waved through is the
// postinst dh gives every library — set -e, `if configure then ldconfig fi` — because runpaths
// do ldconfig's work in a prefix.
func (a *Apt) checkScripts(ctx context.Context, p Pkg, deb, trees string) error {
	ctrl := filepath.Join(trees, "."+p.Name+"-control")
	if err := os.MkdirAll(ctrl, 0o755); err != nil {
		return err
	}
	if _, err := a.run(ctx, "dpkg-deb", "-e", deb, ctrl); err != nil {
		return fmt.Errorf("reading %s's control: %w", p.Name, err)
	}
	for _, script := range []string{"preinst", "postinst"} {
		body, err := os.ReadFile(filepath.Join(ctrl, script))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !ldconfigOnly(string(body)) {
			return fmt.Errorf("%s %s carries a %s script, which does not run here: the package would land half-configured, so it is refused", p.Name, p.Version, script)
		}
	}
	return os.RemoveAll(ctrl)
}

// ldconfigOnly is whether a maintainer script is the stock dh ldconfig stanza and nothing else.
var ldconfigLines = map[string]bool{
	"#!/bin/sh": true, "set -e": true,
	`if [ "$1" = "configure" ]; then`: true, "ldconfig": true, "fi": true, "exit 0": true,
}

func ldconfigOnly(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#!"): // a comment
		case ldconfigLines[line]: // the shebang and the stanza's lines
		default:
			return false
		}
	}
	return true
}

// assemble lays the prefix out of the extracted trees: the named package's commands into bin/
// (read through a packaging symlink) and its share/ whole, every package's shared objects flat
// into lib/ with their soname links respelled, then the runpath discipline on each ELF —
// `$ORIGIN/../lib` on a command, `$ORIGIN` on a library, because DT_RUNPATH does not reach a
// transitive dependency, so a library must find its own siblings itself.
func (a *Apt) assemble(ctx context.Context, stage, named, trees string) error {
	for _, dir := range []string{"bin", "lib"} {
		if err := os.MkdirAll(filepath.Join(stage, dir), 0o755); err != nil {
			return err
		}
	}
	for _, bins := range []string{"usr/bin", "bin"} {
		entries, err := os.ReadDir(filepath.Join(named, bins))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			src := filepath.Join(named, bins, e.Name())
			info, err := os.Stat(src) // through a packaging symlink
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			to := filepath.Join(stage, "bin", e.Name())
			if _, err := os.Lstat(to); err == nil {
				continue // usr/bin landed it: a pre-usrmerge /bin carries the same command as a symlink
			}
			if err := copyFile(src, to, info.Mode().Perm()); err != nil {
				return err
			}
		}
	}
	if _, err := os.Stat(filepath.Join(named, "usr/share")); err == nil {
		if err := copyTree(filepath.Join(named, "usr/share"), filepath.Join(stage, "share")); err != nil {
			return err
		}
	}
	err := filepath.WalkDir(trees, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".so") && !strings.Contains(name, ".so.") {
			return nil
		}
		to := filepath.Join(stage, "lib", name)
		if _, err := os.Lstat(to); err == nil {
			return nil // first one in wins, as the flat dir has one name
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(filepath.Base(target), to)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, to, info.Mode().Perm())
	})
	if err != nil {
		return err
	}
	for _, p := range []struct{ dir, runpath string }{{"bin", "$ORIGIN/../lib"}, {"lib", "$ORIGIN"}} {
		entries, err := os.ReadDir(filepath.Join(stage, p.dir))
		if err != nil {
			return err
		}
		for _, e := range entries {
			path := filepath.Join(stage, p.dir, e.Name())
			if elf, err := isELF(path); err != nil || !elf {
				continue
			}
			if _, err := a.run(ctx, "patchelf", "--set-rpath", p.runpath, path); err != nil {
				return fmt.Errorf("setting %s's runpath: %w", e.Name(), err)
			}
		}
	}
	return nil
}

// isELF is whether a regular file begins with the ELF magic; a script or a data file is left as
// it is.
func isELF(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false, nil // shorter than a header is no ELF
	}
	return bytes.Equal(magic[:], []byte{0x7f, 'E', 'L', 'F'}), nil
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copyTree writes dst as src: directories made, symlinks respelled, files copied with their mode.
func copyTree(src, dst string) error {
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
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		default:
			info, err := d.Info()
			if err != nil {
				return err
			}
			return copyFile(path, to, info.Mode().Perm())
		}
	})
}
