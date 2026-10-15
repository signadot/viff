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
//
// A sandbox REFUSES what it cannot build rather than building less. A task running with a weaker
// floor than was asked for is undetectable from the outside, which is the direction that costs; a
// refusal comes back as a refused spawn, in the runner's words, onto the flow that asked.
type Sandbox interface {
	// Name is how the sandbox is named on the command line and in the log.
	Name() string
	// Provides reports whether this sandbox can build net, and says what it cannot if not.
	Provides(net Network) error
	// Wrap rewrites cmd to run confined, with dir its working directory and the only path it
	// may write, and net what it may reach. The descriptors on cmd.ExtraFiles are passed through.
	// Called only after Provides said yes.
	Wrap(cmd *exec.Cmd, dir string, net Network) error
	// EgressAt is where a socket the runner serves at sock appears to the TASK. Only the sandbox
	// knows: one that remaps the filesystem binds it somewhere fixed, one that does not hands
	// back the path it was given. The task process dials whatever comes back.
	EgressAt(sock string) string
}

// Network is what a task's process may reach.
type Network struct {
	// Mode is the runner's own network (NetworkOpen, the default), none at all (NetworkNone), or
	// none but one way out: an allow-listing proxy the runner serves for the task
	// (NetworkRestricted).
	Mode string
	// Egress is the unix socket of that proxy, on the runner's side, for NetworkRestricted. The
	// sandbox binds it in, and the task is told where it landed (EgressAt).
	Egress string
}

// The network modes.
const (
	NetworkOpen       = "open"
	NetworkNone       = "none"
	NetworkRestricted = "restricted"
)

// NetworkModes is every mode, weakest first.
var NetworkModes = []string{NetworkOpen, NetworkNone, NetworkRestricted}

// EgressPath is where the egress socket appears INSIDE a sandbox that remaps the filesystem. A
// fixed path because the sandbox binds it and the task process dials it, and those are two
// different programs agreeing about one name.
const EgressPath = "/run/verse-egress"

func (n Network) mode() string {
	if n.Mode == "" {
		return NetworkOpen
	}
	return n.Mode
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
//
// It provides the runner's own network and refuses the rest. A proxy would work here, since it is
// a socket and a loopback port, and it would be advice: nothing stops the task opening its own
// connection. A floor that can be stepped around is not one.
type Unconfined struct{}

func (Unconfined) Name() string { return "none" }

func (Unconfined) Provides(net Network) error {
	switch net.mode() {
	case NetworkOpen:
		return nil
	case NetworkNone, NetworkRestricted:
		return fmt.Errorf("network: %s asks for a network this process cannot take away — an "+
			"unconfined task has the runner's network, so a proxy would be advice it is free to "+
			"ignore; run with a sandbox that unshares the network (bwrap)", net.mode())
	default:
		return fmt.Errorf("network: no mode %q (want %s)", net.Mode, strings.Join(NetworkModes, ", "))
	}
}

func (Unconfined) Wrap(cmd *exec.Cmd, dir string, _ Network) error {
	cmd.Dir = dir
	return nil
}

// EgressAt: nothing is remapped here, so the task sees the path the runner made. It never comes
// up in practice — Provides refuses restricted — and is answered rather than left to panic.
func (Unconfined) EgressAt(sock string) string { return sock }

// BwrapSandbox confines a task's process with bubblewrap: its own pid, ipc and uts namespaces,
// no capabilities, a read-only system, and the task's directory the only writable path. The
// network is the runner's, none, or none with one socket bound back in, as the task's Network
// asks: bwrap's network control is one switch, --unshare-net, and restricted egress is that
// switch plus a proxy the runner supplies (Egress).
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
	if err := b.Wrap(probe, os.TempDir(), Network{}); err != nil {
		return nil, err
	}
	if out, err := probe.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sandbox bwrap: bwrap doesn't run here (%v): %s", err,
			strings.TrimSpace(string(out)))
	}
	return b, nil
}

// BwrapAt is the bwrap sandbox using the program at bin, unprobed: for a deployment that knows
// where bwrap is, and for a test that only reads what Wrap writes.
func BwrapAt(bin string) *BwrapSandbox { return &BwrapSandbox{bin: bin} }

func (*BwrapSandbox) Name() string { return "bwrap" }

func (*BwrapSandbox) Provides(net Network) error {
	switch net.mode() {
	case NetworkOpen, NetworkNone, NetworkRestricted:
		return nil
	default:
		return fmt.Errorf("network: no mode %q (want %s)", net.Mode, strings.Join(NetworkModes, ", "))
	}
}

// systemDirs are bound read-only, by name: binding / and then a writable subtree is the shape
// that exposes whatever wasn't thought of.
var systemDirs = []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/opt"}

func (b *BwrapSandbox) Wrap(cmd *exec.Cmd, dir string, net Network) error {
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
	args = append(args, "--bind", dir, dir, "--chdir", dir)
	switch net.mode() {
	case NetworkNone:
		args = append(args, "--unshare-net")
	case NetworkRestricted:
		// No route out, and then exactly one thing bound back in. A socket rather than an
		// address, because an address inside an unshared netns is a different netns's address
		// and reaches nothing — the same reason the control plane is a descriptor.
		//
		// Unlike the control socket, this one is MEANT to be reachable by the task: what is on
		// the other end is an allowlist, so dialing it is the point rather than the risk.
		if net.Egress == "" {
			return fmt.Errorf("network: restricted, but this task was given no egress proxy to " +
				"bind — the runner serves one per task and did not")
		}
		args = append(args, "--unshare-net", "--bind", net.Egress, EgressPath)
	}
	args = append(args, "--")
	args = append(args, cmd.Args...)
	cmd.Path = b.bin
	cmd.Args = append([]string{b.bin}, args...)
	return nil
}

// EgressAt: bound to a fixed path inside, because the filesystem is remapped and the host's path
// means nothing in there.
func (*BwrapSandbox) EgressAt(string) string { return EgressPath }

func underSystem(path string) bool {
	for _, d := range systemDirs {
		if path == d || strings.HasPrefix(path, d+"/") {
			return true
		}
	}
	return false
}
