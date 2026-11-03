package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// WHAT NEEDS YOU IS NOTIFIED, and only that: a question starting to wait on you (urgent, keyed by
// its id), and settling, which closes that notification; an ask of yours answered. An ask of yours
// being made is a line and no notification.
func TestTheWatchNotifiesWhatNeedsYou(t *testing.T) {
	var got []alert
	quiet := func(rev, line string) {}

	q := newAskWatch(apiClient(), daniel, false, true)
	q.alert = func(a alert) { got = append(got, a) }
	q.frame(askFrame{at: "verse.ask.q1", rev: "5", to: entity.Obj{
		"status": "pending", "waitingOn": []any{daniel}, "pose": "Land it?",
	}.Payload()}, quiet)
	q.frame(askFrame{at: "verse.ask.q1", rev: "6", to: entity.Obj{
		"status": "approved", "waitingOn": []any{daniel}, "pose": "Land it?",
	}.Payload()}, quiet)

	a := newAskWatch(apiClient(), daniel, true, false)
	a.alert = func(al alert) { got = append(got, al) }
	mine := entity.Obj{mirror.FromField: mirror.FromPrincipal(daniel), "question": "Lunch?"}
	a.frame(askFrame{at: "verse.ask.a1", rev: "7", to: mine.Payload()}, quiet)
	mine["answer"] = "yes"
	a.frame(askFrame{at: "verse.ask.a1", rev: "8", to: mine.Payload()}, quiet)

	if len(got) != 3 {
		t.Fatalf("notified %d times, want 3: %+v", len(got), got)
	}
	if !got[0].urgent || got[0].key != "q1" || !strings.Contains(got[0].title, "waits on you") || !strings.Contains(got[0].body, "Land it?") {
		t.Errorf("a question waiting on you: %+v", got[0])
	}
	if !got[1].withdraw || got[1].key != "q1" {
		t.Errorf("a question settling closes its notification: %+v", got[1])
	}
	if got[2].title != "verse: answered yes" || !strings.Contains(got[2].body, "Lunch?") {
		t.Errorf("an ask answered: %+v", got[2])
	}
}

// OPENING A WATCH IS NOT NEWS: what stands when it opens, or what a replay catches up on, is one
// summary, not urgent, once the stream is quiet; a question that waited and settled within the
// replay is not in it. What comes after is notified as it happens.
func TestCatchingUpIsOneQuietSummary(t *testing.T) {
	var mu sync.Mutex
	var got []alert
	quiet := func(rev, line string) {}
	w := newAskWatch(apiClient(), daniel, true, true)
	w.quietFor = time.Hour // ended by hand below
	w.alert = func(a alert) { mu.Lock(); got = append(got, a); mu.Unlock() }
	w.catchUp()
	pending := func(pose string) entity.Payload {
		return entity.Obj{"status": "pending", "waitingOn": []any{daniel}, "pose": pose}.Payload()
	}
	w.frame(askFrame{at: "verse.ask.q1", rev: "3", to: pending("Land it?")}, quiet)
	w.frame(askFrame{at: "verse.ask.q2", rev: "4", to: pending("Ship it?")}, quiet)
	w.frame(askFrame{at: "verse.ask.q3", rev: "5", to: pending("Gone soon?")}, quiet)
	w.frame(askFrame{at: "verse.ask.q3", rev: "6", to: entity.Obj{"status": "denied", "waitingOn": []any{daniel}}.Payload()}, quiet)
	w.frame(askFrame{at: "verse.ask.a1", rev: "7", to: entity.Obj{
		mirror.FromField: mirror.FromPrincipal(daniel), "question": "Lunch?", "answer": "yes"}.Payload()}, quiet)
	for _, a := range got {
		if !a.withdraw {
			t.Fatalf("notified while catching up: %+v", a)
		}
	}
	got = nil
	w.caughtUp()
	if len(got) != 1 || got[0].urgent {
		t.Fatalf("caught up with %+v, want one summary, not urgent", got)
	}
	if got[0].title != "verse: 2 questions wait on you; 1 ask of yours was answered" || got[0].body != "Land it?\nShip it?" {
		t.Errorf("the summary: %+v", got[0])
	}
	got = nil
	w.caughtUp()
	w.frame(askFrame{at: "verse.ask.q4", rev: "8", to: pending("Now?")}, quiet)
	if len(got) != 1 || !got[0].urgent || got[0].key != "q4" {
		t.Errorf("after catching up, a new question: %+v", got)
	}
	w.quiet.Stop()
}

