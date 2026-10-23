package entity

import (
	"testing"
	"time"
)

// A QUIET, LIVE WATCH IS ASKED, NOT REPLACED. The heal that bounds how long a dead watch goes
// unnoticed (subtreeHealAfter) fires on silence, and silence is what an idle slice looks like
// too. Replacing the watch at every silence costs a State of the whole slice each time, and on
// staging, where most slices see one event a minute, that was ~13 States a second. Asked
// instead, a live watch answers, the pump stays on the stream it has, and the consumer hears
// nothing: not the ask's own progress event either, which is the pump's and not theirs. The
// watch is still a watch afterwards: a write lands.
func TestAQuietLiveWatchIsAskedNotReplaced(t *testing.T) {
	defer func(a, m time.Duration) { subtreeHealAfter, subtreeHealMax = a, m }(subtreeHealAfter, subtreeHealMax)
	subtreeHealAfter, subtreeHealMax = 40*time.Millisecond, 80*time.Millisecond

	s := openStore(t)
	before := subtreeReopens.Load()
	ch, unsub, err := s.WatchSubtree(3, "quiet", "k")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	heard := make(chan Commit, 256)
	go func() {
		for c := range ch {
			select {
			case heard <- c:
			default:
			}
		}
	}()

	time.Sleep(time.Second) // a dozen heal intervals of silence

	if n := subtreeReopens.Load() - before; n != 0 {
		t.Errorf("a quiet live watch was re-established %d times in a second of silence, want 0: asked, it answers", n)
	}
	for drained := false; !drained; {
		select {
		case c := <-heard:
			if c.progress {
				t.Errorf("the consumer was told of progress it never asked for: rev %d", c.Rev)
			}
			if len(c.Changes) > 0 {
				t.Errorf("a quiet slice reported changes: %v", c.Changes)
			}
		default:
			drained = true
		}
	}

	want := RefAt("quiet", "k", "after")
	if ok, _, err := s.Write(Write{At: want.At(), Value: Obj{"n": 1}.Payload()}); err != nil || !ok {
		t.Fatalf("write after the silence: ok=%v err=%v", ok, err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case c := <-heard:
			for _, ev := range c.Changes {
				if ev.Ref == want {
					return
				}
			}
		case <-deadline:
			t.Fatal("a write after a second of silence was never reported: the watch is gone")
		}
	}
}
