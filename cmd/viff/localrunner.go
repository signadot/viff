package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"golang.org/x/mod/module"

	"go.viff.dev/viff/internal/viffd"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/runner/runnerd/stock"
)

// THE LOCAL RUNNER is a runnerd `viff up` starts beside itself, as it starts logd and docd: a
// runner like any other, registered as `local`, proved by a static token kept in the data dir,
// sandboxed as a deployed one is where bubblewrap runs. Nothing routes to it for being local; it
// takes what it offers, as every registered runner does (viffd route.go).
//
// It is found by dialing, never by a pid: what answers the describe at the url the last run
// recorded, proving the token in the data dir, is this viff's runner, and is used as it
// stands, so its tasks outlive a crash of `viff up` as a deployed runner's outlive viffd's.
// Nothing answers, or something else does: a runner is started. It is stopped by a request
// (runnerd.StopPath), which the runner takes from its viff alone, so a runner a crashed run
// left is stopped the way a child is.

// adoptedCheck is how often `viff up` asks a runner a crashed run left whether it still answers.
const adoptedCheck = 30 * time.Second

const (
	localRunnerConfig = "local-runner.tony"  // in the data dir, rewritten on every up
	localRunnerToken  = "local-runner.token" // in the data dir, made once and kept
	viffModule        = "go.viff.dev/viff"
)

// localRunner is the runner `viff up` keeps beside itself.
type localRunner struct {
	srv       *viffd.Server
	bin       string // runnerd
	dir       string // where its config and token are kept
	sandbox   string // bwrap or none
	env       []string
	out       io.Writer
	subject   string // its token's hash: how viff recognises it
	tokenFile string

	mu      sync.Mutex
	url     string
	child   *exec.Cmd
	exited  chan struct{}
	adopted bool
}

// startLocalRunner registers the local runner and has one serving: the one the last run left, if
// it answers as this viff's, else a runnerd started now on the port the last run recorded, or a
// fresh one. It keeps the runner up until ctx ends, starting it again when it exits.
func startLocalRunner(ctx context.Context, srv *viffd.Server, bin, dir string, viff runnerd.Viff, c stock.Config, secrets stock.Secrets, out io.Writer) (*localRunner, error) {
	tokenFile, token, err := localToken(dir)
	if err != nil {
		return nil, err
	}
	subject, err := runner.StaticSubject(token)
	if err != nil {
		return nil, err
	}
	lr := &localRunner{srv: srv, bin: bin, dir: dir, out: out, subject: subject, tokenFile: tokenFile,
		sandbox: localSandbox(out), env: localRunnerEnv(secrets)}
	recorded := recordedListen(filepath.Join(dir, localRunnerConfig))
	// The one a crashed run left, proving this data dir's token to this viff.
	if recorded != "" {
		at := "http://" + recorded
		if err := srv.RegisterLocalRunner(at, subject); err != nil {
			return nil, err
		}
		ours, why := srv.LocalRunnerAt(at, subject)
		if ours {
			lr.url, lr.adopted = at, true
			fmt.Fprintf(out, "local runner: %s, left running by the last `viff up`, used as it stands\n", at)
			go lr.watch(ctx)
			return lr, nil
		}
		// Something answers there and is not this viff's: a runner the last run left serving
		// another --addr, which takes no stop from this one, or something else on the port.
		if !portFree(recorded) {
			fmt.Fprintf(out, "viff up: what answers at %s, where the last run's local runner listened, is not "+
				"this viff's (%v); it is left running, and a runner is started on another port\n", at, why)
		}
	}
	listen := recorded
	if listen == "" || !portFree(listen) {
		if listen, err = freeAddr(); err != nil {
			return nil, err
		}
	}
	if err := writeLocalRunnerConfig(filepath.Join(dir, localRunnerConfig), listen, viff, c); err != nil {
		return nil, err
	}
	lr.url = "http://" + listen
	if err := srv.RegisterLocalRunner(lr.url, subject); err != nil {
		return nil, err
	}
	if err := lr.spawn(); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "local runner: %s (runnerd %s, sandbox %s)\n", lr.url, bin, lr.sandbox)
	go lr.watch(ctx)
	return lr, nil
}

