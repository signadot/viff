package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/scott-cotton/cli"
)

// upPidPath is where `verse up` records its pid, keyed by the verse address so `down`
// (which targets the same --addr) signals the right instance. This is a dev monolith,
// so a plain tmp pidfile rather than a service manager.
func upPidPath(addr string) string {
	safe := strings.NewReplacer(":", "_", "/", "_").Replace(addr)
	return filepath.Join(os.TempDir(), "verse-"+safe+".pid")
}

// removeOwnPidfile deletes the pidfile only if it still holds our pid — so if up-A
// exits just as up-B binds the same address and rewrites the pidfile, A's deferred
// cleanup can't remove B's entry.
func removeOwnPidfile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(data)) == strconv.Itoa(os.Getpid()) {
		_ = os.Remove(path)
	}
}

// downCmd stops the `verse up` running on --addr by sending it SIGTERM — which triggers
// its graceful shutdown, reaping the logd+docd it spawned (see resolveDocd's stop). It
// signals rather than tears down out-of-band so there is one shutdown path.
func downCmd() *cli.Command {
	return leaf("down", "down — stop the verse on --addr (signals it to reap its daemons)",
		func(cc *cli.Context, args []string) error {
			pidPath := upPidPath(g.Addr)
			data, err := os.ReadFile(pidPath)
			if err != nil {
				return fmt.Errorf("verse down: no running verse for %s (no pidfile %s)", g.Addr, pidPath)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				return fmt.Errorf("verse down: bad pidfile %s: %w", pidPath, err)
			}
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				_ = os.Remove(pidPath) // stale pidfile: the task is already gone
				return fmt.Errorf("verse down: verse (pid %d) not running on %s", pid, g.Addr)
			}
			// Wait for it to actually exit (it reaps its daemons on the way out).
			for i := 0; i < 100; i++ {
				if syscall.Kill(pid, 0) != nil { // signal 0 = existence probe
					fmt.Fprintf(cc.Out, "verse down: stopped verse (pid %d) on %s\n", pid, g.Addr)
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			fmt.Fprintf(cc.Out, "verse down: signaled verse (pid %d) on %s (still shutting down)\n", pid, g.Addr)
			return nil
		})
}
