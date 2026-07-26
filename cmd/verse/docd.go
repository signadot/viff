package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/scott-cotton/cli"
)

// goTonyO is the o command built from the pinned go-tony module — so the daemons
// `go run` brings up are always the version verse's libctl client is built against
// (no client/server skew).
const goTonyO = "github.com/signadot/tony-format/go-tony/cmd/o"

// docd is the resolved docd the store and Sources use: its client face (logd
// session protocol) and controller/MOUNT face, plus a stop that tears down anything
// verse spawned.
type docd struct {
	clientAddr string
	mountAddr  string
	stop       func()
}

// resolveDocd yields docd's addresses. Three modes, in order:
//   - --docd-addr given: connect to a running docd (verse spawns nothing).
//   - --data given: bring logd+docd up on that dir via `o sys up`.
//   - neither: make a fresh (kept) temp data dir and do the same — a real docd by
//     default, zero config, for actual use rather than a demo.
//
// For the spawned modes verse reserves free loopback ports and passes them to
// `o sys up`, so it already knows the addresses (no stdout scraping), and waits for
// docd to accept connections before returning.
func resolveDocd(cfg *upConfig, cc *cli.Context) (docd, error) {
	if cfg.DocdAddr != "" { // contradictory combinations are refused by upConfig.validate
		if cfg.DocdMountAddr == "" {
			// Not fatal — the store only needs the client face — but `verse connect` reads
			// the mount face off /status, so federation would fail later with no hint why.
			fmt.Fprintf(cc.Err, "verse up: no --docd-mount-addr; `verse connect` (which mounts a content controller) will not work against this verse\n")
		}
		return docd{clientAddr: cfg.DocdAddr, mountAddr: cfg.DocdMountAddr, stop: func() {}}, nil
	}

	dataDir := cfg.Data
	if dataDir == "" {
		d, err := os.MkdirTemp("", "verse-data-*")
		if err != nil {
			return docd{}, err
		}
		dataDir = d
	}
	fmt.Fprintf(cc.Out, "data dir: %s (kept)\n", dataDir)

	logdAddr, err := freeAddr()
	if err != nil {
		return docd{}, err
	}
	clientAddr, err := freeAddr()
	if err != nil {
		return docd{}, err
	}
	mountAddr, err := freeAddr()
	if err != nil {
		return docd{}, err
	}

	oargs := []string{
		"sys", "up",
		"--data", dataDir,
		"--logd-addr", logdAddr,
		"--docd-addr", clientAddr,
		"--docd-mount-addr", mountAddr,
	}
	var child *exec.Cmd
	if cfg.OBin != "" {
		child = exec.Command(cfg.OBin, oargs...)
	} else {
		child = exec.Command("go", append([]string{"run", goTonyO}, oargs...)...)
	}
	child.Stdout, child.Stderr = cc.Err, cc.Err
	// Own process group, so stop() can reap the WHOLE tree. `go run` execs the compiled
	// `o` as a child, so signalling just child.Process (the `go run`) would orphan the
	// actual `o sys up` (and its logd+docd) — the leak that piled up abandoned daemons.
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		return docd{}, fmt.Errorf("start `o sys up`: %w", err)
	}
	stop := func() {
		if child.Process == nil {
			return
		}
		pgid := child.Process.Pid // == the group id (Setpgid made child the leader)
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = child.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-pgid, syscall.SIGKILL) // force it out if SIGTERM stalls
			<-done
		}
	}
	// `go run` may compile o on first use, so allow generous startup time.
	if err := waitDial(clientAddr, 45*time.Second); err != nil {
		stop()
		return docd{}, fmt.Errorf("docd did not come up on %s: %w", clientAddr, err)
	}
	return docd{clientAddr: clientAddr, mountAddr: mountAddr, stop: stop}, nil
}

// freeAddr reserves and releases a loopback TCP address, so a daemon can bind it
// shortly after — conflict-free ports without scraping the daemon's output.
func freeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

// waitDial blocks until addr accepts a connection, or the timeout elapses.
func waitDial(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s not reachable after %s", addr, timeout)
}
