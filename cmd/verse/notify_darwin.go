//go:build darwin

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// macOS has no session bus: Notification Center is reached through osascript, which every macOS
// carries. The title and body go to the script as its arguments, never into its text, so nothing
// a question says is read as AppleScript.
var notifyScript = []string{
	"-e", "on run argv",
	"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
	"-e", "end run",
}

type osascriptAlerter struct {
	bin string
}

func newAlerter() (alerter, error) {
	bin, err := exec.LookPath("osascript")
	if err != nil {
		return nil, fmt.Errorf("no osascript to reach Notification Center with (%w)", err)
	}
	return &osascriptAlerter{bin: bin}, nil
}

// alert has no urgency to say: Notification Center keeps or banners a notification as the
// person set verse's terminal to, under System Settings › Notifications.
func (o *osascriptAlerter) alert(a alert) error {
	args := append(append([]string{}, notifyScript...), a.title, a.body)
	out, err := exec.Command(o.bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
