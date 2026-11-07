package stock

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
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
// runner reads the same step from the request and decides it again. The call blocks until the
// command ends, as `run_bash` does, and what the model reads back is the exit status and the
// output's tail, the output itself written to files in the workspace and served at unix sockets
// while the command runs.
//
// `secret-exec-kill` ends a run by its id: the task's ends of the run's output are closed, which
// the runner reads as the task letting go, and kills the command. It is no step, since it acts on
// nothing but a run the autonomy already decided, and it is reachable only beside the blocking
// call: by a script that made the call in the background, and not by an agent whose loop runs
// one tool at a time, whose way to bound a run is `timeout`.
var (
	secretExecTool     = systems.Tool("secret", "exec")
	secretExecKillTool = "secret-exec-kill"
)

// Where a run's output goes, under the workspace root, and how much of it: the file is cut at
// execFileMost and the answer carries the last execTailMost of each stream.
const (
	execDir      = ".verse/exec"
	execFileMost = 16 << 20
	execTailMost = 16 << 10
)

// secretTools are the secret system's tools, where the environment gives a `secret.exec`: the
// run and the kill, which share the runs under way.
func secretTools(cl *delivery.Client, ws *workspace.Workspace, env systems.Environment) []verse.Tool {
	if !env.Gives(task.StepSecretExec) {
		return nil
	}
	runs := &execRuns{kills: map[string]func(){}}
	return []verse.Tool{secretExecToolFor(cl, ws, env, runs), secretExecKillToolFor(runs)}
}

// execRuns are the runs under way, each with what kills it, by the id the task gave it.
type execRuns struct {
	n     atomic.Int64
	mu    sync.Mutex
	kills map[string]func()
}

func (r *execRuns) add(id string, kill func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kills[id] = kill
}

func (r *execRuns) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.kills, id)
}

// kill kills the run id, and reports whether there was one.
func (r *execRuns) kill(id string) bool {
	r.mu.Lock()
	k, ok := r.kills[id]
	r.mu.Unlock()
	if ok {
		k()
	}
	return ok
}

// execParams are `secret-exec`'s arguments: the secret, the command as a list, the plain
// variables, the directory and the timeout.
var execParams = map[string]any{
	"name":    map[string]any{"type": "string"},
	"argv":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	"env":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
	"dir":     map[string]any{"type": "string"},
	"timeout": map[string]any{"type": "integer"},
}

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
		e, why := delivery.ReadExec(env, str(in, "name"), argv, vars, str(in, "dir"))
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
		Description: "Run one command with the secret `name` in its environment, under the variable the environment's " +
			"`secret` block gives it in (`as`). You never read the secret: the runner runs the command in a sandbox of " +
			"its own over your workspace, and the secret's value is redacted from the output you read. `argv` is the " +
			"program and its arguments as a list of strings, never a shell line; the program is a bare name found on the " +
			"command's PATH, one the runner keeps, never a path. What that program then runs (a script of the workspace's, " +
			"a package's hooks) runs with the secret too: the environment's shapes and your autonomy's rows on `argv` " +
			"and `env` are what bound the command. `env` is plain " +
			"variables set beside the secret, by name, and is part of what your autonomy decides; `dir` is where the " +
			"command runs, a path under your workspace (empty is its root); `timeout` is seconds, " +
			fmt.Sprintf("%d by default and at most %d. ", int(delivery.ExecTimeoutDefault.Seconds()), int(delivery.ExecTimeoutMost.Seconds())) +
			"The call blocks until the command ends. Its stdout and stderr are written to `" + execDir + "/<id>.out` and " +
			"`<id>.err` in your workspace, and served while it runs at `<id>.sock` and `<id>.err.sock` beside them, unix " +
			"sockets a shell can read along from. You read back the run's id, the exit status, the files and the tail of each.",
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
			id := "e" + strconv.FormatInt(runs.n.Add(1), 10)
			dir := filepath.Join(ws.Root, filepath.FromSlash(execDir))
			var out, errs *execOutput
			var pumping sync.WaitGroup
			rep, err := cl.AskStreaming(delivery.Request{Op: delivery.OpSecretExec, Body: string(body), Fields: fields, Decision: tu.Decision},
				func(files []*os.File) {
					if len(files) != 2 {
						for _, f := range files {
							f.Close()
						}
						return
					}
					// Letting go of both ends is what kills the run: the kill tool's, and the
					// loop's when it gives the call up.
					var once sync.Once
					letGo := func() { once.Do(func() { files[0].Close(); files[1].Close() }) }
					runs.add(id, letGo)
					stop := context.AfterFunc(ctx, letGo)
					out = newExecOutput(dir, id+".out", id+".sock")
					errs = newExecOutput(dir, id+".err", id+".err.sock")
					pumping.Add(2)
					go func() { defer pumping.Done(); out.pump(files[0]) }()
					go func() { defer pumping.Done(); errs.pump(files[1]) }()
					// Once both streams have ended, the ends are closed here too: a run that
					// ended on its own leaves no descriptor behind.
					go func() { pumping.Wait(); stop(); runs.remove(id); letGo() }()
				})
			pumping.Wait()
			if err != nil || !rep.OK {
				if out != nil {
					out.Close()
					errs.Close()
				}
				return relay(rep, err)
			}
			if out == nil {
				return "", false, fmt.Errorf("the runner answered %q and streamed no output", rep.Out)
			}
			out.Close()
			errs.Close()
			return fmt.Sprintf("%s: %s\n\n%s\n\n%s", id, rep.Out, out.said("stdout"), errs.said("stderr")), false, nil
		},
	}
}

func secretExecKillToolFor(runs *execRuns) verse.Tool {
	params := map[string]any{"id": map[string]any{"type": "string"}}
	return verse.Tool{
		Name: secretExecKillTool,
		Description: "Kill a command `secret-exec` is running, by the id the run was given (`e1`): the run's output ends " +
			"and the command is killed, and the `secret-exec` call answers that the command was killed. Only a call made " +
			"beside the running one reaches it, since `secret-exec` blocks until the command ends: a script's " +
			"`verse-call-tool … &`. Nothing is killed once the run has ended; `timeout` bounds a run otherwise.",
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
// workspace, cut at execFileMost; served at a unix socket to whoever connects, from then on; and
// its tail kept for the answer. What could not be done (the directory, the file, the socket) is
// said in the answer rather than failing the run, whose output the runner streams regardless.
//
// A reader at the socket reads along, and no more: one that stops taking the output is dropped
// after execWriteGrace, so it can hold up neither the pump nor the run's end.
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

// accept takes each connection to the socket, which reads along from here on.
func (o *execOutput) accept() {
	for {
		c, err := o.ln.Accept()
		if err != nil {
			return
		}
		o.mu.Lock()
		if o.closed {
			// Accepted as the run ended: there is nothing more to read along, and nothing
			// after this would close it.
			c.Close()
		} else {
			o.conns = append(o.conns, c)
		}
		o.mu.Unlock()
	}
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
