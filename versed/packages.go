package versed

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/schedule"
	"github.com/signadot/verse/trigger"
	"github.com/signadot/verse/trigger/charter"
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
// read for how this works, and this file is that with an address in place of a scope.
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
	return s.packageEngineLocked(confine, nil)
}

// packageEngineLocked is packageEngine with the lock already held — the restore path below
// builds several in one pass — and reads what the package declares it reads, for an engine made
// here: set before the engine observes, since its rules are placed from their records the
// moment it does and each is checked against the declaration as it is placed.
func (s *Server) packageEngineLocked(confine trigger.Subtree, reads []trigger.Subtree) (*trigger.Engine, error) {
	key := "at:" + confine.Stamp()
	if pe, ok := s.scopeEngs[key]; ok {
		return pe.eng, nil
	}
	if s.ctx == nil {
		return nil, fmt.Errorf("engine not started")
	}
	eng := trigger.New(s.Store, s.Reg)
	eng.Confine(confine)
	eng.SetSuspensions(s.susp) // a package is baseline's, and so are the suspensions that hold it
	if s.tripPaceSet {
		eng.SetTripPace(s.tripPace)
	}
	eng.SetSpawner(s.spawnerFor(s.Store, "", eng))
	eng.SetDeadlineMover(s.moveDeadline)
	eng.SetOfferCheck(s.offerCheck())
	eng.SetReads(reads)
	ctx, cancel := context.WithCancel(s.ctx)
	// Per-trigger observation, exactly as a scope's engine has: each rule watches the subtrees
	// it reads, which for a package is usually wider than what it may write.
	eng.ObserveScoped(ctx)
	s.scopeEngs[key] = &scopeEngine{eng: eng, ctx: ctx, cancel: cancel}
	return eng, nil
}

// packageEngineIfRunning is the engine at an address, or nil when none is running there.
//
// LOOKUP RATHER THAN packageEngine, which CREATES one: a read that describes the verse must not
// change it. An address with records and no engine is a real state — a package whose install
// was refused, or one seen before its restore — and the honest answer about it is that nothing
// is running, not a fresh engine holding nothing.
func (s *Server) packageEngineIfRunning(at string) *trigger.Engine {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return nil
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	if pe, ok := s.scopeEngs["at:"+confine.Stamp()]; ok {
		return pe.eng
	}
	return nil
}

// packageEngines is every engine running a package's charter — not a scope's.
//
// The distinction is the key prefix: a package engine is keyed by its ADDRESS (`at:<stamp>`),
// a scope by its name. They are different planes and only one of them is
// visible from baseline, which is what restorePackages exists for.
func (s *Server) packageEngines() []*trigger.Engine {
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	out := make([]*trigger.Engine, 0, len(s.scopeEngs))
	for key, pe := range s.scopeEngs {
		if strings.HasPrefix(key, "at:") {
			out = append(out, pe.eng)
		}
	}
	return out
}

// kickEngines asks every engine this verse runs — baseline's, each package's, each scope's —
// for a pass over its pending rules (trigger.Engine.Kick): something that could clear a cause
// happened that no engine sees for itself, a runner registered or removed.
func (s *Server) kickEngines() {
	s.Eng.Kick()
	s.scopeMu.Lock()
	engs := make([]*trigger.Engine, 0, len(s.scopeEngs))
	for _, pe := range s.scopeEngs {
		engs = append(engs, pe.eng)
	}
	s.scopeMu.Unlock()
	for _, eng := range engs {
		eng.Kick()
	}
}

// offersChanged is what follows a runner's offers being written or taken away: a pass in every
// engine, since a rule pending for a spawn no runner offered may arm now, and the system
// charters refused at start tried again, since one may have been refused for want of this
// runner. The retry installs, so it runs behind the caller, which may be a launch's handshake.
func (s *Server) offersChanged() {
	s.kickEngines()
	go s.installWaitingSystemCharters()
}

