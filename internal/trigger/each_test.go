package trigger

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/codec"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/launch"
	"go.viff.dev/viff/pkg/task"
)

// A fan-out installs, in both layouts. The layout half is not decoration: a tag is a
// dot-separated chain and the parser prepends what it saw, so the same charter carries `!each`
// written in block style and `!bracket.each` in flow style. Comparing Node.Tag as a string
// would have accepted one and read the other as a payload with three odd fields.
func TestEachInstallsInEitherLayout(t *testing.T) {
	for _, src := range []string{
		`
- name: reflected
  level: 0
  match: {path: "git"}
- name: findings
  level: 10
  match: {path: "drift"}
---
- name: fan
  stratum: reflected
  condition:
    subject: {path: "git.commit.*", value: {reviewed: pending}}
  action:
    spawn: {agent: {name: viff}, prompt: "review $[sha]"}
    onDone:
      patch: {reviewed: done}
      derive:
      - path: "drift.finding"
        value: !each
          over: .[data.findings]
          key: .[item.symbol]
          value:
            symbol: .[item.symbol]
            was: .[item.was]
            about: .[subject.name]
`,
		`
- name: reflected
  level: 0
  match: {path: "git"}
- name: findings
  level: 10
  match: {path: "drift"}
---
- name: fan
  stratum: reflected
  condition:
    subject: {path: "git.commit.*", value: {reviewed: pending}}
  action:
    spawn: {agent: {name: viff}, prompt: "review $[sha]"}
    onDone:
      patch: {reviewed: done}
      derive: [{path: "drift.finding", value: !each {over: .[data.findings], key: .[item.symbol], value: {symbol: .[item.symbol]}}}]
`,
	} {
		trigs, _, strata, _, ok := parseBlock(src)
		if !ok {
			t.Fatalf("did not parse as a charter:\n%s", src)
		}
		eng := New(entitytest.Store(t), task.AutoResolver{})
		for _, s := range strata {
			if err := eng.addStratum(s); err != nil {
				t.Fatalf("stratum %q: %v", s.Name, err)
			}
		}
		for _, tr := range trigs {
			if err := eng.addTrigger(tr, "", RunnersChecked); err != nil {
				t.Fatalf("rule %q does not install: %v", tr.Name, err)
			}
			// …and the target was READ as a fan-out rather than as a payload.
			ds := tr.Action.allDerives()
			if len(ds) != 1 || ds[0].Each == nil {
				t.Fatalf("derive read as %+v, want one target carrying an Each", ds)
			}
			if ds[0].Payload != nil {
				t.Error("a fan-out target also carries a payload; the two are exclusive")
			}
		}
	}
}

