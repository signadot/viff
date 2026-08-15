package entity

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/codec"
)

// fixture is the awkward corners of the id space, together, so every test below is asked about
// the ones a rendered ref used to hide: a dotted id, a colon-bearing derived id, a numeric id
// (`.1` is the FIELD named 1, not an index), a non-object payload and a null one.
func fixture() []Entity {
	return []Entity{
		{Ref: Ref{"demo", "pr", "1"}, Payload: Obj{"stage": "open"}.Payload()},
		{Ref: Ref{"demo", "pr", "2"}, Payload: Obj{"stage": "reviewed"}.Payload()},
		{Ref: Ref{"demo", "pr", "a.b"}, Payload: Obj{"stage": "open"}.Payload()},
		{Ref: Ref{"git", "ref", "proposal-appeared@git:ref:x+2a6c8212"}, Payload: Obj{"sha": "2a6c"}.Payload()},
		{Ref: Ref{"count", "n", "hits"}, Payload: ir.FromInt(42)},
		{Ref: Ref{"count", "n", "nothing"}, Payload: ir.Null()},
	}
}

func at(t *testing.T, s string) *kpath.KPath {
	t.Helper()
	kp, err := ParseAt(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return kp
}

func nest(t *testing.T, ents []Entity, path string) Doc {
	t.Helper()
	d, err := Nest(ents, at(t, path))
	if err != nil {
		t.Fatalf("nest at %q: %v", path, err)
	}
	return d
}

// The nesting and the store's own materialized walk are inverses. That is the whole claim: the
// document is not a shape assembled for the wire, it is what the store holds.
func TestNestIsWalkEntityDocInverted(t *testing.T) {
	want := fixture()
	got := walkEntityDoc(nest(t, want, "").Node)
	sortEntities(want)
	sortEntities(got)
	if len(got) != len(want) {
		t.Fatalf("walked %d entities, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Ref != want[i].Ref {
			t.Fatalf("ref %d = %s, want %s", i, got[i].Ref, want[i].Ref)
		}
		if !Equal(got[i].Payload, want[i].Payload) {
			t.Fatalf("payload of %s did not survive the round trip", want[i].Ref)
		}
	}
}

// The depth table: what a path of each length answers with.
func TestNestAnswersTheSubtreeAtThePath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string // the tony rendering of the answer
	}{
		{"demo.pr.1", "{stage: open}"},
		{"demo.pr.1.stage", "open"},
		{"count.n.hits", "42"},
		{"count.n.nothing", "null"},
		{`demo.pr."a.b".stage`, "open"},
		{"demo.pr", `{"1": {stage: open} "2": {stage: reviewed} a.b: {stage: open}}`},
		{"count", `{n: {hits: 42 nothing: null}}`},
	} {
		got := nest(t, fixture(), tc.path)
		if got.Node == nil {
			t.Errorf("%s: nothing stands there, want %s", tc.path, tc.want)
			continue
		}
		if flat := flatten(t, got.Node); flat != tc.want {
			t.Errorf("%s = %s, want %s", tc.path, flat, tc.want)
		}
	}
}

// A numeric id is the FIELD named 1, not an array index — the distinction the kpath grammar
// carries in its syntax, and the reason nothing here has to guess by counting segments.
func TestANumericIDIsAFieldNotAnIndex(t *testing.T) {
	if d := nest(t, fixture(), "demo.pr.1"); d.Node == nil {
		t.Fatal("demo.pr.1 named nothing — a numeric id must read as the field it is")
	}
	if _, err := Nest(fixture(), at(t, "demo.pr[1]")); err == nil {
		t.Fatal("demo.pr[1] asked an object for its second element and was answered anyway")
	}
}

// An id carrying colons, `@` and `+` needs no quoting: an unquoted kpath field runs until one
// of `.[{(`, so a derived id is pasteable as written.
func TestADerivedIDNeedsNoQuoting(t *testing.T) {
	d := nest(t, fixture(), "git.ref.proposal-appeared@git:ref:x+2a6c8212")
	if d.Node == nil {
		t.Fatal("a derived id did not address its entity")
	}
	if got := Str(d.Node, "sha"); got != "2a6c" {
		t.Fatalf("sha = %q, want 2a6c", got)
	}
}

