package stock

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/runner/runnerd/delivery"
	"github.com/signadot/verse/pkg/runner/runnerd/verse"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// THE SECRET SYSTEM'S TOOL is `secret-exec`: one command run by the runner with one secret of
// its store, which the task never holds (delivery.OpSecretExec). The call is the step
// `secret.exec`, decided by the task's autonomy on the command as one line, the directory and
// the variables, with `as` from the environment's block for the secret (delivery.ReadExec); the
// runner reads the same step from the request and decides it again.
//
// THE CALL ANSWERS AT THE START, with the run's id and where its sockets and files are, and the
// command runs on beside the task: the output is served at unix sockets while it runs, and that
// is of use only to a task that is not waiting in the call. An agent's loop runs one tool at a
// time, and a script's calls go through the loop one at a time too, so a call that blocked until
// the end would leave nothing able to read along, give the input, or kill. A call that is
// refused, or whose command could not start, answers that instead.
//
// `secret-exec-wait` blocks until a run ends, by its id, and answers the end and each stream's
// file, size and tail. `secret-exec-kill` ends a run by its id: the task's ends of the run's
// output are closed, which the runner reads as the task letting go, and kills the command. The
// task's end lets go of every run the same way (secretTools' ended).
// Neither is a step, since each acts on nothing but a run the autonomy already decided. The end
// is written to a file beside the output as well (`<id>.exit`), so a shell reads it with no call.
var (
	secretExecTool     = systems.Tool("secret", "exec")
	secretExecWaitTool = "secret-exec-wait"
	secretExecKillTool = "secret-exec-kill"
)

// Where a run's output goes, under the workspace root, and how much of it: the file is cut at
// execFileMost and the answer carries the last execTailMost of each stream. The answers of the
// last execRunsKept runs that ended are kept for a wait that comes after.
const (
	execDir      = ".verse/exec"
	execFileMost = 16 << 20
	execTailMost = 16 << 10
	execRunsKept = 32
)

// secretTools are the secret system's tools, where the environment gives a `secret.exec`: the
// run, the wait and the kill, which share the runs. ended is for when the task has ended: it
// lets go of every run under way, and of any that starts after, so a command does not outlive
// the task that started it, whatever becomes of the task's process.
func secretTools(cl *delivery.Client, ws *workspace.Workspace, env systems.Environment) (tools []verse.Tool, ended func()) {
	if !env.Gives(task.StepSecretExec) {
		return nil, func() {}
	}
	runs := &execRuns{runs: map[string]*execRun{}}
	return []verse.Tool{secretExecToolFor(cl, ws, env, runs), secretExecWaitToolFor(runs), secretExecKillToolFor(runs)}, runs.letGo
}

// execRun is one run: what kills it, and once done is closed, what it came to, as a call that
// waited for it reads it.
type execRun struct {
	id   string
	kill func()
	done chan struct{}

	out     string
	blocked bool
	err     error
}

// execRuns are the runs by the id the task gave each: those under way, and the last
// execRunsKept that ended. over is the task ended, after which no run is left under way.
type execRuns struct {
	n     atomic.Int64
	mu    sync.Mutex
	runs  map[string]*execRun
	ended []string
	over  bool
}

func (r *execRuns) add(run *execRun) {
	r.mu.Lock()
	r.runs[run.id] = run
	over := r.over
	r.mu.Unlock()
	if over {
		run.kill()
	}
}

// letGo is the task ended: every run under way is killed, and each that starts after.
func (r *execRuns) letGo() {
	r.mu.Lock()
	r.over = true
	var under []*execRun
	for _, run := range r.runs {
		select {
		case <-run.done:
		default:
			under = append(under, run)
		}
	}
	r.mu.Unlock()
	for _, run := range under {
		run.kill()
	}
}

// end is the run over, with what it came to: whoever waits on it reads that, and it is kept for
// a wait that comes after, until execRunsKept later runs have ended.
func (r *execRuns) end(run *execRun, out string, blocked bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run.out, run.blocked, run.err = out, blocked, err
	close(run.done)
	if r.runs[run.id] != run {
		return // it ended before it was a run the task was told of
	}
	r.ended = append(r.ended, run.id)
	for len(r.ended) > execRunsKept {
		delete(r.runs, r.ended[0])
		r.ended = r.ended[1:]
	}
}

