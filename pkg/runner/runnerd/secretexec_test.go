package runnerd_test

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/parse"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/runner/runnerd/delivery"
	"github.com/signadot/verse/pkg/runner/runnerd/secretstore"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// fakeBwrap is a stand-in for bwrap where there is none: it changes to the directory --chdir
// names and runs the command after --, so what reaches the command is what the sandbox would
// give it, in the directory it would run in.
const fakeBwrap = `#!/bin/sh
while [ $# -gt 0 ]; do
	case "$1" in
	--) shift; break ;;
	--chdir) cd "$2" || exit 97; shift 2 ;;
	--bind|--ro-bind|--ro-bind-try) shift 3 ;;
	*) shift ;;
	esac
done
exec "$@"
`

// A COMMAND RUN WITH A SECRET RUNS IN THE RUNNER'S SANDBOX over the task's root, in the
// directory asked for, with the secret under the block's variable and nowhere the task reads it
// plain: the output comes to the task on its two streams with the value redacted, and the log
// holds the command and the exit, never the value. The program is one the runner keeps, under a
// system directory or a readOnly path; one of the task's own is refused, PATH or no PATH.
func TestASecretCommandRunsInTheSandboxWithTheProgramTheRunnerKeeps(t *testing.T) {
	bin := t.TempDir()
	bwrap := filepath.Join(bin, "bwrap")
	if err := os.WriteFile(bwrap, []byte(fakeBwrap), 0o755); err != nil {
		t.Fatal(err)
	}
	// hello is a program the operator keeps read only; mine is one in the task's tree.
	ro := t.TempDir()
	if err := os.WriteFile(filepath.Join(ro, "hello"), []byte("#!/bin/sh\necho \"hi $NPM_TOKEN\" >&2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, d := range []string{"packages/a", "tools"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "tools", "mine"), []byte("#!/bin/sh\necho $NPM_TOKEN\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	if err := os.WriteFile(filepath.Join(store, "npm-token"), []byte("npm_s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	anyEnv, err := parse.Parse([]byte("null"))
	if err != nil {
		t.Fatal(err)
	}
	exec := []systems.Shape{{"env": systems.Plain(anyEnv)}}
	s := &delivery.Server{Log: &log,
		Ceilings: map[string]systems.Ceiling{"secret": {{Verbs: map[string][]systems.Shape{"exec": exec}}}},
		Services: delivery.Services{Secrets: secretstore.Open(store),
			Confine: runnerd.Confiner{Sandbox: runnerd.BwrapAt(bwrap), ReadOnly: []string{ro}, Log: &log}}}
	l := &runner.Launch{ID: "t1", Prompt: "p", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}},
		Environment: systems.Environment{Systems: systems.Systems{
			"secret": {{Attributes: map[string]string{"name": "npm-token", "as": "NPM_TOKEN"}, Verbs: map[string][]systems.Shape{"exec": exec}}},
		}}}
	near, far, err := delivery.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		s.Serve(delivery.Task{ID: "t1", Root: root, Launch: l,
			Env: []string{"PATH=" + ro + ":" + filepath.Join(root, "tools") + ":/usr/bin:/bin", "HOME=" + root}}, near)
		close(served)
	}()
	conn, err := net.FileConn(far)
	far.Close()
	if err != nil {
		t.Fatal(err)
	}
	cl := delivery.NewClient(conn)
	run := func(body, dir string) (delivery.Reply, string, string) {
		t.Helper()
		var out, errs bytes.Buffer
		read := make(chan struct{})
		handed := false
		rep, err := cl.AskStreaming(delivery.Request{Op: delivery.OpSecretExec, Body: body, Fields: map[string]string{"name": "npm-token", "dir": dir}},
			func(files []*os.File) {
				handed = true
				go func() {
					defer close(read)
					done := make(chan struct{})
					go func() { defer close(done); io.Copy(&errs, files[1]); files[1].Close() }()
					io.Copy(&out, files[0])
					files[0].Close()
					<-done
				}()
			})
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if handed {
			<-read
		} else if rep.OK {
			t.Fatalf("%s: answered %+v with no stream", body, rep)
		}
		return rep, out.String(), errs.String()
	}
	rep, out, errs := run(`{argv: [sh, -c, "echo token=$NPM_TOKEN; pwd"]}`, "packages/a")
	// The directory as --chdir had it: the fake's cd keeps the path as given.
	want := "token=[secret npm-token]\n" + filepath.Join(root, "packages", "a") + "\n"
	if !rep.OK || rep.Out != "exited 0" || out != want || errs != "" {
		t.Errorf("sh: %+v\nstdout %q\nstderr %q", rep, out, errs)
	}
	if rep, out, errs := run(`{argv: [hello]}`, ""); !rep.OK || rep.Out != "exited 0" || out != "" || errs != "hi [secret npm-token]\n" {
		t.Errorf("a kept program: %+v\nstdout %q\nstderr %q", rep, out, errs)
	}
	if rep, _, _ := run(`{argv: [mine]}`, ""); !strings.Contains(rep.Refused, "not run: the program "+filepath.Join(root, "tools", "mine")+" is under no system directory and no readOnly path") {
		t.Errorf("a program of the task's: %+v", rep)
	}
	cl.Close()
	<-served
	for _, want := range []string{"sh -c 'echo token=$NPM_TOKEN; pwd' with the secret npm-token: exited 0", "hello with the secret npm-token: exited 0"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, log.String())
		}
	}
	if strings.Contains(log.String(), "s3cr3t") {
		t.Error("the value is in the log")
	}
}
