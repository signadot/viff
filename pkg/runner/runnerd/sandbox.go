package runnerd

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/delivery"
)

// Sandbox confines a task's process. It is drawn once, around the whole process, so everything
// the task runs (an agent's loop, its shell, a script) is inside it.
//
// A SubprocessHandler asks for one. An InProcessHandler runs its tasks in its own process and has
// none.
type Sandbox interface {
	// Name is how the sandbox is named on the command line and in the log.
	Name() string
	// Provides reports whether this sandbox can build net, and says what it can't if not.
	Provides(net Network) error
	// Wrap rewrites cmd to run confined, with dir the only path it may write and its working
	// directory, unless cmd.Dir names one under it, and net what it may reach. programs are
	// the other programs it runs (a script's verse-call-tool), which it finds where they are,
	// read only. The descriptors on cmd.ExtraFiles are passed through. Called only after
	// Provides said yes.
	Wrap(cmd *exec.Cmd, dir string, net Network, programs []string) error
}

// Confiner confines a command the runner runs for a task apart from the task's own process
// (delivery.Confine, for `secret.exec`): in the same kind of sandbox the task's process is in,
// over the task's root, under the task's network, with the paths the operator keeps read only.
// The program is one of the runner's, under a system directory or a readOnly path, where it is
// and where it resolves to: a program of the task's would read the secret for it. A runner that
// runs its tasks unconfined confines nothing, and says so.
type Confiner struct {
	Sandbox  Sandbox
	ReadOnly []string
	// Kept is the runner's own directories (KeptDirs), which no readOnly path may reach.
	Kept map[string]string
}

// Confine implements delivery.Confine.
func (c Confiner) Confine(cmd *exec.Cmd, root, dir string, net delivery.Network) error {
	if c.Sandbox == nil || c.Sandbox.Name() == (Unconfined{}).Name() {
		return errors.New("this runner runs its tasks unconfined (-sandbox none), and runs no command with a secret")
	}
	if !filepath.IsAbs(cmd.Path) {
		return fmt.Errorf("the program %q is no absolute path", cmd.Path)
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(dir) || !underAny(filepath.Clean(dir), []string{root}) {
		return fmt.Errorf("the directory %s is not under the task's root %s", dir, root)
	}
	// Each path as written and as it resolves, on both sides: a link under a kept path to a
	// program elsewhere is refused for where it resolves, and a readOnly path that is itself
	// reached through a link (/var on macOS) admits what lies under where it resolves.
	var kept []string
	for _, d := range c.ReadOnly {
		kept = append(kept, d, resolved(d))
	}
	for _, p := range []string{cmd.Path, resolved(cmd.Path)} {
		if !UnderSystem(p) && !underAny(p, kept) {
			return fmt.Errorf("the program %s is under no system directory and no readOnly path of the runner's; "+
				"a command run with a secret starts from one of the runner's own, and what that one runs is the "+
				"environment's shapes' to bound", p)
		}
	}
	cmd.Dir = dir
	// The readOnly paths this host lacks were said when the task's own process started
	// (SubprocessHandler), and are not said again for each command.
	return c.Sandbox.Wrap(cmd, root, Network{Mode: net.Mode, Egress: net.Egress}, withReadOnly(nil, c.ReadOnly, c.Kept, io.Discard))
}

// underAny is whether path is, or lies under, one of dirs.
func underAny(path string, dirs []string) bool {
	for _, d := range dirs {
		if within(path, d) {
			return true
		}
	}
	return false
}

// Network is what a task's process may reach: the runner's own network (NetworkOpen,
// the default), nothing (NetworkNone), or nothing but one way out, an allow-listing proxy
// the runner serves for the task (NetworkAllow).
type Network struct {
	Mode string
	// Egress is the unix socket of that proxy, on the runner's side, for NetworkAllow.
	// The sandbox binds it in at EgressPath.
	Egress string
}

// The network a sandbox builds a task, as the launch's environment's `network` names it: the runner's
// own (open), none, or the hosts a proxy allows (allow).
const (
	NetworkOpen  = runner.NetworkOpen
	NetworkNone  = runner.NetworkNone
	NetworkAllow = runner.NetworkAllow
)

// NetworkModes is every mode, weakest first.
var NetworkModes = []string{NetworkOpen, NetworkNone, NetworkAllow}

// EgressPath is where the egress socket appears inside a sandbox: a fixed path, because the
// sandbox binds it and the task's process dials it (UseEgress), two programs agreeing on a name.
const EgressPath = "/run/verse-egress"

func (n Network) mode() string {
	if n.Mode == "" {
		return NetworkOpen
	}
	return n.Mode
}

func checkMode(n Network) error {
	if !slices.Contains(NetworkModes, n.mode()) {
		return fmt.Errorf("network: no mode %q (want %s)", n.Mode, strings.Join(NetworkModes, ", "))
	}
	return nil
}

// SandboxNamed is the sandbox a command line names: "bwrap", or "none". bwrap that can't run
// on this machine is an error here, at startup, not at the first launch.
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

// Provides an open network alone: an unconfined process has the runner's network, and a proxy
// it is free to ignore is advice, not a limit.
func (Unconfined) Provides(net Network) error {
	if err := checkMode(net); err != nil {
		return err
	}
	if net.mode() != NetworkOpen {
		return fmt.Errorf("network: %s needs a sandbox that can take the network away (bwrap); "+
			"this runner runs its tasks unconfined", net.Mode)
	}
	return nil
}

func (Unconfined) Wrap(cmd *exec.Cmd, dir string, _ Network, _ []string) error {
	cmd.Dir = cmp.Or(cmd.Dir, dir)
	return nil
}

// BwrapSandbox confines a task's process with bubblewrap: its own pid, ipc and uts namespaces,
// no capabilities, a read-only system, and the task's directory the only writable path. Its
// network is the runner's (open), none, or none but its egress proxy (allow).
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
	b := BwrapAt(bin)
	probe := exec.Command("true")
	if err := b.Wrap(probe, os.TempDir(), Network{Mode: NetworkNone}, nil); err != nil {
		return nil, err
	}
	if out, err := probe.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sandbox bwrap: bwrap doesn't run here (%v): %s", err,
			strings.TrimSpace(string(out)))
	}
	return b, nil
}