// A QUIET STREAM ENDS THE CATCHING UP by itself.
func TestAQuietStreamIsCaughtUp(t *testing.T) {
	done := make(chan alert, 1)
	w := newAskWatch(apiClient(), daniel, false, true)
	w.quietFor = 10 * time.Millisecond
	w.alert = func(a alert) { done <- a }
	w.catchUp()
	w.frame(askFrame{at: "verse.ask.q1", rev: "3", to: entity.Obj{
		"status": "pending", "waitingOn": []any{daniel}, "pose": "Land it?"}.Payload()}, func(string, string) {})
	select {
	case a := <-done:
		if a.title != "verse: 1 question waits on you" || a.urgent {
			t.Errorf("summary %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never caught up")
	}
}

// A WATCH WITHOUT --notify notifies nothing, and says the same lines.
func TestTheWatchWithoutNotifyHasNoAlerter(t *testing.T) {
	w := newAskWatch(apiClient(), daniel, false, true)
	var said []string
	w.frame(askFrame{at: "verse.ask.q1", rev: "5", to: entity.Obj{
		"status": "pending", "waitingOn": []any{daniel}, "pose": "Land it?",
	}.Payload()}, func(rev, line string) { said = append(said, line) })
	if len(said) != 1 || !strings.HasPrefix(said[0], "waiting on you q1: Land it?") {
		t.Fatalf("said %q", said)
	}
}

type fakeAlerter struct {
	mu        sync.Mutex
	got       []alert
	replaced  []uint32
	withdrawn []uint32
	next      uint32
	fail      error
	block     chan struct{}
}

func (f *fakeAlerter) alert(ctx context.Context, a alert, replaces uint32) (uint32, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, a)
	f.replaced = append(f.replaced, replaces)
	f.next++
	return f.next, f.fail
}

func (f *fakeAlerter) withdraw(_ context.Context, id uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.withdrawn = append(f.withdrawn, id)
	return nil
}

// THE DESKTOP REFUSING is said once, and the watch goes on: what is queued still reaches the
// alerter, and stop waits for it.
func TestADesktopThatRefusesIsSaidOnce(t *testing.T) {
	f := &fakeAlerter{fail: errors.New("no server")}
	var mu sync.Mutex
	var said []string
	send, stop := alertsTo(f, func(rev, line string) { mu.Lock(); said = append(said, line); mu.Unlock() }, time.Second, time.Second)
	send(alert{title: "one"})
	send(alert{title: "two"})
	stop()
	stop()
	send(alert{title: "after"})
	if len(f.got) != 2 {
		t.Fatalf("the alerter was handed %d notifications, want 2", len(f.got))
	}
	if len(said) != 1 || !strings.Contains(said[0], "no server") {
		t.Fatalf("said %q, want the refusal once", said)
	}
}

// A KEYED NOTIFICATION is replaced by the next with its key, and a withdrawal closes it, by the
// id the desktop gave it.
func TestAKeyedNotificationIsReplacedAndClosed(t *testing.T) {
	f := &fakeAlerter{}
	send, stop := alertsTo(f, func(string, string) {}, time.Second, time.Second)
	send(alert{title: "waits", key: "q1"})
	send(alert{title: "waits again", key: "q1"})
	send(alert{key: "q1", withdraw: true})
	send(alert{key: "q2", withdraw: true}) // never shown: nothing to close
	stop()
	if len(f.got) != 2 || f.replaced[0] != 0 || f.replaced[1] != 1 {
		t.Errorf("shown %+v replacing %v, want the second to replace the first", f.got, f.replaced)
	}
	if len(f.withdrawn) != 1 || f.withdrawn[0] != 2 {
		t.Errorf("closed %v, want the second's id", f.withdrawn)
	}
}

// A STUCK DESKTOP does not hold up stopping: each call is bounded, stop waits its grace and no
// more, and what the queue could not take is said, once.
func TestAStuckDesktopNeitherHangsNorHidesDrops(t *testing.T) {
	f := &fakeAlerter{block: make(chan struct{})}
	var mu sync.Mutex
	var said []string
	send, stop := alertsTo(f, func(rev, line string) { mu.Lock(); said = append(said, line); mu.Unlock() }, time.Hour, 50*time.Millisecond)
	for range alertQueue + 5 {
		send(alert{title: "x"})
	}
	began := time.Now()
	stop()
	if d := time.Since(began); d > 2*time.Second {
		t.Errorf("stop took %v", d)
	}
	mu.Lock()
	defer mu.Unlock()
	drops := 0
	for _, l := range said {
		if strings.Contains(l, "dropped") {
			drops++
		}
	}
	if drops != 1 {
		t.Errorf("said %q, want the drop said once", said)
	}
}
