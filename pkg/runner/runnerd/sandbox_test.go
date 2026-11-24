package runnerd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/runner/runnerd/delivery"
)

// An unconfined process has the runner's network, so Unconfined provides an open network and
// REFUSES the rest: a proxy a task is free to ignore is advice, and a floor that can be stepped
// around is not one.
func TestUnconfinedProvidesOnlyAnOpenNetwork(t *testing.T) {
	u := runnerd.Unconfined{}
	if err := u.Provides(runnerd.Network{}); err != nil {
		t.Errorf("the default network is refused: %v", err)
	}
	if err := u.Provides(runnerd.Network{Mode: runnerd.NetworkOpen}); err != nil {
		t.Errorf("an open network is refused: %v", err)
	}
	for _, mode := range []string{runnerd.NetworkNone, runnerd.NetworkAllow} {
		err := u.Provides(runnerd.Network{Mode: mode})
		if err == nil {
			t.Errorf("unconfined claimed a %s network, which it cannot take away", mode)
			continue
		}
		if !strings.Contains(err.Error(), "network") || !strings.Contains(err.Error(), "bwrap") {
			t.Errorf("the refusal of %s does not say what would provide it: %v", mode, err)
		}
	}
	if err := u.Provides(runnerd.Network{Mode: "firewalled"}); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

// bwrap's network control is one switch: open leaves it, none takes it away, and allow takes
// it away and binds one socket back in, at the path the task is told to dial.
func TestBwrapWrapsTheNetworkAsAsked(t *testing.T) {
	b := runnerd.BwrapAt("/usr/bin/bwrap")
	for _, mode := range runnerd.NetworkModes {
		if err := b.Provides(runnerd.Network{Mode: mode}); err != nil {
			t.Errorf("bwrap refuses %s: %v", mode, err)
		}
	}

	open := exec.Command("runner", "-task")
	if err := b.Wrap(open, "/w", runnerd.Network{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if open.Path != "/usr/bin/bwrap" {
		t.Errorf("Path = %q, want the bwrap binary", open.Path)
	}
	joined := strings.Join(open.Args, " ")
	for _, want := range []string{"--die-with-parent", "--unshare-pid", "--bind /w /w", "--chdir /w", "-- runner -task"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "--unshare-net") {
		t.Error("the network was unshared for an open network")
	}
	if strings.Contains(joined, "--bind /usr") {
		t.Error("/usr is bound writable")
	}

	none := exec.Command("x")
	_ = b.Wrap(none, "/w", runnerd.Network{Mode: runnerd.NetworkNone}, nil, "")
	if !strings.Contains(strings.Join(none.Args, " "), "--unshare-net") {
		t.Error("a network of none did not unshare the network")
	}

	allow := exec.Command("x")
	if err := b.Wrap(allow, "/w", runnerd.Network{Mode: runnerd.NetworkAllow, Egress: "/ctl/e1.sock"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(allow.Args, " ")
	if !strings.Contains(joined, "--unshare-net") {
		t.Error("allow did not unshare the network, so the allowlist is optional")
	}
	if !strings.Contains(joined, "--bind /ctl/e1.sock "+runnerd.EgressPath) {
		t.Errorf("the egress socket is not bound into the sandbox:\n%s", joined)
	}
	// Allow with no socket to bind is a refusal rather than a sandbox with no way out that
	// nobody asked for.
	if err := b.Wrap(exec.Command("x"), "/w", runnerd.Network{Mode: runnerd.NetworkAllow}, nil, ""); err == nil {
		t.Error("an allow network with no egress proxy was wrapped anyway")
	}
}

// THE INSTALL ROOT IS ONE READ-ONLY BIND AT A FIXED PATH, made when the task starts and never
// again: a wrap given one binds it read only at InstPath, where the runner's relative links and
// the task's PATH agree to look, and a wrap given none binds nothing there.
func TestTheInstallRootIsBoundReadOnlyAtItsPath(t *testing.T) {
	b := runnerd.BwrapAt("/usr/bin/bwrap")
	with := exec.Command("runner", "-task")
	if err := b.Wrap(with, "/w", runnerd.Network{}, nil, "/state/cellar/.tasks/task-1"); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(with.Args, " "); !strings.Contains(joined, "--ro-bind /state/cellar/.tasks/task-1 "+runnerd.InstPath) {
		t.Errorf("the install root is not bound read only:\n%s", joined)
	}
	without := exec.Command("runner", "-task")
	if err := b.Wrap(without, "/w", runnerd.Network{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(without.Args, " "), runnerd.InstPath) {
		t.Errorf("a task with no install root was given a bind at %s", runnerd.InstPath)
	}
}

// The task process is reached by inherited DESCRIPTORS, so wrapping it must not disturb them: a
// sandbox that rebuilt the command rather than rewriting it would drop them, and the symptom would
// be a process that starts and never answers.
func TestWrappingKeepsTheInheritedDescriptors(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "fd")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, sb := range []runnerd.Sandbox{runnerd.Unconfined{}, runnerd.BwrapAt("/usr/bin/bwrap")} {
		cmd := exec.Command("runner", "-task")
		cmd.ExtraFiles = []*os.File{f, f}
		if err := sb.Wrap(cmd, t.TempDir(), runnerd.Network{}, nil, ""); err != nil {
			t.Fatalf("%s: %v", sb.Name(), err)
		}
		if len(cmd.ExtraFiles) != 2 {
			t.Errorf("%s: the sandbox dropped the control listener and the lifeline", sb.Name())
		}
		if !strings.Contains(strings.Join(cmd.Args, " "), "runner -task") {
			t.Errorf("%s: the task process is no longer what gets run: %v", sb.Name(), cmd.Args)
		}
	}
}

// A COMMAND RUN FOR A TASK IS CONFINED AS THE TASK'S PROCESS IS, over the task's root and under
// its network, started in the directory of the tree it is for: the sandbox binds the root and
// changes to the directory. The program is one of the runner's, under a system directory, a
// readOnly path or the task's install root, which is bound as the task's own sandbox has it;
// another is refused, and so is every command where the tasks run unconfined.
func TestAConfinedCommandRunsOverTheRootInItsDirectory(t *testing.T) {
	ro := t.TempDir()
	c := runnerd.Confiner{Sandbox: runnerd.BwrapAt("/usr/bin/bwrap"), ReadOnly: []string{ro}}
	cmd := exec.Command("/usr/bin/npm", "publish")
	if err := c.Confine(cmd, "/w", "/w/packages/a", delivery.Network{Mode: runnerd.NetworkAllow, Egress: "/ctl/e1.sock"}, ""); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--bind /w /w --chdir /w/packages/a", "--unshare-net --bind /ctl/e1.sock " + runnerd.EgressPath,
		"--ro-bind " + ro + " " + ro, "-- /usr/bin/npm publish"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q:\n%s", want, joined)
		}
	}
	// A program the operator keeps read only is the runner's too; one anywhere else is not.
	if err := c.Confine(exec.Command(filepath.Join(ro, "bin/node")), "/w", "/w", delivery.Network{}, ""); err != nil {
		t.Errorf("a program under a readOnly path: %v", err)
	}
	own := exec.Command(filepath.Join(t.TempDir(), "node"))
	if err := c.Confine(own, "/w", "/w", delivery.Network{}, ""); err == nil || !strings.Contains(err.Error(), "under no system directory, no readOnly path of the runner's and no install root") {
		t.Errorf("a program of the task's: %v", err)
	}
	// A link under a system directory to a program elsewhere is refused for where it resolves.
	link := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(link, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "evil")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(link, "node")); err != nil {
		t.Fatal(err)
	}
	linked := runnerd.Confiner{Sandbox: runnerd.BwrapAt("/usr/bin/bwrap"), ReadOnly: []string{link}}
	if err := linked.Confine(exec.Command(filepath.Join(link, "node")), "/w", "/w", delivery.Network{}, ""); err == nil || !strings.Contains(err.Error(), target) {
		t.Errorf("a link out of a readOnly path: %v", err)
	}
	// A PROGRAM IN THE TASK'S INSTALL ROOT is the runner's writing, not the task's, so a command
	// with a secret may start from it: it is named by the path inside the sandbox, the root is
	// bound read only at that path, and the check lands on this side, under inst.
	inst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inst, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inst, "bin", "jq"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	installed := exec.Command(runnerd.InstPath+"/bin/jq", ".")
	if err := c.Confine(installed, "/w", "/w", delivery.Network{}, inst); err != nil {
		t.Fatalf("an installed program: %v", err)
	}
	joined = strings.Join(installed.Args, " ")
	for _, want := range []string{"--ro-bind " + inst + " " + runnerd.InstPath, "-- " + runnerd.InstPath + "/bin/jq ."} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q:\n%s", want, joined)
		}
	}
	// The program's own path is not bound: it is no path of this side's, and the root's bind is
	// what puts it inside.
	if strings.Contains(joined, "--ro-bind "+runnerd.InstPath) {
		t.Errorf("the program's inside path was bound as if it were the runner's:\n%s", joined)
	}
	// A link that leaves the install root is refused for where it lands, as one out of a
	// readOnly path is; and a task with no install root has no program in one.
	out := filepath.Join(t.TempDir(), "evil")
	if err := os.WriteFile(out, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(inst, "bin", "node")); err != nil {
		t.Fatal(err)
	}
	if err := c.Confine(exec.Command(runnerd.InstPath+"/bin/node"), "/w", "/w", delivery.Network{}, inst); err == nil || !strings.Contains(err.Error(), out) {
		t.Errorf("a link out of the install root: %v", err)
	}
	if err := c.Confine(exec.Command(runnerd.InstPath+"/bin/jq"), "/w", "/w", delivery.Network{}, ""); err == nil || !strings.Contains(err.Error(), "this task has none") {
		t.Errorf("an install root the task does not have: %v", err)
	}
	// Unconfined, nothing is run.
	none := runnerd.Confiner{Sandbox: runnerd.Unconfined{}}
	if err := none.Confine(exec.Command("/usr/bin/npm"), "/w", "/w", delivery.Network{}, ""); err == nil || !strings.Contains(err.Error(), "unconfined") {
		t.Errorf("unconfined: %v", err)
	}
}

