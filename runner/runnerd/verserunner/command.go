package verserunner

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/signadot/verse/runner/runnerd/calltool"
	"github.com/signadot/verse/runner/runnerd/verserunner/agent"
	"github.com/signadot/verse/task"
)

// The command driver runs an external program in the workspace and takes what it says as the
// task's result. A coding agent binary is one such program; so is a script.
//
// One driver for both, because the distinction is not verse's. From verse's side, invoking a
// foreign agent and running a script are the SAME SHAPE: a subprocess, an exit status, some
// output. Two drivers would be two subprocess spawners — two places to apply containment, build
// an environment, choose a working directory — and the one tool that can leave a workspace, the
// shell, is covered only if it is the same path. One path is one place to get that right.
//
// What is NOT folded away is the ability to RE-INVOKE. A driver that runs once and completes
// cannot honour a steer (it has nowhere to put it) and cannot repair an answer that missed its
// contract (it cannot ask again). Both are the same capability, and it is generic rather than
// anything to do with agents — so it lives here, and a program that can resume a session gets
// steering and contract repair for free.
//
// What verse gates is the INVOCATION, and then each CALL the program makes. The program is
// opaque — nothing can gate inside a subprocess — so the invocation is one action, asked about
// by default. What the program may say back to verse is the task's tools, called from its shell
// as `verse-call-tool` (cmd/verse-call-tool) over a socket this driver serves (calltool.Server):
// each call arrives while the program runs and is put in front of the loop as an action of its
// own, derived by the tool's own Derive, gated by the vector, narrated on the read plane, and
// answered to the program with what happened. A gate's refusal is an answer the program reads,
// exit 3, not the end of its run; a call's failure is one too, exit 1, and the record says the
// action failed. A question about a call whose caller has gone — the shell gave up waiting — is
// withdrawn, so the run is not held for an answer nobody reads. The read plane gets what the
// program reports about itself, which is a self-report and labelled as one, and every call,
// which is not.
//
// Words said to the task while a program runs go two ways by who said them: what a gate's
// answerer said about a call goes to the program with that call's answer, since the call is
// what the words are about and the program is in the middle of its turn; what an operator said
// is the program's next turn, which a program with a `resume:` has and one without is refused
// (Unsteerable).

// Command is how to invoke one program, from the runner's config. `claude` is an entry in that
// config rather than a type in this file, which is the whole point: nothing here knows what a
// coding agent is.
type Command struct {
	// Argv is the program and its arguments. `$prompt` is replaced with the text this turn is
	// asking for. Required.
	Argv []string `tony:"field=argv"`
	// Resume is appended instead of nothing on turns after the first, with `$session`
	// replaced by whatever the last turn reported. A program with no notion of a session
	// leaves this empty and gets one turn — which is a correct program, not a broken one, and
	// simply cannot be steered or asked to repair an answer.
	Resume []string `tony:"field=resume,omitzero"`
	// Output says how to read what the program said: `text` (stdout IS the result) or `json`
	// (stdout is one object carrying `result`, `session_id` and `is_error`). Empty = text.
	//
	// Two values rather than a path language on purpose. A field path would be a small
	// query dialect nobody validates, for a shape that is either "the program printed the
	// answer" or "the program printed a wrapper around it".
	Output string `tony:"field=output,omitzero"`
}

func (c Command) argv(prompt, session string) []string {
	out := make([]string, 0, len(c.Argv)+len(c.Resume))
	for _, a := range c.Argv {
		out = append(out, strings.ReplaceAll(a, "$prompt", prompt))
	}
	if session != "" {
		for _, a := range c.Resume {
			out = append(out, strings.ReplaceAll(a, "$session", session))
		}
	}
	return out
}

// commandResult is the slice of a json-output program's document this depends on. Anything else
// it reports is ignored rather than decoded, so a new field upstream is not a breaking change.
type commandResult struct {
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
	Subtype   string `json:"subtype"`
}

// runTool is the invocation's action: the one that hands the work to the program.
const runTool = "run"

