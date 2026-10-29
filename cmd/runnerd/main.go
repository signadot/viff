// Command runnerd is the runner verse ships: a program that does the work a launch describes,
// and that verse reaches over a URL. What it offers is its configuration's: the agents its
// `agents:` map names, and scripts where it has a `script:` section.
//
//	runnerd -config runner.tony -token-file /var/run/verse/token
//
// Each task runs in a process of its own, confined by bwrap unless -sandbox none says otherwise.
// The runner serves the one verse its `verse:` block names, and proves itself to it with its
// token; it does not start without both.
//
// Then register it, from anywhere that can reach verse:
//
//	verse runner add my-runner http://localhost:9802 -token-file <the same token>
//
// WHY THIS IS A SEPARATE BINARY, for the reason cmd/iod gives: a runner is a program somebody
// deploys, and verse doesn't have to be built with one. A runner registered from elsewhere is
// no different from one shipped here.
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
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/signadot/verse/adminface"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/stock"
	"github.com/signadot/verse/systems"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("runnerd: ")
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func usage(fs *flag.FlagSet) func() {
	return func() {
		fmt.Fprint(fs.Output(), `runnerd runs the runner verse ships: the agents its configuration names under
agents:, and scripts where it has a script: section.

  runnerd -config <file> -token-file <file> [-sandbox bwrap|none]

`)
		fs.PrintDefaults()
	}
}

// offers is what the runner offers, as its start says it.
func offers(c stock.Config) []string {
	var out []string
	for _, o := range c.Describe(nil).Offers {
		out = append(out, o.Name())
	}
	return out
}

// run is the runner: what its configuration offers, each task in a process of its own, in the
// sandbox -sandbox names, serving the one verse its `verse:` block names.
func run(args []string) error {
	fs := flag.NewFlagSet("runnerd", flag.ExitOnError)
	fs.Usage = usage(fs)
	config := fs.String("config", "runner.tony", "the runner's configuration file")
	sandbox := fs.String("sandbox", "bwrap", "what confines each task's process: bwrap, or none")
	adminAddr := fs.String("admin-addr", os.Getenv("VERSE_RUNNER_ADMIN_ADDR"),
		"admin/pprof listen address, or off to disable (default: the listen port + 1)")
	tokenFile := fs.String("token-file", os.Getenv("VERSE_TOKEN_FILE"),
		"the file holding this runner's token, a workload token or a static one, answered on GET /describe to the verse it verified; required (VERSE_TOKEN_FILE)")
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
	if fs.NArg() > 0 {
		return fmt.Errorf("runnerd takes flags and no arguments, and was given %q: what it offers is its configuration's (`agents:`, `script:`), and `runnerd verse -config` is `runnerd -config`",
			strings.Join(fs.Args(), " "))
	}
	if *taskProc {
		return stock.ServeTask(os.Stdin, os.Stderr)
	}

	rc, c, err := stock.Load(*config)
	if err != nil {
		return err
	}
	// The door: the runner serves the verse its `verse:` block names, in the file or from the
	// flags, and proves itself to it with its token. A runner with neither would answer no token,
	// take no spawn, and find out at its first; it is refused here instead.
	if *verseURL != "" || *verseIssuer != "" || *verseAudience != "" || *verseCA != "" {
		if rc.Verse != nil {
			return fmt.Errorf("the verse this runner serves is named twice: `verse:` in %s, and -verse-url/-verse-issuer/-verse-audience/-verse-ca (VERSE_URL, VERSE_ISSUER, VERSE_AUDIENCE, VERSE_CA); name it once", *config)
		}
		rc.Verse = &runnerd.Verse{URL: *verseURL, Issuer: *verseIssuer, Audience: *verseAudience, CA: *verseCA}
	}
	if rc.Verse == nil {
		return fmt.Errorf("%s: no `verse:` block names the verse this runner serves (or -verse-url, VERSE_URL); a runner serves one verse, and proves itself to it", *config)
	}
	if *tokenFile == "" {
		return fmt.Errorf("no -token-file (VERSE_TOKEN_FILE): a runner proves itself to its verse with a token, a workload token or a static one (`verse runner add -subject` or `-token-file`)")
	}
	// A system whose credential is not set is closed rather than refused: the runner starts,
	// and offers what it can perform.
	c, closed := c.Held(os.Getenv)
	warnings, err := c.CheckStart(os.Getenv, exec.LookPath)
	if err != nil {
		return fmt.Errorf("%s: %w", *config, err)
	}
	for _, w := range append(closed, warnings...) {
		log.Printf("%s", w)
	}
	// The admin face before the work: probing the sandbox can hang on something outside the
	// process, and this is how to see what the runner and its tasks' goroutines are doing.
	adm, err := adminface.Start(adminface.Spec{
		Name: "runner", Addr: *adminAddr, Serve: rc.Listen, Log: log.Printf,
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

	s := stock.SecretsFromEnv(c)
	tokens := stock.TokensFromEnv(c)
	// The systems' documents say what a task may do (package systems); this runner's code claims
	// what it implements. A verb documented and not implemented is named here, not found by a
	// task calling it.
	if err := systems.Check(); err != nil {
		return fmt.Errorf("this runner does not honour the systems' documents: %w", err)
	}
	// The door's guard before the runner, since what the runner carries to its verse for a task
	// (the verse system's verbs) goes with the guard's own token.
	guard, err := runnerd.NewGuard(*rc.Verse, *tokenFile)
	if err != nil {
		return err
	}
	r, err := stock.Standalone(c, s, tokens, sb, []string{self, "-task"}, work, control, guard.Carrier(), os.Stderr)
	if err != nil {
		return err
	}
	// Stopped by a signal, the runner stops its tasks' processes, and the deferred removals of
	// its directories run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Its verse may ask it to stop (POST /stop), behind the door, as a signal would.
	h := guard.Wrap(runnerd.WithStop(r.HTTPHandler(), stop))
	log.Printf("serving one verse: %s, by the tokens it signs for %s", guard.Verse().Issuer, guard.Verse().Audience)
	srv := &http.Server{Handler: runnerd.WithReady(h)}
	ln, err := net.Listen("tcp", rc.Listen)
	if err != nil {
		return err
	}
	// The hello, once the runner listens: the verse describes this runner as soon as it is told
	// the runner is here. Readiness (GET /ready) is the server's alone.
	go guard.Announce(ctx, log.Printf)
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	log.Printf("runner listening on %s, sandbox %s, offering %s — register it with: "+
		"verse runner add <name> <its url> -subject <its token's subject> (or -token-file <its static token>)",
		rc.Listen, sb.Name(), strings.Join(offers(c), ", "))
	err = srv.Serve(ln)
	r.Shutdown()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
