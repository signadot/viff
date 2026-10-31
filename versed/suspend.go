package versed

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/trigger"
)

// Suspensions (docs/control.md): one record per thing suspended, keyed by what it covers,
// holding who holds it and why. The records are the durable half and the table — one per scope,
// baseline's on the server and a scope's beside its engine — is what every engine asks. Only the
// functions here write either, under one lock and in the same order, so the table never learns
// of a suspension by observing the store.

// errSuspensionsLoading refuses a suspension written or lifted while its table has not loaded:
// the record the write would change is one the table does not yet know.
var errSuspensionsLoading = errors.New("the suspensions are still being restored; ask again")

// errNoSuspension is a lift of a suspension nobody holds.
var errNoSuspension = errors.New("no such suspension")

// suspensionsOf is a scope's table: baseline's for the empty scope, the scope's own when its
// engine is open, and nil for a scope nobody has opened — a listing never brings one into being.
func (s *Server) suspensionsOf(scope string) *trigger.Suspensions {
	if scope == "" {
		return s.susp
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	if se, ok := s.scopeEngs[scope]; ok {
		return se.susp
	}
	return nil
}

// suspensionsFor is suspensionsOf for a write, which opens the scope's engine when nobody has:
// a suspension is a control write in its scope, and the scope's table is where it stands.
func (s *Server) suspensionsFor(scope string) *trigger.Suspensions {
	if scope != "" {
		s.scopeEngine(scope)
	}
	return s.suspensionsOf(scope)
}

// readSuspensions reads a scope's records. A scoped store reads baseline's through the overlay,
// so a record is the scope's only when its stamp says so, as a rule record is. A read that did
// not happen is reported as one, so whatever loads the table asks again.
func readSuspensions(store entity.Store, scope string) ([]trigger.Suspension, error) {
	ents, err := entity.BelowErr(store, trigger.IsSuspension, 1, entity.VerseRoot, trigger.CharterSystem, trigger.SuspensionKind)
	if err != nil {
		return nil, entity.Unread(fmt.Errorf("the suspensions could not be read, so everything stays held: %w", err))
	}
	var out []trigger.Suspension
	for _, ent := range ents {
		if entity.Str(ent.Payload, "scope") != scope {
			continue
		}
		sus, err := trigger.SuspensionOf(ent.Payload)
		if err != nil {
			// Not dropped silently, and not fatal: the rest are still suspensions somebody made.
			fmt.Fprintf(os.Stderr, "verse: suspension %s: %v\n", ent.Ref, err)
			continue
		}
		if sus.ID() != ent.Ref.Name() {
			fmt.Fprintf(os.Stderr, "verse: suspension %s reads as %s; it is kept under its record's id\n",
				ent.Ref, sus.ID())
		}
		out = append(out, sus)
	}
	return out, nil
}

// restoreSuspensions loads baseline's table, the first phase of Start: nothing is re-attached or
// restored until it has, and until then the table holds everything.
func (s *Server) restoreSuspensions() error {
	recs, err := readSuspensions(s.Store, "")
	if err != nil {
		return err
	}
	s.susp.Load(recs)
	return nil
}

// loadScopeSuspensions loads a scope's table, asked again with a growing pause until it answers:
// a table that has not loaded holds the scope's every rule, so one that gave up would freeze the
// scope until a restart rather than fail it once.
func (s *Server) loadScopeSuspensions(se *scopeEngine, scope string) {
	store := s.Store.Scope(scope)
	recs, err := readSuspensions(store, scope)
	if err == nil {
		se.susp.Load(recs)
		return
	}
	fmt.Fprintf(os.Stderr, "verse: suspensions (scope %s): asked again until they answer: %v\n", scope, err)
	go func() {
		wait := s.retryWait
		if wait <= 0 {
			wait = retryWait
		}
		for {
			select {
			case <-se.ctx.Done():
				return
			case <-time.After(wait):
			}
			recs, err := readSuspensions(store, scope)
			if err == nil {
				se.susp.Load(recs)
				return
			}
			wait = min(wait*2, retryWaitMax)
		}
	}()
}

// hold makes by a holder of the suspension in scope, writing its record and then its table under
// the one lock. The record is the system's; who holds it is what `held` says. Engine code holding
// a suspension of its own calls this with a system principal.
func (s *Server) hold(scope string, sus trigger.Suspension, by, why string) (trigger.Suspension, error) {
	s.suspMu.Lock()
	defer s.suspMu.Unlock()
	tbl := s.suspensionsFor(scope)
	if tbl == nil || !tbl.Loaded() {
		return trigger.Suspension{}, errSuspensionsLoading
	}
	store := s.storeForScope(scope)
	ref := trigger.SuspensionRef(sus.ID())
	h := trigger.Holder{Why: why, Since: s.now().UTC().Format(time.RFC3339)}
	held := map[string]trigger.Holder{}
	cur, standing := tbl.Get(sus.ID())
	for who, was := range cur.Held {
		held[who] = was
	}
	sus.Scope = scope
	w := entity.Write{At: ref.At("held", by), Value: trigger.HolderPayload(h)}
	if !standing {
		sus.Held = map[string]trigger.Holder{by: h}
		w = entity.Write{At: ref.At(), Value: sus.Payload()}
	}
	_, rev, err := store.Write(w)
	if err != nil {
		return trigger.Suspension{}, err
	}
	// The revision the hold landed at, which the write has just told us: what the listing
	// measures movement under the cover from.
	h.Occurrence = rev
	if _, _, err := store.Write(entity.Write{At: ref.At("held", by, "occurrence"), Value: entity.Val(rev)}); err != nil {
		fmt.Fprintf(os.Stderr, "verse: suspension %s: recording where %s's hold landed: %v\n", sus.ID(), by, err)
	}
	held[by] = h
	sus.Held = held
	tbl.Put(sus)
	return sus, nil
}

// release is a resume of the suspension by id in scope, by by. A person's resume lifts it,
// whoever held it; the system's removes only the system's own hold, so an automatic suspension
// lifting itself never lifts a person's. lifted says which happened; rest is the suspension as it
// stands after a hold was removed.
func (s *Server) release(scope, id, by string) (rest trigger.Suspension, lifted bool, err error) {
	s.suspMu.Lock()
	defer s.suspMu.Unlock()
	tbl := s.suspensionsOf(scope)
	if tbl == nil || !tbl.Loaded() {
		return trigger.Suspension{}, false, errSuspensionsLoading
	}
	cur, ok := tbl.Get(id)
	if !ok {
		return trigger.Suspension{}, false, errNoSuspension
	}
	store := s.storeForScope(scope)
	ref := trigger.SuspensionRef(id)
	if entity.IsSystem(by) && len(cur.Held) > 1 {
		if _, holds := cur.Held[by]; !holds {
			return cur, false, nil
		}
		if _, _, err := store.Write(entity.Write{At: ref.At("held", by), Value: nil}); err != nil {
			return trigger.Suspension{}, false, err
		}
		held := map[string]trigger.Holder{}
		for who, h := range cur.Held {
			if who != by {
				held[who] = h
			}
		}
		cur.Held = held
		tbl.Put(cur)
		return cur, false, nil
	}
	if entity.IsSystem(by) {
		if _, holds := cur.Held[by]; !holds {
			return cur, false, nil
		}
	}
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: nil}); err != nil {
		return trigger.Suspension{}, false, err
	}
	tbl.Lift(id)
	return cur, true, nil
}

