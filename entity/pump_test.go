package entity

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// pumpsHeld is how many pumps the store holds now.
func pumpsHeld(s *LogdStore) int {
	s.pumpsMu.Lock()
	defer s.pumpsMu.Unlock()
	return len(s.pumps)
}

// Two subscribers on one prefix are one watch: the second opens nothing, and both hear the
// replay and the writes after it.
func TestTwoSubscribersOnOnePrefixOpenOneWatch(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"stage": "open"}.Payload())

	before := pumpOpens.Load()
	a, unsubA, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("first watch: %v", err)
	}
	defer unsubA()
	b, unsubB, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("second watch: %v", err)
	}
	defer unsubB()
	if n := pumpOpens.Load() - before; n != 1 {
		t.Fatalf("two watches on one prefix opened %d watches, want 1", n)
	}
	if n := pumpsHeld(s); n != 1 {
		t.Fatalf("the store holds %d pumps, want 1", n)
	}
	ra, rb := reader(a), reader(b)
	for name, r := range map[string]*watch{"first": ra, "second": rb} {
		if ev := r.next(t, name+"'s replay"); ev.Ref != ref || ev.Op != Set {
			t.Fatalf("%s replayed %s %v, want %s set", name, ev.Ref, ev.Op, ref)
		}
	}
	set(t, s, ref, Obj{"stage": "reviewed"}.Payload())
	for name, r := range map[string]*watch{"first": ra, "second": rb} {
		if ev := r.next(t, name+"'s write"); Str(ev.To, "stage") != "reviewed" {
			t.Fatalf("%s saw stage=%s, want reviewed", name, Str(ev.To, "stage"))
		}
	}
}

// A subscriber that joins a pump already open is handed the slice from the pump's copy: the
// whole of it, as of the pump's commit, with no watch opened for it.
func TestALateJoinerIsSeededFromThePump(t *testing.T) {
	s := openStore(t)
	set(t, s, RefAt("demo", "pr", "1"), Obj{"stage": "open"}.Payload())
	a, unsubA, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("first watch: %v", err)
	}
	defer unsubA()
	ra := reader(a)
	ra.next(t, "the first's replay")
	set(t, s, RefAt("demo", "pr", "2"), Obj{"stage": "open"}.Payload())
	second := ra.next(t, "the first's second entity")

	before := pumpOpens.Load()
	b, unsubB, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("late watch: %v", err)
	}
	defer unsubB()
	if n := pumpOpens.Load() - before; n != 0 {
		t.Fatalf("a late joiner opened %d watches, want none", n)
	}
	select {
	case c := <-b:
		if len(c.Changes) != 2 {
			t.Fatalf("the late joiner's picture carries %d changes, want both entities: %+v", len(c.Changes), c.Changes)
		}
		if c.Rev < second.Rev {
			t.Fatalf("the late joiner's picture stands at %d, behind the write at %d it includes", c.Rev, second.Rev)
		}
		if !c.resync {
			t.Fatal("an opening picture is a resync, and the late joiner's says it is not")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the late joiner got no picture")
	}
}

