package trigger

import (
	"context"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// proposalCharter is the sweep, as a charter would write it: a proposal is stale exactly when the
// commit its branch was cut from is no longer the tip of main. See
// docs/working/merge-gate-residual.md — the predicate is `base != <main's tip>`, and everything
// about it was sayable except the comparison.
const proposalCharter = `
- name: proposal-stale
  condition:
    subject:
      system: merge
      kind: proposal
      value: !let
        let: [{tip: !get-path {at: [git, head, verse], path: sha}}]
        in:  {state: open, base: !not .[tip]}
  action:
    patch: {state: stale}
`

func mustParseCharter(t *testing.T, src string) Trigger {
	t.Helper()
	trigs, _, _, _, ok := parseBlock(src)
	if !ok || len(trigs) == 0 {
		t.Fatalf("did not parse as a charter:\n%s", src)
	}
	return trigs[0]
}

func field(t *testing.T, s entity.Store, ref entity.Ref, key string) any {
	t.Helper()
	e, _, _ := s.Get(ref)
	return entity.ToObj(e.Payload)[key]
}

func setPayload(t *testing.T, store entity.Store, ref entity.Ref, src string) {
	t.Helper()
	payload, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	entitytest.Set(t, store, ref, payload)
}

// A condition can compare its subject against another entity's field, and the rule fires WHEN
// THAT OTHER ENTITY MOVES.
//
// Both halves matter and they are separate. The comparison is what `!let` plus a read makes
// sayable; being re-evaluated when main moves is what makes the rule mean anything, and it is
// the read joining Trigger.reads that buys it — otherwise the commit is skipped as touching
// nothing this trigger reads, and the proposals sit open forever.
func TestAConditionComparesAgainstAnotherEntity(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	head := entity.RefAt("git", "head", "verse")
	fresh := entity.RefAt("merge", "proposal", "fresh")
	old := entity.RefAt("merge", "proposal", "old")

	setPayload(t, store, head, `{sha: aaa111}`)
	setPayload(t, store, fresh, `{state: open, base: aaa111}`) // cut from the current tip
	setPayload(t, store, old, `{state: open, base: 000zzz}`)   // cut from something older

	if err := eng.AddTrigger(mustParseCharter(t, proposalCharter)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// A commit to consider the rule at: the engine is level-triggered per commit, and writing
	// the entities before Start gave it none. Touching one proposal evaluates the rule over
	// EVERY open proposal, which is the fan-out half and is not what this test is about.
	setPayload(t, store, old, `{state: open, base: 000zzz, touched: 1}`)

	// The one cut from an older commit is stale; the one cut from the tip is not.
	waitField(t, store, old, "state", "stale")
	if got := field(t, store, fresh, "state"); got != "open" {
		t.Fatalf("the proposal cut from the current tip is %q — the comparison is not comparing", got)
	}

	// Main moves. Nothing about the fresh proposal changed, and it must go stale anyway: this
	// is the commit that would be skipped if the read were not part of what the trigger reads.
	setPayload(t, store, head, `{sha: bbb222}`)
	waitField(t, store, fresh, "state", "stale")
}

// The read is answered from the SAME snapshot the condition is evaluated against, so a rule
// reading an entity that is not there yet selects nothing rather than failing the firing.
func TestAConditionReadOfAnAbsentEntitySelectsNothing(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	open := entity.RefAt("merge", "proposal", "open")
	setPayload(t, store, open, `{state: open, base: aaa111}`)

	if err := eng.AddTrigger(mustParseCharter(t, proposalCharter)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// git.head.verse does not exist. Touch the proposal to give the engine a commit to consider.
	setPayload(t, store, open, `{state: open, base: aaa111, touched: 1}`)
	if got := field(t, store, open, "state"); got != "open" {
		t.Fatalf("state = %q — a read of an absent entity should leave the rule selecting nothing", got)
	}
}

// A condition's read cannot name $subject, and the refusal happens at INSTALL with the reason
// rather than at a firing nobody is watching.
func TestAConditionReadCannotNameTheSubject(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	err := eng.AddTrigger(mustParseCharter(t, `
- name: self-referential
  condition:
    subject:
      system: merge
      kind: proposal
      value: !let
        let: [{tip: !get-path {at: [git, head, $subject.value.branch], path: sha}}]
        in:  {base: !not .[tip]}
  action:
    patch: {state: stale}
`))
	if err == nil {
		t.Fatal("installed a condition whose read names the subject it is selecting")
	}
	if !strings.Contains(err.Error(), "$subject") || !strings.Contains(err.Error(), "selects") {
		t.Fatalf("err = %v — want it to say why, not just that something is unknown", err)
	}
}

// The subtree a condition reads is part of what the trigger reads, which is what `touches` is
// entitled to skip on. Asserted directly, because the behaviour it buys (the test above) would
// also pass for a while on a coincidence of dispatch.
func TestAConditionReadJoinsTheReadSet(t *testing.T) {
	trig := mustParseCharter(t, proposalCharter)
	var found bool
	for _, st := range trig.reads() {
		if p := st.Path(); len(p) == 2 && p[0] == "git" && p[1] == "head" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reads() = %v, want git:head among them — main moving must re-evaluate the rule", trig.reads())
	}
}

// A TAG SURVIVES EXPANSION, which is the difference between a predicate and its opposite.
//
// expandInto rebuilds a string node after substituting `$refs` into it, and CloneTo copies the
// source's tag over the destination's — so a tagged string walked by the expander came back
// bare. `base: !not .[tip]` became `base: .[tip]`: no error, no complaint, the exact inverse of
// what the charter said, in the rule that decides which branches to delete.
//
// It is older than the condition feature and reaches an action's patch too, which is why this
// tests the expander rather than the condition.
func TestExpansionKeepsATagOnAString(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"an operator over a binding", `{base: !not .[tip]}`, "!not"},
		{"an operator over a reference", `{branch: !glob $name}`, "!glob"},
		{"and one over a plain string", `{branch: !glob "verse/auto/*"}`, "!glob"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pred, err := codec.ParseNode([]byte(tc.src), codec.Tony)
			if err != nil {
				t.Fatal(err)
			}
			root := refRoot(entity.Obj{"name": "verse/auto/1"}.Payload(), ir.FromString(`demo.pr."1"`))
			out, err := expandPatch(pred, root, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := entity.Fields(out)
			for k, v := range got {
				if v.Tag != tc.want {
					t.Fatalf("%s: %s came back tagged %q, want %q — the operator was dropped",
						tc.src, k, v.Tag, tc.want)
				}
			}
		})
	}
}

// A condition can hold a subject against a LIST OF OBJECTS read from another slice: `!list-paths`
// over a field that is an object answers the objects, and `!not.or` over them selects the
// subject that matches none — which is "not yet judged as it now stands", the staleness a
// triage row needs to say without naming the subject in the condition (it cannot). An empty
// list selects everything, since nothing has been judged.
func TestAConditionHoldsASubjectAgainstAListOfObjects(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	a := entity.RefAt("tracker", "issue", "a")
	b := entity.RefAt("tracker", "issue", "b")
	setPayload(t, store, a, `{id: a, status: open, updatedAt: "t1"}`)
	setPayload(t, store, b, `{id: b, status: open, updatedAt: "t1"}`)
	// a was judged as it stands; b was judged at an earlier updatedAt, so it is stale.
	setPayload(t, store, entity.RefAt("triage", "next", "a"), `{judged: {id: a, updatedAt: "t1"}}`)
	setPayload(t, store, entity.RefAt("triage", "next", "b"), `{judged: {id: b, updatedAt: "t0"}}`)

	if err := eng.AddTrigger(mustParseCharter(t, `
- name: judge
  condition:
    subject:
      path: [tracker, issue, "*"]
      value: !let
        let:
        - judged: !list-paths {at: [triage, next, "*"], path: judged}
        in: !and
        - {status: open}
        - !not.or .[judged]
  action:
    patch: {stale: yes}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	setPayload(t, store, b, `{id: b, status: open, updatedAt: "t1", touched: 1}`)
	waitField(t, store, b, "stale", "yes")
	if got := field(t, store, a, "stale"); got != nil {
		t.Fatalf("the issue judged as it stands was selected: stale=%v", got)
	}
	// a moves: its judgement no longer matches, and it is selected on that commit.
	setPayload(t, store, a, `{id: a, status: open, updatedAt: "t2"}`)
	waitField(t, store, a, "stale", "yes")
}
