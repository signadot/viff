package versed

import (
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/schedule"
	"github.com/signadot/verse/trigger"
)

// A PRUNE IS `verse c -R <root>` AND `verse c -a -d <dir>` AS ONE STEP: what stands at and under
// the root afterwards is what the request holds, and nothing else.
//
// NOT TAKEN DOWN AND PUT BACK. A package the request holds is installed over — its rules replaced
// by name, as any install does — and then loses what its file leaves out, so a rule the directory
// keeps goes on running through the request rather than being stopped and re-armed. The removals
// go on the request's undo beside the installs, so a refusal anywhere puts back both.
//
// A package the request does NOT hold goes whole, as `-R` takes it. Its parts are asked about
// before anything is installed (mayUninstall) and it is taken down last, after everything that
// can be put back: an uninstall is not undoable, so it is the step nothing can come after.

// NOTHING, WITH A PRUNE ROOT, IS THE ROOT EMPTIED: a directory with no charter left in it is a
// state a repository's charters can be in (system/repocharters: the empty tree), and installing
// it takes down every package at and under the root. Without a root, a request with nothing in
// it is refused, since it says nothing to do.
func charterFilesPruning(docs []*ir.Node, at, prune string) ([]charterGroup, error) {
	if prune != "" && len(docs) == 0 {
		return nil, nil
	}
	return charterFiles(docs, at)
}

// pruneTargets checks a prune before anything changes: the root is an address, every charter in
// the request is at or under it, and the requester may take down every package at or under it
// that the request leaves out — which it answers, deepest first.
func (s *Server) pruneTargets(store entity.Store, scope, root string, groups []charterGroup) ([]string, int, error) {
	if scope != "" {
		return nil, http.StatusBadRequest, errors.New("prune: a scope's charter is its own copy " +
			"of the verse and has no packages under an address; prune in baseline")
	}
	confine, err := trigger.ParseAt(root)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("prune: %w", err)
	}
	stamp := confine.Stamp()
	if stamp == "" {
		return nil, http.StatusBadRequest, errors.New("prune wants the address it clears; " +
			"baseline has none, for the reason `verse c -R` refuses it")
	}
	prefix, err := entity.PathSegs(stamp)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("prune: %w", err)
	}
	kept := map[string]bool{}
	for _, g := range groups {
		// OUTSIDE THE ROOT IS REFUSED rather than installed beside it: a prune says "under here
		// is what I sent", and a charter it installs somewhere it does not prune is one the
		// sender took for covered.
		where, under := "baseline", false
		if g.at != "" {
			c, err := trigger.ParseAt(g.at)
			if err != nil {
				return nil, http.StatusBadRequest, err
			}
			where = c.Stamp()
			segs, err := entity.PathSegs(where)
			under = err == nil && entity.Under(prefix, segs, entity.Literal)
		}
		if !under {
			return nil, http.StatusBadRequest, fmt.Errorf("prune %s: the request installs a "+
				"charter at %s, which is not at or under it", stamp, where)
		}
		kept[where] = true
	}
	targets := s.packagesUnder(stamp)
	if s.packagesSeenAt(stamp) {
		targets = append(targets, stamp)
	}
	var gone []string
	for _, t := range targets {
		if kept[t] {
			continue
		}
		if err := s.mayUninstall(store, t); err != nil {
			return nil, charterCode(err), fmt.Errorf("prune %s: %s: %w", stamp, t, err)
		}
		gone = append(gone, t)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(gone)))
	return gone, 0, nil
}

// pruneGone takes down the packages the request left out, deepest first.
//
// A failure here is the store failing between checks that passed and writes that did not, and it
// is said as what it is: the install stands, and the prune stopped part-way.
func (s *Server) pruneGone(store entity.Store, gone []string) (int, error) {
	for _, at := range gone {
		if err := s.takeDown(store, at); err != nil {
			return http.StatusServiceUnavailable, fmt.Errorf("the charters installed, and the "+
				"prune stopped at %s: %w — `verse c -R %s` finishes it", at, err, at)
		}
	}
	return 0, nil
}

// keep notes that the request holds this rule, level or schedule at the package being installed.
func (tx *charterTx) keep(kind, name string) {
	if tx.kept == nil {
		tx.kept = map[string]map[string]bool{}
	}
	key := tx.at + "\x00" + kind
	if tx.kept[key] == nil {
		tx.kept[key] = map[string]bool{}
	}
	tx.kept[key][name] = true
}

func (tx *charterTx) holds(kind, name string) bool { return tx.kept[tx.at+"\x00"+kind][name] }

