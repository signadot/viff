package delivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/runner/runnerd/secretstore"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// THE SECRET SYSTEM IS ONE COMMAND RUN WITH ONE SECRET OF THE RUNNER'S STORE, which the task
// never holds. The runner reads the secret at the act, starts the command in a sandbox of its
// own over the task's root and under the task's network, and gives the task the command's
// output with the secret's value redacted, and its exit status. The environment says which
// secret and which commands (a `secret` block, with the variable the secret is given in, `as`);
// the autonomy decides each run; the ceiling bounds both; and the runner holds the run to each.
//
// The request: `name` is the secret's, `dir` where the command runs, a path under the task's
// root (empty is the root), `timeout` seconds, `stdin` whether the command's stdin is open to
// the task, and the body `{argv: [...], env: {...}}`, the command as a list and the plain
// variables it is given beside the secret. The step the autonomy and the ceiling see spells the
// list as one line (systems.Argv), and carries `as` from the environment's block, never from
// the call: what the secret is called is the block's to say.
//
// THE OUTPUT IS TWO STREAMS THE TASK'S PROCESS SERVES, and the runner is in the middle of them:
// stdout on the I/O pair, stderr on its own, each redacted here, the task's ends sent on the
// stream frame ahead of the answer. With `stdin`, the I/O pair carries the other direction too:
// what the task writes on its end is the command's stdin, unredacted — the input goes to the
// process that already holds the secret — and the task's half-close is the command's EOF.
// The command lives while the task holds its ends: the task letting go of both (its process
// dying, or closing them) kills the run, read as EOF on the stderr pair, which the task never
// writes. Past `timeout` it is killed too. A kill is the sandbox's pid namespace's, whole.
const OpSecretExec = "secret.exec"

// How long a command may run when the call does not say, and at most; and what the op has past
// the longest run, for the kill and the wait after it.
const (
	ExecTimeoutDefault = 10 * time.Minute
	ExecTimeoutMost    = time.Hour
	execGrace          = time.Minute
	// execWaitDelay bounds the wait on the output once the command has exited: a child it left
	// behind holding the pipes, which the namespace's end kills with it.
	execWaitDelay = 10 * time.Second
)

// execBody is a `secret.exec` request's body, read strictly.
type execBody struct {
	Argv []string          `tony:"field=argv"`
	Env  map[string]string `tony:"field=env,omitzero"`
}

// Exec is a `secret.exec` as read from a call (ReadExec): the secret's name and the variable the
// environment's block gives it in ("" where the environment has no block for it), the command as
// a list, the plain variables, the directory as a clean path under the root ("" for the root),
// and whether the command's stdin is open to the task over the run's I/O socket.
type Exec struct {
	Name, As string
	Argv     []string
	Env      map[string]string
	Dir      string
	Stdin    bool
}

// ReadExec reads a `secret.exec` from what a call gives, and says why it is refused before any
// row decides it, "" where it is not: the secret's name, the command a list naming a bare
// program first, each variable by a name, the directory under the root, and no variable where
// the secret goes. The runner reads it as the request arrives (secretExec), and the task's tool
// before it sends the call, so a row decides one step (Exec.Step) on both sides.
func ReadExec(env systems.Environment, name string, argv []string, vars map[string]string, dir string, stdin bool) (Exec, string) {
	if err := secretstore.CheckName(name); err != nil {
		return Exec{}, err.Error()
	}
	switch {
	case len(argv) == 0 || argv[0] == "":
		return Exec{}, "argv names the program first, then its arguments"
	case strings.Contains(argv[0], "/"):
		return Exec{}, fmt.Sprintf("the program is a bare name found on the command's PATH, never a path (%s)", argv[0])
	}
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		if !systems.IsVarName(k) {
			return Exec{}, fmt.Sprintf("env: %q is no variable's name: letters, digits and `_`, not starting with a digit", k)
		}
	}
	rel, err := underRoot(dir)
	if err != nil {
		return Exec{}, err.Error()
	}
	// The environment's block for the secret says what the secret is called. With none, the
	// step carries no `as`, and is refused as outside the environment, as every op's is.
	var as string
	for _, b := range env.Blocks("secret") {
		if b.Attributes["name"] == name {
			as = b.Attributes["as"]
			break
		}
	}
	if _, taken := vars[as]; as != "" && taken {
		return Exec{}, fmt.Sprintf("env names %s, which is the secret's", as)
	}
	return Exec{Name: name, As: as, Argv: argv, Env: vars, Dir: rel, Stdin: stdin}, ""
}

// Step is the step the run is, as the autonomy and the ceiling see it: the list spelled as one
// line (systems.Argv), `as` where the environment gives it, `stdin` always said, and the
// variables an object the form env matches closed. The step carries `stdin` even false, so a
// shape, read with the field's default where it says nothing, holds every call to what it means.
func (e Exec) Step() task.Step {
	fields := map[string]string{"name": e.Name, "argv": systems.Argv(e.Argv), "dir": e.Dir,
		"stdin": strconv.FormatBool(e.Stdin)}
	if e.As != "" {
		fields["as"] = e.As
	}
	return task.NewStep(task.StepSecretExec, fields).With("env", envNode(e.Env))
}

