package entity_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/parse"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

func mustParse(t *testing.T, src string) entity.Payload {
	t.Helper()
	p, err := parse.Parse([]byte(src), parse.ParseTony())
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return p
}

// A grant's `at` is a path with at most a trailing `*`, spelled as a list or a kinded path, its
// grantees are in the grammar and stored as it spells them, and a line that grants nothing
// readable is refused where it is written.
func TestParseGrants(t *testing.T) {
	gs, err := entity.ParseGrants(nil, mustParse(t, `{grants: [
	  {at: [verse, task, instance], child-owner: ["*"]},
	  {at: 'spaces."walter@example.com".shared', read-write: [ana@example.com, "user:*"]},
	  {at: verse.task.instance.t1, read-write: [runner:bot], control: [runner:bot]},
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 3 {
		t.Fatalf("parsed %d grants, want 3", len(gs))
	}
	if !gs[0].Below || strings.Join(gs[0].At, ".") != "verse.task.instance" || gs[0].To[entity.OwnerLevel][0] != "*" {
		t.Fatalf("list spelling: %+v", gs[0])
	}
	if gs[1].Below || strings.Join(gs[1].At, "|") != "spaces|walter@example.com|shared" ||
		strings.Join(gs[1].To[entity.ReadWrite], ",") != "user:ana@example.com,user:*" {
		t.Fatalf("kinded-path spelling: %+v", gs[1])
	}
	if gs[2].Below || len(gs[2].To[entity.Control]) != 1 {
		t.Fatalf("a control grant: %+v", gs[2])
	}
	// Round trip.
	back, err := entity.ParseGrants(nil, entity.Obj{entity.GrantsField: entity.GrantsPayload(nil, gs)}.Payload())
	if err != nil || len(back) != 3 || !back[0].Below || back[2].To[entity.Control][0] != "runner:bot" {
		t.Fatalf("round trip: %+v %v", back, err)
	}

	// ON A RECORD, a line about the record's own node says no `at`, and `below` is the one other
	// thing it says about where. Spelled back for that node, the address is not repeated; spelled
	// for no node, it is.
	node := []string{"verse", "task", "instance", "t1"}
	own, err := entity.ParseGrants(node, mustParse(t, `{grants: [
	  {read-write: [runner:bot], control: [runner:bot]},
	  {below: true, read: [user:ana@example.com]},
	  {at: [verse, task, instance, t1], read: ["*"]},
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 3 || strings.Join(own[0].At, ".") != "verse.task.instance.t1" || own[0].Below || !own[1].Below || own[2].Below {
		t.Fatalf("lines about the node: %+v", own)
	}
	spelled := entity.GrantsPayload(node, own)
	for i, want := range []string{"", "", ""} {
		if at := entity.Get(spelled.Values[i], "at"); (at == nil) != (want == "") {
			t.Errorf("line %d spelled for its node carries at=%v", i, at)
		}
	}
	if b := entity.Get(spelled.Values[1], "below"); b == nil || !b.Bool {
		t.Errorf("the below line lost its below: %v", spelled.Values[1])
	}
	if at := entity.Get(entity.GrantsPayload(nil, own).Values[0], "at"); at == nil {
		t.Error("spelled for no node, a line does not say where it is")
	}
	if _, err := entity.ParseGrants(nil, mustParse(t, `{grants: [{below: true, read: ["*"]}]}`)); err == nil {
		t.Error("a line with no at and no node parsed")
	}

	// `owner` IS HELD OUT AS `child-owner`, and no other way. It reaches what is BENEATH its
	// path and never the path, so it says no `below` and is spelled back without one; beside a
	// level that is about the node itself it is two grants, since the two reach differently.
	kids, err := entity.ParseGrants(node, mustParse(t, `{grants: [
	  {child-owner: ["*"]},
	  {child-owner: [group:makers], read: ["*"]},
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 3 || !kids[0].Below || !kids[1].Below || kids[2].Below ||
		kids[1].To[entity.OwnerLevel][0] != "group:makers" || kids[2].To[entity.Read][0] != "*" {
		t.Fatalf("child-owner lines: %+v", kids)
	}
	line := entity.GrantsPayload(node, kids).Values[0]
	if entity.Get(line, "below") != nil || entity.Get(line, entity.ChildOwnerKey) == nil || entity.Get(line, "owner") != nil {
		t.Errorf("a child-owner line is spelled back as %v", line)
	}
	for _, held := range []string{
		`{grants: [{at: [demo], owner: ["*"]}]}`,
		`{grants: [{at: [demo, "*"], owner: ["*"]}]}`,
		`{grants: [{at: [demo], below: true, owner: ["*"]}]}`,
	} {
		if _, err := entity.ParseGrants(nil, mustParse(t, held)); err == nil || !strings.Contains(err.Error(), entity.ChildOwnerKey) {
			t.Errorf("%s: %v, want a refusal that names %s", held, err, entity.ChildOwnerKey)
		}
	}
	if _, _, err := entity.ParseLevels(mustParse(t, `{child-grants: {owner: ["*"]}}`), "child-grants"); err == nil ||
		!strings.Contains(err.Error(), entity.ChildOwnerKey) {
		t.Errorf("a default holding out owner: %v, want a refusal that names %s", err, entity.ChildOwnerKey)
	}

	for _, bad := range []string{
		`{grants: [{at: [verse, "*", instance], read: ["*"]}]}`, // a star not at the end
		`{grants: [{at: "verse.ta*", read: ["*"]}]}`,            // a glob, not a whole segment
		`{grants: [{read: ["*"]}]}`,                             // no path
		`{grants: [{at: [demo]}]}`,                              // no level
		`{grants: [{at: [demo], read: "*"}]}`,                   // grantees are a list
		`{grants: {at: [demo], read: ["*"]}}`,                   // grants are a list
	} {
		if _, err := entity.ParseGrants(nil, mustParse(t, bad)); err == nil {
			t.Errorf("%s: parsed, want a refusal", bad)
		}
	}
	// A grantee the grammar does not read is refused where it is typed, in a grant as in a
	// default, and the refusal says the grammar.
	for _, bad := range []string{
		`{grants: [{at: [demo], read: [user]}]}`,
		`{grants: [{at: [demo], read: [verse.principal.group.release]}]}`,
		`{grants: [{at: [demo], control: [verse-runner]}]}`,
	} {
		if _, err := entity.ParseGrants(nil, mustParse(t, bad)); err == nil || !strings.Contains(err.Error(), entity.Grammar()) {
			t.Errorf("%s: %v, want a refusal quoting the grammar", bad, err)
		}
	}
	if _, _, err := entity.ParseLevels(mustParse(t, `{child-grants: {read-write: [agent]}}`), "child-grants"); err == nil ||
		!strings.Contains(err.Error(), entity.Grammar()) {
		t.Errorf("a default naming a bare kind: %v, want a refusal quoting the grammar", err)
	}
	if gs, err := entity.ParseGrants(nil, mustParse(t, `{owner: alice}`)); err != nil || gs != nil {
		t.Fatalf("a record with no grants: %v %v", gs, err)
	}
}

// mayCreate is whether who may bring ref into being, which a `child-owner` line holds out and
// holding `owner` at a node that stands does not say.
func mayCreate(t *testing.T, s entity.Store, who string, ref entity.Ref) bool {
	t.Helper()
	ok, err := entity.MayCreate(s, who, ref)
	if err != nil {
		t.Fatalf("MayCreate(%s, %s): %v", who, ref, err)
	}
	return ok
}

func holds(t *testing.T, s entity.Store, who string, level entity.Level, ref entity.Ref) bool {
	t.Helper()
	ok, err := entity.Holds(s, who, level, ref)
	if err != nil {
		t.Fatalf("Holds(%s, %s, %s): %v", who, level, ref, err)
	}
	return ok
}

// The model's own examples, as a table: who holds what where, once the records say what the
// seed will say.
func TestWhoHoldsWhatWhere(t *testing.T) {
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	entity.PolicyOf(system).SetRootChildGrants(nil) // the granted model: nothing is held out by default
	alice, bob, bot := "user:alice@example.com", "user:bob@example.com", "runner:bot"
	rw := func(to ...string) map[entity.Level][]string { return map[entity.Level][]string{entity.ReadWrite: to} }

	// The seed: anyone may create a task; a person may answer a question.
	tasks := entity.VerseRef("task", "instance")
	seedRecord(t, system, tasks, "", entity.Grant{
		At: tasks.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {entity.Everyone}},
	})
	questions := entity.VerseRef("gate", "question")
	seedRecord(t, system, questions, "", entity.Grant{At: questions.Segs(), Below: true, To: rw(entity.UserKind)})

	t1 := entity.VerseRef("task", "instance", "t1")
	// A creation grant reaches beneath the path and not the path itself.
	if !mayCreate(t, system, alice, t1) || holds(t, system, alice, entity.ReadWrite, tasks) {
		t.Fatal("the `verse.task.instance.*` grant is about creating beneath it")
	}
	// The unattributed request is everyone too.
	if !mayCreate(t, system, "", t1) {
		t.Fatal("`*` names the unattributed request")
	}

	// alice creates t1: hers, on record; and now the seed's grant stops at her record.
	if _, _, err := entity.As(system, alice).Write(entity.Write{At: t1.At(), Value: entity.Obj{"status": "running"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, inner, t1); o != alice {
		t.Fatalf("t1 is owned by %q", o)
	}
	if holds(t, system, bob, entity.Read, t1) || holds(t, system, bob, entity.ReadWrite, entity.RefAt(append(t1.Segs(), "report")...)) {
		t.Fatal("a grant above an owner record reaches through it")
	}
	if !holds(t, system, alice, entity.OwnerLevel, entity.RefAt(append(t1.Segs(), "report")...)) {
		t.Fatal("ownership does not descend")
	}

	// The spawn's grant to the runner: read-write and control on the task, on the task's own
	// record, and it does not reach the question the task raises.
	seedRecord(t, system, t1, alice, entity.Grant{
		At: t1.Segs(), To: map[entity.Level][]string{entity.ReadWrite: {bot}, entity.Control: {bot}},
	})
	if !holds(t, system, bot, entity.ReadWrite, t1) || !holds(t, system, bot, entity.Control, t1) ||
		mayCreate(t, system, bot, t1) {
		t.Fatal("the runner holds read-write and control on its task, and not owner")
	}
	if !holds(t, system, bot, entity.ReadWrite, entity.RefAt(append(t1.Segs(), "report")...)) {
		t.Fatal("a grant without a star covers the node and what is beneath it")
	}
	q := entity.VerseRef("gate", "question", "t1~d1")
	seedRecord(t, system, q, entity.SystemDocket) // posed by versed: its own record
	if holds(t, system, bot, entity.Read, q) {
		t.Fatal("the runner reaches the question its task raised")
	}

	// A kind: a principal whose name carries the kind answers, whether or not anybody has met
	// them; a principal of another kind, a name with no kind, and the unattributed request do
	// not.
	carol := "user:carol@example.com"
	q2 := entity.VerseRef("gate", "question", "t2~d1")
	seedRecord(t, system, q2, entity.SystemDocket)
	// The grants on the record above the question hang above an owner record, and do not
	// cross it: the seed's `user` grant reaches a question only through the question's own
	// record. Grant it there, as the mirror will.
	seedRecord(t, system, q2, entity.SystemDocket, entity.Grant{At: q2.Segs(), To: rw("user:*")})
	if !holds(t, system, carol, entity.ReadWrite, q2) {
		t.Fatal("a user does not hold what `user:*` is granted")
	}
	if holds(t, system, bot, entity.ReadWrite, q2) || holds(t, system, "agent", entity.ReadWrite, q2) ||
		holds(t, system, "", entity.ReadWrite, q2) {
		t.Fatal("a runner, a kindless name, or nobody, holds what `user:*` is granted")
	}

	// A place acting holds what its owner holds.
	rule := entity.VerseRef("trigger", "rule", "review")
	seedRecord(t, system, rule, alice)
	if !holds(t, system, entity.Via(rule), entity.OwnerLevel, t1) || holds(t, system, entity.Via(rule), entity.Read, q) {
		t.Fatal("a firing of alice's rule does not act as alice")
	}

	// The world's half is open until a record says otherwise.
	pr := entity.RefAt("demo", "pr", "1")
	if !mayCreate(t, system, bob, pr) || !mayCreate(t, system, "", pr) {
		t.Fatal("the world half is not open")
	}
	if _, _, err := entity.As(system, alice).Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if holds(t, system, bob, entity.Read, pr) || !holds(t, system, alice, entity.OwnerLevel, pr) {
		t.Fatal("alice's pr is not alice's")
	}

	// The system holds everything, the mirror included; nobody else reaches the mirror.
	if !holds(t, system, entity.SystemDefault, entity.OwnerLevel, entity.PermsRef(pr)) ||
		holds(t, system, alice, entity.Read, entity.PermsRef(pr)) {
		t.Fatal("the mirror is the system's alone")
	}
}

// A refusal refuses and writes nothing, says who wanted what and what decided, and is counted.
func TestARefusalRefusesAndIsCounted(t *testing.T) {
	inner := &stamped{Store: entitytest.OpenStore(t)}
	system := entity.As(inner, entity.SystemDefault)
	alice := entity.As(system, "user:alice@example.com")
	bob := entity.As(system, "user:bob@example.com")
	pr := entity.RefAt("demo", "pr", "1")
	if _, _, err := alice.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}

	before := inner.last.At.String()
	committed, _, err := bob.Write(entity.Write{At: pr.At("stage"), Value: entity.Val("merged")})
	if !errors.Is(err, entity.ErrForbidden) || committed {
		t.Fatalf("committed=%v err=%v", committed, err)
	}
	if inner.last.At.String() != before || entity.Str(inner.last.Value, "") == "merged" {
		t.Fatalf("a refused write reached the store: %s", inner.last.At)
	}
	// The record that decided stands at the place alice created, which is where her ownership
	// of the row begins — the refusal names it rather than the row, because that is what a
	// reader has to go and look at.
	if !strings.Contains(err.Error(), "bob@example.com holds read at "+pr.String()) ||
		!strings.Contains(err.Error(), "alice@example.com at demo") {
		t.Fatalf("the refusal does not say who, where, or what decided: %v", err)
	}
	byLevel, unreadable := entity.PolicyOf(system).Refusals()
	if byLevel[string(entity.ReadWrite)] != 1 || unreadable != 0 {
		t.Fatalf("refusals = %v, %d", byLevel, unreadable)
	}
	// Modify is the same write, and reports the same refusal rather than retrying it.
	if err := entity.Modify(t.Context(), bob, pr, func(cur entity.Payload) entity.Payload { return cur }); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("Modify: %v", err)
	}
	// alice, and the system, write.
	if _, _, err := alice.Write(entity.Write{At: pr.At("stage"), Value: entity.Val("merged")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := system.Write(entity.Write{At: pr.At("stage"), Value: entity.Val("closed")}); err != nil {
		t.Fatal(err)
	}
}

// A grant written to the mirror is honoured on the next check, and one withdrawn is not: the
// view learns of every write this process makes to the mirror.
func TestAGrantWithdrawnIsNotHonoured(t *testing.T) {
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	alice := entity.As(system, "user:alice@example.com")
	bob := entity.As(system, "user:bob@example.com")
	notes := entity.RefAt("spaces", "alice", "notes")
	if _, _, err := alice.Write(entity.Write{At: notes.At(), Value: entity.Obj{"n": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bob.Write(entity.Write{At: notes.At("n"), Value: entity.Val(2)}); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("before the grant: %v", err)
	}
	seedRecord(t, system, notes, "user:alice@example.com", entity.Grant{
		At: notes.Segs(), To: map[entity.Level][]string{entity.ReadWrite: {"user:bob@example.com"}},
	})
	if _, _, err := bob.Write(entity.Write{At: notes.At("n"), Value: entity.Val(2)}); err != nil {
		t.Fatalf("with the grant: %v", err)
	}
	// A read-write grantee may not delete the node whole: eviction is a co-owner's act.
	if _, _, err := bob.Write(entity.Write{At: notes.At()}); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("a grantee deleting the node: %v", err)
	}
	seedRecord(t, system, notes, "user:alice@example.com")
	if _, _, err := bob.Write(entity.Write{At: notes.At("n"), Value: entity.Val(3)}); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("after the grant was withdrawn: %v", err)
	}
	// And the owner may delete THE PLACE she made, which forgets its record and every record
	// beneath it, so what bob then creates is bob's — the place included.
	space := entity.RefAt(notes.Segs()[0])
	if _, _, err := alice.Write(entity.Write{At: space.At()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bob.Write(entity.Write{At: notes.At(), Value: entity.Obj{"n": 4}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, inner, space); o != "user:bob@example.com" {
		t.Fatalf("re-created by bob, owner of %s = %q", space, o)
	}
}