// A drop re-establishes one watch for the prefix, whatever the number of its subscribers, and
// every one of them is live after it.
func TestADropReestablishesOnePumpForItsSubscribers(t *testing.T) {
	docd := startDocd(t, startLogd(t))
	g := newGate(t, docd.ClientTCPAddr())
	s := openStoreAt(t, g.addr())
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"stage": "open"}.Payload())

	var rs []*watch
	for i := 0; i < 3; i++ {
		ch, unsub, err := s.WatchSubtree(3, "demo", "pr")
		if err != nil {
			t.Fatalf("watch %d: %v", i, err)
		}
		t.Cleanup(unsub)
		r := reader(ch)
		r.next(t, fmt.Sprintf("subscriber %d's replay", i))
		rs = append(rs, r)
	}
	before := subtreeReopens.Load()
	g.cut()
	eventuallyWithin(t, 30*time.Second, "the pump re-established", func() bool {
		return subtreeReopens.Load() > before
	})
	// The rest of the round, if any, then a write every subscriber hears.
	eventuallyWithin(t, 10*time.Second, "the line drained", func() bool {
		return s.gate.waiting.Load() == 0 && s.gate.inFlight.Load() == 0
	})
	// One pump, re-established for all three. (A reopen can happen twice across a cut, so the
	// count is not asserted exact; that there is still one watch on the prefix is.)
	if n := pumpsHeld(s); n != 1 {
		t.Fatalf("after the drop the store holds %d pumps for one prefix, want 1", n)
	}
	set(t, s, ref, Obj{"stage": "reviewed"}.Payload())
	for i, r := range rs {
		if ev := r.next(t, fmt.Sprintf("subscriber %d's write after the drop", i)); Str(ev.To, "stage") != "reviewed" {
			t.Fatalf("subscriber %d saw stage=%s after the drop, want reviewed", i, Str(ev.To, "stage"))
		}
	}
}

// A subscriber leaving leaves the pump and the others' streams standing.
func TestASubscriberLeavingLeavesTheOthersStream(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"stage": "open"}.Payload())
	a, unsubA, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("first watch: %v", err)
	}
	b, unsubB, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("second watch: %v", err)
	}
	defer unsubB()
	reader(a).next(t, "the first's replay")
	rb := reader(b)
	rb.next(t, "the second's replay")

	unsubA()
	if _, ok := <-a; ok {
		t.Fatal("the leaver's stream did not close")
	}
	if n := pumpsHeld(s); n != 1 {
		t.Fatalf("after one of two left, the store holds %d pumps, want the one", n)
	}
	set(t, s, ref, Obj{"stage": "reviewed"}.Payload())
	if ev := rb.next(t, "the second's write after the first left"); Str(ev.To, "stage") != "reviewed" {
		t.Fatalf("the second saw stage=%s, want reviewed", Str(ev.To, "stage"))
	}
}

// A subscriber whose channel is full stalls nobody and loses nothing: the commits it cannot
// take yet wait in its backlog, and it reads every one of them, in order, once it reads.
func TestASlowSubscriberReadsEveryCommitFromItsBacklog(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	slow, unsubSlow, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("slow watch: %v", err)
	}
	defer unsubSlow()
	fast, unsubFast, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("fast watch: %v", err)
	}
	defer unsubFast()
	n := cap(slow) + 50
	rf := reader(fast)
	for i := 0; i < n; i++ {
		set(t, s, ref, Obj{"n": fmt.Sprint(i)}.Payload())
		rf.next(t, fmt.Sprintf("the fast one's commit %d", i))
	}
	rs := reader(slow)
	for i := 0; i < n; i++ {
		if ev := rs.next(t, fmt.Sprintf("the slow one's commit %d", i)); Str(ev.To, "n") != fmt.Sprint(i) {
			t.Fatalf("the slow one saw n=%s at commit %d, want every commit in order", Str(ev.To, "n"), i)
		}
	}
	rs.quiet(t, "the slow one heard more than was written")
}

// A subscriber whose backlog is full too is caught up with the net of what it missed, marked as
// a resync, behind the backlog it holds, once it reads; the other streams on meanwhile.
func TestAFullSubscriberIsReseededWhileTheOtherStreamsOn(t *testing.T) {
	defer func(n int) { pumpBacklog = n }(pumpBacklog)
	pumpBacklog = 20
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	slow, unsubSlow, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("slow watch: %v", err)
	}
	defer unsubSlow()
	fast, unsubFast, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("fast watch: %v", err)
	}
	defer unsubFast()

	// More commits than the slow one's channel and backlog hold, each read by the fast one as
	// it lands.
	n := cap(slow) + pumpBacklog + 50
	rf := reader(fast)
	for i := 0; i < n; i++ {
		set(t, s, ref, Obj{"n": fmt.Sprint(i)}.Payload())
		if ev := rf.next(t, fmt.Sprintf("the fast one's commit %d", i)); Str(ev.To, "n") != fmt.Sprint(i) {
			t.Fatalf("the fast one saw n=%s at commit %d: a commit was merged away or lost", Str(ev.To, "n"), i)
		}
	}
	// The slow one reads: what it buffered, then a resync carrying the net — the last state.
	var last Commit
	deadline := time.After(5 * time.Second)
	for Str(lastTo(last), "n") != fmt.Sprint(n-1) {
		select {
		case c, ok := <-slow:
			if !ok {
				t.Fatal("the slow one's stream closed")
			}
			last = c
		case <-deadline:
			t.Fatalf("the slow one never reached n=%d; last %+v", n-1, last)
		}
	}
	if !last.resync {
		t.Fatal("the commit that caught the slow one up does not say it is a resync")
	}
}

