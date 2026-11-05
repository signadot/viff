package api

import (
	"errors"
	"net/url"
	"testing"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
)

// THE DATA GRAMMAR SAYS WHICH WORD IS WRONG, named as verse mcp's parameters are, and whether it
// is a misuse of the words or a value that does not read: the CLI says a misuse as a usage error
// and the rest as what they are, and a tool names its own parameter.
func TestTheDataGrammarSaysWhichWordIsWrong(t *testing.T) {
	segs := []string{"acme", "notes", "n1"}
	for _, c := range []struct {
		what   string
		err    error
		field  string
		misuse bool
	}{
		{"a read at no address", third(StateQuery("demo..[", "", "", false, entity.Unbounded, codec.Tony)), "at", true},
		{"if-at alone", FilterQuery(url.Values{}, "", "labels", codec.Tony), "if_at", true},
		{"an if that does not read", FilterQuery(url.Values{}, "{stage: ", "", codec.Tony), "if", false},
		{"a write at nowhere", second(DataWrite{Payload: "1"}.Body(nil, codec.Tony)), "at", true},
		{"a value that does not read", second(DataWrite{Payload: "{stage: "}.Body(segs, codec.Tony)), "value", false},
		{"create and update", second(DataWrite{Payload: "1", Create: true, Update: true}.Body(segs, codec.Tony)), "create", true},
		{"an expect that does not read", second(DataWrite{Payload: "1", Expect: "{a: "}.Body(segs, codec.Tony)), "expect", false},
		{"grants on an update", second(DataWrite{Payload: "1", Update: true, Grants: "[]"}.Body(segs, codec.Tony)), "grants", true},
		{"grants that do not read", second(DataWrite{Payload: "1", Grants: "[{"}.Body(segs, codec.Tony)), "grants", false},
		{"a removal of nothing", third(RemoveQuery(nil)), "at", true},
	} {
		var fe *FieldError
		if !errors.As(c.err, &fe) || fe.Field != c.field || fe.Misuse != c.misuse {
			t.Errorf("%s: %v (%+v), want %s, misuse %v", c.what, c.err, fe, c.field, c.misuse)
		}
	}
}

func second[A any](_ A, err error) error        { return err }
func third[A, B any](_ A, _ B, err error) error { return err }
