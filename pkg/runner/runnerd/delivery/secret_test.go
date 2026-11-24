package delivery

import (
	"bytes"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/parse"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd/secretstore"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// secretLaunch is a launch whose environment gives one `secret` block, npm-token as NPM_TOKEN,
// with the exec shapes given, under the autonomy given.
func secretLaunch(shapes []systems.Shape, policy task.Autonomy) *runner.Launch {
	return &runner.Launch{ID: "t1", Prompt: "p", Autonomy: policy, Environment: systems.Environment{Systems: systems.Systems{
		"secret": {{Attributes: map[string]string{"name": "npm-token", "as": "NPM_TOKEN"}, Verbs: map[string][]systems.Shape{"exec": shapes}}},
	}}}
}

var allow = task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}

// secretCeiling is a runner's ceiling for `secret` that gives any command with any variables:
// the open ceiling names no variable (the form is closed by default), so one that admits them is
// written out.
func secretCeiling(t *testing.T) map[string]systems.Ceiling {
	return map[string]systems.Ceiling{"secret": {{Verbs: map[string][]systems.Shape{"exec": {{"env": pattern(t, `null`)}}}}}}
}

// pattern is the tony text as a pattern, as an environment or a row spells one.
func pattern(t *testing.T, text string) *ir.Node {
	t.Helper()
	n, err := parse.Parse([]byte(text))
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return systems.Plain(n)
}

// unconfined stands in for a sandbox in a test that runs its command as the test: the directory
// and nothing else, as runnerd.Unconfined does. A program named under InstPath is rewritten to
// where it is on this side, which is the one thing the install root's bind does and a test with
// no sandbox cannot have.
type unconfined struct{}

func (unconfined) Confine(cmd *exec.Cmd, root, dir string, _ Network, inst string) error {
	if inst != "" && under(cmd.Path, InstPath) {
		if rel, err := filepath.Rel(InstPath, cmd.Path); err == nil {
			cmd.Path = filepath.Join(inst, rel)
		}
	}
	cmd.Dir = dir
	return nil
}

