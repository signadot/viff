package trigger

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// get / list build the two tags. The tag is the whole of the spelling — a plain `{get-path: …}`
// key is ORDINARY DATA and stays that way, which is deliberate: a magic key turns a payload
// somebody wrote into a lookup nobody asked for.
func get(addr any) entity.Payload  { return entity.Val(addr).WithTag("!" + GetPathTag) }
func list(addr any) entity.Payload { return entity.Val(addr).WithTag("!" + ListPathsTag) }

func readAction(addr any) Action {
	return Action{Patch: entity.Obj{"why": get(addr)}.Payload()}
}

// `!get-path` names ONE node: all three segments, literal system and kind, no predicate, and
// nothing that could name several — a globbing id or a wildcard path is the LIST form's
// business, and saying so points at the tag that answers it.
func TestGetPathMustNameOneNode(t *testing.T) {
	eng, _ := newEng(t)
	bad := map[string]any{
		"no id":            entity.Obj{"system": "git", "kind": "repo"},
		"a globbing id":    entity.Obj{"system": "git", "kind": "repo", "id": "*"},
		"a wildcard path":  entity.Obj{"system": "git", "kind": "repo", "id": "abc", "path": "labels.*"},
		"no kind":          entity.Obj{"system": "git", "id": "abc"},
		"a predicate":      entity.Obj{"system": "git", "kind": "repo", "id": "abc", "value": entity.Obj{"x": 1}},
		"an unknown key":   entity.Obj{"system": "git", "kind": "repo", "id": "abc", "branch": "main"},
		"a globbing kind":  entity.Obj{"system": "git", "kind": "*", "id": "abc"},
		"a glob on kind":   entity.Obj{"system": "git", "kind": "re*", "id": "abc"},
		"a ref for system": entity.Obj{"system": "$subject.system", "kind": "repo", "id": "abc"},
		"a short address":  "git:repo",
		"a bare address":   "git",
	}
	for why, addr := range bad {
		err := eng.AddTrigger(Trigger{
			Name:      "bad-" + strings.ReplaceAll(why, " ", "-"),
			Condition: Condition{Subject: Match{"system": "demo", "kind": "pr"}},
			Action:    readAction(addr),
		})
		if err == nil {
			t.Errorf("installed a read with %s", why)
		}
	}
	ok := map[string]any{
		"the object form":       entity.Obj{"system": "git", "kind": "repo", "id": "abc"},
		"the address form":      "git:repo:abc",
		"a reference id":        entity.Obj{"system": "git", "kind": "repo", "id": "$subject.value.head"},
		"a payload path":        entity.Obj{"system": "git", "kind": "repo", "id": "abc", "path": "branch"},
		"a reference in a addr": "git:repo:$subject.value.head",
	}
	for why, addr := range ok {
		if err := eng.AddTrigger(Trigger{
			Name:      "ok-" + strings.ReplaceAll(why, " ", "-"),
			Condition: Condition{Subject: Match{"system": "demo", "kind": "pr"}},
			Action:    readAction(addr),
		}); err != nil {
			t.Errorf("%s was refused: %v", why, err)
		}
	}
}