type commandDriver struct {
	name   string // the driver name a spawn asked for, for messages
	cmd    Command
	prompt string
	frame  string
	env    Environment
	dir    string

	// contractSrc is the shape the answer must have, for the OPENING turn only — the check
	// itself is the loop's (task.AnswerCheck). Stated up front for the reason agent.Build
	// states it: meeting the check for the first time as a rejection wastes a repair attempt
	// learning what it could have been told at the start.
	contractSrc string

	session string // what the program called this conversation, for Resume
	last    string // its last final message: this task's result
	started bool

	mu      sync.Mutex
	folding []task.Said // what the loop handed over, with who said it (Hear)

	emit func(task.Event)

	// The program's calls. tools is the socket the program calls, nil when nothing is offered;
	// set is what the program's environment says about it (calltool.Env, and PATH with the
	// binary's directory). calls carries each call from the socket's goroutine to Next;
	// proposed is the call Next last put in front of the loop, until Execute answers it or Next
	// learns it was refused; running is the program in flight, nil between turns.
	tools    *calltool.Server
	set      []string
	calls    chan *toolCall
	proposed *toolCall
	// answering is the call Execute performed and has not answered: the answer waits for the
	// boundary, where what a decider said while approving the call has been folded in.
	answering *toolCall
	running   *program
}

// toolCall is one call the program made, waiting to be an action and then for its answer.
type toolCall struct {
	ctx   context.Context // the caller's: ends when the program's shell gives up on it
	tool  agent.Tool
	input map[string]any
	reply chan reply
	// pending is the answer Execute arrived at, until Next sends it.
	pending reply
}

type reply struct {
	out     string
	refused bool
	err     error
}

func (c *toolCall) answer(r reply) {
	select {
	case c.reply <- r:
	default: // answered once already
	}
}

// program is one turn's process, running beside the loop.
type program struct {
	out, errb bytes.Buffer
	done      chan struct{}
	err       error // Wait's, once done is closed
}

func (d *commandDriver) Next(ctx context.Context, prev task.StepResult) (task.ToolUse, bool, error) {
	if a := d.answering; a != nil {
		// The call was performed. What a decider said while approving it arrives folded at
		// this boundary, as words for the next turn; the program is in this one, and the call
		// is what the words are about, so they go with its answer — with the failure, when the
		// call failed, since the words may be why. What anyone else said stays for the next
		// turn. Under a pause the answer waits with the run, which is what a pause holds; a
		// cancel kills the program with the task.
		d.answering = nil
		r := a.pending
		if said := d.drain(byGate); said != "" {
			if r.err != nil {
				r.err = fmt.Errorf("%w\n\n%s", r.err, said)
			} else {
				r.out = strings.TrimRight(r.out, "\n") + "\n\n" + said
			}
		}
		a.answer(r)
		// A call's failure is the program's to read, and it has just been given it. It is not
		// the run's: the program decides what to do next.
		prev.Err = nil
	}
	if p := d.proposed; p != nil {
		// The loop decided about the last call without executing it.
		d.proposed = nil
		switch {
		case prev.Err != nil:
			p.answer(reply{err: prev.Err})
			return task.ToolUse{}, false, prev.Err
		case prev.Blocked:
			// The gate declined, or the question was withdrawn. An answer the program reads
			// and plans around, as the model does: the run goes on. The note carries what the
			// decider said; the same words arrive folded for the next turn too, and the
			// program is in the middle of this one, so they are taken here and not kept for a
			// turn a program may not have. An operator's words, which withdraw the question,
			// are not about the call: they stay, and are the next turn.
			d.drain(byGate)
			p.answer(reply{out: "refused: " + cmp.Or(prev.Note, "by a gate"), refused: true})
		}
	} else if prev.Err != nil && d.started {
		return task.ToolUse{}, false, prev.Err
	}
	if !d.started {
		d.started = true
		return d.turn(d.opening()), true, nil
	}
	if d.running == nil {
		if prev.Blocked && prev.Use.Name == runTool {
			// The gate refused the invocation. There is no other way to do this work — the
			// program IS the doer — so the honest end is to stop and say so rather than loop.
			return task.ToolUse{}, false, fmt.Errorf("the invocation was refused: %s", prev.Note)
		}
		// Between turns. A steer that arrived during the turn is the next turn; a contract
		// repair is one too — it arrives here the same way, because the loop hands both in
		// through Fold — and the precedence between them is the loop's (machine.go: an
		// instruction from outside is new authorization and outranks a shape complaint the
		// program has not seen yet).
		if s := d.drain(nil); s != "" {
			return d.again(s, "there is more to say to the program")
		}
		return task.ToolUse{}, false, nil
	}
	return d.await(ctx)
}

