package entity

import (
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
)

// A WATCH REPORTS AT THE DEPTH ITS CALLER NAMED, because nothing else can know it: a document
// does not say where its entities end, so "which entity changed" is a question with a parameter
// (docs/working/entity-tree.md).
//
// A FIXED ARITY IS NOT A ROUNDING ERROR HERE. A derived entity stands at its target, the rule
// that derived it, and the subject's own segments — six segments where a rule keeps two levels —
// so answering at three delivers a write to one as a change to its ancestor, and every consumer
// comparing the reported address to one it already holds compares an ancestor with a
// descendant.
func TestWatchSubtreeReportsAtTheDepthItWasAsked(t *testing.T) {
	s := openStore(t)
	deep := RefAt("verse-dev", "drift", "head", "abc")

	ch, unsub, err := s.WatchSubtree(4, "verse-dev", "drift")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer unsub()

	set(t, s, deep, Obj{"sha": "deadbeef"}.Payload())
	ev := oneChange(t, ch, "the deep write")
	if ev.Ref != deep {
		t.Fatalf("reported %s, want %s — the watch answered at some other depth", ev.Ref, deep)
	}
	if Str(ev.To, "sha") != "deadbeef" {
		t.Errorf("To = %+v, want the entity's own payload", ev.To)
	}
}

// The SAME subtree, watched at the ordinary arity, reports the three-segment ancestor and
// carries the rest as payload. Both answers are correct; they are answers to different
// questions, which is the whole of what "entity-hood is a lens" costs.
func TestTwoDepthsOverOneSubtreeAreTwoQuestions(t *testing.T) {
	s := openStore(t)
	deep := RefAt("verse-dev", "drift", "head", "abc")

	ch, unsub, err := s.WatchSubtree(Depth, "verse-dev", "drift")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer unsub()

	set(t, s, deep, Obj{"sha": "deadbeef"}.Payload())
	ev := oneChange(t, ch, "the deep write seen at the ordinary arity")
	if want := RefAt("verse-dev", "drift", "head"); ev.Ref != want {
		t.Fatalf("reported %s, want the three-segment ancestor %s", ev.Ref, want)
	}
	if inner := Get(ev.To, "abc"); inner == nil {
		t.Fatalf("To = %+v, want the deeper structure inside it", ev.To)
	}
}

// ChangedAt is the same question asked of a COMMIT rather than of a watch, and it is what lets
// the root stream — which serves every consumer at once and so must pick one arity — still
// answer exactly for a consumer whose addresses are deeper.
func TestChangedAtAnswersAtTheDepthAsked(t *testing.T) {
	s := openStore(t)
	ch, unsub := s.Observe()
	defer unsub()

	deep := RefAt("verse-dev", "drift", "head", "abc")
	set(t, s, deep, Obj{"sha": "deadbeef"}.Payload())

	c := oneCommitOf(t, ch, "the deep write")
	if got := c.ChangedAt(4); len(got) != 1 || got[0] != deep {
		t.Fatalf("ChangedAt(4) = %v, want [%s]", got, deep)
	}
	if want := RefAt("verse-dev", "drift", "head"); len(c.ChangedAt(3)) != 1 || c.ChangedAt(3)[0] != want {
		t.Fatalf("ChangedAt(3) = %v, want [%s]", c.ChangedAt(3), want)
	}
	// And the ordinary Changes is the three-segment reading, which is what `ChangedAt(3)` says.
	if len(c.Changes) != 1 || c.Changes[0].Ref != RefAt("verse-dev", "drift", "head") {
		t.Fatalf("Changes = %+v, want the ordinary arity", c.Changes)
	}
}

// A WATCH ON AN ADDRESS REPORTS WHAT STANDS THERE, whatever it is. There is nothing left below
// the path to descend, so the value at it is the entity — and a watch that asked for an object
// there dropped every scalar, at any depth, while the read of the same address answered it
// (git-issue ke35500vh12krhbcn1n0).
func TestAWatchOnAnAddressReportsWhateverStandsThere(t *testing.T) {
	for _, segs := range [][]string{{"a", "b", "c"}, {"x", "y", "z", "w"}} {
		s := openStore(t)
		ref := RefAt(segs...)
		ch, unsub, err := s.WatchSubtreeAt(len(segs), true, segs...)
		if err != nil {
			t.Fatalf("watch %s: %v", ref, err)
		}
		r := reader(ch)
		mustWrite(t, s, Write{At: ref.At(), Value: ir.FromString("d")})
		if ev := r.next(t, "the scalar written at "+ref.String()); ev.Ref != ref || ev.Op != Set || ev.To.String != "d" {
			t.Fatalf("event = %s %v %+v, want %s set to d", ev.Ref, ev.Op, ev.To, ref)
		}
		mustWrite(t, s, Write{At: ref.At()})
		if ev := r.next(t, "its removal"); ev.Ref != ref || ev.Op != Delete {
			t.Fatalf("event = %s %v, want %s delete", ev.Ref, ev.Op, ref)
		}
		unsub()
	}

	// The same address watched as a rule watches its subject (trigger.Engine.untilMoot), opening
	// on what already stands there: the replay carries it.
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	mustWrite(t, s, Write{At: ref.At(), Value: ir.FromString("closed")})
	ch, unsub, err := s.WatchSubtree(Depth, "demo", "pr", "1")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer unsub()
	if ev := reader(ch).next(t, "the replay of the scalar standing at the subject"); ev.Ref != ref || ev.To.String != "closed" {
		t.Fatalf("event = %s %+v, want %s holding closed", ev.Ref, ev.To, ref)
	}
}

// A VALUE ABOVE THE REPORTING DEPTH IS REPORTED WHERE IT STANDS, on every watch that covers it —
// the root stream and a watch at any prefix of it — because the read of the same path lists it
// there (walkLeaves) and a stream answering less misses a write the listing beside it shows
// (git-issue ga22z20vh12krhbcn1n0).
func TestAValueAboveTheReportingDepthReachesEveryWatch(t *testing.T) {
	s := openStore(t)
	ref := RefAt("a", "b")
	root, unsubRoot := s.Observe()
	defer unsubRoot()
	watches := map[string]*watch{"the root stream": reader(root)}
	for _, prefix := range [][]string{{"a"}, {"a", "b"}} {
		ch, unsub, err := s.WatchSubtreeAt(Depth, true, prefix...)
		if err != nil {
			t.Fatalf("watch %v: %v", prefix, err)
		}
		defer unsub()
		watches["the watch at "+JoinSegs(prefix)] = reader(ch)
	}
	time.Sleep(200 * time.Millisecond) // the root watch opens lazily, on the first observer

	mustWrite(t, s, Write{At: ref.At(), Value: ir.FromString("c")})
	for name, w := range watches {
		if ev := w.next(t, name+" to report a.b"); ev.Ref != ref || ev.Op != Set || ev.To.String != "c" {
			t.Errorf("%s: event = %s %v %+v, want a.b set to c", name, ev.Ref, ev.Op, ev.To)
		}
	}
	mustWrite(t, s, Write{At: ref.At()})
	for name, w := range watches {
		if ev := w.next(t, name+" to report a.b removed"); ev.Ref != ref || ev.Op != Delete {
			t.Errorf("%s: event = %s %v, want a.b deleted", name, ev.Ref, ev.Op)
		}
	}
}

// oneCommitOf takes the next commit off a stream, for a test asking about the commit itself
// rather than about the one change in it.
func oneCommitOf(t *testing.T, ch <-chan Commit, why string) Commit {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatalf("commit stream closed waiting for %s", why)
		}
		return c
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", why)
	}
	return Commit{}
}