// spawn starts runnerd on the config in the data dir, in its own process group, so a ctrl-c
// reaches viff alone and viff decides when the runner stops.
func (lr *localRunner) spawn() error {
	cmd := exec.Command(lr.bin, "-config", filepath.Join(lr.dir, localRunnerConfig),
		"-token-file", lr.tokenFile, "-sandbox", lr.sandbox, "-admin-addr", "off")
	cmd.Env = lr.env
	cmd.Stdout, cmd.Stderr = prefixed(lr.out, "local runner: "), prefixed(lr.out, "local runner: ")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("viff up: starting the local runner (%s): %w", lr.bin, err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	lr.mu.Lock()
	lr.child, lr.exited, lr.adopted = cmd, exited, false
	lr.mu.Unlock()
	return nil
}

// watch starts the runner again when it exits before ctx ends, waiting longer each time it does so
// quickly. A runner this run did not start has no process here to wait on, so it is asked every
// adoptedCheck whether it still answers as this viff's, and one is started in its place when not:
// the one poll in viff, and only of a runner a crashed run left.
func (lr *localRunner) watch(ctx context.Context) {
	wait := time.Second
	for {
		lr.mu.Lock()
		exited, adopted := lr.exited, lr.adopted
		lr.mu.Unlock()
		started := time.Now()
		if adopted {
			for ours := true; ours; {
				select {
				case <-ctx.Done():
					return
				case <-time.After(adoptedCheck):
				}
				ours, _ = lr.srv.LocalRunnerAt(lr.url, lr.subject)
			}
			fmt.Fprintf(lr.out, "viff up: the local runner at %s stopped answering; starting one\n", lr.url)
		} else {
			select {
			case <-ctx.Done():
				return
			case <-exited:
			}
			fmt.Fprintf(lr.out, "viff up: the local runner exited; starting it again in %s\n", wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		if time.Since(started) > time.Minute {
			wait = time.Second
		} else {
			wait = min(2*wait, 30*time.Second)
		}
		if err := lr.respawn(); err != nil {
			fmt.Fprintf(lr.out, "viff up: %v\n", err)
		}
	}
}

// respawn starts the runner again, on its port where it is free and on a fresh one where not, the
// registration following it.
func (lr *localRunner) respawn() error {
	listen := strings.TrimPrefix(lr.url, "http://")
	if !portFree(listen) {
		fresh, err := freeAddr()
		if err != nil {
			return err
		}
		path := filepath.Join(lr.dir, localRunnerConfig)
		if err := rewriteListen(path, fresh); err != nil {
			return err
		}
		lr.mu.Lock()
		lr.url = "http://" + fresh
		lr.mu.Unlock()
		if err := lr.srv.RegisterLocalRunner(lr.url, lr.subject); err != nil {
			return err
		}
	}
	return lr.spawn()
}

// stop asks the runner to stop, as its viff, and waits for it; a child that does not stop is
// killed with its process group. The registration goes with it.
func (lr *localRunner) stop() {
	lr.mu.Lock()
	child, exited, at := lr.child, lr.exited, lr.url
	lr.mu.Unlock()
	if err := lr.srv.StopRunnerAt(at, lr.subject); err != nil {
		fmt.Fprintf(lr.out, "viff up: asking the local runner to stop: %v\n", err)
	}
	if child != nil {
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
			<-exited
		}
	} else {
		// One this run did not start has no process here to wait on: it is waited on by dialing,
		// until it no longer answers as this viff's.
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if ours, _ := lr.srv.LocalRunnerAt(at, lr.subject); !ours {
				break
			}
		}
	}
	if err := lr.srv.RemoveLocalRunner(); err != nil {
		fmt.Fprintf(lr.out, "viff up: removing the local runner's registration: %v\n", err)
	}
}

// localToken is the local runner's static token, made on the first run on a data dir (32 random
// bytes as 64 hex digits, as `openssl rand -hex 32` makes one) and kept there, 0600.
func localToken(dir string) (path, token string, err error) {
	path = filepath.Join(dir, localRunnerToken)
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return path, t, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("viff up: reading the local runner's token: %w", err)
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(b[:])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", "", fmt.Errorf("viff up: writing the local runner's token: %w", err)
	}
	return path, token, nil
}

