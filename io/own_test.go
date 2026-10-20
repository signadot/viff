package io_test

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"

	verseio "github.com/signadot/verse/io"
)

// The case the whole rule exists for: two git repos, on two hosts, in one verse. If this needed
// --allow-overlap then the flag would be the ordinary path and would mark nothing — which is
// why "distinct system or distinct kind" was not enough on its own.
func TestTwoReposAreProvablyDisjoint(t *testing.T) {
	verse := owns("git.*.verse/*", "git.head.verse")
	kernel := owns("git.*.kernel/*", "git.head.kernel")

	if ok, err := verseio.Disjoint(verse, kernel); err != nil || !ok {
		t.Fatalf("two repos should be provably disjoint: ok=%v err=%v", ok, err)
	}

	// The near miss, and the reason a repo claims two globs rather than the one that looks
	// tidier. `verse*` would claim `verse-extras/main`, and these two would refuse to install
	// alongside each other for an overlap that is not real.
	extras := owns("git.*.verse-extras/*", "git.head.verse-extras")
	if ok, err := verseio.Disjoint(verse, extras); err != nil || !ok {
		t.Fatalf("verse and verse-extras should be disjoint: ok=%v err=%v", ok, err)
	}
	if ok, _ := verseio.Disjoint(owns("git.*.verse*"), extras); ok {
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
			a, err := verseio.ParseClaim(tc.a)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.a, err)
			}
			b, err := verseio.ParseClaim(tc.b)
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

// Two sources may share a THING by owning two nodes inside it, which is two single writers
// rather than two writers of anything. The table is the one in
// the shared-ownership note, now in the history — and every row of it is the same comparison the table above
// makes, because a place inside a shared node is an address like any other.
func TestSharingIsSingleWritershipAtADeeperAddress(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b string
		want bool
	}{
		{"two teams' places", "linear.project.*.owners.ENG", "linear.project.*.owners.OPS", true},
		// Segment-wise, not string-wise. A string prefix test would say the first contains the
		// second and refuse two sources that share nothing.
		{"a place whose name prefixes another's",
			"linear.project.*.owners.ENG", "linear.project.*.owners.ENGINE", true},
		{"the level above holds the place", "linear.project.*.owners", "linear.project.*.owners.ENG", false},
		{"the whole project holds every place", "linear.project.*", "linear.project.*.owners.ENG", false},
		{"the same place twice", "linear.project.*.owners.ENG", "linear.project.*.owners.ENG", false},
		{"a place under a deeper one",
			"linear.project.*.owners.ENG", "linear.project.*.owners.ENG.seen", false},
		// A difference HIGHER UP still decides it, whatever the tails say.
		{"a different kind, the same tail",
			"linear.project.*.owners.ENG", "linear.cycle.*.owners.ENG", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := mustClaim(t, tc.a), mustClaim(t, tc.b)
			if got := a.Disjoint(b); got != tc.want {
				t.Fatalf("%s disjoint %s = %v, want %v", a, b, got, tc.want)
			}
			// Symmetric, for the reason the ref-level table is: an install checks one way and
			// means both, so an asymmetric answer makes a refusal depend on installation order.
			if got := b.Disjoint(a); got != tc.want {
				t.Fatalf("%s disjoint %s = %v, want %v (asymmetric)", b, a, got, tc.want)
			}
		})
	}
}

// EVERY CLAIM IS THE SAME KIND OF THING, at every depth: the ones the connectors ship parse
// and render back unchanged, which is what lets a refusal quote the operator's own words.
func TestEveryDeclaredClaimRoundTrips(t *testing.T) {
	g := verseio.NewGit()
	g.AddRepo("verse", t.TempDir())
	declared := g.Owns() // git's own, built by the code that ships
	declared = append(declared, owns(
		"slack.Acme",                                // slack.Owns
		"linear.ENG", "linear.project.*.owners.ENG", // linear.Owns
	)...)
	for _, o := range declared {
		s := o.At
		c, err := o.Claim()
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		if c.String() != s {
			t.Fatalf("%q round-tripped to %q", s, c.String())
		}
	}
	// And ClaimAt spells one with a wildcard in it, which a Ref cannot: a ref renders `*` as
	// the quoted NAME, and a claim carrying that is refused rather than read as a wildcard.
	if got, want := verseio.ClaimAt("linear", "project", "*", "owners", "ENG"),
		"linear.project.*.owners.ENG"; got != want {
		t.Fatalf("ClaimAt spelled %q, want %q", got, want)
	}
	if _, err := verseio.ParseClaim(`linear.project."*".owners.ENG`); err == nil {
		t.Fatal("a quoted `*` parsed as a claim — it is a name, and one nothing is called")
	}
}

