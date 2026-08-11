package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/signadot/verse/codec"
)

// knownTools is the llm driver's vocabulary, as harness.toolDefs offers it. Named here so an
// environment's floor can be checked at startup; `call_agent` and `fail` are deliberately
// absent from what an environment would list, being the loop's own rather than the work's.
var knownTools = []string{"read_file", "list_dir", "write_file", "run_bash", "call_agent"}

// driverLLM is the one driver this runner implements itself. Every other driver name selects a
// program from `agents:` — see Command.
const driverLLM = "llm"

func agentNames(m map[string]Command) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"none declared"}
	}
	return out
}

// Config is the runner's own file. It is NOT an entity: verse installs a runner as a url and
// nothing else, deliberately, so what a runner can be asked for is the runner's to declare and
// the operator's to deploy. See docs/runners.md.
type Config struct {
	// Environments are the workspaces this runner can build. A spawn names one
	// (`environment:` on a charter, `--environment` on `verse task run`) and verse carries
	// the name without resolving it — the same indirection `driver:` already is.
	Environments []Environment `tony:"field=environments"`
	// Default is the environment a spawn that named none gets. Empty means a spawn must
	// name one, which is the safer of the two and is what an operator serving several
	// repositories wants.
	Default string `tony:"field=default,omitzero"`
	// LLM is where the `llm` driver's model credentials come from, and it exists for the
	// reason `-token-env` does: a runner is a separate process from verse, so verse's own
	// configured creds are not visible to it, and without this the harness falls back to
	// whatever PORTKEY_* happens to be in the runner's environment. That is the ambient
	// credential this design refuses everywhere else — named here instead.
	LLM LLM `tony:"field=llm,omitzero"`
	// Agents are the external programs this runner can drive, by the name a spawn writes in
	// `driver:`. `claude` is an entry here rather than a type in the code, which is the point:
	// nothing in this runner knows what a coding agent is, only how to run a program and read
	// what it said.
	Agents map[string]Command `tony:"field=agents,omitzero"`
}

// LLM names where the model credentials live. Env-var NAMES rather than values, so a config
// file that is read, copied and committed carries no secret — the same choice the git token
// makes, for the same reason.
type LLM struct {
	BaseURL       string `tony:"field=baseUrl,omitzero"`
	APIKeyEnv     string `tony:"field=apiKeyEnv,omitzero"`
	VirtualKeyEnv string `tony:"field=virtualKeyEnv,omitzero"`
	Model         string `tony:"field=model,omitzero"`
}

