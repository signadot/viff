package linear_test

import (
	"context"
	"github.com/signadot/tony-format/go-tony/ir"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/io/linear"
)

func newSource(t *testing.T, f *fakeLinear, cfg linear.Config) *linear.Linear {
	t.Helper()
	cfg.Auth = "lin_api_test"
	cfg.Base = f.serve(t)
	src, err := linear.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("linear.New: %v", err)
	}
	return src
}

func payloads(rn verseio.Reflection) map[string]entity.Obj {
	out := map[string]entity.Obj{}
	for _, e := range rn.Entities {
		out[e.Ref.String()] = entity.ToObj(e.Payload)
	}
	return out
}

// A team reflected whole: itself, its cycles, its issues — every one carrying the team key
// that scopes the covers.
func TestLinearReflectsATeamWhole(t *testing.T) {
	f := newFake()
	f.team("ENG", "Engineering")
	i := f.issue("ENG", "ENG-1", "ship the source", "In Progress", "started")
	i["assignee"] = map[string]any{"name": "ada", "displayName": "Ada", "email": "ada@example.com"}
	i["labels"] = map[string]any{"nodes": []map[string]any{{"name": "sdlc"}, {"name": "api"}}}
	i["project"] = map[string]any{"name": "Sources"}
	i["cycle"] = map[string]any{"number": 4}
	f.issue("ENG", "ENG-2", "write it down", "Done", "completed")
	f.cycles["ENG"] = []map[string]any{{
		"id": "cyc-4", "number": 4, "name": "Cycle 4",
		"startsAt": "2026-01-01T00:00:00Z", "endsAt": "2026-01-14T00:00:00Z",
	}}

	src := newSource(t, f, linear.Config{})
	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rn.Check(linear.Root); err != nil {
		t.Fatalf("the reflection is not safe to converge: %v", err)
	}
	got := payloads(rn)

	if team := got["linear.ENG.team"]; team == nil || team["name"] != "Engineering" {
		t.Fatalf("linear.ENG.team = %+v", team)
	}
	one := got["linear.ENG.issue.ENG-1"]
	if one == nil || one["title"] != "ship the source" || one["team"] != "ENG" {
		t.Fatalf("linear.ENG.issue.ENG-1 = %+v", one)
	}
	// stateType is the one to write a rule against: a workspace renames "In Progress", the
	// type behind it is Linear's own vocabulary and does not move.
	if one["state"] != "In Progress" || one["stateType"] != "started" {
		t.Fatalf("state/stateType = %v/%v", one["state"], one["stateType"])
	}
	if one["assignee"] != "Ada" || one["assigneeEmail"] != "ada@example.com" {
		t.Fatalf("assignee = %v/%v", one["assignee"], one["assigneeEmail"])
	}
	if labels := entity.Strs(entity.Obj(one).Payload(), "labels"); len(labels) != 2 ||
		labels[0] != "api" || labels[1] != "sdlc" {
		t.Fatalf("labels = %v, want them sorted so a re-read is not a change", labels)
	}
	// The project's NAME stays a field on the issue — it is what a rule about an issue reads,
	// and it costs no join. The project itself stands too, as one node per owner: see
	// TestTwoTeamsShareAProjectWithoutTombstoningEachOther.
	if one["project"] != "Sources" {
		t.Fatalf("project = %v", one["project"])
	}
	// A CYCLE IS KEYED BY ITS NUMBER, because its team is the level above it rather than
	// something to pack into an id. The quotes are the address grammar's, not this source's: a
	// bare `4` in a path is an index, so a name that is a number is written as one.
	if c := got[`linear.ENG.cycle."4"`]; c == nil || c["endsAt"] != "2026-01-14T00:00:00Z" {
		t.Fatalf(`linear.ENG.cycle."4" = %+v`, c)
	}

	// EVERY COVER IS A PLACE, and the team is a level of it rather than a payload field a
	// predicate narrows a whole kind back to.
	kinds := map[string]bool{}
	reach := map[string]entity.Ref{
		"team":    entity.RefAt("linear", "ENG", "team"),
		"issue":   entity.RefAt("linear", "ENG", "issue", "ENG-9"),
		"cycle":   entity.RefAt("linear", "ENG", "cycle", "9"),
		"project": entity.RefAt("linear", "project", "p9", "owners", "ENG"),
	}
	for _, c := range rn.Covers {
		for kind, ref := range reach {
			if c.MatchRef(ref) {
				kinds[kind] = true
			}
		}
		if c.Predicate != nil {
			t.Fatalf("a cover narrowed by a payload rather than by its address: %+v",
				entity.ToObj(c.Predicate))
		}
		// NOT ANOTHER TEAM'S, at either address: its own place, or its own place inside a
		// project. A cover reaching one of those would tombstone what the other source wrote.
		for _, theirs := range []entity.Ref{
			entity.RefAt("linear", "OPS", "issue", "OPS-1"),
			entity.RefAt("linear", "project", "p9", "owners", "OPS"),
		} {
			if c.MatchRef(theirs) {
				t.Fatalf("a cover reaches %s, which is another team's", theirs)
			}
		}
	}
	for _, want := range []string{"team", "issue", "cycle", "project"} {
		if !kinds[want] {
			t.Fatalf("no cover over %s", want)
		}
	}
}

