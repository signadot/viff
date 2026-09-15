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
		{Ref: RefAt("demo", "pr", "1"), Payload: Obj{"stage": "open"}.Payload()},
		{Ref: RefAt("demo", "pr", "2"), Payload: Obj{"stage": "reviewed"}.Payload()},
		{Ref: RefAt("demo", "pr", "a.b"), Payload: Obj{"stage": "open"}.Payload()},
		{Ref: RefAt("git", "ref", "proposal-appeared@git.ref.x+2a6c8212"), Payload: Obj{"sha": "2a6c"}.Payload()},
		{Ref: RefAt("count", "n", "hits"), Payload: ir.FromInt(42)},
		{Ref: RefAt("count", "n", "nothing"), Payload: ir.Null()},
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

// docOf is the document the fixture's nodes stand in, rooted at the root.
func docOf(ents []Entity) *ir.Node {
	tree := map[string]any{}
	for _, e := range ents {
		segs := e.Ref.Segs()
		level := tree
		for _, s := range segs[:len(segs)-1] {
			next, ok := level[s].(map[string]any)
			if !ok {
				next = map[string]any{}
				level[s] = next
			}
			level = next
		}
		level[segs[len(segs)-1]] = clone(e.Payload)
	}
	var build func(map[string]any) *ir.Node
	build = func(m map[string]any) *ir.Node {
		out := map[string]*ir.Node{}
		for k, v := range m {
			switch v := v.(type) {
			case map[string]any:
				out[k] = build(v)
			case *ir.Node:
				out[k] = v
			}
		}
		return ir.FromMap(out)
	}
	return build(tree)
}

// read is a read of the fixture at a path: the node the path reaches, nil when it reaches none.
func read(t *testing.T, ents []Entity, path string) Doc {
	t.Helper()
	out, _, err := Reach(docOf(ents), at(t, path), nil)
	if err != nil {
		t.Fatalf("read at %q: %v", path, err)
	}
	return Doc{Node: out}
}

// The depth table: what a path of each length answers with — the node standing there, whatever
// it is, with everything beneath it.
func TestAReadAnswersTheNodeAtThePath(t *testing.T) {
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
		// A null reads as null wherever it is asked, a wildcard included.
		{"count.n.*", `{hits: 42 nothing: null}`},
	} {
		got := read(t, fixture(), tc.path)
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
	if d := read(t, fixture(), "demo.pr.1"); d.Node == nil {
		t.Fatal("demo.pr.1 named nothing — a numeric id must read as the field it is")
	}
	if d := read(t, fixture(), "demo.pr[1]"); d.Node != nil {
		t.Fatalf("demo.pr[1] asked an object for its second element and was answered %s", flatten(t, d.Node))
	}
}

// A DERIVED ID HOLDS THE ADDRESS IT IS ABOUT, so it holds dots, so it is quoted like any other
// segment that does — `git.ref."proposal-appeared@git.ref.x+2a6c8212"`. `@` and `+` need no
// quoting on their own (an unquoted kpath field runs until one of `.[{(`); the dots do.
func TestADerivedIDIsOneQuotedSegment(t *testing.T) {
	d := read(t, fixture(), `git.ref."proposal-appeared@git.ref.x+2a6c8212"`)
	if d.Node == nil {
		t.Fatal("a derived id did not address its node")
	}
	if got := Str(d.Node, "sha"); got != "2a6c" {
		t.Fatalf("sha = %q, want 2a6c", got)
	}
}

// A wildcard keeps its container's shape, so the answer stays addressable: the ids are still
// the keys, rather than a flat list of values nobody can point at.
func TestAWildcardKeepsTheKeys(t *testing.T) {
	got := read(t, fixture(), "demo.pr.*.stage")
	if want := `{"1": open "2": reviewed a.b: open}`; flatten(t, got.Node) != want {
		t.Fatalf("demo.pr.*.stage = %s, want %s", flatten(t, got.Node), want)
	}
	// …at any level, and `demo.*.1` reads its kind back out of the one key standing there.
	if want := `{pr: {stage: open}}`; flatten(t, read(t, fixture(), "demo.*.1").Node) != want {
		t.Fatalf("demo.*.1 = %s, want %s", flatten(t, read(t, fixture(), "demo.*.1").Node), want)
	}
}

