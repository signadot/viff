package main

import (
	"bytes"
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

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/stock"
	"github.com/signadot/verse/versed"
)

// THE LOCAL RUNNER is a runnerd `verse up` starts beside itself, as it starts logd and docd: a
// runner like any other, registered as `local`, proved by a static token kept in the data dir,
// sandboxed as a deployed one is where bubblewrap runs. Nothing routes to it for being local; it
// takes what it offers, as every registered runner does (versed route.go).
//
// It is found by dialing, never by a pid: what answers the describe at the url the last run
// recorded, proving the token in the data dir, is this verse's runner, and is used as it
// stands, so its tasks outlive a crash of `verse up` as a deployed runner's outlive versed's.
// Nothing answers, or something else does: a runner is started. It is stopped by a request
// (runnerd.StopPath), which the runner takes from its verse alone, so a runner a crashed run
// left is stopped the way a child is.

// adoptedCheck is how often `verse up` asks a runner a crashed run left whether it still answers.
const adoptedCheck = 30 * time.Second

const (
	localRunnerConfig = "local-runner.tony"  // in the data dir, rewritten on every up
	localRunnerToken  = "local-runner.token" // in the data dir, made once and kept
	verseModule       = "github.com/signadot/verse"
)

// localRunner is the runner `verse up` keeps beside itself.
type localRunner struct {
	srv       *versed.Server
	bin       string // runnerd
	dir       string // where its config and token are kept
	sandbox   string // bwrap or none
	env       []string
	out       io.Writer
	subject   string // its token's hash: how verse recognises it
	tokenFile string

	mu      sync.Mutex
	url     string
	child   *exec.Cmd
	exited  chan struct{}
	adopted bool
}

