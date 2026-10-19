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
// spec's shape (Launch.Check), then what no runnerd runner can honour yet, then what this
// runner offers (desc). owned is the variables the runner gives a task's shell itself, which
// `with.env` may not name; nil is task.DefaultShellEnv. A handler with no offers — one a test or
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
	for name := range l.Doer.With.Env {
		if slices.Contains(owned, name) || name == task.CredentialFileEnv {
			return fmt.Errorf("with.env: %s is the runner's to set, not the launch's", name)
		}
	}
	return CheckOffer(l, desc)
}

// CheckOffer checks a launch against what a runner offers (its description): that the doer is
// one of its offers, and that what the launch asks of the offer — `with`, repositories within
// its `git` ceiling, annotations, a conversation, rows on systems it holds — is what the offer
// takes. A runner with no offers takes no repos and otherwise refuses nothing. Verse
// makes this check with the description it read from the runner, so a spawn is refused where
// it is written rather than at its first firing.
func CheckOffer(l *runner.Launch, desc runner.Description) error {
	if len(desc.Offers) == 0 {
		if len(l.Doer.With.Git) > 0 {
			return unsupported("with.git")
		}
		return nil
	}
	o, err := offerFor(l.Doer, desc)
	if err != nil {
		return err
	}
	for key, v := range map[string]string{"model": l.Doer.With.Model, "effort": l.Doer.With.Effort} {
		if v == "" {
			continue
		}
		accepts, ok := o.With[key]
		if !ok {
			return fmt.Errorf("with: %s takes no `%s`", l.Doer.Name(), key)
		}
		if !accepts.Accept(v) {
			return fmt.Errorf("with.%s: %s takes none called %q", key, l.Doer.Name(), v)
		}
	}
	// A repository is a fact of the `git` system: the offer holds it, and its ceiling lets a
	// task reach the repository at all. One whose every git call the ceiling refuses, a forge
	// the runner has no repositories on among them, is a repository this runner does not serve.
	git, holdsGit := o.Systems["git"]
	if len(l.Doer.With.Git) > 0 && !holdsGit {
		return fmt.Errorf("with.git: %s on this runner intercepts no `git`, so it checks out no repositories", l.Doer.Name())
	}
	// A tracker is a fact of the `git-issue` system, as a repository is of `git`.
	if _, holds := o.Systems["git-issue"]; len(l.Doer.With.GitIssue) > 0 && !holds {
		return fmt.Errorf("with.git-issue: %s on this runner intercepts no `git-issue`, so it keeps no trackers", l.Doer.Name())
	}
	for i, r := range l.Doer.With.Git {
		c := git.Ceiling
		if row, at, refused := c.Refuses(task.Step{Name: task.StepGitFetch, Fields: map[string]string{"repo": r.Canonical()}}); refused {
			return fmt.Errorf("with.git[%d]: this runner's ceiling refuses git on %s (its row %s: %s)",
				i, r.Canonical(), c.NameOf(at), rowWords(row))
		}
	}
	// The network is a fact of the run, absent being none: one the runner does not build is
	// refused here, where the spawn is written, rather than at the launch. An offer that names
	// no network builds none a launch may ask for.
	if accepts := o.With["network"]; !accepts.Accept(l.Doer.With.NetworkMode()) {
		return fmt.Errorf("with.network: %s on this runner builds the network %s, not %s (absent is none)",
			l.Doer.Name(), strings.Join(accepts.Values, " or "), l.Doer.With.NetworkMode())
	}
	for name := range l.Doer.Annotations {
		if _, ok := o.Annotations[name]; !ok {
			return fmt.Errorf("annotations: %s takes no %q on this runner", l.Doer.Name(), name)
		}
	}
	if l.Converse && !o.Converse {
		return fmt.Errorf("converse: %s on this runner can't hold a conversation", l.Doer.Name())
	}
	if err := checkPolicy(l.Policy, l.Doer.Name(), o); err != nil {
		return err
	}
	return checkCeilings(l.Policy, l.Doer.With, o)
}

