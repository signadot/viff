package verserunner

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/signadot/verse/runner/runnerd/verserunner/agent"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// Drivers builds the tasks the verse runner hosts, choosing the driver a launch names: `llm` is
// the verse agent (package agent), `scripted` and `calls` run without a model, and any other name
// is a program under Agents. `verse up`'s in-process runner uses it too, until that runner is
// removed, so what a test runs is what both run: the same driver choice, the same credentials
// rule.
//
// With a Workspace, the tasks run in it: the agent is briefed on its layout and on how work
// leaves it, offered the runner's own tools (Extra: push, fetch), and floored by the
// Environment's tools; and what a gate can match on is the workspace's branch, base and changes.
type Drivers struct {
	Root string   // the working directory an agent acts in
	Env  []string // what a task's shell inherits
	Log  io.Writer
	// Default is the driver of a spawn that names none.
	Default string
	// The model: an OpenAI-compatible endpoint (agent/llm.go). Key empty means no model, and an
	// `llm` spawn fails its own task rather than the engine.
	LLMBaseURL, LLMKey, LLMVirtualKey, LLMModel string
	// VirtualKeyOr is the credential a spawn's named key resolves to (config.LLMConfig).
	VirtualKeyOr func(named string) string
	// NoKey says how to give the agent a model key, to an `llm` spawn that fails without one:
	// where the key comes from depends on who hosts the runner.
	NoKey string

	// Workspace is the checkout the tasks work in, when the runner gave them one, and
	// Environment the environment it was opened for. Brief is what the agent is told about the
	// workspace's layout. Extra is the runner's own tools: push and fetch.
	Workspace   *workspace.Workspace
	Environment *Environment
	Brief       string
	Extra       []agent.Tool
	// Agents are the programs a driver may name.
	Agents map[string]Command
	// Tools is the runner's own floor (Config.Tools): what a task gets when neither its spawn
	// nor its environment names a set. Nil is everything.
	Tools []string
}