// A `secret.exec` IS READ INTO ONE STEP, AND REFUSED WHERE IT DOES NOT READ: the body as a list
// and variables, the secret's name, a bare program, variables by their names, the directory
// under the root, the timeout within the hour, and no variable where the secret goes. The step
// carries `as` from the environment's block, never from the call, so a secret the environment
// gives no block for is outside it; the command is spelled as one line, which the block's shapes
// and the rows match; the variables match closed.
func TestASecretExecIsReadIntoOneStep(t *testing.T) {
	publish := []systems.Shape{{"argv": pattern(t, `!regexp "^npm publish "`), "dir": pattern(t, `!glob "packages/*"`),
		"env": pattern(t, `{NODE_OPTIONS: null}`)}}
	deny := task.Autonomy{
		{Name: "no-tag", Match: &task.Match{System: "secret", Verb: "exec", Params: map[string]*ir.Node{"argv": pattern(t, `!regexp "--tag"`)}}, Then: task.Then{Mode: task.ThenDeny}},
		{Name: "no-preload", Match: &task.Match{System: "secret", Params: map[string]*ir.Node{"env": pattern(t, `{LD_PRELOAD: null}`)}}, Then: task.Then{Mode: task.ThenDeny}},
		{Then: task.Then{Mode: task.ThenAllow}},
	}
	s := &Server{Log: io.Discard, Ceilings: secretCeiling(t)}
	ok := Request{Op: OpSecretExec, Body: "{argv: [npm, publish, --ignore-scripts]}", Fields: map[string]string{"name": "npm-token", "dir": "packages/a"}}
	with := func(body string, fields map[string]string) Request {
		return Request{Op: OpSecretExec, Body: body, Fields: fields}
	}
	for _, c := range []struct {
		name string
		l    *runner.Launch
		req  Request
		says string
	}{
		{"no body", secretLaunch(publish, allow), with("", ok.Fields), "argv names the program first"},
		{"a body with more", secretLaunch(publish, allow), with("{argv: [npm], secret: x}", ok.Fields), "the body does not read"},
		{"no name", secretLaunch(publish, allow), with(ok.Body, map[string]string{"dir": "packages/a"}), "a secret's name is not empty"},
		{"a name that is none", secretLaunch(publish, allow), with(ok.Body, map[string]string{"name": "../x"}), "is no secret's name"},
		{"no program", secretLaunch(publish, allow), with("{argv: []}", ok.Fields), "argv names the program first"},
		{"a program by path", secretLaunch(publish, allow), with("{argv: [/w/node_modules/.bin/npm, publish]}", ok.Fields), "never a path"},
		{"a variable that is none", secretLaunch(publish, allow), with("{argv: [npm, publish], env: {\"1X\": y}}", ok.Fields), "is no variable's name"},
		{"an absolute dir", secretLaunch(publish, allow), with(ok.Body, map[string]string{"name": "npm-token", "dir": "/etc"}), "is absolute"},
		{"a dir above", secretLaunch(publish, allow), with(ok.Body, map[string]string{"name": "npm-token", "dir": "packages/../../x"}), "reaches above"},
		{"a timeout past the hour", secretLaunch(publish, allow), with(ok.Body, map[string]string{"name": "npm-token", "dir": "packages/a", "timeout": "3601"}), "up to 1h0m0s"},
		{"a variable where the secret goes", secretLaunch(publish, allow), with("{argv: [npm, publish, --ignore-scripts], env: {NPM_TOKEN: mine}}", ok.Fields), "env names NPM_TOKEN, which is the secret's"},
		{"no block for the secret", secretLaunch(publish, allow), with(ok.Body, map[string]string{"name": "pypi-token", "dir": "packages/a"}), "outside the environment"},
		{"a command the shapes do not give", secretLaunch(publish, allow), with("{argv: [npm, view, left-pad]}", ok.Fields), "outside the environment"},
		{"a dir the shapes do not give", secretLaunch(publish, allow), with(ok.Body, map[string]string{"name": "npm-token", "dir": "scripts"}), "outside the environment"},
		{"a variable the shapes do not name", secretLaunch(publish, allow), with("{argv: [npm, publish, --ignore-scripts], env: {NPM_CONFIG_REGISTRY: x}}", ok.Fields), "outside the environment"},
		{"a list spelled to look like one argument", secretLaunch(publish, allow), with("{argv: [npm, \"publish --ignore-scripts\"]}", ok.Fields), "outside the environment"},
		{"stdin the shapes do not give", secretLaunch(publish, allow), with(ok.Body, map[string]string{"name": "npm-token", "dir": "packages/a", "stdin": "true"}), "outside the environment"},
		{"a row that denies the command", secretLaunch(publish, deny), with("{argv: [npm, publish, --ignore-scripts, --tag, next]}", ok.Fields), "refused by the autonomy's row no-tag"},
	} {
		tk := Task{ID: "t1", Root: "/w", Launch: c.l}
		rep := s.answer(tk, frame(t, c.req))
		if rep.OK || !strings.Contains(rep.Refused, c.says) {
			t.Errorf("%s: %+v, want refused: %s", c.name, rep, c.says)
		}
	}
	// A variable the shape names, whatever its value, is within; and the shape with `env: null`
	// gives any variable, the LD_PRELOAD row still denying it.
	any := []systems.Shape{{"argv": pattern(t, `!regexp "^npm "`), "env": pattern(t, `null`)}}
	for _, c := range []struct {
		name string
		l    *runner.Launch
		req  Request
	}{
		{"a variable the shape names", secretLaunch(publish, allow), with("{argv: [npm, publish, --ignore-scripts], env: {NODE_OPTIONS: --max-old-space-size=4096}}", ok.Fields)},
		{"any variable under env: null", secretLaunch(any, allow), with("{argv: [npm, view, left-pad], env: {NPM_CONFIG_REGISTRY: x}}", map[string]string{"name": "npm-token"})},
	} {
		// Within the environment, the act runs, and fails for the store this server lacks: the
		// decision was made, which is what this checks.
		tk := Task{ID: "t1", Root: "/w", Launch: c.l}
		if rep := s.answer(tk, frame(t, c.req)); !strings.Contains(rep.Refused, "no secret to run with") {
			t.Errorf("%s: %+v, want the act reached and refused for the store", c.name, rep)
		}
	}
	if rep := s.answer(Task{ID: "t1", Root: "/w", Launch: secretLaunch(any, deny)},
		frame(t, with("{argv: [npm, view], env: {LD_PRELOAD: x}}", map[string]string{"name": "npm-token"}))); !strings.Contains(rep.Refused, "no-preload") {
		t.Errorf("LD_PRELOAD under env: null: %+v", rep)
	}
}