// The point of the feature: a charter can COPY state it does not own.
func TestReadCopiesAnotherEntity(t *testing.T) {
	eng, store := newEng(t)
	entitytest.Set(t, store, entity.Ref{System: "git", Kind: "repo", ID: "abc"},
		entity.Obj{"branch": "main", "clean": true}.Payload())
	if err := eng.AddTrigger(Trigger{
		Name:      "copy",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action: Action{Patch: entity.Obj{
			"stage": "read",
			// The id is a reference, which is the whole reason a read is worth having: the
			// address is as dynamic as anyone needs, while what is checked at install stays put.
			"repo": get(entity.Obj{"system": "git", "kind": "repo", "id": "$subject.value.head"}),
		}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open", "head": "abc"}.Payload())
	waitStage(t, store, ref, "read")
	got, _, _ := store.Get(ref)
	repo := entity.ToObj(entity.Get(got.Payload, "repo"))
	if repo["branch"] != "main" || repo["clean"] != true {
		t.Fatalf("the read did not copy the entity's payload: %+v", repo)
	}
}

// Absent is a failure, not a null. The firing asked for that entity's data and there is none;
// writing null would launder "nothing is there" into "something is there, and it is nothing" —
// a difference the store records and a match can ask about.
func TestReadOfSomethingAbsentFailsTheFiring(t *testing.T) {
	eng, store := newEng(t)
	if err := eng.AddTrigger(Trigger{
		Name:      "copy-missing",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action: Action{Patch: entity.Obj{
			"stage": "read",
			"repo":  get("git:repo:nope"),
		}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	time.Sleep(400 * time.Millisecond)
	got, _, _ := store.Get(ref)
	if s := entity.ToObj(got.Payload)["stage"]; s != "open" {
		t.Fatalf("stage = %v, want it left at \"open\": a read of something absent must fail the "+
			"firing rather than write half of it", s)
	}
}

// A read is answered at the commit that caused the firing, like $subject.value and for the same
// reason: a firing held at a gate acts whenever the answer comes, and a late read writes from a
// state nobody evaluated.
func TestReadAnswersAtTheCausingCommit(t *testing.T) {
	store := entitytest.Store(t)
	release := make(chan struct{})
	eng := New(store, blockingResolver{release: release})
	entitytest.Set(t, store, entity.Ref{System: "git", Kind: "repo", ID: "abc"},
		entity.Obj{"branch": "main"}.Payload())
	if err := eng.AddTrigger(Trigger{
		Name:      "gated-read",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Gates:     []Gate{{Mode: "ask"}},
		Action: Action{Patch: entity.Obj{
			"stage": "read",
			"repo":  get("git:repo:abc"),
		}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	time.Sleep(150 * time.Millisecond) // let it reach the gate
	entitytest.Set(t, store, entity.Ref{System: "git", Kind: "repo", ID: "abc"},
		entity.Obj{"branch": "moved-on"}.Payload())
	time.Sleep(100 * time.Millisecond)
	close(release)

	waitStage(t, store, ref, "read")
	got, _, _ := store.Get(ref)
	if b := entity.ToObj(entity.Get(got.Payload, "repo"))["branch"]; b != "main" {
		t.Fatalf("branch = %v, want \"main\" — the read was answered when the gate was, rather "+
			"than at the commit that caused the firing", b)
	}
}

// ── the level rule, which is the only place a read is constrained ─────────────────

// A read inside a DERIVE must be at or below the rule's own level. Reading upward is how a
// level above would change what a derive writes, and a derive that writes something new every
// lap wakes that level again — a cycle across a level.
func TestReadInADeriveMustBeAtOrBelow(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng,
		Stratum{Name: "low", Level: 0, Match: []Match{{"system": "gh"}}},
		Stratum{Name: "mid", Level: 10, Match: []Match{{"system": "signadot"}}},
		Stratum{Name: "high", Level: 100, Match: []Match{{"system": "do"}}},
	)
	derivingRead := func(name, stratum, subjSys string, addr any) Trigger {
		return Trigger{
			Name: name, Stratum: stratum,
			Condition: Condition{Subject: Match{"system": subjSys, "kind": "thing"}},
			Action: Action{Derive: entity.Obj{"do": entity.Obj{"approve": entity.Obj{
				"why": get(addr),
			}}}.Payload()},
		}
	}
	// Below, and at its own level: both fine.
	if err := eng.AddTrigger(derivingRead("reads-below", "mid", "signadot", "gh:issue:1")); err != nil {
		t.Errorf("a derive reading BELOW its level was refused: %v", err)
	}
	if err := eng.AddTrigger(derivingRead("reads-level", "mid", "signadot", "signadot:other:1")); err != nil {
		t.Errorf("a derive reading AT its level was refused: %v", err)
	}
	// Above, and outside the ordering: both refused.
	err := eng.AddTrigger(derivingRead("reads-above", "low", "gh", "signadot:feature:1"))
	if err == nil {
		t.Error("a derive read ABOVE its own level, which is how a cycle crosses a level")
	} else if !strings.Contains(err.Error(), "above") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
	if err := eng.AddTrigger(derivingRead("reads-nowhere", "low", "gh", "elsewhere:thing:1")); err == nil {
		t.Error("a derive read an entity in no stratum, which has no level to compare")
	}
}

// The same read in a PATCH is unconstrained: nothing downstream depends on a patch being
// reproducible, so there is no inertness to protect.
func TestReadInAPatchIsUnconstrained(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng,
		Stratum{Name: "low", Level: 0, Match: []Match{{"system": "gh"}}},
		Stratum{Name: "high", Level: 10, Match: []Match{{"system": "signadot"}}},
	)
	if err := eng.AddTrigger(Trigger{
		Name:      "reads-up",
		Stratum:   "low",
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
		Action:    Action{Patch: entity.Obj{"why": get("signadot:feature:1")}.Payload()},
	}); err != nil {
		t.Fatalf("a patch reading a higher level was refused: %v", err)
	}
}

// A read inside a completion hook has no causing commit to be answered at — the hook is woken by
// a task finishing rather than by a write — so it fails by name rather than reading whatever
// the store happens to hold now.
func TestReadInAHookHasNoCommit(t *testing.T) {
	eng, _ := newEng(t)
	oc := occasion{} // what applyHook builds: the task event, and no commit
	_, err := eng.resolveRead(get("git:repo:abc"), nil, oc)
	if err == nil {
		t.Fatal("a read resolved with no causing commit")
	}
	if !strings.Contains(err.Error(), "no causing commit") {
		t.Fatalf("the failure does not say why: %v", err)
	}
}

// One snapshot per firing, shared by every read in the action — not one per reference. AsOf
// answers by replaying the log to that commit, so three reads would otherwise be three replays
// to learn three things about one world.
//
// Asserted over the expander directly rather than through a running engine, because the engine
// takes snapshots for its own reasons too — edge.go reads at C and C-1 to evaluate an `on` —
// and a counter over a live firing would be measuring those as well.
func TestOneSnapshotPerFiring(t *testing.T) {
	store := entitytest.Store(t)
	counting := &countingStore{Store: store}
	eng := New(counting, task.AutoResolver{})
	for _, id := range []string{"a", "b", "c"} {
		entitytest.Set(t, store, entity.Ref{System: "git", Kind: "repo", ID: id}, entity.Obj{"n": id}.Payload())
	}
	oc := occasion{commit: entity.Commit{Rev: store.Rev()}, snap: &snapCache{}}
	patch := entity.Obj{
		"a": get("git:repo:a"),
		"b": get("git:repo:b"),
		"c": get("git:repo:c"),
	}.Payload()
	out, err := expandPatch(patch, refRoot(nil, subjectNode(entity.Ref{System: "demo", Kind: "pr", ID: "1"}, nil, 0)),
		eng.readerFor(oc))
	if err != nil {
		t.Fatalf("expanding three reads: %v", err)
	}
	for _, k := range []string{"a", "b", "c"} {
		if got := entity.ToObj(entity.Get(out, k))["n"]; got != k {
			t.Fatalf("read %q gave %v", k, got)
		}
	}
	if n := counting.asOf(); n != 1 {
		t.Fatalf("took %d snapshots for one firing with three reads, want 1", n)
	}
}

// countingStore counts the snapshots taken through it.
type countingStore struct {
	entity.Store
	mu sync.Mutex
	n  int
}

func (c *countingStore) AsOf(rev int64, within ...[2]string) (entity.Snapshot, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.Store.AsOf(rev, within...)
}

func (c *countingStore) asOf() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// `!get-path` with a `path:` reaches into the payload rather than taking all of it. That is
// what makes the pair one grammar: a verse is one document, an entity address is a path prefix,
// and `path:` continues it.
func TestGetPathReachesIntoThePayload(t *testing.T) {
	eng, store := newEng(t)
	entitytest.Set(t, store, entity.Ref{System: "git", Kind: "repo", ID: "abc"},
		entity.Obj{"branch": "main", "meta": entity.Obj{"tags": []any{"v1", "v2"}}}.Payload())
	if err := eng.AddTrigger(Trigger{
		Name:      "deep",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action: Action{Patch: entity.Obj{
			"stage":  "read",
			"branch": get(entity.Obj{"system": "git", "kind": "repo", "id": "abc", "path": "branch"}),
			"second": get(entity.Obj{"system": "git", "kind": "repo", "id": "abc", "path": "meta.tags[1]"}),
		}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, ref, "read")
	got, _, _ := store.Get(ref)
	obj := entity.ToObj(got.Payload)
	if obj["branch"] != "main" {
		t.Errorf("branch = %v, want \"main\"", obj["branch"])
	}
	if obj["second"] != "v2" {
		t.Errorf("second = %v, want \"v2\" — an index into the payload", obj["second"])
	}
}

// `!list-paths` is where the globs live, on the id and on the path alike. It answers with a
// list, in the selection's order, which Match.Select sorts by ref.
func TestListPathsCollects(t *testing.T) {
	eng, store := newEng(t)
	for _, r := range []struct{ id, branch string }{{"c", "three"}, {"a", "one"}, {"b", "two"}} {
		entitytest.Set(t, store, entity.Ref{System: "git", Kind: "repo", ID: r.id},
			entity.Obj{"branch": r.branch}.Payload())
	}
	entitytest.Set(t, store, entity.Ref{System: "git", Kind: "other", ID: "x"},
		entity.Obj{"branch": "not-a-repo"}.Payload())
	if err := eng.AddTrigger(Trigger{
		Name:      "collect",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action: Action{Patch: entity.Obj{
			"stage":    "read",
			"branches": list(entity.Obj{"system": "git", "kind": "repo", "id": "*", "path": "branch"}),
			"nothing":  list(entity.Obj{"system": "git", "kind": "repo", "id": "zz-*", "path": "branch"}),
		}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, ref, "read")
	got, _, _ := store.Get(ref)

	branches := entity.Get(got.Payload, "branches")
	if branches == nil || branches.Type != ir.ArrayType {
		t.Fatalf("branches is not a list: %v", branches)
	}
	var vals []string
	for _, v := range branches.Values {
		vals = append(vals, v.String)
	}
	// Sorted by ref, not by the order they were written — a list written into an entity has to
	// be deterministic, or a re-derive writes a real change every lap.
	if strings.Join(vals, ",") != "one,two,three" {
		t.Fatalf("branches = %v, want [one two three] — the selection's order, sorted by ref", vals)
	}
	// The kind is literal, so `git:other:x` is not in the selection however its payload looks.
	if len(vals) != 3 {
		t.Fatalf("the list crossed a kind: %v", vals)
	}
	// Empty is a LIST, where an absent get-path is a failure.
	nothing := entity.Get(got.Payload, "nothing")
	if nothing == nil || nothing.Type != ir.ArrayType || len(nothing.Values) != 0 {
		t.Fatalf("a list matching nothing should be an empty list, got %v", nothing)
	}
}

// A wildcard PATH collects within each payload, which is the case GetKPath refuses outright
// ("any field .* in get") and ListKPath is for.
func TestListPathsWildcardPath(t *testing.T) {
	eng, store := newEng(t)
	entitytest.Set(t, store, entity.Ref{System: "git", Kind: "repo", ID: "abc"},
		entity.Obj{"labels": entity.Obj{"a": "one", "b": "two"}}.Payload())
	if err := eng.AddTrigger(Trigger{
		Name:      "labels",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action: Action{Patch: entity.Obj{
			"stage": "read",
			"all":   list(entity.Obj{"system": "git", "kind": "repo", "id": "abc", "path": "labels.*"}),
		}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, ref, "read")
	got, _, _ := store.Get(ref)
	all := entity.Get(got.Payload, "all")
	if all == nil || all.Type != ir.ArrayType || len(all.Values) != 2 {
		t.Fatalf("a wildcard path did not collect the fields: %v", all)
	}
}

// Both tags are constrained inside a derive, by the same rule and the same check.
func TestListPathsInADeriveMustBeAtOrBelow(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng,
		Stratum{Name: "low", Level: 0, Match: []Match{{"system": "gh"}}},
		Stratum{Name: "high", Level: 10, Match: []Match{{"system": "signadot"}}},
	)
	err := eng.AddTrigger(Trigger{
		Name: "lists-up", Stratum: "low",
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
		Action: Action{Derive: entity.Obj{"signadot": entity.Obj{"feature": entity.Obj{
			"why": list(entity.Obj{"system": "signadot", "kind": "other", "id": "*"}),
		}}}.Payload()},
	})
	if err == nil {
		t.Fatal("a derive listed a higher level, which is how a cycle crosses a level")
	}
}
