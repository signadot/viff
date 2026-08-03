package source

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
)

// The content slice is served by a controller mount, so entity must render its refs under
// entity.MountRoot — otherwise the store would look for them under the entity root, miss
// the mount, and every federated read_file would fail "file not found". entity cannot
// import source (source imports entity), so it names the slice itself; this test is what
// keeps the two in step if either side is renamed.
func TestFederationKindsAreMountOwned(t *testing.T) {
	const scope = "a1b2c3"

	if !entity.IsMountOwned(FederationSystem, ContentPrefix+scope) {
		t.Fatalf("entity does not treat %s:%s%s as mount-owned — add it to entity.mountedKinds; "+
			"refPath would render it under the entity root and miss the content mount",
			FederationSystem, ContentPrefix, scope)
	}

	// The status slice is ordinary log-backed metadata written by connect. It must NOT be
	// mount-owned: it is what the per-file validator trigger matches, so it has to stay
	// under the entity root where it is watchable and has history.
	if entity.IsMountOwned(FederationSystem, StatusPrefix+scope) {
		t.Fatalf("%s:%s%s must stay log-backed (it is watched and written, not mounted)",
			FederationSystem, StatusPrefix, scope)
	}

	// MountKPath must agree, and must land under MountRoot rather than the entity root.
	kp := entity.MountKPath(FederationSystem, ContentPrefix+scope)
	if !strings.HasPrefix(kp, entity.MountRoot+".") {
		t.Fatalf("MountKPath(%s, %s%s) = %q, want it under %q",
			FederationSystem, ContentPrefix, scope, kp, entity.MountRoot)
	}
	if strings.HasPrefix(kp, entity.DefaultRoot+".") {
		t.Fatalf("MountKPath returned a path under the entity root (%q) — a mount below a "+
			"watched root composes the watch and breaks FromCommit replay: %q", entity.DefaultRoot, kp)
	}
}

// The two roots must be disjoint as kpath FIELDS, not merely different strings: docd's
// coordinator and its watch composition both test whether one path is a field-prefix of
// the other. "verse" vs "versemnt" are distinct fields, so neither contains the other.
func TestMountRootIsDisjointFromEntityRoot(t *testing.T) {
	if entity.MountRoot == entity.DefaultRoot {
		t.Fatal("MountRoot must differ from DefaultRoot")
	}
	if entity.MountRoot+"." == entity.DefaultRoot+"." {
		t.Fatal("roots collide")
	}
	// Neither may be a whole-field prefix of the other (i.e. "a" vs "a.b").
	if strings.HasPrefix(entity.MountRoot, entity.DefaultRoot+".") ||
		strings.HasPrefix(entity.DefaultRoot, entity.MountRoot+".") {
		t.Fatalf("%q and %q overlap as kpaths — a watch on one would compose across the other",
			entity.DefaultRoot, entity.MountRoot)
	}
}
