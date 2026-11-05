package entity_test

import (
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

// An audience admits whoever holds, at its address, the level it is listed under: a place read
// and written as another place is. It lends neither owner nor control, and it is followed one
// place deep: a place read as a place that is itself read as another admits nobody through the
// second.
func TestAnAudienceHoldsWhatItsAddressHolds(t *testing.T) {
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	entity.PolicyOf(system).SetRootChildGrants(nil)
	alice := entity.As(system, "user:alice@example.com")
	put := func(ref entity.Ref, to map[entity.Level][]string) {
		t.Helper()
		if _, _, err := alice.Write(entity.Write{At: ref.At(), Value: entity.Obj{"n": 1}.Payload()}); err != nil {
			t.Fatal(err)
		}
		if err := entity.AddGrant(alice, ref, entity.Grant{At: ref.Segs(), To: to}); err != nil {
			t.Fatal(err)
		}
	}
	pr, ask, onward := entity.RefAt("demo", "pr", "1"), entity.RefAt("demo", "ask", "1"), entity.RefAt("demo", "ask", "2")
	put(pr, map[entity.Level][]string{entity.Read: {"user:bob@example.com"}, entity.ReadWrite: {"user:carol@example.com"}})
	about := entity.Audience(pr)
	put(ask, map[entity.Level][]string{entity.Read: {about}, entity.ReadWrite: {about}})
	put(onward, map[entity.Level][]string{entity.Read: {entity.Audience(ask)}})

	for _, c := range []struct {
		who   string
		level entity.Level
		at    entity.Ref
		want  bool
	}{
		{"user:bob@example.com", entity.Read, ask, true},        // reads the pr, so reads what is about it
		{"user:bob@example.com", entity.ReadWrite, ask, false},  // and writes neither
		{"user:carol@example.com", entity.ReadWrite, ask, true}, // writes the pr, so answers
		{"user:carol@example.com", entity.OwnerLevel, ask, false},
		{"user:dan@example.com", entity.Read, ask, false},
		{"user:bob@example.com", entity.Read, onward, false}, // two places deep
	} {
		if got, _ := entity.Holds(system, c.who, c.level, c.at); got != c.want {
			t.Errorf("%s holds %s at %s = %v, want %v", c.who, c.level, c.at, got, c.want)
		}
	}
	if ok, _ := entity.Holds(system, "user:carol@example.com", entity.Control, ask); ok {
		t.Error("carol controls what is about the pr she may write")
	}

	// Refused where it is written, at any level an address does not lend.
	for _, level := range []string{"control", "child-owner"} {
		_, err := entity.ParseGrants(ask.Segs(), entity.Obj{entity.GrantsField: []any{
			entity.Obj{level: []any{about}},
		}}.Payload())
		if err == nil || !strings.Contains(err.Error(), "an audience holds read or read-write") {
			t.Errorf("an audience granted %s: %v", level, err)
		}
	}
	// And never one principal.
	if _, err := entity.ParsePrincipal(about); err == nil {
		t.Error("an audience parsed as one principal")
	}
}
