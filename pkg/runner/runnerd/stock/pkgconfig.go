package stock

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/runner/runnerd/apt"
	"github.com/signadot/verse/pkg/runner/runnerd/cellar"
	"github.com/signadot/verse/pkg/runner/runnerd/delivery"
	"github.com/signadot/verse/pkg/systems"
)

// Pkg is the packages the runner installs into its tasks' read-only prefixes (`pkg:`): the
// cellar it keeps installed prefixes in, the manager it wraps, and the repositories it trusts.
// A doer's `pkg` ceiling bounds which packages its tasks may install, shapes over the install's
// fields; the trust in what an install fetches is the repositories' signed index, so the runner
// holds no credential for the system.
type Pkg struct {
	// Cellar is the directory the runner keeps installed prefixes in, and the tasks' install
	// roots under it: on a volume, since a root is hardlinked from the cellar. One of the
	// runner's own directories: no `readOnly` path may reach it, and no task sees more of it
	// than its own root, through its sandbox's one bind.
	Cellar string `tony:"field=cellar"`
	// Manager is the package manager the runner wraps: `apt`.
	Manager string `tony:"field=manager"`
	// Repos is the repositories the runner trusts, sources.list lines
	// (`deb http://deb.debian.org/debian bookworm main`): `apt-get update` verifies each index's
	// signature with the image's keyrings, and a package is fetched by that index's hash.
	Repos []string `tony:"field=repos"`
	// CollectAbove is the cellar's size past which the least recently used prefixes are evicted
	// (`50Gi`); absent, never. A running task keeps an evicted prefix by its root's hardlinks.
	CollectAbove string `tony:"field=collect-above,omitzero"`
}

// hasPkg is pkg performed: where the runner has a `pkg:` section and a doer's ceiling names the
// system. The sandbox's bind and the toolchain it needs too are not the file's to say:
// Standalone closes the system where the tasks run unconfined or the toolchain is not there.
func hasPkg(c Config) bool { return c.Pkg != nil && len(c.naming("pkg")) > 0 }

// checkPkg checks the `pkg:` section: the cellar's path, the manager, and the repositories.
func (c *Config) checkPkg() error {
	p := c.Pkg
	if p == nil {
		return nil
	}
	switch {
	case p.Cellar == "":
		return fmt.Errorf("pkg: `cellar` names the directory the runner keeps installed prefixes in")
	case !filepath.IsAbs(p.Cellar) || filepath.Clean(p.Cellar) == "/":
		return fmt.Errorf("pkg.cellar: %q is not an absolute path below /", p.Cellar)
	case runnerd.UnderSystem(filepath.Clean(p.Cellar)):
		return fmt.Errorf("pkg.cellar: %s is under a directory every task's sandbox has, and a task sees only its own install root", p.Cellar)
	case p.Manager != "apt":
		return fmt.Errorf("pkg.manager: %q is no manager this runner wraps (apt)", p.Manager)
	case len(p.Repos) == 0:
		return fmt.Errorf("pkg.repos: names the signed repositories the runner trusts (`deb http://deb.debian.org/debian bookworm main`)")
	}
	for i, r := range p.Repos {
		if !strings.HasPrefix(strings.TrimSpace(r), "deb ") {
			return fmt.Errorf("pkg.repos[%d]: %q is no sources.list line (`deb <url> <suite> <component>…`)", i, r)
		}
	}
	if p.CollectAbove != "" {
		if _, err := parseBytes(p.CollectAbove); err != nil {
			return fmt.Errorf("pkg.collect-above: %w", err)
		}
	}
	return nil
}

// collectAbove is the cellar's bound in bytes, 0 for never; checkPkg held it to parsing.
func (p *Pkg) collectAbove() int64 {
	if p == nil || p.CollectAbove == "" {
		return 0
	}
	n, _ := parseBytes(p.CollectAbove)
	return n
}