// Build is a runnerd builder.
func (h *Drivers) Build(l task.Launch, reg *task.Registry) *task.Task {
	driver := l.Driver
	if driver == "" {
		driver = h.Default
	}
	// Tools: a spawn that names none (nil) gets the floor — the environment's set, else the
	// runner's, else all; one that names its own gets what it names, `[]` included, which is
	// none.
	tools := l.Tools
	if tools == nil {
		tools = h.floor()
	}
	var t *task.Task
	switch driver {
	case "scripted":
		// A contract the driver cannot enforce fails the spawn — there is no model to
		// ask again, so honouring the field would mean dropping it, and a dropped
		// contract is worse than none. The refusal lives in NewCallsTaskFrom now, so
		// both runtimes state it once and neither can forget it.
		t = NewScriptedTask(l, h.Root, h.Env, reg)
	case "llm":
		if h.LLMKey == "" {
			return task.NewFailed(l.ID, reg, "llm driver unavailable: "+cmp.Or(h.NoKey, "no model key"))
		}
		set, ended, err := handCredential(l, h.Root)
		if err != nil {
			return task.NewFailed(l.ID, reg, err.Error())
		}
		// WHICH CREDENTIAL: the one the spawn named, else this verse's own. The spawn's is
		// already the key — versed read it from the address the spawn named, as whoever
		// named it, and refused there if that read was not theirs to make.
		virtualKey := h.LLMVirtualKey
		if h.VirtualKeyOr != nil {
			virtualKey = h.VirtualKeyOr(l.VirtualKey)
		}
		// The workspace layout is the runner's convention, so the runner describes it. It rides
		// the PROMPT rather than the frame because agent.Build derives a default frame from the
		// tools and vector when a spawn writes none, and setting one here would silently replace
		// advice that is more useful than this. Only for a driver that READS it: a call list's
		// prompt is a document.
		//
		// Delivery is the agent's: it commits what it means to deliver and calls `push`. No
		// finisher, so what it did not push does not leave — and the brief says so.
		prompt := l.Prompt
		if h.Workspace != nil {
			prompt = h.Brief + deliveryBrief(l, h.Workspace, offeredTools(h.Extra, tools)) + prompt
		}
		// Frame/Tools/Vector come from the spawn. The vector is handed to Build rather
		// than left for the daemon to install afterwards because the DEFAULT FRAME is
		// derived from it: describing each tool as it will actually behave only works
		// if the vector is known while the frame is being written. The daemon installs
		// it again after Build, which is idempotent and is also what bounds the
		// scripted driver, so neither path depends on a builder remembering.
		p, _ := agent.Build(agent.Config{
			ID: l.ID, Prompt: prompt, Root: h.Root, Resolver: reg, Log: h.Log,
			Frame: l.Frame, Tools: tools, Answer: l.Answer, Vector: l.Vector, Env: h.Env, Set: set,
			Extra:      h.Extra,
			LLMBaseURL: h.LLMBaseURL, LLMKey: h.LLMKey,
			LLMVirtualKey: virtualKey, LLMModel: h.LLMModel, Converse: l.Converse,
		})
		t = ended(p)
	case task.DriverCalls:
		// A list of calls the charter wrote, with the harness's own executor and NO model —
		// which is why it is here rather than under the llm case: `calls` needs the four
		// tools and none of the credentials, so a verse with no keys configured can still
		// run the mechanical work. `scripted` above is the same driver with a
		// one-shell-command list built for it. The same extras as the agent: a list that wants
		// to deliver writes `push`, and the credential still never reaches the task.
		//
		// The list arrives as the launch's own field, checked by the engine before it crossed.
		// Checked again here, because a task can be posted straight at a runner without
		// passing through a charter at all.
		if err := task.CheckCalls(l.Calls); err != nil {
			return task.NewFailed(l.ID, reg, task.DriverCalls+": "+err.Error())
		}
		set, ended, err := handCredential(l, h.Root)
		if err != nil {
			return task.NewFailed(l.ID, reg, err.Error())
		}
		t = ended(agent.Calls(agent.Config{
			ID: l.ID, Root: h.Root, Resolver: reg, Log: h.Log,
			Tools: tools, Answer: l.Answer, Vector: l.Vector, Env: h.Env, Set: set, Extra: h.Extra,
		}, l.Calls))
	default:
		cmd, ok := h.Agents[driver]
		if !ok {
			return task.NewFailed(l.ID, reg, fmt.Sprintf("unknown driver %s (want scripted, calls, llm%s)",
				driver, h.agentList()))
		}
		var env Environment
		if h.Environment != nil {
			env = *h.Environment
		}
		// The program's tools: the runner's own and `ask`, passed the floor. A spawn that names
		// one the program cannot have here fails before anything runs, as a call list does: the
		// charter asked for something specific, and a program told it has `push` when it has
		// not would deliver nothing and believe it had.
		extra := append(slices.Clone(h.Extra), agent.Ask(reg))
		offered := offeredTools(extra, tools)
		for _, name := range l.Tools {
			if !slices.ContainsFunc(offered, func(t agent.Tool) bool { return t.Name == name }) {
				return task.NewFailed(l.ID, reg, fmt.Sprintf("no tool %q for %s here (it has: %s)",
					name, driver, strings.Join(toolNamesOf(offered), ", ")))
			}
		}
		// The workspace layout and how the tools are reached ride the prompt, as they do for the
		// verse agent and for the same reason: the arrangement is the runner's.
		if h.Workspace != nil {
			l.Prompt = h.Brief + callToolBrief(offered, callToolInstalled(h.Root)) + deliveryBrief(l, h.Workspace, offered) + l.Prompt
		}
		var err error
		t, err = buildCommandTask(l, reg, cmd, env, h.Root, offered)
		if err != nil {
			return task.NewFailed(l.ID, reg, err.Error())
		}
	}
	if h.Workspace != nil {
		// What a gate can match on: the branch, the base, what has been touched, and where it
		// would go. `GateMatch.Branches` and `GateMatch.OnPush` read this, so a charter can say
		// `ask` on anything touching a release branch and `auto` everywhere else.
		t.SetRepo(h.Workspace.RepoContext)
	}
	return t
}

func toolNamesOf(tools []agent.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

// floor is the tools a task here gets when its spawn names none (floorOf).
func (h *Drivers) floor() []string {
	var env []string
	if h.Environment != nil {
		env = h.Environment.Tools
	}
	return floorOf(env, h.Tools)
}

// agentList is the programs this runner may be asked for, for a refusal that lists the drivers.
func (h *Drivers) agentList() string {
	if len(h.Agents) == 0 {
		return ""
	}
	return ", or one of the agents it declares: " + strings.Join(agentNames(h.Agents), ", ")
}

// handCredential hands a launch's credential to the task being built for it: written to a file
// outside the workspace root (task.CredentialFile), with the variable that names the file to add to
// the task's shell. ended is given the built task, and removes the file when the task has ended.
// A launch with no credential gets no file and no variable.
func handCredential(l task.Launch, root string) (set []string, ended func(*task.Task) *task.Task, err error) {
	if l.Credential == "" {
		return nil, func(p *task.Task) *task.Task { return p }, nil
	}
	env, remove, err := task.CredentialFile(l.Credential, root)
	if err != nil {
		return nil, nil, fmt.Errorf("the task's credential could not be handed to it: %w", err)
	}
	return []string{env}, func(p *task.Task) *task.Task {
		frames, stop := p.Subscribe()
		go func() {
			for range frames { // the stream ends when the task does
			}
			stop()
			remove()
		}()
		return p
	}, nil
}