// Every way of getting it wrong is refused at install, by name and by rule.
func TestEachRefusals(t *testing.T) {
	cases := []struct{ name, derive, want string }{
		{"no address", `{over: .[data.findings], value: {a: 1}}`, "needs a `key`"},
		{"both key and id", `{over: .[data.f], key: .[item.k], id: .[item.k], value: {a: 1}}`, "answered twice"},
		{"id off the element", `{over: .[data.f], id: .[subject.name], value: {a: 1}}`, "out of THE ELEMENT"},
		{"no over", `{key: .[item.k], value: {a: 1}}`, `missing "over"`},
		{"unknown field", `{over: .[data.f], key: .[item.k], value: {a: 1}, sweep: true}`, `no field "sweep"`},
		{"over is text", `{over: "the $[data.findings]", key: .[item.k], value: {a: 1}}`, "one expression and nothing else"},
		{"key off the element", `{over: .[data.f], key: .[subject.name], value: {a: 1}}`, "out of THE ELEMENT"},
		{"key reads nothing", `{over: .[data.f], key: '.["fixed"]', value: {a: 1}}`, "reads nothing of the element"},
		{"key is arithmetic", `{over: .[data.f], key: '.[1 + 1]', value: {a: 1}}`, "reads nothing of the element"},
		{"value is scalar", `{over: .[data.f], key: .[item.k], value: 1}`, "must be an object"},
		{"value reads state", `{over: .[data.f], key: .[item.k], value: {a: .[data.total]}}`, "and nothing else"},
		{"value reads the occasion", `{over: .[data.f], key: .[item.k], value: {a: .[subject.commit]}}`, "and nothing else"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := `
- name: reflected
  level: 0
  match: {path: "git"}
- name: findings
  level: 10
  match: {path: "drift"}
---
- name: fan
  stratum: reflected
  condition:
    subject: {path: "git.commit.*", value: {reviewed: pending}}
  action:
    spawn: {agent: {name: viff}, prompt: "x"}
    onDone:
      patch: {reviewed: done}
      derive: [{path: "drift.finding", value: !each ` + c.derive + `}]
`
			trigs, _, strata, _, ok := parseBlock(src)
			if !ok {
				t.Fatalf("did not parse as a charter")
			}
			eng := New(entitytest.Store(t), task.AutoResolver{})
			for _, s := range strata {
				_ = eng.addStratum(s)
			}
			err := eng.addTrigger(trigs[0], "", RunnersChecked)
			if err == nil {
				t.Fatal("installed, and should not have")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("refused with %q, want it to mention %q", err, c.want)
			}
		})
	}
}

// A plain derive is untouched by the fan-out reader: no tag, so it is a payload, and the Each
// half stays nil. The two are exclusive and this is the side that already worked.
func TestPlainDeriveIsUnchanged(t *testing.T) {
	src := `
- name: reflected
  level: 0
  match: {path: "gh"}
- name: sdlc
  level: 10
  match: {path: "signadot"}
---
- name: urgent
  stratum: reflected
  condition:
    subject: {path: "gh.issue.*", value: {label: urgent}}
  action:
    patch: {triaged: true}
    derive: [{path: "signadot.feature", value: {priority: urgent, about: .[subject.name]}}]
`
	trigs, _, strata, _, ok := parseBlock(src)
	if !ok {
		t.Fatal("did not parse as a charter")
	}
	eng := New(entitytest.Store(t), task.AutoResolver{})
	for _, s := range strata {
		if err := eng.addStratum(s); err != nil {
			t.Fatalf("stratum %q: %v", s.Name, err)
		}
	}
	if err := eng.addTrigger(trigs[0], "", RunnersChecked); err != nil {
		t.Fatalf("does not install: %v", err)
	}
	ds := trigs[0].Action.allDerives()
	if len(ds) != 1 || ds[0].Each != nil || ds[0].Payload == nil {
		t.Fatalf("plain derive read as %+v, want a payload and no Each", ds)
	}
	if ds[0].addr() != "signadot.feature" {
		t.Errorf("address = %q", ds[0].addr())
	}
}

// The level check sees a fan-out, including one inside a completion hook. Losing that would
// be the invisible bug in this step: a whole class of cross-level write that installs because
// allDerives stopped reporting it.
func TestEachInAHookFacesTheLevelCheck(t *testing.T) {
	src := `
- name: findings
  level: 10
  match: {path: "drift"}
---
- name: fan
  stratum: findings
  condition:
    subject: {path: "drift.head.*", value: {reviewed: pending}}
  action:
    spawn: {agent: {name: viff}, prompt: "x"}
    onDone:
      patch: {reviewed: done}
      derive: [{path: "drift.finding", value: !each {over: .[data.f], key: .[item.k], value: {a: .[item.k]}}}]
`
	trigs, _, strata, _, ok := parseBlock(src)
	if !ok {
		t.Fatal("did not parse as a charter")
	}
	eng := New(entitytest.Store(t), task.AutoResolver{})
	for _, s := range strata {
		if err := eng.addStratum(s); err != nil {
			t.Fatalf("stratum %q: %v", s.Name, err)
		}
	}
	err := eng.addTrigger(trigs[0], "", RunnersChecked)
	if err == nil {
		t.Fatal("a fan-out into its own stratum installed; a derive must land strictly above")
	}
	if !strings.Contains(err.Error(), "drift.finding") {
		t.Errorf("refused with %q, want it to name the derive target", err)
	}
}

