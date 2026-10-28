// Package claude is the claude agent: Anthropic's Claude Code as a verse task, with every
// command, every edit and every question it proposes decided by the task's autonomy and shown on
// the read plane. A spawn asks for it with `agent: {name: claude}`, and a runner that hosts it
// builds its task with Task.
//
// It is an agent of its own beside verse's (package agent) and codex (package codex), and it
// exists for the reason codex does: a program the runner runs and reads the output of is gated
// once, at the invocation, and nothing inside it is seen. Claude Code offers something else.
//
// Headless, Claude Code speaks a protocol over its stdio: this side sends the prompt as a user
// message and reads the session as it goes, and, told that this side answers its permission
// prompts (`--permission-prompt-tool stdio`), Claude ASKS THIS SIDE before every tool call its
// permission mode does not already allow. Under `manual`, that is each command, each edit and
// each question for a person; a read in the workspace asks nothing. So the decision points verse wants are
// there, and this package's job is to put each of them in front of the loop as an ordinary
// action:
//
//   - Next waits on Claude. Each permission request is the next proposed action, with the
//     step it is derived here — `commands.run` for Bash, `files.write` on the path for Edit
//     and Write (and on the file it lands on, where a symlink leads elsewhere in the
//     workspace), `question.ask` for a question where the environment gives questions. The
//     supervisor gates it exactly as it gates the verse agent's `run_bash` or `write_file`: the
//     autonomy decides, a blocked question goes to the docket, `verse question` answers it. The
//     gate model gains nothing new.
//   - Execute answers Claude. Proceed is allow, and the executor then waits for the call's
//     result so the loop's exec frame carries what the command printed. A refused gate is a
//     deny with the reason, which the model reads as the call's result and plans around rather
//     than retrying.
//   - The runner owns the session's settings. No settings file is read (`--setting-sources
//     ""`), the one setting is the runner's (`--settings`: an `ask` rule on every tool that is
//     a step, so the commands Claude's own list calls read-only ask too), no MCP server but
//     this side's (`--strict-mcp-config`), and the tools are the ones named here (`--tools`),
//     so a workspace cannot loosen a gate and a tool this side has no step for is never offered.
//
// The runner's own tools are offered to Claude as an MCP server this side serves over the same
// pipe (the `sdk` server kind), and a call Claude makes of one is an action of the loop like a
// permission request is: the tool's own step classifies it, the autonomy decides it, its Do
// performs it in this process, and the result is the call's output. `git-push` is the first; it
// is how Claude delivers, since there is no finisher doing it for an agent that can decide.
//
// What the spawn's agent runs with reaches Claude: its `model` setting is the session's model
// and its `effort` the session's effort; one the spawn names none of is the runner's configured
// default, filled in before the launch reaches here (verserunner), or Claude's own.
//
// What Claude does WITHOUT asking is the part that is attested rather than enforced: its reads
// in the workspace (Read, Glob, Grep). Those are narrated when they complete, so the read plane
// shows them, but no gate saw them first. A read outside the workspace asks, and is refused.
// The verse agent has the same shape for its reads, which are no step; here the set of free
// reads is the tool list below, and this comment is where that is said.
//
// Claude Code's own sandbox is not used; the runner's containment is the sandbox, and the
// permission prompt is the gate.
//
// A turn ends at Claude's `result` only once Claude has echoed back every message this side
// sent (`--replay-user-messages`): words that reach it as it ends a turn start a turn of their
// own, and the run waits for that one.
//
// The wire lives in wire.go alone and is on the json allowlist (scripts/check-json.sh); the
// message shapes were read from Claude Code 2.1.304.
package claude

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/verserunner/agent"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// Agent is the agent's name: what a spawn writes in `agent: {name: …}`.
const Agent = "claude"

// ModelHost is where Claude calls its model: what a runner lets its process reach whatever the
// task's network.
const ModelHost = "api.anthropic.com"

// Efforts are the efforts Claude takes (`--effort`): what a spawn's agent's `effort` may name.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// mcpServer is the name this side's MCP server is declared under; Claude prefixes its tools
// `mcp__<server>__`.
const mcpServer = "verse"

// The variables the runner reads Claude's credential from, the first set taken: a
// subscription's token (`claude setup-token`), which is a person's and runs on a runner that is
// theirs; then an API key, which is what a shared runner holds. The runner sends the value to the
// task process (Creds.Credential), which puts it in the claude process's environment under the
// same name, with a CLAUDE_CONFIG_DIR of its own that dies with the task. Neither set, Claude's own
// login, under the HOME the environment passes through, is what authenticates it.
const (
	EnvOAuthToken = "CLAUDE_CODE_OAUTH_TOKEN"
	EnvAPIKey     = "ANTHROPIC_API_KEY"
)

// Config is how this runner runs Claude Code: the program keys of `agents.claude`. It holds no
// secret.
type Config struct {
	// Bin is the program, `claude` on PATH by default.
	Bin string
}

// Program is the program the runner runs.
func (c *Config) Program() string { return cmp.Or(c.Bin, "claude") }

// Key reads Claude's credential from this process's environment, the runner's: the variable it
// is under, and its value. Both "" when neither is set.
func Key() (env, value string) {
	for _, env := range []string{EnvOAuthToken, EnvAPIKey} {
		if v := os.Getenv(env); v != "" {
			return env, v
		}
	}
	return "", ""
}

// Creds is the section with its credential read: what a task process runs Claude with.
func (c *Config) Creds(env, key string) Creds {
	return Creds{Bin: c.Program(), CredentialEnv: env, Credential: key}
}