// A team is read TO EXHAUSTION, because the answer covers the whole of it — and a partial list
// under a complete cover would tombstone what it merely failed to read.
func TestLinearPagesATeamToExhaustion(t *testing.T) {
	f := newFake()
	f.team("ENG", "Engineering")
	for _, id := range []string{"ENG-1", "ENG-2", "ENG-3", "ENG-4", "ENG-5"} {
		f.issue("ENG", id, id, "Todo", "unstarted")
	}
	f.page = 2

	src := newSource(t, f, linear.Config{PageSize: 2})
	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	got := payloads(rn)
	for _, id := range []string{"ENG-1", "ENG-2", "ENG-3", "ENG-4", "ENG-5"} {
		if got["linear.ENG.issue."+id] == nil {
			t.Fatalf("%s did not survive paging: %d entities", id, len(rn.Entities))
		}
	}
	// And the cap is an ERROR rather than a truncation, for the same reason.
	capped := newSource(t, f, linear.Config{PageSize: 2, MaxIssues: 3})
	if _, err := capped.Reflect(context.Background(), entity.Ref{}); err == nil {
		t.Fatal("a team over the cap answered partially under a complete cover")
	}
}

// THE ONE THAT COSTS ENTITIES: GraphQL refuses with HTTP 200 and an `errors` array. Read as an
// answer, that is an empty reflection carrying covers — an instruction to tombstone the team.
func TestLinearARefusalIsAnErrorAndNotAnEmptyReflection(t *testing.T) {
	f := newFake()
	f.team("ENG", "Engineering")
	f.issue("ENG", "ENG-1", "one", "Todo", "unstarted")
	src := newSource(t, f, linear.Config{})

	f.failure = "Access denied"
	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err == nil {
		t.Fatal("a refusal answered as a reflection")
	}
	if !strings.Contains(err.Error(), "Access denied") {
		t.Fatalf("error %v does not say what Linear said", err)
	}
	if len(rn.Covers) != 0 || len(rn.Entities) != 0 {
		t.Fatalf("a failed reflect answered %+v", rn)
	}
	// A bad credential fails at construction, not on the first pass.
	if _, err := linear.New(context.Background(), linear.Config{
		Auth: "wrong", Base: f.serve(t),
	}); err == nil {
		t.Fatal("a bad credential built a source")
	}
	// So does a team the credential cannot see — an install that names one would otherwise
	// stand there claiming a slice nothing writes.
	f.failure = ""
	if _, err := linear.New(context.Background(), linear.Config{
		Auth: "lin_api_test", Base: f.serve(t), Teams: []string{"NOPE"},
	}); err == nil {
		t.Fatal("a team this credential cannot see built a source")
	}
}