func (r *execRuns) get(id string) *execRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[id]
}

// kill kills the run id, and reports whether one was under way.
func (r *execRuns) kill(id string) bool {
	run := r.get(id)
	if run == nil {
		return false
	}
	select {
	case <-run.done:
		return false
	default:
	}
	run.kill()
	return true
}

// execParams are `secret-exec`'s arguments: the secret, the command as a list, the plain
// variables, the directory, the timeout, and whether the command's stdin is open to the task.
var execParams = map[string]any{
	"name":    map[string]any{"type": "string"},
	"argv":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	"env":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
	"dir":     map[string]any{"type": "string"},
	"timeout": map[string]any{"type": "integer"},
	"stdin":   map[string]any{"type": "boolean"},
}

// execStarted is how `secret-exec`'s answer opens for a run that started: the id, these words,
// then the directory its sockets and files are in, which is the rest of the line. A program
// reads the two from that line: cmd/verse-secret-exec, which spells the words itself.
const execStarted = ": started, in "

func secretExecToolFor(cl *delivery.Client, ws *workspace.Workspace, env systems.Environment, runs *execRuns) verse.Tool {
	read := func(in map[string]any) (delivery.Exec, int, string) {
		argv, err := stringList(in["argv"])
		if err != nil {
			return delivery.Exec{}, 0, "argv " + err.Error()
		}
		vars, err := stringMap(in["env"])
		if err != nil {
			return delivery.Exec{}, 0, "env " + err.Error()
		}
		timeout, err := secondsOf(in, "timeout")
		if err != nil {
			return delivery.Exec{}, 0, err.Error()
		}
		if timeout < 0 || timeout > int(delivery.ExecTimeoutMost.Seconds()) {
			return delivery.Exec{}, 0, fmt.Sprintf("timeout: %d seconds; a run is up to %s", timeout, delivery.ExecTimeoutMost)
		}
		stdin, err := boolOf(in, "stdin")
		if err != nil {
			return delivery.Exec{}, 0, err.Error()
		}
		e, why := delivery.ReadExec(env, str(in, "name"), argv, vars, str(in, "dir"), stdin)
		if why != "" {
			return delivery.Exec{}, 0, why
		}
		if e.As == "" {
			return delivery.Exec{}, 0, "the environment gives no `secret` block for " + e.Name
		}
		return e, timeout, ""
	}
	return verse.Tool{
		Name: secretExecTool,
		Description: "Start one command with the secret `name` in its environment, under the variable the environment's " +
			"`secret` block gives it in (`as`). You never read the secret: the runner runs the command in a sandbox of " +
			"its own over your workspace, and the secret's value is redacted from the output you read. `argv` is the " +
			"program and its arguments as a list of strings, never a shell line; the program is a bare name found on the " +
			"command's PATH, one the runner keeps — one of the image's, or one a `pkg-install` of yours put on your PATH — " +
			"never a path and never a program of your workspace's. What that program then runs (a script of the workspace's, " +
			"a package's hooks) runs with the secret too: the environment's shapes and your autonomy's rows on `argv` " +
			"and `env` are what bound the command. `env` is plain " +
			"variables set beside the secret, by name, and is part of what your autonomy decides; `dir` is where the " +
			"command runs, a path under your workspace (empty is its root); `timeout` is seconds, " +
			fmt.Sprintf("%d by default and at most %d. ", int(delivery.ExecTimeoutDefault.Seconds()), int(delivery.ExecTimeoutMost.Seconds())) +
			"The call answers once the command has started, with the run's id (`e1`), and the command runs on: " +
			"`" + secretExecWaitTool + "` with the id blocks until it ends and answers the exit status and the tail of each " +
			"stream, and `" + secretExecKillTool + "` with the id kills it. Its stdout and stderr are written to `" + execDir +
			"/<id>.out` and `<id>.err` in your workspace, and served while it runs at `<id>.sock` and `<id>.err.sock` beside " +
			"them, unix sockets a shell reads from the stream's start; its end is written to `<id>.exit` when it ends. " +
			"`stdin: true` — admitted only where the environment's shape for the secret says `stdin` — opens the command's " +
			"stdin to you at `<id>.in.sock` beside them: the first connection is the input, whole, written along to `<id>.in`, " +
			"and closing it is the command's EOF; nothing connects, and the command waits on its stdin until the timeout. " +
			"Nothing is redacted on the way in. Without `stdin: true` the command gets no input.",
		Params: execParams, Required: []string{"name", "argv"},
		Step: task.StepSecretExec,
		Build: func(in map[string]any) (task.Step, bool) {
			e, _, why := read(in)
			if why != "" {
				return task.Step{}, false
			}
			return e.Step(), true
		},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			if why, stray := untaken(secretExecTool, tu.Input, execParams); stray {
				return why, true, nil
			}
			e, timeout, why := read(tu.Input)
			if why != "" {
				return "not run: " + why, true, nil
			}
			body, err := codec.Marshal(struct {
				Argv []string          `tony:"field=argv"`
				Env  map[string]string `tony:"field=env,omitzero"`
			}{e.Argv, e.Env}, codec.Tony)
			if err != nil {
				return "", false, err
			}
			fields := map[string]string{"name": e.Name, "dir": e.Dir}
			if timeout != 0 {
				fields["timeout"] = strconv.Itoa(timeout)
			}
			if e.Stdin {
				fields["stdin"] = "true"
			}
			id := "e" + strconv.FormatInt(runs.n.Add(1), 10)
			dir := filepath.Join(ws.Root, filepath.FromSlash(execDir))
			run := &execRun{id: id, done: make(chan struct{})}
			req := delivery.Request{Op: delivery.OpSecretExec, Body: string(body), Fields: fields, Decision: tu.Decision}
			// The call's context bounds the run until it has started, and not after: a call given
			// up while the runner is still starting the command lets go of it as it starts, and
			// the call then answers its end.
			var (
				mu      sync.Mutex
				gaveUp  bool
				letGo   func()
				started = make(chan string, 1)
			)
			stop := context.AfterFunc(ctx, func() {
				mu.Lock()
				gaveUp = true
				l := letGo
				mu.Unlock()
				if l != nil {
					l()
				}
			})
			defer stop()
			go func() {
				var out, errs *execOutput
				var input *execInput
				var pumping sync.WaitGroup
				rep, err := cl.AskStreaming(req, func(files []*os.File) {
					if len(files) != 2 {
						for _, f := range files {
							f.Close()
						}
						return
					}
					// The I/O end is the command's stdout; with stdin it carries the other
					// direction too, so it becomes a connection whose write side the input's
					// close half-closes. One that cannot be is said, and the run goes on with
					// no input, bounded by its timeout.
					var ioEnd io.ReadCloser = files[0]
					if e.Stdin {
						if conn, cerr := net.FileConn(files[0]); cerr == nil {
							files[0].Close()
							uc := conn.(*net.UnixConn)
							input = newExecInput(dir, id+".in", id+".in.sock", uc)
							ioEnd = uc
						} else {
							input = &execInput{file: id + ".in", problems: []string{"not served: " + cerr.Error()}}
						}
					}
					// Letting go of both ends is what kills the run: the kill tool's, and the
					// call's where it is given up before the run has started.
					var once sync.Once
					run.kill = func() {
						once.Do(func() {
							ioEnd.Close()
							files[1].Close()
							if input != nil {
								input.Close()
							}
						})
					}
					// An end left by a run of this id before (a task's process started again
					// counts from e1) is not this run's.
					os.Remove(filepath.Join(dir, id+".exit"))
					out = newExecOutput(dir, id+".out", id+".sock")
					errs = newExecOutput(dir, id+".err", id+".err.sock")
					pumping.Add(2)
					go func() { defer pumping.Done(); out.pump(ioEnd) }()
					go func() { defer pumping.Done(); errs.pump(files[1]) }()
					// Once both streams have ended, the ends are closed here too: a run that
					// ended on its own leaves no descriptor behind.
					go func() { pumping.Wait(); run.kill() }()
					mu.Lock()
					letGo = run.kill
					gone := gaveUp
					mu.Unlock()
					if gone {
						run.kill()
						return
					}
					runs.add(run)
					started <- execStartedAnswer(id, dir, out, errs, input)
				})
				pumping.Wait()
				if input != nil {
					input.Close()
					input.serving.Wait()
				}
				if out == nil {
					// It never started: refused, failed, or an answer that streamed nothing.
					if err == nil && rep.OK {
						err = fmt.Errorf("the runner answered %q and streamed no output", rep.Out)
					}
					o, blocked, err := relay(rep, err)
					runs.end(run, o, blocked, err)
					return
				}
				// The end is in its file before the sockets close, so a reader that reads a
				// stream to its end finds the end there.
				end, blocked, err := relay(rep, err)
				if err != nil {
					end = "failed: " + err.Error()
				}
				answer := id + ": " + end
				if problem := writeExecEnd(dir, id+".exit", end); problem != "" {
					answer += "; " + problem
				}
				out.Close()
				errs.Close()
				if err != nil || blocked {
					runs.end(run, end, blocked, err)
					return
				}
				answer += "\n\n" + out.said("stdout") + "\n\n" + errs.said("stderr")
				if input != nil {
					answer += "\n\n" + input.said()
				}
				runs.end(run, answer, false, nil)
			}()
			select {
			case answer := <-started:
				return answer, false, nil
			case <-run.done:
				return run.out, run.blocked, run.err
			}
		},
	}
}

