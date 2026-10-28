package stock

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/calltool"
	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// newScriptTask builds a script's task, rooted at root: the script runs once, by its #! line or
// with bash (scriptCommand), and its output is the task's result. env names the variables its
// shell is given from this process (task.ShellEnv), and set is set beside them: the launch's
// `with.env`, and where the task's credential file is.
//
// Nothing asks before it runs: the script is one action, and no step; verse decided to start it.
// What it may then do beyond its directory is call the task's tools from its shell, with
// `verse-call-tool` (bin, "" for none) over a socket this serves: tools, the ones its repos give
// it, and `question-ask`. Each call is an action of the task, the same step the agent's use of the tool
// is, so the same gates decide it; a refusal is an answer the script reads, not the end of its
// run. What the answerer of a call's question says goes back with the call's answer, approved or
// not; an operator's words are refused when said (Unsteerable), since a script has no turn for
// them.
//
// A call the script left running in the background may outlive it. One that reached the socket
// before the script's exit was noticed is proposed first: its question stands while the binary
// waits, and if it is approved after the script exited it is performed, since its caller is still
// waiting on it; then the exit is noticed and the run ends. One made after the exit was noticed
// is never proposed: it waits until the task ends and closes the socket, and reads a failure
// (exit 1). WaitDelay bounds how long a background child holds the script's output open.
//
// stop ends the socket, once the task has ended.
func newScriptTask(l runner.Launch, root string, env, set []string, tools []verse.Tool, bin string,
	reg *task.Registry, ceilings map[string]systems.Ceiling) (p *task.Task, stop func(), err error) {
	tools = offered(append(append([]verse.Tool{}, tools...), verse.Ask(reg)), l.Environment)
	s := &scriptTask{script: l.Script, root: root, env: env, set: set, bin: bin,
		tools: map[string]verse.Tool{}, calls: make(chan *toolCall)}
	for _, t := range tools {
		s.tools[t.Name] = t
	}
	dir, err := os.MkdirTemp("", "verse-tools-")
	if err != nil {
		return nil, nil, fmt.Errorf("a directory for the tools socket: %w", err)
	}
	if task.Inside(dir, root) {
		os.RemoveAll(dir)
		return nil, nil, fmt.Errorf("the temporary directory %s is inside the task's workspace %s, "+
			"and the tools socket is kept outside it", filepath.Dir(dir), root)
	}
	sock := filepath.Join(dir, "tools.sock")
	if len(sock) >= sunPathMax {
		os.RemoveAll(dir)
		return nil, nil, fmt.Errorf("the tools socket's path %s is too long for a unix socket (%d bytes at "+
			"most): the temporary directory, TMPDIR, needs a shorter path", sock, sunPathMax-1)
	}
	srv := &calltool.Server{Tools: described(tools), Call: s.call}
	if err := srv.Start(sock); err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	s.sock = srv.Path()
	sup := task.NewSupervisor(l.Environment, l.Autonomy, reg)
	sup.SetCeilings(ceilings)
	sup.SetStepOf(verse.StepOf(tools))
	return task.NewTask(l.ID, sup, s, s), func() { srv.Close(); os.RemoveAll(dir) }, nil
}

// scriptTask is a script's task, as its driver and its executor both: it proposes the script, then
// each call the script makes while it runs, and ends with its output, done if it exited 0 and
// failed with the reason otherwise.
type scriptTask struct {
	script, root string
	env, set     []string // the shell's: names passed through, and NAME=value set
	bin, sock    string   // verse-call-tool, "" for none, and the socket it calls

	tools map[string]verse.Tool
	// calls carries each call from the socket to Next; proposed is the one Next last put in
	// front of the loop, until the loop has decided about it and Next answers it.
	calls    chan *toolCall
	proposed *toolCall

	started bool
	run     *running // the script, once it has started
	result  string

	// heard is what the answerers of the calls' questions said, for the answer to the call they
	// are about. It relies on the loop handing a gate's words over (Hear) at the boundary after
	// the call's step and before the Next that answers it, so they are the words about that call.
	mu    sync.Mutex
	heard []string
}

// running is the script's process.
type running struct {
	out       bytes.Buffer
	done      chan struct{} // closed at its exit, when err and cancelled are set
	err       error
	cancelled bool
}

// toolCall is one call the script made, waiting to be an action and then for its answer.
type toolCall struct {
	ctx   context.Context // the caller's: it ends when the script's shell gives up on the call
	tool  string
	input map[string]any
	reply chan reply
}

