//go:build linux || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/godbus/dbus/v5"
)

// The freedesktop notification service, on the session bus
// (https://specifications.freedesktop.org/notification-spec/latest/).
const (
	notifyName   = "org.freedesktop.Notifications"
	notifyPath   = "/org/freedesktop/Notifications"
	notifyMethod = notifyName + ".Notify"
	closeMethod  = notifyName + ".CloseNotification"
	capsMethod   = notifyName + ".GetCapabilities"
)

// The spec's urgency hint: normal, and critical, which a server keeps until it is dismissed.
const (
	urgencyNormal   byte = 1
	urgencyCritical byte = 2
)

type dbusAlerter struct {
	conn *dbus.Conn
	obj  dbus.BusObject
	// markup is whether the server reads a body as markup (the `body-markup` capability): a
	// question is written by someone else, so its text is escaped there, and only there.
	markup bool
}

// newAlerter connects to the session bus and finds a notification service on it: one already
// running, or one the bus can start (a daemon installed for D-Bus activation).
func newAlerter() (alerter, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("no session bus to notify on (%w)", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), alertTimeout)
	defer cancel()
	var running bool
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, notifyName).Store(&running); err != nil {
		conn.Close()
		return nil, fmt.Errorf("asking the session bus for %s: %w", notifyName, err)
	}
	if !running {
		var activatable []string
		if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.ListActivatableNames", 0).Store(&activatable); err != nil || !slices.Contains(activatable, notifyName) {
			conn.Close()
			return nil, fmt.Errorf("no notification service on the session bus (%s): start a notification daemon", notifyName)
		}
	}
	d := &dbusAlerter{conn: conn, obj: conn.Object(notifyName, notifyPath)}
	var caps []string
	if d.obj.CallWithContext(ctx, capsMethod, 0).Store(&caps) == nil {
		d.markup = slices.Contains(caps, "body-markup")
	}
	return d, nil
}

// markupEscaper escapes what the spec's body markup reads as markup.
var markupEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func (d *dbusAlerter) alert(ctx context.Context, a alert, replaces uint32) (uint32, error) {
	urgency := urgencyNormal
	if a.urgent {
		urgency = urgencyCritical
	}
	body := a.body
	if d.markup {
		body = markupEscaper.Replace(body)
	}
	hints := map[string]dbus.Variant{"urgency": dbus.MakeVariant(urgency)}
	// app_name, replaces_id, app_icon, summary, body, actions, hints, expire_timeout (-1: the
	// server's default).
	var id uint32
	err := d.obj.CallWithContext(ctx, notifyMethod, 0, "viff", replaces, "", a.title, body, []string{}, hints, int32(-1)).Store(&id)
	return id, err
}

func (d *dbusAlerter) withdraw(ctx context.Context, id uint32) error {
	return d.obj.CallWithContext(ctx, closeMethod, 0, id).Err
}

func (d *dbusAlerter) Close() error { return d.conn.Close() }
