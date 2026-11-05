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
		for _, addr := range read(t, fixture(), tc.path).Paths(at(t, tc.path), Unbounded) {
			got = append(got, addr.KPath())
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q paths = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// `..` REACHES EVERY DEPTH BENEATH IT, the node itself included, and the answer nests each match
// where it stands — in full, since matches at different depths would otherwise collide.
func TestADescentReachesEveryDepth(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{"demo.pr.1..", `{stage: open}`},
		{"count..", `{n: {hits: 42 nothing: null}}`},
		// Each match at its own path: the stages of three prs, under the ids they stand at.
		{"demo..stage", `{pr: {"1": {stage: open} "2": {stage: reviewed} a.b: {stage: open}}}`},
	} {
		if got := flatten(t, read(t, fixture(), tc.path).Node); got != tc.want {
			t.Errorf("%s = %s, want %s", tc.path, got, tc.want)
		}
	}
	// A filter is asked of every node the descent reaches, and what it keeps nests where it
	// stands — which is what `verse data '..' -if X` answers.
	open := func(n *ir.Node) (*ir.Node, bool, error) { return n, Str(n, "stage") == "open", nil }
	out, reached, err := Reach(docOf(fixture()), at(t, "demo.."), open)
	if err != nil || !reached {
		t.Fatalf("demo.. with a filter: reached=%v err=%v", reached, err)
	}
	if want := `{pr: {"1": {stage: open} a.b: {stage: open}}}`; flatten(t, out) != want {
		t.Errorf("demo.. -if {stage: open} = %s, want %s", flatten(t, out), want)
	}
	// A path that reaches nothing is still nothing, `..` included.
	if out, reached, err := Reach(docOf(fixture()), at(t, "nosuch.."), nil); err != nil || reached || out != nil {
		t.Errorf("nosuch.. = %v (reached=%v, err=%v), want nothing", out, reached, err)
	}
	// And every address in the answer comes back, once each.
	var got []string
	for _, addr := range read(t, fixture(), "demo.pr..").Paths(at(t, "demo.pr.."), Unbounded) {
		got = append(got, addr.KPath())
	}
	sort.Strings(got)
	want := []string{`demo.pr`, `demo.pr."1"`, `demo.pr."1".stage`, `demo.pr."2"`,
		`demo.pr."2".stage`, `demo.pr."a.b"`, `demo.pr."a.b".stage`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("demo.pr.. paths = %v, want %v", got, want)
	}
}

// `(key)` reaches into an array by identity, which no verse address could do before: a
// proposal's votes are addressable one voter at a time.
func TestAKeyedArrayIsAddressable(t *testing.T) {
	votes := ir.FromSlice([]*ir.Node{
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("walter"), "choice": ir.FromString("yes")}),
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("ana"), "choice": ir.FromString("no")}),
	}).WithTag("!key(voter)")
	ents := []Entity{{
		Ref:     RefAt("merge", "proposal", "X"),
		Payload: ir.FromMap(map[string]*ir.Node{"votes": votes}),
	}}

	if want := `{choice: yes voter: walter}`; flatten(t, read(t, ents, "merge.proposal.X.votes(walter)").Node) != want {
		t.Fatalf("votes(walter) = %s, want %s", flatten(t, read(t, ents, "merge.proposal.X.votes(walter)").Node), want)
	}
	if d := read(t, ents, "merge.proposal.X.votes(nobody)"); d.Node != nil {
		t.Fatalf("votes(nobody) answered %s, want nothing", flatten(t, d.Node))
	}
	if got := flatten(t, read(t, ents, "merge.proposal.X.votes[0].choice").Node); got != "yes" {
		t.Fatalf("votes[0].choice = %s, want yes", got)
	}
}

