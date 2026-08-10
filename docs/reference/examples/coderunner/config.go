package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/signadot/verse/codec"
)

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
	// Env is the passthrough allowlist: the variables a task's shell inherits. Everything
	// else is UNSET, which is the point — the harness today runs `bash -lc` with no
	// c.Env at all, so an agent's shell inherits the server's entire environment including
	// the model credentials the harness itself runs on (docs/roadmap/expose.md names this).
	// A floor that has to be enforced by a gate is not a floor.
	Env []string `tony:"field=env,omitzero"`
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
		if d := e.Driver; d != "" && d != "llm" && d != "claude" {
			return nil, fmt.Errorf("coderunner: environment %q wants driver %q (want llm or claude)",
				e.Name, d)
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
