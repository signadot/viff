package apt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// stripOpts drops the -o redirections and -q, which point apt away from the system and say the
// same thing in every call: what remains is the command a reader would type.
func stripOpts(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-q":
		case "-o":
			i++
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// fakeApt answers each toolchain command from a script of outputs, keyed by the command with its
// redirections stripped, and records what ran.
func fakeApt(t *testing.T, calls *[]string, outs map[string]string) Exec {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		t.Helper()
		key := name + " " + strings.Join(stripOpts(args), " ")
		*calls = append(*calls, key)
		out, ok := outs[key]
		if !ok {
			t.Fatalf("no script for %q", key)
		}
		return []byte(out), nil
	}
}

const simJq = `NOTE: This is only a simulation!
Inst libonig5 (6.9.8-1 Debian:12.5/stable [amd64])
Inst libjq1 (1.6-2.1+deb12u2 Debian:12.5/stable [amd64])
Inst jq (1.6-2.1+deb12u2 Debian:12.5/stable [amd64])
Conf libonig5 (6.9.8-1 Debian:12.5/stable [amd64])
`

const urisJq = `'http://deb.example/debian/pool/main/o/onig/libonig5_6.9.8-1_amd64.deb' libonig5_6.9.8-1_amd64.deb 735848 MD5Sum:aa
'http://deb.example/debian/pool/main/j/jq/libjq1_1.6-2.1+deb12u2_amd64.deb' libjq1_1.6-2.1+deb12u2_amd64.deb 1355 MD5Sum:bb
'http://deb.example/debian/pool/main/j/jq/jq_1.6-2.1+deb12u2_amd64.deb' jq_1.6-2.1+deb12u2_amd64.deb 64432 MD5Sum:cc
`

