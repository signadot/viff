package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/verserunner"
)

// The verse runner: verse's own agent, with its model reached through Portkey, and scripts.
// Each task runs in a process of its own, in the sandbox -sandbox names.
func verseMain(args []string) error {
	fs := flag.NewFlagSet("verse", flag.ExitOnError)
	config := fs.String("config", "verse-runner.tony", "the runner's configuration file")
	sandbox := fs.String("sandbox", "bwrap", "what confines each task's process: bwrap, or none")
	taskProc := fs.Bool("task", false,
		"run as one task's process; the runner starts these itself, with its configuration on stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *taskProc {
		return verserunner.ServeTask(os.Stdin, os.Stderr)
	}

	rc, c, err := verserunner.Load(*config)
	if err != nil {
		return err
	}
	sb, err := runnerd.SandboxNamed(*sandbox)
	if err != nil {
		return err
	}
	if sb.Name() == "none" {
		log.Printf("-sandbox none: tasks run unconfined, as this runner's user")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "verse-runner-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	control, err := os.MkdirTemp("", "verse-runner-control-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(control)

	s := verserunner.SecretsFromEnv()
	if s.PortkeyAPIKey == "" {
		log.Printf("%s is not set: the verse agent is not offered, only scripts",
			verserunner.EnvPortkeyAPIKey)
	}
	r, err := verserunner.Standalone(c, s, sb, []string{self, "verse", "-task"}, work, control, os.Stderr)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: rc.Listen, Handler: r.HTTPHandler()}

	// Stopped by a signal, the runner stops its tasks' processes, and the deferred removals of
	// its directories run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	log.Printf("verse runner listening on %s, sandbox %s — register it with: "+
		"verse runner add <name> <its url>", rc.Listen, sb.Name())
	err = srv.ListenAndServe()
	r.Shutdown()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