// secretExec answers a `secret.exec`: the request read into the one step, decided by the
// environment's block for the secret, the autonomy and the ceiling, and then the run.
func (s *Server) secretExec(ctx context.Context, tk Task, req Request) (asked, Reply) {
	var body execBody
	if err := codec.UnmarshalStrict([]byte(req.Body), &body, codec.Tony); err != nil {
		return before(Reply{Refused: "the body does not read as {argv: [...], env: {...}}: " + err.Error()})
	}
	e, why := ReadExec(tk.environment(), req.Field("name"), body.Argv, body.Env, req.Field("dir"), req.Flag("stdin"))
	if why != "" {
		return before(Reply{Refused: why})
	}
	timeout := time.Duration(req.Int("timeout")) * time.Second
	switch {
	case timeout == 0:
		timeout = ExecTimeoutDefault
	case timeout < 0 || timeout > ExecTimeoutMost:
		return before(Reply{Refused: fmt.Sprintf("timeout: %d seconds; a run is up to %s", req.Int("timeout"), ExecTimeoutMost)})
	}
	return asking(e.Step(), func() Reply {
		if e.As == "" {
			return s.bug(tk, "%s acts with no variable for the secret", req.Op)
		}
		return s.runSecret(ctx, tk, req.ID, e, timeout)
	})
}

// underRoot is a request's `dir` as a path under the root: relative, cleaned, and reaching no
// higher. Empty is the root.
func underRoot(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" || dir == "." {
		return "", nil
	}
	if filepath.IsAbs(dir) {
		return "", fmt.Errorf("dir: %s is absolute; a directory is a path under the workspace root", dir)
	}
	clean := filepath.Clean(dir)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("dir: %s reaches above the workspace root", dir)
	}
	return clean, nil
}

// envNode is a call's variables as the step carries them: an object of their names to their
// values, which the form env matches closed.
func envNode(env map[string]string) *ir.Node {
	var kvs []ir.KeyVal
	for _, k := range slices.Sorted(maps.Keys(env)) {
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(k), Val: ir.FromString(env[k])})
	}
	return ir.FromKeyVals(kvs)
}

// letGo is why a run was killed when the task let go of its output.
var letGo = errors.New("the task let go of the command's output")

