package entity

import (
	"sort"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// leavesAt collects what walkLeaves reports, as `segs=value`, so a test can say which leaves a
// walk found rather than which it happened to descend to.
func leavesAt(n *ir.Node, depth int) []string {
	var out []string
	walkLeaves(n, depth, func(segs []string, v *ir.Node) {
		out = append(out, strings.Join(segs, ".")+"="+v.Type.String())
	})
	sort.Strings(out)
	return out
}

// A VALUE ABOVE THE DEPTH ASKED FOR IS STILL THERE. Only an object has anything below it, so a
// walk that descends past a scalar has walked past the whole of what stands at that path — and
// a listing built on it answers that a written value is not there.
//
// It is asymmetric with a null on purpose. A Match at a path nothing has been written to answers
// null, so reading one as a value would invent an entity at every prefix anybody asks about; at
// the depth the caller NAMED a null is an ordinary payload, because the caller named that path.
func TestAWalkKeepsAValueAboveTheDepthAsked(t *testing.T) {
	n := doc(t, `{cc: {n: 1}, zz: true, nn: null, deep: {a: {b: 2}}}`)
	got := leavesAt(n, 2)
	want := []string{"cc.n=Number", "deep.a=Object", "zz=Bool"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("walkLeaves(depth 2) = %v, want %v", got, want)
	}
	// At the depth the caller named, a null IS the answer about that path.
	if got := leavesAt(doc(t, `{a: null}`), 1); len(got) != 1 || got[0] != "a=Null" {
		t.Errorf("walkLeaves(depth 1) over {a: null} = %v, want [a=Null]", got)
	}
}

// THE STORE ANSWERS ABOUT WHAT IT HOLDS. A value two segments down is a value two segments
// down: the write lands there, a read at that address answers with it, and the listing the
// document is built from carries it — which is what makes `verse data` show a write back.
func TestAShortAddressIsReadBack(t *testing.T) {
	s := openStore(t)
	set(t, s, RefAt("t2", "zz"), doc(t, "true"))   // a scalar, two segments down
	set(t, s, RefAt("t2", "cc"), doc(t, "{n: 1}")) // an object at the same depth
	set(t, s, RefAt("demo", "pr", "1"), doc(t, "{stage: open}"))

	if e, ok, err := s.Get(RefAt("t2", "zz")); err != nil || !ok || !e.Payload.DeepEqual(doc(t, "true")) {
		t.Fatalf("Get(t2.zz) = (%+v, %v, %v), want the value written there", e, ok, err)
	}
	held := map[string]bool{}
	for _, e := range s.List(nil) {
		held[e.Ref.String()] = true
	}
	for _, want := range []Ref{RefAt("t2", "zz"), RefAt("demo", "pr", "1")} {
		if !held[want.String()] {
			t.Errorf("the listing lost %s; it holds %v", want, held)
		}
	}
}

// AND A PATH NOBODY WROTE TO NAMES NOTHING. The walk reports what stands somewhere; it does not
// invent an entity at each level of a path that was merely asked about — which is the failure
// mode of reading a Match's null as a value, since that is what an empty path answers.
func TestAnUnwrittenPathNamesNothing(t *testing.T) {
	s := openStore(t)
	set(t, s, RefAt("demo", "pr", "1"), doc(t, "{stage: open}"))
	if _, ok, err := s.Get(RefAt("nope", "here")); err != nil || ok {
		t.Errorf("Get(nope.here) = (ok=%v, %v), want nothing standing there", ok, err)
	}
	// A prefix that HAS something under it answers with what is under it, at every depth —
	// which is what a subtree read is, and is not this question.
	want := RefAt("demo", "pr", "1").String()
	for _, e := range s.List(nil) {
		if e.Ref.String() != want {
			t.Errorf("the listing invented %s, beside the one entity written", e.Ref)
		}
	}
}
