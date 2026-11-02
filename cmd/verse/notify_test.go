package main

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// WHAT NEEDS YOU IS NOTIFIED, and only that: a question starting to wait on you (urgent), and
// settling; an ask of yours answered. An ask of yours being made is a line and no notification.
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
	if !got[0].urgent || !strings.Contains(got[0].title, "waits on you") || !strings.Contains(got[0].body, "Land it?") {
		t.Errorf("a question waiting on you: %+v", got[0])
	}
	if got[1].urgent || !strings.Contains(got[1].title, "settled") || !strings.Contains(got[1].body, "approved") {
		t.Errorf("a question settling: %+v", got[1])
	}
	if !strings.Contains(got[2].title, "answered yes") || !strings.Contains(got[2].body, "Lunch?") {
		t.Errorf("an ask answered: %+v", got[2])
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
	mu   sync.Mutex
	got  []alert
	fail error
}

func (f *fakeAlerter) alert(a alert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, a)
	return f.fail
}

// THE DESKTOP REFUSING is said once, and the watch goes on: what is queued still reaches the
// alerter, and stop waits for it.
func TestADesktopThatRefusesIsSaidOnce(t *testing.T) {
	f := &fakeAlerter{fail: errors.New("no server")}
	var mu sync.Mutex
	var said []string
	send, stop, err := alertsTo(f, func(rev, line string) { mu.Lock(); said = append(said, line); mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	send(alert{title: "one"})
	send(alert{title: "two"})
	stop()
	stop()
	if len(f.got) != 2 {
		t.Fatalf("the alerter was handed %d notifications, want 2", len(f.got))
	}
	if len(said) != 1 || !strings.Contains(said[0], "no server") {
		t.Fatalf("said %q, want the refusal once", said)
	}
}
