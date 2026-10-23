package versed

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
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
	if r.URL.Query().Get("probe") != "false" {
		found, desc, whyErr := client.Probe(e.URL)
		reach = found.String()
		switch found {
		case client.NotRunner:
			http.Error(w, fmt.Sprintf("runner %q: %s is not a runner: %s — --no-probe installs it anyway",
				e.Name, e.URL, whyErr), http.StatusBadRequest)
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
	store := s.scopedStore(r)
	if _, _, err := store.Write(entity.Write{
		At: runnerEntityRef(e.Name).At(), Value: body.Payload(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// What it offers replaces what a registration of the name kept, never merges into it.
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
	out := runnerRow(taskRows(store), e, false)
	out.Reach, out.Why, out.Warning = reach, why, warning
	writeOut(w, r, http.StatusCreated, out)
}

func (s *Server) listRunners(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	probe := r.URL.Query().Get("probe") == "true"
	out := make([]api.RunnerRow, 0)
	ents, bad, readErr := readRegistrations[api.Runner](store, runnersSystem, isRunnerRef)
	// READ ONCE, COUNT PER RUNNER. The counts are a walk of every task instance, and asking
	// inside the loop made the listing cost runners x instances for an answer that is one pass
	// over the same rows.
	instances := taskRows(store)
	for _, e := range ents {
		e.Described = nil // GET /runners/{name} says what it offers; a listing row does not
		out = append(out, runnerRow(instances, e, probe))
	}
	for _, b := range bad {
		out = append(out, api.RunnerRow{Runner: api.Runner{Name: b.Ref.Name()}, At: b.Ref.String(), Unreadable: b.Err.Error()})
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
// of the work on it, counted from instances. probe asks the runner what it is, as `?probe=true`
// does.
func runnerRow(instances []entity.Entity, e api.Runner, probe bool) api.RunnerRow {
	ref := runnerEntityRef(e.Name)
	rec := api.RunnerRow{Runner: e, At: ref.String()}
	rec.Running, rec.Total, rec.LastSpawn = runnerCounts(instances, ref, e.URL)
	// Asked for, never stored. A reachability column is as of now or it is a cache that can be
	// stale in the direction that costs — saying a runner is well because it was well when
	// something last looked.
	if probe {
		reach, _, why := client.Probe(e.URL)
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
	var e api.Runner
	if err := codec.FromNode(ent.Payload, &e); err != nil {
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
	if !installed(w, r, fmt.Sprintf("runner %q", name), ok, err) {
		return
	}
	reach, desc, why := client.Probe(e.URL)
	out := api.RunnerCheck{At: runnerEntityRef(name).String(), URL: e.URL, Reach: reach.String()}
	if why != nil {
		out.Why = why.Error()
	}
	// A runner that answers describes itself afresh: what it offers may have changed since it
	// was installed (a restart with a model credential, say), and the registration says what
	// the runner says now. Written as verse, not as whoever asked: the offers are the runner's
	// words, recorded the way a task's status is, and anybody who may read the runner may ask.
	if reach == client.IsRunner {
		if err := desc.Check(); err != nil {
			http.Error(w, fmt.Sprintf("runner %q: %s does not say what it offers and the networks it builds: %v; its registration "+
				"keeps what it said before", name, e.URL, err), http.StatusBadGateway)
			return
		}
		out.Offers = desc.Offers
		if err := writeDescribed(s.storeForScope(r.URL.Query().Get("scope")), name, desc); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeOut(w, r, http.StatusOK, out)
}

// writeDescribed keeps a runner's description on its registration, under `described`, in place
// of what stood there: a write merges, a list element by element (so ReplaceAt's removals, which
// reach a value's fields and not its list's elements, would leave an old offer's fields on a new
// one), so what it said before is removed first, and `offers`, which an older verse kept the
// offers under, with it.
//
// BETWEEN THE TWO WRITES the registration has no description, and that is safe: a spawn naming
// the runner reads it through offersOf, which asks the runner again where the registration is
// not Current, so a spawn arriving then is checked against what the runner says, at the cost of
// one more probe, never against nothing.
func writeDescribed(w entity.Store, name string, desc runner.Description) error {
	at := runnerEntityRef(name).At()
	if _, _, err := w.Write(entity.Write{At: at, Value: entity.Obj{"described": entity.Removed(), "offers": entity.Removed()}.Payload()}); err != nil {
		return err
	}
	_, _, err := w.Write(entity.Write{At: at, Value: entity.Obj{"described": desc}.Payload()})
	return err
}

// defaultDescription is what the default runner offers, for the check a spawn naming no runner
// is made against before it is launched (offersOf reads an installed runner's).
func (s *Server) defaultDescription() runner.Description {
	// The embedded runner's offers, where it is the default. A default reached by url (`verse up
	// --runner`) has no registration to keep its offers on: it checks its launches itself.
	if _, local := s.runner.(localRunner); local {
		return s.Embedded.Description()
	}
	return runner.Description{}
}

// checkOffer refuses a launch the runner it is for would refuse, before it leaves: the doer is
// not on offer there, or what the launch asks of it is not what the offer takes. A runner the
// launch names that is not installed refuses it, as the launch itself would (runnerFor). One
// installed with no offers on record (--no-probe, or a pod that was not up when it was added)
// is asked now, and what it says is kept on its registration and checked against; one that
// still does not describe itself takes no spawn. A check with nothing to check against is no
// check, and the runner is the side that answers for what it runs. The default runner reached
// by url alone (`verse up --runner`) has no registration to keep offers on, and is the one
// exception: it checks its launches itself.
func (s *Server) checkOffer(store entity.Reader, name string, l *runner.Launch) error {
	return s.checkOfferBy(store, name, l, runnerd.CheckOffer)
}

// checkOfferBy is checkOffer with the check made: runnerd.CheckOffer for a launch, bound, and
// runnerd.CheckOfferAsWritten for a charter's spawn at install (offerCheck).
func (s *Server) checkOfferBy(store entity.Reader, name string, l *runner.Launch, check func(*runner.Launch, runner.Description) error) error {
	desc, where, err := s.offersOf(store, name)
	if err != nil {
		return err
	}
	if len(desc.Offers) == 0 {
		return nil
	}
	if err := check(l, desc); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	return nil
}

// offersOf is what the runner a spawn names offers, as its registration keeps it or as it
// answers now, and the name a refusal calls it by.
func (s *Server) offersOf(store entity.Reader, name string) (runner.Description, string, error) {
	if name == "" {
		return s.defaultDescription(), "the default runner", nil
	}
	e, ok, err := findRunnerErr(store, name)
	if err != nil && !errors.Is(err, entity.ErrForbidden) {
		return runner.Description{}, "", errors.New(notRead(fmt.Sprintf("runner %q", name), err))
	}
	if !ok {
		return runner.Description{}, "", fmt.Errorf("no runner %q is installed — a spawn naming a runner that is "+
			"not installed is refused rather than run somewhere else", name)
	}
	where := runnerEntityRef(e.Name).String()
	if e.Current() {
		return e.Description(), where, nil
	}
	reach, desc, why := client.Probe(e.URL)
	if reach != client.IsRunner {
		return runner.Description{}, "", fmt.Errorf("%s has not described itself (%s: %v): a spawn naming it is "+
			"refused until it answers `verse runner check %s`", where, reach, why, e.Name)
	}
	if err := desc.Check(); err != nil {
		return runner.Description{}, "", fmt.Errorf("%s does not say what it offers and the networks it builds: %v", where, err)
	}
	s.recordOffers(store, e.Name, desc)
	return desc, where, nil
}

// recordOffers keeps what a runner said on its registration, as `verse runner check` does,
// where the store this read was made in can be written; a view that cannot keeps nothing, and
// the next spawn asks again.
func (s *Server) recordOffers(store entity.Reader, name string, desc runner.Description) {
	w, ok := store.(entity.Store)
	if !ok {
		return
	}
	if err := writeDescribed(w, name, desc); err != nil {
		fmt.Fprintf(os.Stderr, "verse: runner %s: keeping what it offers: %v\n", runnerEntityRef(name), err)
	}
}

// probeRunners asks each installed runner with no offers on record what it offers, when verse
// starts: a registration made with --no-probe, or against a pod that was not up yet, is what a
// repopulate leaves, and until the runner has described itself a spawn naming it is refused
// (checkOffer). One that answers has its offers kept; one that does not is named here, and
// asked again by the next spawn that names it.
func (s *Server) probeRunners() error {
	runners, err := installedRunnersErr(s.Store)
	if err != nil {
		return err
	}
	for _, e := range runners {
		if e.Current() {
			continue
		}
		reach, desc, why := client.Probe(e.URL)
		if reach != client.IsRunner {
			fmt.Fprintf(os.Stderr, "verse: runner %s has not described itself (%s: %v); a spawn naming it is refused until it does\n",
				runnerEntityRef(e.Name), reach, why)
			continue
		}
		if err := desc.Check(); err != nil {
			fmt.Fprintf(os.Stderr, "verse: runner %s does not say what it offers and the networks it builds: %v\n", runnerEntityRef(e.Name), err)
			continue
		}
		s.recordOffers(s.Store, e.Name, desc)
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
		out.Cancelled, unreachable = cancelOn(e.URL, live, why)
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
func cancelOn(url string, ids []string, why string) (cancelled, unreachable []string) {
	p := client.HTTP(url)
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

// runnerFor resolves the runner a spawn NAMED, to the one runner that spawn runs on.
//
// TWO NAMES, and the default is the one nobody types. Naming nothing means "run this", and
// the answer is this verse's own runtime; an installed runner means wherever its url says.
//
// A spawn naming a runner that is not installed is REFUSED rather than quietly falling back to
// the default. A misspelled runner that ran the work in-process would be the same class of bug
// as a misspelled resolver that quietly left work unsupervised: the deployment says one thing
// and the verse does another.
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

func (s *Server) runnerFor(store entity.Reader, name string) (client.Launcher, string, error) {
	if name == "" {
		return s.runner, "", nil
	}
	e, ok, err := findRunnerErr(store, name)
	if err != nil && !errors.Is(err, entity.ErrForbidden) {
		// The read did not happen, which is not a runner that is not installed: said as what
		// it is, so whoever reads the refusal waits for the store rather than installs again.
		return nil, "", errors.New(notRead(fmt.Sprintf("runner %q", name), err))
	}
	if !ok {
		// IN THE SPAWNER'S VIEW: the store is theirs, so a runner they may not read and one
		// that is not there are one answer, and the listing beside it is what they may read.
		// A failed listing names none rather than saying none are installed.
		have := "its runners could not be listed"
		if listed, err := installedRunnersErr(store); err == nil {
			names := make([]string, 0, len(listed))
			for _, i := range listed {
				names = append(names, runnerEntityRef(i.Name).String())
			}
			have = "none are installed"
			if len(names) > 0 {
				have = "installed: " + strings.Join(names, ", ")
			}
		}
		// Whose view, by name where the store says: a store read as nobody in particular is
		// the spawner's all the same.
		who := "the spawner"
		if st, ok := store.(entity.Store); ok && entity.PrincipalOf(st) != "" {
			who = entity.PrincipalOf(st)
		}
		return nil, "", fmt.Errorf("in the view of %s, no runner %q is installed (%s) — a spawn "+
			"naming a runner that is not installed is refused rather than run somewhere else",
			who, name, have)
	}
	return client.HTTP(e.URL), runnerEntityRef(e.Name).String(), nil
}