// A hint names a team, and every ref narrows to the team it belongs to — an issue by its
// identifier's prefix, a cycle by its id's. Another source's team is not this source's
// question, and answers with nothing rather than erroring.
func TestLinearTargetsTheTeamARefBelongsTo(t *testing.T) {
	f := newFake()
	f.team("ENG", "Engineering")
	f.team("OPS", "Operations")
	f.issue("ENG", "ENG-1", "one", "Todo", "unstarted")
	f.issue("OPS", "OPS-9", "nine", "Todo", "unstarted")

	src := newSource(t, f, linear.Config{Teams: []string{"ENG"}})
	for _, ref := range []entity.Ref{
		linear.TeamRef("ENG"),
		entity.RefAt("linear", "ENG", "issue", "ENG-1"),
		entity.RefAt("linear", "ENG", "cycle", "4"),
	} {
		rn, err := src.Reflect(context.Background(), ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if got := payloads(rn); got["linear.ENG.team"] == nil {
			t.Fatalf("%s did not narrow to ENG", ref)
		}
	}
	// A team this source does not reflect: no covers, so nothing of another source's is
	// tombstoned by a hint that arrived at the wrong door.
	rn, err := src.Reflect(context.Background(), linear.TeamRef("OPS"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rn.Covers) != 0 || len(rn.Entities) != 0 {
		t.Fatalf("another team's ref answered %+v", rn)
	}
}

// TWO PLACES PER TEAM: the team's own, which everything it owns alone stands under, and its
// place inside every project, which is what lets two teams share a node neither owns the whole
// of. No glob inside a segment anywhere — `linear.ENG` and `linear.ENGINE` are two places, and
// telling them apart is a comparison of names rather than of string prefixes.
func TestLinearOwnershipIsTwoPlacesPerTeam(t *testing.T) {
	f := newFake()
	f.team("ENG", "Engineering")
	f.team("ENGINE", "Engine")
	src := newSource(t, f, linear.Config{Teams: []string{"ENG"}})
	other := newSource(t, f, linear.Config{Teams: []string{"ENGINE"}})

	owns := verseio.Describe(src).Owns
	want := []verseio.Own{{At: "linear.ENG"}, {At: "linear.project.*.owners.ENG"}}
	if len(owns) != len(want) {
		t.Fatalf("owns %v, want %v", owns, want)
	}
	for i, w := range want {
		if owns[i].At != w.At {
			t.Fatalf("owns[%d] = %v, want %v", i, owns[i].At, w.At)
		}
	}
	ok, err := verseio.Disjoint(owns, verseio.Describe(other).Owns)
	if err != nil || !ok {
		t.Fatalf("ENG and ENGINE were not provably disjoint (%v, %v)", ok, err)
	}
	// Everything this source writes is claimed — one claim covering a whole subtree, however
	// deep it goes, which is the bound on what a `--forget` removal may tombstone.
	for _, ref := range []entity.Ref{
		entity.RefAt("linear", "ENG", "team"),
		entity.RefAt("linear", "ENG", "issue", "ENG-1"),
		entity.RefAt("linear", "ENG", "cycle", "4"),
		entity.RefAt("linear", "project", "p1", "owners", "ENG"),
	} {
		if !claims(t, owns, ref) {
			t.Fatalf("%s is written but not claimed", ref)
		}
	}
	for _, theirs := range []entity.Ref{
		entity.RefAt("linear", "ENGINE", "issue", "ENGINE-1"),
		entity.RefAt("linear", "project", "p1", "owners", "ENGINE"),
	} {
		if claims(t, owns, theirs) {
			t.Fatalf("ENG claims %s", theirs)
		}
	}
}

// Converge, end to end: an issue that moves is a change, an unchanged pass writes nothing, and
// an issue deleted in Linear is tombstoned under the team's cover.
func TestLinearConvergesATeamThroughTheReflector(t *testing.T) {
	ctx := context.Background()
	store := entitytest.Store(t)
	f := newFake()
	f.team("ENG", "Engineering")
	f.issue("ENG", "ENG-1", "one", "Todo", "unstarted")
	f.issue("ENG", "ENG-2", "two", "Todo", "unstarted")

	src := newSource(t, f, linear.Config{})
	rf := verseio.NewReflector(src, store, 0)
	var wrote int
	rf.OnPass(func(p verseio.Pass) { wrote = p.Wrote })
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	get := func(id string) (entity.Obj, bool) {
		e, ok, _ := store.Get(entity.RefAt(linear.Root, "ENG", "issue", id))
		return entity.ToObj(e.Payload), ok
	}
	if _, ok := get("ENG-1"); !ok {
		t.Fatal("ENG-1 did not land")
	}

	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	if wrote != 0 {
		t.Fatalf("an unchanged pass wrote %d entities — every resync would re-fire every "+
			"trigger over the slice", wrote)
	}

	// ENG-1 moves; ENG-2 is deleted in Linear.
	f.issues["ENG"] = f.issues["ENG"][:1]
	f.issues["ENG"][0]["state"] = map[string]any{"name": "Done", "type": "completed"}
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	if p, _ := get("ENG-1"); p["stateType"] != "completed" {
		t.Fatalf("ENG-1 = %+v, want the new state", p)
	}
	if _, ok := get("ENG-2"); ok {
		t.Fatal("ENG-2 is gone from Linear and still standing in the verse")
	}
}

// claims reports whether an ownership list owns anything at ref. Parsed once and asserted on,
// because the parse is where a malformed claim is supposed to stop — see verseio.ParseOwns.
func claims(t *testing.T, owns []verseio.Own, ref entity.Ref) bool {
	t.Helper()
	cs, err := verseio.ParseOwns(owns)
	if err != nil {
		t.Fatalf("parsing %v: %v", owns, err)
	}
	return len(verseio.Claimed(cs, ref)) > 0
}

// THE CASE THAT PRODUCED THE FINDING, end to end.
//
// A Linear project belongs to several teams at once, so it has no positional owner: two sources
// over two teams would each tombstone what the other wrote if either owned the project itself.
// Each is instead the single writer of one node INSIDE it — `linear.project.<id>.owners.ENG`,
// `…owners.OPS` — proved apart at install and kept apart by converge.
//
// Two sources, two teams, one shared project and one that is not shared. It is the whole of the
// design in one test, which is why it is the one to break first when something moves.
func TestTwoTeamsShareAProjectWithoutTombstoningEachOther(t *testing.T) {
	f := newFake()
	f.team("ENG", "Engineering")
	f.team("OPS", "Operations")
	f.project("ENG", "p-shared", "Migration") // the same project, under both teams
	f.project("OPS", "p-shared", "Migration")
	f.project("ENG", "p-eng", "Sources") // and one that is ENG's alone

	store := entitytest.Store(t)
	shared := entity.RefAt("linear", "project", "p-shared")
	engOnly := entity.RefAt("linear", "project", "p-eng")

	reflector := func(team string) *verseio.Reflector {
		return verseio.NewReflector(newSource(t, f, linear.Config{Teams: []string{team}}), store, 0)
	}
	eng, ops := reflector("ENG"), reflector("OPS")
	pass := func(rf *verseio.Reflector) int {
		n := -1
		rf.OnPass(func(p verseio.Pass) {
			if p.Err != nil {
				t.Fatalf("pass: %v", p.Err)
			}
			n = p.Wrote
		})
		if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	owners := func(ref entity.Ref) map[string]*ir.Node {
		e, ok, err := store.Get(ref)
		if err != nil || !ok {
			t.Fatalf("get %s: ok=%v err=%v", ref, ok, err)
		}
		return entity.Fields(entity.Get(e.Payload, "owners"))
	}

	pass(eng)
	pass(ops)

	// Both owners stand, and each holds the project as its own team saw it.
	both := owners(shared)
	if len(both) != 2 || both["ENG"] == nil || both["OPS"] == nil {
		t.Fatalf("one team tombstoned the other's node: %v", both)
	}
	if got := entity.Str(both["OPS"], "name"); got != "Migration" {
		t.Fatalf("OPS's node = %q", got)
	}
	// And the project has no top: every field Linear has about one is global, so it lives under
	// each owner and a reader picks an owner. This is asserted rather than merely documented,
	// because it is the shape a charter has to be written against.
	e, _, _ := store.Get(shared)
	if entity.Get(e.Payload, "name") != nil {
		t.Fatalf("a project grew a top-level field nobody owns: %v", entity.ToObj(e.Payload))
	}

	// ENG's own project is written by ENG and untouched by OPS's pass.
	if len(owners(engOnly)) != 1 || owners(engOnly)["ENG"] == nil {
		t.Fatalf("ENG's own project = %v", owners(engOnly))
	}

	// The pass that changes nothing writes nothing — four alternating passes, and neither
	// source ever rewrites what the other wrote. This is the failure the whole design exists
	// not to have, and it is unbounded rather than wrong-once.
	for i := 0; i < 2; i++ {
		if n := pass(eng); n != 0 {
			t.Fatalf("ENG rewrote %d after OPS's pass", n)
		}
		if n := pass(ops); n != 0 {
			t.Fatalf("OPS rewrote %d after ENG's pass", n)
		}
	}

	// OPS stops owning the shared project. Its node goes; ENG's stands, and so does the project.
	f.projects["OPS"] = nil
	pass(ops)
	left := owners(shared)
	if _, still := left["OPS"]; still {
		t.Fatalf("OPS left the project and kept its node: %v", left)
	}
	if left["ENG"] == nil {
		t.Fatalf("OPS leaving took ENG's node with it: %v", left)
	}

	// And when the last owner goes, so does the project: the removal empties `owners`, which
	// empties the project, and each step up is conditional on nothing else standing there.
	f.projects["ENG"] = nil
	pass(eng)
	if _, ok, _ := store.Get(shared); ok {
		t.Fatal("the last owner left and the project stands")
	}
	if _, ok, _ := store.Get(engOnly); ok {
		t.Fatal("ENG's own project outlived its only owner")
	}
}

// One source, two teams, one project they share. The same project is answered with two nodes
// under it and two covers, which is the case a per-source claim cannot express and a per-place
// one can.
func TestOneSourceOverTwoTeamsWritesBothOwners(t *testing.T) {
	f := newFake()
	f.team("ENG", "Engineering")
	f.team("OPS", "Operations")
	f.project("ENG", "p-shared", "Migration")
	f.project("OPS", "p-shared", "Migration")

	store := entitytest.Store(t)
	src := newSource(t, f, linear.Config{Teams: []string{"ENG", "OPS"}})
	rf := verseio.NewReflector(src, store, 0)
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	e, ok, _ := store.Get(entity.RefAt("linear", "project", "p-shared"))
	if !ok {
		t.Fatal("the shared project was not written")
	}
	own := entity.Fields(entity.Get(e.Payload, "owners"))
	if len(own) != 2 || own["ENG"] == nil || own["OPS"] == nil {
		t.Fatalf("one source over two teams wrote %v", own)
	}
}
