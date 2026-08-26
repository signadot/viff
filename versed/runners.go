package versed

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// ── runners: where work runs ─────────────────────────────────────────────────
//
// A runner is installed at `runners:<kind>:<name>` carrying a url, and a spawn names one. It
// is not a provider: a provider picks the DOER — which driver, under which frame, with which
// grant — and a runner is WHERE the work runs. The two are orthogonal, so folding runners into
// provider selection would make one selection answer two questions and force a doer entity per
// (doer, place) pair. See docs/working/per-task-runners.md.

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
	if err := runner.CheckFields(node); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var e runner.Entity
	if err := codec.FromNode(node, &e); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := e.Check(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := map[string]any{"at": runner.Ref(e.Kind, e.Name).String()}
	// The probe, and the two dispositions it feeds. They differ on whether waiting could
	// change the answer: a url that answers as something else is answering NOW — a broker's
	// worker leg is the case worth catching — while a url nothing answers is the ordinary
	// state of a runner deployed alongside the entity that names it, where the manifest lands,
	// the pod is pending, and the entity is already written. Refusing that would make
	// deployment order-dependent for a fact that fixes itself.
	if r.URL.Query().Get("probe") != "false" {
		reach, why := runner.Probe(e.URL)
		switch reach {
		case runner.NotRunner:
			http.Error(w, fmt.Sprintf("runner %q: %s is not a runner: %s — if this is a "+
				"broker's worker leg, that is the dial-in face rather than the verse-facing "+
				"one; --no-probe installs it anyway", e.Name, e.URL, why), http.StatusBadRequest)
			return
		case runner.Unreachable:
			// Installed, and said so. The warning rides on the response rather than being
			// logged, because the operator running the command is the one who can act on it.
			out["reach"] = reach.String()
			out["warning"] = why.Error()
		default:
			out["reach"] = reach.String()
		}
	}
	body := entity.Obj{"name": e.Name, "kind": e.Kind, "url": e.URL}
	if _, _, err := s.scopedStore(r).Write(entity.Write{
		At: runner.Ref(e.Kind, e.Name).At(), Value: body.Payload(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeOut(w, r, http.StatusCreated, out)
}

func (s *Server) listRunners(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	probe := r.URL.Query().Get("probe") == "true"
	out := make([]map[string]any, 0)
	ents, readErr := runner.ListErr(store)
	// READ ONCE, COUNT PER RUNNER. The counts are a walk of every task instance, and asking
	// inside the loop made the listing cost runners x instances for an answer that is one pass
	// over the same rows.
	instances := taskRows(store)
	for _, e := range ents {
		ref := runner.Ref(e.Kind, e.Name)
		rec := map[string]any{
			"at": ref.String(), "name": e.Name, "kind": e.Kind, "url": e.URL,
		}
		for k, v := range runnerCounts(instances, ref, e.URL) {
			rec[k] = v
		}
		// Asked for, never stored. A reachability column is as of now or it is a cache that
		// can be stale in the direction that costs — saying a runner is well because it was
		// well when something last looked.
		if probe {
			reach, why := runner.Probe(e.URL)
			rec["reach"] = reach.String()
			if why != nil {
				rec["why"] = why.Error()
			}
		}
		out = append(out, rec)
	}
	// A FAILED READ IS NOT AN EMPTY LIST. List answers nil when the read did not happen, so
	// without this a store that could not be read renders as "nothing is installed" — and
	// `verse source` answered `[]` on a verse holding three sources, which is the one shape an
	// operator acts on. Same guard /state has carried all along, for the same reason. THIS
	// read's error, not the store's last one — see unreadable.
	if unreadable(w, r, readErr) {
		return
	}
	writeOut(w, r, http.StatusOK, out)
}

func (s *Server) getRunner(w http.ResponseWriter, r *http.Request) {
	ref := runner.Ref(r.PathValue("kind"), r.PathValue("name"))
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

// checkRunner probes one on demand. It is the command an operator runs before choosing between
// --cancel and --leave, because the case removal has to be honest about is the runner that is
// already gone — and this says so beforehand rather than in the report afterwards.
func (s *Server) checkRunner(w http.ResponseWriter, r *http.Request) {
	kind, name := r.PathValue("kind"), r.PathValue("name")
	e, ok := runner.Find(s.scopedStore(r), entity.JoinSegs([]string{kind, name}))
	if !ok {
		http.NotFound(w, r)
		return
	}
	reach, why := runner.Probe(e.URL)
	out := map[string]any{
		"at": runner.Ref(kind, name).String(), "url": e.URL, "reach": reach.String(),
	}
	if why != nil {
		out["why"] = why.Error()
	}
	writeOut(w, r, http.StatusOK, out)
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
	kind, name := r.PathValue("kind"), r.PathValue("name")
	ref := runner.Ref(kind, name)
	e, ok := runner.Find(store, entity.JoinSegs([]string{kind, name}))
	if !ok {
		http.NotFound(w, r)
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
	out := map[string]any{"removed": ref.String()}
	if cancelling && len(live) > 0 {
		cancelled, unreachable := cancelOn(e.URL, live, why)
		out["cancelled"] = cancelled
		// What it could not reach, reported rather than counted as ended. Saying "cancelled 3"
		// when three are unreachable is the same lie as recording a queued answer as
		// delivered — and an already-gone runner is WHY an operator removes the entity half
		// the time, so this is the ordinary case rather than the exotic one. Those are
		// orphans, and the orphan path is what covers them.
		if len(unreachable) > 0 {
			out["unreachable"] = unreachable
		}
	}
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: nil}); err != nil {
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
	for _, ent := range entity.ListUnder(r, func(e entity.Ref) bool {
		return mirror.IsTask(e)
	}, entity.VerseRoot, mirror.System, mirror.InstanceKind) {
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
			out = append(out, ent.Ref.ID())
		}
	}
	sort.Strings(out)
	return out
}

// cancelOn ends each task on a runner, and separates the ones it ended from the ones it
// could not reach. A cancel that never arrived must not be counted as one that did.
func cancelOn(url string, ids []string, why string) (cancelled, unreachable []string) {
	p := runner.HTTP(url)
	for _, id := range ids {
		h := p.Attach(id)
		if h == nil {
			unreachable = append(unreachable, id)
			continue
		}
		if err := h.Cancel(why); err != nil {
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
func runnerCounts(instances []entity.Entity, ref entity.Ref, url string) map[string]any {
	var running, total int
	var last string
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
	out := map[string]any{"running": running, "total": total}
	if last != "" {
		out["lastSpawn"] = last
	}
	return out
}

// brokerName is the reserved runner name for this verse's own dial-in broker, and it is
// broker.Broker's Ref() — the string a task already records in `runner` when it ran on one, so
// what a spawn names and what its task reports are one word. It is reserved the way `local` is:
// neither is an installed entity, because an entity carries a url for runner.HTTP to drive and
// both of these are runtimes this process holds.
const brokerName = "broker"

// runnerFor resolves the runner a spawn NAMED, to the one runner that spawn runs on.
//
// THREE NAMES, and the default is the one nobody types. Naming nothing means "run this", and
// the answer is this verse's own runtime; `broker` means "somebody should do this", and it
// waits to be claimed; an installed runner means wherever its url says. Serving a broker adds
// a name and takes none away, which is what keeps a charter that mentions no runner meaning
// what it says: run it.
//
// A spawn naming a runner that is not installed is REFUSED rather than quietly falling back to
// the default. A misspelled runner that ran the work in-process would be the same class of bug
// as a misspelled resolver that quietly left work unsupervised: the deployment says one thing
// and the verse does another.
func (s *Server) runnerFor(store entity.Reader, name string) (task.Runner, string, error) {
	if name == "" {
		return s.runner, "", nil
	}
	// `broker` is a reserved name, resolved here rather than out of the store, for the reason
	// `local` is the meaning of the empty one: both are runtimes this process holds, and a
	// runner entity is an address for runner.HTTP to drive. The name is the broker's own
	// Ref(), which is what a task already records in `runner`, so a spawn names the runner
	// its task will say it ran on.
	if name == brokerName {
		if s.broker == nil {
			return nil, "", fmt.Errorf("no runner %q — this verse serves no dial-in broker "+
				"(start it with --broker --broker-addr <host:port>)", name)
		}
		return s.broker, brokerName, nil
	}
	e, ok := runner.Find(store, name)
	if !ok {
		installed := runner.List(store)
		names := make([]string, 0, len(installed))
		for _, i := range installed {
			names = append(names, runner.Ref(i.Kind, i.Name).String())
		}
		if s.broker != nil {
			names = append(names, brokerName)
		}
		have := "none are installed"
		if len(names) > 0 {
			have = "installed: " + strings.Join(names, ", ")
		}
		return nil, "", fmt.Errorf("no runner %q (%s) — a spawn naming a runner that is not "+
			"installed is refused rather than run somewhere else", name, have)
	}
	return runner.HTTP(e.URL), runner.Ref(e.Kind, e.Name).String(), nil
}
