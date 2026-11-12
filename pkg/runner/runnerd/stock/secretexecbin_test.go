package stock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/calltool"
	"github.com/signadot/verse/pkg/runner/runnerd/delivery"
	"github.com/signadot/verse/pkg/runner/runnerd/secretstore"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// VERSE-SECRET-EXEC IS THE COMMAND IN PLACE: built as main builds it and run against the tools
// a task's process offers, the command's stdout and stderr are its own, its stdin the command's
// input, its exit status the command's; a refusal is 126 with the reason; and a signal to it
// kills the run and is its exit status.
func TestVerseSecretExecRunsTheCommandInPlace(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH to build verse-secret-exec with")
	}
	bin := filepath.Join(t.TempDir(), "verse-secret-exec")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/signadot/verse/cmd/verse-secret-exec").CombinedOutput(); err != nil {
		t.Fatalf("building verse-secret-exec: %v\n%s", err, out)
	}
	// A root short enough for a unix socket's path under it, on macOS too.
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
	tools, _ := secretTools(cl, &workspace.Workspace{Root: root}, env)
	srv := &calltool.Server{Tools: described(tools), Call: func(ctx context.Context, name string, in map[string]any) (string, bool, error) {
		for _, tool := range tools {
			if tool.Name == name {
				return tool.Do(ctx, task.ToolUse{Name: name, Input: in})
			}
		}
		return "", false, fmt.Errorf("no tool %s", name)
	}}
	sock := filepath.Join(root, "t.sock")
	if err := srv.Start(sock); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	command := func(stdin string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), calltool.Env+"="+sock)
		cmd.Dir = t.TempDir() // the run's directory is the answer's, wherever this runs
		var o, e bytes.Buffer
		cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(stdin), &o, &e
		return cmd, &o, &e
	}
	status := func(err error) int {
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee):
			return ee.ExitCode()
		case err != nil:
			t.Fatal(err)
		}
		return 0
	}
	// The outputs and the status, a command that ends at once included: what the sockets did
	// not give is read from the files.
	cmd, o, e := command("", "-name", "npm-token", "-timeout", "20", "--", "sh", "-c", "echo t=$NPM_TOKEN; echo oops >&2; exit 3")
	if code := status(cmd.Run()); code != 3 || o.String() != "t=[secret npm-token]\n" || e.String() != "oops\n" {
		t.Errorf("the run: exit %d, stdout %q, stderr %q", code, o, e)
	}
	// The input, and output that comes while it runs.
	cmd, o, e = command("hello\n", "-name", "npm-token", "-timeout", "20", "-stdin", "--", "sh", "-c", "cat; sleep 0.3; echo done")
	if code := status(cmd.Run()); code != 0 || o.String() != "hello\ndone\n" || e.String() != "" {
		t.Errorf("the run with input: exit %d, stdout %q, stderr %q", code, o, e)
	}
	// A refusal: no block gives the secret.
	cmd, o, e = command("", "-name", "pypi-token", "--", "true")
	if code := status(cmd.Run()); code != 126 || o.String() != "" || !strings.Contains(e.String(), "not run: ") {
		t.Errorf("the refusal: exit %d, stdout %q, stderr %q", code, o, e)
	}
	// A signal kills the run, and is the status.
	cmd, o, e = command("", "-name", "npm-token", "-timeout", "60", "--", "sh", "-c", "echo up; exec sleep 60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for range 400 {
		if b, _ := os.ReadFile(filepath.Join(root, ".verse", "exec", "e3.out")); len(b) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	began := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := status(cmd.Wait()); code != 128+int(syscall.SIGTERM) || o.String() != "up\n" || time.Since(began) > 20*time.Second {
		t.Errorf("the signalled run: exit %d, stdout %q, stderr %q, after %s", code, o, e, time.Since(began))
	}
	if got, err := os.ReadFile(filepath.Join(root, ".verse", "exec", "e3.exit")); err != nil || !strings.HasPrefix(string(got), "killed: the task let go") {
		t.Errorf("the signalled run's end: %q, %v", got, err)
	}
	cl.Close()
	<-served
}
