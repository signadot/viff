package versed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/source"
)

// ── sources: what reflects an external system into the verse ─────────────────
//
// A source is installed at `sources:<system>:<name>` carrying one way in, and it is the single
// writer of its system's slice. Where a runner's kind is the operator's own category, a
// source's system is already a question verse asks — who writes `git:*` — so it goes in the
// address, and an overlap is visible in a listing. See docs/sources.md.

// sourceFor builds the Source a registration names. The three ways in differ only in where the
// net.Conn comes from; Reflect is the same call down all three.
//
// It is here rather than in package source because `dir` needs a *source.Git built in this
// task, which is a choice about how verse hosts a source rather than part of the contract.
func (s *Server) sourceFor(e source.Entity) (source.Source, error) {
	switch {
	case e.URL != "":
		return source.HTTP(e.System, e.URL, nil), nil
	case e.Dir != "":
		if e.System != source.GitSystem {
			return nil, fmt.Errorf("source %q: `dir` is the in-process git source, so its "+
				"system must be %q, not %q", e.Name, source.GitSystem, e.System)
		}
		g := source.NewGit()
		g.AddRepo("", e.Dir) // the dir's base name is the repo id
		return g, nil
	default:
		// Check has already refused dial-in, so this is a registration that got in some other
		// way — a hand-written entity, or a field this build does not know.
		return nil, fmt.Errorf("source %q: no way in", e.Name)
	}
}

// describe asks a registration what it is, and is the whole of the install probe.
//
// A `dir` source is described WITHOUT a network, by building it and asking it — which is the
// same question `sourced` answers over HTTP, put to the same value. That is what keeps `dir`
// first-class rather than a path with fewer checks than the others.
func (s *Server) describe(r *http.Request, e source.Entity) (source.Reach, source.Descriptor, error) {
	if e.Dir != "" {
		src, err := s.sourceFor(e)
		if err != nil {
			return source.NotSource, source.Descriptor{}, err
		}
		return source.IsSource, source.Describe(src), nil
	}
	return source.Probe(r.Context(), e.URL, nil)
}

