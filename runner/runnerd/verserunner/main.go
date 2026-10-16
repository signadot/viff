package verserunner

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/signadot/verse/adminface"
	"github.com/signadot/verse/runner/runnerd"
)

// TaskArgs is how this runner starts a task process: its own executable with these arguments.
// `runnerd verse -task` answers to them, and so must any program that is this runner under
// another name, since the executable the runner starts is whichever one it is running as (the
// coderunner example, docs/reference/examples/coderunner).
var TaskArgs = []string{"verse", "-task"}

// Main is `runnerd verse`: the verse runner as a program, with its model reached through Portkey.
// Each task runs in a process of its own, in the sandbox -sandbox names, and in a workspace of
// the environment it names when the configuration declares environments.
//
//	runnerd verse -config verse-runner.tony
//	verse runner add verse-runner http://localhost:9802
func Main(args []string) error {
	fs := flag.NewFlagSet("verse", flag.ExitOnError)
	config := fs.String("config", "verse-runner.tony", "the runner's configuration file")
	listen := fs.String("listen", "", "the address to serve verse on (default: the configuration's `listen`)")
	sandbox := fs.String("sandbox", "bwrap", "what confines each task's process: bwrap, or none")
	data := fs.String("data", "", "directory for the environments' mirrors and workspaces (default: a temp dir)")
	keep := fs.Bool("keep", false, "keep workspaces after a task ends, for debugging")
	tokenEnv := fs.String("git-token-env", "VERSE_GIT_TOKEN",
		"environment variable holding the git token this runner fetches and pushes with; a task never holds it")
	adminAddr := fs.String("admin-addr", os.Getenv("VERSE_RUNNER_ADMIN_ADDR"),
		"admin/pprof listen address, or off to disable (default: the listen port + 1)")
	taskProc := fs.Bool("task", false,
		"run as one task's process; the runner starts these itself, with its configuration on stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *taskProc {
		return ServeTask(os.Stdin, os.Stderr)
	}
	rc, c, err := Load(*config)
	if err != nil {
		return err
	}
	if *listen != "" {
		rc.Listen = *listen
	}

	// THE ADMIN FACE BEFORE THE WORK, and before the contract is served: what this runner does
	// between here and ListenAndServe is clone mirrors and probe a sandbox, either of which can
	// hang on something outside the process — a remote that will not answer, a bwrap that will
	// not unshare — and a runner that hangs there never logs another line. It is also the only
	// way to see what a task's goroutines are doing on a box `kubectl exec` cannot reach into.
	adm, err := adminface.Start(adminface.Spec{
		Name: "verse-runner", Addr: *adminAddr, Serve: rc.Listen, Log: log.Printf,
	})
	if err != nil {
		return err
	}
	defer adm.Close()
	adm.SetAddrs(adminface.Addr{Name: "control", Addr: rc.Listen})

	// Named by the deployment rather than sniffed. Choosing a sandbox by what happens to be
	// installed would mean an image missing bwrap silently runs every task unconfined, which is
	// the degrade-instead-of-refuse this whole design is arranged against.
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
	root := *data
	if root == "" {
		if root, err = os.MkdirTemp("", "verse-runner-"); err != nil {
			return err
		}
		defer os.RemoveAll(root)
	}
	// Where this process reaches its task processes. NOT under -data, and deliberately: it holds
	// one unix socket per running task, and a control plane a task can dial is a task that can
	// answer its own gates. Under a temp root it is short (the sun path limit is about a hundred
	// bytes), outside every workspace, and outside anything a sandbox binds in.
	control, err := os.MkdirTemp("", "verse-runner-control-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(control)
	s := SecretsFromEnv()
	if s.PortkeyAPIKey == "" {
		log.Printf("%s is not set: the verse agent is not offered, and an `llm` spawn is refused",
			EnvPortkeyAPIKey)
	}
	callTool := FindCallTool(self)
	if callTool == "" {
		log.Printf("%s is neither beside this program nor on its PATH: a program under `agents:` "+
			"has no command for the task's tools", CallToolBinary)
	}
	r, err := Standalone{
		Config: c, Secrets: s, Sandbox: sb, Command: append([]string{self}, TaskArgs...),
		Work: root, Control: control, Data: root, Keep: *keep, GitToken: os.Getenv(*tokenEnv),
		CallTool: callTool, Log: os.Stderr,
	}.Start()
	if err != nil {
		return err
	}
	defer r.Shutdown()

	// A runner that is killed must not leave task processes holding workspaces. The lifeline
	// covers a crash; this covers the ordinary stop, and does it before the sockets go.
	srv := &http.Server{Addr: rc.Listen, Handler: r.Handler()}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		log.Printf("stopping %d task(s)", r.running())
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()

	// Anything this runner staged and never delivered gets another try now, before it serves
	// anything: a pod that died holding approved work should not wait for the next task on that
	// repository to notice. Only `pending` is swept — `held` was never authorized to leave.
	r.ReflectAll(context.Background())
	r.describeEnvironments()
	log.Printf("verse runner listening on %s, sandbox %s, state under %s — register it with: "+
		"verse runner add <name> <its url>", rc.Listen, sb.Name(), root)
	err = srv.ListenAndServe()
	r.Shutdown()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// describeEnvironments logs what this runner serves, once, at the top: which environments, and
// what each runs the work with.
func (r *Runner) describeEnvironments() {
	// And what the containment of every secret in a task process actually rests on, read rather
	// than assumed. Not a refusal — see ptraceScope for why.
	if len(r.cfg.Environments) > 0 {
		log.Printf("%s", ptraceScope())
	}
	if len(r.cfg.Tools) > 0 {
		log.Printf("  tools offered: %s", strings.Join(r.cfg.Tools, ","))
	}
	for _, e := range r.cfg.Environments {
		log.Printf("  environment %s: driver %s, tools %s, egress %s", e.Name,
			orDefault(e.Driver, r.drivers.Default), orDefault(strings.Join(e.Tools, ","), "all"),
			orDefault(strings.Join(e.Egress, ","), "open"))
	}
}

func lookupEnv(name string) (string, bool) { return os.LookupEnv(name) }

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