// again is another turn, for a program that can have one. A program with no Resume cannot, and
// saying so is better than looping on a first turn forever or dropping the request in silence.
func (d *commandDriver) again(text, why string) (task.ToolUse, bool, error) {
	if len(d.cmd.Resume) == 0 {
		return task.ToolUse{}, false, fmt.Errorf(
			"%s: %s, but this command declares no way to resume a session", d.name, why)
	}
	return d.turn(text), true, nil
}

// await is the driver while the program runs: the next call it makes is the next action, and
// its exit ends the turn.
func (d *commandDriver) await(ctx context.Context) (task.ToolUse, bool, error) {
	// A call already waiting is taken before the exit is noticed, so a program's last call
	// before it exits is not lost to the order two channels happened to be ready in. A call
	// whose caller has gone — the shell was interrupted, or gave up waiting — is not proposed:
	// nobody is asked to decide about an action nobody is waiting on.
	for {
		select {
		case c := <-d.calls:
			if c.ctx.Err() != nil {
				continue
			}
			return d.propose(c), true, nil
		default:
		}
		select {
		case c := <-d.calls:
			if c.ctx.Err() != nil {
				continue
			}
			return d.propose(c), true, nil
		case <-d.running.done:
			return d.ended()
		case <-ctx.Done():
			return task.ToolUse{}, false, ctx.Err()
		}
	}
}

// propose is a call as the loop will gate it: the tool's name, the input the program sent, and
// the effects the tool itself derives from it — the same classification the verse agent's use
// of the tool gets, so the same gate matches.
func (d *commandDriver) propose(c *toolCall) task.ToolUse {
	d.proposed = c
	tu := task.ToolUse{Name: c.tool.Name, Input: c.input}
	if tu.Input == nil {
		tu.Input = map[string]any{}
	}
	if c.tool.Derive != nil {
		c.tool.Derive(&tu)
	}
	return tu
}

// ended reads what the program said and ends the turn: the next turn when there is more to
// say, else nothing more.
func (d *commandDriver) ended() (task.ToolUse, bool, error) {
	p := d.running
	d.running = nil
	res, err := d.read(p.out.Bytes(), p.errb.String(), p.err)
	if err != nil {
		return task.ToolUse{}, false, err
	}
	if res.SessionID != "" {
		d.session = res.SessionID // every later turn resumes this
	}
	if res.Result != "" {
		d.last = res.Result
	}
	if res.IsError {
		return task.ToolUse{}, false, fmt.Errorf("%s failed (%s): %s", d.name, res.Subtype, firstLine(res.Result))
	}
	if d.emit != nil {
		d.emit(task.Event{Kind: task.EventSay, Text: res.Result})
	}
	if s := d.drain(nil); s != "" {
		return d.again(s, "there is more to say to the program")
	}
	return task.ToolUse{}, false, nil
}

// opening is the first turn: the frame (standing instructions) and the prompt (this job),
// concatenated because a program takes one text. That is a real difference from the harness,
// where a frame is a system message the model cannot confuse with the task.
func (d *commandDriver) opening() string {
	var b strings.Builder
	if d.frame != "" {
		b.WriteString(d.frame)
		b.WriteString("\n\n")
	}
	if d.contractSrc != "" {
		fmt.Fprintf(&b, "ANSWER CONTRACT. Your final message must satisfy:\n%s\n\n", d.contractSrc)
	}
	b.WriteString(d.prompt)
	return b.String()
}

func (d *commandDriver) turn(text string) task.ToolUse {
	return task.ToolUse{
		Name:  runTool,
		Input: map[string]any{"text": text, "command": d.name},
		// Derived here rather than left to a deriver's guess: this action hands the work to
		// another program, which is the verb the vector already knows how to gate as
		// delegation.
		//
		// `CallAgent` is deliberately nil. That field is not a label — it is the instruction
		// that makes the loop route an action to spawnChild and build a sub-task through a
		// SpawnFunc this runner does not have. With it set, every task failed with "no spawn
		// factory configured" the moment its gate was approved.
		Effects: []task.Effect{{Verb: task.CallAgent, Resource: task.AgentRes(d.name)}},
	}
}

