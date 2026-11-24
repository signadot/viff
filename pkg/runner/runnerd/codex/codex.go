// Package codex is the codex agent: OpenAI's Codex as a viff task, with every command and every
// patch it proposes decided by the task's autonomy and shown on the read plane. A spawn asks for it
// with `doer: {agent: codex}`, and a runner that hosts it builds its task with Task.
//
// It is an agent of its own beside viff's (package viff), and it exists because of what the
// other way of running a foreign agent gives up. A program the runner runs and reads the output
// of is gated once, at the invocation, and nothing inside it is seen; the read plane is silent
// until the program exits. That is the honest shape for a program that offers nothing else.
// Codex offers something else.
//
// `codex app-server` is a protocol, not a print-out. A client starts a thread and a turn, and
// codex then ASKS THE CLIENT before it runs a command or applies a patch, asks it the questions
// the model has for a person, and streams each item it starts and completes. A running turn
// takes more input, and can be interrupted. So the decision points viff wants are already
// there, and this package's job is to put each of them in front of the loop as an ordinary
// action:
//
//   - Next waits on codex. Each approval codex requests is the next proposed action, with the
//     steps it is derived here — `commands.run` for a command, `files.write` for each path of
//     a patch. A host a command asks to reach is no step: what the task reaches is its
//     environment's `network`, built around codex's process, whatever codex is told. A request that is several
//     steps goes ahead only where each of them would (stepsOf). The supervisor
//     gates it exactly as it gates the viff agent's `run_bash` or `write_file`: the autonomy
//     decides, a blocked question goes to the docket, `viff question` answers it.
//     A question codex has for a person is the step `question.ask`, as the `question-ask` tool's use is,
//     and once the autonomy lets it go it is put to them.
//     The gate model gains nothing new.
//   - Execute answers codex. Proceed is `accept`, and the executor then waits for the item to
//     complete so the loop's exec frame carries what the command printed and how it exited. A
//     refused gate is `decline`, and codex is told why through the turn, so the model plans
//     around it rather than retrying it.
//   - Approval policy `untrusted` makes codex ask about every command it does not know to be
//     read-only and every patch. Under an autonomy that allows them viff answers yes at once, and the
//     run is unattended but narrated and on the record, action by action.
//
// The runner's own tools are offered to codex as its own (`dynamicTools` on `thread/start`, in
// codex's experimental API), and a call codex makes of one (`item/tool/call`) is an action of
// the loop like an approval is: the tool's own step classifies it, the autonomy decides
// it, its Do performs it in this process, and the result is the call's output. `git-push` is the first; it is
// how codex delivers, since there is no finisher doing it for an agent that can decide.
//
// What the spawn's doer runs with reaches codex: `with.model` is the model of the thread and
// `with.effort` the reasoning effort of its turns; one the spawn names none of is the runner's
// configured default, filled in before the launch reaches here (package stock), or codex's own.
//
// What codex does WITHOUT asking is the part that is attested rather than enforced: the
// commands its own autonomy trusts as read-only (listing, reading, searching), and its web
// search. Those are narrated when they complete, so the read plane shows them, but no gate saw
// them first. The viff agent has the same shape for its reads, which are no step; here the set
// of free reads is codex's to define, and this comment is where that is said.
//
// Codex's own sandbox runs inside the runner's containment. The two compose: the sandbox this
// process was started in bounds what codex can reach at all, and codex's sandbox bounds what an
// approved command may touch within it.
//
// The wire lives in rpc.go alone and is on the json allowlist (scripts/check-json.sh); the
// message shapes were generated from codex-cli 0.162.0.
package codex

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.viff.dev/viff/pkg/answer"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd/viff"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// Agent is the agent's name: what a spawn writes in `doer: {agent: …}`.
const Agent = "codex"

// ModelHost is where codex calls its model with an API key: what a runner lets its process
// reach whatever the task's network.
const ModelHost = "api.openai.com"

// EnvAPIKey is the variable the runner reads the OpenAI API key from. It sends the value to the
// task process (Creds.Key), which writes it as a login file in a private CODEX_HOME for the
// codex process and nothing else (see driver.start). Unset, codex's own login (`codex login`,
// under the HOME the environment passes through) is what authenticates it.
const EnvAPIKey = "OPENAI_API_KEY"

// Config is how this runner runs Codex: the program keys of `agents.codex`. It holds no
// secret.
type Config struct {
	// Bin is the program, `codex` on PATH by default. The runner runs `<bin> app-server`.
	Bin string
	// Sandbox is codex's own sandbox for approved commands: `read-only`, `workspace-write`
	// (the default) or `danger-full-access`. It is what an APPROVED command runs under, and it
	// is why approving a command is not the same as approving anything.
	Sandbox string
	// Config is extra codex configuration, as `-c key=value` overrides; the values are TOML.
	Config map[string]string
}

