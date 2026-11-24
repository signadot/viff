package stock

import (
	"slices"

	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/runner/runnerd/apt"
	"go.viff.dev/viff/pkg/runner/runnerd/cellar"
	"go.viff.dev/viff/pkg/runner/runnerd/delivery"
	"go.viff.dev/viff/pkg/systems"
)

// pkgSection is a `pkg:` section that checks out.
func pkgSection() *Pkg {
	return &Pkg{Cellar: "/state/cellar", Manager: "apt",
		Repos: []string{"deb http://deb.debian.org/debian bookworm main"}, CollectAbove: "50Gi"}
}

// THE PKG SECTION IS CHECKED AT LOAD, AND THE SYSTEM IS PERFORMED WHERE IT AND A CEILING ARE: the
// cellar an absolute path under no system directory, the manager one the runner wraps, the
// repositories sources.list lines, and the bound a size.
func TestThePkgSectionIsCheckedAndPerformedWithACeiling(t *testing.T) {
	good := Config{Script: &Script{Systems: map[string]System{"pkg": {Ceiling: systems.Open()}}}, Pkg: pkgSection()}
	if err := good.checkPkg(); err != nil {
		t.Fatal(err)
	}
	if !hasPkg(good) {
		t.Fatal("a section and a ceiling perform pkg")
	}
	if hasPkg(Config{Script: &Script{}, Pkg: pkgSection()}) {
		t.Fatal("pkg is performed with no ceiling naming it")
	}
	if hasPkg(Config{Script: &Script{Systems: map[string]System{"pkg": {Ceiling: systems.Open()}}}}) {
		t.Fatal("pkg is performed with no section")
	}
	for name, breaks := range map[string]struct {
		change func(*Pkg)
		says   string
	}{
		"no cellar":           {func(p *Pkg) { p.Cellar = "" }, "`cellar` names the directory"},
		"a relative cellar":   {func(p *Pkg) { p.Cellar = "state/cellar" }, "absolute path"},
		"a system-dir cellar": {func(p *Pkg) { p.Cellar = "/usr/local/cellar" }, "every task's sandbox has"},
		"another manager":     {func(p *Pkg) { p.Manager = "brew" }, "no manager this runner wraps"},
		"no repos":            {func(p *Pkg) { p.Repos = nil }, "signed repositories"},
		"a repo line that is no sources.list line": {func(p *Pkg) { p.Repos = []string{"http://deb.debian.org bookworm"} }, "sources.list line"},
		"a bound that is no size":                  {func(p *Pkg) { p.CollectAbove = "fifty" }, "no size"},
	} {
		c := Config{Pkg: pkgSection()}
		breaks.change(c.Pkg)
		if err := c.checkPkg(); err == nil || !strings.Contains(err.Error(), breaks.says) {
			t.Errorf("%s: %v, want it to say %q", name, err, breaks.says)
		}
	}
	for s, want := range map[string]int64{"1024": 1024, "512Ki": 512 << 10, "512Mi": 512 << 20, "50Gi": 50 << 30, "2Ti": 2 << 40} {
		if got, err := parseBytes(s); err != nil || got != want {
			t.Errorf("parseBytes(%s) = %d, %v", s, got, err)
		}
	}
	for _, bad := range []string{"", "-1", "0", "1.5Gi", "50G", "Gi", "9999999999999999999Gi"} {
		if _, err := parseBytes(bad); err == nil {
			t.Errorf("parseBytes(%q) read", bad)
		}
	}
}

// fakeWrap answers a fixed resolution and builds a prefix of the commands given.
type fakeWrap struct {
	res  apt.Resolution
	err  error
	cmds []string
}

func (f fakeWrap) Resolve(context.Context, string, string) (apt.Resolution, error) {
	return f.res, f.err
}
func (f fakeWrap) Build(context.Context, apt.Resolution) func(string) error {
	return func(stage string) error {
		if err := os.MkdirAll(filepath.Join(stage, "bin"), 0o755); err != nil {
			return err
		}
		for _, cmd := range f.cmds {
			if err := os.WriteFile(filepath.Join(stage, "bin", cmd), []byte("#!"+cmd), 0o755); err != nil {
				return err
			}
		}
		return nil
	}
}