// MissingKey reports why Claude may not be able to authenticate here, or "". A warning rather
// than a refusal: without a credential, Claude may still be logged in under the HOME it is
// given, and only a turn can tell.
func MissingKey(getenv func(string) string) string {
	if getenv(EnvOAuthToken) == "" && getenv(EnvAPIKey) == "" {
		return fmt.Sprintf("neither %s nor %s is set in this runner's environment", EnvOAuthToken, EnvAPIKey)
	}
	return ""
}

// Creds is the resolved claude configuration: the section's values and the credential read.
type Creds struct {
	Bin           string
	CredentialEnv string
	Credential    string
}

// claudeTools is the tools of Claude's own this side offers it: the ones it has a step for,
// and the reads, which are no step. Everything else of Claude's (sub-agents, the web, its task
// list) is left out, so the session has nothing this side cannot account for.
var claudeTools = []string{"Bash", "Read", "Edit", "Write", "Glob", "Grep"}

// askedTools is the tools of Claude's own that are a step: each is asked about, every time,
// whatever Claude's own rules would allow unasked (settings).
var askedTools = []string{"Bash", "Edit", "Write"}

// askTool is Claude's question for the person, offered where the environment gives
// `question.ask`, and asked about like the others.
const askTool = "AskUserQuestion"

// argv is the claude process to start: headless, speaking the stream protocol both ways,
// echoing back each user message as it takes it up, asking this side before each call, with
// none of a workspace's or a user's settings but the runner's own, offered its question tool
// where the environment gives questions (ask), and this side's MCP server where there are tools
// to offer.
func (c Creds) argv(model, effort string, tools, ask bool) []string {
	offered, asked := claudeTools, askedTools
	if ask {
		offered, asked = append(slices.Clone(offered), askTool), append(slices.Clone(asked), askTool)
	}
	out := []string{cmp.Or(c.Bin, "claude"), "-p",
		"--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--replay-user-messages",
		"--permission-prompt-tool", "stdio", "--permission-mode", "manual",
		"--setting-sources", "", "--settings", settings(asked),
		"--strict-mcp-config", "--no-session-persistence",
		"--tools", strings.Join(offered, ","),
	}
	if tools {
		out = append(out, "--mcp-config", mcpConfig(mcpServer))
	}
	if model != "" {
		out = append(out, "--model", model)
	}
	if effort != "" {
		out = append(out, "--effort", effort)
	}
	return out
}

// The names an action proposed by this agent carries: Claude's own words for what it is about
// to do, lowered, which is what a person reading `verse question` or `attach` should see.
const (
	kindBash  = "bash"
	kindEdit  = "edit"
	kindWrite = "write"
	kindAsk   = "ask"
	// kindTool is a proposal's kind for a call of one of the runner's tools; the action's name
	// is the tool's own.
	kindTool = "tool"
)

// Task builds a task that runs Claude Code over its stream protocol in dir, with env as the
// claude process's environment (the runner's allowlist, the launch's environment variables, and
// where the task's credential file is, for a task that has one), offering it tools (the
// runner's) as its own.
func Task(l runner.Launch, reg *task.Registry, creds Creds, env []string, dir string, tools []agent.Tool, ceilings map[string]systems.Ceiling) (*task.Task, error) {
	var contract *answer.Contract
	if l.Answer != nil {
		c, err := answer.Parse(l.Answer)
		if err != nil {
			return nil, fmt.Errorf("answer contract: %w", err)
		}
		contract = c
	}
	// Claude's shell and its edits are its own: it can't run without them, which its offer says
	// (Requires), so a launch whose environment gives none of one is refused where it is written
	// (runnerd.CheckOffer), at install and again by the runner, not here.
	d := &driver{
		creds: creds, prompt: l.Prompt, dir: dir,
		model: l.AgentSettings().Model(), effort: l.AgentSettings().Settings["effort"],
		env:      env,
		resolver: reg,
		log:      os.Stderr,
		tools:    tools,
		toolBy:   map[string]agent.Tool{},
		asks:     l.Environment.Gives(task.StepQuestionAsk),
	}
	for _, t := range tools {
		d.toolBy[t.Name] = t
	}
	if contract != nil {
		d.contractSrc = contract.Source()
	}
	d.toolStep = agent.StepOf(tools)
	sup := task.NewSupervisor(l.Environment, l.Autonomy, reg)
	sup.SetCeilings(ceilings)
	sup.SetStepsOf(d.stepsOf)
	t := task.NewTask(l.ID, sup, d, &executor{d: d})
	// What it runs with: the record's to say. A setting the launch names none of is Claude's
	// own.
	settings := map[string]string{}
	for name, v := range map[string]string{runner.SettingModel: d.model, "effort": d.effort} {
		if v != "" {
			settings[name] = v
		}
	}
	if len(settings) > 0 {
		t.SetAgent(Agent, settings)
	}
	t.SetLog(os.Stderr)
	if contract != nil {
		t.SetAnswerCheck(&answer.Checker{C: contract, FailHint: "saying you cannot"})
	}
	d.emit = t.Emit
	return t, nil
}

