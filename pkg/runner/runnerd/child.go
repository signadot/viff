package runnerd

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"

	"go.viff.dev/viff/pkg/runner"
)

// ServeTask is a task's process, started by a SubprocessHandler: an InProcessHandler holding the
// one task build makes, served on the listener the SubprocessHandler handed it, until that
// handler closes its lifeline. The process's working directory is the task's directory, and its
// stdin was the runner's Config, which the caller reads before this.
//
// The process is made non-dumpable first, so the task it runs can't read its memory, where the
// runner's secrets are.
func ServeTask(build Builder, desc runner.Description, owned EnvOf) error {
	if err := undumpable(); err != nil {
		return fmt.Errorf("making the task's process non-dumpable: %w", err)
	}
	// The descriptors the runner handed over stay this process's: closed, or marked close-on-exec,
	// so nothing the task runs inherits them. A command holding the control listener could
	// answer the runner in the task's place.
	lf := os.NewFile(controlFD, "control")
	ln, err := net.FileListener(lf) // a duplicate, close-on-exec
	lf.Close()
	if err != nil {
		return fmt.Errorf("fd %d is not the listener a task's process serves on (a runner "+
			"starts this, not a person): %w", controlFD, err)
	}
	syscall.CloseOnExec(lifelineFD)
	go exitWithLifeline()
	// A task whose network allows hosts has its runner's proxy bound in, and nothing else: served
	// here at the address this process's environment has named since its start (EgressEnv), for
	// this process and what it runs.
	if _, err := os.Stat(EgressPath); err == nil {
		if _, err := UseEgress(EgressPath, EgressAddr); err != nil {
			return err
		}
	}
	h := NewInProcessHandler(build)
	h.taskProcess = true
	h.Describe(desc)
	h.OwnEnv(owned)
	return http.Serve(ln, h.HTTPHandler())
}

// exitWithLifeline ends the process when the runner closes the lifeline or dies, and with it the
// commands it started: the runner starts it at the head of a process group of its own, and it
// kills that group, itself included. No sandbox is needed for a task's commands to end with the
// task, and the runner need not live to end them. bwrap's --die-with-parent covers a runner
// that dies on Linux; this covers both, sandboxed or not.
func exitWithLifeline() {
	f := os.NewFile(lifelineFD, "lifeline")
	var b [1]byte
	for {
		if _, err := f.Read(b[:]); err != nil {
			if syscall.Getpgrp() == os.Getpid() {
				_ = syscall.Kill(0, syscall.SIGKILL)
			}
			os.Exit(0)
		}
	}
}
