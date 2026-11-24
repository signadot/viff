package entity_test

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

func doc(t *testing.T, src string) entity.Payload {
	t.Helper()
	n, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return n
}

func rendered(t *testing.T, p entity.Payload) string {
	t.Helper()
	var b strings.Builder
	if err := codec.EncodeNodeWire(&b, p, codec.Tony); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b.String()
}

// A write descends through what STANDS only when it is an object; where nothing stands it
// builds the path, which is what a deep address is. The array row is the one this guard exists
// for: `votes bo --put '"yes"'` used to replace the whole array with `{bo: yes}`,
// exit 0, with the two ballots gone and nothing in the answer saying so.
func TestAWriteDescendsOnlyThroughAValueThatStands(t *testing.T) {
	st := entitytest.OpenStore(t)
	for _, tc := range []struct {
		name, before string
		lands        bool
	}{
		{"object", `{p: {keep: yes}}`, true},
		{"empty object", `{p: {}}`, true},
		{"array", `{p: [{by: ana}, {by: bo}]}`, false},
		{"scalar", `{p: hello}`, false},
		// NULL LANDS: a null payload is a value, but not one a reader can read — and absence
		// reads back as a bare null from a path whose ancestors do not resolve, so refusing it
		// would refuse creation under another name.
		{"null", `{p: null}`, true},
		// ABSENT LANDS: a write does not refuse to build a path nothing stands on. It cannot —
		// an address is a path of any depth, so a package deriving `viff-dev.drift.head.abc`
		// is this very write, and refusing it refuses the package.
		{"absent", `{other: x}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := entity.RefAt("demo", "kind", tc.name)
			before := doc(t, tc.before)
			entitytest.Set(t, st, ref, before)

			ok, _, err := st.Write(entity.Write{At: ref.At("p", "foo"), Value: ir.FromString("zed")})
			got, _, _ := st.Get(ref)
			switch {
			case tc.lands:
				if err != nil || !ok {
					t.Fatalf("a write through an object was refused: ok=%v err=%v", ok, err)
				}
				if want := "zed"; !strings.Contains(rendered(t, got.Payload), want) {
					t.Fatalf("the write did not land: %s", rendered(t, got.Payload))
				}
			default:
				if ok {
					t.Fatalf("a write descended through %s and committed: %s",
						tc.name, rendered(t, got.Payload))
				}
				if rendered(t, got.Payload) != rendered(t, before) {
					t.Fatalf("a refused write changed the entity:\n before %s\n after  %s",
						rendered(t, before), rendered(t, got.Payload))
				}
			}
		})
	}
}

// THE REFUSAL SAYS WHAT IT RAN INTO. "A condition did not hold" is the answer for a caller's
// own guard and the wrong one here: the defect this closes was a transcript that did not say
// what happened to the data.
func TestTheRefusalNamesWhatStandsThere(t *testing.T) {
	st := entitytest.OpenStore(t)
	for _, tc := range []struct{ name, before, want string }{
		{"array", `{votes: [{by: ana}]}`, "holds an array"},
		{"scalar", `{votes: hello}`, "holds a scalar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := entity.RefAt("demo", "said", tc.name)
			entitytest.Set(t, st, ref, doc(t, tc.before))
			_, _, err := st.Write(entity.Write{At: ref.At("votes", "bo"), Value: ir.FromString("yes")})
			if err == nil {
				t.Fatal("the refusal said nothing")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal does not say what it ran into: %v", err)
			}
		})
	}
}

// `--create` is the SPELLING THAT SOUNDS SAFEST and used to be the one that clobbered: the
// precondition asks about the leaf, which genuinely is absent, and the loss is at the parent.
func TestCreateNoLongerClobbersTheContainer(t *testing.T) {
	st := entitytest.OpenStore(t)
	ref := entity.RefAt("demo", "pr", "1")
	before := doc(t, `{votes: [{by: ana}, {by: bo}]}`)
	entitytest.Set(t, st, ref, before)

	ok, _, _ := st.Write(entity.Write{
		At: ref.At("votes", "bo"), Value: ir.FromString("yes"),
		When: []entity.Require{{Absent: true}},
	})
	if ok {
		t.Fatal("--create committed over an array")
	}
	got, _, _ := st.Get(ref)
	if rendered(t, got.Payload) != rendered(t, before) {
		t.Fatalf("the ballots did not survive --create: %s", rendered(t, got.Payload))
	}
}

// A WRITE AT AN ADDRESS IS UNTOUCHED: creating what is not there is what a put at an address
// means, and only descending PAST one asks the question. The first entity of a kind, on a viff
// where neither the system nor the kind stands, is the case that would break if it did.
func TestAWriteAtAnAddressStillCreates(t *testing.T) {
	st := entitytest.OpenStore(t)
	ref := entity.RefAt("brandnew", "kind", "1")
	if _, _, err := st.Write(entity.Write{At: ref.At(), Value: doc(t, `{stage: open}`)}); err != nil {
		t.Fatalf("creating the first entity of a kind: %v", err)
	}
	got, ok, _ := st.Get(ref)
	if !ok {
		t.Fatal("the entity was not created")
	}
	if !strings.Contains(rendered(t, got.Payload), "open") {
		t.Fatalf("payload = %s", rendered(t, got.Payload))
	}
	// One field deep, whose parent is the entity payload — an object, so it descends.
	if _, _, err := st.Write(entity.Write{At: ref.At("stage"), Value: ir.FromString("shut")}); err != nil {
		t.Fatalf("writing a field of an entity that stands: %v", err)
	}
}

// THE `--at` SPELLING GOES THROUGH THE SAME GUARD. It arrives as a Ref of the whole depth with
// no Field at all (viffd.addressAt), where the steps arrive as a Ref plus Field — so the
// question is asked on the address, which is what both of them are.
func TestTheAtSpellingIsGuardedToo(t *testing.T) {
	st := entitytest.OpenStore(t)
	ref := entity.RefAt("demo", "pr", "9")
	before := doc(t, `{votes: [{by: ana}]}`)
	entitytest.Set(t, st, ref, before)

	deep := entity.RefAt("demo", "pr", "9", "votes", "bo") // what --at parses to
	ok, _, err := st.Write(entity.Write{At: deep.At(), Value: ir.FromString("yes")})
	if ok || err == nil {
		t.Fatalf("the --at spelling descended through an array: ok=%v err=%v", ok, err)
	}
	got, _, _ := st.Get(ref)
	if rendered(t, got.Payload) != rendered(t, before) {
		t.Fatalf("the ballots did not survive: %s", rendered(t, got.Payload))
	}
}

// A PATCH IS A DOCUMENT, NOT A PATH, so absence is its ordinary business — it builds the key —
// and only landing on a VALUE is refused.
func TestPatchLands(t *testing.T) {
	for _, tc := range []struct {
		name, cur, patch string
		ok               bool
	}{
		{"into an object", `{votes: {ana: yes}}`, `{votes: {bo: yes}}`, true},
		{"onto an absent key", `{stage: x}`, `{notes: {release: {shipped: v1}}}`, true},
		{"onto an absent entity", `null`, `{notes: {release: {shipped: v1}}}`, true},
		{"flat fields only", `{votes: [{by: ana}]}`, `{stage: counted}`, true},
		{"a scalar replaces", `{votes: [{by: ana}]}`, `{votes: gone}`, true},
		{"ONTO AN ARRAY", `{votes: [{by: ana}]}`, `{votes: {bo: yes}}`, false},
		{"ONTO A SCALAR", `{votes: hello}`, `{votes: {bo: yes}}`, false},
		{"NESTED, onto an array", `{a: {b: [1]}}`, `{a: {b: {c: 1}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := entity.PatchLands(doc(t, tc.cur), doc(t, tc.patch))
			if tc.ok && err != nil {
				t.Fatalf("refused a patch that lands: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("admitted a patch that would replace what it landed on")
			}
		})
	}
}