// writeLocalRunnerConfig writes the runner's configuration: where it listens, the viff it serves
// (where it reaches it, and the issuer its tokens are signed as where that is spelled otherwise),
// and what it offers (localRunnerOffers), rewritten on every up.
func writeLocalRunnerConfig(path, listen string, viff runnerd.Viff, c stock.Config) error {
	if viff.Issuer == viff.URL {
		viff.Issuer = "" // the default: said once
	}
	door, err := codec.Marshal(runnerd.Config{Listen: listen, Viff: &viff}, codec.Tony)
	if err != nil {
		return err
	}
	offers, err := codec.Marshal(c, codec.Tony)
	if err != nil {
		return err
	}
	a, err := codec.ParseNode(door, codec.Tony)
	if err != nil {
		return err
	}
	b, err := codec.ParseNode(offers, codec.Tony)
	if err != nil {
		return err
	}
	var kvs []ir.KeyVal
	for _, n := range []*ir.Node{a, b} {
		if n = ir.Uncomment(n); n != nil && n.Type == ir.ObjectType {
			for i := range n.Fields {
				kvs = append(kvs, ir.KeyVal{Key: n.Fields[i], Val: n.Values[i]})
			}
		}
	}
	merged := ir.FromKeyVals(kvs)
	var body bytes.Buffer
	body.WriteString("# Written by viff on every `viff up`, for the local runner it starts; edits here do not survive one.\n")
	if err := codec.EncodeNode(&body, merged, codec.Tony); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body.Bytes(), 0o644)
}

// recordedListen is the address the last run's local runner listened on, or "".
func recordedListen(path string) string {
	var c stock.Config
	rc, err := runnerd.Load(path, &c)
	if err != nil || rc.Listen == runnerd.DefaultListen {
		return ""
	}
	return rc.Listen
}

// rewriteListen moves the runner's configuration to another port.
func rewriteListen(path, listen string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	old := recordedListen(path)
	if old == "" {
		return fmt.Errorf("viff up: %s names no address the local runner listens on", path)
	}
	return os.WriteFile(path, bytes.Replace(b, []byte(old), []byte(listen), 1), 0o644)
}

// portFree is whether addr can be bound now.
func portFree(addr string) bool {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// localSandbox is what confines the local runner's tasks: bubblewrap where it runs here, as on a
// deployed runner, and nothing elsewhere (a Mac has none), said once.
func localSandbox(out io.Writer) string {
	if runtime.GOOS != "linux" {
		fmt.Fprintf(out, "viff up: the local runner runs its tasks unconfined, as you (no bubblewrap on %s)\n", runtime.GOOS)
		return "none"
	}
	if _, err := runnerd.Bwrap(); err != nil {
		fmt.Fprintf(out, "viff up: the local runner runs its tasks unconfined, as you: %v\n", err)
		return "none"
	}
	return "bwrap"
}

// localRunnerEnv is the runner's environment: this process's, its model credential as the config
// resolved it, and none of the variables that would name another viff than its config does.
func localRunnerEnv(s stock.Secrets) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "VIFF_URL", "VIFF_ISSUER", "VIFF_AUDIENCE", "VIFF_CA", "VIFF_TOKEN_FILE",
			"PORTKEY_API_KEY", "PORTKEY_BASE_URL":
			continue
		}
		env = append(env, kv)
	}
	if s.PortkeyAPIKey != "" {
		env = append(env, "PORTKEY_API_KEY="+s.PortkeyAPIKey)
	}
	if s.PortkeyBaseURL != "" {
		env = append(env, "PORTKEY_BASE_URL="+s.PortkeyBaseURL)
	}
	return env
}