// restorePackages brings each package's charter back into an engine of its own, and waits for
// the records to be in.
//
// A package is VISIBLE where a scope is not, and that is the whole difference in lifecycle: a
// scope's layer cannot be enumerated from baseline, so a scope comes back when its owner
// reconnects, while a package is a stamp on records anyone can list — so a restart brings every
// package back by reading the charter and grouping it by address. Each engine then follows its
// own records (trigger/follow.go), taking the ones stamped with its address.
//
// ASKED AGAIN WHOLE when the listing did not happen (entity.IsUnread), and when an engine's
// records are not in by the wait: an engine is found rather than made, so a second run makes
// nothing twice.
func (s *Server) restorePackages() error {
	if err := s.awaiting(phaseSuspensions); err != nil {
		return err
	}
	// THE CHARTER LOCK, because versed serves while it restores — that is what /healthz
	// distinguishes — so an operator's install can arrive in the middle of this and race the
	// charter it is being installed alongside. Taken here rather than inside the loop: the
	// restore is one charter operation, and half of one is what the lock is for.
	//
	// Before scopeMu below, never inside it (charterlock.go).
	release, err := s.lockCharter(s.ctxOrBackground())
	if err != nil {
		return entity.Unread(fmt.Errorf("charter packages: %w", err))
	}
	defer release()

	// ONE READ OF THE CHARTER, for which packages exist and what each declares it reads. The
	// declarations and the rules read apart could fail apart: every package then read as
	// declaring nothing, and its rules were refused one after another (git-issue
	// wb2pp5j0h12kssy9whn0). Each engine takes its rules and levels from its own follow.
	records, err := entity.BelowErr(s.Store, isCharterRecord, 2, entity.VerseRoot, charter.System)
	if err != nil {
		return entity.Unread(fmt.Errorf("the charter could not be read, so no package was restored: %w", err))
	}
	// EVERY ADDRESS THAT HAS RECORDS, not only those with rules: a package whose levels are
	// installed and whose rules were refused is still a package, and one that did not come back
	// at a restart would be a set of levels nobody could see or remove. packagesIn is the one
	// answer to "which packages exist", asked here too.
	names, err := s.packagesIn(records)
	if err != nil {
		return fmt.Errorf("charter packages could not be read, so none were restored: %w", err)
	}
	if len(names) == 0 {
		return nil
	}
	imports := importsIn(records, names)
	s.scopeMu.Lock()
	engs := make([]*trigger.Engine, 0, len(names))
	for _, at := range names {
		confine, err := trigger.ParseAt(at)
		if err != nil {
			logPackage("charter package %q: %v", at, err)
			continue
		}
		// With its declaration, for an engine made here: a rule is checked against what its
		// package declares it reads as it is placed.
		eng, err := s.packageEngineLocked(confine, imports[at])
		if err != nil {
			logPackage("charter package %q: %v", at, err)
			continue
		}
		engs = append(engs, eng)
	}
	s.scopeMu.Unlock()
	// And for an engine that stood already — made by an install that arrived before this —
	// the declaration as the records have it (restorePackageImports).
	s.restorePackageImports(imports)
	// OFF THE LOCK, for the reason a scope's restore is: a charter coming in opens a watch per
	// rule, and holding the registry lock across that stalls every other lookup behind it.
	ctx := s.ctxOrBackground()
	for _, eng := range engs {
		if err := s.charterIn(ctx, eng, "the charter at "+eng.At()); err != nil {
			return err
		}
	}
	return nil
}

// packagesSeen is the addresses that currently have a charter package, for the status surface.
// isCharterRecord is the three kinds of record a charter is kept in. One predicate, because
// the listing and the removal have to agree about what a package IS — a package that can be
// seen by records the removal does not remove is a package nobody can take down.
func isCharterRecord(r entity.Ref) bool {
	return charter.IsRule(r) || charter.IsStratum(r) || charter.IsPackage(r)
}

func (s *Server) packagesSeen() []string {
	out, _ := s.packagesSeenErr()
	return out
}