// call is the socket's side (calltool.Server.Call): a call from the program's shell, handed to
// Next and answered once the loop has decided and, if approved, the tool has run. The caller's
// context ending — the shell gave up — ends the wait here; a call already proposed is then
// not performed (Execute).
func (d *commandDriver) call(ctx context.Context, t agent.Tool, input map[string]any) (string, bool, error) {
	c := &toolCall{ctx: ctx, tool: t, input: input, reply: make(chan reply, 1)}
	select {
	case d.calls <- c:
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

// close ends what the driver holds beside the loop: the socket. The program, started under the
// loop's context, ends with it.
func (d *commandDriver) close() {
	if d.tools != nil {
		d.tools.Close()
	}
}

// Withdrawn implements task.Withdrawer: the question about a call is withdrawn when its caller
// goes — the shell was interrupted, or gave up waiting — rather than standing on the docket for
// an answer nobody will read while the program, which may have exited, is held for it.
func (d *commandDriver) Withdrawn(tu task.ToolUse) <-chan struct{} {
	if c := d.proposed; c != nil && c.tool.Name == tu.Name {
		return c.ctx.Done()
	}
	return nil
}

// Hear implements task.Hearer: the loop hands over what was said at the boundary before Next,
// so it lands cooperatively — never interrupting a turn in flight — and with who said it, since
// a gate's words go with the call they are about and an operator's are the next turn (Next).
func (d *commandDriver) Hear(said []task.Said) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.folding = append(d.folding, said...)
}

// byGate is what a gate's answerer said about the decision.
func byGate(s task.Said) bool { return s.Kind == task.SaidByGate }

// drain takes what was said by the speakers only admits, joined, and leaves the rest; nil takes
// everything.
func (d *commandDriver) drain(only func(task.Said) bool) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var taken []string
	kept := d.folding[:0]
	for _, s := range d.folding {
		if only == nil || only(s) {
			taken = append(taken, s.Text)
		} else {
			kept = append(kept, s)
		}
	}
	d.folding = kept
	if len(d.folding) == 0 {
		d.folding = nil
	}
	return strings.Join(taken, "\n")
}

// Unsteerable implements task.Unsteerable: a program with no Resume runs once, and words for it
// have nowhere to go. Said to whoever is speaking, now, rather than failing the task at its end
// for words it found waiting.
func (d *commandDriver) Unsteerable() string {
	if len(d.cmd.Resume) > 0 {
		return ""
	}
	return d.name + " runs once and declares no way to resume a session, so words reach it nowhere"
}

// Result implements task.Resulter: the program's last output is this task's result, which is
// what a completed task's `result` carries and what a charter's `$result` derefs.
func (d *commandDriver) Result() string { return d.last }

// commandExec performs the driver's actions: the invocation, which starts the program, and each
// call the program makes, which runs the tool and answers the program. It is THE subprocess path
// of this runner: where containment is applied and where the environment is BUILT rather than
// inherited.
type commandExec struct{ d *commandDriver }

func (e *commandExec) Execute(ctx context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	d := e.d
	if tu.Name == runTool {
		return d.start(ctx, tu)
	}
	c := d.proposed
	if c == nil || c.tool.Name != tu.Name {
		return task.StepResult{Use: tu}, nil, fmt.Errorf("%s: no call of %s is waiting", d.name, tu.Name)
	}
	d.proposed = nil
	if c.ctx.Err() != nil {
		// The shell gave up before the gate answered. Performed for nobody is not performed:
		// a push whose asker is gone is a push nobody reads the result of.
		return task.StepResult{Use: tu, Output: "not performed: the caller went away"}, nil, nil
	}
	// The tool runs under the loop's context, and also stops if the caller goes: an `ask`
	// whose shell was interrupted is withdrawn from the docket rather than waiting for an
	// answer nobody will read.
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-c.ctx.Done():
			cancel()
		case <-tctx.Done():
		}
	}()
	// The three outcomes go three ways, as the verse agent's executor sends them: a failure is
	// the program's to read (exit 1), a refusal by the far side is an answer (exit 3), and
	// neither is what happened. None of them ends the run: the program decides what to do next.
	out, blocked, err := c.tool.Do(tctx, tu)
	// Answered at the next boundary (Next), once what the decider said has been folded in.
	d.answering = c
	if err != nil {
		// Returned as the failure it is, so the record says the push failed rather than
		// showing an action that happened with "error" in its text; Next answers the program
		// and does not let it end the run.
		c.pending = reply{err: err}
		return task.StepResult{Use: tu}, nil, err
	}
	c.pending = reply{out: out, refused: blocked}
	return task.StepResult{Use: tu, Output: out, Blocked: blocked}, nil, nil
}

