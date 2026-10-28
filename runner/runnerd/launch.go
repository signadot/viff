package runnerd

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/signadot/verse/systems"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// CheckLaunch is what a runner built on runnerd checks a launch against when it arrives: the
// EnvOf is the variables of a runner's environment a launch's task shell is given, by name: its
// doer's. nil, or a nil answer, is task.DefaultShellEnv.
type EnvOf func(*runner.Launch) []string

// of is the variables e gives l's shell.
func (e EnvOf) of(l *runner.Launch) []string {
	if e == nil {
		return nil
	}
	return e(l)
}

// spec's shape (Launch.Check), then what no runnerd runner can honour yet, then what this
// runner offers (desc). owned is the variables the runner gives a task's shell itself, which
// the environment's vars may not name; nil is task.DefaultShellEnv. A handler with no offers — one a test or
// an example builds — checks the first two only.
//
// What is refused is said, with the field: a launch that ran without what it asked for would
// be the failure that matters, since the charter downstream is written believing it held.
func CheckLaunch(l *runner.Launch, desc runner.Description, owned []string) error {
	if err := l.Check(); err != nil {
		return err
	}
	if err := notYet(l); err != nil {
		return err
	}
	if owned == nil {
		owned = task.DefaultShellEnv
	}
	for name := range l.Vars() {
		if slices.Contains(owned, name) || name == task.CredentialFileEnv {
			return fmt.Errorf("environment.vars: %s is the runner's to set, not the launch's", name)
		}
	}
	return CheckOffer(l, desc)
}

// CheckOffer checks a launch against what a runner offers (its description): that its doer is
// one of its offers, and that what the launch asks of the offer — the agent's settings, systems
// it holds (and repositories within its `git` ceiling), a network it builds, a conversation,
// rows on systems it holds — is what the offer takes. A runner with no offers takes no repos and
// otherwise refuses nothing. Verse makes this check with the description it read from the
// runner, so a spawn is refused where it is written rather than at its first firing.
//
// The launch is bound: a `$` in a value is part of it (systems.Ceiling.Contains). A charter's
// spawn, checked at install before anything is bound, is CheckOfferAsWritten's.
func CheckOffer(l *runner.Launch, desc runner.Description) error { return checkOffer(l, desc, false) }

// CheckOfferAsWritten is CheckOffer for a charter's spawn at install, its environment as written:
// a `$` attribute is within a ceiling where some binding of it could be
// (systems.Ceiling.ContainsAsWritten). The launch is checked again, bound, by CheckOffer.
func CheckOfferAsWritten(l *runner.Launch, desc runner.Description) error {
	return checkOffer(l, desc, true)
}

func checkOffer(l *runner.Launch, desc runner.Description, written bool) error {
	if len(desc.Offers) == 0 {
		if len(l.Repos()) > 0 {
			return unsupported("a git repository")
		}
		return nil
	}
	o, err := offerFor(l.Who(), desc)
	if err != nil {
		return err
	}
	// The agent's settings, each one its offer takes and a value it takes; its annotations,
	// each one the offer names.
	a := l.AgentSettings()
	if o.Agent != nil {
		for _, key := range slices.Sorted(maps.Keys(a.Settings)) {
			setting, ok := o.Agent.Settings[key]
			if !ok {
				return fmt.Errorf("agent.%s: %s on this runner takes no `%s` (it takes: %s)", key, o.Name(), key,
					cmp.Or(strings.Join(slices.Sorted(maps.Keys(o.Agent.Settings)), ", "), "none"))
			}
			if err := setting.Takes(a.Settings[key]); err != nil {
				return fmt.Errorf("agent.%s: %s on this runner takes no such %s: %w", key, o.Name(), key, err)
			}
		}
		for name := range a.Annotations {
			if _, ok := o.Agent.Annotations[name]; !ok {
				return fmt.Errorf("agent.annotations: %s takes no %q on this runner", o.Name(), name)
			}
		}
	}
	// A system of the environment is one the offer holds: the runner sets it up and performs
	// or stops its steps. The network is built by the sandbox, and the offer says which modes.
	for _, name := range slices.Sorted(maps.Keys(l.Environment.Systems)) {
		if name == "network" {
			continue
		}
		if _, held := o.Systems[name]; !held {
			return fmt.Errorf("environment.systems.%s: %s on this runner holds no `%s`", name, o.Name(), name)
		}
	}
	if err := checkCeilings(l.Environment, o, written); err != nil {
		return err
	}
	// The network is built when the run starts, absent being none: one the runner can't build is
	// refused here, where the spawn is written, rather than at the launch, and said as that.
	if err := checkBuilds(l, o); err != nil {
		return err
	}
	// What the doer can't run without, refused where it is written rather than run to fail.
	for _, step := range o.Requires {
		if !l.Environment.Gives(step) {
			system, verb, _ := systems.Split(step)
			return fmt.Errorf("environment.systems.%s: %s on this runner requires %s: add `%s: {}` or `%s: {%s: {}}` under environment.systems",
				system, o.Name(), step, system, system, verb)
		}
	}
	if l.Converse && !o.Converse {
		return fmt.Errorf("converse: %s on this runner can't hold a conversation", o.Name())
	}
	return nil
}

