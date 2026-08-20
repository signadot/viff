package entity

import (
	"testing"
	"time"
)

// A WATCH REPORTS AT THE DEPTH ITS CALLER NAMED, because nothing else can know it: a document
// does not say where its entities end, so "which entity changed" is a question with a parameter
// (docs/working/entity-tree.md).
//
// It used to answer at the fixed arity, and the cost was not a rounding error. A derived entity
// stands at its target, the rule that derived it, and the subject's own segments — six segments
// where a rule keeps two levels — so a write to one arrived as a change to its THREE-segment
// ancestor. Every consumer comparing the reported address to one it already had was then
// comparing an ancestor with a descendant.
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