// packagesSeenErr is packagesSeen reporting the read that did not happen: an empty answer and
// no answer are opposite facts about the charter.
func (s *Server) packagesSeenErr() ([]string, error) {
	// ALL THREE KINDS OF RECORD, because any one of them makes a package exist: its rules, its
	// levels, and the declaration of what it declares it reads. Reading only the rules made a package
	// with levels and no rules INVISIBLE — which is not an exotic state, it is what a
	// half-applied file leaves behind (the levels install, a rule is refused) and exactly when
	// somebody needs to list it and take it down. `verse c -r <address>` answered "no
	// charter is installed" about a charter whose records were sitting in the store.
	//
	// ONE READ AT THE SUBTREE, not three at the root. All three kinds live under
	// `verse.trigger.*`, and List(pred) is a Match on the ROOT — the whole verse across the
	// wire, filtered here — so this cost three full reads, on a function `verse charter` calls
	// on every request and installs call again. On staging that was the difference between an
	// answer and a timeout.
	//
	// EACH READ ANSWERS FOR ITSELF. One check of the store's flag after both let the second
	// read's success clear the first's failure, and a restore then brought back no packages.
	charter, err := entity.BelowErr(s.Store, isCharterRecord, 2, entity.VerseRoot, charter.System)
	if err != nil {
		return nil, entity.Unread(err)
	}
	return s.packagesIn(charter)
}

// packagesIn is the packages a listing of the charter names, with those the schedules name —
// which live apart, so they are read here.
func (s *Server) packagesIn(records []entity.Entity) ([]string, error) {
	var out []string
	for _, ent := range records {
		if at := entity.Str(ent.Payload, charter.AtField); at != "" {
			out = append(out, at)
		}
	}
	// And a fourth, which lives apart: a package's schedules are the daemon's records under
	// verse.schedule, and a package declaring only a schedule is a package.
	schedules, err := entity.BelowErr(s.Store, schedule.IsRecord, 1, entity.VerseRoot, schedule.System)
	if err != nil {
		return nil, entity.Unread(err)
	}
	for _, ent := range schedules {
		if at := entity.Str(ent.Payload, schedule.AtField); at != "" {
			out = append(out, at)
		}
	}
	sort.Strings(out)
	return dedupe(out), nil
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

// setPackageImports installs a package's `reads:` declaration: what its rules may read
// outside their own address (trigger/reads.go).
//
// THREE CHECKS, in the order that gives the most specific answer first:
//
//  1. the addresses parse, and none of them is the whole verse;
//  2. the graph over packages stays ACYCLIC with this declaration in it — the check the whole
//     feature exists for, and the one no single charter's author could make;
//  3. every rule ALREADY installed in this package is still within the declaration, because
//     narrowing must not leave a rule standing that the reads declaration no longer allow. Re-installing
//     a charter with a line deleted is exactly how that would happen.
//
// Then the engine is told, and the record is written — in that order, so a declaration the
// engine refuses is not one a restart brings back.
func (s *Server) setPackageImports(store entity.Store, at string, subs []trigger.Subtree) error {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return err
	}
	if confine.Stamp() == "" {
		return fmt.Errorf("reads: a charter with no address has nothing to import into — " +
			"`reads:` belongs to a package, one installed at an address: its file's `at:`, or " +
			"`verse c -a <address> -f <file>`")
	}
	graph, err := s.packageImports()
	if err != nil {
		return fmt.Errorf("reads: the packages' declarations could not be read, so whether this "+
			"closes a cycle cannot be said: %w", err)
	}
	graph[confine.Stamp()] = subs
	if c := trigger.Cycle(graph); c != "" {
		return fmt.Errorf("reads: this would close a cycle between packages: %s — one reads "+
			"what the next writes, all the way round, so a write on any of them can wake the "+
			"others and come back. A package graph is acyclic: break the loop, or fold the two "+
			"halves into one package", c)
	}
	eng, err := s.packageEngine(at)
	if err != nil {
		return err
	}
	before := eng.Reads()
	eng.SetReads(subs)
	if err := eng.RecheckReads(); err != nil {
		eng.SetReads(before) // narrowing refused: the engine is left as it was
		return fmt.Errorf("reads: %w", err)
	}
	p := entity.Obj{charter.AtField: confine.Stamp()}
	if len(subs) > 0 {
		p[charter.ReadsField] = trigger.StampReads(subs)
	}
	if _, _, err := store.Write(entity.Write{
		At:    entity.Path{Ref: charter.PackageRef(confine.Stamp())},
		Value: p.Payload(),
	}); err != nil {
		eng.SetReads(before)
		return fmt.Errorf("reads: recording the declaration: %w", err)
	}
	return nil
}

