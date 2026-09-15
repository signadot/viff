package entity

import (
	"sort"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// A LISTING ANSWERS EXACTLY ITS DEPTH. Only an object has anything below it, so a walk that
// meets a scalar on the way down has met the whole of what stands at that path — and that value
// is at ITS OWN path, which is where a read of it answers. A listing that reported it at the
// depth asked for would name a node that does not stand there.
func TestAListingAnswersExactlyItsDepth(t *testing.T) {
	n := doc(t, `{cc: {n: 1}, zz: true, nn: null, deep: {a: {b: 2}}}`)
	var got []string
	nodesAt(n, 2, func(segs []string, v *ir.Node) {
		got = append(got, strings.Join(segs, ".")+"="+v.Type.String())
	})
	sort.Strings(got)
	want := []string{"cc.n=Number", "deep.a=Object"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("nodesAt(depth 2) = %v, want %v — a scalar two levels up is not at this depth", got, want)
	}
	// At the depth the caller named, a null IS the answer about that path.
	got = nil
	nodesAt(doc(t, `{a: null}`), 1, func(segs []string, v *ir.Node) {
		got = append(got, strings.Join(segs, ".")+"="+v.Type.String())
	})
	if len(got) != 1 || got[0] != "a=Null" {
		t.Errorf("nodesAt(depth 1) over {a: null} = %v, want [a=Null]", got)
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
	// And the read of what holds it answers it too, at the key it stands at.
	node, ok, err := s.ReadAt([]string{"t2"})
	if err != nil || !ok || Get(node, "zz") == nil || Get(Get(node, "cc"), "n") == nil {
		t.Errorf("read at t2 = %v (ok=%v, %v), want both the scalar and the object under it", flatten(t, node), ok, err)
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
	for _, e := range s.ListAt([]string{"demo"}, 3, nil) {
		if e.Ref.String() != want {
			t.Errorf("the listing invented %s, beside the one entity written", e.Ref)
		}
	}
}
