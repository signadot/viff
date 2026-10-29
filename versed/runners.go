package versed

import (
	"fmt"
	"net/http"
	"os"
	"sort"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/task"
)

// ── runners: where work runs ─────────────────────────────────────────────────
//
// A runner is installed at `verse.runners.<name>` carrying a url, and a spawn names one. It
// is WHERE the work runs, and nothing about who does it: the doer is the spawn's.

// addRunner installs one, after asking the url what it is.
//
// A VERB rather than a raw write, for the reason `verse resolver add` is one: a write has
// nowhere to put the checks, and here they are real from the first commit.
func (s *Server) addRunner(w http.ResponseWriter, r *http.Request) {
	// Read as a DOCUMENT first, so a field the decoder would drop can be named. `ulr:` decodes
	// to nothing and would install a runner with no address, reporting success — and the url
	// is the whole of what this entity carries, so that silence is total.
	node, err := codec.ReadNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := checkRunnerFields(node); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var e api.Runner
	if err := codec.FromNode(node, &e); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := e.Check(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var reach, why, warning string
	// The probe, and the two dispositions it feeds. They differ on whether waiting could
	// change the answer: a url that answers as something else is answering NOW, while a url
	// nothing answers is the ordinary
	// state of a runner deployed alongside the entity that names it, where the manifest lands,
	// the pod is pending, and the entity is already written. Refusing that would make
	// deployment order-dependent for a fact that fixes itself.
	body := entity.Obj{"name": e.Name, "url": e.URL}
	var described *runner.Description
	if e.Identity != "" {
		body["identity"] = e.Identity
	}
	// The subject names a token this verse has to verify, so it is refused where nothing
	// could: a registration nobody can ever prove is a runner that takes no spawn and says why
	// too late. And it is refused in a named scope: a principal is one thing across the verse,
	// and the door reads the baseline registrations, so a subject written anywhere else would
	// neither admit the runner's token nor refuse its bare name.
	if !runner.IsStaticSubject(e.Subject) && s.workloadIssuer() == nil {
		http.Error(w, fmt.Sprintf("runner %q: a subject is a workload token's, and this verse names "+
			"no issuer of workload tokens (auth.iod.issuer in its config), so nothing could verify %s",
			e.Name, e.Subject), http.StatusBadRequest)
		return
	}
	// And the handshake runs both ways: a runner held to its subject holds this verse to
	// its signature, so a verse that issues nothing could never be let in by it.
	if s.issuer == nil {
		http.Error(w, fmt.Sprintf("runner %q: a runner registered with a subject takes this verse's calls "+
			"by the tokens it signs, and this verse issues none (auth.issuer in its config), so it could "+
			"never prove itself to %s", e.Name, e.Subject), http.StatusBadRequest)
		return
	}
	if sc := r.URL.Query().Get("scope"); sc != "" {
		http.Error(w, fmt.Sprintf("runner %q: a subject makes the runner a principal, which is one thing "+
			"across the verse and not a scope's: register it with its subject in the baseline, not in %q",
			e.Name, sc), http.StatusBadRequest)
		return
	}
	// One subject, one principal: the door takes a token for the first name that claims
	// its subject, so a subject two registrations share, or one an iod account holds,
	// would open a door under a name nobody meant.
	if s.iodAccountNamed(e.Subject) {
		http.Error(w, fmt.Sprintf("runner %q: subject %s is an iod's (auth.iod.accounts or auth.iod.static), and a token proves one principal",
			e.Name, e.Subject), http.StatusBadRequest)
		return
	}
	// The registrations' own error refuses too: a check that could not look is not a check
	// that passed.
	other, ok, err := s.registrationWhere(func(o api.Runner) bool { return o.Subject == e.Subject })
	if err != nil {
		http.Error(w, fmt.Sprintf("runner %q: %v", e.Name, err), http.StatusServiceUnavailable)
		return
	}
	if ok && other.Name != e.Name {
		http.Error(w, fmt.Sprintf("runner %q: subject %s is already runner %q's, and a token proves one principal",
			e.Name, e.Subject, other.Name), http.StatusBadRequest)
		return
	}
	// ... and one verified principal is one registration: two runners proving two subjects
	// under one identity would write a record that cannot say which of them acted, and a
	// hello could not find its registration.
	other, ok, err = s.verifiedRunner(e.Principal())
	if err != nil {
		http.Error(w, fmt.Sprintf("runner %q: %v", e.Name, err), http.StatusServiceUnavailable)
		return
	}
	if ok && other.Name != e.Name {
		http.Error(w, fmt.Sprintf("runner %q: %s is already runner %q's, which proves it with the subject %s; "+
			"a verified principal is one registration", e.Name, e.Principal(), other.Name, other.Subject), http.StatusBadRequest)
		return
	}
	body["subject"] = e.Subject
	probe, ok := probeParam(w, r, true)
	if !ok {
		return
	}
	// May they register it at all? Asked BEFORE the probe, because the probe presents this
	// verse's token to the url the body names: a caller the write would refuse must not have
	// had the token sent to an address of their choosing first.
	store := s.scopedStore(r)
	if who := entity.PrincipalOf(store); who != "" && !entity.IsSystem(who) {
		ref := runnerEntityRef(e.Name)
		_, stands, err := store.Get(ref)
		if err != nil {
			http.Error(w, notRead(fmt.Sprintf("runner %q", e.Name), err), http.StatusServiceUnavailable)
			return
		}
		if stands {
			// The level the write itself asks of a write into a node that stands (attributed.Write),
			// and no other: this check is the write's, asked early.
			err = entity.Check(store, who, entity.ReadWrite, ref, "registering a runner again")
		} else if may, mayErr := entity.MayCreate(store, who, ref); mayErr != nil {
			err = mayErr
		} else if !may {
			err = fmt.Errorf("%w: %s may not register a runner at %s: nothing above it holds `owner` out to them", entity.ErrForbidden, who, ref)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}
	if probe {
		found, desc, whyErr := s.probeRegistered(e)
		reach = found.String()
		switch found {
		case client.NotRunner:
			http.Error(w, fmt.Sprintf("runner %q: %s is not a runner: %s — --no-probe installs it anyway",
				e.Name, e.URL, whyErr), http.StatusBadRequest)
			return
		case client.Unverified:
			// Answered as a runner, and not as the one registered: waiting changes nothing, and
			// a registration nobody could prove would take no spawn and say so too late.
			http.Error(w, fmt.Sprintf("runner %q: %s answered as a runner and did not prove it is %s: %s. "+
				"Deploy it with a `verse:` block and its token, or register the subject it holds",
				e.Name, e.URL, e.Subject, whyErr), http.StatusBadRequest)
			return
		case client.Unreachable:
			// Installed, and said so: why it was not reached, as a probed listing says it, and
			// what to do about it. The warning rides on the response rather than being logged,
			// because the operator running the command is the one who can act on it.
			why = whyErr.Error()
			warning = fmt.Sprintf("installed, but nothing answers at %s yet; `verse runner check %s` "+
				"asks again, and a spawn naming it asks before it runs", e.URL, e.Name)
		default:
			// What it offers is kept with the registration, so a spawn naming this runner is
			// checked against it where the spawn is written; a description that says too little
			// to check a spawn by is no registration.
			if err := desc.Check(); err != nil {
				http.Error(w, fmt.Sprintf("runner %q: %s does not say what it offers and the networks it builds: %v", e.Name, e.URL, err),
					http.StatusBadRequest)
				return
			}
			described = &desc
		}
	}
	if described == nil {
		// Nothing was described now, so nothing an earlier add described stands: a description is
		// the describe's to write, and one left behind a subject this add changed would send spawns
		// to a runner nobody has checked is that subject. `verified` and `unverified` are what an
		// older verse wrote.
		for _, k := range []string{"described", "offers", "verified", "unverified"} {
			body[k] = entity.Removed()
		}
	}
	if _, _, err := store.Write(entity.Write{
		At: runnerEntityRef(e.Name).At(), Value: body.Payload(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// What it offers replaces what a registration of the name kept, never merges into it.
	s.forgetRegistrations()
	if described != nil {
		if err := writeDescribed(store, e.Name, *described); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	// The row as the listing would show it, with what the runner described and what the
	// install's own probe found: a name installed again keeps the work recorded on it, and the
	// counts say so.
	e.Described = described
	out := runnerRow(taskRows(store), e, nil)
	out.Reach, out.Why, out.Warning = reach, why, warning
	writeOut(w, r, http.StatusCreated, out)
}

func (s *Server) listRunners(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	probe, ok := probeParam(w, r, false)
	if !ok {
		return
	}
	out := make([]api.RunnerRow, 0)
	ents, bad, readErr := readRunners(store)
	// READ ONCE, COUNT PER RUNNER. The counts are a walk of every task instance, and asking
	// inside the loop made the listing cost runners x instances for an answer that is one pass
	// over the same rows.
	instances := taskRows(store)
	var reach func(api.Runner) (client.Reach, error)
	if probe {
		reach = func(e api.Runner) (client.Reach, error) {
			r, _, why := s.probeRegistered(e)
			return r, why
		}
	}
	for _, e := range ents {
		row := runnerRow(instances, e, reach)
		row.Described = nil // GET /runners/{name} says what it offers; a listing row names it
		out = append(out, row)
	}
	for _, b := range bad {
		out = append(out, api.RunnerRow{Runner: api.Runner{Name: b.Ref.Name()}, At: b.Ref.String(), Offers: []string{}, Unreadable: b.Err.Error()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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

// runnerRow is a runner as the runner routes answer it: as installed, and what verse has recorded
// of the work on it, counted from instances. probe, where given, asks the runner what it is, as
// `?probe=true` does.
func runnerRow(instances []entity.Entity, e api.Runner, probe func(api.Runner) (client.Reach, error)) api.RunnerRow {
	ref := runnerEntityRef(e.Name)
	rec := api.RunnerRow{Runner: e, At: ref.String(), Offers: e.OfferNames()}
	rec.Running, rec.Total, rec.LastSpawn = runnerCounts(instances, ref, e.URL)
	// Asked for, never stored. A reachability column is as of now or it is a cache that can be
	// stale in the direction that costs — saying a runner is well because it was well when
	// something last looked.
	if probe != nil {
		reach, why := probe(e)
		rec.Reach = reach.String()
		if why != nil {
			rec.Why = why.Error()
		}
	}
	return rec
}

func (s *Server) getRunner(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ent, ok, err := s.scopedStore(r).Get(runnerEntityRef(name))
	if !installed(w, r, fmt.Sprintf("runner %q", name), ok, err) {
		return
	}
	e, err := decodeRunner(ent.Payload)
	if err != nil {
		http.Error(w, fmt.Sprintf("runner %s is stored in a shape this verse can't read: %v", ent.Ref, err), http.StatusInternalServerError)
		return
	}
	writeOut(w, r, http.StatusOK, e)
}

// checkRunner probes one on demand. It is the command an operator runs before choosing between
// --cancel and --leave, because the case removal has to be honest about is the runner that is
// already gone — and this says so beforehand rather than in the report afterwards.
func (s *Server) checkRunner(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	e, ok, err := findRunnerErr(s.scopedStore(r), entity.JoinSegs([]string{name}))
	if why := unreadableRunner(s.scopedStore(r), name); !ok && err == nil && why != nil {
		// A check reads the runner, not the registration: one this verse can't read is added
		// again, and a check saying "not installed" would send the operator to add a second.
		codec.HTTPError(w, r, http.StatusConflict, why.Error())
		return
	}
	if !installed(w, r, fmt.Sprintf("runner %q", name), ok, err) {
		return
	}
	// A runner that answers describes itself afresh: what it offers may have changed since it
	// was installed (a restart with a model credential, say), and the registration says what
	// the runner says now. Written as verse, not as whoever asked: the offers are the runner's
	// words, recorded the way a task's status is, and anybody who may read the runner may ask.
	// One that answered and did not prove it is the runner registered has that recorded in
	// place of its words, and takes no spawn until a check finds it proving itself.
	out, code, err := s.describeAgain(s.storeForScope(r.URL.Query().Get("scope")), e, 1)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	writeOut(w, r, http.StatusOK, out)
}

// writeDescribed keeps a runner's description on its registration, under `described`, in place
// of what stood there: a write merges, a list element by element (so ReplaceAt's removals, which
// reach a value's fields and not its list's elements, would leave an old offer's fields on a new
// one), so what it said before is removed first, and `offers`, which an older verse kept the
// offers under, with it, and `verified` and `unverified`, which an older verse wrote beside it.
//
// BETWEEN THE TWO WRITES the registration has no description, and that is safe: routing passes over
// a registration that is not Current, so a spawn arriving then goes elsewhere or is refused
// naming it, and is never checked against nothing.
func writeDescribed(w entity.Store, name string, desc runner.Description) error {
	at := runnerEntityRef(name).At()
	if _, _, err := w.Write(entity.Write{At: at, Value: entity.Obj{
		"described": entity.Removed(), "offers": entity.Removed(), "verified": entity.Removed(), "unverified": entity.Removed(),
	}.Payload()}); err != nil {
		return err
	}
	_, _, err := w.Write(entity.Write{At: at, Value: entity.Obj{"described": desc}.Payload()})
	return err
}

// recordOffers keeps what a runner said on its registration, as `verse runner check` does, and
// as verse: the offers are the runner's words and the proof is what versed found, recorded the
// way a task's status is, whoever's spawn or check asked. A spawner need not own the
// registration, and on a deployment that holds registrations to its admin none but the admin
// does.
func (s *Server) recordOffers(e api.Runner, desc runner.Description) {
	if err := writeDescribed(s.Store, e.Name, desc); err != nil {
		fmt.Fprintf(os.Stderr, "verse: runner %s: keeping what it offers: %v\n", runnerEntityRef(e.Name), err)
	}
}

// recordUnverified is recordOffers for a runner that did not prove it is its subject: what it
// described before is removed, and why is said to whoever asked, not kept.
func (s *Server) recordUnverified(name string) {
	if err := removeDescribed(s.Store, name); err != nil {
		fmt.Fprintf(os.Stderr, "verse: runner %s: removing what it described: %v\n", runnerEntityRef(name), err)
	}
}

// probeRunners asks each installed runner with no offers on record what it offers, when verse
// starts: a registration made with --no-probe, or against a pod that was not up yet, is what a
// repopulate leaves, and until the runner has described itself routing passes it over (route.go).
// One that answers has its offers kept; one that does not is named here, and is described by its
// hello or a check.
func (s *Server) probeRunners() error {
	runners, err := installedRunnersErr(s.Store)
	if err != nil {
		return err
	}
	for _, e := range runners {
		if e.Current() {
			continue
		}
		reach, desc, why := s.probeRegistered(e)
		switch reach {
		case client.Unverified:
			fmt.Fprintf(os.Stderr, "verse: runner %s has not proved it is %s (%v); a spawn naming it is refused until it does\n",
				runnerEntityRef(e.Name), e.Subject, why)
			s.recordUnverified(e.Name)
			continue
		case client.IsRunner:
		default:
			fmt.Fprintf(os.Stderr, "verse: runner %s has not described itself (%s: %v); a spawn naming it is refused until it does\n",
				runnerEntityRef(e.Name), reach, why)
			continue
		}
		if err := desc.Check(); err != nil {
			fmt.Fprintf(os.Stderr, "verse: runner %s does not say what it offers and the networks it builds: %v\n", runnerEntityRef(e.Name), err)
			continue
		}
		s.recordOffers(e, desc)
	}
	return nil
}

// removeRunner takes the entity away, and answers the question a delete cannot: what happens to
// the work still on it.
//
// verse cannot MOVE a running task between runners. It can cancel one, or leave it — and the
// difference between those is an intent verse cannot read from a delete. Retiring the name (a
// rename, a retarget) means the work is fine and should be left alone; tearing the place down
// means the work is doomed and the only question is whether it ends cleanly. Neither default
// survives contact with the motivating use, so removal with work on it is REFUSED until told
// which. The precedent is in the tree: a spawn carrying `provider:` with no `grant:` is refused
// at install rather than defaulted, because the author is present and a guess has consequences.
//
// The removal ITSELF is never refused — `--leave` always succeeds — because this builds no
// drain, and a refusal with no way to satisfy it would be a trap.
func (s *Server) removeRunner(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	name := r.PathValue("name")
	ref := runnerEntityRef(name)
	e, ok, err := findRunnerErr(store, entity.JoinSegs([]string{name}))
	if !ok && err == nil {
		// A registration this verse can't read is still removed by its name: its url is not
		// known, so its work is found by the name alone, and a cancel reaches none of it.
		_, ok, err = store.Get(ref)
		e = api.Runner{Name: name}
	}
	if !installed(w, r, fmt.Sprintf("runner %q", name), ok, err) {
		return
	}
	q := r.URL.Query()
	why, cancelling := q.Get("cancel"), q.Has("cancel")
	leaving := q.Get("leave") == "true"
	live := liveOn(store, ref, e.URL)

	if len(live) > 0 && !cancelling && !leaving {
		http.Error(w, fmt.Sprintf(
			"refused: %d task(es) are running on %s, and removing it means one of two "+
				"things this cannot tell apart:\n"+
				"    --cancel \"<why>\"   the place is going away; end them with a reason "+
				"their charters see\n"+
				"    --leave            the place stays; they keep running on the url already "+
				"stamped on them", len(live), ref), http.StatusConflict)
		return
	}
	// Asked before anything is cancelled, as a source's removal is asked before it stops: the
	// delete below is the owner's act and is the LAST write here.
	if refused(w, entity.Check(store, entity.PrincipalOf(store), entity.OwnerLevel, ref, "removing it")) {
		return
	}
	out := api.Removed{Removed: ref.String()}
	if cancelling && len(live) > 0 {
		var unreachable []string
		out.Cancelled, unreachable = s.cancelOn(e.URL, live, why)
		// What it could not reach, reported rather than counted as ended. Saying "cancelled 3"
		// when three are unreachable is the same lie as recording a queued answer as
		// delivered — and an already-gone runner is WHY an operator removes the entity half
		// the time, so this is the ordinary case rather than the exotic one. Those are
		// orphans, and the orphan path is what covers them.
		out.Unreachable = unreachable
	}
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: nil}); err != nil {
		if refused(w, err) {
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.forgetRegistrations()
	writeOut(w, r, http.StatusOK, out)
}

// liveOn returns the ids of tasks still running on a runner.
//
// Matched on the task:instance mirror, by the NAME a spawn used or by the url stamped on it.
// Both, because the two answer different halves: the name finds work this verse placed there,
// and the url finds work placed there before a rename — and a task carries the url precisely
// so that removing the entity cannot strand it.
func liveOn(r entity.Reader, ref entity.Ref, url string) []string {
	var out []string
	for _, ent := range entity.Below(r, func(e entity.Ref) bool {
		return mirror.IsTask(e)
	}, 1, entity.VerseRoot, mirror.System, mirror.InstanceKind) {
		rec := entity.ToObj(ent.Payload)
		if rec == nil {
			continue
		}
		if st, _ := rec["status"].(string); task.TerminalStatus(st) {
			continue
		}
		named, _ := rec["runnerName"].(string)
		on, _ := rec["runner"].(string)
		if named == ref.String() || (url != "" && on == url) {
			out = append(out, ent.Ref.Name())
		}
	}
	sort.Strings(out)
	return out
}

// cancelOn ends each task on a runner, and separates the ones it ended from the ones it
// could not reach. A cancel that never arrived must not be counted as one that did.
func (s *Server) cancelOn(url string, ids []string, why string) (cancelled, unreachable []string) {
	p := s.dialAt(url)
	for _, id := range ids {
		h := p.Attach(id)
		if h == nil {
			unreachable = append(unreachable, id)
			continue
		}
		if err := h.Cancel(why, versedActor); err != nil {
			unreachable = append(unreachable, id)
			continue
		}
		cancelled = append(cancelled, id)
	}
	return cancelled, unreachable
}

// runnerCounts is what a runner is carrying, derived from the store and never probed: verse
// reports what it has recorded. `running` is now, `total` is ever, and `lastSpawn` is when it
// was last used — so a runner nobody names is visible as such.
func runnerCounts(instances []entity.Entity, ref entity.Ref, url string) (running, total int, last string) {
	for _, ent := range instances {
		rec := entity.ToObj(ent.Payload)
		if rec == nil {
			continue
		}
		named, _ := rec["runnerName"].(string)
		on, _ := rec["runner"].(string)
		if named != ref.String() && (url == "" || on != url) {
			continue
		}
		total++
		if st, _ := rec["status"].(string); !task.TerminalStatus(st) {
			running++
		}
		if at, _ := rec["startedAt"].(string); at > last {
			last = at
		}
	}
	return running, total, last
}

// runnerIdentity is the principal a named runner acts as — the identity on its entity, or its
// name — and "" for the runtimes this process holds, which act as nobody of their own: a local
// task's mirror is the system's.
func runnerIdentity(store entity.Reader, name string) string {
	if name == "" {
		return ""
	}
	if e, ok := findRunner(store, name); ok {
		return e.Principal()
	}
	return ""
}

// handshake asks a registered runner what it is now, right before a launch to it,
// and holds it to its registration. A runner proves itself at the describe and nowhere else, and
// a launch hands whatever answers at the url the prompt, the repositories and the policy, so the
// describe is asked again at that moment: a pod behind the url that cannot produce the
// registered subject's token receives no launch, and the registration records what it found,
// as `verse runner check` would. One GET per launch. What it found is versed's own observation,
// recorded as verse (recordOffers), not as the spawner.
func (s *Server) handshake(e api.Runner) (runner.Description, error) {
	where := runnerEntityRef(e.Name).String()
	reach, desc, why := s.probeRegistered(e)
	switch reach {
	case client.IsRunner:
		if err := desc.Check(); err != nil {
			return runner.Description{}, fmt.Errorf("%s does not say what it offers and the networks it builds: %v", where, err)
		}
		s.recordOffers(e, desc)
		return desc, nil
	case client.Unverified:
		s.recordUnverified(e.Name)
		return runner.Description{}, fmt.Errorf("%s has not proved it is the runner registered (%s): %v; it takes no "+
			"spawn until `verse runner check %s` finds it proving itself", where, e.Subject, why, e.Name)
	default:
		return runner.Description{}, fmt.Errorf("%s did not answer the handshake before the launch (%s: %v)", where, reach, why)
	}
}
