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

	ch, unsub, err := s.WatchSubtree(3, "verse-dev", "drift")
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

// AN OBSERVER ASKS AT ITS OWN PATTERNS, and gets the nodes they reach: the same commit answers
// the four-segment question with the node written and the three-segment one with the node above
// it, whole. Neither is the stream picking a depth.
func TestAnObserverIsAnsweredAtItsOwnPatterns(t *testing.T) {
	s := openStore(t)
	deepCh, unsubDeep := s.Observe(PrefixPattern([]string{"verse-dev", "drift"}, 4))
	defer unsubDeep()
	aboveCh, unsubAbove := s.Observe(PrefixPattern([]string{"verse-dev", "drift"}, 3))
	defer unsubAbove()
	time.Sleep(200 * time.Millisecond) // the root watch opens lazily, on the first observer

	deep := RefAt("verse-dev", "drift", "head", "abc")
	set(t, s, deep, Obj{"sha": "deadbeef"}.Payload())

	c := oneCommitOf(t, deepCh, "the deep write")
	if len(c.Changes) != 1 || c.Changes[0].Ref != deep {
		t.Fatalf("at the write's own depth: %+v, want [%s]", c.Changes, deep)
	}
	c = oneCommitOf(t, aboveCh, "the same write, asked about one level up")
	want := RefAt("verse-dev", "drift", "head")
	if len(c.Changes) != 1 || c.Changes[0].Ref != want || Get(c.Changes[0].To, "abc") == nil {
		t.Fatalf("one level up: %+v, want [%s] holding what changed inside it", c.Changes, want)
	}
	// And a commit answers about a path it was not observed at, from the delta it carries.
	if !c.Changed(deep) || !c.Changed(want) || c.Changed(RefAt("verse-dev", "drift", "other")) {
		t.Fatal("Changed did not answer the write, the node above it, and not a sibling")
	}
}

// ONE LENGTH, WITH OR WITHOUT A DELTA. A subtree watch's commit carries no delta, so Changed
// answers from its Changes, and the path it is asked about carries `verse` as a segment like any
// other. Counted without the root, every address in verse's own half was one level short, and
// the causal chain, which asks this of every firing, began afresh at each one.
func TestChangedCountsTheRootWithoutADelta(t *testing.T) {
	s := openStore(t)
	ref := VerseRef("task", "instance", "run-1")
	segs := ref.Segs()
	ch, unsub, err := s.WatchSubtree(len(segs), segs[:len(segs)-1]...)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer unsub()

	set(t, s, ref, Obj{"status": "running"}.Payload())
	c := oneCommitOf(t, ch, "the write in verse's half")
	if !c.Changed(ref) {
		t.Fatalf("Changed(%s) = false over Changes %+v", ref, c.Changes)
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
	ch, unsub, err := s.WatchSubtree(3, "demo", "pr", "1")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer unsub()
	if ev := reader(ch).next(t, "the replay of the scalar standing at the subject"); ev.Ref != ref || ev.To.String != "closed" {
		t.Fatalf("event = %s %+v, want %s holding closed", ev.Ref, ev.To, ref)
	}
}

// A WRITE REACHES EVERY WATCH WHOSE PATH REACHES IT, and each reports it at the node its own
// path names: a watch at `a b` at `a.b`, one at `a` at `a`, holding it. A watch that answered
// less would miss a write that replaced what a rule reads (git-issue ga22z20vh12krhbcn1n0).
func TestAWriteReachesEveryWatchWhosePathReachesIt(t *testing.T) {
	s := openStore(t)
	ref := RefAt("a", "b")
	type face struct {
		w  *watch
		at Ref
	}
	root, unsubRoot := s.Observe(PrefixPattern(nil, 2))
	defer unsubRoot()
	watches := map[string]face{"the root stream": {reader(root), ref}}
	for _, prefix := range [][]string{{"a"}, {"a", "b"}} {
		ch, unsub, err := s.WatchSubtreeAt(len(prefix), true, prefix...)
		if err != nil {
			t.Fatalf("watch %v: %v", prefix, err)
		}
		defer unsub()
		watches["the watch at "+JoinSegs(prefix)] = face{reader(ch), RefAt(prefix...)}
	}
	time.Sleep(200 * time.Millisecond) // the root watch opens lazily, on the first observer

	mustWrite(t, s, Write{At: ref.At(), Value: ir.FromString("c")})
	for name, f := range watches {
		if ev := f.w.next(t, name+" to report the write"); ev.Ref != f.at || ev.Op != Set {
			t.Errorf("%s: event = %s %v, want a set at %s", name, ev.Ref, ev.Op, f.at)
		}
	}
	mustWrite(t, s, Write{At: ref.At()})
	for name, f := range watches {
		ev := f.w.next(t, name+" to report the removal")
		if ev.Ref != f.at {
			t.Errorf("%s: event = %s %v, want it at %s", name, ev.Ref, ev.Op, f.at)
		}
		if f.at == ref && ev.Op != Delete {
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