// resolveRunnerd answers with the runnerd to start the local runner with, in this order: the flag;
// beside this viff executable; viff's cache, ~/.viff/bin/<version>/runnerd; on PATH; and
// `go install` of this viff's version into the cache, with viff-call-tool beside it.
//
// A runnerd found beside viff or on PATH is refused where it is another build than this viff
// (sameBuild); the flag's is used with a warning, since naming one is the way to run another.
func resolveRunnerd(explicit string, out io.Writer) (string, error) {
	if explicit != "" {
		if err := sameBuild(explicit); err != nil {
			fmt.Fprintf(out, "viff up: %v; using it, since --runnerd-bin names it\n", err)
		}
		return explicit, nil
	}
	if self, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(self); err == nil {
			self = real
		}
		if beside := filepath.Join(filepath.Dir(self), "runnerd"); isFile(beside) {
			if err := sameBuild(beside); err != nil {
				return "", fmt.Errorf("viff up: %w; %s", err, rebuildBoth)
			}
			return beside, nil
		}
	}
	version := viffVersion()
	release := releaseOf(version)
	cached := cachedRunnerd(release)
	// Not run through sameBuild: the cache is keyed by this viff's release, so what it holds
	// is this build by construction. It counts once both binaries are in it: an install cut
	// short between the two leaves a runnerd whose agents have no viff-call-tool, and is done
	// again.
	if cached != "" && cacheComplete(filepath.Dir(cached)) {
		return cached, nil
	}
	if onPath, err := exec.LookPath("runnerd"); err == nil {
		if err := sameBuild(onPath); err != nil {
			return "", fmt.Errorf("viff up: on PATH, %w; %s", err, rebuildBoth)
		}
		return onPath, nil
	}
	if cached == "" {
		return "", fmt.Errorf("viff up: no runnerd beside this viff or on PATH, and this viff (%s) is no release to install one of; %s",
			cmp.Or(version, "no version stamped"), buildBeside)
	}
	if _, err := exec.LookPath("go"); err != nil {
		return "", fmt.Errorf("viff up: no runnerd beside this viff or on PATH, and no `go` to install one (%s/cmd/runnerd@%s); pass --runnerd-bin, or --local-runner=false", viffModule, release)
	}
	fmt.Fprintf(out, "viff up: installing runnerd and viff-call-tool %s into %s (once per version)…\n", release, filepath.Dir(cached))
	for _, cmd := range cachedBinaries {
		install := exec.Command("go", "install", viffModule+"/cmd/"+cmd+"@"+release)
		install.Env = append(os.Environ(), "GOBIN="+filepath.Dir(cached))
		install.Stdout, install.Stderr = out, out
		if err := install.Run(); err != nil {
			return "", fmt.Errorf("viff up: go install %s/cmd/%s@%s: %w; pass --runnerd-bin, or --local-runner=false", viffModule, cmd, release, err)
		}
	}
	if !cacheComplete(filepath.Dir(cached)) {
		return "", fmt.Errorf("viff up: installed runnerd and viff-call-tool %s but found them not both in %s; pass --runnerd-bin", release, filepath.Dir(cached))
	}
	return cached, nil
}

// cachedBinaries are what an install puts in the cache, and what it holds once one is done.
var cachedBinaries = []string{"runnerd", "viff-call-tool"}

// cacheComplete is whether dir, a version's place in the cache, holds every binary an install
// puts there.
func cacheComplete(dir string) bool {
	for _, name := range cachedBinaries {
		if !isFile(filepath.Join(dir, name)) {
			return false
		}
	}
	return true
}

// buildBeside is what to do where there is no runnerd and none to install.
const buildBeside = "build them beside it (`make bin`, or `go build -o <dir> ./cmd/viff ./cmd/runnerd ./cmd/viff-call-tool`), " +
	"or pass --runnerd-bin, or --local-runner=false"