// shaOf is a deterministic sha256 for a fixture, spelled as the index spells one.
func shaOf(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

func show(name, version, dir string) string {
	return fmt.Sprintf(`Package: %[1]s
Version: %[2]s
Filename: pool/main/%[3]s/%[1]s_%[2]s_amd64.deb
Size: 1
SHA256: %[4]s

`, name, version, dir, shaOf(name))
}

// RESOLVE READS THE CLOSURE AND THE PLAN FROM THE INDEX: the packages `install -s` plans, each
// with the url `--print-uris` gives its pool file and the SHA256 the verified index carries —
// never the plan's MD5 — and the named package's version among them. A pinned version rides the
// spec, and a package the image provides resolves to no closure, at the image's version.
func TestResolveReadsTheClosureFromTheIndex(t *testing.T) {
	var calls []string
	a := &Apt{
		State: t.TempDir(),
		Repos: []string{"deb http://deb.example/debian bookworm main"},
		Exec: fakeApt(t, &calls, map[string]string{
			"apt-get update": "",
			"apt-get install -s --no-install-recommends jq":              simJq,
			"apt-get install -y --no-install-recommends --print-uris jq": urisJq,
			"apt-cache show libonig5=6.9.8-1":                            show("libonig5", "6.9.8-1", "o/onig"),
			"apt-cache show libjq1=1.6-2.1+deb12u2":                      show("libjq1", "1.6-2.1+deb12u2", "j/jq"),
			"apt-cache show jq=1.6-2.1+deb12u2":                          show("jq", "1.6-2.1+deb12u2", "j/jq"),
		}),
	}
	r, err := a.Resolve(t.Context(), "jq", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Provided || r.Version != "1.6-2.1+deb12u2" || len(r.Closure) != 3 {
		t.Fatalf("%+v", r)
	}
	jq := r.Closure[2]
	if jq.Name != "jq" || jq.URL != "http://deb.example/debian/pool/main/j/jq/jq_1.6-2.1+deb12u2_amd64.deb" || jq.SHA256 != shaOf("jq") {
		t.Fatalf("jq: %+v", jq)
	}
	if calls[0] != "apt-get update" {
		t.Fatalf("the index is refreshed first: %v", calls)
	}
	if list, err := os.ReadFile(filepath.Join(a.State, "sources.list")); err != nil || strings.TrimSpace(string(list)) != a.Repos[0] {
		t.Fatalf("sources.list: %q %v", list, err)
	}

	// A second resolve within the index's age updates nothing.
	calls = nil
	if _, err := a.Resolve(t.Context(), "jq", ""); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(calls, "apt-get update") {
		t.Fatalf("a fresh index was refetched: %v", calls)
	}

	// A pinned version rides the spec end to end.
	pinned := &Apt{State: t.TempDir(), Repos: a.Repos, Exec: fakeApt(t, &calls, map[string]string{
		"apt-get update": "",
		"apt-get install -s --no-install-recommends jq=1.7.1-1":              "Inst jq [1.6-2.1+deb12u2] (1.7.1-1 Debian:12.6/stable [amd64])\n",
		"apt-get install -y --no-install-recommends --print-uris jq=1.7.1-1": "'http://deb.example/debian/pool/main/j/jq/jq_1.7.1-1_amd64.deb' jq_1.7.1-1_amd64.deb 6 MD5Sum:cc\n",
		"apt-cache show jq=1.7.1-1":                                          show("jq", "1.7.1-1", "j/jq"),
	})}
	if r, err := pinned.Resolve(t.Context(), "jq", "1.7.1-1"); err != nil || r.Version != "1.7.1-1" || len(r.Closure) != 1 {
		t.Fatalf("%+v %v", r, err)
	}

	// What the image provides resolves to no closure, and a version beside the image's is refused.
	img := &Apt{State: t.TempDir(), Repos: a.Repos, Exec: fakeApt(t, &calls, map[string]string{
		"apt-get update": "",
		"apt-get install -s --no-install-recommends git":          "git is already the newest version (1:2.39.5-0+deb12u2).\n",
		"apt-cache policy git":                                    "git:\n  Installed: 1:2.39.5-0+deb12u2\n  Candidate: 1:2.39.5-0+deb12u2\n",
		"apt-get install -s --no-install-recommends git=1:2.40-1": "git is already the newest version.\n",
	})}
	if r, err := img.Resolve(t.Context(), "git", ""); err != nil || !r.Provided || r.Version != "1:2.39.5-0+deb12u2" || len(r.Closure) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := img.Resolve(t.Context(), "git", "1:2.40-1"); err == nil || !strings.Contains(err.Error(), "not installable beside") {
		t.Fatalf("a version beside the image's: %v", err)
	}
}

// elfFile is bytes that read as an ELF to the assembler.
func elfFile(tail string) []byte { return append([]byte{0x7f, 'E', 'L', 'F'}, []byte(tail)...) }

// debs serves fake pool files and answers each one's Pkg, hash true to the bytes.
func debs(t *testing.T, bodies map[string][]byte) (*httptest.Server, func(name, version string) Pkg) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, func(name, version string) Pkg {
		sum := sha256.Sum256(bodies[name])
		return Pkg{Name: name, Version: version, URL: srv.URL + "/" + name, SHA256: hex.EncodeToString(sum[:])}
	}
}

const stockPostinst = `#!/bin/sh
set -e
# Automatically added by dh_makeshlibs/13.11.4
if [ "$1" = "configure" ]; then
	ldconfig
fi
# End automatically added section
`

// BUILD FETCHES BY THE INDEX'S HASH AND ASSEMBLES THE PREFIX UNDER THE RUNPATH DISCIPLINE: the
// named package's commands and share, every package's libraries flat with their soname links,
// `$ORIGIN/../lib` on each ELF command and `$ORIGIN` on each real library; a hash that does not
// match refuses the fetch, and a maintainer script beyond the stock ldconfig stanza refuses the
// package by name.
func TestBuildFetchesByHashAndAssemblesThePrefix(t *testing.T) {
	_, pkg := debs(t, map[string][]byte{"jq": []byte("jq deb"), "libjq1": []byte("libjq1 deb")})
	var patched []string
	exec := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "dpkg-deb":
			mode, deb, dir := args[0], args[1], args[2]
			base := strings.Split(filepath.Base(deb), "_")[0]
			if mode == "-e" {
				if base == "libjq1" {
					return nil, os.WriteFile(filepath.Join(dir, "postinst"), []byte(stockPostinst), 0o755)
				}
				return nil, nil
			}
			switch base {
			case "jq":
				if err := os.MkdirAll(filepath.Join(dir, "usr/bin"), 0o755); err != nil {
					return nil, err
				}
				if err := os.MkdirAll(filepath.Join(dir, "usr/share/doc/jq"), 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(dir, "usr/share/doc/jq/README"), []byte("docs"), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(dir, "usr/bin/jq"), elfFile("jq"), 0o755); err != nil {
					return nil, err
				}
				// A pre-usrmerge /bin carrying the same command as a packaging symlink: one
				// name lands once, not a "file exists" refusal.
				if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
					return nil, err
				}
				return nil, os.Symlink("../usr/bin/jq", filepath.Join(dir, "bin/jq"))
			case "libjq1":
				lib := filepath.Join(dir, "usr/lib/x86_64-linux-gnu")
				if err := os.MkdirAll(lib, 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(lib, "libjq.so.1.0.4"), elfFile("libjq"), 0o644); err != nil {
					return nil, err
				}
				return nil, os.Symlink("libjq.so.1.0.4", filepath.Join(lib, "libjq.so.1"))
			}
			return nil, fmt.Errorf("no fixture for %s", deb)
		case "patchelf":
			patched = append(patched, args[1]+" "+filepath.Base(args[2]))
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected %s", name)
	}
	a := &Apt{State: t.TempDir(), Exec: exec}
	r := Resolution{Name: "jq", Version: "1.6", Closure: []Pkg{pkg("libjq1", "1.6"), pkg("jq", "1.6")}}
	stage := t.TempDir()
	if err := a.Build(t.Context(), r)(stage); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(stage, "bin/jq")); err != nil || string(b) != string(elfFile("jq")) {
		t.Fatalf("bin/jq: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(stage, "share/doc/jq/README")); err != nil || string(b) != "docs" {
		t.Fatalf("share: %v", err)
	}
	if got, err := os.Readlink(filepath.Join(stage, "lib/libjq.so.1")); err != nil || got != "libjq.so.1.0.4" {
		t.Fatalf("the soname link: %q %v", got, err)
	}
	want := []string{"$ORIGIN/../lib jq", "$ORIGIN libjq.so.1.0.4"}
	if !slices.Equal(patched, want) {
		t.Fatalf("runpaths: %v, want %v", patched, want)
	}
	if _, err := os.Stat(filepath.Join(stage, ".x")); !os.IsNotExist(err) {
		t.Fatal("the extracted trees linger")
	}
	if _, err := os.Stat(filepath.Join(stage, ".debs")); !os.IsNotExist(err) {
		t.Fatal("the fetched packages linger")
	}
	if _, err := os.Stat(filepath.Join(a.State, "archives")); !os.IsNotExist(err) {
		t.Fatal("an archives cache grows in the state directory, where no bound governs it")
	}

	// A fetched package is held to the index's hash.
	bad := r
	bad.Closure = []Pkg{{Name: "jq", Version: "1.6", URL: pkg("jq", "1.6").URL, SHA256: strings.Repeat("0", 64)}}
	if err := a.Build(t.Context(), bad)(t.TempDir()); err == nil || !strings.Contains(err.Error(), "does not match the index") {
		t.Fatalf("a wrong hash: %v", err)
	}

	// A maintainer script beyond the stock ldconfig stanza refuses the package.
	evil := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dpkg-deb" && args[0] == "-e" {
			return nil, os.WriteFile(filepath.Join(args[2], "postinst"), []byte("#!/bin/sh\nupdate-alternatives --install x\n"), 0o755)
		}
		return nil, fmt.Errorf("unexpected %s %v", name, args)
	}
	b := &Apt{State: t.TempDir(), Exec: evil}
	scripted := Resolution{Name: "jq", Version: "1.6", Closure: []Pkg{pkg("jq", "1.6")}}
	err := b.Build(t.Context(), scripted)(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "postinst") || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a scripted package: %v", err)
	}
}

