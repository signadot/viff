package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
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
	tokenFile := fs.String("token-file", os.Getenv("VERSE_TOKEN_FILE"),
		"the file holding this runner's workload token, answered on GET /describe to the verse it verified (VERSE_TOKEN_FILE)")
	// The verse this runner serves, where the configuration file does not name it: a deployment
	// writes one configuration for every cluster and names its verse in the pod's environment,
	// as it names the token file.
	verseURL := fs.String("verse-url", os.Getenv("VERSE_URL"),
		"where this runner reaches the one verse it serves, and reads its keys, where the configuration has no verse block (VERSE_URL)")
	verseIssuer := fs.String("verse-issuer", os.Getenv("VERSE_ISSUER"),
		"the issuer name of that verse's tokens, where it differs from the url (VERSE_ISSUER)")
	verseAudience := fs.String("verse-audience", os.Getenv("VERSE_AUDIENCE"),
		"the audience this runner takes its verse's tokens for, where it is not its own token's subject (VERSE_AUDIENCE)")
	verseCA := fs.String("verse-ca", os.Getenv("VERSE_CA"),
		"a PEM bundle the verse's certificate is checked against, for a verse under a certificate no public root signed (VERSE_CA)")
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
	// The door: with a `verse:` block, in the file or from the flags, the runner serves that verse
	// alone, and answers its own token to it on the describe; without one it serves whoever
	// reaches it, and a token file named here would be answered to nobody, which is said rather
	// than left to wonder about.
	if *verseURL != "" || *verseIssuer != "" || *verseAudience != "" || *verseCA != "" {
		if rc.Verse != nil {
			return fmt.Errorf("the verse this runner serves is named twice: `verse:` in %s, and -verse-url/-verse-issuer/-verse-audience/-verse-ca (VERSE_URL, VERSE_ISSUER, VERSE_AUDIENCE, VERSE_CA); name it once", *config)
		}
		rc.Verse = &runnerd.Verse{URL: *verseURL, Issuer: *verseIssuer, Audience: *verseAudience, CA: *verseCA}
	}
	// Stopped by a signal, the runner stops its tasks' processes, and the deferred removals of
	// its directories run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var h http.Handler = r.HTTPHandler()
	var announce func()
	if rc.Verse != nil {
		guard, err := runnerd.NewGuard(*rc.Verse, *tokenFile)
		if err != nil {
			return err
		}
		h = guard.Wrap(h)
		log.Printf("serving one verse: %s, by the tokens it signs for %s", guard.Verse().Issuer, guard.Verse().Audience)
		if *tokenFile == "" {
			log.Printf("no -token-file (VERSE_TOKEN_FILE): the describe carries no token, so a registration naming this runner's subject will not verify it, and this runner has nothing to announce itself with")
		} else {
			// The hello, once the runner listens: the verse describes this runner as soon as it
			// is told the runner is here. Readiness (GET /ready) is the server's alone.
			announce = func() { go guard.Announce(ctx, log.Printf) }
		}
	} else if *tokenFile != "" {
		log.Printf("-token-file is set and no `verse:` block names the verse this runner serves: the token is answered to nobody")
	}
	srv := &http.Server{Handler: runnerd.WithReady(h)}
	ln, err := net.Listen("tcp", rc.Listen)
	if err != nil {
		return err
	}
	if announce != nil {
		announce()
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	log.Printf("verse runner listening on %s, sandbox %s — register it with: "+
		"verse runner add <name> <its url>", rc.Listen, sb.Name())
	err = srv.Serve(ln)
	r.Shutdown()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
