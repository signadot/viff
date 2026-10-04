package versed

import (
	"fmt"
	"io"

	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/harness"
)

// Harness builds the tasks a verse hosts in process — `verse up`'s builder, and a test's. It is
// here rather than in the command so that what a test runs is what `verse up` runs: the same
// driver choice, the same credentials rule.
type Harness struct {
	Root string   // the working directory an agent acts in
	Env  []string // what a task's shell inherits
	Log  io.Writer
	// Default is the driver of a spawn that names none.
	Default string
	// The model: an OpenAI-compatible endpoint (task/harness/llm.go). Key empty means no model,
	// and an `llm` spawn fails its own task rather than the engine.
	LLMBaseURL, LLMKey, LLMVirtualKey, LLMModel string
	// VirtualKeyOr is the credential a spawn's named key resolves to (config.LLMConfig).
	VirtualKeyOr func(named string) string
}

// Build is a taskd builder.
func (h *Harness) Build(l task.Launch, reg *task.Registry, content task.ContentReader) *task.Task {
	driver := l.Driver
	if driver == "" {
		driver = h.Default
	}
	switch driver {
	case "scripted":
		// A contract the driver cannot enforce fails the spawn — there is no model to
		// ask again, so honouring the field would mean dropping it, and a dropped
		// contract is worse than none. The refusal lives in NewCallsTaskFrom now, so
		// both runtimes state it once and neither can forget it.
		return NewScriptedTask(l, h.Root, h.Env, reg)
	case "llm":
		if h.LLMKey == "" {
			return task.NewFailed(l.ID, reg, "llm driver unavailable: set llm.apiKey and llm.virtualKey in verse.tony, or PORTKEY_API_KEY/PORTKEY_VIRTUAL_KEY in the environment")
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
		// content (non-nil for a scoped federation) routes read_file through the
		// verse; otherwise the agent reads the server-side root tree.
		//
		// Frame/Tools/Vector come from the spawn. The vector is handed to Build rather
		// than left for the daemon to install afterwards because the DEFAULT FRAME is
		// derived from it: describing each tool as it will actually behave only works
		// if the vector is known while the frame is being written. The daemon installs
		// it again after Build, which is idempotent and is also what bounds the
		// scripted driver, so neither path depends on a builder remembering.
		p, _ := harness.Build(harness.Config{
			ID: l.ID, Prompt: l.Prompt, Root: h.Root, Resolver: reg, Log: h.Log, Content: content,
			Frame: l.Frame, Tools: l.Tools, Answer: l.Answer, Vector: l.Vector, Env: h.Env, Set: set,
			LLMBaseURL: h.LLMBaseURL, LLMKey: h.LLMKey,
			LLMVirtualKey: virtualKey, LLMModel: h.LLMModel, Converse: l.Converse,
		})
		return ended(p)
	case task.DriverCalls:
		// A list of calls the charter wrote, with the harness's own executor and NO model —
		// which is why it is here rather than under the llm case: `calls` needs the four
		// tools and none of the credentials, so a verse with no keys configured can still
		// run the mechanical work. `scripted` above is the same driver with a
		// one-shell-command list built for it.
		if err := task.CheckCalls(l.Calls); err != nil {
			return task.NewFailed(l.ID, reg, task.DriverCalls+": "+err.Error())
		}
		set, ended, err := handCredential(l, h.Root)
		if err != nil {
			return task.NewFailed(l.ID, reg, err.Error())
		}
		return ended(harness.Calls(harness.Config{
			ID: l.ID, Root: h.Root, Resolver: reg, Log: h.Log, Content: content,
			Tools: l.Tools, Answer: l.Answer, Vector: l.Vector, Env: h.Env, Set: set,
		}, l.Calls))
	default:
		return task.NewFailed(l.ID, reg,
			"unknown driver "+driver+" (want scripted, calls or llm)")
	}
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