// sandboxes is what Sandbox may say.
var sandboxes = []string{"read-only", "workspace-write", "danger-full-access"}

const defaultSandbox = "workspace-write"

// Validate refuses a section that codex would refuse later, less clearly.
func (c *Config) Validate() error {
	if c.Sandbox != "" && !slices.Contains(sandboxes, c.Sandbox) {
		return fmt.Errorf("sandbox %q is not one of %s", c.Sandbox, strings.Join(sandboxes, ", "))
	}
	return nil
}

// Program is the program the runner runs.
func (c *Config) Program() string { return cmp.Or(c.Bin, "codex") }

// Key reads the API key from this process's environment: the runner's, where the deployment put
// it. "" when it is not set.
func Key() string { return os.Getenv(EnvAPIKey) }

// Creds is the section with its key read: what a task process runs codex with.
func (c *Config) Creds(key string) Creds {
	return Creds{Bin: c.Program(), Sandbox: cmp.Or(c.Sandbox, defaultSandbox), Key: key, Config: c.Config}
}

// MissingKey reports why codex may not be able to authenticate here, or "". A warning rather
// than a refusal: without a key, codex may still be logged in under the HOME it is given, and
// only a turn can tell.
func MissingKey(getenv func(string) string) string {
	if getenv(EnvAPIKey) == "" {
		return EnvAPIKey + " is not set in this runner's environment"
	}
	return ""
}

// Creds is the resolved codex configuration: the section's values and the key read.
type Creds struct {
	Bin     string
	Sandbox string
	Key     string
	Config  map[string]string
}

// argv is the codex process to start.
func (c Creds) argv() []string {
	out := []string{cmp.Or(c.Bin, "codex"), "app-server"}
	keys := make([]string, 0, len(c.Config))
	for k := range c.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, "-c", k+"="+c.Config[k])
	}
	return out
}

// The names an action proposed by this agent carries. They are codex's words for what it is
// about to do, which is what a person reading `viff question` or `attach` should see.
const (
	kindShell       = "shell"
	kindPatch       = "apply_patch"
	kindAsk         = "ask"
	kindPermissions = "request_permissions"
	// kindTool is a proposal's kind for a call of one of the runner's tools; the action's name
	// is the tool's own.
	kindTool = "tool"
)