// start runs the program, beside the loop: the action is done when the program is running, and
// what it says is read when it exits (ended).
func (d *commandDriver) start(ctx context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	text, _ := tu.Input["text"].(string)
	argv := d.cmd.argv(text, d.session)
	if len(argv) == 0 {
		return task.StepResult{Use: tu}, nil, fmt.Errorf("%s: empty argv", d.name)
	}
	// Under the loop's context, which is the task's: a cancel kills the program.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// What makes this honour its context, which task.Executor states as an obligation.
	// CommandContext kills the program when the task is cancelled, but stdout below is a buffer
	// rather than a file, so os/exec copies through a pipe and Wait waits for the COPY. Anything
	// the agent left running — a dev server, a watcher, a `&` in a script it wrote — inherits
	// that pipe and holds it open, and the task becomes uncancellable. An agent binary is far
	// more likely to leave something running than a one-line run_bash, so this seam needs it more
	// than the one it is borrowed from (harness.orphanWaitDelay, which explains the number).
	cmd.WaitDelay = 2 * time.Second
	// The environment is the allowlist and nothing else, plus how to reach this task's tools.
	// os.Environ() is deliberately not consulted: a passthrough list is a floor, and
	// "everything except what we remembered to strip" is not.
	cmd.Env = withTools(buildEnv(d.env.Env), d.set)
	// The sandbox is already around this whole process, so the program runs in the workspace
	// and nothing more is claimed here.
	cmd.Dir = d.dir
	p := &program{done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = &p.out, &p.errb
	if err := cmd.Start(); err != nil {
		return task.StepResult{Use: tu}, nil, fmt.Errorf("%s: %v", d.name, err)
	}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	d.running = p
	return task.StepResult{Use: tu, Output: "running " + d.name}, nil, nil
}

// read turns what the program printed into a result, per its declared Output.
func (d *commandDriver) read(stdout []byte, stderr string, runErr error) (commandResult, error) {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = strings.TrimSpace(string(stdout))
	}
	if d.cmd.Output != "json" {
		if runErr != nil {
			return commandResult{}, fmt.Errorf("%s: %v: %s", d.name, runErr, firstLine(detail))
		}
		return commandResult{Result: strings.TrimRight(string(stdout), "\n")}, nil
	}
	var res commandResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &res); err != nil {
		// No decodable answer is not an empty answer. A program that is missing, or that wrote
		// a usage message, fails the turn with what it actually said.
		if runErr != nil && detail == "" {
			detail = runErr.Error()
		}
		return commandResult{}, fmt.Errorf("%s produced no readable result: %s", d.name, detail)
	}
	return res, nil
}

// buildEnv is the passthrough allowlist, as a process environment.
//
// The environment's `commands:` are not applied here, and nothing claims they are: a PATH
// narrowed to a list is a convention until the filesystem is restricted, since an absolute path
// walks straight past it, so what a task can run is the sandbox's to bound (runnerd), and
// `commands:` documents the image the environment expects.
//
// It NEVER returns nil, and that is not a style choice. `exec.Cmd.Env == nil` means "inherit
// os.Environ()", so an environment that declared no passthrough — or whose variables all happened
// to be unset — would get the opposite of what it asked for: the whole of the runner's
// environment, including the git token, handed to the task. A list that is a floor when non-empty
// and an open door when empty is worse than no list.
func buildEnv(passthrough []string) []string {
	env := []string{}
	for _, name := range passthrough {
		if v, ok := lookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 72 {
		s = s[:72]
	}
	return s
}
