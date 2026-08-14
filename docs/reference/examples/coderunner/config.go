package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/signadot/verse/codec"
)

// knownTools is the llm driver's vocabulary, as harness.toolDefs offers it, plus the one this
// runner adds itself. Named here so an environment's floor can be checked at startup;
// `call_agent` and `fail` are deliberately absent from what an environment would list, being the
// loop's own rather than the work's.
//
// `push` is this runner's (push.go, pushTool) and is offered only where the environment
// declares somewhere to push, which is why naming it without destinations is refused below: a
// floor that lists a tool nothing will offer is a floor that means nothing.
var knownTools = []string{"read_file", "list_dir", "write_file", "run_bash", "call_agent", pushToolName}

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
	// Credentials are the secrets a destination here may release, by name. They are NOT in
	// `env:` and must never be: that list is the allowlist a task's subprocesses inherit, which
	// is to say what the MODEL can read, and the whole arrangement is that a credential reaches
	// the task process and not the shell it drives.
	Credentials []Credential `tony:"field=credentials,omitzero"`
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
	// Push is the shorthand for a destination named `auto` — the one place work went when
	// there was only one. It is still what almost every environment writes, and it is a
	// spelling rather than a second mechanism: see Dests.
	//
	// EMPTY MEANS NOTHING LEAVES, and it is per repository on purpose: the ordinary workspace
	// is one repository being changed beside one or more that are there to be read, and a
	// reference repository that could push back is a blast radius nobody asked for.
	Push string `tony:"field=push,omitzero"`
	// Destinations are the named places this repository's work may go, and they are the
	// vocabulary a task pushes in: it asks for a NAME and never for a ref, so "where the work
	// goes is the environment's to say" survives a task that can ask for more than one thing.
	//
	// A table rather than a field is the whole change. `push:` admitted exactly one flow, so
	// anything else — merge this branch into main, move a promotion ref, retire a branch that
	// can no longer land — had no way to be asked for, and the obvious repair (a second
	// operation, then a third) puts a policy in a mechanism and leaves the next flow needing a
	// fourth. See docs/reference/examples/README.md.
	Destinations []Destination `tony:"field=destinations,omitzero"`
}

// autoDestination is what `deliver` has always meant: the branch a task's own work lands on.
// It is a name like any other now, and the finisher asks for it by that name.
const autoDestination = "auto"

// Destination is one named place a repository's work may go.
type Destination struct {
	// Name is what a task asks for, and it is the whole of what a task may say about where
	// work goes. A name this environment does not declare is refused; a ref is never named.
	Name string `tony:"field=name"`
	// Ref is where it goes, with $task expanded to the task id and $sha to the commit the
	// work started from — the same expansion `push:` has always had, and see Tree.PushTarget
	// for why $sha is in it.
	Ref string `tony:"field=ref"`
	// Credential is the name of one this environment declares, and it is what APPROVAL
	// RELEASES: the gate saying yes is what puts the secret in the task process, and nothing
	// else does. Empty falls back to the runner's own `-token-env` token, which is what every
	// environment used when the parent did the pushing.
	//
	// A PAT scopes per repository and not per ref, so `auto` and `main` in one repository
	// cannot be told apart by the credential. That is not a hole to plug here: it is what the
	// forge's own rules are for, and duplicating them would give two places to configure one
	// invariant and two places for them to disagree.
	Credential string `tony:"field=credential,omitzero"`
}

// Credential is a secret this environment can release to a task, named by the ENVIRONMENT
// VARIABLE it is read from rather than by its value — the same choice the model key and the git
// token already make, so a config file that is read, copied and committed carries no secret.
//
// Nothing here creates one. These are hand-made PATs, and a system that can mint credentials is
// a system whose compromise mints credentials. That is the design rather than a placeholder: if
// minting ever becomes cheap on the forge in use it narrows the window and changes nothing else.
type Credential struct {
	Name string `tony:"field=name"`
	Env  string `tony:"field=env"`
}

// Dests is this repository's destinations, with `push:` read as the one named `auto`.
//
// The shorthand is normalised HERE rather than in LoadConfig because a Repo is also built
// without ever passing through it — by a test, and by the task process, which is handed the
// environment the parent resolved rather than the file it came from. A normalisation that only
// happens on the config-reading path is one the other paths quietly disagree with, and the two
// sides disagreeing about which ref a destination names is exactly what childConfig.Bases
// exists to prevent one level down.
func (r Repo) Dests() []Destination {
	if len(r.Destinations) > 0 {
		return r.Destinations
	}
	if r.Push != "" {
		return []Destination{{Name: autoDestination, Ref: r.Push}}
	}
	return nil
}