// THE OFFERED PKG BUILDS PASS THE DESCRIPTION'S OWN CHECK. Every shape of install names `pkg`
// (null: any of the index), so the builds' shape says it too — an offer without it fails
// Description.Check, and viff refuses the runner's announcement, which keeps whatever the
// runner said before and closes pkg to every spawn.
func TestTheOfferedPkgBuildsPassTheDescriptionsCheck(t *testing.T) {
	c := Config{Script: &Script{Systems: map[string]System{"pkg": {Ceiling: systems.Open()}}}, Pkg: pkgSection()}
	d := c.Describe(nil)
	if err := d.Check(); err != nil {
		t.Fatalf("the description does not check: %v", err)
	}
	if len(d.Offers) != 1 {
		t.Fatalf("offers %d, want the script's alone", len(d.Offers))
	}
	builds := d.Offers[0].Systems["pkg"].Builds
	if len(builds) != 1 || len(builds[0].Verbs["install"]) != 1 {
		t.Fatalf("pkg builds %+v, want one install shape", builds)
	}
	if _, named := builds[0].Verbs["install"][0]["pkg"]; !named {
		t.Fatal("the install shape does not name `pkg`")
	}
}

// THE INSTALLER LANDS EVERY COMMAND UNDER ITS OWN NAME, AND `as` RENAMES EXACTLY ONE: the one
// named as the package, or the only one; a package of several commands refuses an `as`, naming
// them, and a package the image provides is answered as provided, which an `as` also refuses.
func TestTheInstallerNamesCommandsAndAsRenamesOne(t *testing.T) {
	dir := t.TempDir()
	store, err := cellar.New(filepath.Join(dir, "cellar"), 0)
	if err != nil {
		t.Fatal(err)
	}
	inst := filepath.Join(dir, "inst")
	ripgrep := &installer{cellar: store, wrap: fakeWrap{res: apt.Resolution{Name: "ripgrep", Version: "14.1.0-1",
		Closure: []apt.Pkg{{Name: "ripgrep", Version: "14.1.0-1"}}}, cmds: []string{"rg"}}}
	got, err := ripgrep.Install(t.Context(), inst, "ripgrep", "", "")
	if err != nil || got.Version != "14.1.0-1" || got.As != "" || len(got.Commands) != 1 || got.Commands[0] != "rg" || got.Provided {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(inst, "bin", "rg")); err != nil {
		t.Fatal("rg is not on the root")
	}
	// `as` renames the only command, though none is named as the package.
	if got, err := ripgrep.Install(t.Context(), inst, "ripgrep", "", "rg-v14"); err != nil || got.As != "rg-v14" {
		t.Fatalf("%+v %v", got, err)
	}

	git := &installer{cellar: store, wrap: fakeWrap{res: apt.Resolution{Name: "git", Version: "1:2.39",
		Closure: []apt.Pkg{{Name: "git", Version: "1:2.39"}}}, cmds: []string{"git", "git-shell", "scalar"}}}
	if got, err := git.Install(t.Context(), inst, "git", "", ""); err == nil {
		// several commands, no `as`: each under its own name, said as the list it is, and As
		// stays empty — a consumer reads As as one name on the PATH, never a list.
		if got.As != "" || strings.Join(got.Commands, " ") != "git git-shell scalar" {
			t.Errorf("landed %+v, want the commands and no As", got)
		}
		for _, cmd := range []string{"git", "git-shell", "scalar"} {
			if _, err := os.Stat(filepath.Join(inst, "bin", cmd)); err != nil {
				t.Errorf("%s is not on the root", cmd)
			}
		}
	} else {
		t.Fatal(err)
	}
	// `as` renames the command named as the package, not the siblings.
	if got, err := git.Install(t.Context(), inst, "git", "", "git-v2"); err != nil || got.As != "git-v2" {
		t.Fatalf("%+v %v", got, err)
	}
	several := &installer{cellar: store, wrap: fakeWrap{res: apt.Resolution{Name: "coreutils", Version: "9",
		Closure: []apt.Pkg{{Name: "coreutils", Version: "9"}}}, cmds: []string{"ls", "cat"}}}
	if _, err := several.Install(t.Context(), inst, "coreutils", "", "cu"); err == nil || !strings.Contains(err.Error(), "renames one command") {
		t.Fatalf("an `as` over several commands: %v", err)
	}

	provided := &installer{cellar: store, wrap: fakeWrap{res: apt.Resolution{Name: "git", Version: "1:2.39", Provided: true}}}
	if got, err := provided.Install(t.Context(), inst, "git", "", ""); err != nil || !got.Provided || got.Version != "1:2.39" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := provided.Install(t.Context(), inst, "git", "", "git-v2"); err == nil || !strings.Contains(err.Error(), "leave it out") {
		t.Fatalf("provided with an `as`: %v", err)
	}

	failed := &installer{cellar: store, wrap: fakeWrap{err: errors.New("no such package")}}
	if _, err := failed.Install(t.Context(), inst, "nope", "", ""); err == nil {
		t.Fatal("a failed resolve installed")
	}
}

