package source_test

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"

	"github.com/signadot/verse/source"
)

// The case the whole rule exists for: two git repos, on two hosts, in one verse. If this needed
// --allow-overlap then the flag would be the ordinary path and would mark nothing — which is
// why "distinct system or distinct kind" was not enough on its own.
func TestTwoReposAreProvablyDisjoint(t *testing.T) {
	verse := owns("git.*.verse/*", "git.head.verse")
	kernel := owns("git.*.kernel/*", "git.head.kernel")

	if ok, err := source.Disjoint(verse, kernel); err != nil || !ok {
		t.Fatalf("two repos should be provably disjoint: ok=%v err=%v", ok, err)
	}

	// The near miss, and the reason a repo claims two globs rather than the one that looks
	// tidier. `verse*` would claim `verse-extras/main`, and these two would refuse to install
	// alongside each other for an overlap that is not real.
	extras := owns("git.*.verse-extras/*", "git.head.verse-extras")
	if ok, err := source.Disjoint(verse, extras); err != nil || !ok {
		t.Fatalf("verse and verse-extras should be disjoint: ok=%v err=%v", ok, err)
	}
	if ok, _ := source.Disjoint(owns("git.*.verse*"), extras); ok {
		t.Fatal("`verse*` really does claim verse-extras/*; it must not read as disjoint")
	}
}

