package charter

import (
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// A record round-trips through its payload: the spec raw, the scope and the address stamped
// when there is one, the task beside it; and its id carries the package.
func TestARecordRoundTrips(t *testing.T) {
	store := entitytest.Store(t)
	spec := ir.FromMap(map[string]*ir.Node{"name": ir.FromString("review")})
	p := Payload(spec, "me", "demo.pr")
	p[TaskField] = "installer"
	entitytest.Set(t, store, RuleRef("demo.pr", "review"), p.Payload())
	ent, ok, err := store.Get(RuleRef("demo.pr", "review"))
	if err != nil || !ok {
		t.Fatalf("the record: ok=%v err=%v", ok, err)
	}
	r, err := Decode(ent, 7)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != RuleKind || r.Name != "review" || r.Scope != "me" || r.At != "demo.pr" || r.Task != "installer" || r.Rev != 7 {
		t.Fatalf("decoded %+v", r)
	}
	if r.Spec == nil || !r.Spec.DeepEqual(spec) {
		t.Fatalf("the spec did not come back: %v", r.Spec)
	}
	if ent.Ref.Name() != "demo.pr:review" || Name(ent.Ref.Name()) != "review" {
		t.Fatalf("id = %q, name = %q", ent.Ref.Name(), Name(ent.Ref.Name()))
	}
	if base := RuleRef("", "review"); base.Name() != "review" || !IsRule(base) {
		t.Fatalf("baseline's id = %q", base.Name())
	}
	if st := StratumRef("demo", "observe"); !IsStratum(st) || st.Name() != "demo:observe" {
		t.Fatalf("stratum ref = %v", st)
	}
	if _, err := Decode(entity.Entity{Ref: PackageRef("demo")}, 1); err == nil {
		t.Fatal("a package declaration decoded as a rule's or a stratum's record")
	}
}

// Follow delivers the records as they change: the picture first, holding what stands, then
// each set and each deletion at the revision it landed, with a package declaration passed over.
func TestFollowDeliversTheRecordsAsTheyChange(t *testing.T) {
	store := entitytest.Store(t)
	spec := func(name string) *ir.Node {
		return ir.FromMap(map[string]*ir.Node{"name": ir.FromString(name)})
	}
	entitytest.Set(t, store, RuleRef("", "review"), Payload(spec("review"), "", "").Payload())
	entitytest.Set(t, store, StratumRef("demo", "observe"), Payload(spec("observe"), "", "demo").Payload())
	entitytest.Set(t, store, PackageRef("demo"), entity.Obj{AtField: "demo"}.Payload())

	w, err := Follow(store)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	next := func(why string) entity.Commit {
		select {
		case c := <-w.Commits:
			return c
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: nothing delivered", why)
		}
		return entity.Commit{}
	}
	picture := Changes(next("the picture"))
	if len(picture) != 2 {
		t.Fatalf("the picture holds %d records, want the rule and the stratum: %+v", len(picture), picture)
	}
	kinds := map[string]string{}
	for _, c := range picture {
		kinds[c.Name] = c.Kind
		if c.Deleted || c.Spec == nil {
			t.Fatalf("a record of the picture is %+v", c)
		}
	}
	if kinds["review"] != RuleKind || kinds["observe"] != StratumKind {
		t.Fatalf("kinds = %v", kinds)
	}

	entitytest.Set(t, store, RuleRef("", "triage"), Payload(spec("triage"), "", "").Payload())
	set := Changes(next("the set"))
	if len(set) != 1 || set[0].Name != "triage" || set[0].Deleted || set[0].Rev != store.Rev() {
		t.Fatalf("the set delivered %+v at rev %d", set, store.Rev())
	}

	if _, _, err := store.Write(entity.Write{At: RuleRef("", "review").At()}); err != nil {
		t.Fatal(err)
	}
	gone := Changes(next("the deletion"))
	if len(gone) != 1 || gone[0].Name != "review" || !gone[0].Deleted || gone[0].Kind != RuleKind {
		t.Fatalf("the deletion delivered %+v", gone)
	}
}
