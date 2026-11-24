package viffd

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/internal/trigger"
	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/schedule"
)

// A PRUNE IS `viff c -R` OF WHAT THE DIRECTORY LEAVES OUT, THEN `viff c -a -d <dir>`: what
// stands at and under the root afterwards is what the request holds, and nothing else.
//
// A PACKAGE THE REQUEST DOES NOT HOLD IS UNINSTALLED, FIRST, as `-R` uninstalls it (uninstall.go):
// locked, its tasks cancelled and their hooks run (those that start nothing: trigger.Engine.Close),
// then removed. Each is asked about before any
// is touched (mayUninstall), all are locked at once, under the charter lock, and uninstalled
// together, and the install begins once every one is gone: nothing of the request is installed
// beside what it replaces, and nothing is installed meanwhile where it is taking down. An
// uninstall is not undone: an install refused after it leaves those packages removed, which is
// where the request was taking them, and says so. The request waits for as long as their tasks
// take to end.
//
// A PACKAGE THE REQUEST HOLDS IS NOT TAKEN DOWN AND PUT BACK. It is installed over, its rules
// replaced by name as any install does, and then loses what its file leaves out, so a rule the
// directory keeps goes on running through the request. Those removals go on the request's undo
// beside the installs, so a refusal puts back both.

// NOTHING, WITH A PRUNE ROOT, IS THE ROOT EMPTIED: a directory with no charter left in it is a
// state a repository's charters can be in (internal/repocharters: the empty tree), and installing
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
			"of the viff instance and has no packages under an address; prune in baseline")
	}
	confine, err := trigger.ParseAt(root)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("prune: %w", err)
	}
	stamp := confine.Stamp()
	if stamp == "" {
		return nil, http.StatusBadRequest, errors.New("prune wants the address it clears; " +
			"baseline has none, for the reason `viff c -R` refuses it")
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

// pruneLeftOut is what a pruning install checks before it changes anything, under the charter
// lock: the request reads, names its installer, installs at no package that is locked, and may
// take down what it leaves out, which it answers, deepest first.
func (s *Server) pruneLeftOut(store entity.Store, scope, at, prune string, docs []*ir.Node) ([]string, int, error) {
	groups, err := charterFilesPruning(docs, at, prune)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if entity.PrincipalOf(store) == "" {
		return nil, http.StatusBadRequest, errors.New("a charter names who installs it: its rules act as their installer")
	}
	for _, g := range groups {
		if err := s.lockedAt(scope, g.at); err != nil {
			return nil, http.StatusTooManyRequests, err
		}
	}
	return s.pruneTargets(store, scope, prune, groups)
}

// ownPackageAmong is the package among those a prune would uninstall whose rules started the
// task that is asking, "" where there is none or no task asks. The uninstall would cancel the
// task, and the request with it, ahead of the install.
func (h *taskCharter) ownPackageAmong(gone []string) string {
	if h == nil {
		return ""
	}
	for _, at := range gone {
		if confine, err := trigger.ParseAt(at); err == nil && trigger.OwnerOf(h.task) == trigger.OwnerAt(confine) {
			return at
		}
	}
	return ""
}

// pruneUninstalled is err, the refusal of a pruning install, saying also what the prune had
// uninstalled ahead of it: an uninstall is not undone.
func pruneUninstalled(err error, gone []string) error {
	return fmt.Errorf("%w\nthe charters the prune leaves out (%s) were uninstalled ahead of the "+
		"install, and stay uninstalled", err, strings.Join(gone, ", "))
}

// pruneStopped says how a prune ended that did not uninstall everything it leaves out: gone[i]
// was not uninstalled where errs[i] says why.
func pruneStopped(root string, gone []string, errs []error) string {
	var done, not []string
	for i, at := range gone {
		if errs[i] == nil {
			done = append(done, at)
		} else {
			why := errs[i].Error()
			if !strings.HasPrefix(why, at+":") {
				why = at + ": " + why
			}
			not = append(not, why)
		}
	}
	said := "none of the charters it leaves out was uninstalled"
	if len(done) > 0 {
		said = strings.Join(done, ", ") + " uninstalled"
	}
	return fmt.Sprintf("prune %s: nothing was installed, and the prune did not uninstall %s; %s", root, strings.Join(not, "; "), said)
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
	// with the rest, record included. THE RECORD FIRST: the delete took it with the rule, and a
	// rule acts as the owner on its own record, so one put back ahead of it is placed as
	// nobody's and held for the record (trigger.Engine.asOwner) until a pass finds it. Put back
	// under its record, it is placed as whose it was.
	_, err = eng.RemoveTriggerAs(tx.ctx, tx.as(), name)
	if err == nil || errors.Is(err, trigger.ErrNotReflected) {
		tx.push(func() error {
			if err := entity.RestoreRecord(tx.store, whose); err != nil {
				return err
			}
			return eng.RestoreTrigger(tx.undoCtx(), old)
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