// A fan-out over the subject's own state derives one entity per element, addressed by the key
// read out of that element.
func TestEachDerivesOnePerElement(t *testing.T) {
	src := `
- name: reflected
  level: 0
  match: {path: "git"}
- name: findings
  level: 10
  match: {path: "drift"}
---
- name: fan
  stratum: reflected
  condition:
    subject: {path: "git.commit.*", value: {reviewed: pending}}
  action:
    patch: {reviewed: done}
    derive: [{path: "drift.finding", value: !each {over: .[subject.value.f], key: .[item.sym], value: {sym: .[item.sym], was: .[item.was]}}}]
`
	trigs, _, strata, _, ok := parseBlock(src)
	if !ok {
		t.Fatal("did not parse as a charter")
	}
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	for _, s := range strata {
		if err := eng.addStratum(s); err != nil {
			t.Fatalf("stratum %q: %v", s.Name, err)
		}
	}
	if err := eng.addTrigger(trigs[0], "", RunnersChecked); err != nil {
		t.Fatalf("does not install: %v", err)
	}

	subj := entity.RefAt("git", "commit", "viff/abc")
	fire := func(t *testing.T, findings string) error {
		t.Helper()
		payload, err := codec.ParseNode([]byte(`{reviewed: pending, f: `+findings+`}`), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Write(entity.Write{At: subj.At(), Value: payload}); err != nil {
			t.Fatal(err)
		}
		oc := occasion{commit: entity.Commit{Rev: store.Rev()}, snap: &snapCache{}, ev: entity.Event{Ref: subj, To: payload}}
		_, err = trigs[0].Action.applyOne("fan", oc, subj, eng, fromSubject)
		return err
	}
	// AT THE DEPTH A FAN-OUT LANDS: the target `drift.finding`, the rule that derived, the
	// segments of the subject, and the element's key. A listing says how deep it reads now that
	// a derived entity is as deep as what it is about.
	drift := func() []entity.Entity {
		return store.ListAt([]string{"drift", "finding"}, 2+1+len(subj.Segs())+1, nil)
	}

	// Twelve findings, twelve entities.
	var elems []string
	for i := 0; i < 12; i++ {
		elems = append(elems, fmt.Sprintf(`{sym: "entity.f%d", was: stale}`, i))
	}
	if err := fire(t, "["+strings.Join(elems, " ")+"]"); err != nil {
		t.Fatalf("firing failed: %v", err)
	}
	if got := drift(); len(got) != 12 {
		t.Fatalf("derived %d entities, want 12", len(got))
	}

	// Each carries what the element said, and the engine's linkage.
	for _, e := range drift() {
		obj := entity.ToObj(e.Payload)
		if obj["sym"] == nil || obj["was"] != "stale" {
			t.Errorf("%s carries %v", e.Ref, obj)
		}
		// The ADDRESS carries (rule, subject) — a segment each, verbatim, rather than one id with
		// them flattened into it. Flattening makes `viff/abc` into `viff_abc`; a segment holds
		// the slash, so the subject reads back as itself.
		want := append([]string{"drift", "finding"}, deriveSegs("fan", subj)...)
		if got := e.Ref.Segs(); len(got) != len(want)+1 {
			t.Errorf("%s is %d segments, want the target, the rule, the subject and a key", e.Ref, len(got))
			continue
		}
		for i, w := range want {
			if e.Ref.Segs()[i] != w {
				t.Errorf("%s: segment %d = %q, want %q", e.Ref, i, e.Ref.Segs()[i], w)
			}
		}
	}

	// THE INVARIANT the whole level ordering rests on: the same answer re-fired writes the same
	// bytes to the same addresses, and the store ends it — the write happens (its CAS
	// precondition is "still what I read", which an identical value satisfies) and raises NO
	// CHANGE, so nothing downstream is woken and a rule may read a higher level without the
	// read becoming a cycle. It is the whole reason `key` is declared rather than positional.
	//
	// Measured as changes rather than revisions, because a revision moves on any write and
	// would report a no-op as a difference.
	mark := store.Rev()
	if err := fire(t, "["+strings.Join(elems, " ")+"]"); err != nil {
		t.Fatalf("re-firing failed: %v", err)
	}
	if got := drift(); len(got) != 12 {
		t.Errorf("re-firing the same answer derived %d entities, want the same 12", len(got))
	}
	commits, err := store.Since(mark, entity.PrefixPattern(nil, 3), entity.PrefixPattern(nil, 4))
	if err != nil {
		t.Fatalf("the store could not answer what changed: %v", err)
	}
	for _, c := range commits {
		for _, ch := range c.Changes {
			if ch.Ref.Under("drift") {
				t.Errorf("re-deriving the same content changed %s — the no-op the ordering "+
					"rests on did not hold", ch.Ref)
			}
		}
	}

	// A thirteenth adds one and touches none of the others.
	if err := fire(t, "["+strings.Join(append(elems, `{sym: "entity.f12", was: stale}`), " ")+"]"); err != nil {
		t.Fatalf("firing failed: %v", err)
	}
	if got := drift(); len(got) != 13 {
		t.Errorf("derived %d entities, want 13", len(got))
	}
}

// An element with no key fails the FIRING, naming which element. Deriving the others and
// recording the refusal would leave a ledger that is quietly short, and short is the one
// failure a reader cannot see.
func TestEachRefusesAnElementWithNoKey(t *testing.T) {
	src := `
- name: reflected
  level: 0
  match: {path: "git"}
- name: findings
  level: 10
  match: {path: "drift"}
---
- name: fan
  stratum: reflected
  condition:
    subject: {path: "git.commit.*", value: {reviewed: pending}}
  action:
    patch: {reviewed: done}
    derive: [{path: "drift.finding", value: !each {over: .[subject.value.f], key: .[item.sym], value: {was: .[item.was]}}}]
`
	trigs, _, strata, _, ok := parseBlock(src)
	if !ok {
		t.Fatal("did not parse as a charter")
	}
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	for _, s := range strata {
		if err := eng.addStratum(s); err != nil {
			t.Fatalf("stratum %q: %v", s.Name, err)
		}
	}
	if err := eng.addTrigger(trigs[0], "", RunnersChecked); err != nil {
		t.Fatalf("does not install: %v", err)
	}
	subj := entity.RefAt("git", "commit", "viff/abc")
	payload, err := codec.ParseNode([]byte(`{reviewed: pending, f: [{sym: "entity.a", was: x} {was: y} {sym: "entity.c", was: z}]}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Write(entity.Write{At: subj.At(), Value: payload}); err != nil {
		t.Fatal(err)
	}
	oc := occasion{commit: entity.Commit{Rev: store.Rev()}, snap: &snapCache{}, ev: entity.Event{Ref: subj, To: payload}}
	_, err = trigs[0].Action.applyOne("fan", oc, subj, eng, fromSubject)
	if err == nil {
		t.Fatal("the firing succeeded; an element with no key must fail it")
	}
	if !strings.Contains(err.Error(), "element 1") {
		t.Errorf("failed with %q, want it to name which element", err)
	}
}

// TWO KEYS THAT WOULD FLATTEN ALIKE ARE SIMPLY TWO SEGMENTS. A key is a segment — a slash, a
// space, a dot are all ordinary characters in one — so there is nothing to flatten and nothing to
// disambiguate with a hash.
//
// The failure this guards against is worth keeping named: losing one member of a fan-out to a
// collision reads as a shorter ledger and nothing else.
func TestEachSegsSeparatesKeysThatWouldFlattenAlike(t *testing.T) {
	subj := entity.RefAt("drift", "head", "x")
	seen := map[string]string{}
	for _, key := range []string{
		"entity.nodeToPayload",
		"source/github.Reflect",
		"source_github.Reflect", // the same segment as the line above, ONCE flattened
		"entity.(*LogdStore).Get",
		"a b",
		"a_b",
	} {
		at := entity.RefAt(eachSegs("fan", subj, key)...).String()
		if prev, dup := seen[at]; dup {
			t.Errorf("keys %q and %q share the address %q", prev, key, at)
		}
		seen[at] = key
	}
	// And the key is carried WHOLE: the address of a derivation says what it is about, where a
	// flattened id could only carry a lossy trace of it.
	segs := eachSegs("fan", subj, "source/github.Reflect")
	if got := segs[len(segs)-1]; got != "source/github.Reflect" {
		t.Errorf("the key segment = %q, want it verbatim", got)
	}
	if got := segs[0]; got != "fan" {
		t.Errorf("the chain segment = %q, want the rule", got)
	}
}

// The motivating path: a completion hook fanning out over the TASK's answer.
//
// This is what the construct is for — an agent reports twelve findings and each becomes a fact
// with an address — and it is a different root from the trigger case above: `over` reads the
// finished task's payload (`data`), while each element's `value` still resolves against the
// subject and `item` alone.
func TestEachOverATaskAnswer(t *testing.T) {
	src := `
- name: reflected
  level: 0
  match: {path: "git"}
- name: findings
  level: 10
  match: {path: "drift"}
---
- name: review
  stratum: reflected
  condition:
    subject: {path: "git.commit.*", value: {reviewed: pending}}
  action:
    spawn: {agent: {name: viff}, prompt: "review $[sha]"}
    onDone:
      patch: {reviewed: done}
      derive:
      - path: "drift.finding"
        value: !each
          over: .[data.findings]
          key: .[item.symbol]
          value:
            symbol: .[item.symbol]
            says: .[item.says]
            about: .[subject.name]
`
	trigs, _, strata, _, ok := parseBlock(src)
	if !ok {
		t.Fatal("did not parse as a charter")
	}
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	for _, s := range strata {
		if err := eng.addStratum(s); err != nil {
			t.Fatalf("stratum %q: %v", s.Name, err)
		}
	}
	if err := eng.addTrigger(trigs[0], "", RunnersChecked); err != nil {
		t.Fatalf("does not install: %v", err)
	}

	// What the engine synthesises when a task ends: status, result, and the parsed answer.
	answer, err := codec.ParseNode([]byte(`{status: done, data: {findings: [`+
		`{symbol: "entity.nodeToPayload", says: "logd's tombstones"} `+
		`{symbol: "source/github.Reflect", says: "the log spells it"}]}}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	subj := entity.RefAt("git", "commit", "viff/abc")
	hook := trigs[0].Action.OnDone
	if _, err := hook.applyOne("review.onDone", occasion{ev: entity.Event{Ref: subj, To: answer}}, subj, eng, fromCause); err != nil {
		t.Fatalf("the hook failed: %v", err)
	}

	// The target, the hook CHAIN as one segment, the subject, and the element's key.
	got := store.ListAt([]string{"drift", "finding"}, 2+1+len(subj.Segs())+1, nil)
	if len(got) != 2 {
		t.Fatalf("derived %d entities from a two-finding answer", len(got))
	}
	for _, e := range got {
		obj := entity.ToObj(e.Payload)
		if obj["about"] != "viff/abc" {
			t.Errorf("%s lost the subject it is about: %v", e.Ref, obj)
		}
		if obj["says"] == nil {
			t.Errorf("%s lost what the element said: %v", e.Ref, obj)
		}
	}
	// A key that no flattening could carry is its own entity, and carries its symbol VERBATIM: it
	// is the last segment, and a segment holds a `/` as readily as anything else.
	var keys []string
	for _, e := range got {
		segs := e.Ref.Segs()
		keys = append(keys, segs[len(segs)-1])
	}
	sort.Strings(keys)
	if keys[1] != "source/github.Reflect" {
		t.Errorf("keys = %v, want the symbol whole in the address", keys)
	}
}

// The seam, both directions, in the shape the charter uses it: facts a fan-out wrote become the
// next prompt's input.
//
// A ledger of findings lives as entities, so a rule reads the OUTSTANDING ones onto its subject
// with `!list-paths` — a patch may read upward freely; only a path read inside a derive is
// confined to its own level — and the spawn that fires next binds them as tony. Before
// composites rendered as tony this arrived as Go's `map[…]`, which is why the two halves are
// one test: either alone is half a loop.
func TestLedgerReachesThePrompt(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	// Two findings: one still open, one a person marked resolved by hand.
	for _, f := range []struct{ id, payload string }{
		{"a", `{symbol: "entity.nodeToPayload", status: open, says: "claims logd spells tombstones"}`},
		{"b", `{symbol: "source/github.Reflect", status: resolved, says: "fixed in aaebbc0"}`},
	} {
		p, err := codec.ParseNode([]byte(f.payload), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		ref := entity.RefAt("drift", "finding", f.id)
		if _, _, err := store.Write(entity.Write{At: ref.At(), Value: p}); err != nil {
			t.Fatal(err)
		}
	}

	src := `
- name: drift
  level: 10
  match: {path: "drift.head"}
- name: ledger
  level: 20
  match: {path: "drift.finding"}
---
- name: gather
  stratum: drift
  condition:
    subject: {path: "drift.head.*", value: {reviewed: pending}}
  action:
    patch:
      reviewed: gathered
      outstanding: !list-paths {at: "drift.finding.*", value: {status: open}}
`
	trigs, _, strata, _, ok := parseBlock(src)
	if !ok {
		t.Fatal("did not parse as a charter")
	}
	for _, s := range strata {
		if err := eng.addStratum(s); err != nil {
			t.Fatalf("stratum %q: %v", s.Name, err)
		}
	}
	if err := eng.addTrigger(trigs[0], "", RunnersChecked); err != nil {
		t.Fatalf("gather does not install: %v", err)
	}

	subj := entity.RefAt("drift", "head", "viff")
	head, _ := codec.ParseNode([]byte(`{sha: abc, reviewed: pending}`), codec.Tony)
	if _, _, err := store.Write(entity.Write{At: subj.At(), Value: head}); err != nil {
		t.Fatal(err)
	}
	oc := occasion{commit: entity.Commit{Rev: store.Rev()}, snap: &snapCache{}, ev: entity.Event{Ref: subj, To: head}}
	if _, err := trigs[0].Action.applyOne("gather", oc, subj, eng, fromSubject); err != nil {
		t.Fatalf("gather failed: %v", err)
	}

	got, _, err := store.Get(subj)
	if err != nil {
		t.Fatal(err)
	}
	// What the NEXT rule's spawn would interpolate.
	outstanding := bound(t, launch.Spec{Prompt: "$[outstanding]"}, bindingFor(got.Payload, subj, got.Payload)).Prompt
	if !strings.Contains(outstanding, "entity.nodeToPayload") {
		t.Errorf("$[outstanding] does not carry the open finding:\n%s", outstanding)
	}
	if strings.Contains(outstanding, "source/github.Reflect") {
		t.Errorf("$[outstanding] carries a resolved finding — the human override does not hold:\n%s",
			outstanding)
	}
	// And it is a document, not Go's formatting of one: a prompt a person can read, and an
	// answer shaped like what it was shown.
	if strings.Contains(outstanding, "map[") {
		t.Errorf("$[outstanding] is Go's map formatting:\n%s", outstanding)
	}
	if _, err := codec.ParseNode([]byte(outstanding), codec.Tony); err != nil {
		t.Errorf("$[outstanding] does not parse as tony: %v\n%s", err, outstanding)
	}
}