// A WILDCARD REACHES THE NODES THAT HAVE WHAT IT ASKS FOR. `demo.pr.*.stage` over a pr that is
// a string reaches the others, which is what the store answers for a literal path through a
// value of the wrong shape.
func TestAWildcardOverMixedShapesReachesTheOnesThatFit(t *testing.T) {
	ents := append(fixture(), Entity{Ref: RefAt("demo", "pr", "3"), Payload: ir.FromString("closed")})
	if got, want := flatten(t, read(t, ents, "demo.pr.*.stage").Node), `{"1": open "2": reviewed a.b: open}`; got != want {
		t.Fatalf("demo.pr.*.stage = %s, want %s", got, want)
	}
}

// A path naming nothing answers with nothing, at every depth — a wildcard over a node with no
// children among them — and reaching nodes a filter kept none of is a different answer.
func TestAPathNamingNothingIsNothing(t *testing.T) {
	ents := append(fixture(), Entity{Ref: RefAt("solo", "hollow"), Payload: ir.FromMap(map[string]*ir.Node{})})
	for _, path := range []string{"nosuch", "demo.nosuch", "demo.pr.nosuch", "demo.pr.1.nosuch", "solo.hollow.*", "demo.pr.1.stage.*"} {
		if out, reached, err := Reach(docOf(ents), at(t, path), nil); err != nil || reached || out != nil {
			t.Errorf("%s answered %v (reached=%v, err=%v), want nothing", path, out, reached, err)
		}
	}
	if got := flatten(t, read(t, ents, "solo.*").Node); got != "{hollow: {}}" {
		t.Errorf("solo.* = %s, want {hollow: {}}", got)
	}
	none := func(*ir.Node) (*ir.Node, bool, error) { return nil, false, nil }
	for _, path := range []string{"demo.pr", "demo.pr.*", "*.pr.*.stage"} {
		if out, reached, err := Reach(docOf(ents), at(t, path), none); err != nil || !reached || out != nil {
			t.Errorf("%s with nothing kept = %v (reached=%v, err=%v), want reached and empty", path, out, reached, err)
		}
	}
	for path, want := range map[string]int{"": 0, "demo": 1, "demo.pr": 2, "demo.pr.1": 3, "demo.pr.1.a": 4} {
		if got := AtDepth(at(t, path)); got != want {
			t.Errorf("AtDepth(%q) = %d, want %d", path, got, want)
		}
	}
}

// The addresses of what a read reached come back out of its answer, which is what a command
// wanting nodes and --paths read: the path fixes some segments and the answer carries the rest.
func TestReachedRecoversTheAddresses(t *testing.T) {
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"demo.pr", []string{`demo.pr`}},
		{"demo.pr.*", []string{`demo.pr."1"`, `demo.pr."2"`, `demo.pr."a.b"`}},
		{"demo.pr.1", []string{`demo.pr."1"`}},
		{"demo.*.1", []string{`demo.pr."1"`}},
		{"*.pr", []string{`demo.pr`}},
		{"demo.pr.1.stage", []string{`demo.pr."1".stage`}},
		{"demo.pr.*.stage", []string{`demo.pr."1".stage`, `demo.pr."2".stage`, `demo.pr."a.b".stage`}},
		{"*.pr.*.stage", []string{`demo.pr."1".stage`, `demo.pr."2".stage`, `demo.pr."a.b".stage`}},
	} {
		var got []string
		for _, e := range read(t, fixture(), tc.path).Reached(at(t, tc.path)) {
			got = append(got, e.Ref.String())
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q reached = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// --paths walks the answer by the path that was asked: `demo.pr.*.stage` prints the stages, not
// the prs, and a path naming one node prints what is beneath it.
func TestPathsWalkTheAnswerByThePath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"demo.pr.*.stage", []string{`demo.pr."1".stage`, `demo.pr."2".stage`, `demo.pr."a.b".stage`}},
		{"demo.pr.1", []string{`demo.pr."1".stage`}},
		{"demo.*", []string{`demo.pr`, `demo.pr."1"`, `demo.pr."1".stage`, `demo.pr."2"`, `demo.pr."2".stage`, `demo.pr."a.b"`, `demo.pr."a.b".stage`}},
	} {
		var got []string
		for _, segs := range read(t, fixture(), tc.path).Paths(at(t, tc.path), 0) {
			got = append(got, RefAt(segs...).String())
		}
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
		Ref:     RefAt("merge", "proposal", "X"),
		Payload: ir.FromMap(map[string]*ir.Node{"votes": votes}),
	}}

	if want := `{choice: yes voter: scott}`; flatten(t, read(t, ents, "merge.proposal.X.votes(scott)").Node) != want {
		t.Fatalf("votes(scott) = %s, want %s", flatten(t, read(t, ents, "merge.proposal.X.votes(scott)").Node), want)
	}
	if d := read(t, ents, "merge.proposal.X.votes(nobody)"); d.Node != nil {
		t.Fatalf("votes(nobody) answered %s, want nothing", flatten(t, d.Node))
	}
	if got := flatten(t, read(t, ents, "merge.proposal.X.votes[0].choice").Node); got != "yes" {
		t.Fatalf("votes[0].choice = %s, want yes", got)
	}
}