// parseBytes reads a size: a whole number of bytes, or one with a binary unit (`512Mi`, `50Gi`).
func parseBytes(s string) (int64, error) {
	num, mult := s, int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}} {
		if trimmed, ok := strings.CutSuffix(s, u.suffix); ok {
			num, mult = trimmed, u.mult
			break
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n <= 0 || n > (1<<62)/mult {
		return 0, fmt.Errorf("%q is no size (a whole number of bytes, or with a unit: 512Mi, 50Gi)", s)
	}
	return n * mult, nil
}

// pkgToolchain is the programs the wrapper runs, each expected in the runner's image.
var pkgToolchain = []string{"apt-get", "apt-cache", "dpkg-deb", "patchelf"}

// missingToolchain is the first program of the wrapper's that lookPath does not find, "" where
// all are there.
func missingToolchain(lookPath func(string) (string, error)) string {
	for _, p := range pkgToolchain {
		if _, err := lookPath(p); err != nil {
			return p
		}
	}
	return ""
}

// wrapper is what the installer resolves and builds prefixes with: apt (package apt), or a
// test's stand-in.
type wrapper interface {
	Resolve(ctx context.Context, pkg, version string) (apt.Resolution, error)
	Build(ctx context.Context, r apt.Resolution) func(stage string) error
}

// installer wires the wrapper and the cellar as the delivery channel's Installer: resolve, fill
// the cellar once per version, and materialise into the asking task's root. With no `as`, every
// command the prefix ships lands under its own name, as an install names them; `as` renames the
// one command — the one named as the package, or the only one — so two versions sit side by side.
type installer struct {
	wrap   wrapper
	cellar *cellar.Cellar
}

func (i *installer) Install(ctx context.Context, inst, pkg, version, as string) (delivery.Installed, error) {
	r, err := i.wrap.Resolve(ctx, pkg, version)
	if err != nil {
		return delivery.Installed{}, err
	}
	if r.Provided {
		if as != "" {
			return delivery.Installed{}, fmt.Errorf("the image provides %s %s already, on PATH under its own name; `as` renames an installed command, so leave it out", pkg, r.Version)
		}
		return delivery.Installed{Version: r.Version, Provided: true}, nil
	}
	// One hold spans the build and the links the task takes from it: released between the two,
	// a collect could evict the just-built prefix before it is materialised.
	var renamed string
	var landed []string
	err = i.cellar.Holding(pkg, r.Version, func(h cellar.Held) error {
		if err := h.Install(i.wrap.Build(ctx, r)); err != nil {
			return err
		}
		cmds, err := h.Commands()
		if err != nil {
			return err
		}
		if len(cmds) == 0 {
			return fmt.Errorf("%s %s ships no command", pkg, r.Version)
		}
		if as == "" {
			for _, cmd := range cmds {
				if err := h.Materialise(inst, cmd, cmd); err != nil {
					return err
				}
			}
			landed = cmds
			return nil
		}
		var cmd string
		switch {
		case slices.Contains(cmds, pkg):
			cmd = pkg
		case len(cmds) == 1:
			cmd = cmds[0]
		default:
			return fmt.Errorf("%s %s ships %s: `as` renames one command, so leave it out and call them by their own names",
				pkg, r.Version, strings.Join(cmds, ", "))
		}
		if err := h.Materialise(inst, cmd, as); err != nil {
			return err
		}
		renamed = as
		return nil
	})
	if err != nil {
		return delivery.Installed{}, err
	}
	return delivery.Installed{Version: r.Version, As: renamed, Commands: landed}, nil
}

// startingInstalls is what the runner installs into a task's root before its process starts:
// the doer's base, each at the index's candidate under its own commands' names, then the
// environment's pinned installs. An install that fails refuses the launch, naming the package.
func startingInstalls(base []string, env systems.Environment) []delivery.Install {
	var out []delivery.Install
	for _, b := range base {
		out = append(out, delivery.Install{Pkg: b})
	}
	return append(out, pinnedInstalls(env)...)
}

// pinnedInstalls is the installs a spawn's environment pins: each `install` shape whose fields
// are single literals, the package named, as the system's document says — a shape that pins the
// package is installed before the process starts, and one with a pattern anywhere admits
// installs the task asks for, pinning none.
func pinnedInstalls(env systems.Environment) []delivery.Install {
	var out []delivery.Install
	for _, b := range env.Systems["pkg"] {
		for _, shape := range b.Verbs["install"] {
			if ins, ok := pinned(shape); ok {
				out = append(out, ins)
			}
		}
	}
	return out
}

// pinned is the one install a shape pins, false where it names no package or holds a pattern.
func pinned(shape systems.Shape) (delivery.Install, bool) {
	one := func(name string) (string, bool) {
		n, has := shape[name]
		if !has {
			return "", true
		}
		lits, ok := systems.Literals(n)
		if !ok || len(lits) != 1 {
			return "", false
		}
		return lits[0], true
	}
	pkg, ok := one("pkg")
	if !ok || pkg == "" {
		return delivery.Install{}, false
	}
	version, ok := one("version")
	if !ok {
		return delivery.Install{}, false
	}
	as, ok := one("as")
	if !ok {
		return delivery.Install{}, false
	}
	ins, why := delivery.ReadInstall(pkg, version, as)
	return ins, why == ""
}

// pkgBuilds is what the runner builds of pkg, by its nature: an install from the repositories it
// trusts, any package of their index under any name, as an offer's `builds` says it. The
// ceiling, not the index, is the operator's bound. The shape names `pkg`, null, since every shape
// of install says it; a shape without it fails the description's own check, and verse would
// refuse the runner's announcement. It names `as`, null, too: a shape that says nothing of it
// means no rename, and a runner builds renames.
func (c Config) pkgBuilds() systems.Ceiling {
	return systems.Ceiling{{Verbs: map[string][]systems.Shape{"install": {{"pkg": ir.Null(), "as": ir.Null()}}}}}
}

// withInstPath is env with the install root's bin first on PATH, so the task's shell finds what
// the runner installs; a task with no PATH of its own gets the default with it.
func withInstPath(env []string) []string {
	bin := runnerd.InstPath + "/bin"
	out := slices.Clone(env)
	for i, kv := range out {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			out[i] = "PATH=" + bin + ":" + v
			return out
		}
	}
	return append(out, "PATH="+bin+":"+defaultPath)
}