// startLocalRunner registers the local runner and has one serving: the one the last run left, if
// it answers as this verse's, else a runnerd started now on the port the last run recorded, or a
// fresh one. It keeps the runner up until ctx ends, starting it again when it exits.
func startLocalRunner(ctx context.Context, srv *versed.Server, bin, dir string, verse runnerd.Verse, c stock.Config, secrets stock.Secrets, out io.Writer) (*localRunner, error) {
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
	// The one a crashed run left, proving this data dir's token to this verse.
	if recorded != "" {
		at := "http://" + recorded
		if err := srv.RegisterLocalRunner(at, subject); err != nil {
			return nil, err
		}
		ours, why := srv.LocalRunnerAt(at, subject)
		if ours {
			lr.url, lr.adopted = at, true
			fmt.Fprintf(out, "local runner: %s, left running by the last `verse up`, used as it stands\n", at)
			go lr.watch(ctx)
			return lr, nil
		}
		// Something answers there and is not this verse's: a runner the last run left serving
		// another --addr, which takes no stop from this one, or something else on the port.
		if !portFree(recorded) {
			fmt.Fprintf(out, "verse up: what answers at %s, where the last run's local runner listened, is not "+
				"this verse's (%v); it is left running, and a runner is started on another port\n", at, why)
		}
	}
	listen := recorded
	if listen == "" || !portFree(listen) {
		if listen, err = freeAddr(); err != nil {
			return nil, err
		}
	}
	if err := writeLocalRunnerConfig(filepath.Join(dir, localRunnerConfig), listen, verse, c); err != nil {
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
// reaches verse alone and verse decides when the runner stops.
func (lr *localRunner) spawn() error {
	cmd := exec.Command(lr.bin, "-config", filepath.Join(lr.dir, localRunnerConfig),
		"-token-file", lr.tokenFile, "-sandbox", lr.sandbox, "-admin-addr", "off")
	cmd.Env = lr.env
	cmd.Stdout, cmd.Stderr = prefixed(lr.out, "local runner: "), prefixed(lr.out, "local runner: ")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("verse up: starting the local runner (%s): %w", lr.bin, err)
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
// adoptedCheck whether it still answers as this verse's, and one is started in its place when not:
// the one poll in verse, and only of a runner a crashed run left.
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
			fmt.Fprintf(lr.out, "verse up: the local runner at %s stopped answering; starting one\n", lr.url)
		} else {
			select {
			case <-ctx.Done():
				return
			case <-exited:
			}
			fmt.Fprintf(lr.out, "verse up: the local runner exited; starting it again in %s\n", wait)
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
			fmt.Fprintf(lr.out, "verse up: %v\n", err)
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

// stop asks the runner to stop, as its verse, and waits for it; a child that does not stop is
// killed with its process group. The registration goes with it.
func (lr *localRunner) stop() {
	lr.mu.Lock()
	child, exited, at := lr.child, lr.exited, lr.url
	lr.mu.Unlock()
	if err := lr.srv.StopRunnerAt(at, lr.subject); err != nil {
		fmt.Fprintf(lr.out, "verse up: asking the local runner to stop: %v\n", err)
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
		// until it no longer answers as this verse's.
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if ours, _ := lr.srv.LocalRunnerAt(at, lr.subject); !ours {
				break
			}
		}
	}
	if err := lr.srv.RemoveLocalRunner(); err != nil {
		fmt.Fprintf(lr.out, "verse up: removing the local runner's registration: %v\n", err)
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
		return "", "", fmt.Errorf("verse up: reading the local runner's token: %w", err)
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
		return "", "", fmt.Errorf("verse up: writing the local runner's token: %w", err)
	}
	return path, token, nil
}

// writeLocalRunnerConfig writes the runner's configuration: where it listens, the verse it serves
// (where it reaches it, and the issuer its tokens are signed as where that is spelled otherwise),
// and what it offers (localRunnerOffers), rewritten on every up.
func writeLocalRunnerConfig(path, listen string, verse runnerd.Verse, c stock.Config) error {
	if verse.Issuer == verse.URL {
		verse.Issuer = "" // the default: said once
	}
	door, err := codec.Marshal(runnerd.Config{Listen: listen, Verse: &verse}, codec.Tony)
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
	body.WriteString("# Written by verse on every `verse up`, for the local runner it starts; edits here do not survive one.\n")
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
		return fmt.Errorf("verse up: %s names no address the local runner listens on", path)
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
		fmt.Fprintf(out, "verse up: the local runner runs its tasks unconfined, as you (no bubblewrap on %s)\n", runtime.GOOS)
		return "none"
	}
	if _, err := runnerd.Bwrap(); err != nil {
		fmt.Fprintf(out, "verse up: the local runner runs its tasks unconfined, as you: %v\n", err)
		return "none"
	}
	return "bwrap"
}

// localRunnerEnv is the runner's environment: this process's, its model credential as the config
// resolved it, and none of the variables that would name another verse than its config does.
func localRunnerEnv(s stock.Secrets) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "VERSE_URL", "VERSE_ISSUER", "VERSE_AUDIENCE", "VERSE_CA", "VERSE_TOKEN_FILE",
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
// beside this verse executable; verse's cache, ~/.verse/bin/<version>/runnerd; on PATH; and
// `go install` of this verse's version into the cache, with verse-call-tool beside it.
func resolveRunnerd(explicit string, out io.Writer) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if self, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(self); err == nil {
			self = real
		}
		if beside := filepath.Join(filepath.Dir(self), "runnerd"); isFile(beside) {
			return beside, nil
		}
	}
	version := verseVersion()
	cached := cachedRunnerd(version)
	if cached != "" && isFile(cached) {
		return cached, nil
	}
	if onPath, err := exec.LookPath("runnerd"); err == nil {
		if v := moduleVersionOf(onPath); version != "" && v != version {
			fmt.Fprintf(out, "verse up: using runnerd from PATH (%s, built from verse %s; this verse is %s)\n", onPath, cmpOr(v, "unknown"), version)
		}
		return onPath, nil
	}
	if cached == "" {
		return "", fmt.Errorf("verse up: no runnerd beside this verse or on PATH, and this is a development build with no version to install; " +
			"build them beside it (`make bin`, or `go build -o <dir> ./cmd/runnerd ./cmd/verse-call-tool`), or pass --runnerd-bin, or --local-runner=false")
	}
	if _, err := exec.LookPath("go"); err != nil {
		return "", fmt.Errorf("verse up: no runnerd beside this verse or on PATH, and no `go` to install one (%s/cmd/runnerd@%s); pass --runnerd-bin, or --local-runner=false", verseModule, version)
	}
	fmt.Fprintf(out, "verse up: installing runnerd and verse-call-tool %s into %s (once per version)…\n", version, filepath.Dir(cached))
	for _, cmd := range []string{"runnerd", "verse-call-tool"} {
		install := exec.Command("go", "install", verseModule+"/cmd/"+cmd+"@"+version)
		install.Env = append(os.Environ(), "GOBIN="+filepath.Dir(cached))
		install.Stdout, install.Stderr = out, out
		if err := install.Run(); err != nil {
			return "", fmt.Errorf("verse up: go install %s/cmd/%s@%s: %w; pass --runnerd-bin, or --local-runner=false", verseModule, cmd, version, err)
		}
	}
	if !isFile(cached) {
		return "", fmt.Errorf("verse up: installed runnerd %s but found none at %s; pass --runnerd-bin", version, cached)
	}
	return cached, nil
}

// verseVersion is this verse's module version, "" for a development build.
func verseVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Version == "" || bi.Main.Version == "(devel)" {
		return ""
	}
	return bi.Main.Version
}

// cachedRunnerd is where verse keeps a runnerd it installed: ~/.verse/bin/<version>/runnerd,
// beside the `o` it installs (cachedO).
func cachedRunnerd(version string) string {
	if version == "" {
		return ""
	}
	home, err := verseHome()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "bin", version, "runnerd")
}

// moduleVersionOf is the verse version a binary was built from.
func moduleVersionOf(bin string) string {
	bi, err := buildinfo.ReadFile(bin)
	if err != nil {
		return ""
	}
	return bi.Main.Version
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// verseHome is where verse keeps what it writes for itself: $VERSE_HOME, else ~/.verse.
func verseHome() (string, error) {
	if h := os.Getenv("VERSE_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".verse"), nil
}

// defaultDataDir is a verse's data dir where --data names none: ~/.verse/<addr>, kept, so a bare
// `verse up` comes back to the same verse, its store and its local runner with it.
func defaultDataDir(addr string) (string, error) {
	home, err := verseHome()
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