// A wildcard keeps its container's shape, so the answer stays addressable: the ids are still
// the keys, rather than a flat list of values nobody can point at.
func TestAWildcardKeepsTheKeys(t *testing.T) {
	got := nest(t, fixture(), "demo.pr.*.stage")
	if want := `{"1": open "2": reviewed a.b: open}`; flatten(t, got.Node) != want {
		t.Fatalf("demo.pr.*.stage = %s, want %s", flatten(t, got.Node), want)
	}
	// …at any level, and `demo.*.1` reads its kind back out of the one key standing there.
	if want := `{pr: {stage: open}}`; flatten(t, nest(t, fixture(), "demo.*.1").Node) != want {
		t.Fatalf("demo.*.1 = %s, want %s", flatten(t, nest(t, fixture(), "demo.*.1").Node), want)
	}
}

// A path naming nothing answers with nothing, at every depth. What the caller does about it —
// 200 for a slice, 404 for a thing — is the handler's, and AtDepth is what it asks.
func TestAPathNamingNothingIsNothing(t *testing.T) {
	for _, path := range []string{"nosuch", "demo.nosuch", "demo.pr.nosuch", "demo.pr.1.nosuch"} {
		if d := nest(t, fixture(), path); d.Node != nil {
			t.Errorf("%s answered %s, want nothing", path, flatten(t, d.Node))
		}
	}
	for path, want := range map[string]int{"": 0, "demo": 1, "demo.pr": 2, "demo.pr.1": 3, "demo.pr.1.a": 4} {
		if got := AtDepth(at(t, path)); got != want {
			t.Errorf("AtDepth(%q) = %d, want %d", path, got, want)
		}
	}
}

// The addresses come back out of the answer at every depth, which is what --paths is: no
// payloads, one address per line, and no grepping an id out of a rendered ref.
func TestEntitiesRecoversTheAddresses(t *testing.T) {
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"", []string{
			"count:n:hits", "count:n:nothing", "demo:pr:1", "demo:pr:2", "demo:pr:a.b",
			"git:ref:proposal-appeared@git:ref:x+2a6c8212",
		}},
		{"demo", []string{"demo:pr:1", "demo:pr:2", "demo:pr:a.b"}},
		{"demo.pr", []string{"demo:pr:1", "demo:pr:2", "demo:pr:a.b"}},
		{"demo.pr.1", []string{"demo:pr:1"}},
		{"demo.*.1", []string{"demo:pr:1"}},
		{"*.pr", []string{"demo:pr:1", "demo:pr:2", "demo:pr:a.b"}},
		{"demo.pr.1.stage", []string{}}, // inside a payload: no entities down here
	} {
		got := nest(t, fixture(), tc.path).Paths(at(t, tc.path))
		sort.Strings(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q paths = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// `(key)` reaches into an array by identity, which no verse address could do before: a
// proposal's votes are addressable one voter at a time.
func TestAKeyedArrayIsAddressable(t *testing.T) {
	votes := ir.FromSlice([]*ir.Node{
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("scott"), "choice": ir.FromString("yes")}),
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("ana"), "choice": ir.FromString("no")}),
	}).WithTag("!key(voter)")
	ents := []Entity{{
		Ref:     Ref{"merge", "proposal", "X"},
		Payload: ir.FromMap(map[string]*ir.Node{"votes": votes}),
	}}

	if want := `{choice: yes voter: scott}`; flatten(t, nest(t, ents, "merge.proposal.X.votes(scott)").Node) != want {
		t.Fatalf("votes(scott) = %s, want %s", flatten(t, nest(t, ents, "merge.proposal.X.votes(scott)").Node), want)
	}
	if d := nest(t, ents, "merge.proposal.X.votes(nobody)"); d.Node != nil {
		t.Fatalf("votes(nobody) answered %s, want nothing", flatten(t, d.Node))
	}
	if got := flatten(t, nest(t, ents, "merge.proposal.X.votes[0].choice").Node); got != "yes" {
		t.Fatalf("votes[0].choice = %s, want yes", got)
	}
}

