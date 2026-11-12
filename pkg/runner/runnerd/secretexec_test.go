package runnerd_test

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
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
// give it, in the directory it would run in. A program named under /inst is in the task's
// install root, a path only that root's bind makes exist: with no mount namespace to bind in,
// the fake rewrites it to where the bind's source is, which is the one thing the bind does.
const fakeBwrap = `#!/bin/sh
inst=
while [ $# -gt 0 ]; do
	case "$1" in
	--) shift; break ;;
	--chdir) cd "$2" || exit 97; shift 2 ;;
	--ro-bind) if [ "$3" = "/inst" ]; then inst="$2"; fi; shift 3 ;;
	--bind|--ro-bind-try) shift 3 ;;
	*) shift ;;
	esac
done
prog=$1
shift
case "$prog" in
/inst/*) if [ -n "$inst" ]; then prog="$inst${prog#/inst}"; fi ;;
esac
exec "$prog" "$@"
`

// A COMMAND RUN WITH A SECRET RUNS IN THE RUNNER'S SANDBOX over the task's root, in the
// directory asked for, with the secret under the block's variable and nowhere the task reads it
// plain: the output comes to the task on its two streams with the value redacted, and the log
// holds the command and the exit, never the value. The program is one the runner keeps, under a
// system directory, a readOnly path or the task's install root, which the runner fills and the
// task cannot write; one of the task's own is refused, PATH or no PATH.
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
	// inst is the task's install root, which the runner laid a tool into and which the command's
	// sandbox binds read only at InstPath, as the task's own sandbox does.
	inst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inst, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inst, "bin", "installed"), []byte("#!/bin/sh\necho \"installed $NPM_TOKEN\"\n"), 0o755); err != nil {
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
			Confine: runnerd.Confiner{Sandbox: runnerd.BwrapAt(bwrap), ReadOnly: []string{ro}}}}
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
		s.Serve(delivery.Task{ID: "t1", Root: root, Launch: l, Inst: inst,
			Env: []string{"PATH=" + runnerd.InstPath + "/bin:" + ro + ":" + filepath.Join(root, "tools") + ":/usr/bin:/bin", "HOME=" + root}}, near)
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
	// A tool the runner installed for the task is the runner's own too: the task names it as its
	// PATH has it, under InstPath, and the sandbox binds the root it is in. The secret is
	// redacted as ever.
	if rep, out, errs := run(`{argv: [installed]}`, ""); !rep.OK || rep.Out != "exited 0" || out != "installed [secret npm-token]\n" || errs != "" {
		t.Errorf("an installed program: %+v\nstdout %q\nstderr %q", rep, out, errs)
	}
	if rep, _, _ := run(`{argv: [mine]}`, ""); !strings.Contains(rep.Refused, "not run: the program "+filepath.Join(root, "tools", "mine")+" is under no system directory, no readOnly path of the runner's and no install root") {
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

// THE SECRET STAYS WITH THE RUNNER WHERE THE ENVIRONMENT, THE CEILING OR THE AUTONOMY FORBIDS
// THE RUN: each way a call might reach it past them is refused before anything runs, and after
// all of them nothing in the workspace, the answers or the log holds the value. The ceiling
// names one secret; the environment names which programs and which variables; the rows deny
// one variable and one command; the runner's own check holds the program to one it keeps,
// wherever the PATH the call was allowed to set points.
func TestASecretStaysWithTheRunnerWhereTheRunIsForbidden(t *testing.T) {
	bin := t.TempDir()
	bwrap := filepath.Join(bin, "bwrap")
	if err := os.WriteFile(bwrap, []byte(fakeBwrap), 0o755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A program of the task's that would write the value into the workspace, were it run.
	if err := os.WriteFile(filepath.Join(root, "tools", "mine"), []byte("#!/bin/sh\necho $NPM_TOKEN > "+filepath.Join(root, "leak")+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	for _, name := range []string{"npm-token", "pypi-token"} {
		if err := os.WriteFile(filepath.Join(store, name), []byte("npm_s3cr3t\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pat := func(text string) *ir.Node {
		n, err := parse.Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		return systems.Plain(n)
	}
	var log bytes.Buffer
	s := &delivery.Server{Log: &log,
		// The ceiling: one secret, any command, any variable.
		Ceilings: map[string]systems.Ceiling{"secret": {{Attributes: map[string]*ir.Node{"name": ir.FromString("npm-token")},
			Verbs: map[string][]systems.Shape{"exec": {{"env": pat("null")}}}}}},
		Services: delivery.Services{Secrets: secretstore.Open(store),
			Confine: runnerd.Confiner{Sandbox: runnerd.BwrapAt(bwrap)}}}
	// The environment: sh -c and mine, with PATH and NODE_OPTIONS; and a block for a secret the
	// ceiling does not name.
	exec := []systems.Shape{{"argv": pat(`!regexp "^(sh -c |mine)"`), "env": pat(`{PATH: null, NODE_OPTIONS: null}`)}}
	l := &runner.Launch{ID: "t1", Prompt: "p",
		Autonomy: task.Autonomy{
			{Name: "no-preload", Match: &task.Match{System: "secret", Params: map[string]*ir.Node{"env": pat(`{LD_PRELOAD: null}`)}}, Then: task.Then{Mode: task.ThenDeny}},
			{Name: "no-oops", Match: &task.Match{System: "secret", Params: map[string]*ir.Node{"argv": pat(`!regexp "oops"`)}}, Then: task.Then{Mode: task.ThenDeny}},
			{Then: task.Then{Mode: task.ThenAllow}}},
		Environment: systems.Environment{Systems: systems.Systems{
			"secret": {{Attributes: map[string]string{"name": "npm-token", "as": "NPM_TOKEN"}, Verbs: map[string][]systems.Shape{"exec": exec}},
				{Attributes: map[string]string{"name": "pypi-token", "as": "PYPI_TOKEN"}, Verbs: map[string][]systems.Shape{"exec": exec}}},
		}}}
	near, far, err := delivery.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		s.Serve(delivery.Task{ID: "t1", Root: root, Launch: l, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + root}}, near)
		close(served)
	}()
	conn, err := net.FileConn(far)
	far.Close()
	if err != nil {
		t.Fatal(err)
	}
	cl := delivery.NewClient(conn)
	var answers []string
	ask := func(body string, fields map[string]string) delivery.Reply {
		t.Helper()
		var got bytes.Buffer
		rep, err := cl.AskStreaming(delivery.Request{Op: delivery.OpSecretExec, Body: body, Fields: fields}, func(files []*os.File) {
			for _, f := range files {
				io.Copy(&got, f)
				f.Close()
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		answers = append(answers, rep.Out, rep.Refused, rep.Error, got.String())
		return rep
	}
	npm := map[string]string{"name": "npm-token"}
	for _, c := range []struct{ name, body, says string }{
		{"a program of the task's, by path", `{argv: ["./tools/mine"]}`, "never a path"},
		{"a program of the task's, on a PATH the call set", `{argv: [mine], env: {PATH: "` + filepath.Join(root, "tools") + `"}}`, "under no system directory, no readOnly path of the runner's and no install root"},
		{"a variable the environment does not name", `{argv: [sh, -c, "echo $NPM_TOKEN"], env: {NPM_CONFIG_REGISTRY: x}}`, "outside the environment"},
		{"the variable the secret is in", `{argv: [sh, -c, "echo"], env: {NPM_TOKEN: x}}`, "env names NPM_TOKEN, which is the secret's"},
		{"a variable a row denies", `{argv: [sh, -c, "echo"], env: {LD_PRELOAD: x}}`, "outside the environment"},
		{"a command a row denies", `{argv: [sh, -c, "echo oops"]}`, "refused by the autonomy's row no-oops"},
		{"a command spelled to look like another", `{argv: [sh, "-c echo"]}`, "outside the environment"},
	} {
		if rep := ask(c.body, npm); rep.OK || !strings.Contains(rep.Refused, c.says) {
			t.Errorf("%s: %+v, want refused: %s", c.name, rep, c.says)
		}
	}
	for _, c := range []struct {
		name   string
		fields map[string]string
		says   string
	}{
		{"a directory above the root", map[string]string{"name": "npm-token", "dir": "../"}, "reaches above"},
		{"a secret the ceiling does not name", map[string]string{"name": "pypi-token"}, "beyond the runner's ceiling"},
		{"a secret by a path", map[string]string{"name": "../npm-token"}, "no secret's name"},
		{"a secret the environment does not name", map[string]string{"name": "other-token"}, "outside the environment"},
	} {
		if rep := ask(`{argv: [sh, -c, "echo"]}`, c.fields); rep.OK || !strings.Contains(rep.Refused, c.says) {
			t.Errorf("%s: %+v, want refused: %s", c.name, rep, c.says)
		}
	}
	// And the one the environment gives: run, and redacted.
	if rep := ask(`{argv: [sh, -c, "echo $NPM_TOKEN"]}`, npm); !rep.OK || rep.Out != "exited 0" || answers[len(answers)-1] != "[secret npm-token]\n" {
		t.Errorf("the run the environment gives: %+v, %q", rep, answers[len(answers)-1])
	}
	cl.Close()
	<-served
	if _, err := os.Stat(filepath.Join(root, "leak")); err == nil {
		t.Error("a program of the task's ran with the secret")
	}
	if all := strings.Join(answers, "\n") + log.String(); strings.Contains(all, "s3cr3t") {
		t.Error("the value is in an answer or the log")
	}
}