// driver is one task's conversation with one claude process: the loop's task.Driver.
type driver struct {
	creds       Creds
	prompt      string
	contractSrc string
	// model and effort are the launch's agent's, the runner's defaults filled in; "" leaves each
	// to Claude.
	model, effort string
	dir           string
	realDir       string // dir with its symlinks resolved, in case Claude reports paths under it
	env           []string
	resolver      task.Resolver
	log           io.Writer
	emit          func(task.Event)
	// tools are the runner's, offered to Claude as an MCP server and run here when it calls one.
	tools  []agent.Tool
	toolBy map[string]agent.Tool
	// toolStep is the step a call of one of the tools is, as the verse agent's is classified.
	toolStep func(task.ToolUse) (task.Step, bool)
	// asks is whether the environment gives `question.ask`, so Claude is offered its question tool.
	asks bool

	rpc     *rpc
	home    string // the private CLAUDE_CONFIG_DIR this task made for Claude, when it made one
	started bool
	// inTurn is whether Claude owes a turn's end: a user message was sent, and no result has
	// come back that followed every message sent.
	inTurn bool
	// unseen is the user messages sent that Claude has not yet echoed back as taken up. A
	// result while one is unseen is not the end of the turn: a message that reached Claude after
	// its turn ended starts a turn of its own.
	unseen int
	// interrupted is whether the running turn has been told to stop; reset by each turn.
	interrupted bool
	closed      bool

	// uses are the tool uses Claude has announced and this side has not seen the result of, by
	// id: a free read's result is narrated from here, since the result alone does not say
	// what was read.
	uses map[string]block
	// answered are the uses this side answered a permission request for, allowed or denied,
	// so their result is the loop's exec or decision frame and not narrated a second time.
	answered map[string]bool
	// queued are requests Claude made while the executor was waiting on a result, held for Next.
	queued []event

	// proposed is the request Next last put in front of the loop, until Execute answers it or
	// Next learns it was refused. Atomic: the supervisor reads it (stepsOf) on the gate's own
	// goroutine, which an intervention can leave behind while the next Next runs.
	proposed atomic.Pointer[proposal]

	last string // the agent's last message: this task's result

	mu      sync.Mutex
	folding []string
}

// proposal is one permission request from Claude, as the loop sees it.
type proposal struct {
	ev   event
	kind string // kindBash | kindEdit | kindWrite | kindAsk | kindTool
	tool agent.Tool
	// steps are what the request is, as the spec names a task's steps. None is no step.
	steps []task.Step
	// gone is closed when Claude cancels the request: the question about it is withdrawn
	// (task.Withdrawer) rather than left in front of somebody for an action Claude no longer
	// waits on.
	gone <-chan struct{}
}

// Withdrawn implements task.Withdrawer: the proposed request's cancellation by Claude, or
// Claude's going away, after which nobody waits on the answer.
func (d *driver) Withdrawn(task.ToolUse) <-chan struct{} {
	p := d.proposed.Load()
	if p == nil {
		return nil
	}
	return p.gone
}

// stepsOf is the supervisor's (Supervisor.SetStepsOf): the steps the proposed request is, each
// of which it decides, so an edit through a link goes ahead only where both the path named and
// the file it lands on would. None is no step.
func (d *driver) stepsOf(task.ToolUse) []task.Step {
	if p := d.proposed.Load(); p != nil {
		return p.steps
	}
	return nil
}

// resultWait bounds how long the executor waits for a call Claude was allowed before treating
// the turn as having moved on without it.
var resultWait = 24 * time.Hour

// Next implements task.Driver: what Claude wants to do next, as an action.
func (d *driver) Next(ctx context.Context, prev task.StepResult) (task.ToolUse, bool, error) {
	if p := d.proposed.Swap(nil); p != nil {
		// The loop decided about the last proposal without executing it.
		switch {
		case prev.Err != nil:
			return task.ToolUse{}, false, prev.Err
		case prev.Blocked:
			d.decline(p, prev.Note)
		}
	} else if prev.Err != nil && d.started {
		return task.ToolUse{}, false, prev.Err
	}
	if !d.started {
		d.started = true
		if err := d.start(ctx); err != nil {
			return task.ToolUse{}, false, err
		}
	} else if !d.inTurn {
		// Between turns: the loop has something more to say, or nothing. A conversation's
		// reply, a contract repair and a steer all arrive here through Fold.
		msg := d.drain()
		if msg == "" {
			return task.ToolUse{}, false, nil
		}
		if err := d.say(msg); err != nil {
			return task.ToolUse{}, false, err
		}
	} else if msg := d.drain(); msg != "" {
		// Mid-turn: words taken at this boundary reach the running session, which takes them
		// up at its next step.
		if err := d.say(msg); err != nil {
			return task.ToolUse{}, false, err
		}
	}
	return d.await(ctx)
}

