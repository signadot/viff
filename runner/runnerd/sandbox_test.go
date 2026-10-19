package runnerd_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/signadot/verse/runner/runnerd"
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
	if err := b.Wrap(open, "/w", runnerd.Network{}, nil); err != nil {
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
	_ = b.Wrap(none, "/w", runnerd.Network{Mode: runnerd.NetworkNone}, nil)
	if !strings.Contains(strings.Join(none.Args, " "), "--unshare-net") {
		t.Error("a network of none did not unshare the network")
	}

	allow := exec.Command("x")
	if err := b.Wrap(allow, "/w", runnerd.Network{Mode: runnerd.NetworkAllow, Egress: "/ctl/e1.sock"}, nil); err != nil {
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
	if err := b.Wrap(exec.Command("x"), "/w", runnerd.Network{Mode: runnerd.NetworkAllow}, nil); err == nil {
		t.Error("an allow network with no egress proxy was wrapped anyway")
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
		if err := sb.Wrap(cmd, t.TempDir(), runnerd.Network{}, nil); err != nil {
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
