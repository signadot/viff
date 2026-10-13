package runnerd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Sandbox confines a task's process. It is drawn once, around the whole process, so everything
// the task runs (an agent's loop, its shell, a script) is inside it.
//
// A SubprocessHandler asks for one. An InProcessHandler runs its tasks in its own process and has
// none.
type Sandbox interface {
	// Name is how the sandbox is named on the command line and in the log.
	Name() string
	// Wrap rewrites cmd to run confined, with dir its working directory and the only path it
	// may write. The descriptors on cmd.ExtraFiles are passed through.
	Wrap(cmd *exec.Cmd, dir string) error
}

// SandboxNamed is the sandbox a command line names: "bwrap", or "none". bwrap that can't run
// on this machine is an error here, at startup, not at the first spawn.
func SandboxNamed(name string) (Sandbox, error) {
	switch name {
	case "bwrap":
		return Bwrap()
	case "none":
		return Unconfined{}, nil
	default:
		return nil, fmt.Errorf("no sandbox %q (want bwrap or none)", name)
	}
}

// Unconfined runs a task's process as the runner: it sets the working directory and nothing
// else. Asked for by name only, so a runner never runs unconfined by accident.
type Unconfined struct{}

func (Unconfined) Name() string { return "none" }

func (Unconfined) Wrap(cmd *exec.Cmd, dir string) error {
	cmd.Dir = dir
	return nil
}

// BwrapSandbox confines a task's process with bubblewrap: its own pid, ipc and uts namespaces,
// no capabilities, a read-only system, and the task's directory the only writable path. The network is the
// runner's.
//
// The task runs as the runner's own uid: bwrap maps the caller to itself.
type BwrapSandbox struct{ bin string }

// Bwrap is the bwrap sandbox, once bwrap is found and has run once on this machine. A kernel
// or AppArmor that doesn't allow unprivileged user namespaces fails here, with bwrap's words.
func Bwrap() (*BwrapSandbox, error) {
	bin, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("sandbox bwrap: bubblewrap is not on PATH: %w", err)
	}
	b := &BwrapSandbox{bin: bin}
	probe := exec.Command("true")
	if err := b.Wrap(probe, os.TempDir()); err != nil {
		return nil, err
	}
	if out, err := probe.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sandbox bwrap: bwrap doesn't run here (%v): %s", err,
			strings.TrimSpace(string(out)))
	}
	return b, nil
}

func (*BwrapSandbox) Name() string { return "bwrap" }

// systemDirs are bound read-only, by name: binding / and then a writable subtree is the shape
// that exposes whatever wasn't thought of.
var systemDirs = []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/opt"}

func (b *BwrapSandbox) Wrap(cmd *exec.Cmd, dir string) error {
	args := []string{
		"--die-with-parent", "--new-session",
		// No capabilities, even for a runner run as root, which bwrap would otherwise let keep
		// them: with them, a task could read its process's /proc files.
		"--cap-drop", "ALL",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
	}
	for _, d := range systemDirs {
		args = append(args, "--ro-bind-try", d, d)
	}
	// /etc/resolv.conf is a link out of /etc on a systemd host.
	if conf, err := filepath.EvalSymlinks("/etc/resolv.conf"); err == nil && !underSystem(conf) {
		args = append(args, "--ro-bind-try", conf, conf)
	}
	// The program has to exist inside: the runner's own binary is a task's process, and may be
	// installed outside the system directories.
	if prog, err := filepath.Abs(cmd.Path); err == nil && !underSystem(prog) {
		args = append(args, "--ro-bind", prog, prog)
	}
	args = append(args, "--bind", dir, dir, "--chdir", dir, "--")
	args = append(args, cmd.Args...)
	cmd.Path = b.bin
	cmd.Args = append([]string{b.bin}, args...)
	return nil
}

func underSystem(path string) bool {
	for _, d := range systemDirs {
		if path == d || strings.HasPrefix(path, d+"/") {
			return true
		}
	}
	return false
}