// start brings the process up and sends the first turn.
func (d *driver) start(ctx context.Context) error {
	env := append([]string{}, d.env...)
	if d.creds.Credential != "" {
		// The credential goes to Claude under the name it reads it by, and nowhere else this
		// side can arrange: Claude reads it from its environment, and its environment is what
		// every command it runs inherits. So a command can read it, and once read it leaves
		// however the task's output does: over the run's network, and, whatever the network, in
		// what the task hands back (its messages, a question, a push). Claude's own state (its config,
		// its sessions) lives in a directory made for the task and removed with it, so no
		// task sees another's and the login a HOME holds is not read.
		home, err := os.MkdirTemp("", "claude-home-")
		if err != nil {
			return fmt.Errorf("claude: a home for the session: %w", err)
		}
		d.home = home
		env = append(env, d.creds.CredentialEnv+"="+d.creds.Credential, "CLAUDE_CONFIG_DIR="+home)
	}
	// Nothing but the model: no update check, no telemetry, so the network a task is given is
	// spent on the task.
	env = append(env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1")
	argv := d.creds.argv(d.model, d.effort, len(d.tools) > 0, d.asks)
	// The process outlives any one ctx the loop hands Next: it is closed by Close, when the
	// run ends, and not when a boundary's context does.
	rpc, err := startProcess(context.Background(), argv, d.dir, env)
	if err != nil {
		return err
	}
	d.rpc = rpc
	d.uses = map[string]block{}
	d.answered = map[string]bool{}
	if _, err := rpc.Control(ctx, "initialize", map[string]any{"hooks": map[string]any{}}); err != nil {
		return d.fail(err)
	}
	d.logf("claude %s, model %s", argv[0], cmp.Or(d.model, "default"))
	return d.say(d.opening())
}

// fail wraps a startup error with what Claude said on stderr, which is where a missing binary
// or a broken login explains itself.
func (d *driver) fail(err error) error {
	if why := d.rpc.Err(); why != "" {
		return fmt.Errorf("%w: %s", err, why)
	}
	return err
}

// opening is the first turn: the contract, if any, and the prompt.
func (d *driver) opening() string {
	var b strings.Builder
	if d.contractSrc != "" {
		fmt.Fprintf(&b, "ANSWER CONTRACT. Your final message must satisfy:\n%s\n\n", d.contractSrc)
	}
	b.WriteString(d.prompt)
	return b.String()
}

// say puts a user message into the session: a new turn when it is idle, words for the running
// one when it is not.
func (d *driver) say(text string) error {
	if !d.inTurn {
		// A new turn: what the last one left unanswered or unfinished (a request set aside
		// while it was interrupted, a use whose result never came) is about calls Claude is
		// no longer making.
		d.queued = nil
		d.uses = map[string]block{}
		d.answered = map[string]bool{}
	}
	if err := d.rpc.SendUser(text); err != nil {
		return d.fail(err)
	}
	d.unseen++
	d.inTurn, d.interrupted = true, false
	return nil
}

// await reads Claude until it asks for something the loop must decide, or the turn ends.
func (d *driver) await(ctx context.Context) (task.ToolUse, bool, error) {
	for {
		ev, ok, err := d.next(ctx)
		if err != nil {
			return task.ToolUse{}, false, err
		}
		if !ok {
			return task.ToolUse{}, false, fmt.Errorf("%s", d.rpc.Err())
		}
		if ev.isRequest() {
			if d.served(ev) {
				continue
			}
			p, tu, err := d.propose(ev)
			if err != nil {
				// A request this side does not understand is refused, so Claude hears an
				// answer rather than waiting forever on one.
				d.logf("refusing %s: %v", ev.Req.Subtype, err)
				d.refuse(ev, err.Error())
				continue
			}
			d.proposed.Store(p)
			return tu, true, nil
		}
		turnEnded, err := d.handle(ev)
		if err != nil {
			return task.ToolUse{}, false, err
		}
		if turnEnded {
			if msg := d.drain(); msg != "" {
				if err := d.say(msg); err != nil {
					return task.ToolUse{}, false, err
				}
				continue
			}
			return task.ToolUse{}, false, nil
		}
	}
}

// next is one event from Claude: what the executor set aside first, then the stream. ok is
// false when the stream has ended. A ctx that ends interrupts the turn, which is what cancel
// means to a running Claude.
func (d *driver) next(ctx context.Context) (event, bool, error) {
	for len(d.queued) > 0 {
		ev := d.queued[0]
		d.queued = d.queued[1:]
		if ev.Req.Subtype == "can_use_tool" && isClosed(d.rpc.Cancelled(ev.RequestID)) {
			// Claude took the request back while it waited here: nobody is asked about it, and
			// the result Claude gave the use in its place is its own.
			d.answered[ev.Req.ToolUseID] = true
			d.rpc.settled(ev.RequestID)
			continue
		}
		return ev, true, nil
	}
	select {
	case ev, ok := <-d.rpc.Events:
		return ev, ok, nil
	case <-ctx.Done():
		d.interrupt()
		return event{}, false, ctx.Err()
	}
}

// served answers the requests that are this side's to answer at once, with nothing to decide:
// the MCP server's handshake and its tool list. It reports whether ev was one. A `tools/call`
// is not: it follows a permission request the loop decided, and the executor answers it.
func (d *driver) served(ev event) bool {
	if ev.Req.Subtype != "mcp_message" {
		return false
	}
	m, err := ev.Req.mcp()
	if err != nil {
		_ = d.rpc.MCPError(ev.RequestID, m, -32700, err.Error())
		return true
	}
	switch m.Method {
	case "initialize":
		_ = d.rpc.MCPRespond(ev.RequestID, m, map[string]any{
			"protocolVersion": m.protocolVersion(),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": mcpServer, "version": "0"},
		})
	case "tools/list":
		specs := make([]map[string]any, 0, len(d.tools))
		for _, t := range d.tools {
			specs = append(specs, mcpToolSpec(t.Name, t.Description, t.Params, t.Required))
		}
		_ = d.rpc.MCPRespond(ev.RequestID, m, map[string]any{"tools": specs})
	case "tools/call":
		// A call no permission request preceded: not approved, so not run.
		_ = d.rpc.MCPRespond(ev.RequestID, m, mcpCallResult(false, "verse did not approve this call"))
	default:
		if m.isNotification() {
			_ = d.rpc.MCPRespond(ev.RequestID, m, nil)
		} else {
			_ = d.rpc.MCPError(ev.RequestID, m, -32601, "method not found: "+m.Method)
		}
	}
	return true
}

// propose turns a permission request from Claude into the action the loop will gate.
func (d *driver) propose(ev event) (*proposal, task.ToolUse, error) {
	if ev.Req.Subtype != "can_use_tool" {
		return nil, task.ToolUse{}, fmt.Errorf("a request this side does not handle: %s", ev.Req.Subtype)
	}
	if ev.Req.Unread != "" {
		return nil, task.ToolUse{}, fmt.Errorf("a %s request this side cannot read: %s", ev.Req.ToolName, ev.Req.Unread)
	}
	if ev.Req.Input == nil {
		// Allowed, the input goes back as the call's: an object, never null.
		ev.Req.Input = map[string]any{}
	}
	in := ev.Req.Input
	p := &proposal{ev: ev, gone: d.rpc.Cancelled(ev.RequestID)}
	switch name := ev.Req.ToolName; name {
	case "Bash":
		if bg, _ := in["run_in_background"].(bool); bg {
			// Claude reads a background command's output with a tool this side does not offer,
			// so nobody would read it; and it would outlive the action decided about it.
			return nil, task.ToolUse{}, errors.New("a command in the background is not offered here: run it in the foreground")
		}
		p.kind = kindBash
		cmd := str(in, "command")
		out := map[string]any{"command": cmd}
		if why := str(in, "description"); why != "" {
			out["reason"] = why
		}
		p.steps = []task.Step{{Name: task.StepCommandsRun, Fields: map[string]string{"command": cmd}}}
		return p, task.ToolUse{Name: kindBash, Input: out}, nil

	case "Edit", "Write":
		p.kind = kindEdit
		if name == "Write" {
			p.kind = kindWrite
		}
		paths, err := d.writePaths(str(in, "file_path"))
		if err != nil {
			return nil, task.ToolUse{}, err
		}
		out := map[string]any{"path": paths[0]}
		if len(paths) > 1 {
			out["lands"] = paths[1]
		}
		if name == "Edit" {
			out["old"] = truncate(str(in, "old_string"), 200)
			out["new"] = truncate(str(in, "new_string"), 200)
		}
		for _, at := range paths {
			p.steps = append(p.steps, task.Step{Name: task.StepFilesWrite, Fields: map[string]string{"path": at}})
		}
		return p, task.ToolUse{Name: p.kind, Input: out}, nil

	case "AskUserQuestion":
		p.kind = kindAsk
		questions := questionsOf(in)
		if len(questions) == 0 {
			return nil, task.ToolUse{}, errors.New("the request asks no question this side can read")
		}
		// []any rather than a typed slice: the input is read as a document, by ToolUse.Describe
		// among others, and a document's list is []any.
		var qs []any
		for _, q := range questions {
			qs = append(qs, q.Question)
		}
		// The step `question.ask`, as the `question-ask` tool's use is: decided by the autonomy,
		// then put to the person.
		p.steps = []task.Step{{Name: task.StepQuestionAsk, Fields: map[string]string{}}}
		return p, task.ToolUse{Name: kindAsk, Input: map[string]any{"questions": qs}}, nil
	}

	if own, ok := strings.CutPrefix(ev.Req.ToolName, "mcp__"+mcpServer+"__"); ok {
		t, ok := d.toolBy[own]
		if !ok {
			return nil, task.ToolUse{}, fmt.Errorf("claude called a tool this task does not offer: %q", own)
		}
		p.kind, p.tool = kindTool, t
		tu := task.ToolUse{Name: t.Name, Input: in}
		// The tool's own step, as the verse agent's call of it is classified: a call that is no
		// step (a branch the spawn never permitted) is refused by its Do, so nobody is asked
		// about something that cannot happen.
		if st, ok := d.toolStep(tu); ok {
			p.steps = []task.Step{st}
		}
		return p, tu, nil
	}
	switch ev.Req.ToolName {
	case "Read", "Glob", "Grep":
		// Claude asks about a read only where it falls outside the directory it works in.
		return nil, task.ToolUse{}, fmt.Errorf("reading outside the workspace is not offered here; read under %s", d.dir)
	}
	return nil, task.ToolUse{}, fmt.Errorf("a tool this side has no step for: %s", ev.Req.ToolName)
}

// writePaths are the paths an edit of p is decided on, as autonomy rows see them: the path Claude
// names, and, where its symlinks lead to another file of the workspace, that file, which is the
// one written. Claude's own rules are checked the same way, on the path named and on where it
// leads, and it asks this side with the path named alone. A link that leads out of the
// workspace adds nothing: the workspace is the task's filesystem, and a write that leaves it is
// the sandbox's to refuse.
func (d *driver) writePaths(p string) ([]string, error) {
	if strings.TrimSpace(p) == "" {
		return nil, errors.New("the request names no file")
	}
	if !filepath.IsAbs(p) {
		// Claude resolves a relative path against its working directory, the workspace.
		p = filepath.Join(d.dir, p)
	}
	named := filepath.ToSlash(d.rel(p))
	out := []string{named}
	if real, err := landsOn(p); err == nil {
		if at := d.rel(real); !filepath.IsAbs(at) && filepath.ToSlash(at) != named {
			out = append(out, filepath.ToSlash(at))
		}
	}
	return out, nil
}

// landsOn is the file a write of p creates or changes: p with every symlink on the way resolved,
// to the end of a chain, including a last link that names a file not there yet, which the write
// creates.
func landsOn(p string) (string, error) {
	for range 40 { // as many links as a path lookup follows before giving up
		real, err := resolveExisting(p)
		if err != nil {
			return "", err
		}
		fi, err := os.Lstat(real)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return real, nil // a file, or none yet
		}
		dest, err := os.Readlink(real)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(filepath.Dir(real), dest)
		}
		p = dest
	}
	return "", errors.New("too many levels of symbolic links")
}