// THE STOCK LDCONFIG STANZA AND NOTHING ELSE IS WAVED THROUGH: comments and blanks read past,
// any other line refuses.
func TestOnlyTheStockLdconfigStanzaIsWavedThrough(t *testing.T) {
	for body, ok := range map[string]bool{
		stockPostinst:                          true,
		"#!/bin/sh\nset -e\n":                  true,
		"":                                     true,
		"#!/bin/bash\nset -e\n":                false,
		"#!/bin/sh\nset -e\nupdate-rc.d x\n":   false,
		"#!/bin/sh\nldconfig || true\n":        false,
		"#!/bin/sh\n# harmless\nrm -rf /tmp\n": false,
	} {
		if got := ldconfigOnly(body); got != ok {
			t.Errorf("%q: %v, want %v", body, got, ok)
		}
	}
}

// A MALFORMED INDEX HASH REFUSES THE RESOLVE, BY NAME: everything the wrapper does with a hash —
// naming a fetched file by it, holding the fetch to it — holds only for 64 hex digits, so an
// entry carrying anything else is refused where it is read, never trusted and never sliced.
func TestAMalformedIndexHashRefusesTheResolve(t *testing.T) {
	var calls []string
	a := &Apt{
		State: t.TempDir(),
		Repos: []string{"deb http://deb.example/debian bookworm main"},
		Exec: fakeApt(t, &calls, map[string]string{
			"apt-get update": "",
			"apt-get install -s --no-install-recommends jq":              simJq,
			"apt-get install -y --no-install-recommends --print-uris jq": urisJq,
			"apt-cache show libonig5=6.9.8-1":                            "Package: libonig5\nVersion: 6.9.8-1\nFilename: pool/main/o/onig/libonig5_6.9.8-1_amd64.deb\nSHA256: abc123\n\n",
		}),
	}
	_, err := a.Resolve(t.Context(), "jq", "")
	if err == nil || !strings.Contains(err.Error(), "is no sha256") || !strings.Contains(err.Error(), "libonig5") {
		t.Fatalf("a malformed hash: %v", err)
	}
}