type reply struct {
	out     string
	refused bool
	err     error
}

// sunPathMax is what a unix socket's path has to fit in, terminator included, on the stricter
// platform: 104 bytes on macOS (108 on Linux).
const sunPathMax = 104

// scriptWaitDelay is how long output is read after the script exits, from children it left.
const scriptWaitDelay = 2 * time.Second

// scriptTool is the action a script is. It is no step.
const scriptTool = "script"

func (s *scriptTask) Next(ctx context.Context, prev task.StepResult) (task.ToolUse, bool, error) {
	if !s.started {
		s.started = true
		return task.ToolUse{Name: scriptTool, Input: map[string]any{"script": s.script}}, true, nil
	}
	if c := s.proposed; c != nil {
		// The loop decided about the call, and ran it if it went ahead. A failure is the
		// script's to read, as a refusal is: it decides what to do next, and the run goes on.
		s.proposed = nil
		said := s.takeHeard()
		switch {
		case prev.Blocked:
			// A denial's note already carries what its answerer said.
			c.reply <- reply{out: cmp.Or(prev.Note, "refused"), refused: true}
		case prev.Err != nil:
			if said != "" {
				prev.Err = fmt.Errorf("%w\n\n%s", prev.Err, said)
			}
			c.reply <- reply{err: prev.Err}
		default:
			out := prev.Output
			if said != "" {
				out = strings.TrimRight(out, "\n") + "\n\n" + said
			}
			c.reply <- reply{out: out}
		}
	} else if s.run == nil {
		return task.ToolUse{}, false, fmt.Errorf("the script failed: %w", cmp.Or(prev.Err, errors.New("it never started")))
	}
	return s.await(ctx)
}

// await is the driver while the script runs: the next call it makes is the next action, and its
// exit ends the run. A call already waiting is taken before the exit is noticed, so the last one
// before it exits isn't lost to the order two channels happen to be ready in; one whose caller
// has gone is not proposed, so nobody is asked about an action nobody waits on.
func (s *scriptTask) await(ctx context.Context) (task.ToolUse, bool, error) {
	for {
		select {
		case c := <-s.calls:
			if c.ctx.Err() != nil {
				continue
			}
			return s.propose(c), true, nil
		default:
		}
		select {
		case c := <-s.calls:
			if c.ctx.Err() != nil {
				continue
			}
			return s.propose(c), true, nil
		case <-s.run.done:
			return s.ended()
		case <-ctx.Done():
			// A cancel or a timeout, which the loop reports: the script is killed with it.
			return task.ToolUse{}, false, nil
		}
	}
}

func (s *scriptTask) propose(c *toolCall) task.ToolUse {
	s.proposed = c
	return task.ToolUse{Name: c.tool, Input: c.input}
}

// ended is the script's exit: its output is the result. A cancel is not a failure: the kill that
// ends the script surfaces as an error too.
func (s *scriptTask) ended() (task.ToolUse, bool, error) {
	s.result = strings.TrimSpace(s.run.out.String())
	if s.run.err != nil && !s.run.cancelled {
		return task.ToolUse{}, false, fmt.Errorf("the script failed: %w", s.run.err)
	}
	return task.ToolUse{}, false, nil
}

// Hear keeps what a call's answerer said, for the call's answer. Nothing else reaches it: an
// operator's words are refused when said (Unsteerable), and a script has no children, no
// answer contract and no turns.
func (s *scriptTask) Hear(said []task.Said) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range said {
		if m.Kind == task.SaidByGate {
			s.heard = append(s.heard, m.Text)
		}
	}
}

func (s *scriptTask) takeHeard() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	said := strings.Join(s.heard, "\n")
	s.heard = nil
	return said
}

// Unsteerable says why an operator's words are refused: a script runs once, and has no turn for
// them.
func (s *scriptTask) Unsteerable() string {
	return "a script runs once, and has no turn for words: what it may be told is the answer to a call it makes"
}

// Result is the script's output.
func (s *scriptTask) Result() string { return s.result }

// Withdrawn is the call being asked about: its question is withdrawn when its caller goes.
func (s *scriptTask) Withdrawn(tu task.ToolUse) <-chan struct{} {
	if c := s.proposed; c != nil && c.tool == tu.Name {
		return c.ctx.Done()
	}
	return nil
}