// resolveExisting resolves the symlinks in p as far as p exists, and rejoins whatever tail does
// not exist yet: a write names a file that is about to be created.
func resolveExisting(p string) (string, error) {
	tail := ""
	for cur := p; ; {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(resolved, tail), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", errors.New("no part of it exists")
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// refuse answers a request this side cannot act on, so Claude hears why.
func (d *driver) refuse(ev event, why string) {
	switch ev.Req.Subtype {
	case "can_use_tool":
		// Answered, so the deny's result is not narrated as a call that ran and failed.
		d.answered[ev.Req.ToolUseID] = true
		_ = d.rpc.Deny(ev.RequestID, "verse refused this call: "+why+". Do not retry it; plan around it.")
	default:
		_ = d.rpc.respondError(ev.RequestID, why)
	}
}

// decline answers a request the loop refused, and tells the model why. The refusal is the
// call's result, so the model reads it where it asked.
func (d *driver) decline(p *proposal, note string) {
	what := p.kind
	if p.kind == kindTool {
		what = "call of " + p.tool.Name
	}
	d.answered[p.ev.Req.ToolUseID] = true
	_ = d.rpc.Deny(p.ev.RequestID, refusal(what, note))
}

// refusal is what Claude reads for an action the loop did not take: a refusal, which it is not
// to retry, or a question withdrawn before anyone answered it (a new instruction arrived,
// say), which it may propose again if it still applies.
func refusal(what, note string) string {
	if strings.HasPrefix(note, task.WithdrawnNote) {
		return "verse did not decide this " + what + ": " + note +
			". It was not refused; propose it again if it still applies after what you were just told."
	}
	why := "verse refused this " + what
	if note != "" {
		why += ": " + note
	}
	return why + ". Do not retry it; plan around it."
}

// handle narrates a message and tracks the turn. It reports whether the turn ended.
func (d *driver) handle(ev event) (bool, error) {
	switch ev.Type {
	case "assistant":
		for _, b := range ev.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					// The result, whether or not anybody is watching; under the lock, since a
					// snapshot reads it while the run goes on.
					d.mu.Lock()
					d.last = b.Text
					d.mu.Unlock()
					d.emitEvent(task.Event{Kind: task.EventSay, Text: b.Text})
				}
			case "tool_use":
				d.uses[b.ID] = b
			}
		}
	case "user":
		if ev.Replay {
			// A message this side sent, taken up.
			if d.unseen > 0 {
				d.unseen--
			}
			return false, nil
		}
		for _, b := range ev.Content {
			if b.Type != "tool_result" {
				continue
			}
			d.narrate(b)
		}
	case "result":
		if ev.Result.IsError {
			d.inTurn = false
			return true, fmt.Errorf("claude: the turn failed: %s", cmp.Or(firstLine(ev.Result.Result), ev.Result.Subtype))
		}
		if ev.Result.Result != "" {
			// The turn's own account of its answer, over the last text seen: a turn that ends
			// on a tool call has said its last words earlier, and this is where Claude says
			// which they were.
			d.mu.Lock()
			d.last = ev.Result.Result
			d.mu.Unlock()
		}
		if d.unseen > 0 {
			// Words sent for this turn reached Claude after it had ended: they are the next
			// turn's, which Claude starts on its own.
			return false, nil
		}
		d.inTurn = false
		return true, nil
	case "rate_limit_event":
		d.logf("rate limit event")
	}
	return false, nil
}

