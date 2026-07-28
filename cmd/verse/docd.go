package main

import (
	"debug/buildinfo"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/scott-cotton/cli"
)

// goTonyModule is the pinned go-tony module and goTonyO its daemon runner — the `o`
// command verse brings logd+docd up with. Keeping the two in one place is what lets
// the resolution below talk about a VERSION rather than a build recipe.
const (
	goTonyModule = "github.com/signadot/tony-format/go-tony"
	goTonyO      = goTonyModule + "/cmd/o"
)

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

	// Resolve `o` BEFORE the data dir and the ports: with no o there is nothing to bring
	// up, and saying so first keeps the failure from arriving after a "data dir: …" line
	// that reads like progress.
	oBin, err := resolveO(cfg.OBin, cc)
	if err != nil {
		return docd{}, err
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

	child := exec.Command(oBin,
		"sys", "up",
		"--data", dataDir,
		"--logd-addr", logdAddr,
		"--docd-addr", clientAddr,
		"--docd-mount-addr", mountAddr,
	)
	child.Stdout, child.Stderr = cc.Err, cc.Err
	// Own process group, so stop() can reap the WHOLE tree: `o sys up` runs logd and docd
	// as children, so signalling only child.Process would orphan the daemons — the leak
	// that piled up abandoned processes.
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		return docd{}, fmt.Errorf("start `%s sys up`: %w", oBin, err)
	}
	// ONE Wait, whose completion both stop() and the readiness loop observe. The loop needs
	// it because `o` exiting early — a stale binary, an unreadable data dir, a port taken
	// between reservation and bind — is the answer to "is docd up yet", and only dialling
	// for it turned a one-line failure into a 45-second wait for a daemon that was already
	// gone.
	var waitErr error
	exited := make(chan struct{})
	go func() { waitErr = child.Wait(); close(exited) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			pgid := child.Process.Pid // == the group id (Setpgid made child the leader)
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				_ = syscall.Kill(-pgid, syscall.SIGKILL) // force it out if SIGTERM stalls
				<-exited
			}
		})
	}
	if err := waitDial(clientAddr, exited, 45*time.Second); err != nil {
		stop()
		if waitErr != nil {
			err = fmt.Errorf("%w (`%s sys up` exited: %v)", err, oBin, waitErr)
		}
		return docd{}, fmt.Errorf("docd did not come up on %s: %w", clientAddr, err)
	}
	return docd{clientAddr: clientAddr, mountAddr: mountAddr, stop: stop}, nil
}

// ── resolving o ──────────────────────────────────────────────────────────────

// resolveO answers with the `o` binary to spawn logd+docd with.
//
// This used to be `go run <pinned cmd/o>`, which works in exactly one place: a checkout of
// this module. Everywhere else — the directory an operator actually starts a verse in —
// `go run` cannot resolve a package it has no go.mod for, so `verse up` printed a module
// error from a tool nobody asked about and then waited out its startup timeout.
//
// So it is resolved rather than compiled, in this order:
//
//  1. --o-bin, which is an answer, not a hint.
//  2. verse's own cache — an `o` installed here earlier AT THE PINNED VERSION, so the
//     install is paid once per pin rather than once per up.
//  3. `o` on PATH, but only if it was built from the pinned go-tony.
//  4. `go install` the pinned cmd/o into the cache and use that.
//  5. failing all of it, a skewed `o` from PATH, with a warning — better a wrong version
//     than no store at all, and the warning names both versions.
//
// The version check at (3) is the part worth stating. verse and o share the logd session
// protocol, so a skewed `o` does not fail cleanly: the first sign of it is a store that
// will not talk, or — as on the machine this was written on, where `o` on PATH is a local
// go-tony build — `o sys up` refusing a flag verse passes it. Since an `o` binary carries
// the version it was built from, that is knowable BEFORE spawning it, and something
// knowable should be acted on rather than warned about. Hence: match the pin and it is
// used silently; miss it and the pinned one is fetched instead. Someone working on go-tony
// itself points --o-bin at their build, which is the flag saying "this one, I mean it".
func resolveO(explicit string, cc *cli.Context) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	pinned := pinnedGoTony()
	cached := cachedO(pinned)
	if cached != "" && isFile(cached) {
		return cached, nil
	}
	onPath, pathErr := exec.LookPath("o")
	if pathErr == nil && pinned != "" && goTonyOf(onPath) == pinned {
		return onPath, nil
	}
	// Nothing pinned to fetch — a build with no module info, or a replaced go-tony — so
	// there is nothing that is knowably the right thing to install.
	if cached == "" {
		if pathErr == nil {
			fmt.Fprintf(cc.Err, "verse up: using `o` from PATH (%s); verse cannot tell which go-tony it pins, so the versions are unchecked\n", onPath)
			return onPath, nil
		}
		return "", fmt.Errorf("verse up: no `o` on PATH, and verse cannot tell which go-tony it pins — pass --o-bin <path> (build one with `go build -o o %s`)", goTonyO)
	}
	if _, err := exec.LookPath("go"); err == nil {
		fmt.Fprintf(cc.Err, "verse up: installing %s@%s into %s (once per version)…\n", goTonyO, pinned, filepath.Dir(cached))
		install := exec.Command("go", "install", goTonyO+"@"+pinned)
		install.Env = append(os.Environ(), "GOBIN="+filepath.Dir(cached))
		install.Stdout, install.Stderr = cc.Err, cc.Err
		runErr := install.Run()
		switch {
		case runErr != nil:
			// An install that failed with no `o` anywhere is the end of the road; with one
			// on PATH there is still something to run, so say what happened and use it.
			if pathErr != nil {
				return "", fmt.Errorf("verse up: go install %s@%s: %w — or pass --o-bin <path>", goTonyO, pinned, runErr)
			}
			fmt.Fprintf(cc.Err, "verse up: go install %s@%s failed (%v); falling back to PATH\n", goTonyO, pinned, runErr)
		case !isFile(cached): // GOBIN honored but the binary is not where we expect it
			return "", fmt.Errorf("verse up: installed %s@%s but found no binary at %s — pass --o-bin <path>", goTonyO, pinned, cached)
		default:
			return cached, nil
		}
	} else if pathErr != nil {
		return "", fmt.Errorf("verse up: no `o` on PATH and no `go` to install one — put `o` (%s) on your PATH, or pass --o-bin <path>", goTonyO)
	}
	fmt.Fprintf(cc.Err, "verse up: %s\n", skewWarning(onPath, goTonyOf(onPath), pinned))
	return onPath, nil
}