// releaseOf is version where it is a release, which `go install` can fetch by it, and "" where it
// is not: a pseudo-version, which Go stamps on a build from a checkout and names a commit the
// module proxy may never have seen, or a version with a build suffix such as +dirty, which names
// a tree nobody can fetch.
func releaseOf(version string) string {
	if version == "" || module.IsPseudoVersion(version) || strings.Contains(version, "+") {
		return ""
	}
	return version
}

// rebuildBoth is what to do about a runnerd of another build.
const rebuildBoth = "build both from one tree (`make bin`, or `go build -o <dir> ./cmd/viff ./cmd/runnerd ./cmd/viff-call-tool`), " +
	"or pass --runnerd-bin to run that one anyway, or --local-runner=false"

// sameBuild is why the runnerd at bin is another build than this viff, or nil. A build is its
// module version as Go stamps it: a release's tag, or, built from a checkout, a pseudo-version
// that carries the commit and +dirty where the tree had changes. Two builds of one tree agree;
// a viff rebuilt after a pull and a runnerd that was not do not, and the runner they make
// describes itself in a form this viff may refuse, as a failed registration a step away from
// its cause. A build either side does not stamp (go run, a test) is not checked.
func sameBuild(bin string) error { return compareBuilds(bin, viffVersion(), moduleVersionOf(bin)) }

// compareBuilds is sameBuild's answer, given the two builds.
func compareBuilds(bin, ours, theirs string) error {
	if ours == "" || theirs == "" || theirs == "(devel)" || ours == theirs {
		return nil
	}
	return fmt.Errorf("the runnerd at %s is built from viff %s, and this viff from %s", bin, theirs, ours)
}

// viffVersion is this viff's module version as Go stamps it: a release's tag, or, built from a
// checkout, a pseudo-version carrying the commit and +dirty where the tree had changes. "" where
// Go stamped none (go run, a test).
func viffVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Version == "(devel)" {
		return ""
	}
	return bi.Main.Version
}

// cachedRunnerd is where viff keeps a runnerd it installed: ~/.viff/bin/<version>/runnerd,
// beside the `o` it installs (cachedO). version is a release (releaseOf); "" has no place.
func cachedRunnerd(version string) string {
	if version == "" {
		return ""
	}
	home, err := viffHome()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "bin", version, "runnerd")
}

// moduleVersionOf is the viff version a binary was built from.
func moduleVersionOf(bin string) string {
	bi, err := buildinfo.ReadFile(bin)
	if err != nil {
		return ""
	}
	return bi.Main.Version
}

// viffHome is where viff keeps what it writes for itself: $VIFF_HOME, else ~/.viff.
func viffHome() (string, error) {
	if h := os.Getenv("VIFF_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".viff"), nil
}

// defaultDataDir is a viff's data dir where --data names none: ~/.viff/<addr>, kept, so a bare
// `viff up` comes back to the same viff, its store and its local runner with it.
func defaultDataDir(addr string) (string, error) {
	home, err := viffHome()
	if err != nil {
		return "", err
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if host == "" {
		host = "localhost"
	}
	return filepath.Join(home, url.PathEscape(host)+"_"+port), nil
}

// prefixed is w with each line of what is written to it prefixed.
func prefixed(w io.Writer, prefix string) io.Writer {
	return &prefixWriter{w: w, prefix: prefix, start: true}
}

type prefixWriter struct {
	mu     sync.Mutex
	w      io.Writer
	prefix string
	start  bool
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out bytes.Buffer
	for _, c := range b {
		if p.start {
			out.WriteString(p.prefix)
			p.start = false
		}
		out.WriteByte(c)
		if c == '\n' {
			p.start = true
		}
	}
	if _, err := p.w.Write(out.Bytes()); err != nil {
		return 0, err
	}
	return len(b), nil
}
