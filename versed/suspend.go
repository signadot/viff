package versed

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/trigger"
)

// Suspensions (docs/working/suspend.md): one record per thing suspended, keyed by what it covers,
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
	ents := entity.ListUnder(store, trigger.IsSuspension, entity.VerseRoot, trigger.CharterSystem, trigger.SuspensionKind)
	if err := entity.ReadFailed(store); err != nil {
		return nil, fmt.Errorf("the suspensions could not be read, so everything stays held: %w", err)
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
		if sus.ID() != ent.Ref.ID() {
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
	h := trigger.Holder{Why: why, Since: time.Now().UTC().Format(time.RFC3339)}
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
	tbl.Remove(id)
	return cur, true, nil
}

// suspensionOf reads a request's body into the suspension it names.
func suspensionOf(w http.ResponseWriter, r *http.Request) (api.Suspend, trigger.Suspension, bool) {
	var body api.Suspend
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return body, trigger.Suspension{}, false
	}
	sus, err := trigger.NewSuspension(body.Charter, body.Rule, body.Subject)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return body, trigger.Suspension{}, false
	}
	return body, sus, true
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
// time it applies (docs/working/suspend.md#authority-is-decided-when-it-applies); this refuses a
// record its writer has no standing for at all.
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

// suspend is `POST /suspend`: hold a suspension, as the request's principal, for why.
func (s *Server) suspend(w http.ResponseWriter, r *http.Request) {
	body, sus, ok := suspensionOf(w, r)
	if !ok {
		return
	}
	if body.Why == "" {
		http.Error(w, "why is required: a suspension says what it is for, since nothing ends it but "+
			"somebody reading that", http.StatusBadRequest)
		return
	}
	if sus.EveryCharter() && sus.EverySubject() {
		http.Error(w, "every charter about every subject is not a suspension anybody can stand behind: "+
			"name a charter, a subject, or both", http.StatusBadRequest)
		return
	}
	if !s.suspensionOwnHere(w, r, sus) || !s.maySuspend(w, r, sus, "suspending it") {
		return
	}
	scope := r.URL.Query().Get("scope")
	by := principalOf(r)
	if by == "" {
		by = entity.SystemDefault
	}
	held, err := s.hold(scope, sus, by, body.Why)
	if err != nil {
		suspensionError(w, r, err)
		return
	}
	writeOut(w, r, http.StatusOK, s.suspensionRecord(held))
}

// resume is `POST /resume`: lift the suspension the body names, whoever holds it.
func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	_, sus, ok := suspensionOf(w, r)
	if !ok {
		return
	}
	scope := r.URL.Query().Get("scope")
	if tbl := s.suspensionsOf(scope); tbl != nil && tbl.Loaded() {
		if _, standing := tbl.Get(sus.ID()); !standing {
			if holders := s.suspensionHolders(scope, sus.ID()); len(holders) > 0 {
				inherited(w, r, "the suspension "+sus.ID(), scope, holders)
				return
			}
		}
	}
	if !s.suspensionOwnHere(w, r, sus) || !s.maySuspend(w, r, sus, "resuming it") {
		return
	}
	by := principalOf(r)
	if by == "" {
		by = entity.SystemDefault
	}
	rest, _, err := s.release(scope, sus.ID(), by)
	if err != nil {
		suspensionError(w, r, err)
		return
	}
	out := s.suspensionRecord(rest)
	// SUSPENSIONS ONLY ADD UP: lifting a pair a wider suspension also covers leaves the pair held,
	// and the answer says by what rather than letting the reader believe it runs.
	for _, other := range s.suspensionsOf(scope).List() {
		if other.ID() != sus.ID() && sus.Within(other) {
			out.Still = append(out.Still, other.ID())
		}
	}
	writeOut(w, r, http.StatusOK, out)
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

// listSuspensions is `GET /suspensions`: every suspension in force in the request's scope.
func (s *Server) listSuspensions(w http.ResponseWriter, r *http.Request) {
	tbl := s.suspensionsOf(r.URL.Query().Get("scope"))
	out := []api.Suspension{}
	if tbl == nil {
		writeOut(w, r, http.StatusOK, out)
		return
	}
	if !tbl.Loaded() {
		suspensionError(w, r, errSuspensionsLoading)
		return
	}
	for _, sus := range tbl.List() {
		out = append(out, s.suspensionRecord(sus))
	}
	writeOut(w, r, http.StatusOK, out)
}

// suspensionRecord is a suspension as the wire carries it.
func (s *Server) suspensionRecord(sus trigger.Suspension) api.Suspension {
	out := api.Suspension{ID: sus.ID(), Charter: sus.Charter, Rule: sus.Rule, Subject: sus.Subject,
		Scope: sus.Scope, Held: []api.SuspensionHolder{}}
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
