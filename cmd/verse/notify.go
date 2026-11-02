package main

import (
	"fmt"
	"sync"
)

// `--notify` on `verse ask --watch` and `verse question --watch`: what needs you is also said on
// the desktop, so a watch left running in a terminal you are not looking at still reaches you.
//
// THE DESKTOP'S OWN DOOR: on Linux and the BSDs, the freedesktop notification service on the
// session bus (org.freedesktop.Notifications, over D-Bus), which every desktop and notification
// daemon serves (GNOME, KDE, dunst, mako, swaync); on macOS, which has no session bus, Notification
// Center through osascript. A system with neither is refused when the watch starts, not
// discovered at the first question.
//
// WHAT NEEDS YOU is said, and nothing else: a question that starts waiting on you, a question of
// yours that settles, an ask of yours that is answered. The lines the watch prints are unchanged.

// alerter puts one notification on the desktop.
type alerter interface {
	alert(a alert) error
}

// alert is one notification: urgent says it needs the person, and stays until dismissed where the
// desktop keeps such a notification.
type alert struct {
	title, body string
	urgent      bool
}

// alertQueue is the size of the backlog a slow desktop may fall behind by before a notification
// is dropped: the watch's lines are never held up by the desktop.
const alertQueue = 16

// desktopAlerts starts the notifier and returns what sends to it, and what stops it. A
// notification the desktop refuses is said once, on a watch line, and the watch goes on.
func desktopAlerts(say func(rev, line string)) (send func(alert), stop func(), err error) {
	n, err := newAlerter()
	if err != nil {
		return nil, nil, fmt.Errorf("--notify: %w", err)
	}
	return alertsTo(n, say)
}

// alertsTo runs n behind a queue: send never blocks, and stop waits for what is queued.
func alertsTo(n alerter, say func(rev, line string)) (send func(alert), stop func(), err error) {
	q := make(chan alert, alertQueue)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		failed := false
		for a := range q {
			if err := n.alert(a); err != nil && !failed {
				failed = true
				say("", fmt.Sprintf("--notify: the desktop refused a notification (%v); the lines go on", err))
			}
		}
	}()
	var once sync.Once
	send = func(a alert) {
		select {
		case q <- a:
		default:
		}
	}
	stop = func() { once.Do(func() { close(q); wg.Wait() }) }
	return send, stop, nil
}