// narrate puts a completed tool use on the read plane. A use this side answered rode the loop's
// exec or decision frame; everything Claude did without asking is reported as the action it
// was, so an observer sees the free reads too.
func (d *driver) narrate(res block) {
	use, seen := d.uses[res.ToolUseID]
	delete(d.uses, res.ToolUseID)
	if d.answered[res.ToolUseID] {
		delete(d.answered, res.ToolUseID)
		return
	}
	if !seen {
		return
	}
	text := d.describeUse(use)
	if res.IsError {
		text += " → error: " + firstLine(res.text())
	}
	d.emitEvent(task.Event{Kind: task.EventExec, Tool: strings.ToLower(use.Name), Text: text})
}

// describeUse is a tool use's one-line account, as a person should read it.
func (d *driver) describeUse(use block) string {
	in := use.Input
	for _, k := range []string{"file_path", "path"} {
		if v := str(in, k); v != "" {
			return d.rel(v)
		}
	}
	if v := str(in, "pattern"); v != "" {
		if p := str(in, "path"); p != "" {
			return v + " in " + d.rel(p)
		}
		return v
	}
	return task.ToolUse{Name: use.Name, Input: in}.Describe()
}

func (d *driver) emitEvent(e task.Event) {
	if d.emit != nil {
		d.emit(e)
	}
}

// rel is a path as the workspace sees it, where Claude reports it absolutely: what is outside
// the workspace stays absolute. The workspace may be reached through a symlink, so both
// spellings of the root are tried.
func (d *driver) rel(p string) string {
	if p == "" {
		return p
	}
	if !filepath.IsAbs(p) || d.dir == "" {
		return filepath.Clean(p)
	}
	if d.realDir == "" {
		d.realDir = d.dir
		if real, err := filepath.EvalSymlinks(d.dir); err == nil {
			d.realDir = real
		}
	}
	for _, root := range []string{d.dir, d.realDir} {
		if r, err := filepath.Rel(root, p); err == nil && r != ".." && !strings.HasPrefix(r, "../") {
			return r
		}
	}
	return p
}

// interrupt stops the running turn. Called when the loop's context ends: the loop is about to
// stop asking, and Claude should stop doing.
func (d *driver) interrupt() {
	if d.rpc == nil || !d.inTurn || d.interrupted {
		return
	}
	d.interrupted = true // once per turn: the loop and Close both ask, and Claude hears it once
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = d.rpc.Control(ctx, "interrupt", nil)
}

// Close ends the claude process, and the home it was given. The task's own end is what calls
// it (task.Closer).
func (d *driver) Close() {
	if d.closed {
		return
	}
	d.closed = true
	if d.rpc != nil {
		d.interrupt()
		d.rpc.Close()
	}
	if d.home != "" {
		_ = os.RemoveAll(d.home)
	}
}

// Fold implements task.Conversation: words for the model, taken at the next boundary.
func (d *driver) Fold(messages []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.folding = append(d.folding, messages...)
}

func (d *driver) drain() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.folding) == 0 {
		return ""
	}
	s := strings.Join(d.folding, "\n")
	d.folding = nil
	return s
}

// Result implements task.Resulter: the agent's last message, so far.
func (d *driver) Result() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}

func (d *driver) logf(format string, args ...any) {
	if d.log != nil {
		fmt.Fprintf(d.log, "claude: "+format+"\n", args...)
	}
}

