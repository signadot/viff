package stock

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/parse"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/delivery"
	"github.com/signadot/verse/pkg/runner/runnerd/secretstore"
	"github.com/signadot/verse/pkg/runner/runnerd/verse"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// anyVars is the shape that gives any command with any variables.
func anyVars(t *testing.T) []systems.Shape {
	t.Helper()
	n, err := parse.Parse([]byte("null"))
	if err != nil {
		t.Fatal(err)
	}
	return []systems.Shape{{"env": systems.Plain(n)}}
}

// withSecret is an environment with one `secret` block, npm-token as NPM_TOKEN.
func withSecret(t *testing.T) systems.Environment {
	return systems.Environment{Systems: systems.Systems{
		"secret": {{Attributes: map[string]string{"name": "npm-token", "as": "NPM_TOKEN"}, Verbs: map[string][]systems.Shape{"exec": anyVars(t)}}},
	}}
}

// THE TOOL IS OFFERED WHERE THE ENVIRONMENT GIVES THE VERB, with the wait and the kill beside it, and each
// use is the step `secret.exec` whole: the command as one line, `as` from the block, the
// directory cleaned, the variables an object. A call that does not read is no step, and is
// refused by the tool before it reaches the runner.
func TestSecretExecIsTheStepTheRunnerDecides(t *testing.T) {
	if tools := secretTools(&delivery.Client{}, &workspace.Workspace{Root: "/w"}, systems.Environment{}); tools != nil {
		t.Errorf("offered with no block: %v", tools)
	}
	tools := secretTools(&delivery.Client{}, &workspace.Workspace{Root: "/w"}, withSecret(t))
	if len(tools) != 3 || tools[0].Name != "secret-exec" || tools[1].Name != "secret-exec-wait" || tools[2].Name != "secret-exec-kill" ||
		tools[0].Step != task.StepSecretExec || tools[1].Step != "" || tools[2].Step != "" {
		t.Fatalf("the tools: %+v", tools)
	}
	// The wait and the kill are no steps, and are not verbs the environment could leave out:
	// offered keeps them.
	if kept := offered(tools, withSecret(t)); len(kept) != 3 {
		t.Errorf("offered keeps %d of the three", len(kept))
	}
	run := tools[0]
	in := map[string]any{"name": "npm-token", "argv": []any{"npm", "publish", "--tag", "a b"}, "dir": "./packages/a/", "env": map[string]any{"NODE_OPTIONS": "--x"}, "timeout": float64(30)}
	st, ok := run.Build(in)
	if !ok {
		t.Fatal("no step")
	}
	want := task.NewStep(task.StepSecretExec, map[string]string{"name": "npm-token", "as": "NPM_TOKEN", "argv": "npm publish --tag 'a b'", "dir": "packages/a"})
	for _, f := range []string{"name", "as", "argv", "dir"} {
		if got, w := st.Field(f).String, want.Field(f).String; got != w {
			t.Errorf("%s = %q, want %q", f, got, w)
		}
	}
	if env := st.Field("env"); env == nil || len(env.Fields) != 1 || env.Fields[0].String != "NODE_OPTIONS" {
		t.Errorf("env: %v", env)
	}
	for name, bad := range map[string]map[string]any{
		"no name":                     {"argv": []any{"npm"}},
		"no block":                    {"name": "pypi-token", "argv": []any{"npm"}},
		"argv not a list":             {"name": "npm-token", "argv": "npm publish"},
		"argv not strings":            {"name": "npm-token", "argv": []any{"npm", 3}},
		"a path":                      {"name": "npm-token", "argv": []any{"./npm"}},
		"env not strings":             {"name": "npm-token", "argv": []any{"npm"}, "env": map[string]any{"A": 1}},
		"the secret's name":           {"name": "npm-token", "argv": []any{"npm"}, "env": map[string]any{"NPM_TOKEN": "x"}},
		"a dir above":                 {"name": "npm-token", "argv": []any{"npm"}, "dir": "../x"},
		"a timeout past":              {"name": "npm-token", "argv": []any{"npm"}, "timeout": 3601},
		"a timeout that is no number": {"name": "npm-token", "argv": []any{"npm"}, "timeout": "30s"},
		"a timeout in halves":         {"name": "npm-token", "argv": []any{"npm"}, "timeout": 30.5},
		"what it does not take":       {"name": "npm-token", "argv": []any{"npm"}, "shell": true},
	} {
		if _, ok := run.Build(bad); ok && name != "what it does not take" {
			t.Errorf("%s builds a step", name)
		}
		out, blocked, err := run.Do(t.Context(), task.ToolUse{Name: run.Name, Input: bad})
		if err != nil || !blocked || !(strings.HasPrefix(out, "not run: ") || strings.HasPrefix(out, "refused, and nothing was done")) {
			t.Errorf("%s: %q, %v, %v", name, out, blocked, err)
		}
	}
	if out, blocked, err := tools[2].Do(t.Context(), task.ToolUse{Name: tools[2].Name, Input: map[string]any{"id": "e9"}}); err != nil || !blocked || out != "not killed: no command is running as e9" {
		t.Errorf("a kill of nothing: %q, %v, %v", out, blocked, err)
	}
	if out, blocked, err := tools[1].Do(t.Context(), task.ToolUse{Name: tools[1].Name, Input: map[string]any{"id": "e9"}}); err != nil || !blocked || !strings.HasPrefix(out, "no run e9 is known here") {
		t.Errorf("a wait for nothing: %q, %v, %v", out, blocked, err)
	}
}