// addSource installs one, after asking it what it is.
//
// It is addRunner with two checks added, and both are what an external source loses that an
// in-process one had by construction: that it is the system somebody named, and that nobody
// else already writes the slice it claims.
func (s *Server) addSource(w http.ResponseWriter, r *http.Request) {
	// Read as a DOCUMENT first, so a field the decoder would drop can be named. It matters more
	// here than for a runner: a dropped `owns` installs a source that claims nothing, and a
	// dropped `scope` reflects into baseline where everybody sees it.
	node, err := codec.ReadNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := source.CheckFields(node); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var e source.Entity
	if err := codec.FromNode(node, &e); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := e.Check(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The scope is the REQUEST's, as it is on every other route, and the field is made to
	// agree. A registration must live in the scope it reflects into — that is what makes its
	// lifetime free, since DeleteScope then drops the overlay and takes the registration and
	// everything it wrote — so the two disagreeing is a registration in one place writing into
	// another, which nothing would ever clean up.
	scope := r.URL.Query().Get("scope")
	if e.Scope != "" && e.Scope != scope {
		http.Error(w, fmt.Sprintf("source %q: `scope: %s` but the request is against %s — a "+
			"registration lives in the scope it reflects into, so name it once, in ?scope=",
			e.Name, e.Scope, scopeName(scope)), http.StatusBadRequest)
		return
	}
	e.Scope = scope
	store := s.scopedStore(r)
	out := map[string]any{"ref": source.Ref(e.System, e.Name).String()}

	if r.URL.Query().Get("probe") != "false" {
		reach, d, why := s.describe(r, e)
		out["reach"] = reach.String()
		switch reach {
		case source.NotSource:
			http.Error(w, fmt.Sprintf("source %q: %s is not a source: %s — --no-probe installs "+
				"it anyway", e.Name, e.URL, why), http.StatusBadRequest)
			return
		case source.Unreachable:
			// Installed, and said so. A manifest landing before its pod is the ordinary state
			// of a source deployed alongside the entity that names it, so refusing would make
			// deployment order-dependent for a fact that fixes itself. The warning rides on the
			// response, because the operator running the command is who can act on it.
			out["warning"] = why.Error()
		default:
			// The check with no runner analogue. A `github` registration pointed at a git
			// source is the mistake this catches at the moment somebody is present — and it is
			// unrecoverable later, because every reflection would be written into the wrong
			// slice by a source that thinks it is right.
			if d.System != e.System {
				http.Error(w, fmt.Sprintf("source %q: you named system %q, but it says it is "+
					"%q — one of the two is wrong, and installing would write %s:* from a "+
					"source that reflects %s:*", e.Name, e.System, d.System, e.System, d.System),
					http.StatusBadRequest)
				return
			}
			// An ownership claim the operator did not write is taken from what the source SAID,
			// which is how `verse source add git:laptop <url>` gets one at all. It is a snapshot
			// exactly as the url is: what the source claimed when somebody installed it.
			if len(e.Owns) == 0 && len(d.Owns) > 0 {
				e.Owns = d.Owns
				out["owns"] = d.Owns
			}
		}
	}

	// Single-writer is what makes a reflected slice need no CAS. In task it held because
	// there was one Git value; across deployments nothing holds it but this.
	if r.URL.Query().Get("allow-overlap") != "true" {
		if why := overlaps(store, e); why != "" {
			http.Error(w, why, http.StatusConflict)
			return
		}
	}

	body := entity.Obj{"name": e.Name, "system": e.System}
	for k, v := range map[string]string{
		"url": e.URL, "dir": e.Dir, "scope": e.Scope,
	} {
		if v != "" {
			body[k] = v
		}
	}
	if len(e.Owns) > 0 {
		body["owns"] = e.Owns
	}
	if _, _, err := store.Write(entity.Write{
		At: source.Ref(e.System, e.Name).At(), Value: body.Payload(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// It reflects from here, with no restart. A registration that installed and then did
	// nothing until somebody bounced the server would be the same silence `dial-in` is refused
	// for.
	if err := s.startSource(e); err != nil {
		out["warning"] = "installed, but not running: " + err.Error()
	}
	writeOut(w, r, http.StatusCreated, out)
}

// overlaps reports why this registration may not stand beside the ones already installed, or ""
// if it provably may.
//
// Disjointness is PROVED, not assumed, and what cannot be proved is refused — so the message
// has to carry both claims, because the operator is the one who can tell whether they really
// are apart. `--allow-overlap` is the way past, and it is not free: two writers on one slice
// each tombstone what the other reflected.
//
// Scoped, which is load-bearing: a scoped source shadowing a baseline one is the feature, not a
// collision, and the store this reads is already the scope's.
func overlaps(r entity.Reader, e source.Entity) string {
	for _, other := range source.List(r) {
		if other.System == e.System && other.Name == e.Name {
			continue // a re-install of the same registration replaces it
		}
		// IN THE SAME SCOPE, which is load-bearing rather than a detail. A scoped store reads
		// baseline layered underneath, so this listing carries baseline's registrations too —
		// and a scoped source claiming what a baseline source also claims is not a collision at
		// all. It is the overlay doing its job: "in here, the repo looks like this" is the
		// question a sandbox exists to answer, and refusing it would refuse the motivating case.
		if other.Scope != e.Scope {
			continue
		}
		// An UNDECLARED ownership is not an empty one, and the difference is the whole of this
		// check. source.Disjoint answers true for an empty list, and is right to: nothing
		// intersects nothing. But a registration that carries no claim has not said it writes
		// nothing — it has not said anything, so there is nothing to prove with, and a source
		// that turns out to write the same slice would be discovered by its neighbour's refs
		// disappearing. Set arithmetic stays in source.Disjoint; this is the install's policy.
		if len(e.Owns) == 0 || len(other.Owns) == 0 {
			return refusal(e, other)
		}
		ok, err := source.Disjoint(e.Owns, other.Owns)
		if err != nil {
			return fmt.Sprintf("source %q: %v", e.Name, err)
		}
		if ok {
			continue
		}
		return refusal(e, other)
	}
	return ""
}

// refusal is the one message this check has, and it carries BOTH claims: verse could not prove
// them apart, so the operator is the one who can tell whether they really are.
// It also names the way out that is not a flag, and that is new: two sources that write
// different PLACES in the same entities are provably disjoint if they say so — `at:` on a claim
// — and converge then keeps them apart for real. `--allow-overlap` is what is left for two
// sources that genuinely write the same place, where the safety comes from nothing at all.
func refusal(e, other source.Entity) string {
	return fmt.Sprintf("refused: %s and the installed %s cannot be proved to write different "+
		"slices, and two sources on one slice each tombstone what the other reflected.\n"+
		"    this one owns: %s\n    %s owns: %s\n"+
		"if they write different PLACES in the same entities, say so — a claim may name a path "+
		"inside the payload ({ref: \"...\", at: [owners, ENG]}), and two of those are proved "+
		"apart\n"+
		"--allow-overlap installs it anyway, with the cost above",
		source.Ref(e.System, e.Name), source.Ref(other.System, other.Name),
		claims(e.Owns), source.Ref(other.System, other.Name), claims(other.Owns))
}

// claims renders an ownership list for a message, saying so when there is none — a source that
// claimed nothing is the commonest reason this refusal fires, and "owns: []" would not say why.
func claims(owns []source.Own) string {
	if len(owns) == 0 {
		return "nothing (it claimed no ownership, so it can be proved apart from nothing)"
	}
	out := make([]string, 0, len(owns))
	for _, o := range owns {
		out = append(out, o.String())
	}
	return strings.Join(out, ", ")
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	probe := r.URL.Query().Get("probe") == "true"
	out := make([]map[string]any, 0)
	for _, e := range source.List(store) {
		rec := map[string]any{
			"ref": source.Ref(e.System, e.Name).String(), "name": e.Name, "system": e.System,
		}
		for k, v := range map[string]string{
			"url": e.URL, "dir": e.Dir, "scope": e.Scope,
			"lastReflect": e.LastReflect, "lastError": e.LastError,
		} {
			if v != "" {
				rec[k] = v
			}
		}
		if len(e.Owns) > 0 {
			rec["owns"] = e.Owns
		}
		// When a pass last COMPLETED, from the live reflector — the signal that catches a source
		// that has wedged (alive, answering, reflecting nothing new), which the stored
		// lastReflect cursor cannot: on a quiet repo a healthy source and a wedged one carry the
		// same cursor. Read here and never written, because it is an opinion about now.
		s.srcMu.Lock()
		run := s.srcRuns[source.Ref(e.System, e.Name).String()]
		s.srcMu.Unlock()
		if run != nil {
			if pass, why := run.status(); pass != "" {
				rec["lastPass"] = pass
				if why != "" {
					rec["failing"] = why
				}
			}
		} else {
			rec["running"] = false
		}
		// Asked for, never stored. A reachability column is as of now or it is a cache that can
		// be stale in the direction that costs — saying a source is well because it was well
		// when something last looked.
		if probe {
			reach, _, why := s.describe(r, e)
			rec["reach"] = reach.String()
			if why != nil {
				rec["why"] = why.Error()
			}
		}
		out = append(out, rec)
	}
	writeOut(w, r, http.StatusOK, out)
}

func (s *Server) getSource(w http.ResponseWriter, r *http.Request) {
	ref := source.Ref(r.PathValue("system"), r.PathValue("name"))
	e, ok, err := s.scopedStore(r).Get(ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeOut(w, r, http.StatusOK, entity.ToObj(e.Payload))
}

// checkSource asks one what it is, right now — including what it claims to own, which is what
// an operator reads before deciding whether `--allow-overlap` is honest.
func (s *Server) checkSource(w http.ResponseWriter, r *http.Request) {
	system, name := r.PathValue("system"), r.PathValue("name")
	e, ok := source.Find(s.scopedStore(r), system+":"+name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	reach, d, why := s.describe(r, e)
	out := map[string]any{
		"ref": source.Ref(system, name).String(), "reach": reach.String(),
	}
	if e.URL != "" {
		out["url"] = e.URL
	}
	if why != nil {
		out["why"] = why.Error()
	}
	// What it says NOW, beside what was recorded at install. Neither is merged into the other:
	// a claim that has drifted is a fact an operator should see as two values, not as one.
	if reach == source.IsSource {
		out["says"] = map[string]any{"system": d.System, "owns": d.Owns}
	}
	writeOut(w, r, http.StatusOK, out)
}

// removeSource takes the registration away, and answers the question a delete cannot: what
// happens to what it reflected.
//
// Tombstoning always is right about the common case and wrong about one that will happen. A
// slice nobody reflects any more is a slice nothing can correct, and a charter would go on
// believing in a branch of a repo verse has stopped watching — so `--forget` is usually what
// an operator means. But MOVING A REPO TO ANOTHER HOST is a remove and an add of the same
// physical truth, and tombstoning it fires every branch-deleted trigger in the verse and then
// every branch-created one behind it: a bookkeeping change destroying and re-creating live
// facts. That is the runner's rename case in a different costume, and it gets the runner's
// resolution — removal with a non-empty reflected slice is REFUSED until told which.
//
// A removal with nothing reflected needs no flag, there being nothing to be wrong about. And
// the removal itself is never refused — `--keep` always succeeds — because a refusal with no
// way to satisfy it would be a trap.
func (s *Server) removeSource(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	system, name := r.PathValue("system"), r.PathValue("name")
	ref := source.Ref(system, name)
	e, ok := source.Find(store, system+":"+name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	forget, keep := q.Get("forget") == "true", q.Get("keep") == "true"
	reflected, err := reflectedBy(store, e)
	if err != nil {
		// Refused rather than approximated: this decides what a --forget deletes.
		http.Error(w, fmt.Sprintf("%s: %v", ref, err), http.StatusInternalServerError)
		return
	}

	if len(reflected) > 0 && !forget && !keep {
		http.Error(w, fmt.Sprintf(
			"refused: %s has reflected %d entit(ies), and removing it means one of two things "+
				"this cannot tell apart:\n"+
				"    --forget   the reflection is over; tombstone what it wrote\n"+
				"    --keep     the same truth is moving elsewhere; leave the last known state, "+
				"now frozen and quietly ageing", ref, len(reflected)), http.StatusConflict)
		return
	}
	// Stopped BEFORE anything else, so nothing is left writing a slice no registration claims —
	// and so a pass in flight cannot re-reflect what --forget is about to tombstone.
	s.stopSource(ref)

	out := map[string]any{"removed": ref.String()}
	if forget {
		var gone int
		for _, p := range reflected {
			// At the PLACE claimed: an entity for every claim that carries no path, and a slot
			// for one that does — a nil value removes whatever stands there, at either depth.
			if _, _, err := store.Write(entity.Write{At: p}); err != nil {
				http.Error(w, fmt.Sprintf("tombstoning %s: %v", p, err), http.StatusInternalServerError)
				return
			}
			gone++
		}
		out["forgotten"] = gone
	} else if len(reflected) > 0 {
		out["kept"] = len(reflected)
	}
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: nil}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeOut(w, r, http.StatusOK, out)
}

// reflectedBy returns the PLACES standing in the store that this registration's ownership
// claims, in path order so a --forget deletes in the same order twice.
//
// Bounded by `owns` and by nothing else, which is what makes a --forget safe: it can only
// tombstone what the install PROVED this source was the single writer of. A registration that
// claimed nothing reflected nothing, as far as a removal is concerned — under-claiming there
// leaves entities standing, which is the direction to be wrong in.
//
// A PLACE rather than a ref, because a claim may be to a slot inside a shared entity. Deleting
// the entity there would take the other owners' slots with it, so what is removed is what was
// claimed — and for every claim that carries no path, which is all of them until a source
// declares one, a place IS its entity and this is what it always did.
//
// The claims are parsed ONCE, and a claim that does not parse fails the whole answer rather
// than being skipped. This is the caller that deletes: a skipped claim there is either an
// entity deleted because verse could not read the claim that protected it, or one left standing
// because it could not read the claim that owned it, and neither is a thing to decide in
// silence. An install refuses an unparseable claim, so reaching this needs a store somebody
// wrote by hand — which is exactly when the loud answer is the one that helps.
func reflectedBy(r entity.Reader, e source.Entity) ([]entity.Path, error) {
	claims, err := source.ParseOwns(e.Owns)
	if err != nil {
		return nil, err
	}
	systems := map[string]bool{}
	wildcard := false
	for _, c := range claims {
		if c.System == "" {
			wildcard = true
		}
		systems[c.System] = true
	}
	var out []entity.Path
	for _, ent := range r.List(func(ref entity.Ref) bool { return wildcard || systems[ref.System] }) {
		out = append(out, source.Claimed(claims, ent.Ref)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

// ── the reflector under a registration ───────────────────────────────────────

// ResyncEvery is the periodic full pass every installed source runs — the net that catches
// every missed hint. Generous rather than tight: a hint is what makes a change land in
// milliseconds, and this is what makes a MISSED hint land eventually, so tightening it buys
// latency nobody is waiting on at the cost of asking every external system a question it has
// already answered.
const ResyncEvery = time.Minute

// sourceRun is one installed source's reflector: the handle that stops it, and the two facts
// about it that are reported rather than stored.
//
// lastPass is deliberately in memory. It is when a pass last COMPLETED, whatever it did, which
// is what catches a source that has wedged — and it is an opinion about now, so it follows the
// runner's rule: a reachability fact that is written down can be stale in the direction that
// costs. What IS written is in recordPass.
type sourceRun struct {
	rf     *source.Reflector
	scope  string
	cancel context.CancelFunc
	// done closes when Run returns, which is what lets a stop WAIT rather than merely signal.
	// Nil on a run nobody started (a test builds one to drive recordPass directly), and stop
	// treats that as already finished.
	done chan struct{}

	mu       sync.Mutex
	lastPass string // RFC3339, or empty until the first pass returns
	lastErr  string // the current error state, for the transition test
}

// stopDrain bounds how long a stop waits for a reflector's loop to return.
//
// It can be short because of the FENCE, and the two are one mechanism: a cancelled reflector
// stops issuing writes at the next entity (see Reflector.converge), so what is being waited out
// is one store round trip and a Reflect that ctx already aborted — not a pass, which can run to
// a quarter of an hour. Without the fence this number would have to be the pass length, which
// is why draining alone was never the answer.
//
// On expiry the stop proceeds anyway. A wedged source must not be able to hold a scope delete
// open, and the fence means the worst it can still do is land the one write it had in flight.
const stopDrain = 2 * time.Second

// stop ends this reflector and waits for its loop to be off the store.
//
// Cancelling alone is not enough for a caller that is about to drop the data underneath it. A
// scope delete is not a write, so no compare-and-swap can be ordered against it: a pass that
// passed its precondition before the delete lands after it and re-creates the overlay it was
// writing into. Waiting until Run has returned is what makes "the sources are stopped" true at
// the moment the delete is issued rather than merely requested.
func (r *sourceRun) stop() {
	r.cancel()
	if r.done == nil {
		return
	}
	select {
	case <-r.done:
	case <-time.After(stopDrain):
	}
}

func (r *sourceRun) status() (lastPass, lastErr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastPass, r.lastErr
}

// startSource runs a reflector for a registration, replacing one already running under that
// address. It is called at install and at restart, so those two paths cannot drift.
func (s *Server) startSource(e source.Entity) error {
	src, err := s.sourceFor(e)
	if err != nil {
		return err
	}
	ref := source.Ref(e.System, e.Name)
	store := s.Store
	if e.Scope != "" {
		// A scoped source writes through the scope's overlay, so its reflections fire that
		// scope's triggers and nobody else's.
		store = s.Store.Scope(e.Scope)
	}
	rf := source.NewReflector(src, store, ResyncEvery)
	run := &sourceRun{rf: rf, scope: e.Scope}

	// The context is built BEFORE the pass sink, so the sink can be fenced by it: a status
	// write is a write like any other, and one landing after its scope was dropped re-creates
	// the registration as a stub (see stillInstalled). Ordering it here rather than storing the
	// context on the run keeps it a closure over a lifetime instead of a field to remember.
	ctx, cancel := context.WithCancel(s.ctx)
	run.cancel = cancel
	rf.OnPass(func(p source.Pass) { s.recordPass(ctx, store, ref, run, p) })
	s.srcMu.Lock()
	// A re-install replaces, and the OLD one has to be stopped first — two reflectors on one
	// slice is exactly what the disjointness check at install exists to prevent, so leaking one
	// here would be the same bug arriving through the back door.
	old, replacing := s.srcRuns[ref.String()]
	run.done = make(chan struct{})
	s.srcRuns[ref.String()] = run
	s.srcMu.Unlock()
	// Outside the lock, because stop WAITS now: holding srcMu across a drain would block every
	// other source's install and stop behind one slow reflector.
	if replacing {
		old.stop()
	}

	go func() {
		defer close(run.done)
		_ = rf.Run(ctx)
	}()
	return nil
}

// stopSource ends a registration's reflector, if one is running.
func (s *Server) stopSource(ref entity.Ref) {
	s.srcMu.Lock()
	run, ok := s.srcRuns[ref.String()]
	delete(s.srcRuns, ref.String())
	s.srcMu.Unlock()
	if ok {
		run.stop()
	}
}

// stopScopeSources ends every reflector reflecting INTO a scope, which is what a scope going
// away means for the sources in it. Called before the overlay is dropped, so nothing is left
// writing into a scope that no longer exists.
func (s *Server) stopScopeSources(scope string) {
	s.srcMu.Lock()
	var doomed []*sourceRun
	for ref, run := range s.srcRuns {
		if run.scope == scope {
			doomed = append(doomed, run)
			delete(s.srcRuns, ref)
		}
	}
	s.srcMu.Unlock()
	// Waited out, not just signalled: this is the call deleteScope makes immediately before it
	// drops the overlay, so "stopped" has to mean off the store rather than told to go.
	for _, run := range doomed {
		run.stop()
	}
}

// scopeName spells a scope for a message, since the empty one has a name people use.
func scopeName(scope string) string {
	if scope == "" {
		return "baseline"
	}
	return scope
}

// stopAllSources ends every reflector this server is running.
//
// In production the lifetime is Start's context and cancelling it is enough. This exists for
// the case that has no such context — a server assembled and never started, which is what a
// test builds — where a reflector would otherwise outlive the store it writes to and go on
// polling into a closed session.
func (s *Server) stopAllSources() {
	s.srcMu.Lock()
	runs := s.srcRuns
	s.srcRuns = map[string]*sourceRun{}
	s.srcMu.Unlock()
	for _, run := range runs {
		run.stop()
	}
}

// recordPass writes what a pass says about ITSELF onto the registration, and nothing else.
//
// Two rules, and both are about not making a quiet verse noisy:
//
//   - `lastError` is written on TRANSITION only. A source failing every minute writes once, and
//     the write that clears it is the one that says it recovered.
//   - `lastReflect` is written only when a pass actually WROTE something. It is the poll
//     cursor: when verse last changed the verse because of this source. Stamping it every pass
//     would put a write on `sources:*` once per resync interval forever, and anything watching
//     that slice would fire on a verse where nothing is happening.
//
// What that costs is stated in docs/sources.md: the cursor cannot catch a source
// that has wedged, since a healthy quiet one looks the same. That is what the in-memory
// lastPass above is for, and why it is reported rather than written.
func (s *Server) recordPass(ctx context.Context, store entity.Store, ref entity.Ref, run *sourceRun, p source.Pass) {
	// A cancelled pass is a SHUTDOWN, not a fault, and recording it would be a lie that
	// outlives the task: every source would come back from a clean stop carrying
	// "context canceled" as its last error, and an operator reading the listing after a
	// restart would see a verse full of broken sources that are all fine.
	if errors.Is(p.Err, context.Canceled) {
		return
	}
	// And a pass that SUCCEEDED just before the stop is the case the line above cannot catch,
	// because nothing about it is an error: p.Err is nil, p.Wrote is positive, and the write
	// below is the one that lands in a scope that has since been deleted. stillInstalled is
	// the precondition meant to stop it and cannot, because it is a compare-and-swap and a
	// scope delete is not a write to be ordered against — so the CAS reads `name` present,
	// the overlay is dropped, and the patch re-creates the registration as a stub carrying
	// nothing but a timestamp. That was reproducible in roughly one run in five.
	//
	// The context is the fence the CAS cannot be: it is the reflector's own lifetime, and once
	// it is done this run has no business writing anything anywhere.
	if err := ctx.Err(); err != nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var errText string
	if p.Err != nil {
		errText = p.Err.Error()
	}
	run.mu.Lock()
	changed := run.lastErr != errText
	run.lastPass, run.lastErr = now, errText
	run.mu.Unlock()

	if changed {
		// A nil value REMOVES the field, which is how a recovery is recorded: the registration
		// stops carrying an error rather than carrying an empty one.
		w := entity.Write{At: ref.At("lastError"), When: []entity.Require{stillInstalled(ref)}}
		if errText != "" {
			w.Value = entity.Val(errText)
		}
		if _, _, err := store.Write(w); err != nil {
			fmt.Fprintf(os.Stderr, "verse: source %s: recording error: %v\n", ref, err)
		}
	}
	if p.Err == nil && p.Wrote > 0 {
		if _, _, err := store.Write(entity.Write{
			At: ref.At("lastReflect"), Value: entity.Val(now),
			When: []entity.Require{stillInstalled(ref)},
		}); err != nil {
			fmt.Fprintf(os.Stderr, "verse: source %s: recording lastReflect: %v\n", ref, err)
		}
	}
}

// stillInstalled is the precondition every status write carries: the registration is still
// there.
//
// Without it a removal loses a race it cannot win. A write at a FIELD of an absent entity
// CREATES that entity — so a pass already in flight when the registration went away brings it
// back as a stub carrying nothing but a timestamp: an installed-looking source with no url and
// no ownership, which verse would then try to restore on every restart. Stopping the reflector
// first narrows the window and does not close it, because a pass mid-flight has already read
// the world it is about to write about.
//
// It asks about `name` rather than about the entity, and that is the whole subtlety.
// entity.Present() is a NULL pattern — "matches any value, so only a missing path fails it" —
// and a tombstone IS a null value at the entity's own path (entity.Absent() is `!irtype null`,
// and absentPath in logdstore ORs it with the no-key case). So a presence test on the ref is
// satisfied by the very tombstone it is meant to notice. A field inside it is not: removing the
// entity takes the subtree, so `name` has no key at all, and the presence test fails as
// intended.
func stillInstalled(ref entity.Ref) entity.Require {
	return entity.Require{At: ref.At("name"), Pattern: entity.Present()}
}

// restoreSources starts a reflector for every registration standing in the verse, so the
// program a restart comes back to is the one it went down with.
//
// It runs AFTER the charter, and that ordering is load-bearing: a reflection is a write that
// triggers react to, so a source restored before its rules would deliver its first pass into a
// verse that has not got them back yet — and a level-triggered pass does not repeat itself, so
// what those rules missed they would miss for good.
//
// SCOPED REGISTRATIONS ARE NOT RESTORED, and cannot be with what the store offers: there is no
// way to enumerate scopes (entity.Store has Scope and DeleteScope and nothing that lists), and
// a scoped store reads baseline layered underneath, so nothing here can find them. It is
// exactly the gap scoped charters have, and it is survivable for the same reason: a scope is
// created on demand by whatever owns it — `verse connect` for a session, a sandbox for its own
// lifetime — so the thing that made the scope is the thing that can install into it again.
func (s *Server) restoreSources() {
	for _, e := range source.List(s.Store) {
		if e.Scope != "" {
			continue
		}
		if err := s.startSource(e); err != nil {
			// Reported and skipped, so one bad registration does not cost the rest — the same
			// rule LoadCharter follows for a rule that will not restore.
			fmt.Fprintf(os.Stderr, "verse: source %s: %v\n", source.Ref(e.System, e.Name), err)
		}
	}
}

// hintSource takes a low-latency hint: something changed over there, look now.
//
// THE HINT LAW, and the whole reason this route is shaped as it is: a hint NEVER writes
// entities. It names the slice that went stale and schedules a reconcile, which reads the
// external source of truth and converges — so a delivery that is duplicated, reordered or
// forged costs at most a reconcile of a slice its sender does not get to choose the contents
// of. That is what lets this route stay unauthenticated in a wide-open dogfood verse, and it
// is why the refs arrive as query parameters rather than as a body: there is nothing to send
// but which slice, and a `/bin/sh` git hook running `curl` is the sender to keep to one line.
//
// verse does not PARSE deliveries. The source owns delivery and parsing, because the source is
// what deployed the git hook or holds the webhook secret; a second implementation of
// "map a delivery to refs" per source is how the law comes to be broken in one of two places.
func (s *Server) hintSource(w http.ResponseWriter, r *http.Request) {
	system, name := r.PathValue("system"), r.PathValue("name")
	ref := source.Ref(system, name)
	if _, ok := source.Find(s.scopedStore(r), system+":"+name); !ok {
		http.NotFound(w, r)
		return
	}
	var refs []entity.Ref
	for _, raw := range r.URL.Query()["ref"] {
		parsed, err := entity.ParseRef(raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		refs = append(refs, parsed)
	}
	s.srcMu.Lock()
	run := s.srcRuns[ref.String()]
	s.srcMu.Unlock()
	if run == nil {
		// Registered but not running — a `dir` whose path went away, or a restore that failed.
		// Said out loud rather than accepted: a hint that is swallowed looks exactly like one
		// that worked, and the sender would go on believing its changes were landing.
		http.Error(w, fmt.Sprintf("%s is installed but not running; its hint cannot be "+
			"scheduled (see lastError on the registration)", ref), http.StatusServiceUnavailable)
		return
	}
	run.rf.Hint(refs...) // no refs is a full resync
	w.WriteHeader(http.StatusNoContent)
}