// Dest looks one up by the name a task asked for.
func (r Repo) Dest(name string) (Destination, bool) {
	for _, d := range r.Dests() {
		if d.Name == name {
			return d, true
		}
	}
	return Destination{}, false
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
		if err := checkCreds(e); err != nil {
			return nil, err
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
			if err := checkDests(e.Name, e.Credentials, r); err != nil {
				return nil, err
			}
			dirs[r.Name] = true
		}
		if err := checkDelivering(e); err != nil {
			return nil, err
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

// checkDests is everything about one repository's destinations that cannot be discovered later.
//
// A destination is asked for BY NAME, so a name that is missing, doubled or attached to no ref
// is a request that resolves to nothing at the moment a task tries to deliver — which is the
// worst place to find out, because by then the work exists and the run is over.
func checkDests(env string, creds []Credential, r Repo) error {
	if r.Push != "" && len(r.Destinations) > 0 {
		// Two spellings of one table, and Dests would silently prefer the list. Refused rather
		// than merged: an operator who wrote both meant one of them, and which one is not a
		// thing to guess about the place work leaves through.
		return fmt.Errorf("coderunner: environment %q, repo %q declares both `push:` and "+
			"`destinations:` — `push:` IS the destination named %q, so write it once",
			env, r.Name, autoDestination)
	}
	seen := map[string]bool{}
	for i, d := range r.Destinations {
		switch {
		case d.Name == "":
			return fmt.Errorf("coderunner: environment %q, repo %q, destination %d has no name — "+
				"the name is the whole of what a task may say about where work goes", env, r.Name, i)
		case seen[d.Name]:
			return fmt.Errorf("coderunner: environment %q, repo %q has two destinations named %q — "+
				"a task asking for it would get whichever was read first", env, r.Name, d.Name)
		case d.Ref == "":
			return fmt.Errorf("coderunner: environment %q, repo %q, destination %q has no ref — "+
				"a destination that names nowhere is a name a task can ask for and nothing can "+
				"perform", env, r.Name, d.Name)
		case d.Credential != "" && !hasCredential(creds, d.Credential):
			// The gate approving a push is what releases the secret, so a destination naming a
			// credential nobody declared is one whose approval releases nothing — a task told
			// yes and then handed no way to act on it, at the end of a run.
			return fmt.Errorf("coderunner: environment %q, repo %q, destination %q wants "+
				"credential %q, which this environment does not declare (it has: %s)",
				env, r.Name, d.Name, d.Credential, strings.Join(credentialNames(creds), ", "))
		}
		seen[d.Name] = true
	}
	return nil
}

func hasCredential(creds []Credential, name string) bool {
	for _, c := range creds {
		if c.Name == name {
			return true
		}
	}
	return false
}

func credentialNames(creds []Credential) []string {
	if len(creds) == 0 {
		return []string{"none"}
	}
	out := make([]string, 0, len(creds))
	for _, c := range creds {
		out = append(out, c.Name)
	}
	return out
}

// checkCreds is the same rule the destinations follow, one level up: a name that resolves to
// nothing is caught when the config is read rather than when somebody's push needs it.
func checkCreds(e Environment) error {
	seen := map[string]bool{}
	for i, c := range e.Credentials {
		switch {
		case c.Name == "":
			return fmt.Errorf("coderunner: environment %q, credential %d has no name — the name "+
				"is what a destination asks for", e.Name, i)
		case seen[c.Name]:
			return fmt.Errorf("coderunner: environment %q has two credentials named %q", e.Name, c.Name)
		case c.Env == "":
			return fmt.Errorf("coderunner: environment %q, credential %q names no variable to "+
				"read it from — the NAME of a variable, because this file is read and copied and "+
				"must carry no secret", e.Name, c.Name)
		}
		seen[c.Name] = true
	}
	return nil
}

// checkDelivering refuses an environment where more than one repository has somewhere to send
// work, and refuses one that offers the push tool with nowhere to send it.
//
// ONE PUSH ACTION IS ONE REPOSITORY, which is what stops a mixed outcome from being a state
// anybody has to model: each action has its own gate, its own result, and its own place in the
// record. The cost lands on the CLOSING act, because `task.Finisher.Finish` returns one action
// and a workspace with N delivering repositories would need N — so this is where that gap is
// stated rather than left latent.
//
// It is refused rather than built for, on the evidence: every environment that exists declares
// exactly one repository that pushes, beside however many are there to be read. If a second one
// ever arrives the shape is `Finish` returning a SEQUENCE proposed once — one proposal, N
// actions, still at most once per quiescence — and not a finisher consulted repeatedly, which
// would make "finish" iterative and reword the loop's contract for nothing.
func checkDelivering(e Environment) error {
	var delivering []string
	for _, r := range e.Repos {
		if len(r.Dests()) > 0 {
			delivering = append(delivering, r.Name)
		}
	}
	if len(delivering) > 1 {
		return fmt.Errorf("coderunner: environment %q has %d repositories with destinations (%s) "+
			"— a task's closing delivery is ONE action, so it could deliver only the first and "+
			"the rest would be silently held. Give one repository destinations; the others are "+
			"there to be read", e.Name, len(delivering), strings.Join(delivering, ", "))
	}
	if len(delivering) == 0 && slices.Contains(e.Tools, pushToolName) {
		return fmt.Errorf("coderunner: environment %q offers the %q tool and declares no "+
			"destinations, so every use of it would be refused — declare where work may go, or "+
			"do not offer it", e.Name, pushToolName)
	}
	return checkReachable(e)
}

// checkReachable refuses an environment that can be approved to push somewhere it has no route
// to, which is the one half of the operational story a program can hold for itself.
//
// The task does the pushing now, so the forge has to be on the egress allowlist — and that
// widening is worth reading twice, because it is the one place in this design where "reachable"
// and "authorised" come apart. The allowlist is per network namespace and the model's SHELL
// shares it, so admitting github.com admits it for the shell too. What the shell does not get is
// a credential: that is on the other side of `buildEnv`, which is where the line actually is.
//
// Everything else in the operational half fails OPEN and cannot be checked from here —
// `receive.denyNonFastForwards` is what makes `--ff-only` more than a prompt, and
// `receive.denyDeletes` on a ref pattern a retiring destination targets stops a sweep silently.
// Both are the receiving end's, deliberately: verse does not compete with git server
// configuration, because humans push to these repositories too and an invariant enforced by the
// receiver applies to all of them.
func checkReachable(e Environment) error {
	if len(e.Egress) == 0 {
		return nil // no allowlist is no restriction: the containment is whatever else says
	}
	eg := &Egress{}
	for _, entry := range e.Egress {
		r, err := parseAllow(entry)
		if err != nil {
			return fmt.Errorf("coderunner: environment %q: %w", e.Name, err)
		}
		eg.rules = append(eg.rules, r)
	}
	for _, r := range e.Repos {
		if len(r.Dests()) == 0 {
			continue
		}
		u, err := url.Parse(r.Repo)
		if err != nil || u.Host == "" {
			continue // a local path, which needs no route at all
		}
		port := u.Port()
		if port == "" {
			port = "443"
			if u.Scheme == "http" {
				port = "80"
			}
		}
		if err := eg.permits(net.JoinHostPort(u.Hostname(), port)); err != nil {
			return fmt.Errorf("coderunner: environment %q may push %s to %s, and its egress "+
				"allowlist has no route there (%w) — a task would be approved and then fail to "+
				"reach the forge, at the end of a run",
				e.Name, r.Name, u.Host, err)
		}
	}
	return nil
}

// CredentialEnv is the variable a destination's credential is read from, and "" when it names
// none — in which case the caller falls back to the runner's own token, which is what every
// environment relied on while the parent did the pushing.
func (e Environment) CredentialEnv(d Destination) string {
	for _, c := range e.Credentials {
		if c.Name == d.Credential {
			return c.Env
		}
	}
	return ""
}

// UnsetCredentials names the credentials this environment declares whose variable is not set in
// this process, for a line at startup.
//
// Said early for the reason MissingLLMCreds is: the alternative is a task that runs, works, is
// approved, and then cannot push — at the end, with the work done, reported as somebody's
// authentication error. It is a WARNING rather than a refusal because a spawn here need not push
// at all, and refusing every task in an environment because one destination's PAT is missing
// would be the tail wagging the dog.
func (e Environment) UnsetCredentials() []string {
	var out []string
	for _, c := range e.Credentials {
		if os.Getenv(c.Env) == "" {
			out = append(out, fmt.Sprintf("%s ($%s)", c.Name, c.Env))
		}
	}
	return out
}

// MissingLLMCreds reports why the `llm` driver cannot run here, or "" if it can.
//
// The credential is named as a VARIABLE and read from the runner's own environment, which means
// there are two ways to have nothing: no `llm.apiKeyEnv` in the config at all, and a name that is
// not set in the process. Both produce the same thing at the model call — an empty key, and a
// vendor error about an invalid one — and neither says which variable anybody should look at.
//
// Checked at startup so it is visible before a spawn, and again at the spawn so it REFUSES rather
// than running a task that cannot possibly finish. That is the same rule the containment follows:
// a runner that cannot do what an environment asks says so instead of trying.
func (c *Config) MissingLLMCreds() string {
	if c.LLM.APIKeyEnv == "" {
		return "this runner's config sets no `llm.apiKeyEnv`, so the `llm` driver has no model " +
			"credential to use"
	}
	if os.Getenv(c.LLM.APIKeyEnv) == "" {
		return fmt.Sprintf("$%s is not set in this runner's environment, so the `llm` driver has "+
			"no model credential — it is read HERE and sent to the task process, so it must be "+
			"set for the runner rather than for verse or for your shell", c.LLM.APIKeyEnv)
	}
	return ""
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