// Task builds a task that runs Codex over its app-server protocol in dir, with env as the codex
// process's environment (the runner's allowlist, the launch's `with.env`, and where the task's
// credential file is, for a task that has one), offering it tools (the runner's) as its own.
// A launch whose autonomy does not name codex's shell and edits is refused: they are how it works.
func Task(l runner.Launch, reg *task.Registry, creds Creds, env []string, dir string, tools []viff.Tool, ceilings map[string]systems.Ceiling) (*task.Task, error) {
	var contract *answer.Contract
	if l.Answer != nil {
		c, err := answer.Parse(l.Answer)
		if err != nil {
			return nil, fmt.Errorf("answer contract: %w", err)
		}
		contract = c
	}
	// Codex's shell and its edits are its own: it can't run without them, which its offer says
	// (Requires), so a launch whose environment gives none of one is refused where it is written
	// (runnerd.CheckOffer), at install and again by the runner, not here. A handler with no
	// offers checks nothing, and there such a task runs to fail at its first action.
	d := &driver{
		creds: creds, prompt: l.Prompt, dir: dir,
		model: l.AgentSettings().Model(), effort: l.AgentSettings().Settings["effort"],
		env:      env,
		resolver: reg,
		log:      os.Stderr,
		tools:    tools,
		toolBy:   map[string]viff.Tool{},
	}
	for _, t := range tools {
		d.toolBy[t.Name] = t
	}
	if contract != nil {
		d.contractSrc = contract.Source()
	}
	d.toolStep = viff.StepOf(tools)
	sup := task.NewSupervisor(l.Environment, l.Autonomy, reg)
	sup.SetCeilings(ceilings)
	sup.SetStepsOf(d.stepsOf)
	t := task.NewTask(l.ID, sup, d, &executor{d: d})
	// What it runs with: the record's to say. A setting the launch names none of is Codex's own,
	// which it reports only to its log.
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

// driver is one task's conversation with one codex process: the loop's task.Driver.
type driver struct {
	creds       Creds
	prompt      string
	contractSrc string
	// model and effort are the launch's agent's, the runner's defaults filled in; "" leaves each
	// to codex.
	model, effort string
	dir           string
	realDir       string // dir with its symlinks resolved, which is how codex reports paths under it
	env           []string
	resolver      task.Resolver
	log           io.Writer
	emit          func(task.Event)
	// tools are the runner's, offered to codex on thread/start and run here when it calls one.
	tools  []viff.Tool
	toolBy map[string]viff.Tool
	// toolStep is the step a call of one of the tools is, as the viff agent's is classified.
	toolStep func(task.ToolUse) (task.Step, bool)

	rpc      *rpc
	home     string // the private CODEX_HOME this task made for codex, when it made one
	threadID string
	turnID   string // the active turn; "" between turns
	started  bool
	closed   bool

	// items are the items codex has started and not completed, by id. A patch's approval
	// request names an item and not its changes, so the changes are read from here.
	items map[string]item
	// approved are the items this side answered an approval for, so their completion is the
	// loop's exec frame and not narrated a second time.
	approved map[string]bool
	// queued are requests codex made while the executor was waiting on an item, held for Next.
	queued []event

	// proposed is the request Next last put in front of the loop, until Execute answers it or
	// Next learns it was refused. Atomic: the supervisor reads it (stepsOf) on the gate's own
	// goroutine, which an intervention can leave behind while the next Next runs.
	proposed atomic.Pointer[proposal]

	last string // the agent's last message: this task's result

	mu      sync.Mutex
	folding []string
}

// proposal is one request from codex, as the loop sees it.
type proposal struct {
	ev     event
	kind   string // kindShell | kindPatch | kindAsk | kindPermissions | kindTool
	itemID string
	cmd    commandApproval
	ui     userInput
	perm   permissionsRequest
	call   toolCall
	tool   viff.Tool
	// steps are what the request is, as the spec names a task's steps: a command is
	// `commands.run`, a patch `files.write` for each path it touches. None is no step.
	steps []task.Step
}

// stepsOf is the supervisor's (Supervisor.SetStepsOf): the steps the proposed request is, each
// of which it decides, so a patch goes ahead only where every write in it would. None is no
// step.
func (d *driver) stepsOf(task.ToolUse) []task.Step {
	if p := d.proposed.Load(); p != nil {
		return p.steps
	}
	return nil
}

// itemWait bounds how long the executor waits for an item codex said it would run before
// treating the turn as having moved on without it.
var itemWait = 24 * time.Hour

// Next implements task.Driver: what codex wants to do next, as an action.
func (d *driver) Next(ctx context.Context, prev task.StepResult) (task.ToolUse, bool, error) {
	if p := d.proposed.Swap(nil); p != nil {
		// The loop decided about the last proposal without executing it.
		switch {
		case prev.Err != nil:
			return task.ToolUse{}, false, prev.Err
		case prev.Blocked:
			d.decline(ctx, p, prev.Note)
		}
	} else if prev.Err != nil && d.started {
		return task.ToolUse{}, false, prev.Err
	}
	if !d.started {
		d.started = true
		if err := d.start(ctx); err != nil {
			return task.ToolUse{}, false, err
		}
	} else if d.turnID == "" {
		// Between turns: the loop has something more to say, or nothing. A conversation's
		// reply, a contract repair and a steer all arrive here through Fold.
		msg := d.drain()
		if msg == "" {
			return task.ToolUse{}, false, nil
		}
		if err := d.startTurn(ctx, msg); err != nil {
			return task.ToolUse{}, false, err
		}
	} else if msg := d.drain(); msg != "" {
		// Mid-turn: words taken at this boundary reach the running turn.
		d.steer(ctx, msg)
	}
	return d.await(ctx)
}

// start brings the process up, opens the thread and starts the first turn.
func (d *driver) start(ctx context.Context) error {
	env := append([]string{}, d.env...)
	if d.creds.Key != "" {
		// Codex does not read a key from its environment; it reads the login file that
		// `codex login --with-api-key` writes under CODEX_HOME. So the task writes that file
		// itself, into a CODEX_HOME of its own that dies with the task, and points codex at it.
		// The key is then in no environment, but the file is in the sandbox's /tmp, as the
		// task's user: a command codex runs can read it, and once read it leaves however the
		// task's output does: over the run's network, and, whatever the network, in what the
		// task hands back (its messages, a question, a push).
		home, err := writeLogin(d.creds.Key)
		if err != nil {
			return err
		}
		d.home = home
		env = append(env, "CODEX_HOME="+home)
	}
	// The process outlives any one ctx the loop hands Next: it is closed by Close, when the
	// run ends, and not when a boundary's context does.
	rpc, err := startServer(context.Background(), d.creds.argv(), d.dir, env)
	if err != nil {
		return err
	}
	d.rpc = rpc
	d.items = map[string]item{}
	d.approved = map[string]bool{}
	if err := rpc.CallInto(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "viff-runner", "title": "viff", "version": "0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, nil); err != nil {
		return d.fail(err)
	}
	if err := rpc.Notify("initialized", nil); err != nil {
		return d.fail(err)
	}
	params := map[string]any{
		"cwd":            d.dir,
		"approvalPolicy": "untrusted",
		"sandbox":        cmp.Or(d.creds.Sandbox, defaultSandbox),
		// Approvals come to this side and to nothing else: codex can route them to a reviewing
		// subagent of its own, which would be a second decider the autonomy never sees.
		"approvalsReviewer": "user",
		"ephemeral":         true,
	}
	if d.model != "" {
		params["model"] = d.model
	}
	if len(d.tools) > 0 {
		specs := make([]map[string]any, 0, len(d.tools))
		for _, t := range d.tools {
			specs = append(specs, dynamicToolSpec(t.Name, t.Description, t.Params, t.Required))
		}
		params["dynamicTools"] = specs
	}
	var th threadStart
	if err := rpc.CallInto(ctx, "thread/start", params, &th); err != nil {
		return d.fail(err)
	}
	if th.Thread.ID == "" {
		return d.fail(errors.New("codex: thread/start answered with no thread id"))
	}
	d.threadID = th.Thread.ID
	d.logf("codex %s, thread %s, model %s", d.creds.argv()[0], d.threadID, cmp.Or(th.Model, "default"))
	return d.startTurn(ctx, d.opening())
}

// fail wraps a startup error with what codex said on stderr, which is where a missing binary
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

// startTurn starts a turn with text. The spawn's model and effort ride every turn, where codex's
// protocol takes them for this turn and the ones after it.
func (d *driver) startTurn(ctx context.Context, text string) error {
	params := map[string]any{
		"threadId": d.threadID,
		"input":    []map[string]any{{"type": "text", "text": text}},
	}
	if d.model != "" {
		params["model"] = d.model
	}
	if d.effort != "" {
		params["effort"] = d.effort
	}
	var res turnStarted
	if err := d.rpc.CallInto(ctx, "turn/start", params, &res); err != nil {
		return d.fail(err)
	}
	d.turnID = res.Turn.ID
	return nil
}

// steer puts words into the running turn. Best effort: a turn that will not take them
// (codex is compacting, say) is logged, and the words are not lost — they were already said
// on the read plane, and the next turn starts with whatever is still folding.
func (d *driver) steer(ctx context.Context, text string) {
	if d.turnID == "" {
		d.Fold([]string{text})
		return
	}
	err := d.rpc.CallInto(ctx, "turn/steer", map[string]any{
		"threadId":       d.threadID,
		"expectedTurnId": d.turnID,
		"input":          []map[string]any{{"type": "text", "text": text}},
	}, nil)
	if err != nil {
		d.logf("steer not taken by the running turn: %v", err)
		d.Fold([]string{text})
	}
}

// await reads codex until it asks for something the loop must decide, or the turn ends.
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
			p, tu, err := d.propose(ev)
			if err != nil {
				// A request this side does not understand is refused, so codex hears an
				// answer rather than waiting forever on one.
				d.logf("refusing %s: %v", ev.Method, err)
				_ = d.rpc.RespondError(ev.ID, -32601, err.Error())
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
				if err := d.startTurn(ctx, msg); err != nil {
					return task.ToolUse{}, false, err
				}
				continue
			}
			return task.ToolUse{}, false, nil
		}
	}
}