// startRun starts a run with the tools' `secret-exec` and is its id: the call answers at the
// start, with the id and the directory on its first line.
func startRun(t *testing.T, tools []verse.Tool, root string, in map[string]any) string {
	t.Helper()
	out, blocked, err := tools[0].Do(t.Context(), task.ToolUse{Name: tools[0].Name, Input: in})
	if err != nil || blocked {
		t.Fatalf("the start: %q, %v, %v", out, blocked, err)
	}
	first, _, _ := strings.Cut(out, "\n")
	id, dir, ok := strings.Cut(first, ": started, in ")
	if !ok || dir != filepath.Join(root, ".verse", "exec") || !strings.Contains(out, "\nstdout: "+id+".sock while it runs, "+id+".out\n") {
		t.Fatalf("the start's answer:\n%s", out)
	}
	return id
}

// waitRun is what `secret-exec-wait` answers for the run id.
func waitRun(ctx context.Context, tools []verse.Tool, id string) (string, error) {
	out, _, err := tools[1].Do(ctx, task.ToolUse{Name: tools[1].Name, Input: map[string]any{"id": id}})
	return out, err
}

// runsHere stands in for the sandbox: the command runs as the test, in its directory.
type runsHere struct{}

func (runsHere) Confine(cmd *exec.Cmd, root, dir string, _ delivery.Network, _ string) error {
	cmd.Dir = dir
	return nil
}