// execStartedAnswer is what `secret-exec` answers for a run that started: the id and the
// directory on the first line (execStarted), then where each stream is, and how the end is read.
func execStartedAnswer(id, dir string, out, errs *execOutput, input *execInput) string {
	b := id + execStarted + dir + "\n" + out.where("stdout") + "\n" + errs.where("stderr")
	if input != nil {
		b += "\n" + input.where()
	}
	return b + "\nits end: " + id + ".exit, written when the command ends; `" + secretExecWaitTool +
		"` with the id blocks until then and answers it with each stream's tail, and `" + secretExecKillTool + "` kills the run."
}

// writeExecEnd writes a run's end to its file, whole or not at all, and says what went wrong.
func writeExecEnd(dir, file, end string) string {
	tmp := filepath.Join(dir, file+".tmp")
	err := os.WriteFile(tmp, []byte(end+"\n"), 0o644)
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, file))
	}
	if err != nil {
		os.Remove(tmp)
		return "its end is not written: " + err.Error()
	}
	return ""
}

func secretExecWaitToolFor(runs *execRuns) verse.Tool {
	params := map[string]any{"id": map[string]any{"type": "string"}}
	return verse.Tool{
		Name: secretExecWaitTool,
		Description: "Wait for a command `secret-exec` started to end, by the id the run was given (`e1`), and read its " +
			"end (`exited 3`, `killed after 30s, the timeout`), then each stream's file, size and last " +
			fmt.Sprintf("%d KiB. ", execTailMost>>10) + "The call blocks until the command ends; a run that has ended answers " +
			"at once. The run's `timeout` bounds the wait.",
		Params: params, Required: []string{"id"},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			if why, stray := untaken(secretExecWaitTool, tu.Input, params); stray {
				return why, true, nil
			}
			id := strings.TrimSpace(str(tu.Input, "id"))
			run := runs.get(id)
			if run == nil {
				return "no run " + id + " is known here: a run's end is also in `" + execDir + "/<id>.exit`, and its output in the files beside it", true, nil
			}
			select {
			case <-run.done:
				return run.out, run.blocked, run.err
			case <-ctx.Done():
				return "", false, ctx.Err()
			}
		},
	}
}

