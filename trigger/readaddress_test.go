package trigger

import (
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// A READ ADDRESS SPLITS WHERE THE COLONS SAY, not where ParseRef happens to succeed.
//
// `Ref.String` only ever puts a colon in the last segment, so a dotted string whose FIRST
// segment holds one is `system:kind:<id with dots>` and anything else with a dot is a path.
// ParseRef was tried first, and SplitN found three parts INSIDE a quoted segment — so an
// address of the shape every derived entity has was read as an entity nobody named
// (git-issue p5qcmpgch12ksd4wgnn0).
func TestAReadAddressSplitsOnTheColonRule(t *testing.T) {
	for _, tc := range []struct {
		addr string
		ref  string
		path string
	}{
		{"git.repo.acme.branch", "git.repo.acme", "branch"},
		{"git.repo.acme", "git.repo.acme", ""},
		// Four and six segments deep. The entity is still read as the first three, and the rest
		// continues into the payload — which reaches the same node, because a verse is one
		// document and entity-hood is a lens over it. The half that matters is that the
		// SEGMENTS are separated correctly.
		{"verse-dev.drift.head.abc", "verse-dev.drift.head", "abc"},
		{"verse-dev.drift.head.abc.result.report", "verse-dev.drift.head", "abc.result.report"},
		// A quoted segment stays ONE segment. The rendered path quotes only where the kpath
		// grammar needs it — a colon does not need one, a dot does — and re-parses to the same
		// segments either way, which is what the read that follows depends on.
		{
			`verse-dev.drift.head."head-moved@git.head.verse@aaaa1111".result.report`,
			"verse-dev.drift.head",
			`"head-moved@git.head.verse@aaaa1111".result.report`,
		},
		{
			`verse-dev.drift.head."v1.2.3".result.report`,
			"verse-dev.drift.head",
			`"v1.2.3".result.report`,
		},
		// The QUOTING says where a segment ends, and it is the only thing that does. A numeric
		// id quoted is the id; the same three segments followed by a bare `stage` continue into
		// the payload.
		{`demo.pr."1"`, `demo.pr."1"`, ""},
		{`demo.pr."1".stage`, `demo.pr."1"`, "stage"},
		{"demo.pr.1.stage", `demo.pr."1"`, "stage"},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			ref, path, err := splitAddress(tc.addr)
			if err != nil {
				t.Fatalf("%s: %v", tc.addr, err)
			}
			if ref.String() != tc.ref || path != tc.path {
				t.Fatalf("%s\n got ref=%s path=%q\nwant ref=%s path=%q",
					tc.addr, ref, path, tc.ref, tc.path)
			}
		})
	}
}

// And the read answers, which is the half a split cannot be checked without: both spellings of
// the same value reach it. The quoted one used to fail as "nothing stands there".
func TestADeepReadAddressAnswers(t *testing.T) {
	store := entitytest.Store(t)
	setPayload(t, store, entity.RefAt("verse-dev", "drift", "head", "abc"), `{result: {report: hello}}`)
	deep := "head-moved@git.head.verse@aaaa1111"
	setPayload(t, store, entity.RefAt("verse-dev", "drift", "head", deep), `{result: {report: deep}}`)
	snap := entity.Reader(store).(entity.Snapshot)

	setPayload(t, store, entity.RefAt("verse-dev", "drift", "head", "v1.2.3"), `{result: {report: dotted}}`)

	for _, tc := range []struct{ addr, want string }{
		{"verse-dev.drift.head.abc.result.report", "hello"},
		{`verse-dev.drift.head."head-moved@git.head.verse@aaaa1111".result.report`, "deep"},
		{`verse-dev.drift.head."v1.2.3".result.report`, "dotted"},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			ref, path, err := splitAddress(tc.addr)
			if err != nil {
				t.Fatalf("split: %v", err)
			}
			spec := pathSpec{
				kind: GetPathTag, System: ref.System(), Kind: ref.Kind(), ID: ref.ID(), Path: path,
			}
			v, err := spec.get(snap)
			if err != nil {
				t.Fatalf("%s: %v", tc.addr, err)
			}
			got, err := codec.Marshal(v, codec.Tony)
			if err != nil {
				t.Fatalf("emit: %v", err)
			}
			if string(got) != tc.want+"\n" {
				t.Fatalf("%s answered %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}
