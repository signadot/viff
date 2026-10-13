package runnerd

import (
	"fmt"
	"net"
	"net/http"
	"os"
)

// ServeTask is a task's process, started by a SubprocessHandler: an InProcessHandler holding the
// one task build makes, served on the listener the SubprocessHandler handed it, until that
// handler closes its lifeline. The process's working directory is the task's directory, and its
// stdin was the runner's Config, which the caller reads before this.
//
// The process is made non-dumpable first, so the task it runs can't read its memory, where the
// runner's secrets are.
func ServeTask(build Builder) error {
	if err := undumpable(); err != nil {
		return fmt.Errorf("making the task's process non-dumpable: %w", err)
	}
	ln, err := net.FileListener(os.NewFile(controlFD, "control"))
	if err != nil {
		return fmt.Errorf("fd %d is not the listener a task's process serves on (a runner "+
			"starts this, not a person): %w", controlFD, err)
	}
	go exitWithLifeline()
	return http.Serve(ln, NewInProcessHandler(build).HTTPHandler())
}

// exitWithLifeline exits when the runner closes the lifeline or dies. bwrap's --die-with-parent
// covers the second on Linux; this covers both, sandboxed or not.
func exitWithLifeline() {
	f := os.NewFile(lifelineFD, "lifeline")
	var b [1]byte
	for {
		if _, err := f.Read(b[:]); err != nil {
			os.Exit(0)
		}
	}
}