// lastTo is the level of the one entity a commit carries, or nil for none.
func lastTo(c Commit) Payload {
	if len(c.Changes) == 0 {
		return nil
	}
	return c.Changes[len(c.Changes)-1].To
}

// A pump nobody joined twice ends with its one subscriber; one that was joined twice lingers
// a moment, for the rule replaced under its name that reopens the same prefix at once.
func TestAPumpEndsWithItsLastSubscriberOrLingers(t *testing.T) {
	defer func(d time.Duration) { pumpLinger = d }(pumpLinger)
	pumpLinger = 2 * time.Second // also the window the joiner below has to arrive in
	s := openStore(t)

	_, unsub, err := s.WatchSubtree(3, "demo", "alone")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	unsub()
	if n := pumpsHeld(s); n != 0 {
		t.Fatalf("a pump nobody joined twice stands after its subscriber left (%d held)", n)
	}

	_, unsubA, err := s.WatchSubtree(3, "demo", "shared")
	if err != nil {
		t.Fatalf("first watch: %v", err)
	}
	_, unsubB, err := s.WatchSubtree(3, "demo", "shared")
	if err != nil {
		t.Fatalf("second watch: %v", err)
	}
	unsubA()
	unsubB()
	if n := pumpsHeld(s); n != 1 {
		t.Fatalf("a pump joined twice ended at once (%d held)", n)
	}
	// A joiner in the linger finds the pump, and opens nothing.
	before := pumpOpens.Load()
	_, unsubC, err := s.WatchSubtree(3, "demo", "shared")
	if err != nil {
		t.Fatalf("watch in the linger: %v", err)
	}
	if n := pumpOpens.Load() - before; n != 0 {
		t.Fatalf("a joiner in the linger opened %d watches, want none", n)
	}
	unsubC()
	eventually(t, "the lingering pump ended", func() bool { return pumpsHeld(s) == 0 })
}

// Subscribers to one prefix with different patterns hear different things: the walk is per
// pattern, and a subscriber is answered at its own.
func TestSubscribersToOnePrefixAreAnsweredAtTheirOwnPatterns(t *testing.T) {
	s := openStore(t)
	all, unsubAll, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("watch of every pr: %v", err)
	}
	defer unsubAll()
	one, unsubOne, err := s.WatchPattern([]string{"demo", "pr"}, PrefixPattern([]string{"demo", "pr", "1"}, 3), true, false)
	if err != nil {
		t.Fatalf("watch of pr 1: %v", err)
	}
	defer unsubOne()
	if n := pumpsHeld(s); n != 1 {
		t.Fatalf("two patterns on one prefix hold %d pumps, want 1", n)
	}
	set(t, s, RefAt("demo", "pr", "2"), Obj{"stage": "open"}.Payload())
	set(t, s, RefAt("demo", "pr", "1"), Obj{"stage": "open"}.Payload())
	ra, r1 := reader(all), reader(one)
	if ev := ra.next(t, "pr 2 at the wide pattern"); ev.Ref != RefAt("demo", "pr", "2") {
		t.Fatalf("the wide pattern saw %s first, want demo.pr.2", ev.Ref)
	}
	if ev := ra.next(t, "pr 1 at the wide pattern"); ev.Ref != RefAt("demo", "pr", "1") {
		t.Fatalf("the wide pattern saw %s second, want demo.pr.1", ev.Ref)
	}
	if ev := r1.next(t, "pr 1 at the narrow pattern"); ev.Ref != RefAt("demo", "pr", "1") {
		t.Fatalf("the narrow pattern saw %s, want demo.pr.1 only", ev.Ref)
	}
	r1.quiet(t, "the narrow pattern heard of pr 2")
}