// BwrapAt is the bwrap sandbox using the bwrap at bin, unprobed.
func BwrapAt(bin string) *BwrapSandbox { return &BwrapSandbox{bin: bin} }

func (*BwrapSandbox) Name() string { return "bwrap" }

func (*BwrapSandbox) Provides(net Network) error { return checkMode(net) }

// systemDirs are bound read-only, by name: binding / and then a writable subtree is the shape
// that exposes whatever wasn't thought of.
var systemDirs = []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/opt"}

func (b *BwrapSandbox) Wrap(cmd *exec.Cmd, dir string, net Network, programs []string) error {
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
	if conf, err := filepath.EvalSymlinks("/etc/resolv.conf"); err == nil && !UnderSystem(conf) {
		args = append(args, "--ro-bind-try", conf, conf)
	}
	// The programs have to exist inside: the runner's own binary is a task's process, and it and
	// the others may be installed outside the system directories. Bound read only at their own
	// paths, so nothing the task may write shares their inode.
	for _, p := range append([]string{cmd.Path}, programs...) {
		if prog, err := filepath.Abs(p); err == nil && !UnderSystem(prog) {
			args = append(args, "--ro-bind", prog, prog)
		}
	}
	// The working directory is dir, or what cmd.Dir names under it: a command run for the task
	// in one directory of its tree (Confiner). It is bwrap's to change to, inside, and not the
	// host's: cmd.Dir is taken off, so nothing changes directory on the runner's side.
	args = append(args, "--bind", dir, dir, "--chdir", cmp.Or(cmd.Dir, dir))
	cmd.Dir = ""
	switch net.mode() {
	case NetworkNone:
		args = append(args, "--unshare-net")
	case NetworkAllow:
		// No route out, and then exactly one thing bound back in: a socket rather than an
		// address, because an address inside an unshared network reaches nothing. Unlike the
		// control socket, the task is meant to reach this one: on its other end is the allow-list.
		if net.Egress == "" {
			return fmt.Errorf("network: allow, but this task was given no egress proxy to bind")
		}
		args = append(args, "--unshare-net", "--bind", net.Egress, EgressPath)
	}
	args = append(args, "--")
	args = append(args, cmd.Args...)
	cmd.Path = b.bin
	cmd.Args = append([]string{b.bin}, args...)
	return nil
}

// UnderSystem reports whether path is, or lies under, one of the system directories: what
// every task's sandbox has, read only, whatever else it is given. So nothing the runner keeps
// from its tasks may be there (CheckSecretStore).
func UnderSystem(path string) bool {
	for _, d := range systemDirs {
		if path == d || strings.HasPrefix(path, d+"/") {
			return true
		}
	}
	return false
}
