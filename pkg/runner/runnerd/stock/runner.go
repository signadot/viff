package stock

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/runner/runnerd/calltool"
	"github.com/signadot/verse/pkg/runner/runnerd/claude"
	"github.com/signadot/verse/pkg/runner/runnerd/codex"
	"github.com/signadot/verse/pkg/runner/runnerd/delivery"
	"github.com/signadot/verse/pkg/runner/runnerd/verse"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// Secrets is what the runner reads from its environment. A task's process doesn't have the
// runner's environment, so they are sent to it on its stdin.
type Secrets struct {
	PortkeyAPIKey  string `tony:"field=portkeyApiKey,omitzero"`
	PortkeyBaseURL string `tony:"field=portkeyBaseUrl,omitzero"`
	// PortkeyVirtualKey is the virtual key a run uses when neither its launch nor the
	// configuration names one: the deployment's, beside its API key.
	PortkeyVirtualKey string `tony:"field=portkeyVirtualKey,omitzero"`
	// CodexKey is the OpenAI API key, read from the runner's environment (codex.Key) where it
	// runs codex, and written by the task process as codex's login.
	CodexKey string `tony:"field=codexKey,omitzero"`
	// ClaudeKey is Claude's credential, read from the runner's environment (claude.Key) where it
	// runs claude, and put in the claude process's environment by the task process under
	// ClaudeKeyEnv, the variable it was read from.
	ClaudeKey    string `tony:"field=claudeKey,omitzero"`
	ClaudeKeyEnv string `tony:"field=claudeKeyEnv,omitzero"`
}

// SecretsFromEnv reads the runner's secrets from its environment, each where c offers the agent
// that needs it: a task's process is sent no secret its runner does not use.
func SecretsFromEnv(c Config) Secrets {
	var s Secrets
	if _, ok := c.Agents[AgentName]; ok {
		k := verse.Key()
		s.PortkeyAPIKey, s.PortkeyBaseURL, s.PortkeyVirtualKey = k.APIKey, k.BaseURL, k.VirtualKey
	}
	if _, ok := c.Agents[codex.Agent]; ok {
		s.CodexKey = codex.Key()
	}
	if _, ok := c.Agents[claude.Agent]; ok {
		s.ClaudeKeyEnv, s.ClaudeKey = claude.Key()
	}
	return s
}

// Standalone is the verse runner as a standalone runner, with a runnerd.SubprocessHandler: each
// task in a process of its own, started with command (which calls ServeTask), confined by sb.
// The process gets the runner's configuration and secrets on its stdin, and an environment with
// only what its launch's doer's shellEnv names: no task's process holds a variable another
// doer's shell is given. tokens is what the runner holds that a task never does: its git
// tokens, for when c checks out repos, and its Linear credential. toVerse is how the runner
// carries a task's request to the verse it serves, with its own token (runnerd.Guard.Carry); nil
// for a runner with none, whose tasks are not offered the verse system.
func Standalone(c Config, s Secrets, tokens Tokens, sb runnerd.Sandbox, command []string, work, control string,
	toVerse delivery.Verse, log io.Writer) (*runnerd.SubprocessHandler, error) {
	c.reachesVerse = toVerse != nil
	hosts, err := modelHosts(c, s)
	if err != nil {
		return nil, err
	}
	nets := networks(sb)
	if err := c.checkBuilds(nets); err != nil {
		return nil, err
	}
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
	// And the agents' own programs, where the runner finds them: one installed outside the system
	// directories, as a person's `claude` is under ~/.local/bin, would not be inside the sandbox.
	programs = append(programs, c.agentPrograms(exec.LookPath)...)
	// And what the operator keeps for every task to read (`readOnly:`), bound read only where it
	// is at each launch. None may hold, or lie in, what the runner keeps from its tasks.
	mirrors := filepath.Join(work, "mirrors")
	if c.Repos != nil {
		mirrors = c.Repos.Mirrors
	}
	// The secret store is the runner's own too, and is where no sandbox binds it: apart from
	// the directories above, and reached by no readOnly path.
	secrets := c.secretsDir()
	if secrets != "" {
		if err := runnerd.CheckSecretStore(secrets, runnerd.KeptDirs(work, control, mirrors, "", "")); err != nil {
			return nil, err
		}
		if _, err := os.Stat(secrets); err != nil {
			fmt.Fprintf(log, "runnerd: secrets.dir %s: %v; no secret is there while it is not\n", secrets, err)
		}
		if sb.Name() == (runnerd.Unconfined{}).Name() {
			fmt.Fprintf(log, "runnerd: secrets.dir %s: this runner runs its tasks unconfined (-sandbox none), so a task "+
				"sees the whole machine, the secret store with it\n", secrets)
			// And a command run with a secret, unconfined, would run as the runner's user on the
			// whole machine, the secret in its environment: the system is closed, before the
			// task's configuration is marshalled, so the task's process offers nothing of it either.
			if naming := c.naming("secret"); len(naming) > 0 {
				fmt.Fprintf(log, "runnerd: -sandbox none: secret is closed to every doer, though %s names it, since a command "+
					"run with a secret runs in a sandbox of its own; a spawn that names it is refused at install\n", strings.Join(naming, ", "))
				c = c.without("secret")
			}
		}
	}
	if err := runnerd.CheckReadOnly(c.ReadOnly, runnerd.KeptDirs(work, control, mirrors, secrets, "")); err != nil {
		return nil, err
	}
	for _, p := range c.ReadOnly {
		if _, err := os.Stat(p); err != nil {
			fmt.Fprintf(log, "runnerd: readOnly %s: %v; the tasks will not have it while it is not there\n", p, err)
		}
	}
	blob, err := codec.Marshal(taskConfig{Config: c, Secrets: s, CallTool: tool, Networks: nets, ReachesVerse: c.reachesVerse}, codec.Tony)
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
		repos.Services.Verse = toVerse
		// A command run with a secret is confined as the task's process is, over the task's
		// root and under its network, with what the operator keeps read only.
		repos.Services.Confine = runnerd.Confiner{Sandbox: sb, ReadOnly: c.ReadOnly,
			Kept: runnerd.KeptDirs(work, control, mirrors, secrets, "")}
		repos.Ceilings = func(l *runner.Launch) map[string]systems.Ceiling { return c.ceilings(c.doer(l)) }
	}
	h := runnerd.NewSubprocessHandler(runnerd.SubprocessConfig{
		Command: command, Env: func(l *runner.Launch) []string { return task.ShellEnv(c.shellEnv(c.doer(l))) }, Config: blob,
		Sandbox: sb, Programs: programs, ReadOnly: c.ReadOnly, Mirrors: mirrors, Secrets: secrets, Repos: repos, AgentHosts: hosts,
		Work: work, Control: control, Log: log,
	})
	h.Describe(c.Describe(nets))
	h.OwnEnv(c.envOf)
	return h, nil
}