// A dropped subscriber's catch-up is the net since ITS last commit, not a picture diffed
// against nothing: an entity removed while it was full arrives as a Delete.
func TestADroppedSubscriberHearsADeleteInItsCatchUp(t *testing.T) {
	defer func(n int) { pumpBacklog = n }(pumpBacklog)
	pumpBacklog = 20
	s := openStore(t)
	doomed := RefAt("demo", "pr", "doomed")
	other := RefAt("demo", "pr", "other")
	slow, unsub, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer unsub()
	set(t, s, doomed, Obj{"stage": "open"}.Payload()) // in the buffer: the slow one will hold it
	for i := 0; i < cap(slow)+pumpBacklog+50; i++ {
		set(t, s, other, Obj{"n": fmt.Sprint(i)}.Payload())
	}
	remove(t, s, doomed) // past the buffer: only the catch-up can say so
	deadline := time.After(5 * time.Second)
	for {
		select {
		case c, ok := <-slow:
			if !ok {
				t.Fatal("the stream closed")
			}
			for _, ev := range c.Changes {
				if ev.Ref == doomed && ev.Op == Delete {
					if !c.resync {
						t.Fatal("the delete came in a commit that does not say it is a catch-up")
					}
					return
				}
			}
		case <-deadline:
			t.Fatal("the dropped subscriber never heard the delete")
		}
	}
}

// A progress subscriber that was full when its answer came is still answered: the mark rides
// its catch-up, at a commit at or past the head it asked about.
func TestADroppedProgressSubscriberIsStillAnswered(t *testing.T) {
	defer func(n int) { pumpBacklog = n }(pumpBacklog)
	pumpBacklog = 20
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	ch, unsub, progress, err := s.WatchSubtreeProgress(3, "demo", "pr")
	if err != nil {
		t.Fatalf("progress watch: %v", err)
	}
	defer unsub()
	for i := 0; i < cap(ch)+pumpBacklog+50; i++ {
		set(t, s, ref, Obj{"n": fmt.Sprint(i)}.Payload())
	}
	head, err := progress(t.Context())
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				t.Fatal("the stream closed")
			}
			if c.progress && c.Rev >= head {
				return
			}
		case <-deadline:
			t.Fatalf("no progress mark at or past %d reached the subscriber that was full", head)
		}
	}
}

// A store closing closes every subscriber's stream.
func TestAStoreClosingClosesEverySubscribersStream(t *testing.T) {
	s := openStore(t)
	a, _, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("first watch: %v", err)
	}
	b, _, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("second watch: %v", err)
	}
	s.Close()
	for name, ch := range map[string]<-chan Commit{"first": a, "second": b} {
		deadline := time.After(3 * time.Second)
		for open := true; open; {
			select {
			case _, open = <-ch:
			case <-deadline:
				t.Fatalf("the %s stream is still open after the store closed", name)
			}
		}
	}
}

// A subscriber that would rather be refused than wait (WatchSubtreeAt) is told what the
// pump's picture showed: nothing there is an error, and the pump it opened for nobody ends.
func TestAWatchOnNothingIsRefusedWhenAskedNotToWait(t *testing.T) {
	s := openStore(t)
	if _, _, err := s.WatchSubtreeAt(3, false, "demo", "nothing"); err == nil {
		t.Fatal("a watch on nothing, asked not to wait, was not refused")
	}
	eventually(t, "the refused watch's pump ended", func() bool { return pumpsHeld(s) == 0 })
	set(t, s, RefAt("demo", "something", "1"), Obj{"stage": "open"}.Payload())
	ch, unsub, err := s.WatchSubtreeAt(3, false, "demo", "something")
	if err != nil {
		t.Fatalf("a watch on something, asked not to wait: %v", err)
	}
	defer unsub()
	reader(ch).quiet(t, "a watch joining at the next change replayed the slice")
}