// executor performs an approved action: it answers Claude, and waits for what follows.
type executor struct{ d *driver }

func (e *executor) Execute(ctx context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	d := e.d
	p := d.proposed.Swap(nil)
	if p == nil {
		return task.StepResult{Use: tu}, nil, errors.New("claude: nothing proposed to execute")
	}
	id := p.ev.Req.ToolUseID
	withdrawn := task.StepResult{Use: tu, Blocked: true,
		Note: task.WithdrawnNote + ": claude no longer waits on it"}
	if isClosed(p.gone) {
		// Decided after Claude took the request back: nothing to allow, and nothing ran.
		d.answered[id] = true
		d.rpc.settled(p.ev.RequestID)
		return withdrawn, nil, nil
	}
	switch p.kind {
	case kindBash, kindEdit, kindWrite:
		d.answered[id] = true
		if err := d.rpc.Allow(p.ev.RequestID, p.ev.Req.Input); err != nil {
			return task.StepResult{Use: tu}, nil, err
		}
		return d.awaitResult(ctx, tu, id)

	case kindAsk:
		// Each question is put to the person as the `question-ask` tool's is, and the answers
		// ride the call's input back to Claude, which reads them as the person's.
		// A question Claude takes back while the person is being asked leaves the docket.
		actx, stop := context.WithCancel(ctx)
		defer stop()
		go func() {
			select {
			case <-p.gone:
				stop()
			case <-actx.Done():
			}
		}()
		answers := map[string]any{}
		var said []string
		for i, q := range questionsOf(p.ev.Req.Input) {
			// Each question is a record of its own on the docket: the first under the action's
			// id, the ones after it suffixed, so an answer names one question and no other.
			qid := tu.ID
			if i > 0 {
				qid += "-" + strconv.Itoa(i+1)
			}
			text, err := d.askPerson(actx, tu, qid, q)
			if err != nil {
				if isClosed(p.gone) && ctx.Err() == nil {
					d.answered[id] = true
					d.rpc.settled(p.ev.RequestID)
					return withdrawn, nil, nil
				}
				return task.StepResult{Use: tu}, nil, err
			}
			answers[q.Question] = text
			said = append(said, text)
		}
		in := map[string]any{}
		for k, v := range p.ev.Req.Input {
			in[k] = v
		}
		in["answers"] = answers
		d.answered[id] = true
		if err := d.rpc.Allow(p.ev.RequestID, in); err != nil {
			return task.StepResult{Use: tu}, nil, err
		}
		res, deferred, err := d.awaitResult(ctx, tu, id)
		if err == nil {
			res.Output = strings.Join(said, "\n")
		}
		return res, deferred, err

	case kindTool:
		// Allowed, the call comes back as the MCP server's `tools/call`, and the tool's own Do
		// answers it in this process. A refusal by the far side (blocked) and a failure are
		// both words the model reads; neither ends the run, which is the harness's reading of
		// a tool that said no.
		d.answered[id] = true
		if err := d.rpc.Allow(p.ev.RequestID, p.ev.Req.Input); err != nil {
			return task.StepResult{Use: tu}, nil, err
		}
		return d.awaitCall(ctx, tu, p, id)
	}
	return task.StepResult{Use: tu}, nil, fmt.Errorf("claude: unknown proposal %q", p.kind)
}

// awaitResult reads Claude until the allowed call's result arrives, so the exec frame reports
// what it did. Requests Claude makes meanwhile are kept for Next; messages are narrated as they
// come.
func (d *driver) awaitResult(ctx context.Context, tu task.ToolUse, id string) (task.StepResult, []task.ToolUse, error) {
	wait, cancel := context.WithTimeout(ctx, resultWait)
	defer cancel()
	for {
		ev, ok, err := d.nextWhile(wait)
		if err != nil {
			if ctx.Err() != nil {
				d.interrupt()
				return task.StepResult{Use: tu}, nil, ctx.Err()
			}
			return task.StepResult{Use: tu}, nil, fmt.Errorf("claude: %s did not complete", tu.Name)
		}
		if !ok {
			return task.StepResult{Use: tu}, nil, fmt.Errorf("%s", d.rpc.Err())
		}
		if ev.isRequest() {
			if !d.served(ev) {
				d.queued = append(d.queued, ev)
			}
			continue
		}
		if ev.Type == "user" {
			if res, ok := resultFor(ev, id); ok {
				// The results of other uses in the same message (a free read beside the
				// allowed call) are narrated as they would have been on their own.
				for _, b := range ev.Content {
					if b.Type == "tool_result" && b.ToolUseID != id {
						d.narrate(b)
					}
				}
				delete(d.uses, id)
				delete(d.answered, id)
				out := task.StepResult{Use: tu, Output: commandOutput(res.text())}
				if res.IsError && out.Output == "" {
					out.Output = "error"
				}
				return out, nil, nil
			}
		}
		turnEnded, err := d.handle(ev)
		if err != nil {
			return task.StepResult{Use: tu}, nil, err
		}
		if turnEnded {
			// The turn moved on without the call's result: interrupted, or Claude gave up on
			// it. Not an error of the action; handle has recorded that no turn is running,
			// which is what the next boundary reads.
			delete(d.answered, id)
			return task.StepResult{Use: tu, Output: "the turn ended before this completed"}, nil, nil
		}
	}
}

