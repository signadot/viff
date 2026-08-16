package versed

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

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
	// The declarations before the rules: a rule is checked against its package's imports as it
	// installs, so a restore that loaded the charter first would refuse what it is restoring.
	s.restorePackageImports()
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

// ── what a package IMPORTS ───────────────────────────────────────────────────

// setPackageImports installs a package's `imports:` declaration: what its rules may read
// outside their own address (trigger/imports.go).
//
// THREE CHECKS, in the order that gives the most specific answer first:
//
//  1. the addresses parse, and none of them is the whole verse;
//  2. the graph over packages stays ACYCLIC with this declaration in it — the check the whole
//     feature exists for, and the one no single charter's author could make;
//  3. every rule ALREADY installed in this package is still within the declaration, because
//     narrowing must not leave a rule standing that the imports no longer allow. Re-installing
//     a charter with a line deleted is exactly how that would happen.
//
// Then the engine is told, and the record is written — in that order, so a declaration the
// engine refuses is not one a restart brings back.
func (s *Server) setPackageImports(at string, subs []trigger.Subtree) error {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return err
	}
	if confine.Stamp() == "" {
		return fmt.Errorf("imports: a charter with no address has nothing to import into — " +
			"`imports:` belongs to a package, installed with `trigger add --at <address>`")
	}
	graph := s.packageImports()
	graph[confine.Stamp()] = subs
	if c := trigger.Cycle(graph); c != "" {
		return fmt.Errorf("imports: this would close a cycle between packages: %s — one reads "+
			"what the next writes, all the way round, so a write on any of them can wake the "+
			"others and come back. A package graph is acyclic: break the loop, or fold the two "+
			"halves into one package", c)
	}
	eng, err := s.packageEngine(at)
	if err != nil {
		return err
	}
	before := eng.Imports()
	eng.SetImports(subs)
	if err := eng.RecheckReads(); err != nil {
		eng.SetImports(before) // narrowing refused: the engine is left as it was
		return fmt.Errorf("imports: %w", err)
	}
	p := entity.Obj{trigger.AtField: confine.Stamp()}
	if len(subs) > 0 {
		p[trigger.ImportsField] = trigger.StampImports(subs)
	}
	if _, _, err := s.Store.Write(entity.Write{
		At:    entity.Path{Ref: trigger.PackageRef(confine.Stamp())},
		Value: p.Payload(),
	}); err != nil {
		eng.SetImports(before)
		return fmt.Errorf("imports: recording the declaration: %w", err)
	}
	return nil
}

// packageImports reads the declared graph out of the store: every package that has a record,
// and what it imports. A package with rules and no record imports nothing, which is the default
// the declaration replaces.
func (s *Server) packageImports() map[string][]trigger.Subtree {
	out := map[string][]trigger.Subtree{}
	for _, at := range s.packagesSeen() {
		out[at] = nil
	}
	for _, ent := range s.Store.List(trigger.IsCharterPackage) {
		at := entity.Str(ent.Payload, trigger.AtField)
		if at == "" {
			at = ent.Ref.ID()
		}
		subs, err := trigger.ParseImports(entity.Strs(ent.Payload, trigger.ImportsField))
		if err != nil {
			logPackage("charter package %q: %v", at, err)
			continue
		}
		out[at] = subs
	}
	return out
}

// restorePackageImports gives each package engine its declaration back, BEFORE the rules are
// loaded — a rule is checked against the imports as it installs, so a restore that loaded the
// charter first would refuse the very rules it is bringing back.
func (s *Server) restorePackageImports() {
	for at, subs := range s.packageImports() {
		if len(subs) == 0 {
			continue
		}
		eng, err := s.packageEngine(at)
		if err != nil {
			logPackage("charter package %q: %v", at, err)
			continue
		}
		eng.SetImports(subs)
	}
}