// suspensionOwnHere is the ownership check a suspension makes before control is asked: a charter
// named by address is baseline's, since packages are, and a rule named in the unaddressed charter
// is the scope's whose engine holds it. Refused (inherited) otherwise, having written the response.
func (s *Server) suspensionOwnHere(w http.ResponseWriter, r *http.Request, sus trigger.Suspension) bool {
	asked := r.URL.Query().Get("scope")
	if sus.Charter != "" && sus.Charter != trigger.Every {
		if asked != "" {
			inherited(w, r, "the charter at "+sus.Charter, asked, []string{""})
			return false
		}
		return true
	}
	if sus.Charter == "" && sus.Rule != trigger.Every {
		if eng := s.engFor(asked); eng == nil || !eng.Knows(sus.Rule) {
			if holders := s.charterHolders(asked, sus.Rule); len(holders) > 0 {
				inherited(w, r, "the rule "+sus.Rule, asked, holders)
				return false
			}
		}
	}
	return true
}

// maySuspend is the check a suspension or a resume makes of its principal: control over the
// charter it names or over the subject it names, either. What it covers is decided again each
// time it applies (docs/perms.md#suspending); this refuses a record its writer has no standing for
// at all.
func (s *Server) maySuspend(w http.ResponseWriter, r *http.Request, sus trigger.Suspension, act string) bool {
	store := s.scopedStore(r)
	principal := principalOf(r)
	charter, hasCharter := sus.CharterPlace()
	subject, hasSubject := sus.SubjectPlace()
	if !hasCharter && !hasSubject {
		http.Error(w, fmt.Sprintf("%s names no charter and no subject anybody could hold control over: "+
			"name a charter's address, a rule, or a subject", sus.ID()), http.StatusBadRequest)
		return false
	}
	for _, place := range []struct {
		ref entity.Ref
		ok  bool
	}{{charter, hasCharter}, {subject, hasSubject}} {
		if !place.ok {
			continue
		}
		held, err := entity.Holds(store, principal, entity.Control, place.ref)
		if unreadable(w, r, err) {
			return false
		}
		if held {
			return true
		}
	}
	at := charter
	if !hasCharter {
		at = subject
	}
	return !refused(w, entity.Check(store, principal, entity.Control, at, act))
}

