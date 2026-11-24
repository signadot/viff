package entity_test

import (
	"testing"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// `answer` is held by being named, and by nothing else: owning a node confers none of it, and a
// resolver acting as whoever installed it holds none by their ownership. It reaches beneath its
// node as a grant does, and stops at a node somebody else owns.
func TestAnAnswerGrantIsHeldOnlyByBeingNamed(t *testing.T) {
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	entity.PolicyOf(system).SetRootChildGrants(nil)
	alice := entity.As(system, "user:alice@example.com")
	pr := entity.RefAt("demo", "pr", "1")
	if _, _, err := alice.Write(entity.Write{At: pr.At(), Value: entity.Obj{"n": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}
	// Two resolvers, both alice's; one is granted.
	r1, r2 := entity.ViffRef("resolvers", "r1"), entity.ViffRef("resolvers", "r2")
	for _, r := range []entity.Ref{r1, r2} {
		if err := entity.Establish(system, r, "user:alice@example.com", entity.Ref{}, []entity.Grant{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := entity.SetLevel(system, pr, entity.Answer, []string{entity.Via(r1)}); err != nil {
		t.Fatal(err)
	}
	// bob's place beneath alice's pr.
	notes := entity.RefAt("demo", "pr", "1", "notes")
	if err := entity.Establish(system, notes, "user:bob@example.com", entity.Ref{}, []entity.Grant{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		who  string
		at   entity.Ref
		want bool
	}{
		{entity.Via(r1), pr, true},
		{entity.Via(r1), entity.RefAt("demo", "pr", "1", "stage"), true}, // beneath, as a grant reaches
		{entity.Via(r2), pr, false},                                      // alice's too, and not named
		{"user:alice@example.com", pr, false},                            // owning it is not answering for it
		{entity.Via(r1), notes, false},                                   // bob's place, not alice's grant
	} {
		if got, err := entity.Holds(system, c.who, entity.Answer, c.at); err != nil || got != c.want {
			t.Errorf("%s holds answer at %s = %v (%v), want %v", c.who, c.at, got, err, c.want)
		}
	}
	// And a record level is no answer, nor the other way round: at carol's place, where nothing
	// of alice's stands above, an answer grant lends no read.
	other := entity.RefAt("carol", "place")
	if err := entity.Establish(system, other, "user:carol@example.com", entity.Ref{}, []entity.Grant{}); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetLevel(system, other, entity.Answer, []string{entity.Via(r1)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := entity.Holds(system, entity.Via(r1), entity.Answer, other); !got {
		t.Error("carol's answer grant is not held")
	}
	if got, _ := entity.Holds(system, entity.Via(r1), entity.Read, other); got {
		t.Error("an answer grant lent read")
	}
	if !entity.Answer.Admits(entity.Answer) || entity.OwnerLevel.Admits(entity.Answer) || entity.Answer.Admits(entity.Read) {
		t.Error("answer is admitted only by itself")
	}
	// Spelled in a grant line, and read back.
	gs, err := entity.ParseGrants(pr.Segs(), entity.Obj{entity.GrantsField: []any{
		entity.Obj{"answer": []any{entity.Via(r1)}},
	}}.Payload())
	if err != nil || len(gs) != 1 || len(gs[0].To[entity.Answer]) != 1 {
		t.Fatalf("an answer line parsed as %+v, %v", gs, err)
	}
}

// `answering:<address>` in a `read` admits whoever holds `answer` at the address, and nothing
// more: it is how an ask is read by the resolver serving it. It is refused outside `read`, and is
// not followed from inside another place's audience.
func TestAnAnsweringGranteeAdmitsWhoAnswersThere(t *testing.T) {
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	entity.PolicyOf(system).SetRootChildGrants(nil)
	r1, r2 := entity.ViffRef("resolvers", "r1"), entity.ViffRef("resolvers", "r2")
	for _, r := range []entity.Ref{r1, r2} {
		if err := entity.Establish(system, r, "user:carol@example.com", entity.Ref{}, []entity.Grant{}); err != nil {
			t.Fatal(err)
		}
	}
	place := entity.RefAt("alice", "place")
	ask := entity.RefAt("asks", "1")
	outer := entity.RefAt("asks", "2")
	for ref, grants := range map[entity.Ref][]entity.Grant{
		place: {{At: place.Segs(), To: map[entity.Level][]string{entity.Answer: {entity.Via(r1)}}}},
		ask:   {{At: ask.Segs(), To: map[entity.Level][]string{entity.Read: {entity.Answering(place)}}}},
		outer: {{At: outer.Segs(), To: map[entity.Level][]string{entity.Read: {entity.Audience(ask)}}}},
	} {
		if err := entity.Establish(system, ref, "user:alice@example.com", entity.Ref{}, grants); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		who   string
		level entity.Level
		at    entity.Ref
		want  bool
	}{
		{entity.Via(r1), entity.Read, ask, true},
		{entity.Via(r1), entity.ReadWrite, ask, false},
		{entity.Via(r2), entity.Read, ask, false},
		{entity.Via(r1), entity.Read, outer, false}, // an audience's reading, one place deep
	} {
		if got, _ := entity.Holds(system, c.who, c.level, c.at); got != c.want {
			t.Errorf("%s holds %s at %s = %v, want %v", c.who, c.level, c.at, got, c.want)
		}
	}
	for _, level := range []string{"read-write", "control"} {
		if _, err := entity.ParseGrants(ask.Segs(), entity.Obj{entity.GrantsField: []any{
			entity.Obj{level: []any{entity.Answering(place)}},
		}}.Payload()); err == nil {
			t.Errorf("answering: granted %s", level)
		}
	}
	if _, err := entity.ParsePrincipal(entity.Answering(place)); err == nil {
		t.Error("answering: parsed as one principal")
	}
}