// removePackage uninstalls the charter at an address: its rules, its levels, the declaration of
// what it imports, and the engine running them.
//
// ALL FOUR, because a package is all four and taking any three leaves something that outlives
// what it belonged to — a rule with no engine, an engine with no rules, or an `imports:` record
// naming a package nobody can list. Rules first, because a stratum still named by a rule refuses
// to go (which is right, and is why the order is not free).
//
// WHAT IT DOES NOT TAKE is what the package WROTE. A charter's writes are the world's — a
// package that derived `drift:head:*` leaves those entities standing, and `verse state rm` is
// how they go. This mirrors a source's `--keep`, and the reasoning is the same: verse cannot
// tell "the reflection is over" from "the same truth is arriving another way", so it does not
// guess on the operator's behalf.
//
// A CHARTER UNDER IT is refused rather than swept up, because `demo` and `demo.pr` are two
// packages and somebody removing the first did not necessarily mean the second. The refusal
// names them; `recurse` is the operator saying they did mean it.
func (s *Server) removePackage(at string, recurse bool) error {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return err
	}
	stamp := confine.Stamp()
	if stamp == "" {
		return fmt.Errorf("uninstall wants the address of a charter package; baseline has none " +
			"— its rules go one at a time (`verse charter remove <name>`), which is what keeps " +
			"`uninstall` from being a way to delete a whole verse's charter by mistyping it")
	}
	under := s.packagesUnder(stamp)
	if len(under) > 0 && !recurse {
		what := "charters are installed under %s (%s): they are packages of their own, and " +
			"removing this one does not mean removing them. `--recurse` says you mean it"
		if !s.packagesSeenAt(stamp) {
			what = "no charter is installed at %s, but %s is under it — `--recurse` removes " +
				"what is under an address as well as what is at it"
		}
		return fmt.Errorf(what, stamp, strings.Join(under, ", "))
	}
	targets := under
	if s.packagesSeenAt(stamp) {
		targets = append([]string{stamp}, under...)
	}
	// Deepest first, so a package is gone before the address above it is.
	sort.Sort(sort.Reverse(sort.StringSlice(targets)))
	for _, target := range targets {
		if err := s.uninstallOne(target); err != nil {
			return err
		}
	}
	return nil
}

// packagesSeenAt reports whether a charter is installed AT an address — the question a removal
// asks first, so "there is nothing there" is a 404 rather than a successful no-op.
func (s *Server) packagesSeenAt(at string) bool {
	for _, other := range s.packagesSeen() {
		if other == at {
			return true
		}
	}
	return false
}

// packagesUnder is every installed charter STRICTLY under an address.
func (s *Server) packagesUnder(at string) []string {
	var out []string
	for _, other := range s.packagesSeen() {
		if other != at && strings.HasPrefix(other, at+".") {
			out = append(out, other)
		}
	}
	sort.Strings(out)
	return out
}

// uninstallOne takes down one package: its rules, then its levels, then its declaration, then
// the engine that was running them.
func (s *Server) uninstallOne(at string) error {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return err
	}
	eng, err := s.packageEngine(at)
	if err != nil {
		return err
	}
	for _, t := range eng.Triggers() {
		eng.RemoveTrigger(t.Name)
	}
	for _, st := range eng.Strata() {
		if err := eng.RemoveStratum(st.Name); err != nil {
			return fmt.Errorf("%s: stratum %q: %w", at, st.Name, err)
		}
	}
	if _, _, err := s.Store.Write(entity.Write{
		At: entity.Path{Ref: trigger.PackageRef(confine.Stamp())},
	}); err != nil {
		return fmt.Errorf("%s: removing the declaration: %w", at, err)
	}
	// The engine LAST, and dropped from the registry as well as cancelled: a package installed
	// again at the same address gets a new one, and a cancelled engine left in the map would be
	// found first and observe nothing.
	s.scopeMu.Lock()
	key := "at:" + confine.Stamp()
	if pe, ok := s.scopeEngs[key]; ok {
		pe.cancel()
		delete(s.scopeEngs, key)
	}
	s.scopeMu.Unlock()
	return nil
}