// pruneKept takes out of each package the request holds what its file leaves out: the rules,
// then the levels (a level a rule still names refuses to go), then the schedules.
func (tx *charterTx) pruneKept(groups []charterGroup) (int, error) {
	for _, g := range groups {
		eng, err := tx.s.charterEngineFor(tx.scope, g.at)
		if err != nil {
			return http.StatusBadRequest, err
		}
		tx.at, tx.eng = g.at, eng
		for _, t := range append(eng.Triggers(), eng.Held()...) {
			if !tx.holds("rules", t.Name) {
				if code, err := tx.dropRule(t.Name); err != nil {
					return code, err
				}
			}
		}
		for _, st := range eng.Strata() {
			if !tx.holds("strata", st.Name) {
				if code, err := tx.dropStratum(st.Name); err != nil {
					return code, err
				}
			}
		}
		refs, err := tx.s.packageSchedules(g.at)
		if err != nil {
			return http.StatusServiceUnavailable, err
		}
		for _, ref := range refs {
			if !tx.holds("schedules", ref.Name()) {
				if code, err := tx.dropSchedule(ref); err != nil {
					return code, err
				}
			}
		}
	}
	return http.StatusCreated, nil
}

// dropRule removes one rule from the package being pruned, remembering it and whose it was.
func (tx *charterTx) dropRule(name string) (int, error) {
	eng := tx.eng
	old, _ := eng.Rule(name)
	whose, err := entity.SnapshotRecord(tx.store, eng.RuleRef(name))
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	// The undo is pushed for what was deleted, not for what was answered: a removal whose
	// delete landed and whose wait for the engine did not (trigger.ErrNotReflected) is unwound
	// with the rest, record included.
	_, err = eng.RemoveTriggerAs(tx.ctx, tx.as(), name)
	if err == nil || errors.Is(err, trigger.ErrNotReflected) {
		tx.push(func() error {
			if err := eng.RestoreTrigger(tx.undoCtx(), old); err != nil {
				return err
			}
			return entity.RestoreRecord(tx.store, whose)
		})
	}
	if err != nil {
		return charterCode(err), fmt.Errorf("prune: rule %q: %w", name, err)
	}
	tx.dropped[tx.at] = append(tx.dropped[tx.at], "rule "+name)
	return 0, nil
}

// dropStratum removes one level from the package being pruned, remembering it and whose it was.
func (tx *charterTx) dropStratum(name string) (int, error) {
	eng := tx.eng
	old, _ := eng.Stratum(name)
	whose, err := entity.SnapshotRecord(tx.store, eng.StratumRef(name))
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	err = eng.RemoveStratumAs(tx.ctx, tx.as(), name)
	if err == nil || errors.Is(err, trigger.ErrNotReflected) { // deleted either way: see dropRule
		tx.push(func() error {
			if err := eng.RestoreStratum(tx.undoCtx(), old); err != nil {
				return err
			}
			return entity.RestoreRecord(tx.store, whose)
		})
	}
	if err != nil {
		return charterCode(err), fmt.Errorf("prune: level %q: %w", name, err)
	}
	tx.dropped[tx.at] = append(tx.dropped[tx.at], "level "+name)
	return 0, nil
}

// dropSchedule removes one schedule of the package being pruned, remembering its record and whose
// it was. Put back as the system: the record is gone, and with it whatever let its installer write
// there, until its perms record is restored after it.
func (tx *charterTx) dropSchedule(ref entity.Ref) (int, error) {
	if who := tx.as(); !entity.IsSystem(who) {
		if err := entity.Check(tx.store, who, entity.OwnerLevel, ref, "removing it"); err != nil {
			return charterCode(err), fmt.Errorf("prune: schedule %s: %w", ref.Name(), err)
		}
	}
	old, had, err := tx.s.Store.Get(ref)
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	if !had {
		return 0, nil
	}
	whose, err := entity.SnapshotRecord(tx.store, ref)
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	id := ref.Name()
	if err := tx.s.removeSchedule(tx.store, id); err != nil {
		return charterCode(err), fmt.Errorf("prune: %w", err)
	}
	store := tx.store
	tx.push(func() error {
		if err := tx.s.putScheduleRecord(entity.As(store, entity.SystemCharter), id, old.Payload); err != nil {
			return err
		}
		return entity.RestoreRecord(store, whose)
	})
	tx.dropped[tx.at] = append(tx.dropped[tx.at], "schedule "+schedule.NameOf(id))
	return 0, nil
}

// prunedReport is what a prune answers with beside the install: per package it kept, what went
// out of it; per package it left out, that it went.
func (tx *charterTx) prunedReport(gone []string) []api.Pruned {
	var out []api.Pruned
	ats := make([]string, 0, len(tx.dropped))
	for at := range tx.dropped {
		ats = append(ats, at)
	}
	sort.Strings(ats)
	for _, at := range ats {
		out = append(out, api.Pruned{At: at, Removed: tx.dropped[at]})
	}
	for _, at := range gone {
		out = append(out, api.Pruned{At: at, Uninstalled: true})
	}
	return out
}