func secretExecKillToolFor(runs *execRuns) verse.Tool {
	params := map[string]any{"id": map[string]any{"type": "string"}}
	return verse.Tool{
		Name: secretExecKillTool,
		Description: "Kill a command `secret-exec` started, by the id the run was given (`e1`): the run's output ends " +
			"and the command is killed, and `" + secretExecWaitTool + "` and the run's `.exit` file say that it was killed. " +
			"Nothing is killed once the run has ended.",
		Params: params, Required: []string{"id"},
		Do: func(ctx context.Context, tu task.ToolUse) (string, bool, error) {
			if why, stray := untaken(secretExecKillTool, tu.Input, params); stray {
				return why, true, nil
			}
			id := strings.TrimSpace(str(tu.Input, "id"))
			if !runs.kill(id) {
				return "not killed: no command is running as " + id, true, nil
			}
			return "killed " + id, false, nil
		},
	}
}

// stringList is a tool's list of strings, as the model gave it.
func stringList(v any) ([]string, error) {
	switch v := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return v, nil
	case []any:
		out := make([]string, len(v))
		for i, x := range v {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("is a list of strings, and item %d is %T", i+1, x)
			}
			out[i] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("is a list of strings, not %T", v)
}

// stringMap is a tool's object of strings, as the model gave it.
func stringMap(v any) (map[string]string, error) {
	switch v := v.(type) {
	case nil:
		return nil, nil
	case map[string]string:
		return v, nil
	case map[string]any:
		out := make(map[string]string, len(v))
		for k, x := range v {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("is an object of strings, and %s is %T", k, x)
			}
			out[k] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("is an object of strings, not %T", v)
}

// boolOf is the bool at k: true or false as JSON gives one, false for none, and an error for
// anything else, rather than a guess the model did not make.
func boolOf(in map[string]any, k string) (bool, error) {
	switch v := in[k].(type) {
	case nil:
		return false, nil
	case bool:
		return v, nil
	}
	return false, fmt.Errorf("%s: true or false, not %T", k, in[k])
}

// secondsOf is the whole number of seconds at k, however the model spelled it; 0 for none, and
// an error for what is no whole number, rather than a default the model did not ask for.
func secondsOf(in map[string]any, k string) (int, error) {
	switch v := in[k].(type) {
	case nil:
		return 0, nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("%s: %v is no whole number of seconds", k, v)
		}
		return int(v), nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, fmt.Errorf("%s: %q is no whole number of seconds", k, v)
		}
		return n, nil
	}
	return 0, fmt.Errorf("%s: a whole number of seconds, not %T", k, in[k])
}