// awaitCall reads Claude until the allowed tool call arrives as `tools/call`, runs the tool,
// and answers with its result, which is the action's output.
func (d *driver) awaitCall(ctx context.Context, tu task.ToolUse, p *proposal, id string) (task.StepResult, []task.ToolUse, error) {
	wait, cancel := context.WithTimeout(ctx, resultWait)
	defer cancel()
	for {
		ev, ok, err := d.nextWhile(wait)
		if err != nil {
			if ctx.Err() != nil {
				d.interrupt()
				return task.StepResult{Use: tu}, nil, ctx.Err()
			}
			return task.StepResult{Use: tu}, nil, fmt.Errorf("claude: the call of %s did not arrive", tu.Name)
		}
		if !ok {
			return task.StepResult{Use: tu}, nil, fmt.Errorf("%s", d.rpc.Err())
		}
		if ev.isRequest() {
			if ev.Req.Subtype == "mcp_message" {
				if m, err := ev.Req.mcp(); err == nil && m.Method == "tools/call" {
					call, err := decodeCall(m.Params)
					if err == nil && call.Meta.ToolUseID == id {
						// Run with the input the gate decided over (the permission request's,
						// which the allow handed back), not whatever the call now carries.
						use := task.ToolUse{ID: tu.ID, Name: p.tool.Name, Input: p.ev.Req.Input, Decision: tu.Decision}
						out, blocked, err := p.tool.Do(ctx, use)
						if err != nil {
							out = "error: " + err.Error()
						}
						if rerr := d.rpc.MCPRespond(ev.RequestID, m, mcpCallResult(err == nil && !blocked, out)); rerr != nil {
							return task.StepResult{Use: tu}, nil, rerr
						}
						return task.StepResult{Use: tu, Output: out, Blocked: blocked}, nil, nil
					}
				}
			}
			if !d.served(ev) {
				d.queued = append(d.queued, ev)
			}
			continue
		}
		if ev.Type == "user" {
			if res, ok := resultFor(ev, id); ok {
				// The use has its result and no call came: Claude failed it on its own side.
				for _, b := range ev.Content {
					if b.Type == "tool_result" && b.ToolUseID != id {
						d.narrate(b)
					}
				}
				delete(d.uses, id)
				delete(d.answered, id)
				out := "the call did not reach the runner"
				if why := firstLine(res.text()); why != "" {
					out += ": " + why
				}
				return task.StepResult{Use: tu, Output: out}, nil, nil
			}
		}
		turnEnded, err := d.handle(ev)
		if err != nil {
			return task.StepResult{Use: tu}, nil, err
		}
		if turnEnded {
			delete(d.answered, id)
			return task.StepResult{Use: tu, Output: "the turn ended before this was called"}, nil, nil
		}
	}
}

// nextWhile is one event from the stream, bounded by wait rather than interrupting on it: the
// caller decides what a wait that ran out means. The stream alone, never the queue: what the
// executor sets aside is for Next, and reading it back here would hand it straight back.
func (d *driver) nextWhile(wait context.Context) (event, bool, error) {
	select {
	case ev, ok := <-d.rpc.Events:
		return ev, ok, nil
	case <-wait.Done():
		return event{}, false, wait.Err()
	}
}

// resultFor is the tool result for id in a user message, if it carries one.
func resultFor(ev event, id string) (block, bool) {
	for _, b := range ev.Content {
		if b.Type == "tool_result" && b.ToolUseID == id {
			return b, true
		}
	}
	return block{}, false
}

// commandOutput is what a call printed, bounded: its last 4000 bytes or fewer, from a rune's
// start.
func commandOutput(out string) string {
	out = strings.TrimSpace(out)
	const keep = 4000
	if len(out) > keep {
		i := len(out) - keep
		for i < len(out) && !utf8.RuneStart(out[i]) {
			i++
		}
		out = "…" + out[i:]
	}
	return out
}

// question is one of AskUserQuestion's questions, in the fields this side reads.
type question struct {
	Question string // as Claude wrote it, which is what its answer is keyed by
	Header   string
	Options  []task.Option
	Multi    bool // more than one option may apply
}

// questionsOf reads AskUserQuestion's input, which arrives as a document.
func questionsOf(in map[string]any) []question {
	raw, _ := in["questions"].([]any)
	var out []question
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		q := question{Question: str(m, "question"), Header: str(m, "header")}
		q.Multi, _ = m["multiSelect"].(bool)
		opts, _ := m["options"].([]any)
		for _, o := range opts {
			om, ok := o.(map[string]any)
			if !ok {
				continue
			}
			if label := strings.TrimSpace(str(om, "label")); label != "" {
				q.Options = append(q.Options, task.Option{ID: label, Says: str(om, "description")})
			}
		}
		out = append(out, q)
	}
	return out
}

// askPerson hands the turn to the person this task runs for, as the harness's `question-ask`
// tool does: the same question record, on the same docket, answered from the same places. The
// answer is what Claude reads.
func (d *driver) askPerson(ctx context.Context, tu task.ToolUse, id string, cq question) (string, error) {
	// Claude's questions always take an answer in words beside their options.
	opts := append(slices.Clone(cq.Options), task.Option{ID: task.Reply, Says: "answer in words"})
	question := cq.Question
	if cq.Header != "" && !strings.Contains(question, cq.Header) {
		question = cq.Header + ": " + question
	}
	if cq.Multi {
		// The docket takes one choice; more than one is said in words.
		question += " (several may apply: answer in words to name more than one)"
	}
	origin, _, _ := strings.Cut(tu.ID, task.GateSep)
	q := task.GateQuestion{
		ID: id, Origin: origin, Gate: kindAsk,
		Reason: question, Options: opts,
		Answerers: []string{task.PrincipalAnswerer},
	}
	res, err := d.resolver.Resolve(ctx, q, tu)
	if err != nil {
		return "", err
	}
	words := strings.TrimSpace(strings.Join(res.Steer, "\n"))
	switch {
	case res.Choice == "" || res.Choice == task.Reply:
		return words, nil
	case words != "":
		return res.Choice + ": " + words, nil
	default:
		return res.Choice, nil
	}
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

// truncate is s's first n bytes or fewer, ending where a rune does.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