// modelHosts is where the runner's agents call their models, which they reach whatever their
// network: the verse agent's gateway where the runner runs it, Codex's API where it runs Codex,
// and Claude's where it runs Claude. A base URL that names no host is refused: no network could
// let the agent reach it.
func modelHosts(c Config, s Secrets) ([]string, error) {
	var hosts []string
	if _, ok := c.Agents[AgentName]; ok {
		host, err := verse.ModelHost(s.PortkeyBaseURL)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	if _, ok := c.Agents[codex.Agent]; ok {
		hosts = append(hosts, codex.ModelHost)
	}
	if _, ok := c.Agents[claude.Agent]; ok {
		hosts = append(hosts, claude.ModelHost)
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
	// ReachesVerse is whether the runner carries the verse system to its verse (Standalone's
	// toVerse), so the task's process offers its tools where it does.
	ReachesVerse bool `tony:"field=reachesVerse,omitzero"`
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
	tc.Config.reachesVerse = tc.ReachesVerse
	r := New(tc.Config, tc.Secrets, dir, log)
	r.callTool = tc.CallTool
	return runnerd.ServeTask(r.Build, tc.Config.Describe(tc.Networks), tc.Config.envOf)
}

// Runner builds the verse runner's tasks from their launch: `agent: verse` is verse's own agent
// (package verse), and a `script` runs by its #! line. The standalone runner builds it in each
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
	set = append(envSet(l.Vars()), set...)
	prompt, tools := l.Prompt, []verse.Tool(nil)
	var cl *delivery.Client
	if r.performs() {
		// The channel the runner performs over, and the repos it checked out: the tools over
		// both, and a brief about the repos where there are any.
		var ws *workspace.Workspace
		if ws, cl, err = r.checkout(l); err != nil {
			return ended(task.NewFailed(l.ID, reg, err.Error()))
		}
		performs := r.cfg.performs()
		tools = append(repoTools(cl, ws, r.cfg.atGitHub()), issueTools(cl, ws, l.Trackers(), r.cfg.atGitHub(), slices.Contains(performs, "linear-issue"))...)
		if slices.Contains(performs, "slack") {
			tools = append(tools, slackTools(cl)...)
		}
		if slices.Contains(performs, "secret") {
			tools = append(tools, secretTools(cl, ws, l.Environment)...)
		}
		if r.cfg.reachesVerse {
			tools = append(append(append(tools, verseTools(cl)...), verseDataTools(cl)...), verseCharterTools(cl)...)
		}
		tools = offered(tools, l.Environment)
		// The brief: how to deliver, where there are repos, and what reaches the world from
		// here, from the tools the task was left with, so a prompt that says "file an issue"
		// finds the tool rather than a shell that has no client for the tracker.
		if len(ws.Trees) > 0 {
			trailers := workspace.RequiredTrailers(&l)
			prompt = repoBrief(ws, trailers, &l) + prompt
			// A script has no brief, so the trailers its commits must end with are in its
			// environment, and in an agent's shell beside its brief. Set after the launch's
			// vars, and the last of a name is the one a process gets, so a spawn cannot
			// shadow them.
			set = append(set, TrailersEnv+"="+strings.Join(trailers, "\n"))
		}
		prompt = toolsBrief(tools, l.Environment) + prompt
	}
	if l.Script != "" {
		p, stop, err := newScriptTask(l, r.root, r.cfg.shellEnv(ScriptDoer), set, tools, r.callTool, reg, r.cfg.taskCeilings(ScriptDoer))
		if err != nil {
			if cl != nil {
				_ = cl.Close()
			}
			return ended(task.NewFailed(l.ID, reg, err.Error()))
		}
		cleanup = append(cleanup, stop)
		return ended(p)
	}
	who := r.cfg.doer(&l)
	l = r.cfg.withDefaults(l, who)
	if who == codex.Agent {
		// The codex agent (package codex): each command and patch it proposes is an action of
		// this loop, gated and narrated like the verse agent's own, and the runner's tools are
		// offered to it as its own. The same brief and the same delivery as the verse agent's.
		cc := r.cfg.codexConfig()
		if cc == nil {
			return ended(task.NewFailed(l.ID, reg, "this runner has no `agents.codex` section, so it cannot run codex"))
		}
		l.Prompt = prompt
		env := append(task.ShellEnv(r.cfg.shellEnv(who)), set...)
		p, err := codex.Task(l, reg, cc.Creds(r.secrets.CodexKey), env, r.root, tools, r.cfg.taskCeilings(who))
		if err != nil {
			return ended(task.NewFailed(l.ID, reg, err.Error()))
		}
		return ended(p)
	}
	if who == claude.Agent {
		// The claude agent (package claude), on the same terms as codex: each command, edit
		// and question it proposes is an action of this loop, and the runner's tools are
		// served to it as its own.
		cc := r.cfg.claudeConfig()
		if cc == nil {
			return ended(task.NewFailed(l.ID, reg, "this runner has no `agents.claude` section, so it cannot run claude"))
		}
		l.Prompt = prompt
		env := append(task.ShellEnv(r.cfg.shellEnv(who)), set...)
		p, err := claude.Task(l, reg, cc.Creds(r.secrets.ClaudeKeyEnv, r.secrets.ClaudeKey), env, r.root, tools, r.cfg.taskCeilings(who))
		if err != nil {
			return ended(task.NewFailed(l.ID, reg, err.Error()))
		}
		return ended(p)
	}
	switch _, offered := r.cfg.Agents[who]; {
	case who != AgentName || !offered:
		return ended(task.NewFailed(l.ID, reg, "this runner offers no agent "+who))
	case r.secrets.PortkeyAPIKey == "":
		return ended(task.NewFailed(l.ID, reg, "the verse agent has no model key on this runner"))
	}
	p := verse.Build(verse.Config{
		ID: l.ID, Prompt: prompt, Root: r.root, Resolver: reg, Log: r.log, Extra: tools,
		Answer: l.Answer, Environment: l.Environment, Autonomy: l.Autonomy, Ceilings: r.cfg.taskCeilings(who), Env: r.cfg.shellEnv(who),
		Set: set, ChildSet: envSet(l.Vars()),
		LLMBaseURL: r.secrets.PortkeyBaseURL, LLMKey: r.secrets.PortkeyAPIKey,
		LLMVirtualKey: cmp.Or(l.AgentSettings().Annotations[verse.AnnotationVirtualKey], r.secrets.PortkeyVirtualKey),
		LLMModel:      l.AgentSettings().Model(),
		Converse:      l.Converse,
	})
	// What it runs with, the configured model where the launch named none (withDefaults): the
	// record's to say.
	settings := map[string]string{}
	if model := l.AgentSettings().Model(); model != "" {
		settings[runner.SettingModel] = model
	}
	p.SetAgent(AgentName, settings)
	return ended(p)
}

// doer is who runs l: a script, the agent it names, or, for a prompt naming none, the first
// agent the runner offers (Config.agents, as Describe orders them), and the verse agent where it
// offers none, which then refuses.
func (c Config) doer(l *runner.Launch) string {
	if who := l.Who(); who != "" {
		return who
	}
	if agents := c.agents(); len(agents) > 0 {
		return agents[0]
	}
	return AgentName
}

// offered is the runner's tools whose verb the environment gives: a tool is its system's verb
// (systems.SplitTool), and one the environment does not give is not offered, so the doer never
// sees it.
func offered(tools []verse.Tool, env systems.Environment) []verse.Tool {
	return slices.DeleteFunc(slices.Clone(tools), func(t verse.Tool) bool {
		system, verb, ok := systems.SplitTool(t.Name)
		return ok && !env.Gives(systems.StepName(system, verb))
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
	lrs := l.Repos()
	repos := make([]workspace.Repo, len(lrs))
	for i, lr := range lrs {
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

// TrailersEnv names the variable holding the trailers every commit a task delivers must end
// with (workspace.RequiredTrailers), one per line, as the last paragraph of a commit message
// holds them: `git commit -m <subject> -m "$VERSE_TRAILERS"`. It is set where the task has a
// checkout. The push checks them against the launch's own values, so knowing them lets a task
// claim nothing but itself.
const TrailersEnv = "VERSE_TRAILERS"

// envSet is a launch's `with.env` as a shell's NAME=value list, in name order.
func envSet(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, k+"="+env[k])
	}
	return out
}