// suspensionHolders is the scopes, other than the one asked, whose table holds a suspension by
// this id: what a resume from the wrong scope is refused naming.
func (s *Server) suspensionHolders(asked, id string) []string {
	var out []string
	if asked != "" {
		if _, ok := s.susp.Get(id); ok {
			out = append(out, "")
		}
	}
	s.scopeMu.Lock()
	for name, se := range s.scopeEngs {
		if se.susp == nil || name == asked {
			continue
		}
		if _, ok := se.susp.Get(id); ok {
			out = append(out, name)
		}
	}
	s.scopeMu.Unlock()
	sort.Strings(out)
	return out
}

// suspensionRow is a suspension as the wire carries it: named by the flags that reach it, with
// who holds it and why, and what it holds now across the engines of its scope.
func (s *Server) suspensionRow(sus trigger.Suspension) api.FlowRow {
	out := api.FlowRow{Kind: api.FlowRowSuspension, ID: sus.ID(), Charter: sus.CharterFlag(),
		Recursive: sus.Recursive, Subject: sus.Subject, Scope: sus.Scope, Held: []api.SuspensionHolder{},
		Holding: &api.SuspensionHolding{}}
	covers := false
	for _, eng := range s.flowEngines(sus.Scope) {
		tasks, questions, launches := eng.Holds(sus.ID())
		out.Holding.Tasks += tasks
		out.Holding.Questions += questions
		out.Holding.Launches += launches
		covers = covers || eng.Names(sus)
	}
	out.Covers = &covers
	head := s.Store.Head()
	for _, who := range sus.Holders() {
		h := sus.Held[who]
		rec := api.SuspensionHolder{By: who, Why: h.Why, Since: h.Since, Occurrence: h.Occurrence}
		if h.Occurrence != 0 && head > h.Occurrence {
			rec.Moved = head - h.Occurrence
		}
		out.Held = append(out.Held, rec)
	}
	return out
}

// suspensionError answers a hold or a release that did not happen.
func suspensionError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errSuspensionsLoading):
		codec.HTTPError(w, r, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, errNoSuspension):
		codec.HTTPError(w, r, http.StatusNotFound, err.Error())
	case errors.Is(err, entity.ErrForbidden):
		codec.HTTPError(w, r, http.StatusForbidden, err.Error())
	default:
		codec.HTTPError(w, r, http.StatusBadGateway, err.Error())
	}
}

