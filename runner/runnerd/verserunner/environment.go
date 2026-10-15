package verserunner

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/task"
)

// knownTools is the llm driver's vocabulary, as harness.toolDefs offers it, plus the two this
// runner adds itself. Named here so an environment's floor can be checked at startup;
// `call_agent` and `fail` are deliberately absent from what an environment would list, being the
// loop's own rather than the work's.
//
// `push`, `fetch`, `create-pr` and `update-pr` are the runner's (runner/runnerd/delivery). `push` is offered only where some
// repository may be pushed to, which is why naming it in an environment where nothing writes is
// refused below: a floor that lists a tool nothing will offer is a floor that means nothing.
var knownTools = []string{"read_file", "list_dir", "write_file", "run_bash", "call_agent", delivery.PushToolName, delivery.FetchToolName, delivery.CreatePullToolName, delivery.UpdatePullToolName}

// driverLLM and task.DriverCalls are the two drivers this runner implements itself: an
// agent, and a list of calls the charter wrote. Every other driver name selects a program from
// `agents:` — see Command.
const driverLLM = "llm"

// builtinDriver reports whether this runner builds the driver itself rather than looking for a
// program under `agents:`.
func builtinDriver(d string) bool { return d == driverLLM || d == task.DriverCalls }

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

// Environment is what the deployment owns about a task's run: which agent runs it, what may run
// against the checkout, what its shell inherits, and what it may reach. The repositories, and
// what a task may push to each, are the spawn's (task.Launch.Repos).
type Environment struct {
	Name string `tony:"field=name"`
	// Driver is which agent runs the work here when a spawn does not say: `llm` (the
	// harness in this tree) or `claude` (the agent binary). A spawn's own `driver:` wins.
	Driver string `tony:"field=driver,omitzero"`
	// Commands is which binaries a task's work expects in its PATH. It is a claim about the
	// IMAGE and a statement to whoever reads the environment, not an enforcement: nothing checks
	// that the image agrees — an environment naming `go` in an image without it produces a task
	// whose commands are not found, with no better diagnosis than the agent's confusion — and
	// nothing narrows the PATH to it, since an absolute path walks straight past a PATH
	// allowlist. What a task can run is the sandbox's to bound.
	Commands []string `tony:"field=commands,omitzero"`
	// Tools is the set the `llm` driver gets when the spawn names none: which tools exist, as
	// against which the agent is advised to avoid. Empty = the driver's full set. A spawn that
	// names its own `tools:` gets what it names.
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
	// else is UNSET, which is the point — the runner's environment carries the git token and
	// the model credentials, and a task's shell is what the MODEL drives.
	// A floor that has to be enforced by a gate is not a floor.
	Env []string `tony:"field=env,omitzero"`
	// Egress is the allowlist of hosts a task here may reach: `api.anthropic.com`,
	// `proxy.golang.org`, `*.githubusercontent.com`, or any of those with an explicit `:port`.
	//
	// Declaring it IS the declaration of a restricted network, derived from this rather than
	// stated beside it, because the two cannot then disagree, and because "restricted" with no
	// list is a claim with no content. Empty is the runner's own network.
	//
	// A confiner that cannot build `restricted` refuses the launch rather than running the task
	// with the proxy present and unenforced, which is the whole difference between a floor and
	// a suggestion. On a machine without bwrap that means an environment with an allowlist
	// cannot run at all — correct, and inconvenient in exactly the way a refusal should be.
	Egress []string `tony:"field=egress,omitzero"`
}

// checkEnvironments is every check on the environments that cannot wait for a spawn: two
// environments with one name, a default naming nothing, a tool the agent does not have, an
// allowlist entry that does not parse.
func (c *Config) checkEnvironments() error {
	seen := map[string]bool{}
	for i, e := range c.Environments {
		switch {
		case e.Name == "":
			return fmt.Errorf("environment %d has no name — the name is what a "+
				"spawn says", i)
		case seen[e.Name]:
			return fmt.Errorf("two environments named %q — a spawn naming it "+
				"would get whichever was read first", e.Name)
		}
		// A tool the harness does not have fails the spawn rather than shrinking the set in
		// silence — the same rule harness.selectTools applies, checked here so a typo in a
		// FLOOR is caught when the runner starts rather than at the first firing.
		for _, name := range e.Tools {
			if !slices.Contains(knownTools, name) {
				return fmt.Errorf("environment %q offers tool %q, which the llm "+
					"driver does not have (it has: %s)", e.Name, name, strings.Join(knownTools, ", "))
			}
		}
		// An allowlist entry that does not parse is caught at STARTUP, because the alternative is
		// a runner that accepts the launch and then refuses every push on it — which reads to
		// whoever wrote the environment as the network being down.
		if len(e.Egress) > 0 {
			if _, err := runnerd.NewEgress(e.Name, e.Egress, nil); err != nil {
				return fmt.Errorf("environment %q: %w", e.Name, err)
			}
		}
		seen[e.Name] = true
	}
	for name, a := range c.Agents {
		if len(a.Argv) == 0 {
			return fmt.Errorf("agent %q has no argv — there is nothing to run", name)
		}
		if o := a.Output; o != "" && o != "text" && o != "json" {
			return fmt.Errorf("agent %q reads output as %q (want text or json)", name, o)
		}
	}
	if c.Default != "" && !seen[c.Default] {
		return fmt.Errorf("default environment %q is not declared", c.Default)
	}
	return nil
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
