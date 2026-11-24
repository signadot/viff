package versed

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/internal/trigger"
	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
)

// THE CHARTER CHECK: whether a tree of charters would install at an address over what runs there,
// and what would change, with nothing installed. `POST /charter/check?at=<address>` takes the
// documents an install with a prune would take (`verse c -a <address> -d <dir> --prune`), and
// answers api.CharterCheck: a verdict, `installs` or `refused` with the refusal, the rules and
// strata each package adds, changes and removes, the rules that would be held, and the packages
// the prune would take away.
//
// IT IS A QUESTION, NEVER PART OF THE INSTALL, and kept apart from it at every turn (dzxn44gw):
// its own route and file, no charter lock, no transaction, and no engine that runs anything. It
// asks the checks an install asks, each without its write: the files split into packages as an
// install splits them, the prune's targets as an install computes them, the address as the
// installer's to take, the outboxes against the iods registered, and each package's strata and
// rules in an engine of the check's own that is never started (trigger.Check), each spawn against
// what the runners offer as the install's is. A
// verdict of `installs` says the install would not be refused by any of these; what it cannot say
// is what the store answers on the day, which is the install's own.
//
// `?runners=to-come` asks for the vet a restore places a rule with: a spawn no runner offers at
// all is held, "waiting for a runner", where the install refuses it — for a charter checked
// against a verse whose runners are not up yet. Without it, or with `checked`, the check vets as
// the install does, whenever it is asked.
func (s *Server) checkCharter(w http.ResponseWriter, r *http.Request) {
	docs, err := codec.ReadNodes(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	at := r.URL.Query().Get("at")
	if at == "" {
		http.Error(w, "a check names the address the tree would be installed at (?at=)", http.StatusBadRequest)
		return
	}
	runners := trigger.RunnersChecked
	switch v := r.URL.Query().Get("runners"); v {
	case "", "checked":
	case "to-come":
		runners = trigger.RunnersToCome
	default:
		http.Error(w, fmt.Sprintf("runners is checked or to-come, not %q", v), http.StatusBadRequest)
		return
	}
	// A task verse holds no launch of is answered as any refusal is: the verdict.
	hold, err := s.taskCharterOf(r)
	if err != nil {
		writeOut(w, r, http.StatusOK, api.CharterCheck{At: at, Verdict: "refused", Refusal: err.Error()})
		return
	}
	writeOut(w, r, http.StatusOK, s.checkCharterAt(s.scopedStore(r), r.URL.Query().Get("scope"), at, docs, hold, runners))
}

// checkCharterAt is the check, as the store's principal, in scope ("" for baseline): a refusal is
// the verdict, never an error. hold is the task asking, whose install would be held within it
// (chartertask.go), or nil; runners is what the vet makes of a spawn no runner offers.
func (s *Server) checkCharterAt(store entity.Store, scope, at string, docs []*ir.Node, hold *taskCharter, runners trigger.Runners) api.CharterCheck {
	out := api.CharterCheck{At: at, Verdict: "installs"}
	refuse := func(err error) api.CharterCheck {
		out.Verdict, out.Refusal, out.Packages = "refused", err.Error(), nil
		return out
	}
	installer := entity.PrincipalOf(store)
	if installer == "" {
		return refuse(errors.New("a charter names who installs it, and this check names nobody"))
	}
	groups, err := charterFilesPruning(docs, "", at)
	if err != nil {
		return refuse(err)
	}
	gone, _, err := s.pruneTargets(store, "", at, groups)
	if err != nil {
		return refuse(err)
	}
	out.Pruned = gone
	for _, g := range groups {
		if err := s.lockedAt(scope, g.at); err != nil {
			return refuse(err)
		}
		if _, err := checkPackageAddress(store, installer, g.at); err != nil {
			return refuse(err)
		}
		if err := ttlInScope(g, scope); err != nil {
			return refuse(err)
		}
		pkg, err := s.checkPackage(store, installer, g, hold, runners)
		if err != nil {
			return refuse(err)
		}
		out.Packages = append(out.Packages, pkg)
	}
	return out
}

// checkPackage checks one package of the tree and says what would change in it.
func (s *Server) checkPackage(store entity.Store, installer string, g charterGroup, hold *taskCharter, runners trigger.Runners) (api.CharterCheckPackage, error) {
	pkg := api.CharterCheckPackage{At: g.at}
	confine, err := trigger.ParseAt(g.at)
	if err != nil {
		return pkg, err
	}
	var reads []trigger.Subtree
	for _, def := range g.reads {
		subs, err := trigger.ReadsFromNode(def)
		if err != nil {
			return pkg, err
		}
		reads = append(reads, subs...)
		sends, err := trigger.SendsFromNode(def, g.at)
		if err != nil {
			return pkg, err
		}
		for _, send := range sends {
			if _, ok := ioFor(s.Store, send.To); !ok {
				return pkg, errors.New("sends: " + send.To + " is no io's out place — an outbox sends to a place " +
					"a registered io reads (`verse iod` lists them, with their `out.places`)")
			}
		}
	}
	var strata []trigger.Stratum
	for _, def := range g.strata {
		var st trigger.Stratum
		if err := codec.FromNode(def, &st); err != nil {
			return pkg, err
		}
		strata = append(strata, st)
	}
	var rules []trigger.Trigger
	for _, def := range g.rules {
		if err := trigger.CheckFields(def); err != nil {
			return pkg, err
		}
		var t trigger.Trigger
		if err := codec.FromNode(def, &t); err != nil {
			return pkg, err
		}
		if t.Name == "" {
			return pkg, errors.New("a rule needs a name")
		}
		if err := hold.holds(t); err != nil {
			return pkg, err
		}
		rules = append(rules, t)
	}
	held, err := trigger.Check(store, confine, reads, installer, strata, rules, s.offerCheck(), runners)
	if err != nil {
		return pkg, err
	}
	pkg.Held = held
	// WHAT WOULD CHANGE, against the package as it runs: read from its engine where one stands,
	// and never by asking for one, which would create it.
	stood := map[string][]byte{}
	if eng, ok := s.standingPackage(confine); ok {
		for _, t := range append(eng.Triggers(), eng.Held()...) {
			stood["rules."+t.Name] = encoded(t)
		}
		for _, st := range eng.Strata() {
			stood["strata."+st.Name] = encoded(st)
		}
	}
	now := map[string][]byte{}
	for _, t := range rules {
		now["rules."+t.Name] = encoded(t)
	}
	for _, st := range strata {
		now["strata."+st.Name] = encoded(st)
	}
	for name, b := range now {
		was, ok := stood[name]
		switch {
		case !ok:
			pkg.Added = append(pkg.Added, name)
		case !bytes.Equal(was, b):
			pkg.Changed = append(pkg.Changed, name)
		}
	}
	for name := range stood {
		if _, ok := now[name]; !ok {
			pkg.Removed = append(pkg.Removed, name)
		}
	}
	sort.Strings(pkg.Added)
	sort.Strings(pkg.Changed)
	sort.Strings(pkg.Removed)
	return pkg, nil
}

// standingPackage is the engine running the package at confine, where one stands.
func (s *Server) standingPackage(confine trigger.Subtree) (*trigger.Engine, bool) {
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	pe, ok := s.scopeEngs["at:"+confine.Stamp()]
	if !ok {
		return nil, false
	}
	return pe.eng, true
}

// encoded is a definition as bytes, to tell one that changed from one that did not: the same
// definition is the same bytes, whichever way it came.
//
// CANONICAL, because the two sides come different ways. The tree's rules are parsed from the
// files, whose objects keep the order they were written in; a standing rule is the one parsed
// at install until a restart, and after one it is parsed from its stored spec, and the store
// gives an object's keys back sorted. Compared as written, every rule whose values were not
// written in sorted order changed at the restart, and the reconciler's minute check opened an
// issue saying verse/live is not what runs over a verse that was running exactly it. Keys are
// sorted at every depth before the bytes are taken, and comments dropped: a comment is not a
// rule's meaning, and it is not what an install changes.
func encoded(v any) []byte {
	n, err := codec.ToNode(v)
	if err != nil {
		return nil
	}
	b, err := codec.NodeBytes(trigger.Canonical(ir.StripComments(n)), codec.Tony)
	if err != nil {
		return nil
	}
	return b
}
