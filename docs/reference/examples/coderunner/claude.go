package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/task"
)

// The claude driver runs a coding agent BINARY in the worktree. It is the "foreign one-shot"
// the task loop already has a shape for: `task.CallAgentRequest` calls the child an opaque
// leaf, against our own loop's transparent frame.
//
// What verse gates here is the INVOCATION, not the agent's steps. Nothing can gate inside a
// foreign agent — it chooses and runs its own tools — so one action per turn is the honest
// unit, and saying so is better than implying a supervision that is not there. What the read
// plane gets in exchange is what the agent reports about itself, which is a self-report and
// labelled as one.
//
// It is MULTI-TURN, by session resume, and that is what makes it a driver rather than a
// shell-out. Two things a single `claude -p` cannot do:
//
//   - Steers. `verse task say` lands as the next turn of the same session. A single-shot
//     driver would drop them, and a control call that reports success and does nothing is
//     the failure this codebase keeps refusing.
//   - Answer contracts. task.Launch.Answer says a runtime whose driver cannot enforce a
//     contract must FAIL the spawn rather than run it unchecked. With resume it can enforce
//     one: check the final message, feed the complaint back as another turn, bounded by the
//     contract's own repair budget — the loop harness.llmDriver already runs, against a
//     different back end.
type claudeDriver struct {
	prompt string
	frame  string
	env    Environment
	point  Point
	conf   Confiner
	dir    string

	contract *answer.Contract
	repairs  int

	session string // the agent's own session id, for --resume
	last    string // its last final message: this task's result
	started bool
	done    bool

	mu     sync.Mutex
	steers []string

	emit func(task.Frame)
}

// agentArgv is the seam. Everything this runner knows about the agent binary's command line
// is here, so pointing it at a different agent is one function rather than a search.
//
// --output-format json makes the final message and the session id readable rather than
// scraped. Permission prompting is off because the containment IS the floor here: a prompt
// the agent answers to itself is not supervision, and an unattended runner has nobody to ask.
// What bounds it is the point this task runs at, which is enforced outside the agent.
func agentArgv(text, session string) []string {
	args := []string{"-p", text, "--output-format", "json", "--permission-mode", "bypassPermissions"}
	if session != "" {
		args = append(args, "--resume", session)
	}
	return args
}

// agentResult is the slice of the binary's JSON we depend on. Anything else it reports is
// ignored rather than decoded, so a new field upstream is not a breaking change here.
type agentResult struct {
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
	Subtype   string `json:"subtype"`
}

func (d *claudeDriver) Next(_ context.Context, prev task.StepResult) (task.ToolUse, bool, error) {
	if !d.started {
		d.started = true
		return d.turn(d.opening()), true, nil
	}
	// Fold in what the last turn produced before deciding whether there is another. A turn
	// that failed at the executor is not a result to check a contract against.
	if prev.Err != nil {
		return task.ToolUse{}, false, prev.Err
	}
	if prev.Blocked {
		// The gate refused this invocation. There is no other way to do this work — the
		// agent IS the doer — so the honest end is to stop and say so rather than loop.
		return task.ToolUse{}, false, fmt.Errorf("the agent invocation was refused: %s", prev.Note)
	}
	// A steer that arrived during the turn is the next turn, and it comes before the
	// contract check: an instruction from outside is new authorization and outranks a shape
	// complaint the model has not seen yet.
	if s := d.drain(); s != "" {
		return d.turn(s), true, nil
	}
	repair, err := d.checkAnswer()
	if err != nil {
		return task.ToolUse{}, false, err
	}
	if repair != "" {
		return d.turn(repair), true, nil
	}
	d.done = true
	return task.ToolUse{}, false, nil
}

// opening is the first turn: the frame (standing instructions) and the prompt (this job).
// They are concatenated because the binary takes one text, which is a real difference from
// the harness — there a frame is a system message the model cannot confuse with the task.
func (d *claudeDriver) opening() string {
	var b strings.Builder
	if d.frame != "" {
		b.WriteString(d.frame)
		b.WriteString("\n\n")
	}
	if d.contract != nil {
		// Stated up front for the reason harness.Build states it: an agent that meets the
		// check for the first time as a rejection wastes a repair attempt learning what it
		// could have been told at the start.
		fmt.Fprintf(&b, "ANSWER CONTRACT. Your final message must satisfy:\n%s\n\n", d.contract.Source())
	}
	b.WriteString(d.prompt)
	return b.String()
}