// Ownership is a union, so one overlapping pair is an overlap however disjoint the rest are.
func TestDisjointIsEveryPair(t *testing.T) {
	a := owns("git.*.verse/*", "git.head.verse")
	b := owns("git.*.kernel/*", "git.head.verse") // the second entry collides
	if ok, err := verseio.Disjoint(a, b); err != nil || ok {
		t.Fatalf("one overlapping pair is an overlap: ok=%v err=%v", ok, err)
	}
	// And a registration claiming nothing is disjoint from everything, which is the honest
	// reading of an empty list rather than a special case.
	if ok, err := verseio.Disjoint(nil, a); err != nil || !ok {
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
		if c, err := verseio.ParseClaim(s); err == nil {
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
		c, err := verseio.ParseClaim(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		if c.String() != s {
			t.Fatalf("%q round-tripped to %q", s, c.String())
		}
	}
	// A colon is an ordinary character in a level, since nothing in the address grammar reads
	// one — and the trailing `*` is the pattern's rather than the name's.
	c, err := verseio.ParseClaim("git.ref.a:b/*")
	if err != nil || len(c.Segs) != 3 || c.Segs[2] != (verseio.Seg{Name: "a:b/", Prefix: true}) {
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
			a, err := verseio.ParseClaim(tc.a)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.a, err)
			}
			b, err := verseio.ParseClaim(tc.b)
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
	claims, err := verseio.ParseOwns([]verseio.Own{{At: "verse-dev.drift"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []entity.Ref{
		entity.RefAt("verse-dev", "drift", "head", "abc"),
		entity.RefAt("verse-dev", "drift", "finding", "1"),
		entity.RefAt("verse-dev", "drift"),
	} {
		if places := verseio.Claimed(claims, ref); len(places) != 1 {
			t.Errorf("%s is under the claim; Claimed = %v", ref, places)
		}
	}
	for _, ref := range []entity.Ref{
		entity.RefAt("verse-dev", "merge", "train", "x"),
		entity.RefAt("git", "ref", "main"),
	} {
		if places := verseio.Claimed(claims, ref); len(places) != 0 {
			t.Errorf("%s is outside the claim; Claimed = %v", ref, places)
		}
	}
}

// A CLAIM COVERS WHAT IS AT OR UNDER IT, AND NOTHING ABOVE: `--forget` tombstones what Claimed
// answers, and an ancestor of a claim holds things the source never owned.
func TestAClaimDoesNotCoverWhatIsAboveIt(t *testing.T) {
	claims := owns("slack.*.acme/*")
	parsed, err := verseio.ParseOwns(claims)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []entity.Ref{entity.RefAt("slack", "foo"), entity.RefAt("slack")} {
		if places := verseio.Claimed(parsed, ref); len(places) != 0 {
			t.Errorf("%s is above the claim, and Claimed = %v", ref, places)
		}
	}
	if places := verseio.Claimed(parsed, entity.RefAt("slack", "chan", "acme/general")); len(places) != 1 {
		t.Errorf("slack.chan.acme/general is the claim's own, and Claimed = %v", places)
	}
}

// The claim a source declares has to be the claim its reflection actually writes under, or the
// install proves something about a slice nobody touches. This is the one place the two halves
// meet, so it is asserted rather than assumed.
func TestGitOwnsCoversWhatItReflects(t *testing.T) {
	g := verseio.NewGit()
	g.AddRepo("A", t.TempDir())
	claims, err := verseio.ParseOwns(g.Owns())
	if err != nil {
		t.Fatalf("git declares a claim that does not parse: %v", err)
	}
	for _, ref := range []string{"git.A.ref.main", "git.A.head", "git.A.commit.abc123"} {
		// The place claimed is the ENTITY, since git claims no path — which is what makes a
		// --forget on it exactly the removal it has always been.
		places := verseio.Claimed(claims, mustRef(t, ref))
		if len(places) != 1 || places[0].Ref != mustRef(t, ref) {
			t.Fatalf("%s is reflected; Claimed = %v, want the whole entity", ref, places)
		}
	}
	// And it does not claim a neighbour's — which is what bounds a --forget removal to the
	// slice this registration was proved to own.
	for _, ref := range []string{"git.B.ref.main", "git.B.head", "git.A-extras.head"} {
		if places := verseio.Claimed(claims, mustRef(t, ref)); len(places) != 0 {
			t.Fatalf("%s belongs to another repo but is claimed at %v", ref, places)
		}
	}
}

// owns spells a declaration list from its terse refs, which is what every claim in the tree is.
func owns(refs ...string) []verseio.Own {
	out := make([]verseio.Own, 0, len(refs))
	for _, r := range refs {
		out = append(out, verseio.Own{At: r})
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
	g := verseio.NewGit()
	if err := g.SetRoot("mirror"); err != nil {
		t.Fatal(err)
	}
	g.AddRepo("verse", t.TempDir())
	if g.Root() != "mirror" {
		t.Fatalf("git System() = %q", g.Root())
	}
	for _, o := range g.Owns() {
		if !strings.HasPrefix(o.At, "mirror.") {
			t.Fatalf("a git claim outside the chosen system: %s", o.At)
		}
	}
	// The hook a git source installs hints in its own system, or the hint reaches nothing.
	if got := g.RepoRef("verse").String(); got != "mirror.verse" {
		t.Fatalf("git RepoRef = %q", got)
	}
	// And two systems are provably disjoint with nothing else said.
	plain := verseio.NewGit()
	plain.AddRepo("verse", t.TempDir())
	ok, err := verseio.Disjoint(g.Owns(), plain.Owns())
	if err != nil || !ok {
		t.Fatalf("two systems over the same repo were not disjoint: ok=%v err=%v", ok, err)
	}
	// The punctuation the address is built from is refused, as it is at install.
	for _, bad := range []string{"a:b", "a/b"} {
		if err := verseio.NewGit().SetRoot(bad); err == nil {
			t.Fatalf("system %q should be refused", bad)
		}
	}
}

// A claim carries no root, so `task.*.*` — and the wildcard `*:*:*`, which
// a source may register when nothing else is installed — asks about `verse.task.instance.x` as
// readily as about the world's `task.instance.x`. It must own neither: the caller that deletes
// (`verse iod remove --forget`) writes straight to the store, below the refusal that guards
// the HTTP face, so this was the one client-reachable path to a tombstone over verse's charter.
func TestNoClaimReachesVersesOwnHalf(t *testing.T) {
	for _, spell := range []string{"*.*.*", "task.*.*", "trigger.rule.*", "sources.git.verse"} {
		claims, err := verseio.ParseOwns([]verseio.Own{{At: spell}})
		if err != nil {
			t.Fatalf("%s: %v", spell, err)
		}
		for _, ref := range []entity.Ref{
			entity.VerseRef("task", "instance", "run-1"),
			entity.VerseRef("trigger", "rule", "review"),
			entity.VerseRef("iod", "git", "verse"),
		} {
			if got := verseio.Claimed(claims, ref); len(got) > 0 {
				t.Errorf("owns %q claims %s → %v, want nothing in verse's half", spell, ref, got)
			}
		}
		// And the world's half is unchanged: the same claim still owns what it is for.
		if spell == "task.*.*" {
			if got := verseio.Claimed(claims, entity.RefAt("task", "instance", "run-1")); len(got) != 1 {
				t.Errorf("owns %q lost its own slice: %v", spell, got)
			}
		}
	}
}

// mustClaim parses a claim, which is the only way to get one.
func mustClaim(t *testing.T, s string) verseio.Claim {
	t.Helper()
	c, err := verseio.ParseClaim(s)
	if err != nil {
		t.Fatalf("ParseClaim(%q): %v", s, err)
	}
	return c
}

// A claim's node is its levels up to the first glob, and a registration's nodes drop any that
// lie beneath another: an owner record at `git` already owns `git.verse`.
func TestAClaimsNodeIsItsLiteralLevels(t *testing.T) {
	claim := func(s string) verseio.Claim {
		t.Helper()
		c, err := verseio.ParseClaim(s)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, c := range []struct {
		claim, node string
		ok          bool
	}{
		{"git.verse", "git.verse", true},
		{"github.signadot.verse", "github.signadot.verse", true},
		{"linear.issue.ENG-*", "linear.issue", true},
		{"git.*.head", "git", true},
		{"*.*.*", "", false},
	} {
		n, ok := claim(c.claim).Node()
		if ok != c.ok || (ok && n.String() != c.node) {
			t.Errorf("%s: node %q %v, want %q %v", c.claim, n.String(), ok, c.node, c.ok)
		}
	}
	nodes := verseio.Nodes([]verseio.Claim{claim("git.verse"), claim("git.*.head"), claim("git-issue.verse")})
	var got []string
	for _, n := range nodes {
		got = append(got, n.String())
	}
	if strings.Join(got, " ") != "git git-issue.verse" {
		t.Errorf("nodes = %v, want [git git-issue.verse]", got)
	}
}