// Environment is one workspace: where the code comes from, what may run against it, and where
// the result goes.
type Environment struct {
	Name string `tony:"field=name"`
	// Repos are the repositories checked out side by side into one working directory, each
	// under its own name. A LIST rather than a single repo because the work usually is: a
	// change to a service and its client, a fix in a library and the tree that consumes it,
	// or a repository that only needs reading to make sense of the one being changed. An
	// agent that can only see one of them can only guess about the rest.
	//
	// They are independent in everything but the branch: each has its own base, its own push
	// target (or none), and its own mirror, and the branch name is the task's so it is the
	// same in all of them.
	Repos []Repo `tony:"field=repos"`
	// Driver is which agent runs the work here when a spawn does not say: `llm` (the
	// harness in this tree) or `claude` (the agent binary). A spawn's own `driver:` wins.
	Driver string `tony:"field=driver,omitzero"`
	// Commands is the ceiling: which binaries exist in the task's PATH at all. It is a
	// claim about the IMAGE, and nothing here checks that the image agrees — an environment
	// naming `go` in an image without it produces a task whose commands are not found, with
	// no better diagnosis than the agent's confusion.
	//
	// Empty means the runner's whole PATH, which is the unconfined case and is only sane
	// while `filesystem.scope` is `shared` — a PATH allowlist is a convention until the
	// filesystem is restricted, since an absolute path walks straight past it.
	Commands []string `tony:"field=commands,omitzero"`
	// Tools is the FLOOR for the `llm` driver: which tools exist at all, as against which the
	// agent is advised to avoid. Empty = the driver's full set. A spawn's own `tools:` narrows
	// further; it cannot widen, because a tool absent here is never offered.
	//
	// This is where a shell is granted or withheld, and withholding it is the sharpest
	// containment available without a kernel. `read_file`, `list_dir` and `write_file` are
	// confined to the workspace by construction — harness.safePath refuses a path that
	// resolves outside the root — while `run_bash` gets the root as its working directory and
	// nothing else, so it is the one tool that can leave. An environment whose work is reading
	// and editing files does not need it, and a task that cannot run a shell cannot escape
	// through one whatever it decides to do.
	//
	// It also makes the autonomy dial mean something. A foreign agent is opaque, so a vector
	// has exactly two positions for it — invoke or do not — where every tool listed here is an
	// action the vector sees, gates, and narrates onto the read plane.
	Tools []string `tony:"field=tools,omitzero"`
	// Env is the passthrough allowlist: the variables a task's shell inherits. Everything
	// else is UNSET, which is the point — the harness today runs `bash -lc` with no
	// c.Env at all, so an agent's shell inherits the server's entire environment including
	// the model credentials the harness itself runs on (docs/roadmap/expose.md names this).
	// A floor that has to be enforced by a gate is not a floor.
	Env []string `tony:"field=env,omitzero"`
	// Egress is the allowlist of destinations a task here may reach: `api.anthropic.com`,
	// `proxy.golang.org`, `*.githubusercontent.com`, or any of those with an explicit `:port`.
	//
	// Declaring it IS the declaration of `network.egress: restricted` — the point is derived
	// from this rather than stated beside it, because the two cannot then disagree, and because
	// "restricted" with no list is a claim with no content. Empty leaves egress wherever the
	// rest of the containment puts it.
	//
	// A confiner that cannot build `restricted` refuses the launch rather than running the task
	// with the proxy present and unenforced, which is the whole difference between a floor and
	// a suggestion. On a machine without bwrap that means an environment with an allowlist
	// cannot run at all — correct, and inconvenient in exactly the way a refusal should be.
	Egress []string `tony:"field=egress,omitzero"`
	// Isolation names a containment profile this runner advertises. Empty = the runner's
	// defaults. A profile this runner cannot build refuses the launch rather than running
	// the work less contained than the environment said.
	Isolation string `tony:"field=isolation,omitzero"`
}

// Repo is one repository in a workspace.
type Repo struct {
	// Name is the directory it checks out into, and it is REQUIRED rather than derived from
	// the remote. It is what the agent sees and what a prompt refers to, so inferring it from
	// the last path segment of a url would let a rename upstream silently rename the
	// directory every charter's prompt mentions.
	Name string `tony:"field=name"`
	// Repo is a git remote or a local path. It is fetched into a bare mirror shared by every
	// task using it, so N concurrent tasks cost one object store per repository.
	Repo string `tony:"field=repo"`
	// Base is the ref a worktree starts from — a branch, a tag, a sha. Empty is refused
	// rather than defaulted to HEAD: which ref work starts from must not be guessed.
	Base string `tony:"field=base"`
	// Push is the ref this repository's branch goes to, with $task expanded to the task id.
	// EMPTY MEANS NOTHING LEAVES, and it is per repository on purpose: the ordinary workspace
	// is one repository being changed beside one or more that are there to be read, and a
	// reference repository that could push back is a blast radius nobody asked for.
	Push string `tony:"field=push,omitzero"`
}

