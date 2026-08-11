package main

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// The pin verse resolves `o` at comes from its OWN build info, so it travels with an
// installed binary — there is no go.mod where an operator starts a verse. Nothing else
// notices if that stops being readable: resolveO would quietly go back to "whatever `o` is
// on PATH", which is the skew the version check exists to catch.
func TestPinnedGoTonyIsReadable(t *testing.T) {
	if goTonyIsReplaced() {
		t.Skip("go-tony is replaced in this build: no pin is the right answer, not a lost one")
	}
	v := pinnedGoTony()
	if !strings.HasPrefix(v, "v") {
		t.Fatalf("pinnedGoTony() = %q, want the go-tony version this binary was built against", v)
	}
	// The same read, through the path resolveO uses on a binary it did not build. This
	// test binary is built from this module, so the two must agree — and if buildinfo
	// cannot be read from a file at all, the PATH check silently passes everything.
	self, err := os.Executable()
	if err != nil {
		t.Skipf("no executable path: %v", err)
	}
	if got := goTonyOf(self); got != v {
		t.Errorf("goTonyOf(self) = %q, pinnedGoTony() = %q — want the same version", got, v)
	}
}

// goTonyIsReplaced reports whether this binary was built against a `replace`d go-tony — a
// worktree under development, say. That is the one case where pinnedGoTony returning "" is
// the deliberate answer (a filesystem replacement has no version to `go install`) rather
// than the silent failure the test above exists to catch.
func goTonyIsReplaced() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, d := range bi.Deps {
		if d.Path == goTonyModule {
			return d.Replace != nil
		}
	}
	return false
}

// The cache is keyed by version so a verse built against a newer go-tony installs BESIDE
// the old one rather than over it — two verses of different vintages can be running.
func TestCachedOIsVersioned(t *testing.T) {
	a, b := cachedO("v0.0.108"), cachedO("v0.0.109")
	if a == b {
		t.Fatalf("cachedO is not version-keyed: both are %q", a)
	}
	if filepath.Base(a) != "o" {
		t.Errorf("cachedO(...) = %q, want the binary to still be called o", a)
	}
	if !strings.Contains(a, filepath.Join(".verse", "bin", "v0.0.108")) {
		t.Errorf("cachedO(v0.0.108) = %q, want it under ~/.verse/bin/v0.0.108", a)
	}
	// No pin, no cache path: there is nothing that is knowably the right thing to install.
	if got := cachedO(""); got != "" {
		t.Errorf("cachedO(\"\") = %q, want no cache path", got)
	}
}

// The warning is the LAST resort — resolveO prefers a matching o and installs the pinned
// one — so it must be silent when the versions agree, and must name both when they don't:
// a skew shows up as a store that will not talk or a flag `o sys up` does not know, and
// neither of those says "your o is old".
func TestSkewWarning(t *testing.T) {
	if got := skewWarning("/usr/bin/o", "v0.0.108", "v0.0.108"); got != "" {
		t.Errorf("matching versions warn: %q", got)
	}
	if got := skewWarning("/usr/bin/o", "", ""); got != "" {
		t.Errorf("no pin to compare against warns: %q", got)
	}
	for name, found := range map[string]string{"older": "v0.0.99", "local build": "(devel)"} {
		got := skewWarning("/usr/bin/o", found, "v0.0.108")
		if !strings.Contains(got, found) || !strings.Contains(got, "v0.0.108") {
			t.Errorf("%s: warning %q must name both %q and the pin", name, got, found)
		}
	}
	if got := skewWarning("/usr/bin/o", "", "v0.0.108"); !strings.Contains(got, "v0.0.108") {
		t.Errorf("unreadable version: warning %q must still name the pin", got)
	}
}
