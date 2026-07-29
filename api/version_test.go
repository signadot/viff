package api

import (
	"strings"
	"testing"
)

// The rule, stated as cases. Semver read plainly: the major is the compatibility promise, the
// minor is features, the patch is nothing worth saying.
//
// The two entries that are NOT semver reading are the ones to look at. v0.1 against v0.2 only
// warns, though semver says a 0.x minor may break anything — see VersionSkew for why the strict
// reading is worse here. And an unparseable version never reaches incompatible, which is what
// keeps a checkout usable while nothing is tagged at all.
func TestVersionSkewRule(t *testing.T) {
	for _, c := range []struct {
		client, server string
		want           Skew
	}{
		{"v1.4.2", "v1.4.2", SameVersion},
		{"(devel)", "(devel)", SameVersion},
		{"", "", SameVersion},
		{"v1.4.2", "v1.4.9", SameVersion},         // patch: a release that changed the wire is not one
		{"v1.4.2", "v1.9.0", VersionDrift},        // minor: features
		{"v1.9.0", "v1.4.2", VersionDrift},        // …either direction
		{"v1.4.2", "v2.0.0", VersionIncompatible}, // major: the whole content of the number
		{"v2.0.0", "v1.4.2", VersionIncompatible},
		{"v0.1.0", "v0.2.0", VersionDrift},  // deliberately NOT incompatible
		{"v0.0.1", "v0.0.2", SameVersion},   // …and a patch bump is silent, which 0.0.x needs
		{"(devel)", "v1.4.2", VersionDrift}, // cannot compare, so never worse than drift
		{"v1.4.2", "", VersionDrift},
		{"v1.4.2", "main", VersionDrift},
		{"v1.4.2-rc1", "v1.4.2", SameVersion}, // a pre-release of the same version
	} {
		got, msg := VersionSkew(c.client, c.server)
		if got != c.want {
			t.Errorf("VersionSkew(%q, %q) = %v, want %v", c.client, c.server, got, c.want)
		}
		if got == SameVersion {
			if msg != "" {
				t.Errorf("VersionSkew(%q, %q) says %q about versions that agree", c.client, c.server, msg)
			}
			continue
		}
		// The line has to name both, because what the operator sees otherwise is a missing
		// field or a 404, and neither says which two things disagreed.
		for _, v := range []string{c.client, c.server} {
			if v == "" {
				continue // named as "an unreadable version"; there is nothing to quote
			}
			if !strings.Contains(msg, v) {
				t.Errorf("VersionSkew(%q, %q) = %q, which does not name %q",
					c.client, c.server, msg, v)
			}
		}
	}
}

// The version travels with the binary or it is worth nothing: a constant in the source says
// what someone typed, not what was built and shipped.
func TestVersionComesFromBuildInfo(t *testing.T) {
	if got := Version(); got != "(devel)" {
		t.Errorf("Version() = %q; a test binary is built from the checkout, so it should be (devel)", got)
	}
}
