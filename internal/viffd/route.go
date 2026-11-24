package viffd

import (
	"cmp"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.viff.dev/viff/internal/trigger"
	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/launch"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/client"
	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/task"
)

// WHERE A SPAWN RUNS is viff's to say. A spawn names its doer, an agent or a script, and what it
// may reach; viff finds a runner that offers the doer and holds the environment, and launches it
// there. Nothing on a spawn names a runner: a runner is the deployment's, and a charter that named
// one carried the deployment through every rule that spawned.
//
// The steps, at every launch (route):
//
//  1. Candidates: the registrations that are described (api.Runner.Current) and offer the doer,
//     the local runner `viff up` starts among them. A registration with no description is no
//     candidate: the hello, the probe at start and `viff runner check` describe it, never a
//     spawn.
//  2. Fit: the candidates whose offer takes the launch (runnerd.CheckOffer).
//  3. Load: fewest tasks on record not yet ended, then by name.
//  4. Handshake: the first is asked what it is now (handshake); one that does not prove itself,
//     or does not answer, is passed over for the next.
//
// At an install, and before a hand-written spawn's record is written, steps 1 and 2 alone say
// whether the spawn would run anywhere (fits).

// DefaultAgent is the agent a prompt naming none is for, where the Server names none.
const DefaultAgent = "viff"

// placed is where a launch went: the runner, and how the task's record names it.
type placed struct {
	launcher client.Launcher
	name     string // the registration's address, `mirror.runners.<name>`
	subject  string // the subject the registration names
}

// candidate is a runner a launch could go to, with what it offers.
type candidate struct {
	reg  *api.Runner
	desc runner.Description
}

func (c candidate) name() string { return runnerEntityRef(c.reg.Name).String() }

// defaultAgent is the agent a prompt naming none is for.
func (s *Server) defaultAgent() string { return cmp.Or(s.DefaultAgent, DefaultAgent) }

// withDefaultAgent is spec with a prompt that names no agent bound to viff's default, so the
// launch, the routing and the task's record all name the agent that runs it.
func (s *Server) withDefaultAgent(spec launch.Spec) launch.Spec {
	if spec.Prompt == "" || (spec.Agent != nil && spec.Agent.Name != "") {
		return spec
	}
	a := runner.Agent{}
	if spec.Agent != nil {
		a = *spec.Agent
	}
	a.Name = s.defaultAgent()
	spec.Agent = &a
	return spec
}

// bindDefaultAgent is withDefaultAgent for a launch.
func (s *Server) bindDefaultAgent(l *runner.Launch) {
	if l.Prompt == "" || (l.Agent != nil && l.Agent.Name != "") {
		return
	}
	a := runner.Agent{}
	if l.Agent != nil {
		a = *l.Agent
	}
	a.Name = s.defaultAgent()
	l.Agent = &a
}

// doerOf is what a launch asks for, as a refusal says it.
func doerOf(l *runner.Launch) string {
	if l.Script != "" {
		return "scripts"
	}
	if l.Agent != nil && l.Agent.Name != "" {
		return "the agent " + l.Agent.Name
	}
	return "the default agent"
}

// candidates is step 1: the runners that offer the launch's doer, read in viff's own view, and
// the registrations passed over, for having no description or for being none this viff instance reads
// (each as the refusal names it). on, where set, holds the launch to one runner, by the address a
// task's record names it by.
func (s *Server) candidates(l *runner.Launch, on *string) (offer []candidate, passed []string, err error) {
	regs, bad, err := readRunners(s.Store)
	if err != nil {
		return nil, nil, errors.New(notRead("the runners' registrations", err))
	}
	for _, b := range bad {
		if on == nil || *on == b.Ref.String() {
			passed = append(passed, fmt.Sprintf("%s, unreadable: %v", b.Ref, b.Err))
		}
	}
	for i := range regs {
		e := regs[i]
		if on != nil && *on != runnerEntityRef(e.Name).String() {
			continue
		}
		if !e.Current() {
			passed = append(passed, fmt.Sprintf("%s, not described (`viff runner check %s` says why)", runnerEntityRef(e.Name), e.Name))
			continue
		}
		if offersDoer(e.Description(), l) {
			offer = append(offer, candidate{reg: &e, desc: e.Description()})
		}
	}
	sort.Strings(passed)
	return offer, passed, nil
}

// offersDoer is whether desc offers what l asks for: scripts, or the agent it names.
func offersDoer(desc runner.Description, l *runner.Launch) bool {
	for _, o := range desc.Offers {
		switch {
		case l.Script != "" && o.Script:
			return true
		case l.Script == "" && o.Agent != nil && l.Agent != nil && o.Agent.Name == l.Agent.Name:
			return true
		}
	}
	return false
}

// fitting is step 2: the candidates whose offer takes the launch, and why each other did not.
func fitting(offer []candidate, l *runner.Launch, check func(*runner.Launch, runner.Description) error) (fit []candidate, refused []string) {
	for _, c := range offer {
		if err := check(l, c.desc); err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", c.name(), err))
			continue
		}
		fit = append(fit, c)
	}
	return fit, refused
}