func (d *claudeDriver) turn(text string) task.ToolUse {
	return task.ToolUse{
		Name:  "agent",
		Input: map[string]any{"text": text, "agent": "claude"},
		// Derived here rather than left to a deriver's guess: this action invokes another
		// agent, which is the one verb the vector already knows how to gate as delegation.
		//
		// The EFFECT says so and the `CallAgent` field is deliberately left nil, which is a
		// distinction worth stating because setting it looks right and is not. That field is
		// not a label — it is the instruction that makes the loop route this action to
		// spawnChild and build a sub-task through a SpawnFunc. This runner has none, and
		// should not: the agent is a foreign one-shot run by the executor below, an opaque
		// leaf rather than a recursive invocation of our own loop. With it set, every task
		// failed with "no spawn factory configured" the moment its gate was approved.
		Effects: []task.Effect{{Verb: task.CallAgent, Resource: task.AgentRes("claude")}},
	}
}

// Steer implements task.Steerable: the loop drains instructions at the boundary before Next,
// so they land cooperatively — honoured at the next turn, never interrupting one in flight.
func (d *claudeDriver) Steer(instructions []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.steers = append(d.steers, instructions...)
}

func (d *claudeDriver) drain() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.steers) == 0 {
		return ""
	}
	s := strings.Join(d.steers, "\n")
	d.steers = nil
	return s
}

// Result implements task.Resulter: the agent's last final message is this task's result, which
// is what a completed task's `result` carries and what a charter's `$result` derefs.
func (d *claudeDriver) Result() string { return d.last }

// checkAnswer is harness.llmDriver.checkAnswer against a different back end, and the reasoning
// transfers whole: a run that cannot produce its declared shape FAILS, because failure is a
// branch every rule already declares, where "the charter could not deref the result" is a hook
// error nobody is watching, raised after the tokens are spent.
func (d *claudeDriver) checkAnswer() (string, error) {
	if d.contract == nil {
		return "", nil
	}
	if _, err := d.contract.Check(d.last); err == nil {
		return "", nil
	} else {
		var f *answer.Failure
		if !errors.As(err, &f) {
			return "", err // the contract itself is unusable; retrying spends budget on a bug
		}
		if d.repairs >= d.contract.Repairs() {
			return "", fmt.Errorf("answer contract not satisfied after %d repair attempt(s): %w",
				d.repairs, err)
		}
		d.repairs++
		// "fail" is the harness's give-up tool and this driver has none, so the repair text
		// names it and the agent simply will not have it. Saying so plainly beats offering a
		// tool that does not exist.
		return f.Repair(d.contract, "saying you cannot"), nil
	}
}

// claudeExec runs one turn. It is where the containment is applied and where the binary's
// environment is BUILT rather than inherited — the leak docs/roadmap/expose.md names, which a
// runner holding a git credential is the worst place to reproduce.
type claudeExec struct {
	d *claudeDriver
}

func (e *claudeExec) Execute(ctx context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	d := e.d
	text, _ := tu.Input["text"].(string)
	cmd := exec.CommandContext(ctx, "claude", agentArgv(text, d.session)...)
	// The environment is the allowlist and nothing else. os.Environ() is deliberately not
	// consulted: a passthrough list is a floor, and "everything except what we remembered to
	// strip" is not.
	cmd.Env = buildEnv(d.env.Env, d.env.Commands)
	if err := d.conf.Confine(cmd, d.point, d.dir); err != nil {
		return task.StepResult{Use: tu}, nil, err
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()

	var res agentResult
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &res); err != nil {
		// No decodable answer is not an empty answer. A binary that is missing, or that
		// wrote a usage message, fails the turn with what it actually said.
		detail := strings.TrimSpace(errb.String())
		if detail == "" {
			detail = strings.TrimSpace(out.String())
		}
		if runErr != nil && detail == "" {
			detail = runErr.Error()
		}
		return task.StepResult{Use: tu}, nil, fmt.Errorf("agent produced no readable result: %s", detail)
	}
	if res.SessionID != "" {
		d.session = res.SessionID // every later turn resumes this
	}
	if res.Result != "" {
		d.last = res.Result
	}
	if res.IsError {
		return task.StepResult{Use: tu, Output: res.Result},
			nil, fmt.Errorf("agent failed (%s): %s", res.Subtype, firstLine(res.Result))
	}
	if d.emit != nil {
		d.emit(task.Frame{Kind: task.EvSay, Text: res.Result})
	}
	return task.StepResult{Use: tu, Output: res.Result}, nil, nil
}

// buildEnv is the passthrough allowlist plus a PATH narrowed to the commands the environment
// opted into.
//
// The PATH half is honest only as far as the filesystem is: an absolute path walks straight
// past an allowlist, so this is a floor when `filesystem.scope` is `private` and a convention
// otherwise. Stated here rather than discovered, because a list that looks like a permission
// and is not is worse than no list.
func buildEnv(passthrough, commands []string) []string {
	var env []string
	for _, name := range passthrough {
		if v, ok := lookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	_ = commands // the symlink farm belongs with the mount namespace; see confine.go
	return env
}