// heldBySuspension refuses resuming work a suspension holds, 409, naming the suspensions and the
// verb that lifts them.
func heldBySuspension(w http.ResponseWriter, r *http.Request, what string, by []string) {
	codec.HTTPError(w, r, http.StatusConflict, fmt.Sprintf(
		"%s is held by %s; resuming it would run what that suspends — `verse flow resume` with a "+
			"suspension's flags lifts it, and `verse flow --args` prints them", what, strings.Join(by, ", ")))
}

// suspensionCount is what status says of a scope's suspensions: how many are in force and what
// they hold now across the scope's engines, or nil when none is in force.
func (s *Server) suspensionCount(scope string) *api.SuspensionCount {
	tbl := s.suspensionsOf(scope)
	if tbl == nil {
		return nil
	}
	n := len(tbl.List())
	if n == 0 {
		return nil
	}
	out := &api.SuspensionCount{Count: n}
	subjects := map[string]bool{}
	for _, eng := range s.flowEngines(scope) {
		tasks, questions, launches := eng.HeldNow()
		out.Tasks += len(tasks)
		for _, lt := range tasks {
			subjects[lt.Subject] = true
		}
		out.Questions += questions
		out.Launches += launches
	}
	out.Subjects = len(subjects)
	return out
}

// markSuspendedRules marks each rule in a charter listing that a suspension holds about every
// subject with what holds it.
// markTroubledRules marks each listed rule whose firings have gone wrong with `trouble`: what
// went wrong, the latest of each kind, as the engine kept it (trigger.Trouble). The rule's
// name is left off each entry, since the entry is on the rule.
func markTroubledRules(eng *trigger.Engine, rules *ir.Node) error {
	if rules == nil || rules.Type != ir.ArrayType {
		return nil
	}
	byRule := map[string][]trigger.Trouble{}
	for _, t := range eng.Troubles() {
		byRule[t.Rule] = append(byRule[t.Rule], t)
	}
	for _, rule := range rules.Values {
		troubles := byRule[entity.Str(rule, "name")]
		if len(troubles) == 0 || rule.Type != ir.ObjectType {
			continue
		}
		node, err := codec.ToNode(troubles)
		if err != nil {
			return err
		}
		for _, t := range node.Values {
			dropField(t, "rule")
		}
		rule.Fields = append(rule.Fields, ir.FromString("trouble"))
		rule.Values = append(rule.Values, node)
	}
	return nil
}

// dropField takes one field out of an object node.
func dropField(obj *ir.Node, name string) {
	if obj == nil || obj.Type != ir.ObjectType {
		return
	}
	for i, f := range obj.Fields {
		if f.String == name {
			obj.Fields = append(obj.Fields[:i], obj.Fields[i+1:]...)
			obj.Values = append(obj.Values[:i], obj.Values[i+1:]...)
			return
		}
	}
}

// markHeldRules marks each listed rule that is held with `held: <cause>`, what it waits
// for: it fires on nothing until that arrives and the rule is armed.
func markHeldRules(pending []trigger.Pending, rules *ir.Node) {
	if rules == nil || rules.Type != ir.ArrayType {
		return
	}
	waits := map[string]string{}
	for _, p := range pending {
		waits[p.Rule] = p.Cause
	}
	for _, rule := range rules.Values {
		cause, ok := waits[entity.Str(rule, "name")]
		if !ok || rule.Type != ir.ObjectType {
			continue
		}
		rule.Fields = append(rule.Fields, ir.FromString("held"))
		rule.Values = append(rule.Values, ir.FromString(cause))
	}
}

func markSuspendedRules(eng *trigger.Engine, rules *ir.Node) {
	if rules == nil || rules.Type != ir.ArrayType {
		return
	}
	for _, rule := range rules.Values {
		name := entity.Str(rule, "name")
		if name == "" || rule.Type != ir.ObjectType {
			continue
		}
		if held := eng.SuspendedWhole(name); len(held) > 0 {
			rule.Fields = append(rule.Fields, ir.FromString("suspended"))
			rule.Values = append(rule.Values, strNodes(held))
		}
	}
}
