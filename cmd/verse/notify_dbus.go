//go:build linux || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"fmt"
	"slices"

	"github.com/godbus/dbus/v5"
)

// The freedesktop notification service, on the session bus
// (https://specifications.freedesktop.org/notification-spec/latest/).
const (
	notifyName   = "org.freedesktop.Notifications"
	notifyPath   = "/org/freedesktop/Notifications"
	notifyMethod = notifyName + ".Notify"
)

// The spec's urgency hint: normal, and critical, which a server keeps until it is dismissed.
const (
	urgencyNormal   byte = 1
	urgencyCritical byte = 2
)

type dbusAlerter struct {
	obj dbus.BusObject
}

// newAlerter connects to the session bus and finds a notification service on it: one already
// running, or one the bus can start (a daemon installed for D-Bus activation).
func newAlerter() (alerter, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("no session bus to notify on (%w)", err)
	}
	var running bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, notifyName).Store(&running); err != nil {
		conn.Close()
		return nil, fmt.Errorf("asking the session bus for %s: %w", notifyName, err)
	}
	if !running {
		var activatable []string
		if err := conn.BusObject().Call("org.freedesktop.DBus.ListActivatableNames", 0).Store(&activatable); err != nil || !slices.Contains(activatable, notifyName) {
			conn.Close()
			return nil, fmt.Errorf("no notification service on the session bus (%s): start a notification daemon", notifyName)
		}
	}
	return &dbusAlerter{obj: conn.Object(notifyName, notifyPath)}, nil
}

func (d *dbusAlerter) alert(a alert) error {
	urgency := urgencyNormal
	if a.urgent {
		urgency = urgencyCritical
	}
	hints := map[string]dbus.Variant{"urgency": dbus.MakeVariant(urgency)}
	// app_name, replaces_id, app_icon, summary, body, actions, hints, expire_timeout (-1: the
	// server's default).
	return d.obj.Call(notifyMethod, 0, "verse", uint32(0), "", a.title, a.body, []string{}, hints, int32(-1)).Err
}