// execOutput is one stream of a run as the task's process keeps it: written to a file in the
// workspace, cut at execFileMost; served at a unix socket to whoever connects, from the stream's
// start; and its tail kept for the answer. What could not be done (the directory, the file, the socket) is
// said in the answer rather than failing the run, whose output the runner streams regardless.
//
// A reader at the socket reads along, and no more: one that stops taking the output is dropped
// after execWriteGrace, so it can hold up neither the pump nor the run's end. What it reads is
// always the stream from its start, however far: a reader that connects late is sent the file's
// part first, and one that connects once the file is cut, or where there is no file to send,
// is closed, since the start is no longer to be had. So a reader holds a prefix of the stream,
// and the file the rest of it up to the cut.
type execOutput struct {
	dir, file, sock string
	f               *os.File
	ln              net.Listener
	mu              sync.Mutex
	conns           []net.Conn
	closed          bool
	written         int64
	tail            []byte
	problems        []string
}

// execWriteGrace is how long a reader at the socket may leave a write waiting.
const execWriteGrace = 10 * time.Second

func newExecOutput(dir, file, sock string) *execOutput {
	o := &execOutput{dir: dir, file: file, sock: sock}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		o.problems = append(o.problems, "not written: "+err.Error())
		return o
	}
	f, err := os.OpenFile(filepath.Join(dir, file), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		o.problems = append(o.problems, "not written: "+err.Error())
	} else {
		o.f = f
	}
	path := filepath.Join(dir, sock)
	if len(path) >= sunPathMax {
		o.problems = append(o.problems, fmt.Sprintf("not served: the socket's path %s is too long for a unix socket (%d bytes at most)", path, sunPathMax-1))
		return o
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		o.problems = append(o.problems, "not served: "+err.Error())
		return o
	}
	o.ln = ln
	go o.accept()
	return o
}