// THE RUN: the secret is read at the act and the command finds it under `as` alone; the plain
// variables are set beside it, over the task's own; the output comes to the task on two streams,
// stdout and stderr apart, the value redacted; the answer is the exit status. A command that is
// not on the PATH, and a secret that is not in the store, refuse the call. Past the timeout the
// command is killed, and so it is when the task lets go of its output.
func TestASecretExecRunsTheCommandWithTheSecret(t *testing.T) {
	store := t.TempDir()
	if err := os.WriteFile(filepath.Join(store, "npm-token"), []byte("npm_s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "packages", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{Log: io.Discard, Ceilings: secretCeiling(t),
		Services: Services{Secrets: secretstore.Open(store), Confine: unconfined{}}}
	l := secretLaunch([]systems.Shape{{"env": pattern(t, `null`)}}, allow)
	near, far, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		s.Serve(Task{ID: "t1", Root: root, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + root, "GREETING=hello"}, Launch: l}, near)
		close(served)
	}()
	conn, err := net.FileConn(far)
	far.Close()
	if err != nil {
		t.Fatal(err)
	}
	cl := NewClient(conn)
	// run asks for a command and reads both streams whole, unless letGo says to let go at once.
	run := func(argv, env, dir, timeout string, letGo bool) (Reply, string, string) {
		t.Helper()
		body := "{argv: [" + argv + "]"
		if env != "" {
			body += ", env: " + env
		}
		body += "}"
		var out, errs bytes.Buffer
		read := make(chan struct{})
		handed := false
		rep, err := cl.AskStreaming(Request{Op: OpSecretExec, Body: body, Fields: map[string]string{"name": "npm-token", "dir": dir, "timeout": timeout}},
			func(files []*os.File) {
				handed = true
				if letGo {
					closeAll(files)
					close(read)
					return
				}
				go func() {
					defer close(read)
					defer closeAll(files)
					done := make(chan struct{})
					go func() { defer close(done); io.Copy(&errs, files[1]) }()
					io.Copy(&out, files[0])
					<-done
				}()
			})
		if err != nil {
			t.Fatalf("%s: %v", argv, err)
		}
		if handed {
			<-read
		} else if rep.OK {
			t.Fatalf("%s: answered %+v with no stream", argv, rep)
		}
		return rep, out.String(), errs.String()
	}
	rep, out, errs := run(`sh, -c, "echo token=$NPM_TOKEN greeting=$GREETING; echo oops >&2; pwd; exit 3"`, "", "packages/a", "", false)
	want := "token=[secret npm-token] greeting=hello\n" + resolvedDir(t, filepath.Join(root, "packages", "a")) + "\n"
	if !rep.OK || rep.Out != "exited 3" || out != want || errs != "oops\n" {
		t.Errorf("the run: %+v\nstdout %q\nstderr %q", rep, out, errs)
	}
	if strings.Contains(out+errs+rep.Out+rep.Error, "s3cr3t") {
		t.Error("the value reached the task")
	}
	// A plain variable is set beside the secret, over the task's own.
	if rep, out, _ := run(`sh, -c, "echo $GREETING"`, "{GREETING: hi}", "", "", false); !rep.OK || out != "hi\n" {
		t.Errorf("a plain variable: %+v, %q", rep, out)
	}
	// Not on the PATH, and not in the store.
	if rep, _, _ := run(`no-such-program-here`, "", "", "", false); !strings.Contains(rep.Refused, "no no-such-program-here on the command's PATH") {
		t.Errorf("a program not there: %+v", rep)
	}
	os.Remove(filepath.Join(store, "npm-token"))
	if rep, _, _ := run(`sh, -c, "true"`, "", "", "", false); !strings.Contains(rep.Refused, "no secret to run with: the secret npm-token: it is not in the store") {
		t.Errorf("a secret not there: %+v", rep)
	}
	if err := os.WriteFile(filepath.Join(store, "npm-token"), []byte("npm_s3cr3t"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The timeout, and the task letting go, each kill the command: in well under what it slept.
	started := time.Now()
	if rep, _, _ := run(`sleep, "30"`, "", "", "1", false); !rep.OK || rep.Out != "killed after 1s, the timeout" {
		t.Errorf("the timeout: %+v", rep)
	}
	if rep, _, _ := run(`sleep, "30"`, "", "", "", true); !rep.OK || rep.Out != "killed: the task let go of the command's output" {
		t.Errorf("letting go: %+v", rep)
	}
	if time.Since(started) > 20*time.Second {
		t.Errorf("the two kills took %s", time.Since(started))
	}
	cl.Close()
	<-served
}

// resolvedDir is dir with its links followed, as a command's pwd prints it.
func resolvedDir(t *testing.T, dir string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// THE REDACTION CATCHES THE VALUE WHOLE, WHEREVER IT FALLS: within one write, across two, at the
// very end, and more than once; what may begin one is held until it is known, and let out at
// the close.
func TestTheRedactionCatchesTheValueAcrossWrites(t *testing.T) {
	for _, c := range []struct {
		name   string
		writes []string
		want   string
	}{
		{"within one", []string{"token=npm_s3cr3t done"}, "token=[secret npm-token] done"},
		{"across two", []string{"token=npm_s", "3cr3t done"}, "token=[secret npm-token] done"},
		{"across three", []string{"npm", "_s3c", "r3t"}, "[secret npm-token]"},
		{"at the end", []string{"a npm_s3cr3t"}, "a [secret npm-token]"},
		{"twice", []string{"npm_s3cr3tnpm_s3cr3t"}, "[secret npm-token][secret npm-token]"},
		{"a prefix that is not it", []string{"npm_s3", "x"}, "npm_s3x"},
		{"a prefix at the end", []string{"npm_s3"}, "npm_s3"},
		{"a prefix of a prefix", []string{"npm_npm_s3cr3t"}, "npm_[secret npm-token]"},
	} {
		var out bytes.Buffer
		r := newRedactor(&out, "npm-token", "npm_s3cr3t")
		for _, w := range c.writes {
			if n, err := r.Write([]byte(w)); err != nil || n != len(w) {
				t.Fatalf("%s: wrote %d, %v", c.name, n, err)
			}
		}
		r.Close()
		if out.String() != c.want {
			t.Errorf("%s: %q, want %q", c.name, out.String(), c.want)
		}
	}
}

// The helpers: a directory under the root, the environment merged, the program found.
func TestTheSecretExecHelpers(t *testing.T) {
	for dir, want := range map[string]string{"": "", ".": "", "packages/a": "packages/a", "./packages//a/": "packages/a", "a/../b": "b"} {
		if got, err := underRoot(dir); err != nil || got != want {
			t.Errorf("underRoot(%q) = %q, %v, want %q", dir, got, err, want)
		}
	}
	for _, dir := range []string{"/etc", "..", "../x", "a/../../x"} {
		if _, err := underRoot(dir); err == nil {
			t.Errorf("underRoot(%q) is accepted", dir)
		}
	}
	got := mergeEnv([]string{"PATH=/bin", "HOME=/w", "X=1"}, map[string]string{"X": "2", "Y": "3"})
	if strings.Join(got, " ") != "PATH=/bin HOME=/w X=2 Y=3" {
		t.Errorf("mergeEnv: %v", got)
	}
	if _, err := findProgram("sh", []string{"HOME=/w"}, ""); err == nil || !strings.Contains(err.Error(), "no PATH") {
		t.Errorf("no PATH: %v", err)
	}
	if p, err := findProgram("sh", []string{"PATH=relative:/bin:/usr/bin"}, ""); err != nil || !strings.HasSuffix(p, "/bin/sh") {
		t.Errorf("sh: %q, %v", p, err)
	}
	// A PATH entry under InstPath is the task's install root, which is inst on this side: the
	// name is looked for there and answered as the path inside, the one the command runs by. A
	// task with no install root finds nothing there, and neither does a name that is not in it.
	inst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inst, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"jq", "sh"} {
		if err := os.WriteFile(filepath.Join(inst, "bin", name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	instBin := InstPath + "/bin"
	if p, err := findProgram("jq", []string{"PATH=" + instBin + ":/bin"}, inst); err != nil || p != instBin+"/jq" {
		t.Errorf("an installed program: %q, %v", p, err)
	}
	if _, err := findProgram("jq", []string{"PATH=" + instBin}, ""); err == nil {
		t.Error("a task with no install root found a program in one")
	}
	if _, err := findProgram("rg", []string{"PATH=" + instBin}, inst); err == nil {
		t.Error("a name the install root does not hold was found anyway")
	}
	// The install root's bin is first on the task's PATH, so an installed program is the one a
	// command runs where the system directories hold the name too.
	if p, err := findProgram("sh", []string{"PATH=" + instBin + ":/bin:/usr/bin"}, inst); err != nil || p != instBin+"/sh" {
		t.Errorf("the install root is not first: %q, %v", p, err)
	}
}

// THE INPUT RIDES THE I/O PAIR THE OTHER WAY, where the call says `stdin` and a shape admits it:
// what the task writes on its end is the command's stdin, unredacted on the way in — the echo
// coming back redacted is the proof the command read the raw value — and the task's half-close
// is the command's EOF while stdout still flows. A run the task feeds nothing waits on its stdin
// until the timeout.
func TestASecretExecFeedsTheCommandsStdin(t *testing.T) {
	store := t.TempDir()
	if err := os.WriteFile(filepath.Join(store, "npm-token"), []byte("npm_s3cr3t"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	interactive := []systems.Shape{{"env": pattern(t, `null`), "stdin": pattern(t, `null`)}}
	s := &Server{Log: io.Discard,
		Ceilings: map[string]systems.Ceiling{"secret": {{Verbs: map[string][]systems.Shape{"exec": interactive}}}},
		Services: Services{Secrets: secretstore.Open(store), Confine: unconfined{}}}
	near, far, err := SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		s.Serve(Task{ID: "t1", Root: root, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + root}, Launch: secretLaunch(interactive, allow)}, near)
		close(served)
	}()
	conn, err := net.FileConn(far)
	far.Close()
	if err != nil {
		t.Fatal(err)
	}
	cl := NewClient(conn)
	var out, errs bytes.Buffer
	read := make(chan struct{})
	rep, err := cl.AskStreaming(Request{Op: OpSecretExec, Body: `{argv: [sh, -c, "cat; echo done"]}`,
		Fields: map[string]string{"name": "npm-token", "stdin": "true", "timeout": "20"}},
		func(files []*os.File) {
			go func() {
				defer close(read)
				uc, err := net.FileConn(files[0])
				files[0].Close()
				if err != nil {
					t.Error(err)
					files[1].Close()
					return
				}
				u := uc.(*net.UnixConn)
				if _, err := u.Write([]byte("token is npm_s3cr3t\n")); err != nil {
					t.Error(err)
				}
				if err := u.CloseWrite(); err != nil {
					t.Error(err)
				}
				done := make(chan struct{})
				go func() { defer close(done); io.Copy(&errs, files[1]) }()
				io.Copy(&out, u)
				<-done
				u.Close()
				files[1].Close()
			}()
		})
	if err != nil {
		t.Fatal(err)
	}
	<-read
	if !rep.OK || rep.Out != "exited 0" || out.String() != "token is [secret npm-token]\ndone\n" || errs.String() != "" {
		t.Errorf("the interactive run: %+v\nstdout %q\nstderr %q", rep, out.String(), errs.String())
	}
	if strings.Contains(out.String()+errs.String()+rep.Out+rep.Error, "s3cr3t") {
		t.Error("the value reached the task")
	}
	// Nothing fed — the ends held, neither written nor closed, since closing the I/O end is
	// stdin's EOF itself — and the command waiting on its stdin is the timeout's to end.
	var held []*os.File
	rep, err = cl.AskStreaming(Request{Op: OpSecretExec, Body: `{argv: [cat]}`,
		Fields: map[string]string{"name": "npm-token", "stdin": "true", "timeout": "1"}},
		func(files []*os.File) { held = files })
	if err != nil || !rep.OK || rep.Out != "killed after 1s, the timeout" {
		t.Errorf("the unfed run: %+v, %v", rep, err)
	}
	closeAll(held)
	cl.Close()
	<-served
}