// An unkeyed array says so rather than answering no: `(scott)` over it is a question the data
// cannot be asked, not one whose answer is nothing.
func TestAKeyOnAnUnkeyedArrayIsAnError(t *testing.T) {
	ents := []Entity{{
		Ref:     Ref{"merge", "proposal", "X"},
		Payload: ir.FromMap(map[string]*ir.Node{"votes": ir.FromSlice([]*ir.Node{ir.FromString("scott")})}),
	}}
	if _, err := Nest(ents, at(t, "merge.proposal.X.votes(scott)")); err == nil {
		t.Fatal("a keyed lookup into an unkeyed array was answered rather than refused")
	}
}

// The document is deterministic: ir.FromMap sorts at every level, so two reads of an unchanged
// verse are the same bytes however the entities arrived.
func TestTheDocumentIsOrdered(t *testing.T) {
	forward := fixture()
	backward := fixture()
	for i, j := 0, len(backward)-1; i < j; i, j = i+1, j-1 {
		backward[i], backward[j] = backward[j], backward[i]
	}
	if a, b := flatten(t, nest(t, forward, "").Node), flatten(t, nest(t, backward, "").Node); a != b {
		t.Fatalf("the document depends on arrival order:\n %s\n %s", a, b)
	}
}

// The codec is the document itself, with nothing wrapped around it.
func TestDocIsItsOwnWire(t *testing.T) {
	d := nest(t, fixture(), "demo.pr.1")
	node, err := d.ToTonyIR()
	if err != nil {
		t.Fatalf("ToTonyIR: %v", err)
	}
	if got := Str(node, "stage"); got != "open" {
		t.Fatalf("the wire is not the document: %s", flatten(t, node))
	}
	var back Doc
	if err := back.FromTonyIR(node); err != nil {
		t.Fatalf("FromTonyIR: %v", err)
	}
	if !Equal(back.Node, d.Node) {
		t.Fatal("the document did not survive its own codec")
	}
}

func sortEntities(es []Entity) {
	sort.Slice(es, func(i, j int) bool { return es[i].Ref.String() < es[j].Ref.String() })
}

// flatten renders a node compact and single-line, which is what makes a whole expected
// document fit on one line of a test table.
func flatten(t *testing.T, n *ir.Node) string {
	t.Helper()
	b, err := codec.NodeBytesWire(n, codec.Tony)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// `(key)` reaches a stored array, against a real store — the end of the claim the unit test
// above makes about the walk.
//
// The annotation has to be STORED, and that is the whole subtlety: `!key` is a registered merge
// operator, so an ordinary write EXECUTES it — logd merges the array by key and keeps the
// result without the tag, and the array that comes back is not keyed. Raw is what says "this
// tag is data", so the payload lands carrying its own annotation and a read can follow it.
func TestKeyedReachAgainstTheStore(t *testing.T) {
	s := openStore(t)
	ref := Ref{System: "merge", Kind: "proposal", ID: "X"}
	votes := ir.FromSlice([]*ir.Node{
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("scott"), "choice": ir.FromString("yes")}),
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("ana"), "choice": ir.FromString("no")}),
	}).WithTag("!key(voter)")
	if ok, _, err := s.Write(Write{
		At:    ref.At(),
		Value: Raw(ir.FromMap(map[string]*ir.Node{"votes": votes})),
	}); err != nil || !ok {
		t.Fatalf("write: committed=%v err=%v", ok, err)
	}

	d, err := Nest(s.List(nil), at(t, "merge.proposal.X.votes(scott)"))
	if err != nil {
		t.Fatalf("nest: %v", err)
	}
	if got := Str(d.Node, "choice"); got != "yes" {
		t.Fatalf("votes(scott).choice = %q, want yes — the stored array did not keep its key", got)
	}
}