// accept takes each connection to the socket, which reads the stream so far and then along.
func (o *execOutput) accept() {
	for {
		c, err := o.ln.Accept()
		if err != nil {
			return
		}
		o.mu.Lock()
		if o.closed || !o.sendStart(c) {
			// Accepted as the run ended, where nothing after this would close it, or with
			// the stream's start not to be had.
			c.Close()
		} else {
			o.conns = append(o.conns, c)
		}
		o.mu.Unlock()
	}
}

// sendStart sends c the stream so far, from the file, and reports whether c then holds all of
// it. The pump waits meanwhile (o.mu), for execWriteGrace at most.
func (o *execOutput) sendStart(c net.Conn) bool {
	if o.written == 0 {
		return true
	}
	if o.f == nil || o.written > execFileMost {
		return false
	}
	f, err := os.Open(filepath.Join(o.dir, o.file))
	if err != nil {
		return false
	}
	defer f.Close()
	_ = c.SetWriteDeadline(time.Now().Add(execWriteGrace))
	n, err := io.CopyN(c, f, o.written)
	return err == nil && n == o.written
}

// where is the stream as the start's answer tells it: its socket and its file, by name.
func (o *execOutput) where(name string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	b := fmt.Sprintf("%s: %s while it runs, %s", name, o.sock, o.file)
	for _, p := range slices.Sorted(slices.Values(o.problems)) {
		b += "; " + p
	}
	return b
}

// Write is one piece of the stream: to the file while it is under the cut, to each connection
// that still takes it, and to the tail.
func (o *execOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.f != nil {
		room := execFileMost - o.written
		if room > int64(len(p)) {
			room = int64(len(p))
		}
		if room > 0 {
			if _, err := o.f.Write(p[:room]); err != nil {
				o.problems = append(o.problems, "the file: "+err.Error())
				o.f.Close()
				o.f = nil
			}
		}
	}
	o.written += int64(len(p))
	kept := o.conns[:0]
	for _, c := range o.conns {
		_ = c.SetWriteDeadline(time.Now().Add(execWriteGrace))
		if _, err := c.Write(p); err != nil {
			c.Close()
			continue
		}
		kept = append(kept, c)
	}
	o.conns = kept
	o.tail = append(o.tail, p...)
	if len(o.tail) > execTailMost {
		o.tail = append([]byte(nil), o.tail[len(o.tail)-execTailMost:]...)
	}
	return len(p), nil
}

// pump copies the run's end of the stream here until it ends.
func (o *execOutput) pump(r io.Reader) { _, _ = io.Copy(o, r) }

// Close ends the serving: the socket is gone, each connection sees the end, the file is whole.
func (o *execOutput) Close() {
	if o.ln != nil {
		o.ln.Close()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	for _, c := range o.conns {
		c.Close()
	}
	o.conns = nil
	if o.f != nil {
		o.f.Close()
		o.f = nil
	}
}

// said is the stream as the answer tells it: the file, its size, and the tail.
func (o *execOutput) said(name string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	path := filepath.ToSlash(filepath.Join(execDir, o.file))
	b := fmt.Sprintf("%s: %s, %d bytes", name, path, o.written)
	if o.written > execFileMost {
		b += fmt.Sprintf(", the file cut at %d", execFileMost)
	}
	sort.Strings(o.problems)
	for _, p := range o.problems {
		b += "; " + p
	}
	switch {
	case o.written == 0:
		return b + " (empty)"
	case o.written > int64(len(o.tail)):
		b += fmt.Sprintf(", the last %d:", len(o.tail))
	default:
		b += ":"
	}
	return b + "\n" + string(o.tail)
}

// execInput is a run's stdin as the task's process serves it: a unix socket beside the outputs',
// whose FIRST connection is the input, whole. Its bytes go to the run's I/O end, unredacted —
// the input goes to the process that already holds the secret — and along to a file in the
// workspace, cut as the outputs are; its close is the half-close the command reads as stdin's
// EOF. A later connection is closed at once. What could not be done (the file, the socket) is
// said in the answer rather than failing the run, which then gets no input and has the timeout
// as its bound.
type execInput struct {
	io      *net.UnixConn
	file    string
	ln      net.Listener
	serving sync.WaitGroup

	mu       sync.Mutex
	f        *os.File
	conn     net.Conn
	taken    bool
	closed   bool
	written  int64
	problems []string
}

func newExecInput(dir, file, sock string, io *net.UnixConn) *execInput {
	in := &execInput{io: io, file: file}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		in.problems = append(in.problems, "not written: "+err.Error())
		return in
	}
	f, err := os.OpenFile(filepath.Join(dir, file), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		in.problems = append(in.problems, "not written: "+err.Error())
	} else {
		in.f = f
	}
	path := filepath.Join(dir, sock)
	if len(path) >= sunPathMax {
		in.problems = append(in.problems, fmt.Sprintf("not served: the socket's path %s is too long for a unix socket (%d bytes at most)", path, sunPathMax-1))
		return in
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		in.problems = append(in.problems, "not served: "+err.Error())
		return in
	}
	in.ln = ln
	in.serving.Add(1)
	go in.serve()
	return in
}