// A wildcard of each kind reaches only what has that kind: `(*)` the elements of a keyed array,
// `[*]` those of any array, `*` the fields of an object — and over the wrong kind, nothing
// (git-issue anbz58g6h12krp0sn5n0).
func TestAWildcardReachesOnlyItsOwnKind(t *testing.T) {
	votes := ir.FromSlice([]*ir.Node{
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("scott"), "choice": ir.FromString("yes")}),
	}).WithTag("!key(voter)")
	ents := []Entity{{
		Ref:     RefAt("merge", "proposal", "X"),
		Payload: ir.FromMap(map[string]*ir.Node{"votes": votes}),
	}}
	for path, want := range map[string]string{
		"merge.proposal.X.votes(*).choice": "[yes]",
		"merge.proposal.X.votes[*].choice": "[yes]",
		"merge.proposal.*.votes(*).choice": "{X: [yes]}",
	} {
		if got := flatten(t, read(t, ents, path).Node); got != want {
			t.Errorf("%s = %s, want %s", path, got, want)
		}
	}
	for _, path := range []string{"merge.proposal[*]", "merge.proposal(*)", "merge.proposal{*}", "merge.proposal.X.votes.*"} {
		if out, reached, err := Reach(docOf(ents), at(t, path), nil); err != nil || reached || out != nil {
			t.Errorf("%s answered %v (reached=%v, err=%v), want nothing", path, out, reached, err)
		}
	}
}

// An unkeyed array says so rather than answering no: `(scott)` over it is a question the data
// cannot be asked, not one whose answer is nothing.
func TestAKeyOnAnUnkeyedArrayIsAnError(t *testing.T) {
	ents := []Entity{{
		Ref:     RefAt("merge", "proposal", "X"),
		Payload: ir.FromMap(map[string]*ir.Node{"votes": ir.FromSlice([]*ir.Node{ir.FromString("scott")})}),
	}}
	if _, _, err := Reach(docOf(ents), at(t, "merge.proposal.X.votes(scott)"), nil); err == nil {
		t.Fatal("a keyed lookup into an unkeyed array was answered rather than refused")
	}
}

// The codec is the document itself, with nothing wrapped around it.
func TestDocIsItsOwnWire(t *testing.T) {
	d := read(t, fixture(), "demo.pr.1")
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
	ref := RefAt("merge", "proposal", "X")
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

	node, ok, err := s.ReadAt(ref.Segs())
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	out, _, err := Reach(node, at(t, "votes(scott)"), nil)
	if err != nil {
		t.Fatalf("reach: %v", err)
	}
	if got := Str(out, "choice"); got != "yes" {
		t.Fatalf("votes(scott).choice = %q, want yes — the stored array did not keep its key", got)
	}
}
