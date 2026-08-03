package versed

import (
	"context"
	"fmt"
	"net/http"
	"os"
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
// address, and an overlap is visible in a listing. See docs/working/sources.md.

// sourceFor builds the Source a registration names. The three ways in differ only in where the
// net.Conn comes from; Reflect is the same call down all three.
//
// It is here rather than in package source because `dir` needs a *source.Git built in this
// process, which is a choice about how verse hosts a source rather than part of the contract.
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

	// Single-writer is what makes a reflected slice need no CAS. In process it held because
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
func refusal(e, other source.Entity) string {
	return fmt.Sprintf("refused: %s and the installed %s cannot be proved to write different "+
		"slices, and two sources on one slice each tombstone what the other reflected.\n"+
		"    this one owns: %s\n    %s owns: %s\n"+
		"--allow-overlap installs it anyway, with that cost",
		source.Ref(e.System, e.Name), source.Ref(other.System, other.Name),
		claims(e.Owns), source.Ref(other.System, other.Name), claims(other.Owns))
}

// claims renders an ownership list for a message, saying so when there is none — a source that
// claimed nothing is the commonest reason this refusal fires, and "owns: []" would not say why.
func claims(owns []string) string {
	if len(owns) == 0 {
		return "nothing (it claimed no ownership, so it can be proved apart from nothing)"
	}
	return strings.Join(owns, ", ")
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

// removeSource takes the registration away. The fork — what happens to what it reflected — is
// docs/working/sources-plan.md step 5; today it removes the entity and nothing else.
func (s *Server) removeSource(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	system, name := r.PathValue("system"), r.PathValue("name")
	ref := source.Ref(system, name)
	if _, ok := source.Find(store, system+":"+name); !ok {
		http.NotFound(w, r)
		return
	}
	// Stopped BEFORE the entity goes, so nothing is left writing a slice no registration
	// claims — a reflector outliving its registration is a writer past the disjointness check.
	s.stopSource(ref)
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: nil}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeOut(w, r, http.StatusOK, map[string]any{"removed": ref.String()})
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
	cancel context.CancelFunc

	mu       sync.Mutex
	lastPass string // RFC3339, or empty until the first pass returns
	lastErr  string // the current error state, for the transition test
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
	run := &sourceRun{}
	rf := source.NewReflector(src, store, ResyncEvery)
	rf.OnPass(func(p source.Pass) { s.recordPass(store, ref, run, p) })

	ctx, cancel := context.WithCancel(s.ctx)
	run.cancel = cancel
	s.srcMu.Lock()
	// A re-install replaces, and the OLD one has to be stopped first — two reflectors on one
	// slice is exactly what the disjointness check at install exists to prevent, so leaking one
	// here would be the same bug arriving through the back door.
	if old, ok := s.srcRuns[ref.String()]; ok {
		old.cancel()
	}
	s.srcRuns[ref.String()] = run
	s.srcMu.Unlock()

	go rf.Run(ctx)
	return nil
}

// stopSource ends a registration's reflector, if one is running.
func (s *Server) stopSource(ref entity.Ref) {
	s.srcMu.Lock()
	run, ok := s.srcRuns[ref.String()]
	delete(s.srcRuns, ref.String())
	s.srcMu.Unlock()
	if ok {
		run.cancel()
	}
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
		run.cancel()
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
// What that costs is stated in docs/working/sources-plan.md: the cursor cannot catch a source
// that has wedged, since a healthy quiet one looks the same. That is what the in-memory
// lastPass above is for, and why it is reported rather than written.
func (s *Server) recordPass(store entity.Store, ref entity.Ref, run *sourceRun, p source.Pass) {
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
// Scoped registrations are not restored here, for the reason scoped charters are not: a scope's
// engine is created on demand, and a scoped store reads baseline layered underneath, so
// restoring one means enumerating scopes and reading only the scope's own layer. See
// docs/working/sources-plan.md step 6.
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