// serve takes the first connection as the input and feeds the run until the connection closes,
// then half-closes the I/O end, which the command reads as stdin's EOF.
func (in *execInput) serve() {
	defer in.serving.Done()
	c, err := in.ln.Accept()
	if err != nil {
		return // closed with the run, nothing connected
	}
	go func() { // one input, whole: a second connection is closed at once
		for {
			c2, err := in.ln.Accept()
			if err != nil {
				return
			}
			c2.Close()
		}
	}()
	in.mu.Lock()
	if in.closed {
		in.mu.Unlock()
		c.Close()
		return
	}
	in.conn, in.taken = c, true
	in.mu.Unlock()
	buf := make([]byte, 32<<10)
	for {
		n, rerr := c.Read(buf)
		if n > 0 {
			if _, werr := in.io.Write(buf[:n]); werr != nil {
				break // the run is over, or letting go
			}
			in.fed(buf[:n])
		}
		if rerr != nil {
			break
		}
	}
	_ = in.io.CloseWrite()
	c.Close()
}

// fed is one piece of the input, counted, and mirrored to the file while it is under the cut.
func (in *execInput) fed(p []byte) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.f != nil {
		if room := min(int64(len(p)), execFileMost-in.written); room > 0 {
			if _, err := in.f.Write(p[:room]); err != nil {
				in.problems = append(in.problems, "the file: "+err.Error())
				in.f.Close()
				in.f = nil
			}
		}
	}
	in.written += int64(len(p))
}

// Close ends the serving: the socket is gone and the connection with it, the file whole. The
// I/O end is not its to close — that is letting go of the run, which is the caller's.
func (in *execInput) Close() {
	if in.ln != nil {
		in.ln.Close()
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.closed = true
	if in.conn != nil {
		in.conn.Close()
		in.conn = nil
	}
	if in.f != nil {
		in.f.Close()
		in.f = nil
	}
}

// where is the input as the start's answer tells it: its socket and its file, by name.
func (in *execInput) where() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	b := "stdin: "
	if in.ln != nil {
		b += strings.TrimSuffix(in.file, ".in") + ".in.sock, whose first connection is the input, written along to " + in.file
	} else {
		b += "none"
	}
	for _, p := range slices.Sorted(slices.Values(in.problems)) {
		b += "; " + p
	}
	return b
}

// said is the input as the answer tells it: the file and its size, or that nothing connected.
func (in *execInput) said() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	b := "stdin: "
	if !in.taken {
		b += "nothing connected at its socket"
	} else {
		b += fmt.Sprintf("%s, %d bytes", filepath.ToSlash(filepath.Join(execDir, in.file)), in.written)
		if in.written > execFileMost {
			b += fmt.Sprintf(", the file cut at %d", execFileMost)
		}
	}
	sort.Strings(in.problems)
	for _, p := range in.problems {
		b += "; " + p
	}
	return b
}