// A watch is answered at once, as a handle that says where the store stands with it: opening,
// then open, with Ready closed; a joiner to a pump already open is Open, and Ready, in the call.
func TestAWatchSaysItsPhases(t *testing.T) {
	s := openStore(t)
	set(t, s, RefAt("demo", "pr", "1"), Obj{"stage": "open"}.Payload())
	w, err := s.Watch([]string{"demo", "pr"}, PrefixPattern([]string{"demo", "pr"}, 3), WatchOptions{WaitIfAbsent: true})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer w.Close()
	select {
	case <-w.Ready:
	case <-time.After(5 * time.Second):
		st, _ := w.State()
		t.Fatalf("the watch never opened; it says %v", st)
	}
	if st, _ := w.State(); st.Phase != WatchOpen || st.Err != nil {
		t.Fatalf("after Ready the watch says %v, want open", st)
	}
	if c := <-w.Commits; len(c.Changes) != 1 {
		t.Fatalf("the opening picture carries %d changes, want the one entity", len(c.Changes))
	}

	late, err := s.Watch([]string{"demo", "pr"}, PrefixPattern([]string{"demo", "pr"}, 3), WatchOptions{WaitIfAbsent: true})
	if err != nil {
		t.Fatalf("late watch: %v", err)
	}
	defer late.Close()
	select {
	case <-late.Ready:
	default:
		t.Fatal("a joiner to an open pump is not Ready in the call")
	}
	if st, _ := late.State(); st.Phase != WatchOpen {
		t.Fatalf("a joiner to an open pump says %v, want open", st)
	}

	// A subscriber that asked not to wait on a path holding nothing Fails on the picture, with
	// the words, and its Ready never closes.
	none, err := s.Watch([]string{"demo", "nothing"}, PrefixPattern([]string{"demo", "nothing"}, 3), WatchOptions{})
	if err != nil {
		t.Fatalf("watch on nothing: %v", err)
	}
	defer none.Close()
	eventually(t, "the watch on nothing says Failed", func() bool {
		st, _ := none.State()
		return st.Phase == WatchFailed && st.Err != nil && strings.Contains(st.Err.Error(), "nothing stands there")
	})
	select {
	case <-none.Ready:
		t.Fatal("a Failed watch closed Ready")
	default:
	}
}

// A refusal is a phase, not an error: the store asks again behind the handle, and the watch
// opens when the store answers.
func TestARefusedWatchIsAskedAgainBehindItsHandle(t *testing.T) {
	docd := startDocd(t, startLogd(t))
	g := newGate(t, docd.ClientTCPAddr())
	s := openStoreAt(t, g.addr())
	set(t, s, RefAt("demo", "pr", "1"), Obj{"stage": "open"}.Payload())
	// New connections go nowhere, and the one standing is cut: the next establish is refused.
	g.point("127.0.0.1:1")
	g.cut()
	w, err := s.Watch([]string{"demo", "pr"}, PrefixPattern([]string{"demo", "pr"}, 3), WatchOptions{WaitIfAbsent: true})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer w.Close()
	eventuallyWithin(t, 30*time.Second, "the watch says Refused", func() bool {
		st, _ := w.State()
		return st.Phase == WatchRefused && st.Err != nil
	})
	select {
	case <-w.Ready:
		t.Fatal("Ready closed on a refused watch")
	default:
	}
	g.point(docd.ClientTCPAddr())
	select {
	case <-w.Ready:
	case <-time.After(30 * time.Second):
		st, _ := w.State()
		t.Fatalf("the refused watch never opened once the store answered; it says %v", st)
	}
	if c := <-w.Commits; len(c.Changes) != 1 {
		t.Fatalf("the opening picture carries %d changes, want the one entity", len(c.Changes))
	}
}
