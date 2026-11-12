package systems

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// install is a pkg.install step's fields as the runner fills them: the package, and the version
// and the command's name where the step carries them — an absent field is absent, not empty. A
// shape that names a version does not match a step without one; an absent `as` is read as its
// default, no rename.
func install(t *testing.T, pkg, version, as string) *ir.Node {
	t.Helper()
	fields := map[string]string{"pkg": pkg}
	if version != "" {
		fields["version"] = version
	}
	if as != "" {
		fields["as"] = as
	}
	return FieldsDoc("pkg.install", fields)
}

// AN INSTALL IS ADMITTED BY A SHAPE ON ITS PACKAGE, ITS VERSION AND ITS NAME: a pattern on the
// package admits what it matches, a shape that pins the version refuses a step that names another
// or none, a shape that says nothing of the version admits any, and a shape that says nothing of
// `as` admits no rename.
func TestAnInstallIsMatchedByPackageVersionAndName(t *testing.T) {
	if f, _ := FieldOf("pkg.install", "pkg"); f.Form != FormLower || !f.Required {
		t.Fatalf("pkg: %+v", f)
	}
	if f, _ := FieldOf("pkg.install", "version"); f.Required || f.Default != "" {
		t.Fatalf("version: %+v", f)
	}
	if f, _ := FieldOf("pkg.install", "as"); f.Required || f.Default != `""` {
		t.Fatalf("as: %+v", f)
	}
	e, err := readEnv(t, `systems: {pkg: {install: [{pkg: !glob "lib*"}, {pkg: jq, version: "1.6-2.1+deb12u2", as: !glob "jq-*"}, {pkg: ripgrep}, {pkg: fd-find, as: null}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Check(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		pkg, version, as string
		allowed          bool
	}{
		{"ripgrep", "", "", true},
		{"ripgrep", "14.1.0-1", "", true},        // a shape that says nothing of the version admits any
		{"ripgrep", "14.1.0-1", "rg-v14", false}, // and one that says nothing of `as` renames nothing
		{"libonig5", "", "", true},
		{"libonig5", "", "make", false},
		{"fd-find", "", "fd", true},          // `as: null` admits any name
		{"fd-find", "", "", true},            // and none
		{"jq", "1.6-2.1+deb12u2", "", false}, // a shape that names `as` with a pattern is its renames alone
		{"jq", "1.6-2.1+deb12u2", "jq-v1", true},
		{"jq", "1.6-2.1+deb12u2", "npm", false}, // outside the shape's pattern on `as`
		{"jq", "1.7.1-1", "", false},            // the shape pins the version
		{"jq", "", "", false},                   // and a step that names none is outside it
		{"git", "", "", false},
	} {
		if got := e.Allows("pkg.install", install(t, c.pkg, c.version, c.as)); got != c.allowed {
			why, _ := e.Refuses("pkg.install", install(t, c.pkg, c.version, c.as))
			t.Errorf("%s %s %s: allowed %v, want %v (%s)", c.pkg, c.version, c.as, got, c.allowed, why)
		}
	}
}

// A CEILING ON INSTALLS CONTAINS WHAT ITS SHAPES ADMIT: an environment within it pins no less
// than the ceiling pins, and one that reaches a package or a version beyond it is named.
func TestACeilingBoundsWhatAnEnvironmentInstalls(t *testing.T) {
	c := readCeiling(t, `{install: [{pkg: !glob "lib*"}, {pkg: jq, version: !glob "1.6*"}]}`)
	if err := c.Check("pkg"); err != nil {
		t.Fatal(err)
	}
	for text, want := range map[string]string{
		`{install: {pkg: libonig5}}`:                         "",
		`{install: [{pkg: !glob "libx*"}, {pkg: libonig5}]}`: "",
		`{install: {pkg: jq, version: "1.6-2.1+deb12u2"}}`:   "",
		`{install: {pkg: jq}}`:                               "within no shape", // the ceiling pins the version
		`{install: {pkg: jq, version: "1.7.1-1"}}`:           "within no shape",
		`{install: {pkg: git}}`:                              "within no shape",
		`{install: {pkg: !glob "*"}}`:                        "within no shape",
	} {
		e, err := readEnv(t, `systems: {pkg: `+text+`}`)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Check(); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		err = c.Contains("pkg", e.Systems["pkg"][0])
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", text, err, want)
		}
	}
}