// THE INDEX REFRESH IS SINGLE-FLIGHTED: two resolves finding the lists stale at once would both
// run `apt-get update` into one state directory, and the loser would fail on apt's lists lock —
// so one updates while the other waits and reads the fresh clock.
func TestConcurrentResolvesUpdateTheIndexOnce(t *testing.T) {
	outs := map[string]string{
		"apt-get install -s --no-install-recommends jq":              simJq,
		"apt-get install -y --no-install-recommends --print-uris jq": urisJq,
		"apt-cache show libonig5=6.9.8-1":                            show("libonig5", "6.9.8-1", "o/onig"),
		"apt-cache show libjq1=1.6-2.1+deb12u2":                      show("libjq1", "1.6-2.1+deb12u2", "j/jq"),
		"apt-cache show jq=1.6-2.1+deb12u2":                          show("jq", "1.6-2.1+deb12u2", "j/jq"),
	}
	var mu sync.Mutex
	updates := 0
	exec := func(_ context.Context, name string, args ...string) ([]byte, error) {
		key := name + " " + strings.Join(stripOpts(args), " ")
		if key == "apt-get update" {
			mu.Lock()
			updates++
			mu.Unlock()
			time.Sleep(20 * time.Millisecond) // long enough for the second resolve to find it stale too
			return nil, nil
		}
		out, ok := outs[key]
		if !ok {
			return nil, fmt.Errorf("no script for %q", key)
		}
		return []byte(out), nil
	}
	a := &Apt{State: t.TempDir(), Repos: []string{"deb http://deb.example/debian bookworm main"}, Exec: exec}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Resolve(t.Context(), "jq", ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if updates != 1 {
		t.Fatalf("updates %d, want one for both resolves", updates)
	}
}

// AN EPOCH, AN ARCH QUALIFIER, AND A PLAN THAT MOVES LIBC: the plan's url is joined to the
// closure by package and version as apt spells them — an epoch'd pool file percent-encodes the
// `:` that the index's Filename omits, so a filename join would miss — an arch-qualified Inst
// line resolves by its bare name, and a plan that upgrades libc is refused naming it, since the
// prefix runs on the image's loader and libc, which move together. An upgrade of anything else
// bundles as a dependency does: the image is never touched.
func TestResolveReadsEpochsArchesAndRefusesALibcMove(t *testing.T) {
	var calls []string
	a := &Apt{
		State: t.TempDir(),
		Repos: []string{"deb http://deb.example/debian bookworm main"},
		Exec: fakeApt(t, &calls, map[string]string{
			"apt-get update": "",
			"apt-get install -s --no-install-recommends git-man":              "Inst git-man:amd64 (1:2.39.5-0+deb12u2 Debian:12.5/stable [all])\n",
			"apt-get install -y --no-install-recommends --print-uris git-man": "'http://deb.example/debian/pool/main/g/git/git-man_1%3a2.39.5-0+deb12u2_all.deb' git-man_1%3a2.39.5-0+deb12u2_all.deb 1 MD5Sum:aa\n",
			"apt-cache show git-man:amd64=1:2.39.5-0+deb12u2":                 show("git-man", "1:2.39.5-0+deb12u2", "g/git"),
		}),
	}
	r, err := a.Resolve(t.Context(), "git-man", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "1:2.39.5-0+deb12u2" || len(r.Closure) != 1 ||
		r.Closure[0].Name != "git-man" ||
		r.Closure[0].URL != "http://deb.example/debian/pool/main/g/git/git-man_1%3a2.39.5-0+deb12u2_all.deb" {
		t.Fatalf("%+v", r)
	}

	moved := &Apt{State: t.TempDir(), Repos: a.Repos, Exec: fakeApt(t, &calls, map[string]string{
		"apt-get update": "",
		"apt-get install -s --no-install-recommends jq": "Inst libc6 [2.36-9+deb12u4] (2.36-9+deb12u9 Debian-Security:12/stable-security [amd64])\n" + simJq,
	})}
	_, err = moved.Resolve(t.Context(), "jq", "")
	if err == nil || !strings.Contains(err.Error(), "needs a newer libc than the image's") || !strings.Contains(err.Error(), "libc6 2.36-9+deb12u4 to 2.36-9+deb12u9") {
		t.Fatalf("a libc move: %v", err)
	}

	// A libc the image does not provide at all — a foreign arch's — is no upgrade line, and is
	// refused the same.
	fresh := &Apt{State: t.TempDir(), Repos: a.Repos, Exec: fakeApt(t, &calls, map[string]string{
		"apt-get update": "",
		"apt-get install -s --no-install-recommends wine32": "Inst libc6:i386 (2.36-9+deb12u9 Debian:12.5/stable [i386])\nInst wine32:i386 (8.0-1 Debian:12.5/stable [i386])\n",
	})}
	_, err = fresh.Resolve(t.Context(), "wine32", "")
	if err == nil || !strings.Contains(err.Error(), "no libc is ever bundled") {
		t.Fatalf("a fresh libc: %v", err)
	}

	// One version from two architectures cannot share the flat lib/: refused at the plan.
	twoArch := &Apt{State: t.TempDir(), Repos: a.Repos, Exec: fakeApt(t, &calls, map[string]string{
		"apt-get update": "",
		"apt-get install -s --no-install-recommends libonig5": "Inst libonig5 (6.9.8-1 Debian:12.5/stable [amd64])\nInst libonig5:i386 (6.9.8-1 Debian:12.5/stable [i386])\n",
		"apt-get install -y --no-install-recommends --print-uris libonig5": "'http://deb.example/debian/pool/main/o/onig/libonig5_6.9.8-1_amd64.deb' libonig5_6.9.8-1_amd64.deb 1 MD5Sum:aa\n" +
			"'http://deb.example/debian/pool/main/o/onig/libonig5_6.9.8-1_i386.deb' libonig5_6.9.8-1_i386.deb 1 MD5Sum:bb\n",
	})}
	_, err = twoArch.Resolve(t.Context(), "libonig5", "")
	if err == nil || !strings.Contains(err.Error(), "for two architectures") {
		t.Fatalf("two arches of one version: %v", err)
	}
}

// A PACKAGE WHOSE PROGRAMS LIVE IN A TREE OF THEIR OWN IS REFUSED, NOT INSTALLED BROKEN: the
// prefix lands commands (sbin's too), share and shared objects, and a usr/lib tree past the
// libraries — git-core, a python stdlib — would be dropped, so the build refuses it naming the
// first path. A binary the assembler cannot read fails the build too, rather than landing an
// immutable prefix with no runpath.
func TestAPrivateTreeRefusesTheBuildAndSbinLands(t *testing.T) {
	_, pkg := debs(t, map[string][]byte{"git": []byte("git deb"), "tool": []byte("tool deb"), "dim": []byte("dim deb"), "gone": []byte("gone deb")})
	lay := func(files map[string]struct {
		body []byte
		perm os.FileMode
	}) Exec {
		return func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "dpkg-deb":
				if args[0] == "-e" {
					return nil, nil
				}
				dir := args[2]
				for rel, f := range files {
					path := filepath.Join(dir, rel)
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						return nil, err
					}
					if err := os.WriteFile(path, f.body, f.perm); err != nil {
						return nil, err
					}
				}
				return nil, nil
			case "patchelf":
				return nil, nil
			}
			return nil, fmt.Errorf("unexpected %s", name)
		}
	}
	type file = struct {
		body []byte
		perm os.FileMode
	}
	git := &Apt{State: t.TempDir(), Exec: lay(map[string]file{
		"usr/bin/git":              {elfFile("git"), 0o755},
		"usr/lib/git-core/git-add": {elfFile("git-add"), 0o755},
	})}
	err := git.Build(t.Context(), Resolution{Name: "git", Version: "2.39", Closure: []Pkg{pkg("git", "2.39")}})(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "usr/lib/git-core/git-add") || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a private tree: %v", err)
	}

	// An sbin command lands in bin, beside the libraries.
	tool := &Apt{State: t.TempDir(), Exec: lay(map[string]file{
		"usr/sbin/toolctl":                        {elfFile("toolctl"), 0o755},
		"usr/lib/x86_64-linux-gnu/libtool.so.1.0": {elfFile("libtool"), 0o644},
	})}
	stage := t.TempDir()
	if err := tool.Build(t.Context(), Resolution{Name: "tool", Version: "1", Closure: []Pkg{pkg("tool", "1")}})(stage); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stage, "bin/toolctl")); err != nil {
		t.Fatal("the sbin command did not land in bin")
	}

	// A command the assembler cannot read fails the build, loudly, rather than being folded
	// into "not an ELF" and landing with no runpath: the stage is seeded with an unreadable
	// binary (the copy skips a name already landed), and the runpath pass must surface it.
	if os.Geteuid() == 0 {
		t.Skip("root reads everything, and the unreadable fixture reads")
	}
	dim := &Apt{State: t.TempDir(), Exec: lay(map[string]file{"usr/bin/dim": {elfFile("dim"), 0o755}})}
	stage = t.TempDir()
	if err := os.MkdirAll(filepath.Join(stage, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "bin/dim"), elfFile("dim"), 0o311); err != nil {
		t.Fatal(err)
	}
	err = dim.Build(t.Context(), Resolution{Name: "dim", Version: "1", Closure: []Pkg{pkg("dim", "1")}})(stage)
	if err == nil || !strings.Contains(err.Error(), "for its runpath") {
		t.Fatalf("an unreadable binary: %v", err)
	}

	// A dangling packaging symlink in bin fails the build naming the command, rather than
	// being skipped: skipped, the immutable prefix would be cached missing it.
	gone := &Apt{State: t.TempDir(), Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "dpkg-deb":
			if args[0] == "-e" {
				return nil, nil
			}
			dir := filepath.Join(args[2], "usr/bin")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
			return nil, os.Symlink("../lib/elsewhere/gone", filepath.Join(dir, "gone"))
		case "patchelf":
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected %s", name)
	}}
	err = gone.Build(t.Context(), Resolution{Name: "gone", Version: "1", Closure: []Pkg{pkg("gone", "1")}})(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "to land it in bin") {
		t.Fatalf("a dangling bin symlink: %v", err)
	}
}
