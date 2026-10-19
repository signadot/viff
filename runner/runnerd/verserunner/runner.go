package verserunner

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/calltool"
	"github.com/signadot/verse/runner/runnerd/codex"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/verserunner/agent"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// Secrets is what the runner reads from its environment. A task's process doesn't have the
// runner's environment, so they are sent to it on its stdin.
type Secrets struct {
	PortkeyAPIKey  string `tony:"field=portkeyApiKey,omitzero"`
	PortkeyBaseURL string `tony:"field=portkeyBaseUrl,omitzero"`
	// PortkeyVirtualKey is the virtual key a run uses when neither its launch nor the
	// configuration names one: the deployment's, beside its API key.
	PortkeyVirtualKey string `tony:"field=portkeyVirtualKey,omitzero"`
	// CodexKey is the OpenAI API key the `codex:` section names a variable for, read from the
	// runner's environment (codex.Config.Key) and written by the task process as codex's login.
	CodexKey string `tony:"field=codexKey,omitzero"`
}

// SecretsFromEnv reads the runner's secrets from its environment.
func SecretsFromEnv() Secrets {
	return Secrets{
		PortkeyAPIKey:     os.Getenv(EnvPortkeyAPIKey),
		PortkeyBaseURL:    os.Getenv(EnvPortkeyBaseURL),
		PortkeyVirtualKey: os.Getenv(EnvPortkeyVirtualKey),
	}
}

// Standalone is the verse runner as a standalone runner, with a runnerd.SubprocessHandler: each
// task in a process of its own, started with command (which calls ServeTask), confined by sb.
// The process gets the runner's configuration and secrets on its stdin, and an environment with
// only what c's shellEnv names. tokens is what the runner holds that a task never does: its git
// tokens, for when c checks out repos, and its Linear credential.
func Standalone(c Config, s Secrets, tokens Tokens, sb runnerd.Sandbox, command []string, work, control string,
	log io.Writer) (*runnerd.SubprocessHandler, error) {
	hosts, err := modelHosts(c, s)
	if err != nil {
		return nil, err
	}
	nets := networks(sb)
	// The binary a script calls its tools with: bound into each task's sandbox where it is, and
	// named in the task's configuration, so the task process uses the one the runner bound.
	tool := FindCallTool(command[0])
	var programs []string
	if tool != "" {
		programs = []string{tool}
	} else {
		fmt.Fprintf(log, "runnerd: no %s beside %s or on PATH: a script reaches its tools only by "+
			"the socket $%s names\n", CallToolBinary, command[0], calltool.Env)
	}
	blob, err := codec.Marshal(taskConfig{Config: c, Secrets: s, CallTool: tool, Networks: nets}, codec.Tony)
	if err != nil {
		return nil, err
	}
	// The channel every task is given where the runner performs anything: pushes from its
	// mirrors, or a Linear filing with its credential. A runner with no `repos:` section keeps
	// mirrors nowhere a launch could name, and offers no `git`.
	var repos *runnerd.Repos
	if c.Repos != nil {
		_, forge := c.Repos.forge()
		repos = runnerd.NewRepos(c.Repos.Mirrors, forge, tokens.Git)
		// What the last run was approved to push and did not land, tried once more.
		go repos.ReflectAll(context.Background(), log)
	} else if len(c.performs()) > 0 {
		repos = runnerd.NewRepos(filepath.Join(work, "mirrors"), "", "")
	}
	if repos != nil {
		repos.Services = c.services(tokens)
		repos.Ceilings = c.ceilings()
	}
	h := runnerd.NewSubprocessHandler(runnerd.SubprocessConfig{
		Command: command, Env: task.ShellEnv(c.shellEnv()), Config: blob,
		Sandbox: sb, Programs: programs, Repos: repos, AgentHosts: hosts,
		Work: work, Control: control, Log: log,
	})
	h.Describe(c.Describe(s, nets))
	h.OwnEnv(c.shellEnv())
	return h, nil
}

// modelHosts is where the runner's agents call their models, which they reach whatever their
// network: the verse agent's gateway, and Codex's API where the runner runs Codex. A base URL
// that names no host is refused: no network could let the agent reach it.
func modelHosts(c Config, s Secrets) ([]string, error) {
	base := cmp.Or(s.PortkeyBaseURL, agent.DefaultBaseURL)
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%s: %q is not a url with a host, and the agent's model is called there", EnvPortkeyBaseURL, base)
	}
	hosts := []string{u.Host}
	if c.Codex != nil {
		hosts = append(hosts, codex.ModelHost)
	}
	return hosts, nil
}

