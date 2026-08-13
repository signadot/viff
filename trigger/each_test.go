package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
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
  match: {system: git}
- name: findings
  level: 10
  match: {system: drift}
---
- name: fan
  stratum: reflected
  condition:
    subject: {system: git, kind: commit, value: {reviewed: pending}}
  action:
    spawn: {driver: scripted, prompt: "review $sha"}
    onDone:
      patch: {reviewed: done}
      derive:
        drift:
          finding: !each
            over: $data.findings
            key: $item.symbol
            value:
              symbol: $item.symbol
              was: $item.was
              about: $subject.id
`,
		`
- name: reflected
  level: 0
  match: {system: git}
- name: findings
  level: 10
  match: {system: drift}
---
- name: fan
  stratum: reflected
  condition:
    subject: {system: git, kind: commit, value: {reviewed: pending}}
  action:
    spawn: {driver: scripted, prompt: "review $sha"}
    onDone:
      patch: {reviewed: done}
      derive: {drift: {finding: !each {over: $data.findings, key: $item.symbol, value: {symbol: $item.symbol}}}}
`,
	} {
		trigs, _, strata, ok := parseBlock(src)
		if !ok {
			t.Fatalf("did not parse as a charter:\n%s", src)
		}
		eng := New(entitytest.Store(t), task.AutoResolver{})
		for _, s := range strata {
			if err := eng.addStratum(s, false); err != nil {
				t.Fatalf("stratum %q: %v", s.Name, err)
			}
		}
		for _, tr := range trigs {
			if err := eng.addTrigger(tr, false); err != nil {
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
		{"no key", `{over: $data.findings, value: {a: 1}}`, `missing "key"`},
		{"no over", `{key: $item.k, value: {a: 1}}`, `missing "over"`},
		{"unknown field", `{over: $data.f, key: $item.k, value: {a: 1}, sweep: true}`, `no field "sweep"`},
		{"over is text", `{over: "the $data.findings", key: $item.k, value: {a: 1}}`, "single reference"},
		{"key off the element", `{over: $data.f, key: $subject.id, value: {a: 1}}`, "rooted at $item"},
		{"value is scalar", `{over: $data.f, key: $item.k, value: 1}`, "must be an object"},
		{"value reads state", `{over: $data.f, key: $item.k, value: {a: $data.total}}`, "and nothing else"},
		{"value reads the occasion", `{over: $data.f, key: $item.k, value: {a: $subject.commit}}`, "and nothing else"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := `
- name: reflected
  level: 0
  match: {system: git}
- name: findings
  level: 10
  match: {system: drift}
---
- name: fan
  stratum: reflected
  condition:
    subject: {system: git, kind: commit, value: {reviewed: pending}}
  action:
    spawn: {driver: scripted, prompt: "x"}
    onDone:
      patch: {reviewed: done}
      derive: {drift: {finding: !each ` + c.derive + `}}
`
			trigs, _, strata, ok := parseBlock(src)
			if !ok {
				t.Fatalf("did not parse as a charter")
			}
			eng := New(entitytest.Store(t), task.AutoResolver{})
			for _, s := range strata {
				_ = eng.addStratum(s, false)
			}
			err := eng.addTrigger(trigs[0], false)
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
  match: {system: gh}
- name: sdlc
  level: 10
  match: {system: signadot}
---
- name: urgent
  stratum: reflected
  condition:
    subject: {system: gh, kind: issue, value: {label: urgent}}
  action:
    patch: {triaged: true}
    derive: {signadot: {feature: {priority: urgent, about: $subject.id}}}
`
	trigs, _, strata, ok := parseBlock(src)
	if !ok {
		t.Fatal("did not parse as a charter")
	}
	eng := New(entitytest.Store(t), task.AutoResolver{})
	for _, s := range strata {
		if err := eng.addStratum(s, false); err != nil {
			t.Fatalf("stratum %q: %v", s.Name, err)
		}
	}
	if err := eng.addTrigger(trigs[0], false); err != nil {
		t.Fatalf("does not install: %v", err)
	}
	ds := trigs[0].Action.allDerives()
	if len(ds) != 1 || ds[0].Each != nil || ds[0].Payload == nil {
		t.Fatalf("plain derive read as %+v, want a payload and no Each", ds)
	}
	if ds[0].addr() != "signadot:feature" {
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
  match: {system: drift}
---
- name: fan
  stratum: findings
  condition:
    subject: {system: drift, kind: head, value: {reviewed: pending}}
  action:
    spawn: {driver: scripted, prompt: "x"}
    onDone:
      patch: {reviewed: done}
      derive: {drift: {finding: !each {over: $data.f, key: $item.k, value: {a: $item.k}}}}
`
	trigs, _, strata, ok := parseBlock(src)
	if !ok {
		t.Fatal("did not parse as a charter")
	}
	eng := New(entitytest.Store(t), task.AutoResolver{})
	for _, s := range strata {
		if err := eng.addStratum(s, false); err != nil {
			t.Fatalf("stratum %q: %v", s.Name, err)
		}
	}
	err := eng.addTrigger(trigs[0], false)
	if err == nil {
		t.Fatal("a fan-out into its own stratum installed; a derive must land strictly above")
	}
	if !strings.Contains(err.Error(), "drift:finding") {
		t.Errorf("refused with %q, want it to name the derive target", err)
	}
}

// And a firing that reaches one FAILS, loudly, rather than deriving nothing. A charter the
// engine accepts and then silently ignores is indistinguishable from a condition that never
// matched, which is the failure this step is shaped to avoid while step 3 is unwritten.
func TestEachDoesNotFireYet(t *testing.T) {
	src := `
- name: reflected
  level: 0
  match: {system: git}
- name: findings
  level: 10
  match: {system: drift}
---
- name: fan
  stratum: reflected
  condition:
    subject: {system: git, kind: commit, value: {reviewed: pending}}
  action:
    patch: {reviewed: done}
    derive: {drift: {finding: !each {over: $subject.value.f, key: $item.k, value: {a: $item.k}}}}
`
	trigs, _, strata, ok := parseBlock(src)
	if !ok {
		t.Fatal("did not parse as a charter")
	}
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	for _, s := range strata {
		if err := eng.addStratum(s, false); err != nil {
			t.Fatalf("stratum %q: %v", s.Name, err)
		}
	}
	if err := eng.addTrigger(trigs[0], false); err != nil {
		t.Fatalf("does not install: %v", err)
	}
	subj := entity.Ref{System: "git", Kind: "commit", ID: "verse/abc"}
	_, err := trigs[0].Action.applyOne("fan", occasion{ev: entity.Event{Ref: subj}}, subj, eng, fromSubject)
	if err == nil {
		t.Fatal("the firing succeeded; an unimplemented fan-out must fail it")
	}
	if !strings.Contains(err.Error(), "not implemented") || !strings.Contains(err.Error(), "drift:finding") {
		t.Errorf("failed with %q, want it to say what is unimplemented and where", err)
	}
	// And nothing was derived — the refusal is before the write, not after it.
	if es := store.List(func(r entity.Ref) bool { return r.System == "drift" }); len(es) != 0 {
		t.Errorf("derived %d entities from a fan-out that does not fire", len(es))
	}
}