// THE RUN: the call answers at the start with the id, and the wait answers the id, the exit
// status and each stream's file and tail, the secret's value redacted; the files are in the
// workspace, the end among them; the sockets serve the output from its start; a kill ends the
// run, and a wait given up does not. The log and the answer never hold the value.
func TestSecretExecRunsTheCommandAndServesItsOutput(t *testing.T) {
	// A root short enough for a unix socket's path under it, on macOS too.
	root, err := os.MkdirTemp("", "vx")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	store := t.TempDir()
	if err := os.WriteFile(filepath.Join(store, "npm-token"), []byte("npm_s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := withSecret(t)
	l := &runner.Launch{ID: "t1", Prompt: "p", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, Environment: env}
	s := &delivery.Server{Log: io.Discard,
		Ceilings: map[string]systems.Ceiling{"secret": {{Verbs: map[string][]systems.Shape{"exec": anyVars(t)}}}},
		Services: delivery.Services{Secrets: secretstore.Open(store), Confine: runsHere{}}}
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
	tools := secretTools(cl, &workspace.Workspace{Root: root}, env)
	kill := tools[2]
	script := func(script string) map[string]any {
		return map[string]any{"name": "npm-token", "argv": []any{"sh", "-c", script}, "timeout": 20}
	}
	exec := filepath.Join(root, ".verse", "exec")
	id := startRun(t, tools, root, script("echo t=$NPM_TOKEN; echo oops >&2; exit 3"))
	out, err := waitRun(t.Context(), tools, id)
	if err != nil || id != "e1" {
		t.Fatalf("%s: %q, %v", id, out, err)
	}
	for _, want := range []string{"e1: exited 3\n", "stdout: .verse/exec/e1.out, 21 bytes:\nt=[secret npm-token]\n", "stderr: .verse/exec/e1.err, 5 bytes:\noops\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("the answer lacks %q:\n%s", want, out)
		}
	}
	for file, want := range map[string]string{"e1.out": "t=[secret npm-token]\n", "e1.err": "oops\n", "e1.exit": "exited 3\n"} {
		if got, err := os.ReadFile(filepath.Join(exec, file)); err != nil || string(got) != want {
			t.Errorf("%s: %q, %v", file, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(exec, "e1.sock")); err == nil {
		t.Error("the socket is still there after the run")
	}
	// A wait after the end answers the same.
	if again, err := waitRun(t.Context(), tools, id); err != nil || again != out {
		t.Errorf("a second wait: %q, %v", again, err)
	}
	// The socket serves the stream from its start: connected once the command has written and
	// while it sleeps, the reader gets what was written before, what comes after, and the end.
	id = startRun(t, tools, root, script("echo early; sleep 1; echo late"))
	for range 200 {
		if b, _ := os.ReadFile(filepath.Join(exec, id+".out")); len(b) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, err := net.Dial("unix", filepath.Join(exec, id+".sock"))
	if err != nil {
		t.Fatal(err)
	}
	along, err := io.ReadAll(c)
	c.Close()
	if err != nil || string(along) != "early\nlate\n" {
		t.Errorf("read along: %q, %v", along, err)
	}
	// The end is in its file by the time the stream ends.
	if got, err := os.ReadFile(filepath.Join(exec, id+".exit")); err != nil || string(got) != "exited 0\n" {
		t.Errorf("the end's file: %q, %v", got, err)
	}
	if out, err := waitRun(t.Context(), tools, id); err != nil || !strings.Contains(out, "e2: exited 0") {
		t.Errorf("the run: %q, %v", out, err)
	}
	// A wait given up leaves the run, and the kill ends it.
	id = startRun(t, tools, root, script("sleep 30"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if out, err := waitRun(ctx, tools, id); err == nil {
		t.Errorf("a wait given up answered %q", out)
	}
	if out, blocked, err := kill.Do(t.Context(), task.ToolUse{Name: kill.Name, Input: map[string]any{"id": id}}); err != nil || blocked || out != "killed e3" {
		t.Errorf("the kill: %q, %v, %v", out, blocked, err)
	}
	if out, err := waitRun(t.Context(), tools, id); err != nil || !strings.Contains(out, "e3: killed: the task let go of the command's output") {
		t.Errorf("the killed run: %q, %v", out, err)
	}
	if got, err := os.ReadFile(filepath.Join(exec, "e3.exit")); err != nil || !strings.HasPrefix(string(got), "killed: the task let go") {
		t.Errorf("the killed run's end: %q, %v", got, err)
	}
	if out, blocked, _ := kill.Do(t.Context(), task.ToolUse{Name: kill.Name, Input: map[string]any{"id": id}}); !blocked || !strings.HasPrefix(out, "not killed") {
		t.Errorf("a second kill: %q", out)
	}
	// A call given up before the command has started answers no run.
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	if out, _, err := tools[0].Do(gone, task.ToolUse{Name: tools[0].Name, Input: script("sleep 30")}); err != nil || strings.Contains(out, ": started, in ") || !strings.Contains(out, "e4: killed: the task let go") {
		t.Errorf("the call given up: %q, %v", out, err)
	}
	cl.Close()
	<-served
}

// dialSoon connects to the unix socket at path once it is there.
func dialSoon(path string) (net.Conn, error) {
	var err error
	for range 200 {
		var c net.Conn
		if c, err = net.Dial("unix", path); err == nil {
			return c, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil, err
}

// withStdinSecret is withSecret with shapes that also admit an interactive run.
func withStdinSecret(t *testing.T) systems.Environment {
	t.Helper()
	n, err := parse.Parse([]byte("null"))
	if err != nil {
		t.Fatal(err)
	}
	shapes := []systems.Shape{{"env": systems.Plain(n), "stdin": systems.Plain(n)}}
	return systems.Environment{Systems: systems.Systems{
		"secret": {{Attributes: map[string]string{"name": "npm-token", "as": "NPM_TOKEN"}, Verbs: map[string][]systems.Shape{"exec": shapes}}},
	}}
}

// THE INPUT SOCKET: with `stdin: true` the run serves `<id>.in.sock` beside the outputs. The
// first connection is the input, whole, mirrored to `<id>.in`; a later one is closed at once;
// and closing the first is the command's EOF while its output still flows. The wait's answer
// says the input's file and size.
func TestSecretExecServesTheCommandsStdin(t *testing.T) {
	root, err := os.MkdirTemp("", "vx")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	store := t.TempDir()
	if err := os.WriteFile(filepath.Join(store, "npm-token"), []byte("npm_s3cr3t"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := withStdinSecret(t)
	l := &runner.Launch{ID: "t1", Prompt: "p", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, Environment: env}
	s := &delivery.Server{Log: io.Discard,
		Ceilings: map[string]systems.Ceiling{"secret": {{Verbs: env.Systems["secret"][0].Verbs}}},
		Services: delivery.Services{Secrets: secretstore.Open(store), Confine: runsHere{}}}
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
	tools := secretTools(cl, &workspace.Workspace{Root: root}, env)
	id := startRun(t, tools, root, map[string]any{"name": "npm-token", "argv": []any{"sh", "-c", "cat; echo done"}, "timeout": 20, "stdin": true})
	c, err := net.Dial("unix", filepath.Join(root, ".verse", "exec", id+".in.sock"))
	if err != nil {
		t.Fatal(err)
	}
	// A second connection is closed at once: one input, whole.
	c2, err := net.Dial("unix", filepath.Join(root, ".verse", "exec", "e1.in.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("the second connection read: %v, want EOF", err)
	}
	c2.Close()
	if _, err := c.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	c.Close()
	out, err := waitRun(t.Context(), tools, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"e1: exited 0", "stdout: .verse/exec/e1.out, 11 bytes:\nhello\ndone\n", "stdin: .verse/exec/e1.in, 6 bytes"} {
		if !strings.Contains(out, want) {
			t.Errorf("the answer lacks %q:\n%s", want, out)
		}
	}
	if got, err := os.ReadFile(filepath.Join(root, ".verse", "exec", "e1.in")); err != nil || string(got) != "hello\n" {
		t.Errorf("e1.in: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".verse", "exec", "e1.in.sock")); err == nil {
		t.Error("the input socket is still there after the run")
	}
	// Without `stdin: true` nothing serves: a plain run leaves no in.sock behind it.
	id = startRun(t, tools, root, map[string]any{"name": "npm-token", "argv": []any{"sh", "-c", "true"}, "timeout": 20})
	if out, err := waitRun(t.Context(), tools, id); err != nil || !strings.Contains(out, "e2: exited 0") || strings.Contains(out, "stdin:") {
		t.Errorf("the plain run: %q, %v", out, err)
	}
	cl.Close()
	<-served
}
