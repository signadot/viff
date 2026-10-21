package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/signadot/verse/adminface"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/verserunner"
	"github.com/signadot/verse/systems"
)

// The verse runner: verse's own agent, with its model reached through Portkey, and scripts.
// Each task runs in a process of its own, in the sandbox -sandbox names.
func verseMain(args []string) error {
	fs := flag.NewFlagSet("verse", flag.ExitOnError)
	config := fs.String("config", "verse-runner.tony", "the runner's configuration file")
	sandbox := fs.String("sandbox", "bwrap", "what confines each task's process: bwrap, or none")
	adminAddr := fs.String("admin-addr", os.Getenv("VERSE_RUNNER_ADMIN_ADDR"),
		"admin/pprof listen address, or off to disable (default: the listen port + 1)")
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
	// The admin face before the work: probing the sandbox can hang on something outside the
	// process, and this is how to see what the runner and its tasks' goroutines are doing.
	adm, err := adminface.Start(adminface.Spec{
		Name: "verse-runner", Addr: *adminAddr, Serve: rc.Listen, Log: log.Printf,
	})
	if err != nil {
		return err
	}
	defer adm.Close()
	adm.SetAddrs(adminface.Addr{Name: "control", Addr: rc.Listen})
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
		log.Printf("%s is not set: the verse agent is not offered", verserunner.EnvPortkeyAPIKey)
	}
	if c.Codex != nil {
		// The key is read HERE and sent to the task process: a missing one is said once, at the
		// top, where otherwise it is invisible until a turn fails with a vendor's error.
		s.CodexKey = c.Codex.Key()
		if why := c.Codex.MissingKey(); why != "" {
			log.Printf("WARNING: %s. Codex will have to be logged in under the HOME its environment "+
				"passes through, or its turns will fail.", why)
		}
	}
	if c.Claude != nil {
		s.ClaudeKey = c.Claude.Key()
		if why := c.Claude.MissingKey(); why != "" {
			log.Printf("WARNING: %s. Claude will have to be logged in under the HOME its environment "+
				"passes through, or its turns will fail.", why)
		}
	}
	tokens, unset := verserunner.TokensFromEnv(c)
	for _, name := range unset {
		log.Printf("%s is not set: what reads it is refused where it reaches for it", name)
	}
	// The systems' documents say what a task may do (package systems); this runner's code claims
	// what it implements. A verb documented and not implemented is named here, not found by a
	// task calling it.
	if err := systems.Check(); err != nil {
		return fmt.Errorf("this runner does not honour the systems' documents: %w", err)
	}
	r, err := verserunner.Standalone(c, s, tokens, sb, []string{self, "verse", "-task"}, work, control, os.Stderr)
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