// A LISTING REACHES WHAT A READ REACHES, elements included. It used to stop at an array, so
// `--paths` showed less than the address model could address: a read and a write both take
// `votes[0]`, and the listing that is supposed to say what is there did not mention it.
//
// AN ELEMENT IS NAMED BY POSITION, which is the one spelling now: identity is the store's
// (!logd-key), a `!key` tag is refused on the way in (TestAKeyTagIsRefusedOnAWrite), and the
// verse surface for the store's keying is not designed yet. 6a04ga4nh12krh87ndn0 is the watch's
// silence at these addresses, which is a separate question.
func TestPathsReachIntoArrays(t *testing.T) {
	votes := ir.FromSlice([]*ir.Node{
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("walter"), "choice": ir.FromString("yes")}),
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("ana"), "choice": ir.FromString("no")}),
	}).WithTag("!key(voter)")
	plain := ir.FromSlice([]*ir.Node{ir.FromString("a"), ir.FromString("b")})
	ents := []Entity{{
		Ref:     RefAt("merge", "proposal", "X"),
		Payload: ir.FromMap(map[string]*ir.Node{"votes": votes, "tags": plain}),
	}}

	var got []string
	for _, addr := range read(t, ents, "merge.proposal.X").Paths(at(t, "merge.proposal.X"), Unbounded) {
		got = append(got, addr.KPath())
	}
	sort.Strings(got)
	want := []string{
		`merge.proposal.X.tags`,
		`merge.proposal.X.tags[0]`,
		`merge.proposal.X.tags[1]`,
		`merge.proposal.X.votes`,
		`merge.proposal.X.votes[0]`,
		`merge.proposal.X.votes[0].choice`,
		`merge.proposal.X.votes[0].voter`,
		`merge.proposal.X.votes[1]`,
		`merge.proposal.X.votes[1].choice`,
		`merge.proposal.X.votes[1].voter`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("paths =\n%v\nwant\n%v", got, want)
	}

	// EVERY ADDRESS IT PRINTS IS ONE A READ TAKES, which is the whole contract of a listing.
	for _, kp := range got {
		if d := read(t, ents, kp); d.Node == nil {
			t.Errorf("%s was listed and reads as nothing", kp)
		}
	}

	// AN ACCESS IS A STEP, so --depth counts it as one and reads in the units a reader types:
	// one level beneath `merge proposal X` is its fields, two is their elements.
	for _, tc := range []struct {
		depth int
		want  []string
	}{
		{1, []string{`merge.proposal.X.tags`, `merge.proposal.X.votes`}},
		{2, []string{
			`merge.proposal.X.tags`,
			`merge.proposal.X.tags[0]`,
			`merge.proposal.X.tags[1]`,
			`merge.proposal.X.votes`,
			`merge.proposal.X.votes[0]`,
			`merge.proposal.X.votes[1]`,
		}},
	} {
		var bounded []string
		for _, addr := range read(t, ents, "merge.proposal.X").Paths(at(t, "merge.proposal.X"), tc.depth) {
			bounded = append(bounded, addr.KPath())
		}
		sort.Strings(bounded)
		if !reflect.DeepEqual(bounded, tc.want) {
			t.Errorf("--depth %d paths =\n%v\nwant\n%v", tc.depth, bounded, tc.want)
		}
	}
}

// A wildcard of each kind reaches only what has that kind: `(*)` the elements of a keyed array,
// `[*]` those of any array, `*` the fields of an object — and over the wrong kind, nothing
// (git-issue anbz58g6h12krp0sn5n0).
func TestAWildcardReachesOnlyItsOwnKind(t *testing.T) {
	votes := ir.FromSlice([]*ir.Node{
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("walter"), "choice": ir.FromString("yes")}),
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

// An unkeyed array says so rather than answering no: `(walter)` over it is a question the data
// cannot be asked, not one whose answer is nothing.
func TestAKeyOnAnUnkeyedArrayIsAnError(t *testing.T) {
	ents := []Entity{{
		Ref:     RefAt("merge", "proposal", "X"),
		Payload: ir.FromMap(map[string]*ir.Node{"votes": ir.FromSlice([]*ir.Node{ir.FromString("walter")})}),
	}}
	if _, _, err := Reach(docOf(ents), at(t, "merge.proposal.X.votes(walter)"), nil); err == nil {
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

// A `!key` TAG IS REFUSED ON A WRITE, because keying is the store's to hold.
//
// It used to land: `!key` is a merge operator, so an ordinary write executes it and keeps the
// merged array untagged, and a payload written as data (Raw) kept the annotation — which is how
// an array came to be positional in the store while its value claimed a name for each element.
// Two keyings for one array, and only verse could read the second.
//
// logd keys an array in the SCHEMA (!logd-key), which is what makes an element addressable by
// identity, refuses a position on a keyed array, and rewrites the elements when the keying
// changes. Surfacing that in verse is not designed yet; until it is, an array is positional and
// its elements are addressed by position, which is what every path in a listing now spells.
func TestAKeyTagIsRefusedOnAWrite(t *testing.T) {
	s := openStore(t)
	votes := ir.FromSlice([]*ir.Node{
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("walter"), "choice": ir.FromString("yes")}),
	}).WithTag("!key(voter)")
	for _, tc := range []struct {
		name  string
		value Payload
	}{
		{"as data", Raw(ir.FromMap(map[string]*ir.Node{"votes": votes}))},
		{"as an ordinary write", ir.FromMap(map[string]*ir.Node{"votes": votes})},
		{"at the value itself", Raw(votes)},
	} {
		ok, _, err := s.Write(Write{At: RefAt("merge", "proposal", "X").At(), Value: tc.value})
		if err == nil || ok {
			t.Errorf("%s: committed=%v err=%v, want a refusal", tc.name, ok, err)
			continue
		}
		if !strings.Contains(err.Error(), "keying is the store's to hold") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// And nothing of it landed.
	if _, ok, err := s.ReadAt([]string{"merge", "proposal", "X"}); ok || err != nil {
		t.Errorf("a refused write left something: ok=%v err=%v", ok, err)
	}
}