// THE DIRECTORY IS UNDER THE ROOT, AND THE CHANGE OF DIRECTORY IS THE SANDBOX'S: a Confiner
// refuses a directory outside the root, and bwrap's Wrap takes cmd.Dir for its own --chdir and
// leaves it unset, so nothing changes directory on the runner's side. The task process's own
// Wrap, which sets no cmd.Dir, changes to the root as it did.
func TestTheDirectoryIsUnderTheRootAndTheSandboxChangesToIt(t *testing.T) {
	c := runnerd.Confiner{Sandbox: runnerd.BwrapAt("/usr/bin/bwrap")}
	for _, dir := range []string{"/etc", "/", "/w2", "/w/../etc", "packages"} {
		if err := c.Confine(exec.Command("/usr/bin/npm"), "/w", dir, delivery.Network{}, ""); err == nil || !strings.Contains(err.Error(), "not under the task's root") {
			t.Errorf("dir %s: %v", dir, err)
		}
	}
	if err := c.Confine(exec.Command("no-such-program-here"), "/w", "/w", delivery.Network{}, ""); err == nil || !strings.Contains(err.Error(), "no absolute path") {
		t.Errorf("a bare program: %v", err)
	}
	cmd := exec.Command("/usr/bin/npm")
	if err := c.Confine(cmd, "/w", "/w/a", delivery.Network{}, ""); err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != "" || !strings.Contains(strings.Join(cmd.Args, " "), "--chdir /w/a") {
		t.Errorf("Dir %q, args %v", cmd.Dir, cmd.Args)
	}
	own := exec.Command("runner", "-task")
	if err := runnerd.BwrapAt("/usr/bin/bwrap").Wrap(own, "/w", runnerd.Network{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if own.Dir != "" || !strings.Contains(strings.Join(own.Args, " "), "--chdir /w") {
		t.Errorf("the task's process: Dir %q, args %v", own.Dir, own.Args)
	}
}
