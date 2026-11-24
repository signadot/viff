// Command runnerd is the runner viff ships: a program that does the work a launch describes,
// and that viff reaches over a URL. What it offers is its configuration's: the agents its
// `agents:` map names, and scripts where it has a `script:` section.
//
//	runnerd -config runner.tony -token-file /var/run/viff/token
//
// Each task runs in a process of its own, confined by bwrap unless -sandbox none says otherwise.
// The runner serves the one viff its `viff:` block names, and proves itself to it with its
// token; it does not start without both.
//
// Then register it, from anywhere that can reach viff:
//
//	viff runner add my-runner http://localhost:9802 -token-file <the same token>
//
// WHY THIS IS A SEPARATE BINARY, for the reason cmd/iod gives: a runner is a program somebody
// deploys, and viff doesn't have to be built with one. A runner registered from elsewhere is
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

	"go.viff.dev/viff/internal/adminface"
	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/runner/runnerd/stock"
	"go.viff.dev/viff/pkg/systems"
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
		fmt.Fprint(fs.Output(), `runnerd runs the runner viff ships: the agents its configuration names under
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
// sandbox -sandbox names, serving the one viff its `viff:` block names.
func run(args []string) error {
	fs := flag.NewFlagSet("runnerd", flag.ExitOnError)
	fs.Usage = usage(fs)
	config := fs.String("config", "runner.tony", "the runner's configuration file")
	sandbox := fs.String("sandbox", "bwrap", "what confines each task's process: bwrap, or none")
	adminAddr := fs.String("admin-addr", os.Getenv("VIFF_RUNNER_ADMIN_ADDR"),
		"admin/pprof listen address, or off to disable (default: the listen port + 1)")
	tokenFile := fs.String("token-file", os.Getenv("VIFF_TOKEN_FILE"),
		"the file holding this runner's token, a workload token or a static one, answered on GET /describe to the viff it verified; required (VIFF_TOKEN_FILE)")
	// The viff this runner serves, where the configuration file does not name it: a deployment
	// writes one configuration for every cluster and names its viff in the pod's environment,
	// as it names the token file.
	viffURL := fs.String("viff-url", os.Getenv("VIFF_URL"),
		"where this runner reaches the one viff it serves, and reads its keys, where the configuration has no viff block (VIFF_URL)")
	viffIssuer := fs.String("viff-issuer", os.Getenv("VIFF_ISSUER"),
		"the issuer name of that viff's tokens, where it differs from the url (VIFF_ISSUER)")
	viffAudience := fs.String("viff-audience", os.Getenv("VIFF_AUDIENCE"),
		"the audience this runner takes its viff's tokens for, where it is not its own token's subject (VIFF_AUDIENCE)")
	viffCA := fs.String("viff-ca", os.Getenv("VIFF_CA"),
		"a PEM bundle the viff's certificate is checked against, for a viff under a certificate no public root signed (VIFF_CA)")
	taskProc := fs.Bool("task", false,
		"run as one task's process; the runner starts these itself, with its configuration on stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("runnerd takes flags and no arguments, and was given %q: what it offers is its configuration's (`agents:`, `script:`), and `runnerd viff -config` is `runnerd -config`",
			strings.Join(fs.Args(), " "))
	}
	if *taskProc {
		return stock.ServeTask(os.Stdin, os.Stderr)
	}

	rc, c, err := stock.Load(*config)
	if err != nil {
		return err
	}
	// The door: the runner serves the viff its `viff:` block names, in the file or from the
	// flags, and proves itself to it with its token. A runner with neither would answer no token,
	// take no spawn, and find out at its first; it is refused here instead.
	if *viffURL != "" || *viffIssuer != "" || *viffAudience != "" || *viffCA != "" {
		if rc.Viff != nil {
			return fmt.Errorf("the viff this runner serves is named twice: `viff:` in %s, and -viff-url/-viff-issuer/-viff-audience/-viff-ca (VIFF_URL, VIFF_ISSUER, VIFF_AUDIENCE, VIFF_CA); name it once", *config)
		}
		rc.Viff = &runnerd.Viff{URL: *viffURL, Issuer: *viffIssuer, Audience: *viffAudience, CA: *viffCA}
	}
	if rc.Viff == nil {
		return fmt.Errorf("%s: no `viff:` block names the viff this runner serves (or -viff-url, VIFF_URL); a runner serves one viff, and proves itself to it", *config)
	}
	if *tokenFile == "" {
		return fmt.Errorf("no -token-file (VIFF_TOKEN_FILE): a runner proves itself to its viff with a token, a workload token or a static one (`viff runner add -subject` or `-token-file`)")
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
	work, err := os.MkdirTemp("", "viff-runner-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	control, err := os.MkdirTemp("", "viff-runner-control-")
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
	// The door's guard before the runner, since what the runner carries to its viff for a task
	// (the viff system's verbs) goes with the guard's own token.
	guard, err := runnerd.NewGuard(*rc.Viff, *tokenFile)
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

	// Its viff may ask it to stop (POST /stop), behind the door, as a signal would.
	h := guard.Wrap(runnerd.WithStop(r.HTTPHandler(), stop))
	log.Printf("serving one viff: %s, by the tokens it signs for %s", guard.Viff().Issuer, guard.Viff().Audience)
	srv := &http.Server{Handler: runnerd.WithReady(h)}
	ln, err := net.Listen("tcp", rc.Listen)
	if err != nil {
		return err
	}
	// The hello, once the runner listens: the viff describes this runner as soon as it is told
	// the runner is here. Readiness (GET /ready) is the server's alone.
	go guard.Announce(ctx, log.Printf)
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	log.Printf("runner listening on %s, sandbox %s, offering %s — register it with: "+
		"viff runner add <name> <its url> -subject <its token's subject> (or -token-file <its static token>)",
		rc.Listen, sb.Name(), strings.Join(offers(c), ", "))
	err = srv.Serve(ln)
	r.Shutdown()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