// next is one event from codex: what the executor set aside first, then the stream. ok is
// false when the stream has ended. A ctx that ends interrupts the turn, which is what cancel
// means to a running codex.
func (d *driver) next(ctx context.Context) (event, bool, error) {
	if len(d.queued) > 0 {
		ev := d.queued[0]
		d.queued = d.queued[1:]
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

// propose turns a request from codex into the action the loop will gate.
func (d *driver) propose(ev event) (*proposal, task.ToolUse, error) {
	p := &proposal{ev: ev}
	switch ev.Method {
	case "item/commandExecution/requestApproval":
		if err := ev.Decode(&p.cmd); err != nil {
			return nil, task.ToolUse{}, err
		}
		p.kind, p.itemID = kindShell, p.cmd.ItemID
		cmd := p.cmd.commandText()
		in := map[string]any{"command": cmd}
		if p.cmd.Cwd != nil && *p.cmd.Cwd != "" {
			in["cwd"] = d.rel(*p.cmd.Cwd)
		}
		if p.cmd.Reason != nil && *p.cmd.Reason != "" {
			in["reason"] = *p.cmd.Reason
		}
		if p.cmd.Kind == "writeStdin" {
			in["kind"] = "stdin"
		}
		p.steps = []task.Step{task.NewStep(task.StepCommandsRun, map[string]string{"command": cmd})}
		return p, task.ToolUse{Name: kindShell, Input: in}, nil

	case "item/fileChange/requestApproval":
		var fc fileChangeApproval
		if err := ev.Decode(&fc); err != nil {
			return nil, task.ToolUse{}, err
		}
		p.kind, p.itemID = kindPatch, fc.ItemID
		in := map[string]any{}
		if fc.Reason != nil && *fc.Reason != "" {
			in["reason"] = *fc.Reason
		}
		// []any rather than a typed slice: the input is read as a document, by ToolUse.Describe
		// among others, and a document's list is []any.
		var changes []any
		write := func(path string) {
			p.steps = append(p.steps, task.NewStep(task.StepFilesWrite, map[string]string{"path": path}))
		}
		for _, ch := range d.items[fc.ItemID].Changes {
			path := d.rel(ch.Path)
			changes = append(changes, map[string]any{"kind": ch.Kind.Type, "path": path})
			// Adding, changing and deleting a file are each a write of it, and a move writes
			// where it goes as well.
			write(path)
			if ch.Kind.MovePath != "" {
				write(d.rel(ch.Kind.MovePath))
			}
		}
		if len(p.steps) == 0 {
			// Codex asked about a patch whose item this side never saw. Still a write, and gated
			// as one; what it touches is unknown, which the gate's reason will say.
			root := ""
			if fc.GrantRoot != nil {
				root = d.rel(*fc.GrantRoot)
			}
			write(root)
		}
		in["changes"] = changes
		return p, task.ToolUse{Name: kindPatch, Input: in}, nil

	case "item/tool/requestUserInput":
		if err := ev.Decode(&p.ui); err != nil {
			return nil, task.ToolUse{}, err
		}
		p.kind, p.itemID = kindAsk, p.ui.ItemID
		var qs []string
		for _, q := range p.ui.Questions {
			qs = append(qs, q.Question)
		}
		// The step `question.ask`, as the `question-ask` tool's use is: decided by the autonomy,
		// then put to the person.
		p.steps = []task.Step{task.NewStep(task.StepQuestionAsk, map[string]string{})}
		return p, task.ToolUse{Name: kindAsk, Input: map[string]any{"questions": qs}}, nil

	case "item/tool/call":
		tc, err := decodeToolCall(ev)
		if err != nil {
			return nil, task.ToolUse{}, err
		}
		t, ok := d.toolBy[tc.Tool]
		if !ok {
			return nil, task.ToolUse{}, fmt.Errorf("codex called a tool this task does not offer: %q", tc.Tool)
		}
		p.kind, p.itemID, p.call, p.tool = kindTool, tc.CallID, tc, t
		tu := task.ToolUse{Name: t.Name, Input: tc.Arguments}
		// The tool's own step, as the viff agent's call of it is classified: a call that is no
		// step (a branch the spawn never permitted) is refused by its Do, so nobody is asked
		// about something that cannot happen.
		if st, ok := d.toolStep(tu); ok {
			p.steps = []task.Step{st}
		}
		return p, tu, nil

	case "item/permissions/requestApproval":
		perm, err := decodePermissions(ev)
		if err != nil {
			return nil, task.ToolUse{}, err
		}
		p.kind, p.itemID, p.perm = kindPermissions, perm.ItemID, perm
		in := map[string]any{}
		if perm.Reason != nil && *perm.Reason != "" {
			in["reason"] = *perm.Reason
		}
		// named is whether the request asks for something this side knows to be no step: a read,
		// or the network, which is the task's, built around codex's process whatever codex is told.
		named := false
		if n := perm.Permissions.Network; n != nil && n.Enabled != nil && *n.Enabled {
			named = true
			in["network"] = true
		}
		if fs := perm.Permissions.FileSystem; fs != nil {
			var paths []string
			for _, e := range fs.Entries {
				ref := d.rel(cmp.Or(e.Path.Path, e.Path.Pattern))
				// A write, or an access this side does not read, which the answer would grant
				// whole: each is gated as a write.
				if e.Access != "read" && e.Access != "deny" {
					p.steps = append(p.steps, task.NewStep(task.StepFilesWrite, map[string]string{"path": ref}))
				}
				if e.Access != "deny" {
					named = true
					paths = append(paths, e.Access+" "+ref)
				}
			}
			for _, r := range fs.Read {
				// Reading is no step, as the viff agent's reads are not.
				named = true
				paths = append(paths, "read "+d.rel(r))
			}
			for _, w := range fs.Write {
				p.steps = append(p.steps, task.NewStep(task.StepFilesWrite, map[string]string{"path": d.rel(w)}))
				paths = append(paths, "write "+d.rel(w))
			}
			in["paths"] = paths
		}
		if len(perm.Other) > 0 {
			in["other"] = perm.Other
		}
		if (len(p.steps) == 0 && !named) || len(perm.Other) > 0 {
			// A request for what this side cannot name is still a widening, and the answer grants
			// the request whole: gate it as a write.
			p.steps = append(p.steps, task.NewStep(task.StepFilesWrite, map[string]string{"path": ""}))
		}
		return p, task.ToolUse{Name: kindPermissions, Input: in}, nil
	}
	return nil, task.ToolUse{}, fmt.Errorf("a request this side does not handle: %s", ev.Method)
}

// decline answers a request the loop refused, and tells the model why.
func (d *driver) decline(ctx context.Context, p *proposal, note string) {
	switch p.kind {
	case kindShell, kindPatch:
		_ = d.rpc.Respond(p.ev.ID, map[string]any{"decision": "decline"})
	case kindPermissions:
		_ = d.rpc.Respond(p.ev.ID, permissionsAnswer(p.perm, false))
	case kindTool:
		// The refusal is the call's result, so the model reads it where it asked; no steer.
		why := "viff refused this call of " + p.tool.Name
		if note != "" {
			why += ": " + note
		}
		_ = d.rpc.Respond(p.ev.ID, toolCallAnswer(false, why+". Do not retry it; plan around it."))
		return
	case kindAsk:
		// The autonomy refused the question, or the loop cancelled it: the model gets no answers.
		_ = d.rpc.Respond(p.ev.ID, map[string]any{"answers": map[string]any{}})
	}
	why := "viff refused this " + p.kind
	if note != "" {
		why += ": " + note
	}
	why += ". Do not retry it; plan around it."
	d.steer(ctx, why)
}

// handle narrates a notification and tracks the turn. It reports whether the turn ended.
func (d *driver) handle(ev event) (bool, error) {
	switch ev.Method {
	case "turn/started":
		var t turnStarted
		if err := ev.Decode(&t); err == nil && t.Turn.ID != "" {
			d.turnID = t.Turn.ID
		}
	case "turn/completed":
		var t turnCompleted
		if err := ev.Decode(&t); err != nil {
			return false, err
		}
		d.turnID = ""
		if t.Turn.Error != nil && t.Turn.Error.Message != "" {
			return true, fmt.Errorf("codex: the turn failed: %s", t.Turn.Error.Message)
		}
		if t.Turn.Status == "failed" {
			return true, errors.New("codex: the turn failed")
		}
		return true, nil
	case "item/started":
		var it itemEvent
		if err := ev.Decode(&it); err == nil && it.Item.ID != "" {
			d.items[it.Item.ID] = it.Item
		}
	case "item/completed":
		var it itemEvent
		if err := ev.Decode(&it); err != nil {
			return false, nil
		}
		delete(d.items, it.Item.ID)
		d.narrate(it.Item)
	case "error":
		d.logf("codex error: %s", firstLine(string(ev.Params)))
	}
	return false, nil
}

// narrate puts a completed item on the read plane. The agent's words are the task's own
// narration; everything codex did without asking is reported as the action it was, so an
// observer sees the free reads too.
func (d *driver) narrate(it item) {
	if it.Type == "agentMessage" && it.Text != "" {
		// The result, whether or not anybody is watching; under the lock, since a snapshot
		// reads it while the run goes on.
		d.mu.Lock()
		d.last = it.Text
		d.mu.Unlock()
	}
	if d.emit == nil {
		return
	}
	switch it.Type {
	case "agentMessage":
		if it.Text != "" {
			d.emit(task.Event{Kind: task.EventSay, Text: it.Text})
		}
	case "commandExecution":
		if d.approved[it.ID] {
			return
		}
		d.emit(task.Event{Kind: task.EventExec, Tool: kindShell, Text: commandSummary(it)})
	case "fileChange":
		if d.approved[it.ID] {
			return
		}
		d.emit(task.Event{Kind: task.EventExec, Tool: kindPatch, Text: patchSummary(d, it)})
	case "webSearch":
		d.emit(task.Event{Kind: task.EventExec, Tool: "web_search", Text: it.Query})
	case "mcpToolCall":
		name := it.Tool
		if it.Server != "" {
			name = it.Server + "/" + it.Tool
		}
		d.emit(task.Event{Kind: task.EventExec, Tool: name, Text: it.Status})
	case "dynamicToolCall":
		// One of this side's own tools: the loop narrated it as the action it was.
	}
}

// commandSummary is a command's one-line account: what ran, how it exited, what it printed last.
func commandSummary(it item) string {
	s := it.Command
	if it.ExitCode != nil {
		s += fmt.Sprintf(" → exit %d", *it.ExitCode)
	} else if it.Status != "" && it.Status != "completed" {
		s += " → " + it.Status
	}
	if out := strings.TrimSpace(it.AggregatedOutput); out != "" {
		s += ": " + lastLine(out)
	}
	return s
}

// patchSummary names what a patch touched.
func patchSummary(d *driver, it item) string {
	var paths []string
	for _, ch := range it.Changes {
		paths = append(paths, ch.Kind.Type+" "+d.rel(ch.Path))
	}
	s := strings.Join(paths, ", ")
	if it.Status != "" && it.Status != "completed" {
		s += " → " + it.Status
	}
	return s
}

// rel is a path as the workspace sees it, where codex reports it absolutely. Codex reports
// the workspace with its symlinks resolved, so both spellings of the root are tried.
func (d *driver) rel(p string) string {
	if p == "" || !filepath.IsAbs(p) || d.dir == "" {
		return p
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
// stop asking, and codex should stop doing.
func (d *driver) interrupt() {
	if d.rpc == nil || d.turnID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = d.rpc.CallInto(ctx, "turn/interrupt", map[string]any{"threadId": d.threadID, "turnId": d.turnID}, nil)
}

// Close ends the codex process, and the login it was given. The task's own end is what calls it
// (task.Closer).
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

// writeLogin makes a CODEX_HOME holding an API-key login, as `codex login --with-api-key`
// would have written it, and returns the directory. Readable by this user alone.
func writeLogin(key string) (string, error) {
	home, err := os.MkdirTemp("", "codex-home-")
	if err != nil {
		return "", fmt.Errorf("codex: a home for the login: %w", err)
	}
	// The one document this file writes in codex's format. Quoted rather than encoded, since
	// a key is a token without a character that quoting would mishandle, and this keeps the
	// JSON codec where codexrpc.go keeps it.
	doc := fmt.Sprintf("{\"auth_mode\":\"apikey\",\"OPENAI_API_KEY\":%s}\n", strconv.Quote(key))
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(doc), 0o600); err != nil {
		_ = os.RemoveAll(home)
		return "", fmt.Errorf("codex: writing the login: %w", err)
	}
	return home, nil
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
		fmt.Fprintf(d.log, "codex: "+format+"\n", args...)
	}
}

// executor performs an approved action: it answers codex, and waits for what follows.
type executor struct{ d *driver }

func (e *executor) Execute(ctx context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	d := e.d
	p := d.proposed.Swap(nil)
	if p == nil {
		return task.StepResult{Use: tu}, nil, errors.New("codex: nothing proposed to execute")
	}
	switch p.kind {
	case kindShell, kindPatch:
		if err := d.rpc.Respond(p.ev.ID, map[string]any{"decision": "accept"}); err != nil {
			return task.StepResult{Use: tu}, nil, err
		}
		if p.kind == kindShell && p.cmd.Kind == "writeStdin" {
			// Input to a running command: there is no item to wait for, and the command's
			// own completion is still worth narrating.
			return task.StepResult{Use: tu, Output: "sent"}, nil, nil
		}
		d.approved[p.itemID] = true
		return d.awaitItem(ctx, tu, p.itemID)

	case kindAsk:
		answers := map[string]any{}
		var said []string
		for _, q := range p.ui.Questions {
			text, err := d.askPerson(ctx, tu, q.Question, q.Header, q.IsOther, optionsOf(q.Options))
			if err != nil {
				return task.StepResult{Use: tu}, nil, err
			}
			answers[q.ID] = map[string]any{"answers": []string{text}}
			said = append(said, text)
		}
		if err := d.rpc.Respond(p.ev.ID, map[string]any{"answers": answers}); err != nil {
			return task.StepResult{Use: tu}, nil, err
		}
		return task.StepResult{Use: tu, Output: strings.Join(said, "\n")}, nil, nil

	case kindPermissions:
		if err := d.rpc.Respond(p.ev.ID, permissionsAnswer(p.perm, true)); err != nil {
			return task.StepResult{Use: tu}, nil, err
		}
		return task.StepResult{Use: tu, Output: "granted for this turn"}, nil, nil

	case kindTool:
		// The tool's own Do, in this process, with the result handed back as the call's output.
		// A refusal by the far side (blocked) and a failure are both words the model reads;
		// neither ends the run, which is the harness's reading of a tool that said no.
		out, blocked, err := p.tool.Do(ctx, tu)
		if err != nil {
			out = "error: " + err.Error()
		}
		if rerr := d.rpc.Respond(p.ev.ID, toolCallAnswer(err == nil && !blocked, out)); rerr != nil {
			return task.StepResult{Use: tu}, nil, rerr
		}
		return task.StepResult{Use: tu, Output: out, Blocked: blocked}, nil, nil
	}
	return task.StepResult{Use: tu}, nil, fmt.Errorf("codex: unknown proposal %q", p.kind)
}

// awaitItem reads codex until the approved item completes, so the exec frame reports what it
// did. Requests codex makes meanwhile are kept for Next; notifications are narrated as they
// come.
func (d *driver) awaitItem(ctx context.Context, tu task.ToolUse, itemID string) (task.StepResult, []task.ToolUse, error) {
	wait, cancel := context.WithTimeout(ctx, itemWait)
	defer cancel()
	for {
		var ev event
		var ok bool
		select {
		case ev, ok = <-d.rpc.Events:
		case <-wait.Done():
			if ctx.Err() != nil {
				d.interrupt()
				return task.StepResult{Use: tu}, nil, ctx.Err()
			}
			return task.StepResult{Use: tu}, nil, fmt.Errorf("codex: %s did not complete", tu.Name)
		}
		if !ok {
			return task.StepResult{Use: tu}, nil, fmt.Errorf("%s", d.rpc.Err())
		}
		if ev.isRequest() {
			d.queued = append(d.queued, ev)
			continue
		}
		if ev.Method == "item/completed" {
			var it itemEvent
			if err := ev.Decode(&it); err == nil && it.Item.ID == itemID {
				delete(d.items, itemID)
				delete(d.approved, itemID)
				out := task.StepResult{Use: tu}
				switch it.Item.Type {
				case "commandExecution":
					out.Output = commandOutput(it.Item)
					out.Exit = it.Item.ExitCode
				default:
					out.Output = patchSummary(d, it.Item)
				}
				if it.Item.Status == "failed" {
					out.Output = cmp.Or(out.Output, it.Item.Status)
				}
				return out, nil, nil
			}
		}
		turnEnded, err := d.handle(ev)
		if err != nil {
			return task.StepResult{Use: tu}, nil, err
		}
		if turnEnded {
			// The turn moved on without completing the item: interrupted, or codex gave up
			// on it. Not an error of the action; handle has recorded that no turn is running,
			// which is what the next boundary reads.
			delete(d.approved, itemID)
			return task.StepResult{Use: tu, Output: "the turn ended before this completed"}, nil, nil
		}
	}
}

// commandOutput is what a command printed, bounded, with its exit code where codex gave one.
func commandOutput(it item) string {
	out := strings.TrimSpace(it.AggregatedOutput)
	const keep = 4000
	if len(out) > keep {
		i := len(out) - keep
		for i < len(out) && !utf8.RuneStart(out[i]) {
			i++
		}
		out = "…" + out[i:]
	}
	if it.ExitCode != nil && *it.ExitCode != 0 {
		if out == "" {
			return fmt.Sprintf("exit %d", *it.ExitCode)
		}
		return fmt.Sprintf("%s\n(exit %d)", out, *it.ExitCode)
	}
	return out
}

// optionsOf renders codex's options for a question as the docket's: the label is the id, and
// the description is what choosing it means.
func optionsOf(opts []struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}) []task.Option {
	var out []task.Option
	for _, o := range opts {
		if strings.TrimSpace(o.Label) != "" {
			out = append(out, task.Option{ID: strings.TrimSpace(o.Label), Says: o.Description})
		}
	}
	return out
}

// askPerson hands the turn to the person this task runs for, as the harness's `question-ask` tool does:
// the same question record, on the same docket, answered from the same places. The answer is
// what codex reads.
func (d *driver) askPerson(ctx context.Context, tu task.ToolUse, question, header string, other bool, opts []task.Option) (string, error) {
	if other || len(opts) == 0 {
		opts = append(opts, task.Option{ID: task.Reply, Says: "answer in words"})
	}
	if header != "" && !strings.Contains(question, header) {
		question = header + ": " + question
	}
	origin, _, _ := strings.Cut(tu.ID, task.GateSep)
	q := task.GateQuestion{
		ID: tu.ID, Origin: origin, Gate: kindAsk,
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

// TakesTool is why codex cannot be offered a tool of this name, nil where it can: a dynamic
// tool's name goes to the model as a function's, bounded in length and in letters — the one
// bound every doer takes (viff.TakesToolNamed). A runtime offering tools it did not write (an
// MCP server's) asks before offering one.
func TakesTool(name string) error { return viff.TakesToolNamed("codex", name, name) }
