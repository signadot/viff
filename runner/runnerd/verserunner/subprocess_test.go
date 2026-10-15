package verserunner

import (
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

// taskArg makes this test binary the verse runner's task process (TestMain), as
// `runnerd verse -task` is.
const taskArg = "verserunner-task-process"

func TestMain(m *testing.M) {
	if slices.Contains(os.Args, taskArg) {
		if err := ServeTask(os.Stdin, os.Stderr); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
		}
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// serve serves the verse runner as a standalone runner, with its task processes confined by sb.
// work is where the tasks' directories are made.
func serve(t *testing.T, s Secrets, sb runnerd.Sandbox) (rt task.Runner, work string) {
	t.Helper()
	// Symlinks resolved, as a task's $PWD has them: on macOS the temp dir is under /var, a link
	// to /private/var.
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control, err := os.MkdirTemp("", "vr") // a unix socket's path is limited to about 100 bytes
	if err != nil {
		t.Fatal(err)
	}
	r, err := Standalone{
		Secrets: s, Sandbox: sb, Command: []string{os.Args[0], "-test.run=^$", taskArg},
		Work: work, Control: control, Log: io.Discard,
	}.Start()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(func() { srv.Close(); r.Shutdown(); os.RemoveAll(control) })
	return client.HTTP(srv.URL), work
}

// script runs a script as a task, and returns its output once it has ended.
func script(t *testing.T, rt task.Runner, id, src string) task.Snapshot {
	t.Helper()
	rt.Spawn(task.Launch{ID: id, Driver: "scripted", Vector: &task.Vector{Default: task.Auto}, Prompt: src})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if in, err := rt.(task.Lister).TaskSnapshot(id); err == nil && in.Status.Terminal() {
			return in
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s didn't end", id)
	return task.Snapshot{}
}

// A task runs in a process of its own, in a directory of its own that is also its HOME, and its
// process has none of the runner's secrets: not in its environment, though the runner's has them.
func TestATaskRunsInAProcessOfItsOwn(t *testing.T) {
	const key = "pk-test-secret"
	t.Setenv(EnvPortkeyAPIKey, key)
	rt, work := serve(t, Secrets{PortkeyAPIKey: key}, runnerd.Unconfined{})
	in := script(t, rt, "t1", "#!/bin/sh\necho $$ $PWD $HOME\nenv | grep -c "+key+" || true")
	if in.Status != task.Done {
		t.Fatalf("task: %+v", in)
	}
	f := strings.Fields(in.Result)
	if len(f) != 4 {
		t.Fatalf("output: %q", in.Result)
	}
	dir, home, secrets := f[1], f[2], f[3]
	if filepath.Dir(dir) != work || !strings.HasPrefix(filepath.Base(dir), "task-") {
		t.Fatalf("ran in %q, want a task- directory under %s", dir, work)
	}
	if home != dir {
		t.Errorf("HOME is %q, want the task's directory", home)
	}
	if secrets != "0" {
		t.Errorf("the runner's secret is in the task process's environment")
	}
}

// The task's process holds the runner's secrets in its memory, so a task can't read it,
// whatever the kernel's ptrace_scope allows: not its memory, and not its environment either
// unless the task runs as root with capabilities, which bwrap drops (TestBwrapConfinesATask).
// The script's parent is the task's process.
func TestATaskCantReadItsProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("procfs and prctl are Linux's")
	}
	rt, _ := serve(t, Secrets{PortkeyAPIKey: "pk-test-secret"}, runnerd.Unconfined{})
	in := script(t, rt, "t1", `#!/bin/sh
for f in environ mem; do
  if (exec 3</proc/$PPID/$f) 2>/dev/null; then echo "$f: open"; else echo "$f: refused"; fi
done
`)
	if in.Status != task.Done {
		t.Fatalf("task: %+v", in)
	}
	want := []string{"mem: refused"}
	if os.Geteuid() != 0 {
		want = append(want, "environ: refused")
	}
	for _, want := range want {
		if !strings.Contains(in.Result, want) {
			t.Errorf("%s: not in the output:\n%s", want, in.Result)
		}
	}
}

// Under bwrap, a task may write its own directory and nothing else of the runner's, and doesn't
// see the runner's other tasks.
func TestBwrapConfinesATask(t *testing.T) {
	sb, err := runnerd.Bwrap()
	if err != nil {
		t.Skipf("bwrap can't run here: %v", err)
	}
	rt, work := serve(t, Secrets{}, sb)
	if err := os.WriteFile(filepath.Join(work, "another-task"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	in := script(t, rt, "t1", `#!/bin/sh
touch mine && echo wrote-own
touch /usr/verse-test 2>/dev/null || echo no-usr
ls .. | grep -q another-task || echo no-others
touch /tmp/scratch && echo own-tmp
(exec 3</proc/$PPID/environ) 2>/dev/null || (exec 3</proc/$PPID/mem) 2>/dev/null || echo process-unreadable
`)
	if in.Status != task.Done {
		t.Fatalf("task: %+v", in)
	}
	for _, want := range []string{"wrote-own", "no-usr", "no-others", "own-tmp", "process-unreadable"} {
		if !strings.Contains(in.Result, want) {
			t.Errorf("%s: not in the output:\n%s", want, in.Result)
		}
	}
}