// packageDecl reads the record standing for a package's declaration, so an install that has to be
// undone can put back exactly what was there — including nothing, which is what a package that
// never declared anything has and is a different state from one that declared it reads nothing.
func (s *Server) packageDecl(at string) (entity.Payload, bool, error) {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return nil, false, err
	}
	ent, ok, err := s.Store.Get(charter.PackageRef(confine.Stamp()))
	if err != nil {
		return nil, false, fmt.Errorf("reading the declaration at %s: %w", at, err)
	}
	if !ok {
		return nil, false, nil
	}
	return ent.Payload, true, nil
}

// removeDecl removes a package's declaration where one stands. A package need not have one, and
// removing a node is its owner's act: asked of a node nothing stands at, it is refused for
// whoever owns nothing there, which is everybody — so nothing is asked where nothing stands.
func (s *Server) removeDecl(store entity.Store, ref entity.Ref) error {
	if _, stands, err := s.Store.Get(ref); err != nil || !stands {
		return err
	}
	_, _, err := store.Write(entity.Write{At: entity.Path{Ref: ref}})
	return err
}

// putPackageDecl writes a declaration back as it was — or removes it, when there was none.
//
// REMOVED AND WRITTEN rather than written over, because a store write MERGES at its path: putting
// back a declaration that had fewer reads than the one it replaces would leave the wider list
// standing, and the next restart would restore a package reading where its charter says it does
// not (restorePackageImports).
func (s *Server) putPackageDecl(store entity.Store, at string, was entity.Payload, had bool) error {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return err
	}
	ref := charter.PackageRef(confine.Stamp())
	if err := s.removeDecl(store, ref); err != nil {
		return fmt.Errorf("removing the declaration at %s: %w", at, err)
	}
	if !had {
		return nil
	}
	if _, _, err := store.Write(entity.Write{At: entity.Path{Ref: ref}, Value: was}); err != nil {
		return fmt.Errorf("putting the declaration at %s back: %w", at, err)
	}
	return nil
}

// dropPackageEngine stops the engine at an address and forgets it, leaving nothing running for a
// charter that is not there. It is uninstallOne's last step on its own, for the caller that has
// already taken the records down — an install that was refused whole, at an address that had no
// charter before it.
func (s *Server) dropPackageEngine(at string) {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	key := "at:" + confine.Stamp()
	if pe, ok := s.scopeEngs[key]; ok {
		pe.cancel()
		s.susp.Detach(pe.eng)
		delete(s.scopeEngs, key)
	}
}

// packageImports reads the declared graph out of the store, reporting the read that did not
// happen: a graph read as empty is one where every package reads nothing, which is the opposite
// fact.
func (s *Server) packageImports() (map[string][]trigger.Subtree, error) {
	charter, err := entity.BelowErr(s.Store, isCharterRecord, 2, entity.VerseRoot, charter.System)
	if err != nil {
		return nil, entity.Unread(err)
	}
	names, err := s.packagesIn(charter)
	if err != nil {
		return nil, err
	}
	return importsIn(charter, names), nil
}