// Execute starts the script, or performs a call the loop let go ahead: the tool's own Do, under
// a context that ends with the caller's too, so a `question-ask` nobody waits on any more is withdrawn.
func (s *scriptTask) Execute(ctx context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	if tu.Name == scriptTool {
		return s.start(ctx, tu), nil, nil
	}
	c := s.proposed
	if c == nil || c.tool != tu.Name {
		return task.StepResult{Use: tu}, nil, fmt.Errorf("no call of %s is waiting", tu.Name)
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(c.ctx, cancel)()
	out, blocked, err := s.tools[tu.Name].Do(cctx, tu)
	if err != nil {
		return task.StepResult{Use: tu}, nil, err
	}
	return task.StepResult{Use: tu, Output: out, Blocked: blocked, Note: out}, nil, nil
}

// start starts the script in root, with the variables env names passed through, set set, and its
// tools' socket and binary, and returns: the script runs beside the loop, which Next waits on.
// A script that can't start is the step's failure.
func (s *scriptTask) start(ctx context.Context, tu task.ToolUse) task.StepResult {
	c, done, err := scriptCommand(ctx, s.script)
	if err != nil {
		return task.StepResult{Use: tu, Err: err}
	}
	c.Dir = s.root
	// Its environment is the declared passthrough rather than the runner's, which carries the
	// runner's secrets.
	c.Env = withTools(append(task.ShellEnv(s.env), s.set...), toolsEnv(s.sock, s.bin))
	r := &running{done: make(chan struct{})}
	c.Stdout, c.Stderr = &r.out, &r.out
	// A child the script left running (`verse-call-tool question-ask ... &`) holds its output open, and
	// Wait would wait for it: the script's exit is its end, and the child's output is cut off
	// after this.
	c.WaitDelay = scriptWaitDelay
	if err := c.Start(); err != nil {
		done()
		return task.StepResult{Use: tu, Err: err}
	}
	s.run = r
	go func() {
		r.err = c.Wait()
		if errors.Is(r.err, exec.ErrWaitDelay) {
			r.err = nil // it exited 0, and only a child it left held its output
		}
		r.cancelled = ctx.Err() != nil
		done()
		close(r.done)
	}()
	return task.StepResult{Use: tu}
}

// call is the socket's side (calltool.Server.Call): a call from the script's shell, handed to
// Next and answered once the loop has decided and, if it went ahead, the tool has run. The
// caller's going ends the wait here, and withdraws the question if one stands (Withdrawn).
func (s *scriptTask) call(ctx context.Context, tool string, input map[string]any) (string, bool, error) {
	c := &toolCall{ctx: ctx, tool: tool, input: input, reply: make(chan reply, 1)}
	select {
	case s.calls <- c:
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
	select {
	case r := <-c.reply:
		return r.out, r.refused, r.err
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
}

// scriptCommand is how a script runs. A script that starts with a #! line is run by the program
// that line names, which can be anything that reads a file: sh, python3, node,
// `signadot sandbox apply -f`. The line is read as the kernel reads it: the program, and at most
// one argument, the rest of the line after a space or a tab (`#!/usr/bin/env -S` splits more).
// The script is written to a file the program is given, and done removes it. The file has no
// extension, so a program that needs one (`go run` wants .go) can't read it.
//
// A script with no #! line is run by bash -c (non-login, which avoids the login shell's
// job-control setpgid, "failed to change group ID", that fails in restricted environments).
//
// The file is read, never executed: executing a file this process has just written can fail
// with "text file busy" when another goroutine forks while it is open.
func scriptCommand(ctx context.Context, script string) (*exec.Cmd, func(), error) {
	line, ok := strings.CutPrefix(script, "#!")
	if !ok {
		return exec.CommandContext(ctx, "bash", "-c", script), func() {}, nil
	}
	line, _, _ = strings.Cut(line, "\n")
	line = strings.TrimSpace(line)
	prog, arg := line, ""
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		prog, arg = line[:i], line[i+1:]
	}
	if prog == "" {
		return nil, nil, errors.New("the script's #! line names no program")
	}
	f, err := os.CreateTemp("", "verse-script-*")
	if err != nil {
		return nil, nil, fmt.Errorf("the script could not be written: %w", err)
	}
	done := func() { os.Remove(f.Name()) }
	_, err = f.WriteString(script)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		done()
		return nil, nil, fmt.Errorf("the script could not be written: %w", err)
	}
	args := []string{f.Name()}
	if arg = strings.TrimSpace(arg); arg != "" {
		args = []string{arg, f.Name()}
	}
	return exec.CommandContext(ctx, prog, args...), done, nil
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