// pinnedGoTony is the go-tony version THIS binary was built against, read from its own
// build info rather than parsed out of go.mod — the go.mod of a checkout is not present
// where an installed verse runs, and the build info is the pin that actually shipped.
func pinnedGoTony() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, d := range bi.Deps {
		if d.Path != goTonyModule {
			continue
		}
		if d.Replace != nil { // a replaced module has no version to install
			return ""
		}
		return d.Version
	}
	return ""
}

// cachedO is where verse keeps an `o` it installed: ~/.verse/bin/<version>/o. The version
// is a directory rather than part of the name so the binary keeps calling itself `o`, and
// so a verse built against a newer go-tony installs beside the old one instead of racing
// another running verse for the same path.
func cachedO(version string) string {
	if version == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".verse", "bin", version, "o")
}

// goTonyOf reports the go-tony version an `o` binary was built from, read straight out of
// the binary's own embedded build info — no `go` toolchain and no `o` subcommand required,
// which matters because the whole point is to check a binary that may be too old to have
// one. It is "" when the file carries no build info, and "(devel)" for a local build.
func goTonyOf(bin string) string {
	bi, err := buildinfo.ReadFile(bin)
	if err != nil {
		return ""
	}
	if bi.Main.Path == goTonyModule {
		return bi.Main.Version
	}
	for _, d := range bi.Deps {
		if d.Path == goTonyModule {
			return d.Version
		}
	}
	return ""
}

// skewWarning is what resolveO prints when it has to fall back to an `o` it could not
// match to the pin — no cache, no install. It is silent when the versions agree, so it can
// be called without asking first.
//
// The line names BOTH versions on purpose: the failure that follows a skew is a store that
// will not talk, or a flag `o sys up` does not know, and neither says "your o is old".
func skewWarning(bin, found, pinned string) string {
	switch {
	case pinned == "" || found == pinned:
		return ""
	case found == "":
		return fmt.Sprintf("using `o` from PATH (%s); it carries no version info, and verse pins go-tony %s — pass --o-bin if the store will not connect", bin, pinned)
	default:
		return fmt.Sprintf("using `o` from PATH (%s), built from go-tony %s; verse pins %s — pass --o-bin if the store will not connect", bin, found, pinned)
	}
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
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

// waitDial blocks until addr accepts a connection, the daemon it belongs to exits, or the
// timeout elapses.
func waitDial(addr string, exited <-chan struct{}, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		select {
		case <-exited:
			return fmt.Errorf("the daemon runner exited before %s accepted a connection", addr)
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s not reachable after %s", addr, timeout)
}