// The proof rule itself, pair by pair. A claim is disjoint when ANY segment is, and a `*`
// segment is disjoint from nothing.
func TestClaimDisjoint(t *testing.T) {
	for _, tc := range []struct {
		name, a, b string
		want       bool
	}{
		{"different systems", "git.ref.x", "linear.issue.x", true},
		{"different literal kinds", "git.ref.x", "git.head.x", true},
		{"same everything", "git.ref.x", "git.ref.x", false},
		{"a wildcard kind overlaps a literal one", "git.*.x", "git.head.x", false},
		{"a wildcard system overlaps a literal one", "*.ref.x", "git.ref.x", false},
		{"different literal ids", "git.ref.a", "git.ref.b", true},
		{"disjoint prefixes", "git.ref.a/*", "git.ref.b/*", true},
		{"a prefix that extends another is not disjoint", "git.ref.a/*", "git.ref.a/b/*", false},
		{"a literal under a prefix is not disjoint", "git.ref.a/b", "git.ref.a/*", false},
		{"a literal outside a prefix is disjoint", "git.ref.b", "git.ref.a/*", true},
		// The claim that swallows its system. It is the one the note calls out: it claims the
		// lot, so nothing in git can be proved apart from it.
		{"everything in a system", "git.*.*", "git.ref.anything", false},
		{"everything, against another system", "git.*.*", "linear.issue.x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := source.ParseClaim(tc.a)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.a, err)
			}
			b, err := source.ParseClaim(tc.b)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.b, err)
			}
			if got := a.Disjoint(b); got != tc.want {
				t.Fatalf("%s disjoint %s = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			// Symmetry, over the whole table: an install checks one way and means both, so an
			// asymmetric answer would make the refusal depend on installation order.
			if got := b.Disjoint(a); got != tc.want {
				t.Fatalf("%s disjoint %s = %v, want %v (asymmetric)", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// A claim addresses a PLACE, so two sources may share an entity by owning two slots of it —
// which is two single writers rather than two writers. The table is the one in
// docs/working/shared-ownership.md.
func TestASlotIsAClaimAtADeeperAddress(t *testing.T) {
	// `linear.project.*` — every project — at the given path. Prefix with an empty ID is `*`.
	project := func(field ...string) source.Claim {
		return mustClaim(t, "linear.project.*", field...)
	}
	for _, tc := range []struct {
		name string
		a, b source.Claim
		want bool
	}{
		{"two teams' slots", project("owners", "ENG"), project("owners", "OPS"), true},
		// Segment-wise, not string-wise. A string prefix test would say the first contains the
		// second and refuse two sources that share nothing.
		{"a slot whose name prefixes another's", project("owners", "ENG"), project("owners", "ENGINE"), true},
		{"the container holds the slot", project("owners"), project("owners", "ENG"), false},
		{"the whole entity holds every slot", project(), project("owners", "ENG"), false},
		{"the same slot twice", project("owners", "ENG"), project("owners", "ENG"), false},
		{"a slot under a deeper one", project("owners", "ENG"), project("owners", "ENG", "seen"), false},
		// The path is the LAST thing asked, so a difference higher up still decides it: two
		// kinds that differ are disjoint whatever their paths would have said.
		{"a different kind, same path", project("owners", "ENG"),
			mustClaim(t, "linear.cycle.*", "owners", "ENG"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Disjoint(tc.b); got != tc.want {
				t.Fatalf("%s disjoint %s = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			// Symmetric, for the reason the ref-level table is: an install checks one way and
			// means both, so an asymmetric answer makes a refusal depend on installation order.
			if got := tc.b.Disjoint(tc.a); got != tc.want {
				t.Fatalf("%s disjoint %s = %v, want %v (asymmetric)", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// The negative that matters most: nothing in the tree has a path, so nothing in the tree
// compares differently than it did. Every claim the three connectors emit parses to an empty
// Field, and an empty Field is what the whole existing table above is about.
func TestTheTerseSpellingIsPathLess(t *testing.T) {
	g := source.NewGit()
	g.AddRepo("verse", t.TempDir())
	declared := g.Owns() // git's own, built by the code that ships
	declared = append(declared, owns(
		"slack.*.acme/*",                                              // slack.Owns
		"linear.team.ENG", "linear.issue.ENG-*", "linear.cycle.ENG/*", // linear.Owns
	)...)
	for _, o := range declared {
		s := o.At
		c, err := o.Claim()
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		if len(c.Field) != 0 {
			t.Fatalf("%q parsed to a claim with a path %v — the terse spelling has none", s, c.Field)
		}
		if c.String() != s {
			t.Fatalf("%q round-tripped to %q", s, c.String())
		}
	}
	// A claim WITH a path has no terse spelling and does not pretend to: it renders for a
	// person reading a refusal, and nothing parses it back.
	slot := mustClaim(t, "linear.project.*", "owners", "ENG")
	if got, want := slot.String(), "linear.project.* at owners.ENG"; got != want {
		t.Fatalf("a slot claim renders as %q, want %q", got, want)
	}
}

// Ownership is a union, so one overlapping pair is an overlap however disjoint the rest are.
func TestDisjointIsEveryPair(t *testing.T) {
	a := owns("git.*.verse/*", "git.head.verse")
	b := owns("git.*.kernel/*", "git.head.verse") // the second entry collides
	if ok, err := source.Disjoint(a, b); err != nil || ok {
		t.Fatalf("one overlapping pair is an overlap: ok=%v err=%v", ok, err)
	}
	// And a registration claiming nothing is disjoint from everything, which is the honest
	// reading of an empty list rather than a special case.
	if ok, err := source.Disjoint(nil, a); err != nil || !ok {
		t.Fatalf("an empty claim should be disjoint: ok=%v err=%v", ok, err)
	}
}

// A malformed claim is a parse ERROR, never a quiet approximation. A claim meaning something
// narrower than it looks is a single-writer guarantee resting on a typo.
func TestParseClaimRefusals(t *testing.T) {
	for _, s := range []string{
		"",                   // no levels at all
		"git.ref.",           // an empty level
		"gi*t.ref.x",         // a `*` that is not trailing
		"git.ref.a*b",        // ditto, in the middle of a name
		"git.ref.*/main",     // ditto, at the front
		"git.ref.a/*/b",      // ditto, inside an id
		"verse.trigger.rule", // verse's own half is nobody's to claim
	} {
		if c, err := source.ParseClaim(s); err == nil {
			t.Fatalf("%q should be refused, parsed as %s", s, c)
		}
	}
	// And what a well-formed one round-trips to, since the refusal messages quote it back. The
	// last three are what the three fixed dimensions could not say at all: a claim shorter than
	// an entity, one deeper, and a glob above the last level.
	for _, s := range []string{
		"git.*.verse/*", "git.head.verse", "*.*.*",
		"git", "verse-dev.drift.head.abc", "git.re*.x",
	} {
		c, err := source.ParseClaim(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		if c.String() != s {
			t.Fatalf("%q round-tripped to %q", s, c.String())
		}
	}
	// A colon is an ordinary character in a level, since nothing in the address grammar reads
	// one — and the trailing `*` is the pattern's rather than the name's.
	c, err := source.ParseClaim("git.ref.a:b/*")
	if err != nil || len(c.Segs) != 3 || c.Segs[2] != (source.Seg{Name: "a:b/", Prefix: true}) {
		t.Fatalf("a level may contain colons: %+v %v", c, err)
	}
}

// A CLAIM IS OVER A TREE, which is what the three fixed dimensions could not be: a source that
// keeps two levels of its own — the shape a package already has — says so by claiming the place
// they live under, and is proved apart from its neighbours by the same walk as everything else.
//
// The pairs below are the ones the old shape got wrong or could not state. A SHORTER claim is
// not a weaker one: it contains everything under it, so it OVERLAPS rather than being disjoint.
func TestAClaimIsOverATree(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b string
		want bool // disjoint?
	}{
		{"a subtree holds what is under it", "git", "git.ref.x", false},
		{"…however deep", "verse-dev.drift", "verse-dev.drift.head.abc", false},
		{"two subtrees that part at the first level", "git", "github", true},
		{"…and at a later one", "verse-dev.drift", "verse-dev.merge", true},
		{"a glob above the last level", "git.re*.x", "git.ref.x", false},
		{"…that does not reach", "git.re*.x", "git.head.x", true},
		{"a wildcard level reaches everything at it", "git.*.x", "git.anything.x", false},
		{"two depths under one system, disjoint below", "verse-dev.drift.head", "verse-dev.drift.finding", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := source.ParseClaim(tc.a)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.a, err)
			}
			b, err := source.ParseClaim(tc.b)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.b, err)
			}
			if got := a.Disjoint(b); got != tc.want {
				t.Errorf("%s disjoint %s = %v, want %v", a, b, got, tc.want)
			}
			// Symmetric: an install checks one way and means both, so an asymmetric answer
			// would make a refusal depend on installation order.
			if got := b.Disjoint(a); got != tc.want {
				t.Errorf("%s disjoint %s = %v, want %v (asymmetric)", b, a, got, tc.want)
			}
		})
	}
}

// A subtree claim covers the addresses under it, which is what a removal reads to decide what
// it may tombstone. Claimed is the point-wise face of Disjoint, and this is the depth question
// asked of it directly.
func TestASubtreeClaimCoversWhatIsUnderIt(t *testing.T) {
	claims, err := source.ParseOwns([]source.Own{{At: "verse-dev.drift"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []entity.Ref{
		entity.RefAt("verse-dev", "drift", "head", "abc"),
		entity.RefAt("verse-dev", "drift", "finding", "1"),
		entity.RefAt("verse-dev", "drift"),
	} {
		if places := source.Claimed(claims, ref); len(places) != 1 {
			t.Errorf("%s is under the claim; Claimed = %v", ref, places)
		}
	}
	for _, ref := range []entity.Ref{
		entity.RefAt("verse-dev", "merge", "train", "x"),
		entity.RefAt("git", "ref", "main"),
	} {
		if places := source.Claimed(claims, ref); len(places) != 0 {
			t.Errorf("%s is outside the claim; Claimed = %v", ref, places)
		}
	}
}

// The claim a source declares has to be the claim its reflection actually writes under, or the
// install proves something about a slice nobody touches. This is the one place the two halves
// meet, so it is asserted rather than assumed.
func TestGitOwnsCoversWhatItReflects(t *testing.T) {
	g := source.NewGit()
	g.AddRepo("A", t.TempDir())
	claims, err := source.ParseOwns(g.Owns())
	if err != nil {
		t.Fatalf("git declares a claim that does not parse: %v", err)
	}
	for _, ref := range []string{`git.ref."A/main"`, "git.head.A", `git.commit."A/abc123"`} {
		// The place claimed is the ENTITY, since git claims no path — which is what makes a
		// --forget on it exactly the removal it has always been.
		places := source.Claimed(claims, mustRef(t, ref))
		if len(places) != 1 || !places[0].IsEntity() {
			t.Fatalf("%s is reflected; Claimed = %v, want the whole entity", ref, places)
		}
	}
	// And it does not claim a neighbour's — which is what bounds a --forget removal to the
	// slice this registration was proved to own.
	for _, ref := range []string{"git.ref.B/main", "git.head.B"} {
		if places := source.Claimed(claims, mustRef(t, ref)); len(places) != 0 {
			t.Fatalf("%s belongs to another repo but is claimed at %v", ref, places)
		}
	}
}

// owns spells a declaration list from its terse refs, which is what every claim in the tree is.
func owns(refs ...string) []source.Own {
	out := make([]source.Own, 0, len(refs))
	for _, r := range refs {
		out = append(out, source.Own{At: r})
	}
	return out
}

func mustRef(t *testing.T, s string) entity.Ref {
	t.Helper()
	ref, err := entity.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return ref
}

// Every connector's system is the operator's to choose, and everything it writes, covers, claims
// and hints follows — which is what makes two deployments that want no coordination able to have
// it: two systems are disjoint on the first segment, so neither owes the other a proof.
//
// Checked here, across the connectors, because the property is one sentence per source and the
// way it breaks is one of them forgetting.
func TestASourcesSystemIsTheOperatorsToChoose(t *testing.T) {
	g := source.NewGit()
	if err := g.SetSystem("mirror"); err != nil {
		t.Fatal(err)
	}
	g.AddRepo("verse", t.TempDir())
	if g.System() != "mirror" {
		t.Fatalf("git System() = %q", g.System())
	}
	for _, o := range g.Owns() {
		if !strings.HasPrefix(o.At, "mirror.") {
			t.Fatalf("a git claim outside the chosen system: %s", o.At)
		}
	}
	// The hook a git source installs hints in its own system, or the hint reaches nothing.
	if got := g.HeadRef("verse").String(); got != "mirror.head.verse" {
		t.Fatalf("git HeadRef = %q", got)
	}
	// And two systems are provably disjoint with nothing else said.
	plain := source.NewGit()
	plain.AddRepo("verse", t.TempDir())
	ok, err := source.Disjoint(g.Owns(), plain.Owns())
	if err != nil || !ok {
		t.Fatalf("two systems over the same repo were not disjoint: ok=%v err=%v", ok, err)
	}
	// The punctuation the address is built from is refused, as it is at install.
	for _, bad := range []string{"a:b", "a/b"} {
		if err := source.NewGit().SetSystem(bad); err == nil {
			t.Fatalf("system %q should be refused", bad)
		}
	}
}

// A claim carries three segments and no root, so `task.*.*` — and the wildcard `*:*:*`, which
// a source may register when nothing else is installed — asks about `verse.task.instance.x` as
// readily as about the world's `task.instance.x`. It must own neither: the caller that deletes
// (`verse source remove --forget`) writes straight to the store, below the refusal that guards
// the HTTP face, so this was the one client-reachable path to a tombstone over verse's charter.
func TestNoClaimReachesVersesOwnHalf(t *testing.T) {
	for _, spell := range []string{"*.*.*", "task.*.*", "trigger.rule.*", "sources.git.verse"} {
		claims, err := source.ParseOwns([]source.Own{{At: spell}})
		if err != nil {
			t.Fatalf("%s: %v", spell, err)
		}
		for _, ref := range []entity.Ref{
			entity.VerseRef("task", "instance", "run-1"),
			entity.VerseRef("trigger", "rule", "review"),
			entity.VerseRef("sources", "git", "verse"),
		} {
			if got := source.Claimed(claims, ref); len(got) > 0 {
				t.Errorf("owns %q claims %s → %v, want nothing in verse's half", spell, ref, got)
			}
		}
		// And the world's half is unchanged: the same claim still owns what it is for.
		if spell == "task.*.*" {
			if got := source.Claimed(claims, entity.RefAt("task", "instance", "run-1")); len(got) != 1 {
				t.Errorf("owns %q lost its own slice: %v", spell, got)
			}
		}
	}
}

// mustClaim parses a claim and attaches a payload path — the two halves of a Claim, since the
// path has no terse spelling of its own (source/own.go).
func mustClaim(t *testing.T, s string, field ...string) source.Claim {
	t.Helper()
	c, err := source.ParseClaim(s)
	if err != nil {
		t.Fatalf("ParseClaim(%q): %v", s, err)
	}
	c.Field = field
	return c
}