// checkCeilings is CONTAINMENT: a launch's policy opens nothing the runner's ceilings close.
// Each row that allows or asks about a held system's calls is held against that system's
// ceiling, field by field: the row's params as the call's known fields (a pattern read as the
// text it is, so a pattern within a pattern is the narrower written inside the wider), and,
// where the row names no `repo`, each repository of `with.git` in turn. A row the ceiling
// refuses every call of is refused here, naming both lines. A row naming no system, the
// default row among them, opens nothing by name, and the ceiling decides each of its calls as
// they are made.
func checkCeilings(p task.Policy, w runner.With, o runner.Offer) error {
	for i, r := range p {
		if r.Match.Any() || r.Match.System == "" || r.Then.Mode == task.ThenDeny || r.Then.Mode == task.ThenWithhold {
			continue
		}
		c := o.Systems[r.Match.System].Ceiling
		doc, _ := systems.Get(r.Match.System)
		var verbs []string
		if r.Match.Verb != "" {
			verbs = []string{r.Match.Verb}
		} else {
			for _, v := range doc.Verbs {
				verbs = append(verbs, v.Name)
			}
		}
		for _, verb := range verbs {
			step := task.Step{Name: systems.StepName(r.Match.System, verb), Fields: map[string]string{}}
			for k, v := range r.Match.Params {
				step.Fields[k] = v
			}
			shapes := []task.Step{step}
			v, _ := doc.Verb(verb)
			if _, named := step.Fields["repo"]; !named && len(w.Git) > 0 && slices.Contains(v.ParamNames(), "repo") {
				shapes = nil
				for _, repo := range w.Git {
					st := task.Step{Name: step.Name, Fields: maps.Clone(step.Fields)}
					st.Fields["repo"] = repo.Canonical()
					shapes = append(shapes, st)
				}
			}
			for _, st := range shapes {
				if row, at, refused := c.Refuses(st); refused {
					return fmt.Errorf("policy[%d]: this runner's ceiling refuses what the row would %s (%s): its row %s: %s",
						i, r.Then.Mode, st, c.NameOf(at), rowWords(row))
				}
			}
		}
	}
	return nil
}

// rowWords is a ceiling's row in words, for a refusal that names it.
func rowWords(r task.Row) string {
	var fs []string
	if r.Match != nil {
		if r.Match.Verb != "" {
			fs = append(fs, "verb="+r.Match.Verb)
		}
		for _, k := range slices.Sorted(maps.Keys(r.Match.Params)) {
			fs = append(fs, k+"="+r.Match.Params[k])
		}
	}
	if len(fs) == 0 {
		return r.Then.Mode + " everything"
	}
	return r.Then.Mode + " " + strings.Join(fs, " ")
}

// checkPolicy refuses a row on a system the doer's offer does not intercept, since nothing
// there could honour it. A row naming no system covers whatever the offer holds,
// and is not checked.
func checkPolicy(p task.Policy, doer string, o runner.Offer) error {
	for i, r := range p {
		if r.Match.Any() || r.Match.System == "" {
			continue
		}
		if _, held := o.Systems[r.Match.System]; !held {
			return fmt.Errorf("policy[%d]: %s on this runner intercepts no %s, so there is nothing to %s",
				i, doer, r.Match.System, r.Then.Mode)
		}
	}
	return nil
}

// CheckPolicy is what a running task's new policy is checked against (PUT /tasks/{id}/policy):
// the shape, that it withholds what the launch's policy (prev) withheld and no more, that it
// names no verb prev did not, that each row's system is one the task's doer holds, and that it
// opens nothing the runner's ceilings close (checkCeilings), with the task's repositories as
// they are.
func CheckPolicy(p task.Policy, prev task.Policy, doer string, w runner.With, desc runner.Description) error {
	if err := p.Check(); err != nil {
		return err
	}
	if err := p.OrDefault().KeepsWithheld(prev.OrDefault()); err != nil {
		return err
	}
	if err := p.KeepsVerbs(prev); err != nil {
		return err
	}
	if len(desc.Offers) == 0 {
		return nil
	}
	o, err := offerFor(doerOf(doer), desc)
	if err != nil {
		return err
	}
	if err := checkPolicy(p, doer, o); err != nil {
		return err
	}
	return checkCeilings(p, w, o)
}

// doerOf is the doer a task's name for it (the agent's name, or "script") stands for.
func doerOf(doer string) runner.Doer {
	if doer == "script" {
		return runner.Doer{Script: "-"}
	}
	return runner.Doer{Agent: doer}
}

// notYet refuses what a launch may say and no runnerd runner can honour yet, naming the field:
// refused, never ignored, until the step of the runners plan that brings it.
func notYet(l *runner.Launch) error {
	if l.Doer.Script != "" && l.Answer != nil {
		return unsupported("an `answer` for a script")
	}
	return nil
}

func unsupported(what string) error {
	return fmt.Errorf("this runner doesn't support %s yet", what)
}

// offerFor is the offer a doer runs under.
func offerFor(d runner.Doer, desc runner.Description) (runner.Offer, error) {
	var agents []string
	for _, o := range desc.Offers {
		switch {
		case d.Script != "" && o.Script:
			return o, nil
		case d.Agent != "" && o.Agent == d.Agent:
			return o, nil
		case o.Agent != "":
			agents = append(agents, o.Agent)
		}
	}
	if d.Script != "" {
		return runner.Offer{}, fmt.Errorf("this runner doesn't run scripts")
	}
	slices.Sort(agents)
	return runner.Offer{}, fmt.Errorf("this runner offers no agent %q (it offers: %s)", d.Agent,
		cmp.Or(strings.Join(agents, ", "), "none"))
}

// errNoRepos refuses a launch that names repos on a handler that checks none out.
var errNoRepos = errors.New("with.git: this runner checks out no repos")

// errNoNetwork refuses a network other than open on a handler whose tasks run in its own
// process.
var errNoNetwork = errors.New("with.network: this runner runs its tasks in its own process, " +
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