// checkBuilds is what the runner can build of each system the environment gives (Builds, its
// nature, apart from the operator's ceiling): today the network's modes. A run with no network
// block has the network none, which the runner has to build too.
func checkBuilds(l *runner.Launch, o runner.Offer) error {
	for name, sys := range o.Systems {
		if len(sys.Builds) == 0 {
			continue
		}
		blocks := l.Environment.Systems[name]
		if name == "network" && len(blocks) == 0 {
			blocks = []systems.Block{{}}
		}
		for _, b := range blocks {
			if err := sys.Builds.Contains(name, b); err != nil {
				if name == "network" && len(sys.Builds) == 1 {
					mode := cmp.Or(b.Attributes["mode"], runner.NetworkNone)
					if modes := sys.Builds[0].Attributes["mode"]; !slices.Contains(modes, mode) {
						return fmt.Errorf("environment.systems.network: mode %s: this runner builds %s only", mode, strings.Join(modes, ", "))
					}
				}
				return fmt.Errorf("environment.systems.%s: this runner can't build it (%s): %w", name, buildsWords(name, sys.Builds), err)
			}
		}
	}
	return nil
}

// buildsWords is what a runner builds of a system, as a refusal says it.
func buildsWords(system string, builds systems.Ceiling) string {
	if system == "network" && len(builds) == 1 {
		return "it builds: " + strings.Join(builds[0].Attributes["mode"], ", ")
	}
	return "its offer's `builds` says what it builds"
}

// checkCeilings is CONTAINMENT: each block of the environment lies within the ceiling of its
// system the offer publishes (systems.Ceiling.Contains), a ceiling being closed, so a system
// held with none gives nothing. The network is the one whose empty ceiling bounds nothing beyond
// what the runner builds. Held at install on the environment as the charter wrote it (written: a
// `$` attribute within where some binding of it could be), and at the launch on the bound one.
// The autonomy is not held to the ceiling: each row reaches the environment (runner.CheckReach),
// which lies within it.
func checkCeilings(env systems.Environment, o runner.Offer, written bool) error {
	names := slices.Sorted(maps.Keys(env.Systems))
	if _, given := env.Systems["network"]; !given {
		names = append(names, "network")
	}
	for _, name := range names {
		sys, held := o.Systems[name]
		if !held || (name == "network" && len(sys.Ceiling) == 0) {
			continue
		}
		blocks := env.Systems[name]
		if name == "network" && len(blocks) == 0 {
			blocks = []systems.Block{{}} // no network block is the network none, bounded too
		}
		for i, b := range blocks {
			contains := sys.Ceiling.Contains
			if written {
				contains = sys.Ceiling.ContainsAsWritten
			}
			if err := contains(name, b); err != nil {
				at := "environment.systems." + name
				if len(blocks) > 1 {
					at = fmt.Sprintf("%s[%d]", at, i)
				}
				return fmt.Errorf("%s: beyond this runner's ceiling for %s: %w", at, name, err)
			}
		}
	}
	return nil
}

// CheckAutonomy is what a running task's new autonomy is checked against (PUT
// /tasks/{id}/autonomy): the shape, and that each row reaches the task's environment, which is
// fixed for the run and lies within the runner's ceilings (runner.CheckReach).
func CheckAutonomy(p task.Autonomy, l *runner.Launch) error {
	if err := p.Check(); err != nil {
		return fmt.Errorf("autonomy%w", err)
	}
	return runner.CheckReach(p, l.Environment)
}

// notYet refuses what a launch may say and no runnerd runner can honour yet, naming the field:
// refused, never ignored, until the step of the runners plan that brings it.
func notYet(l *runner.Launch) error {
	if l.Script != "" && l.Answer != nil {
		return unsupported("an `answer` for a script")
	}
	return nil
}

func unsupported(what string) error {
	return fmt.Errorf("this runner doesn't support %s yet", what)
}

// offerFor is the offer a doer runs under.
// doerName is who does a launch's work, as a task's record says it: the agent's name, the
// runner's default agent's for a prompt that names none, or "script".
func doerName(l *runner.Launch, desc runner.Description) string {
	if o, err := offerFor(l.Who(), desc); err == nil {
		return o.Name()
	}
	return cmp.Or(l.Who(), "agent")
}

func offerFor(who string, desc runner.Description) (runner.Offer, error) {
	var agents []string
	for _, o := range desc.Offers {
		switch {
		case who == "script" && o.Script:
			return o, nil
		case who != "script" && o.Agent != nil && (who == "" || o.Agent.Name == who):
			// No agent named is the runner's default: its first agent offer.
			return o, nil
		case o.Agent != nil:
			agents = append(agents, o.Agent.Name)
		}
	}
	if who == "script" {
		return runner.Offer{}, fmt.Errorf("this runner doesn't run scripts")
	}
	if who == "" {
		return runner.Offer{}, errors.New("this runner offers no agent for a prompt")
	}
	slices.Sort(agents)
	return runner.Offer{}, fmt.Errorf("this runner offers no agent %q (it offers: %s)", who,
		cmp.Or(strings.Join(agents, ", "), "none"))
}

// errNoRepos refuses a launch that names repos on a handler that checks none out.
var errNoRepos = errors.New("environment.systems.git: this runner checks out no repos")

// errNoNetwork refuses a network other than open on a handler whose tasks run in its own
// process.
var errNoNetwork = errors.New("environment.systems.network: this runner runs its tasks in its own process, " +
	"and can't take the network away from them; only open")

// readLaunch reads and checks POST /tasks' launch. A refusal is answered `400` with the reason,
// and ok is false.
//
// It is read STRICTLY: a key this runner doesn't know is refused rather than dropped, so a
// launch written for a newer or older contract fails where it can be read instead of running
// without what it said.
func readLaunch(w http.ResponseWriter, r *http.Request, check func(*runner.Launch) error) (runner.Launch, bool) {
	var l runner.Launch
	if err := codec.ReadStrict(r, &l); err != nil {
		http.Error(w, fmt.Sprintf("the launch does not parse: %v", err), http.StatusBadRequest)
		return l, false
	}
	if err := check(&l); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return l, false
	}
	return l, true
}