// LoadConfig reads and checks the runner's config. Every check here is about something that
// cannot be discovered later: a workspace with no repo, two workspaces with one name, a
// default naming nothing.
func LoadConfig(path string) (*Config, error) {
	var c Config
	if err := codec.ReadFile(path, &c); err != nil {
		return nil, fmt.Errorf("coderunner: read %s: %w", path, err)
	}
	if len(c.Environments) == 0 {
		return nil, fmt.Errorf("coderunner: %s declares no environments — a runner with no "+
			"workspace can accept no spawn", path)
	}
	seen := map[string]bool{}
	for i, e := range c.Environments {
		switch {
		case e.Name == "":
			return nil, fmt.Errorf("coderunner: environment %d has no name — the name is what a "+
				"spawn says", i)
		case seen[e.Name]:
			return nil, fmt.Errorf("coderunner: two environments named %q — a spawn naming it "+
				"would get whichever was read first", e.Name)
		case len(e.Repos) == 0:
			return nil, fmt.Errorf("coderunner: environment %q has no repos", e.Name)
		}
		if d := e.Driver; d != "" && d != driverLLM {
			if _, ok := c.Agents[d]; !ok {
				return nil, fmt.Errorf("coderunner: environment %q wants driver %q, which is "+
					"neither %q nor an agent this runner declares (%s)",
					e.Name, d, driverLLM, strings.Join(agentNames(c.Agents), ", "))
			}
		}
		// A tool the harness does not have fails the spawn rather than shrinking the set in
		// silence — the same rule harness.selectTools applies, checked here so a typo in a
		// FLOOR is caught when the runner starts rather than at the first firing.
		for _, name := range e.Tools {
			if !slices.Contains(knownTools, name) {
				return nil, fmt.Errorf("coderunner: environment %q offers tool %q, which the llm "+
					"driver does not have (it has: %s)", e.Name, name, strings.Join(knownTools, ", "))
			}
		}
		// An allowlist entry that does not parse is caught at STARTUP, because the alternative is
		// a runner that accepts the launch and then refuses every destination on it — which
		// reads to whoever wrote the environment as the network being down.
		for _, entry := range e.Egress {
			if _, err := parseAllow(entry); err != nil {
				return nil, fmt.Errorf("coderunner: environment %q: %w", e.Name, err)
			}
		}
		dirs := map[string]bool{}
		for j, r := range e.Repos {
			switch {
			case r.Name == "":
				return nil, fmt.Errorf("coderunner: environment %q, repo %d has no name — it is "+
					"the directory the agent sees", e.Name, j)
			case dirs[r.Name]:
				return nil, fmt.Errorf("coderunner: environment %q has two repos named %q — they "+
					"would check out over each other", e.Name, r.Name)
			case strings.ContainsAny(r.Name, `/\`):
				return nil, fmt.Errorf("coderunner: environment %q, repo %q: a name is one "+
					"directory, so it may not contain a separator", e.Name, r.Name)
			case r.Repo == "":
				return nil, fmt.Errorf("coderunner: environment %q, repo %q has no repo", e.Name, r.Name)
			case r.Base == "":
				return nil, fmt.Errorf("coderunner: environment %q, repo %q has no base — which "+
					"ref work starts from is not a thing to guess", e.Name, r.Name)
			}
			dirs[r.Name] = true
		}
		seen[e.Name] = true
	}
	for name, a := range c.Agents {
		if len(a.Argv) == 0 {
			return nil, fmt.Errorf("coderunner: agent %q has no argv — there is nothing to run", name)
		}
		if o := a.Output; o != "" && o != "text" && o != "json" {
			return nil, fmt.Errorf("coderunner: agent %q reads output as %q (want text or json)", name, o)
		}
	}
	if c.Default != "" && !seen[c.Default] {
		return nil, fmt.Errorf("coderunner: default environment %q is not declared", c.Default)
	}
	return &c, nil
}

// Environment resolves the name a spawn carried. A name this runner does not have is an
// error rather than a fallback: the charter asked for a workspace, and running the work in a
// different one is the same class of mistake as a misspelled runner that runs in-process.
func (c *Config) Environment(name string) (Environment, error) {
	if name == "" {
		if c.Default == "" {
			return Environment{}, fmt.Errorf("this spawn names no environment and this runner "+
				"has no default — name one of: %s", strings.Join(c.names(), ", "))
		}
		name = c.Default
	}
	for _, e := range c.Environments {
		if e.Name == name {
			return e, nil
		}
	}
	return Environment{}, fmt.Errorf("no environment %q on this runner — it has: %s",
		name, strings.Join(c.names(), ", "))
}

func (c *Config) names() []string {
	out := make([]string, 0, len(c.Environments))
	for _, e := range c.Environments {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}