// taskConfig is what a task's process reads on its stdin.
type taskConfig struct {
	Config  Config  `tony:"field=config"`
	Secrets Secrets `tony:"field=secrets"`
	// CallTool is the verse-call-tool the runner found and bound into the sandbox, "" for none:
	// the runner's decision is the task's, whatever the task's PATH.
	CallTool string `tony:"field=callTool,omitzero"`
	// Networks is the networks the runner's sandbox builds (networks), so the task's process
	// describes what the runner described.
	Networks []string `tony:"field=networks,omitzero"`
}

// ServeTask is a task's process: it reads the runner's configuration and secrets from stdin,
// and serves the one task a SubprocessHandler hands it, in the working directory.
func ServeTask(stdin io.Reader, log io.Writer) error {
	blob, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading the task's configuration: %w", err)
	}
	var tc taskConfig
	if err := codec.Unmarshal(blob, &tc, codec.Tony); err != nil {
		return fmt.Errorf("the task's configuration does not parse: %w", err)
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	r := New(tc.Config, tc.Secrets, dir, log)
	r.callTool = tc.CallTool
	return runnerd.ServeTask(r.Build, tc.Config.Describe(tc.Secrets, tc.Networks), tc.Config.shellEnv())
}

// Runner builds the verse runner's tasks from their launch: `agent: verse` is verse's own agent
// (package agent), and a `script` runs by its #! line. The standalone runner builds it in each
// task's process (ServeTask), and `verse up` embeds it.
type Runner struct {
	cfg     Config
	secrets Secrets
	root    string // the directory tasks run in
	log     io.Writer
	// deliveryFD is where a task that names repos finds its delivery channel.
	deliveryFD uintptr
	// callTool is verse-call-tool, which a script calls its tools with, "" for none: the one the
	// runner bound, in a task's process (ServeTask), and else beside this process's executable or
	// on its PATH (FindCallTool), as for `verse up`.
	callTool string
}

// New is the verse runner under c and s, running its tasks in dir. A task's shell sees only the
// variables c's shellEnv names, never the runner's secrets.
func New(c Config, s Secrets, dir string, log io.Writer) *Runner {
	return &Runner{cfg: c, secrets: s, root: dir, log: log, deliveryFD: runnerd.DeliveryFD, callTool: findCallTool()}
}

// Build is a runnerd builder. The launch was checked as it arrived (runnerd.CheckLaunch), so its
// doer is one this runner offers.
func (r *Runner) Build(l runner.Launch, reg *task.Registry) *task.Task {
	// What goes when the task has ended: its credential file, a script's socket. The list
	// grows below until ended is called, which reads it then.
	var cleanup []func()
	ended := func(p *task.Task) *task.Task { return onEnd(p, cleanup) }
	set, remove, err := handCredential(l, r.root)
	if err != nil {
		return task.NewFailed(l.ID, reg, err.Error())
	}
	cleanup = append(cleanup, remove)
	set = append(envSet(l.Doer.With.Env), set...)
	prompt, tools := l.Prompt, []agent.Tool(nil)
	var cl *delivery.Client
	if r.performs() {
		// The channel the runner performs over, and the repos it checked out: the tools over
		// both, and a brief about the repos where there are any.
		var ws *workspace.Workspace
		if ws, cl, err = r.checkout(l); err != nil {
			return ended(task.NewFailed(l.ID, reg, err.Error()))
		}
		if len(ws.Trees) > 0 {
			prompt = repoBrief(ws, workspace.RequiredTrailers(&l), &l) + prompt
		}
		tools = append(repoTools(cl, ws, r.cfg.atGitHub()), issueTools(cl, ws, l.Doer.With.GitIssue, r.cfg.atGitHub(), r.cfg.Systems["linear-issue"].Credential != "")...)
		if r.cfg.Systems["slack"].Credential != "" {
			tools = append(tools, slackTools(cl)...)
		}
		tools = offered(tools, l.Policy)
	}
	if l.Doer.Script != "" {
		p, stop, err := newScriptTask(l, r.root, r.cfg.shellEnv(), set, tools, r.callTool, reg, r.cfg.taskCeilings())
		if err != nil {
			if cl != nil {
				_ = cl.Close()
			}
			return ended(task.NewFailed(l.ID, reg, err.Error()))
		}
		cleanup = append(cleanup, stop)
		return ended(p)
	}
	if l.Doer.Agent == codex.Agent {
		// The codex agent (package codex): each command and patch it proposes is an action of
		// this loop, gated and narrated like the verse agent's own, and the runner's tools are
		// offered to it as its own. The same brief and the same delivery as the verse agent's.
		if r.cfg.Codex == nil {
			return ended(task.NewFailed(l.ID, reg, "this runner has no `codex:` section, so it cannot run codex"))
		}
		l.Prompt = prompt
		env := append(task.ShellEnv(r.cfg.shellEnv()), set...)
		p, err := codex.Task(l, reg, r.cfg.Codex.Creds(r.secrets.CodexKey), env, r.root, tools, r.cfg.taskCeilings())
		if err != nil {
			return ended(task.NewFailed(l.ID, reg, err.Error()))
		}
		return ended(p)
	}
	switch {
	case l.Doer.Agent != AgentName:
		return ended(task.NewFailed(l.ID, reg, "this runner offers no agent "+l.Doer.Agent))
	case r.secrets.PortkeyAPIKey == "":
		return ended(task.NewFailed(l.ID, reg, "the verse agent has no model key on this runner"))
	}
	p := agent.Build(agent.Config{
		ID: l.ID, Prompt: prompt, Root: r.root, Resolver: reg, Log: r.log, Extra: tools,
		Answer: l.Answer, Policy: l.Policy, Ceilings: r.cfg.taskCeilings(), Env: r.cfg.shellEnv(),
		Set: set, ChildSet: envSet(l.Doer.With.Env),
		LLMBaseURL: r.secrets.PortkeyBaseURL, LLMKey: r.secrets.PortkeyAPIKey,
		LLMVirtualKey: cmp.Or(l.Doer.Annotations[AnnotationVirtualKey], r.cfg.Models.Portkey.VirtualKey, r.secrets.PortkeyVirtualKey),
		LLMModel:      cmp.Or(l.Doer.With.Model, r.cfg.Models.Model),
		Converse:      l.Converse,
	})
	return ended(p)
}