// runSecret is the act: the secret read now, the program found on the command's PATH, the
// command confined and started, its output handed to the task and pumped through the redaction,
// and the exit status answered. The value is in this process's memory and the command's
// environment while it runs, and in no log, no reply and no file.
func (s *Server) runSecret(ctx context.Context, tk Task, id int, e Exec, timeout time.Duration) Reply {
	name, as, rel := e.Name, e.As, e.Dir
	value, err := s.Services.Secrets.Get(name)
	if err != nil {
		return Reply{Refused: "no secret to run with: " + err.Error()}
	}
	if s.Services.Confine == nil {
		return Reply{Error: "this runner runs no command for a task: it has nothing to confine one with"}
	}
	if tk.Root == "" {
		return Reply{Error: "this task has no workspace root to run a command over"}
	}
	env := mergeEnv(tk.Env, e.Env)
	prog, err := findProgram(e.Argv[0], env)
	if err != nil {
		return Reply{Refused: err.Error()}
	}
	// The run ends at the timeout, or when the task lets go (letGo), and the command is killed
	// either way: the sandbox's pid namespace dies with its init.
	run, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	timed, cancel := context.WithTimeout(run, timeout)
	defer cancel()
	cmd := exec.CommandContext(timed, prog, e.Argv[1:]...)
	cmd.Env = append(env, as+"="+value)
	cmd.WaitDelay = execWaitDelay
	if err := s.Services.Confine.Confine(cmd, tk.Root, filepath.Join(tk.Root, rel), tk.Network); err != nil {
		return Reply{Refused: "not run: " + err.Error()}
	}
	ioNear, ioFar, err := SocketPair()
	if err != nil {
		return Reply{Error: err.Error()}
	}
	defer ioNear.Close()
	errNear, errFar, err := SocketPair()
	if err != nil {
		ioFar.Close()
		return Reply{Error: err.Error()}
	}
	defer errNear.Close()
	out, errs := newRedactor(ioNear, name, value), newRedactor(errNear, name, value)
	cmd.Stdout, cmd.Stderr = out, errs
	// The command's stdin, where the call asks for it: the task's writes on the I/O pair, pumped
	// through a pipe of this side's own — never cmd.Stdin, whose copy Wait would wait on until
	// WaitDelay with the socket still open. The task's half-close ends the copy, which closes
	// the pipe, which is the command's EOF; a run the task feeds nothing waits on its stdin
	// until the timeout. Nothing is redacted on the way in: the input goes to the process that
	// already holds the secret.
	var feeding sync.WaitGroup
	if e.Stdin {
		pw, err := cmd.StdinPipe()
		if err != nil {
			ioFar.Close()
			errFar.Close()
			return Reply{Error: "the command's stdin: " + err.Error()}
		}
		feeding.Add(1)
		go func() {
			defer feeding.Done()
			_, _ = io.Copy(pw, ioNear)
			pw.Close()
		}()
	}
	// Started before the stream is handed over, so a command that does not start is answered
	// with why, and nothing is sent that the task would wait on.
	if err := cmd.Start(); err != nil {
		ioFar.Close()
		errFar.Close()
		return Reply{Error: "the command did not start: " + err.Error()}
	}
	err = tk.stream(id, ioFar, errFar)
	ioFar.Close()
	errFar.Close()
	if err != nil {
		stop(errors.New("the output could not be handed to the task"))
		_ = cmd.Wait()
		return Reply{Error: "the command's output could not be handed to the task, and it was killed: " + err.Error()}
	}
	// The task holds its ends now. EOF on the stderr pair, which the task never writes, is the
	// task letting go of both.
	var letting sync.WaitGroup
	letting.Add(1)
	go func() {
		defer letting.Done()
		_, _ = io.Copy(io.Discard, errNear)
		stop(letGo)
	}()
	waited := cmd.Wait()
	// Why the run ended, read before this side lets go of the pairs, which the reader above
	// takes for the task letting go.
	cause := context.Cause(timed)
	out.Close()
	errs.Close()
	ioNear.Close()
	errNear.Close()
	letting.Wait()
	feeding.Wait() // reaped by ioNear's close, and exec closed its pipe after the exit
	rep := Reply{OK: true}
	switch {
	case errors.Is(cause, letGo):
		rep.Out = "killed: " + letGo.Error()
	case errors.Is(cause, context.DeadlineExceeded):
		rep.Out = fmt.Sprintf("killed after %s, the timeout", timeout)
	case cause != nil && !errors.Is(cause, context.Canceled):
		rep.Out = "killed: " + cause.Error()
	default:
		var exit *exec.ExitError
		switch {
		case waited == nil:
			rep.Out = "exited 0"
		case errors.As(waited, &exit):
			rep.Out = fmt.Sprintf("exited %d", exit.ExitCode())
			if exit.ExitCode() < 0 {
				rep.Out = "killed by " + exit.ProcessState.String()
			}
		default:
			rep = Reply{Error: "the command: " + waited.Error()}
		}
	}
	if errors.Is(waited, exec.ErrWaitDelay) {
		rep.Out += fmt.Sprintf("; its output was left %s after it, held open by something it started", execWaitDelay)
	}
	fmt.Fprintf(s.Log, "delivery: %s: %s with the secret %s: %s\n", tk.ID, systems.Argv(e.Argv), name, rep.Out)
	return rep
}

// mergeEnv is base with each of over set, by name, the later value the one the command sees.
func mergeEnv(base []string, over map[string]string) []string {
	out := slices.Clone(base)
	for _, k := range slices.Sorted(maps.Keys(over)) {
		out = slices.DeleteFunc(out, func(kv string) bool { return strings.HasPrefix(kv, k+"=") })
		out = append(out, k+"="+over[k])
	}
	return out
}

// findProgram is where a bare program name resolves on the PATH of env: looked for here, on the
// runner's side, where the system directories and the readOnly paths are what the command sees
// them as. That it is the runner's own is Confine's to hold.
func findProgram(name string, env []string) (string, error) {
	var path string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	if path == "" {
		return "", errors.New("the command's environment has no PATH to find the program on")
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("no %s on the command's PATH (%s)", name, path)
}

// redactor writes through to w with every whole occurrence of the secret's value replaced by
// `[secret <name>]`. Bytes that may begin an occurrence are held back between writes, so a value
// split across two writes is caught; Close writes what is held. An encoding of the value is not
// caught, which the system's words say.
type redactor struct {
	w      io.Writer
	secret []byte
	repl   []byte
	held   []byte
}

func newRedactor(w io.Writer, name, value string) *redactor {
	return &redactor{w: w, secret: []byte(value), repl: []byte("[secret " + name + "]")}
}

func (r *redactor) Write(p []byte) (int, error) {
	buf := append(r.held, p...)
	buf = bytes.ReplaceAll(buf, r.secret, r.repl)
	// What may be the start of an occurrence, held: the longest tail of buf that is a proper
	// prefix of the secret.
	keep := 0
	for n := min(len(r.secret)-1, len(buf)); n > 0; n-- {
		if bytes.HasSuffix(buf, r.secret[:n]) {
			keep = n
			break
		}
	}
	r.held = append([]byte(nil), buf[len(buf)-keep:]...)
	if _, err := r.w.Write(buf[:len(buf)-keep]); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close writes what is held, which begins no occurrence after all.
func (r *redactor) Close() error {
	held := r.held
	r.held = nil
	if len(held) == 0 {
		return nil
	}
	_, err := r.w.Write(held)
	return err
}
