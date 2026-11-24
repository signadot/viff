package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// `--notify` on `viff ask --watch` and `viff question --watch`: what needs you is also said on
// the desktop, so a watch left running in a terminal you are not looking at still reaches you.
//
// THE DESKTOP'S OWN DOOR: on Linux and the BSDs, the freedesktop notification service on the
// session bus (org.freedesktop.Notifications, over D-Bus), which every desktop and notification
// daemon serves (GNOME, KDE, dunst, mako, swaync); on macOS, which has no session bus, Notification
// Center through osascript. A system with neither is refused when the watch starts, not
// discovered at the first question. The bus is spoken to directly (godbus, pure Go, no
// dependencies of its own) rather than through notify-send or gdbus, which are not installed
// everywhere a notification daemon is, and which cannot keep a notification's id to close it.
//
// WHAT NEEDS YOU is said, and nothing else: a question that starts waiting on you, an ask of yours
// that is answered. A question that settles closes its notification, where the desktop can: it no
// longer needs you, and the line says how it settled. What stands when the watch opens, and what
// a replay or a reconnect catches up on, is one quiet summary, not a notification each (askWatch's
// catching up): opening a watch is not news. The lines the watch prints are unchanged.

// alerter puts one notification on the desktop, and takes one away.
type alerter interface {
	// alert shows a and returns the desktop's id for it, where the desktop has ids (0 where not),
	// replacing the notification replaces names.
	alert(ctx context.Context, a alert, replaces uint32) (uint32, error)
	// withdraw closes the notification id, where the desktop can.
	withdraw(ctx context.Context, id uint32) error
}

// alert is one notification: urgent says it needs the person, and stays until dismissed where the
// desktop keeps such a notification. key, where set, names what it is about (a question's id): a
// later alert with the same key replaces it, and a withdrawal closes it.
type alert struct {
	title, body string
	urgent      bool
	key         string
	withdraw    bool
}

// alertQueue is the size of the backlog a slow desktop may fall behind by before a notification
// is dropped: the watch's lines are never held up by the desktop.
const alertQueue = 16

// alertTimeout bounds one call to the desktop: a notification service that can be started but is
// stuck costs a notification, not the watch.
const alertTimeout = 5 * time.Second

// alertGrace is how long stopping waits for what is queued, so a stuck desktop does not hold up
// Ctrl-C by a timeout per queued notification.
const alertGrace = 2 * time.Second

// desktopAlerts starts the notifier and returns what sends to it, and what stops it. A
// notification the desktop refuses, and one dropped because the desktop fell behind, is said
// once, on a watch line, and the watch goes on.
func desktopAlerts(say func(rev, line string)) (send func(alert), stop func(), err error) {
	n, err := newAlerter()
	if err != nil {
		return nil, nil, fmt.Errorf("--notify: %w", err)
	}
	send, stop = alertsTo(n, say, alertTimeout, alertGrace)
	return send, stop, nil
}

// alertsTo runs n behind a queue: send never blocks, and stop waits for what is queued, for grace
// at most. Each call to n is bounded by timeout.
func alertsTo(n alerter, say func(rev, line string), timeout, grace time.Duration) (send func(alert), stop func()) {
	q := make(chan alert, alertQueue)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ids := map[string]uint32{}
		failed := false
		for a := range q {
			if ctx.Err() != nil {
				continue
			}
			err := deliver(ctx, n, a, ids, timeout)
			if err != nil && !failed {
				failed = true
				say("", fmt.Sprintf("--notify: the desktop refused a notification (%v); the lines go on", err))
			}
		}
	}()
	var mu sync.Mutex
	dropped, stopped := false, false
	send = func(a alert) {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return
		}
		select {
		case q <- a:
		default:
			if !dropped {
				dropped = true
				say("", "--notify: the desktop is behind, and a notification was dropped; the lines go on")
			}
		}
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			mu.Lock()
			stopped = true
			close(q)
			mu.Unlock()
			select {
			case <-done:
			case <-time.After(grace):
				cancel()
			}
			cancel()
			if c, ok := n.(io.Closer); ok {
				c.Close()
			}
		})
	}
	return send, stop
}

// deliver hands one alert to n, keeping the desktop's id for its key so a later alert replaces it
// and a withdrawal closes it.
func deliver(ctx context.Context, n alerter, a alert, ids map[string]uint32, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if a.withdraw {
		id, ok := ids[a.key]
		delete(ids, a.key)
		if !ok || id == 0 {
			return nil
		}
		return n.withdraw(ctx, id)
	}
	id, err := n.alert(ctx, a, ids[a.key])
	if err == nil && a.key != "" {
		ids[a.key] = id
	}
	return err
}