// offered is the runner's tools whose verb the policy provides: a tool is its system's verb
// (systems.SplitTool), and a verb no row names, or one withheld, is not offered, so the doer
// never sees it.
func offered(tools []agent.Tool, p task.Policy) []agent.Tool {
	return slices.DeleteFunc(slices.Clone(tools), func(t agent.Tool) bool {
		system, verb, ok := systems.SplitTool(t.Name)
		return ok && !p.Provides(systems.StepName(system, verb))
	})
}

// performs reports whether the runner performs acts for its tasks over a channel: a push where
// it keeps mirrors, a Linear filing where it holds the credential. Where it does, every task
// has the channel (runnerd.Repos), and where it does not, none has.
func (r *Runner) performs() bool { return len(r.cfg.performs()) > 0 }

// checkout is the task's repos as its runner checked them out in its directory, and the channel
// a push goes back over. A task that can't reach the channel doesn't run: its work would have no
// way out.
func (r *Runner) checkout(l runner.Launch) (*workspace.Workspace, *delivery.Client, error) {
	repos := make([]workspace.Repo, len(l.Doer.With.Git))
	for i, lr := range l.Doer.With.Git {
		repos[i] = workspace.Repo{Repo: lr}
	}
	cl, err := delivery.Attach(r.deliveryFD)
	if err != nil {
		return nil, nil, err
	}
	ws, err := workspace.Reopen(r.root, repos, workspace.AutoBranchPrefix+workspace.SafeSeg(l.ID), nil)
	if err != nil {
		_ = cl.Close()
		return nil, nil, fmt.Errorf("the task's repos: %w", err)
	}
	return ws, cl, nil
}

// handCredential hands a launch's credential to the task being built for it: written to a file
// outside the workspace root (task.CredentialFile), with the variable that names the file to add to
// the task's shell, and remove to remove the file once the task has ended. A launch with no
// credential gets no file and no variable.
func handCredential(l runner.Launch, root string) (set []string, remove func(), err error) {
	if l.Credential == "" {
		return nil, func() {}, nil
	}
	env, remove, err := task.CredentialFile(l.Credential, root)
	if err != nil {
		return nil, nil, fmt.Errorf("the task's credential could not be handed to it: %w", err)
	}
	return []string{env}, remove, nil
}

// onEnd runs each of fs once p has ended, and is p.
func onEnd(p *task.Task, fs []func()) *task.Task {
	frames, stop := p.Subscribe()
	go func() {
		for range frames { // the stream ends when the task does
		}
		stop()
		for _, f := range fs {
			f()
		}
	}()
	return p
}

// envSet is a launch's `with.env` as a shell's NAME=value list, in name order.
func envSet(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, k+"="+env[k])
	}
	return out
}
