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
	verseio "github.com/signadot/verse/io"
)

// ── sources: what reflects an external system into the verse ─────────────────
//
// A source is installed at `verse.iod.<name>` carrying one way in, and it is the single
// writer of the tree under its `root`. The root is a question verse asks of every registration
// — who writes `git.*` — so it is on the record, and an overlap is visible in a listing. See
// docs/io.md.

// sourceFor builds the Source a registration names. The three ways in differ only in where the
// net.Conn comes from; Reflect is the same call down all three.
//
// It is here rather than in package io because `dir` needs a *verseio.Git built in this
// task, which is a choice about how verse hosts a source rather than part of the contract.
func (s *Server) sourceFor(e verseio.Entity) (verseio.Source, error) {
	switch {
	case e.URL != "":
		return verseio.HTTP(e.Root, e.URL, nil), nil
	case e.Dir != "":
		if e.Root != verseio.GitRoot {
			return nil, fmt.Errorf("source %q: `dir` is the in-process git source, so its "+
				"root must be %q, not %q", e.Name, verseio.GitRoot, e.Root)
		}
		g := verseio.NewGit()
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
// same question `iod` answers over HTTP, put to the same value. That is what keeps `dir`
// first-class rather than a path with fewer checks than the others.
func (s *Server) describe(r *http.Request, e verseio.Entity) (verseio.Reach, verseio.Descriptor, error) {
	if e.Dir != "" {
		src, err := s.sourceFor(e)
		if err != nil {
			return verseio.NotSource, verseio.Descriptor{}, err
		}
		return verseio.IsSource, verseio.Describe(src), nil
	}
	return verseio.Probe(r.Context(), e.URL, nil)
}

// addIO installs one, after asking it what it is.
//
// It is addRunner with two checks added, and both are what an external source loses that an
// in-process one had by construction: that it reflects the root somebody named, and that nobody
// else already writes the slice it claims.
func (s *Server) addIO(w http.ResponseWriter, r *http.Request) {
	// Read as a DOCUMENT first, so a field the decoder would drop can be named. It matters more
	// here than for a runner: a dropped `owns` installs a source that claims nothing, and a
	// dropped `scope` reflects into baseline where everybody sees it.
	node, err := codec.ReadNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := verseio.CheckFields(node); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var e verseio.Entity
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
	out := map[string]any{"at": verseio.Ref(e.Name).String()}

	if r.URL.Query().Get("probe") != "false" {
		reach, d, why := s.describe(r, e)
		out["reach"] = reach.String()
		switch reach {
		case verseio.NotSource:
			http.Error(w, fmt.Sprintf("source %q: %s is not a source: %s — --no-probe installs "+
				"it anyway", e.Name, e.URL, why), http.StatusBadRequest)
			return
		case verseio.Unreachable:
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
			if d.Root != e.Root {
				http.Error(w, fmt.Sprintf("source %q: you named root %q, but it says it is "+
					"%q — one of the two is wrong, and installing would write %s.* from a "+
					"source that reflects %s.*", e.Name, e.Root, d.Root, e.Root, d.Root),
					http.StatusBadRequest)
				return
			}
			// An ownership claim the operator did not write is taken from what the source SAID,
			// which is how `verse iod add git:laptop <url>` gets one at all. It is a snapshot
			// exactly as the url is: what the source claimed when somebody installed it.
			if len(e.Owns) == 0 && len(d.Owns) > 0 {
				e.Owns = d.Owns
				out["owns"] = d.Owns
			}
		}
	}

	// AND NOW THE OUT HALF'S PLACES, against the claim the install settled on — which is the
	// probe's when the operator named none, so this cannot be asked before the probe.
	if err := e.Out.CheckClaimed(e.Name, e.Owns); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Single-writer is what makes a reflected slice need no CAS. In task it held because
	// there was one Git value; across deployments nothing holds it but this.
	if r.URL.Query().Get("allow-overlap") != "true" {
		if why := overlaps(store, e); why != "" {
			http.Error(w, why, http.StatusConflict)
			return
		}
	}

	// AND THE NODES ITS CLAIMS NAME ARE THE INSTALLER'S TO TAKE (place.go). Ownership is not
	// single-writership: two sources proven disjoint above may share a node, and what the node
	// decides is who answers for what is reflected beneath it — so a node somebody else owns is
	// refused here, and two sources over one node share an installer.
	claims, err := verseio.ParseOwns(e.Owns)
	if err != nil {
		http.Error(w, fmt.Sprintf("source %q: %v", e.Name, err), http.StatusBadRequest)
		return
	}
	nodes := verseio.Nodes(claims)
	installer := entity.PrincipalOf(store)
	if installer == "" { // the door refuses this already (Server.principals); a source acts as its installer
		http.Error(w, "a source names who installs it: it reflects as its installer", http.StatusBadRequest)
		return
	}
	for _, node := range nodes {
		if code, err := checkPlace(store, installer, node, "installing a source that owns it"); err != nil {
			http.Error(w, err.Error(), code)
			return
		}
	}

	body := entity.Obj{"name": e.Name, "root": e.Root}
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
	// The out half rides the record, because everything about it is read back later: a removal
	// takes its grants back from here, a listing says what it last did, and a reader asking what
	// this io is has to be told that it acts at all.
	if e.Out != nil {
		out := entity.Obj{"places": e.Out.Places, "as": e.Out.As}
		if len(e.Out.Requesters) > 0 {
			out["requesters"] = e.Out.Requesters
		}
		body["out"] = out
	}
	ref := verseio.Ref(e.Name)
	before, err := entity.ActsAs(store, ref)
	if unreadable(w, r, err) {
		return
	}
	_, stood, err := store.Get(ref)
	if unreadable(w, r, err) {
		return
	}
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: body.Payload()}); refused(w, err) {
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A source reflects as whoever its registration's record says (actsAsInstaller).
	if err := actsAsInstaller(store, ref, installer, before); err != nil {
		s.takeBackOut(r, ref, stood)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// Taken before the reflector starts, so its first pass writes beneath places already owned.
	for _, node := range nodes {
		takePlace(store, installer, node, "source "+verseio.Ref(e.Name).String())
	}
	// And the out half's places, with the grants the registration describes: an install is one
	// act, and what an operator used to type as four `verse perms` lines is written here, from
	// the record, where it cannot be typed wrong or forgotten.
	if warn := seedOut(store, installer, e); warn != "" {
		out["warning"] = warn
	}
	// And the outboxes packages already declared for its places, which it now reads (outbox.go).
	if e.Out != nil {
		s.grantOutboxesTo(e.Out.Places, e.Out.As)
		// And a system charter refused at start for want of an io to send to, which this may be
		// (Server.installSystemCharters): a verse whose iods were registered after its start (a
		// wipe, then repopulate) gets it now rather than at the next restart.
		s.installWaitingSystemCharters()
	}
	// It reflects from here, with no restart. A registration that installed and then did
	// nothing until somebody bounced the server would be the same silence `dial-in` is refused
	// for.
	if err := s.startSource(e); err != nil {
		out["warning"] = "installed, but not running: " + err.Error()
	}
	writeOut(w, r, http.StatusCreated, out)
}

// passIO records what an out half just did: when its last pass finished, and the failure that
// stopped one.
//
// UNATTRIBUTED, like the hint beside it, and for the same reason: what it carries is a fact about
// the io's own half of the world, not a write anybody could use to say something about the tree. A
// forged report costs a misleading timestamp on one registration, which is the same order of harm
// as a forged hint — a reconcile of a slice the sender does not get to choose the contents of.
//
// It writes onto the registration rather than into a log, because what a reader wants is one
// listing saying how both halves are doing: `verse iod` already shows `lastReflect`, and an io
// whose out half has been refused for an hour should not read as healthy there.
func (s *Server) passIO(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref := verseio.Ref(name)
	store := s.scopedStore(r)
	_, ok, err := verseio.FindErr(entity.As(store, entity.SystemDefault), entity.JoinSegs([]string{name}))
	if !installed(w, r, fmt.Sprintf("io %q", name), ok, err) {
		return
	}
	var body verseio.OutPass
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := s.now().UTC().Format(time.RFC3339)
	rec := entity.Obj{"delivered": body.Delivered, "failed": body.Failed}
	if body.Error == "" {
		rec["lastPass"] = now
		rec["lastError"] = entity.Removed()
	} else {
		// WHEN IT STARTED, kept across repeats, for the reason the reflection's failure keeps
		// it: an error that has been the same since breakfast reads as "now" otherwise, and the
		// distance between it and lastPass is how long this has been going on.
		when := now
		if e, ok := verseio.Find(entity.As(store, entity.SystemDefault), entity.JoinSegs([]string{name})); ok &&
			e.Out != nil && e.Out.LastError != nil && e.Out.LastError.Error == body.Error &&
			e.Out.LastError.When != nil {
			when = *e.Out.LastError.When
		}
		rec["lastError"] = entity.Obj{"error": body.Error, "when": when}
	}
	// Written as VERSE, not as whoever posted: what a report says is verse's own bookkeeping
	// about an io, the same as the `lastReflect` it writes for the other half, and an io holds
	// nothing on its own registration — that record is the installer's.
	if _, _, err := entity.As(store, entity.SystemDefault).Write(entity.Write{
		At: ref.At("out"), Value: rec.Payload(),
		When: []entity.Require{stillInstalled(ref)},
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// seedOut stands up the out half's places and writes the grants the registration describes. It
// answers what to warn about, since a place that could not be granted is an io that installs and
// then cannot be asked for anything — worth saying, and not worth refusing an install over, since
// the in half works regardless and the grants can be written by hand.
//
// FOUR LINES, and each is the answer to a question somebody would otherwise get wrong:
//
//	read-write   requesters     --below   they may ask for things, and may not write the answers
//	read         as             --below   the io reads what is asked
//	child-owner  as                       and OWNS each status it creates, so nobody can forge one
//	child-grants {read: ["*"]}            which everyone may read
//
// The requesters' line is `--below` rather than a child grant on purpose: a child grant is copied
// onto every record created beneath, including the statuses, and would hand the askers write on
// the answers. The distinction is invisible until somebody edits a status, so it is written once,
// here, rather than explained in a runbook.
func seedOut(store entity.Store, installer string, e verseio.Entity) string {
	if e.Out == nil {
		return ""
	}
	for _, at := range e.Out.Places {
		ref, err := entity.ParseAddr(at)
		if err != nil {
			return fmt.Sprintf("out place %s: %v", at, err) // Check parsed it; a store is not it
		}
		// The place itself first, owned by the installer, since a node with no owner record is
		// open inside and the grants below are written onto that record.
		takePlace(store, installer, ref, "io "+verseio.Ref(e.Name).String()+" out")
		to := map[entity.Level][]string{
			entity.Read: {e.Out.As},
			// `child-owner`, which is how ownership is held out and therefore always about what
			// is BENEATH the node (entity.ChildOwnerKey): the io owns each status it creates, so
			// an owner record stands on it and the askers' grant from above stops there.
			entity.OwnerLevel: {e.Out.As},
		}
		if len(e.Out.Requesters) > 0 {
			to[entity.ReadWrite] = e.Out.Requesters
		}
		grants := []entity.Grant{{At: entity.SegsOf(ref), Below: true, To: to}}
		if err := entity.SetGrants(store, ref, grants); err != nil {
			return fmt.Sprintf("out place %s: its grants were not written (%v) — the io cannot "+
				"be asked for anything until they are", at, err)
		}
		if err := entity.SetChildGrants(store, ref, entity.Levels{entity.Read: {entity.Everyone}}); err != nil {
			return fmt.Sprintf("out place %s: what it holds out was not written (%v) — its "+
				"statuses will be readable only by the io itself", at, err)
		}
	}
	return ""
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
func overlaps(r entity.Reader, e verseio.Entity) string {
	for _, other := range verseio.List(r) {
		if other.Name == e.Name {
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
		// check. verseio.Disjoint answers true for an empty list, and is right to: nothing
		// intersects nothing. But a registration that carries no claim has not said it writes
		// nothing — it has not said anything, so there is nothing to prove with, and a source
		// that turns out to write the same slice would be discovered by its neighbour's refs
		// disappearing. Set arithmetic stays in verseio.Disjoint; this is the install's policy.
		if len(e.Owns) == 0 || len(other.Owns) == 0 {
			return refusal(e, other)
		}
		ok, err := verseio.Disjoint(e.Owns, other.Owns)
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
//
// It also names the way out that is not a flag: two sources that write different PLACES in the
// same thing are provably disjoint if they say WHERE, one level further down, and converge then
// keeps them apart for real. `--allow-overlap` is what is left for two sources that genuinely
// write the same place, where the safety comes from nothing at all.
func refusal(e, other verseio.Entity) string {
	return fmt.Sprintf("refused: %s and the installed %s cannot be proved to write different "+
		"slices, and two sources on one slice each tombstone what the other reflected.\n"+
		"    this one owns: %s\n    %s owns: %s\n"+
		"if they write different PLACES in the same things, claim the deeper address — "+
		"`linear.project.*.owners.ENG` and `…owners.OPS` are two places, and two places are "+
		"proved apart\n"+
		"--allow-overlap installs it anyway, with the cost above",
		verseio.Ref(e.Name), verseio.Ref(other.Name),
		claims(e.Owns), verseio.Ref(other.Name), claims(other.Owns))
}

// claims renders an ownership list for a message, saying so when there is none — a source that
// claimed nothing is the commonest reason this refusal fires, and "owns: []" would not say why.
func claims(owns []verseio.Own) string {
	if len(owns) == 0 {
		return "nothing (it claimed no ownership, so it can be proved apart from nothing)"
	}
	out := make([]string, 0, len(owns))
	for _, o := range owns {
		out = append(out, o.String())
	}
	return strings.Join(out, ", ")
}

func (s *Server) listIOs(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	probe := r.URL.Query().Get("probe") == "true"
	out := make([]map[string]any, 0)
	ents, readErr := verseio.ListErr(store)
	for _, e := range ents {
		rec := map[string]any{
			"at": verseio.Ref(e.Name).String(), "name": e.Name, "root": e.Root,
		}
		for k, v := range map[string]string{
			"url": e.URL, "dir": e.Dir, "scope": e.Scope, "lastReflect": e.LastReflect,
		} {
			if v != "" {
				rec[k] = v
			}
		}
		// The error and WHEN IT STARTED travel together or the reader is misled — see
		// verseio.Failure. Rendered as the stored pair rather than flattened to its text.
		if e.LastError != nil {
			rec["lastError"] = e.LastError
		}
		if len(e.Owns) > 0 {
			rec["owns"] = e.Owns
		}
		// BOTH HALVES IN ONE ROW, which is the whole reason an out half reports at all: an io
		// whose reflection is healthy and whose out half has been refused for an hour must not
		// read as healthy here. The in half's columns are verse's own knowledge; the out half's
		// are what it was told.
		if e.Out != nil {
			half := map[string]any{"places": e.Out.Places, "as": e.Out.As}
			for k, v := range map[string]string{"lastPass": e.Out.LastPass} {
				if v != "" {
					half[k] = v
				}
			}
			if e.Out.Delivered > 0 {
				half["delivered"] = e.Out.Delivered
			}
			if e.Out.Failed > 0 {
				half["failed"] = e.Out.Failed
			}
			if e.Out.LastError != nil {
				half["lastError"] = e.Out.LastError
			}
			rec["out"] = half
		}
		// When a pass last COMPLETED, from the live reflector — the signal that catches a source
		// that has wedged (alive, answering, reflecting nothing new), which the stored
		// lastReflect cursor cannot: on a quiet repo a healthy source and a wedged one carry the
		// same cursor. Read here and never written, because it is an opinion about now.
		s.srcMu.Lock()
		run := s.ioRuns[verseio.Ref(e.Name).String()]
		s.srcMu.Unlock()
		if run != nil {
			if pass := run.lastPassAt(); pass != "" {
				rec["lastPass"] = pass
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
	// A FAILED READ IS NOT AN EMPTY LIST. List answers nil when the read did not happen, so
	// without this a store that could not be read renders as "nothing is installed" — and
	// `verse iod` answered `[]` on a verse holding three sources, which is the one shape an
	// operator acts on. Same guard /state has carried all along, for the same reason. THIS
	// read's error, not the store's last one — see unreadable.
	if unreadable(w, r, readErr) {
		return
	}
	writeOut(w, r, http.StatusOK, out)
}

func (s *Server) getIO(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	e, ok, err := s.scopedStore(r).Get(verseio.Ref(name))
	if !installed(w, r, fmt.Sprintf("io %q", name), ok, err) {
		return
	}
	writeOut(w, r, http.StatusOK, entity.ToObj(e.Payload))
}

// checkIO asks one what it is, right now — including what it claims to own, which is what
// an operator reads before deciding whether `--allow-overlap` is honest.
func (s *Server) checkIO(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	e, ok, err := verseio.FindErr(s.scopedStore(r), entity.JoinSegs([]string{name}))
	if !installed(w, r, fmt.Sprintf("io %q", name), ok, err) {
		return
	}
	reach, d, why := s.describe(r, e)
	out := map[string]any{
		"at": verseio.Ref(name).String(), "reach": reach.String(),
	}
	if e.URL != "" {
		out["url"] = e.URL
	}
	if why != nil {
		out["why"] = why.Error()
	}
	// What it says NOW, beside what was recorded at install. Neither is merged into the other:
	// a claim that has drifted is a fact an operator should see as two values, not as one.
	if reach == verseio.IsSource {
		out["says"] = map[string]any{"root": d.Root, "owns": d.Owns}
	}
	writeOut(w, r, http.StatusOK, out)
}

// removeIO takes the registration away, and answers the question a delete cannot: what
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
func (s *Server) removeIO(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	name := r.PathValue("name")
	ref := verseio.Ref(name)
	e, ok, err := verseio.FindErr(store, entity.JoinSegs([]string{name}))
	if !installed(w, r, fmt.Sprintf("io %q", name), ok, err) {
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
			"refused: %s has reflected into %d place(s), and removing it means one of two things "+
				"this cannot tell apart:\n"+
				"    --forget   the reflection is over; tombstone what it wrote\n"+
				"    --keep     the same truth is moving elsewhere; leave the last known state, "+
				"now frozen and quietly ageing", ref, len(reflected)), http.StatusConflict)
		return
	}
	// ASKED BEFORE ANYTHING STOPS. Removing the registration is its owner's act, and the write
	// that removes it is the last thing here: checked only there, a stranger's removal is
	// refused with the reflector already stopped — the source installed, not running, and its
	// hints answered 503.
	if refused(w, entity.Check(store, entity.PrincipalOf(store), entity.OwnerLevel, ref, "removing it")) {
		return
	}
	// Stopped BEFORE anything is written, so nothing is left writing a slice no registration
	// claims — and so a pass in flight cannot re-reflect what --forget is about to tombstone.
	s.stopSource(ref)
	// The outboxes sending to it lose their grant, whichever way the removal goes (outbox.go).
	if e.Out != nil {
		s.releaseOutboxesTo(e.Out.Places, e.Out.As)
	}

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
	// The out half's grants go the way everything else this io left goes: with --forget, which
	// is the answer to "this io is over". --keep keeps them, since a place whose grants stand is
	// one a re-install lands back on, and taking them from somebody who said keep would be this
	// verb deciding more than it was asked.
	if forget {
		unseedOut(store, e)
	}
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: nil}); err != nil {
		if refused(w, err) {
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeOut(w, r, http.StatusOK, out)
}

// unseedOut removes the grants seedOut wrote, for a removal that says the io is over. A failure is
// said on stderr rather than refusing the removal: a registration that will not go because a grant
// would not is worse than a grant left standing, and the grant is visible to `verse perms` either
// way.
func unseedOut(store entity.Store, e verseio.Entity) {
	if e.Out == nil {
		return
	}
	for _, at := range e.Out.Places {
		ref, err := entity.ParseAddr(at)
		if err != nil {
			continue
		}
		if err := entity.SetGrants(store, ref, nil); err != nil {
			fmt.Fprintf(os.Stderr, "verse: io %s: its grants at %s still stand: %v\n", e.Name, at, err)
		}
		if err := entity.SetChildGrants(store, ref, nil); err != nil {
			fmt.Fprintf(os.Stderr, "verse: io %s: what %s holds out still stands: %v\n", e.Name, at, err)
		}
	}
}

// reflectedBy returns the PLACES standing in the store that this registration's ownership
// claims, in path order so a --forget deletes in the same order twice.
//
// Bounded by `owns` and by nothing else, which is what makes a --forget safe: it can only
// tombstone what the install PROVED this source was the single writer of. A registration that
// claimed nothing reflected nothing, as far as a removal is concerned — under-claiming there
// leaves entities standing, which is the direction to be wrong in.
//
// A LITERAL CLAIM IS ITS OWN PLACE. `git.verse` claims everything under that node, at whatever
// depths the source writes there, so the removal is the node — one place standing, however many
// refs and commits it holds. Only a claim with a glob in it is asked about entity by entity.
//
// The claims are parsed ONCE, and a claim that does not parse fails the whole answer rather
// than being skipped. This is the caller that deletes: a skipped claim there is either an
// entity deleted because verse could not read the claim that protected it, or one left standing
// because it could not read the claim that owned it, and neither is a thing to decide in
// silence. An install refuses an unparseable claim, so reaching this needs a store somebody
// wrote by hand — which is exactly when the loud answer is the one that helps.
func reflectedBy(r entity.Reader, e verseio.Entity) ([]entity.Path, error) {
	claims, err := verseio.ParseOwns(e.Owns)
	if err != nil {
		return nil, err
	}
	var out []entity.Path
	seen := map[string]bool{}
	add := func(p entity.Path) {
		if key := p.String(); !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	for _, c := range claims {
		if node, ok := c.Node(); ok && len(node.Segs()) == len(c.Segs) {
			segs := node.Segs()
			if len(r.ListAt(segs, len(segs), nil)) > 0 {
				add(node.At())
			}
			continue
		}
		// A CLAIM WITH A GLOB IS READ WHERE IT POINTS: its literal levels are the prefix, its
		// length is the depth, and what stands there is asked one ref at a time. So a claim over
		// a place inside a shared node — `linear.project.*.owners.ENG`, five levels down — is
		// answered by a read of `linear.project` and not by an enumeration of the world.
		//
		// `!Reserved()` says at the read what `verseio.Claimed` also refuses at the point: verse's
		// own half is not a slice a source can own, so a removal cannot tombstone it.
		var at []string
		if node, ok := c.Node(); ok {
			at = node.Segs()
		}
		for _, ent := range r.ListAt(at, len(c.Segs), func(ref entity.Ref) bool {
			return !ref.Reserved()
		}) {
			for _, p := range verseio.Claimed([]verseio.Claim{c}, ent.Ref) {
				add(p)
			}
		}
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
	rf     *verseio.Reflector
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

// lastPassAt is when this run's last pass completed, failed or not. The run's belief about
// the error is not reported beside it: `lastError` on the registration is that belief, kept
// in step with the store by recordPass, and a second copy of it that was as-of-now would only
// ever differ from the stored one in the moment between a pass ending and its write landing.
func (r *sourceRun) lastPassAt() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastPass
}

// startSource runs a reflector for a registration, replacing one already running under that
// address. It is called at install and at restart, so those two paths cannot drift.
func (s *Server) startSource(e verseio.Entity) error {
	src, err := s.sourceFor(e)
	if err != nil {
		return err
	}
	ref := verseio.Ref(e.Name)
	store := s.Store
	if e.Scope != "" {
		// A scoped source writes through the scope's overlay, so its reflections fire that
		// scope's triggers and nobody else's.
		store = s.Store.Scope(e.Scope)
	}
	// AND AS THE SOURCE. A source has no identity of its own: it is a node with an owner, and it
	// acts as that owner, so what it reflects is written via its record's address and owned by
	// whoever installed it. Written as the system, every reflected entity would read as the
	// verse's own, and the installer answerable for a slice would own nothing in it.
	store = entity.As(store, entity.Via(ref))
	rf := verseio.NewReflector(src, store, ResyncEvery)
	// SEEDED FROM THE REGISTRATION, not from empty. run.lastErr is the comparison recordPass
	// makes to decide whether anything changed, and it is a belief about what the STORE holds.
	// Starting it empty made that belief false on every restart: a registration carrying an
	// error met a healthy pass, both sides read "no error", nothing was written, and the stored
	// error became permanent. It survived a wipe-and-repopulate on staging that way, reported
	// for hours about a source that was fine.
	var lastErr string
	if e.LastError != nil {
		lastErr = e.LastError.Error
	}
	run := &sourceRun{rf: rf, scope: e.Scope, lastErr: lastErr}

	// The context is built BEFORE the pass sink, so the sink can be fenced by it: a status
	// write is a write like any other, and one landing after its scope was dropped re-creates
	// the registration as a stub (see stillInstalled). Ordering it here rather than storing the
	// context on the run keeps it a closure over a lifetime instead of a field to remember.
	ctx, cancel := context.WithCancel(s.ctx)
	run.cancel = cancel
	rf.OnPass(func(p verseio.Pass) { s.recordPass(ctx, store, ref, run, p) })
	s.srcMu.Lock()
	// A re-install replaces, and the OLD one has to be stopped first — two reflectors on one
	// slice is exactly what the disjointness check at install exists to prevent, so leaking one
	// here would be the same bug arriving through the back door.
	old, replacing := s.ioRuns[ref.String()]
	run.done = make(chan struct{})
	s.ioRuns[ref.String()] = run
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
	run, ok := s.ioRuns[ref.String()]
	delete(s.ioRuns, ref.String())
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
	for ref, run := range s.ioRuns {
		if run.scope == scope {
			doomed = append(doomed, run)
			delete(s.ioRuns, ref)
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

// stopAllIOs ends every reflector this server is running.
//
// In production the lifetime is Start's context and cancelling it is enough. This exists for
// the case that has no such context — a server assembled and never started, which is what a
// test builds — where a reflector would otherwise outlive the store it writes to and go on
// polling into a closed session.
func (s *Server) stopAllIOs() {
	s.srcMu.Lock()
	runs := s.ioRuns
	s.ioRuns = map[string]*sourceRun{}
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
//     would put a write on `io:*` once per resync interval forever, and anything watching
//     that slice would fire on a verse where nothing is happening.
//
// What that costs is stated in docs/io.md: the cursor cannot catch a source
// that has wedged, since a healthy quiet one looks the same. That is what the in-memory
// lastPass above is for, and why it is reported rather than written.
func (s *Server) recordPass(ctx context.Context, store entity.Store, ref entity.Ref, run *sourceRun, p verseio.Pass) {
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
	now := s.now().UTC().Format(time.RFC3339)
	var errText string
	if p.Err != nil {
		errText = p.Err.Error()
	}
	run.mu.Lock()
	changed := run.lastErr != errText
	run.lastPass = now
	run.mu.Unlock()

	if changed {
		// A nil value REMOVES the field, which is how a recovery is recorded: the registration
		// stops carrying an error rather than carrying an empty one.
		//
		// `changed` is what makes When mean what it says. It is true only on a TRANSITION — into
		// a failure, out of one, or from one failure to a different one — so the timestamp
		// written here is the instant this failure began and is never refreshed by the same
		// failure repeating. See verseio.Failure.
		w := entity.Write{At: ref.At("lastError"), When: []entity.Require{stillInstalled(ref)}}
		if errText != "" {
			node, err := codec.ToNode(verseio.Failure{Error: errText, When: &now})
			if err != nil { // a two-string struct cannot fail to encode; say so rather than write nothing
				fmt.Fprintf(os.Stderr, "verse: source %s: encoding error record: %v\n", ref, err)
				return
			}
			w.Value = entity.Raw(node)
		}
		// THE MEMORY FOLLOWS THE STORE, and only if the store took it. `changed` is a
		// comparison against what this run believes is RECORDED, so advancing it before the
		// write means a write that fails is never retried: the next pass compares equal, finds
		// nothing changed, and writes nothing — leaving the registration carrying an error it
		// has recovered from, for good.
		//
		// That is not hypothetical. Staging recorded an error, recovered on the next pass, and
		// the clearing write timed out (`recording error: … context deadline exceeded`, which
		// is this very line). Two passes later `verse iod` still reported the source in
		// error while the entity it had failed on was present and correct — the one field an
		// operator reads to decide whether a source is healthy, stuck on a transient.
		// A REFUSED WRITE IS NOT A WRITE. Store.Write answers (committed, rev, err) and a
		// precondition that does not hold is `false, 0, nil` — not an error. Taking the else
		// branch on it would advance the memory to a value the store never took, and every
		// later pass would compare equal and write nothing: the same permanence the err case
		// above is guarded against, reached by the door nobody was watching.
		if ok, _, err := store.Write(w); err != nil || !ok {
			// Leave lastErr alone — the store still says what it said, so the next pass sees a
			// change again and retries. Everything below still runs: a failed status write is
			// no reason to skip the rest of the report.
			if err != nil {
				fmt.Fprintf(os.Stderr, "verse: source %s: recording error: %v\n", ref, err)
			}
		} else {
			run.mu.Lock()
			run.lastErr = errText
			run.mu.Unlock()
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
// It asks about `name` rather than about the entity, and that costs nothing either way now: a
// removal takes the key with it, so a presence test on the ref would fail on a removed
// registration too. It used to be load-bearing, on the belief that a removal left a null
// tombstone standing at the ref — which entity.Present(), a null pattern matching any value,
// would have been satisfied by. Naming a field inside the entity stayed correct through the
// correction, so it stays.
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
// created on demand by whatever owns it — a session's client, a sandbox for its own
// lifetime — so the thing that made the scope is the thing that can install into it again.
//
// A failed read is not "no sources installed", and taking it as one brought a verse back
// reflecting NOTHING with no sign of it but `running: false` on each registration. Reported as
// the read it is (entity.IsUnread), so the phase asks again until the listing happens and the
// sources start — a restart is not what it should take.
func (s *Server) restoreSources() error {
	if err := s.awaiting(phaseStrata, phaseCharter, phasePackages); err != nil {
		return err
	}
	installed := verseio.List(s.Store)
	if err := entity.ReadFailed(s.Store); err != nil {
		return fmt.Errorf("sources could not be read, so none were restored — the "+
			"registrations are still there: %w", err)
	}
	for _, e := range installed {
		if e.Scope != "" {
			continue
		}
		if err := s.startSource(e); err != nil {
			// Reported and skipped, so one bad registration does not cost the rest — the same
			// rule LoadCharter follows for a rule that will not restore.
			fmt.Fprintf(os.Stderr, "verse: source %s: %v\n", verseio.Ref(e.Name), err)
		}
	}
	return nil
}

// hintIO takes a low-latency hint: something changed over there, look now.
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
func (s *Server) hintIO(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref := verseio.Ref(name)
	// Looked up as the system, in the request's scope. A hint names nobody — it is a hook's
	// nudge, and what is reflected is reflected as the source — so looked up as its sender, a
	// registration in a verse that holds out no read was not found by the hook it installed.
	_, ok, err := verseio.FindErr(entity.As(s.scopedStore(r), entity.SystemDefault), entity.JoinSegs([]string{name}))
	if !installed(w, r, fmt.Sprintf("io %q", name), ok, err) {
		return
	}
	var refs []entity.Ref
	for _, raw := range r.URL.Query()["at"] {
		parsed, err := entity.ParseAddr(raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		refs = append(refs, parsed)
	}
	s.srcMu.Lock()
	run := s.ioRuns[ref.String()]
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