// THE INSTALL ROOT'S BIN COMES FIRST ON THE TASK'S PATH, and a task with none gets the default
// with it.
func TestTheInstallRootsBinComesFirstOnPATH(t *testing.T) {
	got := withInstPath([]string{"HOME=/w", "PATH=/usr/bin:/bin"})
	if got[1] != "PATH=/inst/bin:/usr/bin:/bin" {
		t.Fatalf("%v", got)
	}
	if got := withInstPath([]string{"HOME=/w"}); got[1] != "PATH=/inst/bin:"+defaultPath {
		t.Fatalf("%v", got)
	}
}

// A PINNED SHAPE IS INSTALLED BEFORE THE START, AND A PATTERN GRANTS: the doer's base first,
// each at the candidate, then each environment shape whose fields are single literals with the
// package named; a shape with a pattern anywhere, or naming no package, pins nothing.
func TestPinnedShapesAreInstalledBeforeTheStart(t *testing.T) {
	env := systems.Environment{Systems: systems.Systems{"pkg": {{Verbs: map[string][]systems.Shape{"install": {
		{}, // any install: a grant
		{"pkg": ir.FromString("jq"), "version": ir.FromString("1.6-2.1"), "as": ir.FromString("jq-v1")},
		{"pkg": ir.FromString("ripgrep")},
		{"pkg": systems.Glob("lib*")},                                // a pattern: a grant
		{"pkg": ir.FromString("jq"), "version": systems.Glob("1.*")}, // a patterned version: a grant
	}}}}}}
	got := startingInstalls([]string{"git", "curl"}, env)
	want := []delivery.Install{{Pkg: "git"}, {Pkg: "curl"}, {Pkg: "jq", Version: "1.6-2.1", As: "jq-v1"}, {Pkg: "ripgrep"}}
	if !slices.Equal(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
	if got := startingInstalls(nil, systems.Environment{}); len(got) != 0 {
		t.Fatalf("an empty environment pins %v", got)
	}
}

// `base` IS PKG'S ALONE, AND ITS NAMES ARE NAMES: another system's section may not carry one,
// and a base entry that is a path is refused at load.
func TestBaseIsPkgsAloneAndItsNamesAreNames(t *testing.T) {
	good := Config{Pkg: pkgSection(), Script: &Script{Systems: map[string]System{
		"pkg": {Ceiling: systems.Open(), Base: []string{"ripgrep", "jq"}}}}}
	if err := good.checkSystems(ScriptDoer, good.Script.Systems); err != nil {
		t.Fatal(err)
	}
	bad := Config{Pkg: pkgSection(), Script: &Script{Systems: map[string]System{
		"pkg": {Ceiling: systems.Open(), Base: []string{"bin/evil"}}}}}
	if err := bad.checkSystems(ScriptDoer, bad.Script.Systems); err == nil || !strings.Contains(err.Error(), "never a path") {
		t.Fatalf("a base that is a path: %v", err)
	}
	elsewhere := Config{Script: &Script{Systems: map[string]System{
		"question": {Ceiling: systems.Open(), Base: []string{"jq"}}}}}
	if err := elsewhere.checkSystems(ScriptDoer, elsewhere.Script.Systems); err == nil || !strings.Contains(err.Error(), "pkg's alone") {
		t.Fatalf("a base on another system: %v", err)
	}
}
