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

	"github.com/signadot/verse/runner/runnerd/verserunner"
)

// The verse runner: verse's own agent, with its model reached through Portkey, and scripts.
func verseMain(args []string) error {
	fs := flag.NewFlagSet("verse", flag.ExitOnError)
	config := fs.String("config", "verse-runner.tony", "the runner's configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rc, c, err := verserunner.Load(*config)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "verse-runner-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	if c.APIKey() == "" {
		log.Printf("%s is not set: the verse agent is not offered, only scripts",
			verserunner.EnvPortkeyAPIKey)
	}
	srv := &http.Server{Addr: rc.Listen, Handler: verserunner.New(c, work, os.Stderr).Daemon().Handler()}

	// Stopped by a signal, the runner shuts down so the deferred removal of its tasks'
	// directories runs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	log.Printf("verse runner listening on %s — register it with: verse runner add <name> <its url>",
		rc.Listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
