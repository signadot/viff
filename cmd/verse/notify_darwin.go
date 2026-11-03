//go:build darwin

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// macOS has no session bus: Notification Center is reached through osascript, which every macOS
// carries. The title and body go to the script as its arguments, never into its text, so nothing
// a question says is read as AppleScript.
//
// Notification Center attributes what osascript shows to Script Editor, not to the terminal: on
// recent macOS nothing appears until Script Editor is allowed to notify (System Settings ›
// Notifications › Script Editor), and how long a notification stays is Script Editor's setting.
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

// alert has no urgency to say, and no id to replace or close by: Notification Center keeps or
// banners it as Script Editor is set to.
func (o *osascriptAlerter) alert(ctx context.Context, a alert, _ uint32) (uint32, error) {
	args := append(append([]string{}, notifyScript...), a.title, a.body)
	out, err := exec.CommandContext(ctx, o.bin, args...).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return 0, nil
}

func (o *osascriptAlerter) withdraw(context.Context, uint32) error { return nil }