// importsIn is the declared graph in a listing of the charter: every package named, and what it
// declares it reads. A package with rules and no declaration reads nothing, which is the default
// the declaration replaces.
func importsIn(records []entity.Entity, names []string) map[string][]trigger.Subtree {
	out := map[string][]trigger.Subtree{}
	for _, at := range names {
		out[at] = nil
	}
	for _, ent := range records {
		if !charter.IsPackage(ent.Ref) {
			continue
		}
		at := entity.Str(ent.Payload, charter.AtField)
		if at == "" {
			at = ent.Ref.Name()
		}
		subs, err := trigger.ParseReads(entity.Strs(ent.Payload, charter.ReadsField))
		if err != nil {
			logPackage("records package %q: %v", at, err)
			continue
		}
		out[at] = subs
	}
	return out
}

// restorePackageImports gives each package engine its declaration back, BEFORE the rules are
// loaded — a rule is checked against the reads declaration as it installs, so a restore that loaded the
// charter first would refuse the very rules it is bringing back.
func (s *Server) restorePackageImports(imports map[string][]trigger.Subtree) {
	for at, subs := range imports {
		if len(subs) == 0 {
			continue
		}
		eng, err := s.packageEngine(at)
		if err != nil {
			logPackage("charter package %q: %v", at, err)
			continue
		}
		eng.SetReads(subs)
	}
}

// removePackage uninstalls the charter at an address: its rules, its levels, the declaration of
// what it declares it reads, and the engine running them.
//
// ALL FOUR, because a package is all four and taking any three leaves something that outlives
// what it belonged to — a rule with no engine, an engine with no rules, or an `reads:` record
// naming a package nobody can list. Rules first, because a stratum still named by a rule refuses
// to go (which is right, and is why the order is not free).
//
// WHAT IT DOES NOT TAKE is what the package WROTE. A charter's writes are the world's — a
// package that derived `drift:head:*` leaves those entities standing, and `verse data rm` is
// how they go. This mirrors a source's `--keep`, and the reasoning is the same: verse cannot
// tell "the reflection is over" from "the same truth is arriving another way", so it does not
// guess on the operator's behalf.
//
// A CHARTER UNDER IT is refused rather than swept up, because `demo` and `demo.pr` are two
// packages and somebody removing the first did not necessarily mean the second. The refusal
// names them; `recurse` is the operator saying they did mean it.
func (s *Server) removePackage(store entity.Store, at string, recurse bool) error {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return err
	}
	stamp := confine.Stamp()
	if stamp == "" {
		return fmt.Errorf("uninstall wants the address of a charter package; baseline has none " +
			"— its rules go one at a time (`verse c -r rules <name>`), which is what keeps " +
			"`rm` from being a way to delete a whole verse's charter by mistyping it")
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
		if err := s.uninstallOne(store, target); err != nil {
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
	prefix, err := entity.PathSegs(at)
	if err != nil {
		return nil
	}
	var out []string
	for _, other := range s.packagesSeen() {
		segs, err := entity.PathSegs(other)
		if err != nil || other == at || !entity.Under(prefix, segs, entity.Literal) {
			continue
		}
		out = append(out, other)
	}
	sort.Strings(out)
	return out
}

// uninstallOne takes down one package: its rules, then its levels, then its declaration, then
// the engine that was running them.
func (s *Server) uninstallOne(store entity.Store, at string) error {
	if err := s.mayUninstall(store, at); err != nil {
		return err
	}
	return s.takeDown(store, at)
}

// mayUninstall asks whether the requester may take down every part of the package at an address,
// and changes nothing.
func (s *Server) mayUninstall(store entity.Store, at string) error {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return err
	}
	eng, err := s.packageEngine(at)
	if err != nil {
		return err
	}
	// EVERY PART OF IT IS ASKED ABOUT BEFORE ANY PART GOES. The rules and levels are removed by
	// the engine, through the system's store, and the declaration after them through the
	// requester's — so asked only there, a stranger's uninstall would take every rule down and
	// then be refused.
	who := entity.PrincipalOf(store)
	if decl := charter.PackageRef(confine.Stamp()); who != "" && !entity.IsSystem(who) {
		if _, stands, err := s.Store.Get(decl); err != nil {
			return err
		} else if stands {
			if err := entity.Check(store, who, entity.OwnerLevel, decl, "uninstalling it"); err != nil {
				return err
			}
		}
	}
	for _, t := range append(eng.Triggers(), eng.Held()...) {
		if err := eng.MayRemoveTrigger(who, t.Name); err != nil {
			return err
		}
	}
	for _, st := range eng.Strata() {
		if err := eng.MayRemoveStratum(who, st.Name); err != nil {
			return err
		}
	}
	if who != "" && !entity.IsSystem(who) {
		scheds, err := s.packageSchedules(at)
		if err != nil {
			return err
		}
		for _, ref := range scheds {
			if err := entity.Check(store, who, entity.OwnerLevel, ref, "removing it"); err != nil {
				return err
			}
		}
	}
	return nil
}

