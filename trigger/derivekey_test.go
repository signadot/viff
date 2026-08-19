package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// ONE ENTITY PER VALUE, which is what a plain derive cannot say and what a question asked more
// than once needs.
//
// A derive's address is a function of (rule, subject), and a gate's id is a function of (trigger,
// gate, subject) — so a rule deriving a batch from a moving tip writes ONE entity, and the second
// batch inherits the first one's answer without anybody being asked. That is the merge train in
// docs/working/comment-drift.tony: main moves, the proposals change, and the motion that decided
// the previous batch is already settled at the address the new batch lands on.
//
// `key:` is the fix, and it is `!each`'s key without the fan-out: the address carries the value,
// so a new tip is a new subject and therefore a new question.
func TestAKeyedDeriveIsOneEntityPerValue(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	for _, st := range []Stratum{
		{Name: "reflected", Level: 0, Match: []Match{{"system": "git"}}},
		{Name: "batches", Level: 10, Match: []Match{mustPathMatch(t, `{path: [demo, batch]}`)}},
	} {
		if err := eng.AddStratum(st); err != nil {
			t.Fatalf("stratum %q: %v", st.Name, err)
		}
	}
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: cut
  stratum: reflected
  condition:
    subject: git:head:verse
  action:
    derive:
    - path: [demo, batch]
      key: $subject.value.sha
      value: {sha: $subject.value.sha}
`)); err != nil {
		t.Fatalf("the keyed rule did not install: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	head := entity.RefAt("git", "head", "verse")
	entitytest.Set(t, store, head, entity.Obj{"sha": "aaa111"}.Payload())
	waitDeep(t, store, []string{"demo", "batch"}, 3)

	// THE TIP MOVES, and the point is that this is a SECOND entity rather than a rewrite of the
	// first. Unkeyed, the two batches are one address: whatever was decided about the first is
	// what stands when the second arrives.
	entitytest.Set(t, store, head, entity.Obj{"sha": "bbb222"}.Payload())
	var got []entity.Entity
	for i := 0; i < 100; i++ {
		if got = store.ListAt([]string{"demo", "batch"}, 3, nil); len(got) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) != 2 {
		t.Fatalf("the second tip did not get its own batch: %d stand under demo.batch", len(got))
	}

	seen := map[string]bool{}
	for _, e := range got {
		sha, _ := entity.ToObj(e.Payload)["sha"].(string)
		seen[sha] = true
		if !strings.Contains(e.Ref.String(), sha) {
			t.Errorf("batch %s does not carry its key in its address", e.Ref)
		}
	}
	if !seen["aaa111"] || !seen["bbb222"] {
		t.Errorf("the two tips did not each get a batch: %v", seen)
	}
}

// A KEY IS AN ADDRESS, so the two things that make one predictable are checked at install rather
// than discovered when a firing writes somewhere nobody can find.
func TestAKeyedDeriveRefusesAKeyThatIsNotAnAddress(t *testing.T) {
	for _, tc := range []struct {
		name, key, says string
	}{
		{"text with a reference in it", `"batch-$subject.value.sha"`, "single reference"},
		// The same refusal a derive's CONTENT gets, for the same reason: a key that is a
		// function of when the rule fired writes a new entity on every lap.
		{"the occasion of the firing", `$subject.commit`, "SUBJECT and nothing else"},
		{"another entity's state", `$data.sha`, "SUBJECT and nothing else"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := New(entitytest.Store(t), task.AutoResolver{})
			if err := eng.AddStratum(Stratum{Name: "reflected", Level: 0,
				Match: []Match{{"system": "git"}}}); err != nil {
				t.Fatal(err)
			}
			if err := eng.AddStratum(Stratum{Name: "batches", Level: 10,
				Match: []Match{mustPathMatch(t, `{path: [demo, batch]}`)}}); err != nil {
				t.Fatal(err)
			}
			err := eng.AddTrigger(mustParseCharter(t, `
- name: cut
  stratum: reflected
  condition:
    subject: git:head:verse
  action:
    derive:
    - path: [demo, batch]
      key: `+tc.key+`
      value: {noted: true}
`))
			if err == nil {
				t.Fatalf("key %s installed", tc.key)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say why: %v", err)
			}
			if !strings.Contains(err.Error(), "demo.batch") {
				t.Errorf("the refusal does not name the derive it refused: %v", err)
			}
		})
	}
}

// AND `id:` IS THE SAME KEY WITHOUT THE COMPOSITION, which is the reader's half of it.
//
// A keyed id is `cut@git:head:verse@aaa111` — a function of (rule, subject, key), and nothing
// in the charter grammar composes one, so every rule that wants that entity has to DESCRIBE it
// (a `"*"` and a predicate on some field it happens to know) rather than name it. `id:` says
// the value alone identifies the record, so the address is `demo.batch.aaa111` and a reader
// spells it. What the author takes on is the uniqueness the prefix used to guarantee.
func TestANamedDeriveIsAddressedByItsValue(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	for _, st := range []Stratum{
		{Name: "reflected", Level: 0, Match: []Match{{"system": "git"}}},
		{Name: "batches", Level: 10, Match: []Match{mustPathMatch(t, `{path: [demo, batch]}`)}},
	} {
		if err := eng.AddStratum(st); err != nil {
			t.Fatalf("stratum %q: %v", st.Name, err)
		}
	}
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: cut
  stratum: reflected
  condition:
    subject: git:head:verse
  action:
    derive:
    - path: [demo, batch]
      id: $subject.value.sha
      value: {sha: $subject.value.sha}
`)); err != nil {
		t.Fatalf("the named rule did not install: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "aaa111"}.Payload())
	want := entity.RefAt("demo", "batch", "aaa111")
	for i := 0; i < 100; i++ {
		if _, ok, _ := store.Get(want); ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	var have []string
	for _, e := range store.ListAt([]string{"demo", "batch"}, 3, nil) {
		have = append(have, e.Ref.String())
	}
	t.Fatalf("nothing stands at %s, which is the address the charter can spell; have %v", want, have)
}

// The two together are one question answered twice, and which answer stands would decide
// whether two subjects sharing a value are one record or two. Refused rather than picked.
func TestADeriveRefusesBothKeyAndID(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddStratum(Stratum{Name: "reflected", Level: 0, Match: []Match{{"system": "git"}}}); err != nil {
		t.Fatal(err)
	}
	err := eng.AddTrigger(mustParseCharter(t, `
- name: cut
  stratum: reflected
  condition:
    subject: git:head:verse
  action:
    derive:
    - path: [git, batch]
      key: $subject.value.sha
      id: $subject.value.sha
      value: {sha: $subject.value.sha}
`))
	if err == nil {
		t.Fatal("a derive with both `key` and `id` installed")
	}
	if !strings.Contains(err.Error(), "key") || !strings.Contains(err.Error(), "id") {
		t.Fatalf("the refusal does not name both: %v", err)
	}
}

// A FAN-OUT TAKES `id:` TOO, and the asymmetry it removes is the point: keyed, every element
// lands under (rule, subject, key), so a fan-out can only ever create its OWN copies. Named, it
// lands at the place plus the value — which is how one rule's fan-out reaches the entities
// another rule names, and the only way to say "mark every one of those that is already there".
func TestANamedFanOutLandsOnAnotherRulesEntities(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	for _, st := range []Stratum{
		{Name: "reflected", Level: 0, Match: []Match{{"system": "git"}}},
		{Name: "batches", Level: 10, Match: []Match{mustPathMatch(t, `{path: [demo, batch]}`)}},
	} {
		if err := eng.AddStratum(st); err != nil {
			t.Fatalf("stratum %q: %v", st.Name, err)
		}
	}
	// One rule names its record by the sha; another fans out over a list and marks the same
	// addresses. A derive MERGES, so the mark lands beside the facts rather than over them.
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: cut
  stratum: reflected
  condition:
    subject: git:head:verse
  action:
    derive:
    - path: [demo, batch]
      id: $subject.value.sha
      value: {sha: $subject.value.sha}
`)); err != nil {
		t.Fatalf("the naming rule did not install: %v", err)
	}
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: mark
  stratum: reflected
  condition:
    subject: git:mark:verse
  action:
    derive:
    - path: [demo, batch]
      value: !each
        over: $subject.value.shas
        id: $item
        value: {marked: true}
`)); err != nil {
		t.Fatalf("the marking rule did not install: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "aaa111"}.Payload())
	want := entity.RefAt("demo", "batch", "aaa111")
	for i := 0; i < 100; i++ {
		if _, ok, _ := store.Get(want); ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	entitytest.Set(t, store, entity.RefAt("git", "mark", "verse"),
		entity.Obj{"shas": []any{"aaa111", "bbb222"}}.Payload())
	for i := 0; i < 100; i++ {
		if e, ok, _ := store.Get(want); ok && entity.ToObj(e.Payload)["marked"] == true {
			// …and the fan-out did not overwrite what the other rule put there.
			if sha := entity.ToObj(e.Payload)["sha"]; sha != "aaa111" {
				t.Fatalf("the mark replaced the record instead of merging into it: %+v", e.Payload)
			}
			// …and an element naming an address nobody had made one anyway.
			if _, ok, _ := store.Get(entity.RefAt("demo", "batch", "bbb222")); !ok {
				t.Error("the element for bbb222 landed nowhere")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e, _, _ := store.Get(want)
	t.Fatalf("%s was never marked: %+v", want, e.Payload)
}