// nowhere is the refusal of a launch no runner takes: which runners offer the doer and why each
// did not take it, and which registrations were passed over and why.
func nowhere(l *runner.Launch, offered bool, refused, passed []string) error {
	var b strings.Builder
	switch {
	case len(refused) == 1:
		fmt.Fprintf(&b, "no runner takes this spawn: %s", refused[0])
	case len(refused) > 1:
		fmt.Fprintf(&b, "no runner takes this spawn: %s", strings.Join(refused, "; "))
	case !offered:
		fmt.Fprintf(&b, "no runner offers %s", doerOf(l))
	}
	if len(passed) > 0 {
		fmt.Fprintf(&b, "; passed over: %s", strings.Join(passed, "; "))
	}
	if !offered && len(refused) == 0 {
		// Marked, for an install: a doer no runner offers is one a runner may offer later, and
		// the rule is held for it rather than refused (trigger.vet).
		return fmt.Errorf("%w: %s", trigger.ErrNoOffer, b.String())
	}
	return errors.New(b.String())
}

// fits is steps 1 and 2: whether some runner offers what the launch asks and takes it, checked
// with check (runnerd.CheckOffer for a launch, runnerd.CheckOfferAsWritten for a charter's spawn
// at install). It asks no runner anything.
func (s *Server) fits(l *runner.Launch, on *string, check func(*runner.Launch, runner.Description) error) error {
	s.bindDefaultAgent(l)
	offer, passed, err := s.candidates(l, on)
	if err != nil {
		return err
	}
	fit, refused := fitting(offer, l, check)
	if len(fit) == 0 {
		return nowhere(l, len(offer) > 0, refused, passed)
	}
	return nil
}

// route is where a launch runs: steps 1 to 4. A runner passed over at the handshake is named in
// the refusal when none is left.
func (s *Server) route(l *runner.Launch, on *string) (placed, error) {
	s.bindDefaultAgent(l)
	// A RUN THAT IS STILL LIVE goes back to the runner running it, whose join answers with it: a
	// launch is "once, safe to retry" on one runner, and routed afresh a second launch of the id
	// would start a second copy on whichever runner carries less.
	if on == nil {
		on = s.liveOn(l.ID)
	}
	offer, passed, err := s.candidates(l, on)
	if err != nil {
		return placed{}, err
	}
	fit, refused := fitting(offer, l, runnerd.CheckOffer)
	if len(fit) == 0 {
		return placed{}, nowhere(l, len(offer) > 0, refused, passed)
	}
	s.byLoad(fit)
	var refusedNow []string
	for _, c := range fit {
		desc, err := s.handshake(*c.reg)
		if err == nil {
			// What it says now, which may have changed since it was last described.
			if err = runnerd.CheckOffer(l, desc); err != nil {
				err = fmt.Errorf("%s: %w", c.name(), err)
			}
		}
		if err != nil {
			refusedNow = append(refusedNow, err.Error())
			continue
		}
		return placed{launcher: client.HTTPOver(c.reg.URL, s.transportFor(*c.reg)), name: c.name(), subject: c.reg.Subject}, nil
	}
	// Every one that took it failed its handshake: said with the rest of why nothing took it, the
	// runners whose offer refused it and the registrations passed over, so the refusal names
	// every runner an operator would look at, and how (`viff runner check <name>`).
	var b strings.Builder
	fmt.Fprintf(&b, "no runner that takes this spawn took it at the handshake: %s", strings.Join(refusedNow, "; "))
	if len(refused) > 0 {
		fmt.Fprintf(&b, "; refused it: %s", strings.Join(refused, "; "))
	}
	if len(passed) > 0 {
		fmt.Fprintf(&b, "; passed over: %s", strings.Join(passed, "; "))
	}
	return placed{}, errors.New(b.String())
}

// byLoad is step 3: fewest tasks on record not yet ended, then by name. One read of the task
// records per launch.
func (s *Server) byLoad(fit []candidate) {
	if len(fit) < 2 {
		return
	}
	instances := taskRows(s.Store)
	load := make(map[string]int, len(fit))
	for _, c := range fit {
		load[c.name()], _, _ = runnerCounts(instances, runnerEntityRef(c.reg.Name), c.reg.URL)
	}
	sort.SliceStable(fit, func(i, j int) bool {
		a, b := fit[i], fit[j]
		if la, lb := load[a.name()], load[b.name()]; la != lb {
			return la < lb
		}
		return a.name() < b.name()
	})
}

// taskRunner is the runner task id runs on, by the address its record names it by: as viff
// launched it, or as its record says.
func (s *Server) taskRunner(id string) (string, error) {
	if ht := s.hostedOf(id); ht.launched != nil {
		return ht.launched.runner, nil
	}
	ent, ok, err := s.Store.Get(mirror.Ref(id))
	if err != nil {
		return "", errors.New(notRead(fmt.Sprintf("task %s", id), err))
	}
	if !ok {
		return "", fmt.Errorf("task %s has no record, so where what follows its ask runs cannot be held to its runner", id)
	}
	return entity.Str(ent.Payload, "runnerName"), nil
}

// liveOn is the runner a run of id that has not ended is on, by its record (nil where there is
// none, or it has ended): in the scope viff launched it in, else baseline.
func (s *Server) liveOn(id string) *string {
	store := s.Store
	if sc, ok := s.hostedScope(id); ok {
		store = s.storeForScope(sc)
	}
	e, ok, err := store.Get(mirror.Ref(id))
	if err != nil || !ok {
		return nil
	}
	name := entity.Str(e.Payload, "runnerName")
	if name == "" || task.Status(entity.Str(e.Payload, "status")).Terminal() {
		return nil
	}
	return &name
}
