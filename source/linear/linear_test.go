package linear_test

import (
	"context"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/source"
	"github.com/signadot/verse/source/linear"
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

func payloads(rn source.Reflection) map[string]entity.Obj {
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
	if err := rn.Check(linear.System); err != nil {
		t.Fatalf("the reflection is not safe to converge: %v", err)
	}
	got := payloads(rn)

	if team := got["linear:team:ENG"]; team == nil || team["name"] != "Engineering" {
		t.Fatalf("linear:team:ENG = %+v", team)
	}
	one := got["linear:issue:ENG-1"]
	if one == nil || one["title"] != "ship the source" || one["team"] != "ENG" {
		t.Fatalf("linear:issue:ENG-1 = %+v", one)
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
	// The project is a FIELD on the issue and not an entity of its own — a project can belong
	// to several teams, so no team can claim one as an address.
	if one["project"] != "Sources" {
		t.Fatalf("project = %v", one["project"])
	}
	if got["linear:project:Sources"] != nil {
		t.Fatal("a project was reflected as an entity; see linear.Owns")
	}
	if c := got["linear:cycle:ENG/4"]; c == nil || c["endsAt"] != "2026-01-14T00:00:00Z" {
		t.Fatalf("linear:cycle:ENG/4 = %+v", c)
	}

	kinds := map[string]bool{}
	for _, c := range rn.Covers {
		kinds[c.Kind] = true
		if entity.Str(c.Predicate, "team") != "ENG" {
			t.Fatalf("a cover not scoped to the team: %+v", entity.ToObj(c.Predicate))
		}
	}
	for _, want := range []string{"team", "issue", "cycle"} {
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
		if got["linear:issue:"+id] == nil {
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
		{System: "linear", Kind: "issue", ID: "ENG-1"},
		{System: "linear", Kind: "cycle", ID: "ENG/4"},
	} {
		rn, err := src.Reflect(context.Background(), ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if got := payloads(rn); got["linear:team:ENG"] == nil {
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

// Three claims per team, and the issue claim leans on the hyphen: `ENG-*` must not claim
// `ENGINE-1`, or two perfectly disjoint teams would refuse to install alongside each other.
func TestLinearOwnershipIsThreeClaimsPerTeam(t *testing.T) {
	f := newFake()
	f.team("ENG", "Engineering")
	f.team("ENGINE", "Engine")
	src := newSource(t, f, linear.Config{Teams: []string{"ENG"}})
	other := newSource(t, f, linear.Config{Teams: []string{"ENGINE"}})

	owns := source.Describe(src).Owns
	want := []source.Own{{Ref: "linear:team:ENG"}, {Ref: "linear:issue:ENG-*"}, {Ref: "linear:cycle:ENG/*"}}
	if len(owns) != len(want) {
		t.Fatalf("owns %v, want %v", owns, want)
	}
	for i, w := range want {
		if owns[i].String() != w.String() {
			t.Fatalf("owns[%d] = %v, want %v", i, owns[i], w)
		}
	}
	ok, err := source.Disjoint(owns, source.Describe(other).Owns)
	if err != nil || !ok {
		t.Fatalf("ENG and ENGINE were not provably disjoint (%v, %v) — the hyphen in the "+
			"issue claim is what separates them", ok, err)
	}
	for _, ref := range []entity.Ref{
		{System: "linear", Kind: "team", ID: "ENG"},
		{System: "linear", Kind: "issue", ID: "ENG-1"},
		{System: "linear", Kind: "cycle", ID: "ENG/4"},
	} {
		if !claims(t, owns, ref) {
			t.Fatalf("%s is written but not claimed", ref)
		}
	}
	if claims(t, owns, entity.Ref{System: "linear", Kind: "issue", ID: "ENGINE-1"}) {
		t.Fatal("ENG claims ENGINE-1")
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
	rf := source.NewReflector(src, store, 0)
	var wrote int
	rf.OnPass(func(p source.Pass) { wrote = p.Wrote })
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	get := func(id string) (entity.Obj, bool) {
		e, ok, _ := store.Get(entity.Ref{System: linear.System, Kind: "issue", ID: id})
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
// because the parse is where a malformed claim is supposed to stop — see source.ParseOwns.
func claims(t *testing.T, owns []source.Own, ref entity.Ref) bool {
	t.Helper()
	cs, err := source.ParseOwns(owns)
	if err != nil {
		t.Fatalf("parsing %v: %v", owns, err)
	}
	return len(source.Claimed(cs, ref)) > 0
}
