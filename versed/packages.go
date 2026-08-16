package versed

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/trigger"
)

// A CHARTER PACKAGE is a charter installed at an address: its own engine, its own rules, its
// own levels, and one guarantee — it may write only inside that address (trigger/confine.go).
//
// It is what the state-ergonomics note asks for when it says different groups cannot work in
// isolation today. Two teams installing rules into one verse are isolated when neither can
// write where the other reads, and a package is where that stops being a convention: the
// address is on the install, the confinement is on the engine, and a rule reaching past it does
// not install.
//
// It is the SAME SHAPE as a scope's engine one axis over, deliberately. A scope says whose copy
// of the verse a charter acts in; a package says which part of the verse it acts on. Both are a
// second engine over the same store with its own watches, both stamp their records so a restore
// takes only its own, and both are created on demand — so `scopeEngine` below is the thing to
// read for how this works, and this file is that with an address in place of a federation.
//
// READS ARE NOT NARROWED. A package's rules see the whole verse, because reacting to what
// happened elsewhere is most of what a rule does; what is confined is what MOVES.

// packageEngine returns the engine for the charter package at an address, creating and starting
// it on first use. The address is the kinded path a rule was installed at (`demo`, `demo.pr`).
func (s *Server) packageEngine(at string) (*trigger.Engine, error) {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return nil, err
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	return s.packageEngineLocked(confine)
}

// packageEngineLocked is packageEngine with the lock already held — the restore path below
// builds several in one pass.
func (s *Server) packageEngineLocked(confine trigger.Subtree) (*trigger.Engine, error) {
	key := "at:" + confine.Stamp()
	if pe, ok := s.scopeEngs[key]; ok {
		return pe.eng, nil
	}
	if s.ctx == nil {
		return nil, fmt.Errorf("engine not started")
	}
	eng := trigger.New(s.Store, s.Reg)
	eng.Confine(confine)
	if s.tripPaceSet {
		eng.SetTripPace(s.tripPace)
	}
	eng.SetSpawner(s.spawnerFor(s.Store, "", nil))
	ctx, cancel := context.WithCancel(s.ctx)
	// Per-trigger observation, exactly as a scope's engine has: each rule watches the subtrees
	// it reads, which for a package is usually wider than what it may write.
	eng.ObserveScoped(ctx)
	s.scopeEngs[key] = &scopeEngine{eng: eng, cancel: cancel}
	return eng, nil
}

// restorePackages installs each package's durable charter back into an engine of its own.
//
// A package is VISIBLE where a scope is not, and that is the whole difference in lifecycle: a
// scope's layer cannot be enumerated from baseline, so a federation comes back when its dev
// reconnects, while a package is a stamp on records anyone can list — so a restart brings every
// package back by reading the charter and grouping it by address.
//
// It runs after the baseline charter is loaded, and takes only the records stamped with an
// address; the baseline load takes only the unstamped ones (Engine.ownRecord).
func (s *Server) restorePackages() {
	ats := map[string]bool{}
	for _, ent := range s.Store.List(trigger.IsCharterRule) {
		if at := entity.Str(ent.Payload, trigger.AtField); at != "" {
			ats[at] = true
		}
	}
	if len(ats) == 0 {
		return
	}
	names := make([]string, 0, len(ats))
	for at := range ats {
		names = append(names, at)
	}
	sort.Strings(names) // one order, so a restart reads the same way twice
	s.scopeMu.Lock()
	engs := make([]*trigger.Engine, 0, len(names))
	for _, at := range names {
		confine, err := trigger.ParseAt(at)
		if err != nil {
			logPackage("charter package %q: %v", at, err)
			continue
		}
		eng, err := s.packageEngineLocked(confine)
		if err != nil {
			logPackage("charter package %q: %v", at, err)
			continue
		}
		engs = append(engs, eng)
	}
	s.scopeMu.Unlock()
	// OFF THE LOCK, for the reason a scope's restore is: loading a charter opens a watch per
	// rule, and holding the registry lock across that stalls every other lookup behind it.
	for _, eng := range engs {
		if err := eng.LoadStrata(); err != nil {
			logPackage("charter package strata: %v", err)
		}
		if err := eng.LoadCharter(); err != nil {
			logPackage("charter package: %v", err)
		}
	}
}

// packagesSeen is the addresses that currently have a charter package, for the status surface.
func (s *Server) packagesSeen() []string {
	var out []string
	for _, ent := range s.Store.List(trigger.IsCharterRule) {
		if at := entity.Str(ent.Payload, trigger.AtField); at != "" {
			out = append(out, at)
		}
	}
	sort.Strings(out)
	return dedupe(out)
}

func dedupe(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || in[i-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// logPackage reports a package that could not be restored, on stderr where the rest of the
// startup reconcile reports: a charter nobody is running is exactly what an operator has to be
// told at the point it happens rather than deduce from nothing firing.
func logPackage(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "verse: "+format+"\n", a...)
}