// takeDown removes the package at an address without asking (mayUninstall asks).
func (s *Server) takeDown(store entity.Store, at string) error {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return err
	}
	eng, err := s.packageEngine(at)
	if err != nil {
		return err
	}
	// Armed and pending alike: a pending rule is written down and would arm the moment its
	// cause cleared, in a package somebody removed.
	for _, t := range append(eng.Triggers(), eng.Held()...) {
		eng.RemoveTrigger(t.Name)
	}
	for _, st := range eng.Strata() {
		if err := eng.RemoveStratum(st.Name); err != nil {
			return fmt.Errorf("%s: stratum %q: %w", at, st.Name, err)
		}
	}
	if err := s.removePackageSchedules(store, at); err != nil {
		return fmt.Errorf("%s: %w", at, err)
	}
	// Its outboxes go with it: the io stops being held out a place no package declares.
	if sends, err := s.packageSends(at); err == nil {
		for _, send := range sends {
			if e, ok := ioFor(s.Store, send.To); ok {
				releaseOutbox(store, send.From, e.Out.As)
			}
		}
	}
	if err := s.removeDecl(store, charter.PackageRef(confine.Stamp())); err != nil {
		return fmt.Errorf("%s: removing the declaration: %w", at, err)
	}
	// AND WHATEVER ELSE IS IN THE STORE AT THIS ADDRESS, which is not always what the engine
	// held. A rule REFUSED at install is a record with no rule behind it — the file was applied
	// under a declaration that has since narrowed, or its stratum never arrived, or the field
	// it was written with was renamed — and the loop above walks eng.Triggers(), which is the
	// rules that made it in. So the refused ones survived their own uninstall, and because
	// packagesSeen reads the RECORDS, the package went on existing: it could not be installed,
	// could not be removed, and still counted. There was no way out of that state but a wipe.
	//
	// This is the same listing packagesSeen does, with the same predicate, which is the point:
	// what makes a package visible is what a removal has to reach — and a read that did not
	// happen is not "nothing left", or the records it missed outlive their package as above.
	records, err := entity.BelowErr(s.Store, isCharterRecord, 2, entity.VerseRoot, charter.System)
	if err != nil {
		return fmt.Errorf("%s: the charter could not be read, so records the engine had not "+
			"installed may remain: %w", at, entity.Unread(err))
	}
	left := 0
	for _, ent := range records {
		if entity.Str(ent.Payload, charter.AtField) != at {
			continue
		}
		if _, _, err := store.Write(entity.Write{At: entity.Path{Ref: ent.Ref}}); err != nil {
			return fmt.Errorf("%s: removing %s: %w", at, ent.Ref, err)
		}
		left++
	}
	if left > 0 {
		// Said out loud: these are records the engine never had, so their existence is news
		// about a charter that did not fully install, not about this removal.
		fmt.Fprintf(os.Stderr, "verse: uninstall %s: removed %d record(s) the engine had not "+
			"installed (a refused or held rule leaves one)\n", at, left)
	}
	// The engine LAST, and dropped from the registry as well as cancelled: a package installed
	// again at the same address gets a new one, and a cancelled engine left in the map would be
	// found first and observe nothing.
	s.dropPackageEngine(at)
	return nil
}
