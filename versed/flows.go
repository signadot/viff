package versed

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger"
)

// The flow surface: one noun for the work in flight and the work held from now on. A flow is a
// query over live tasks; a suspension is a query over the work from now on, naming the same
// coordinates a live task carries — a charter, a rule of it, a subject. So both are read and
// acted on through one selection (api.FlowSelect), and `GET /flows` narrowed by a selection lists
// exactly what an act with the same selection reaches now.
//
// A selection is a TASK, that run only, or a CHARTER and a SUBJECT, the one left out being `*`. An
// act names something: one that names nothing is refused with its usage, since a bare verb meaning
// everything is how somebody typing it to see its usage would suspend the verse. Only the listing,
// a read, answers for everything when nothing narrows it.

// Usage sentences a refused selection answers with.
const (
	usageHold   = "name a run with --task, or what to suspend with --charter, --subject or both; the one left out is '*'"
	usageSay    = "name what to say it to with --task, --charter or --subject"
	usageCancel = "cancel names one run, with --task: a charter or a subject is suspended, and then its runs cancelled one by one"
	usageTask   = "--task names one run, and combines with neither --charter nor --subject"
)

// selectionOf reads a selection's charter and subject into the suspension that spells them,
// every charter and every subject standing in for an absent one: what a listing narrows by, and
// what a say or a suspension names once it has named something (holdSelection).
func selectionOf(sel api.FlowSelect) (trigger.Suspension, error) {
	charter, subject := sel.Charter, sel.Subject
	if charter == "" {
		if sel.Recursive {
			return trigger.Suspension{}, fmt.Errorf("recursive names the charter at an address and "+
				"every charter beneath it, so it wants --charter: %s", usageHold)
		}
		charter = trigger.Every
	}
	if subject == "" {
		subject = trigger.Every
	}
	address, rule := trigger.ParseCharterFlag(charter)
	return trigger.NewSuspension(address, rule, subject, sel.Recursive)
}

// selectionQuery reads a listing's selection off its query string.
func selectionQuery(r *http.Request) api.FlowSelect {
	q := r.URL.Query()
	return api.FlowSelect{Task: q.Get("task"), Charter: q.Get("charter"), Subject: q.Get("subject"),
		Recursive: q.Get("recursive") == "true"}
}

// readSelection reads an act's body, refusing a task named beside anything else; false means
// refused, having written the response.
func readSelection(w http.ResponseWriter, r *http.Request) (api.FlowSelect, bool) {
	var b api.FlowSelect
	if err := codec.Read(r, &b); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return b, false
	}
	if b.Task != "" && (b.Charter != "" || b.Subject != "" || b.Recursive) {
		http.Error(w, usageTask, http.StatusBadRequest)
		return b, false
	}
	return b, true
}

// listFlows is `GET /flows`: the live tasks and the suspensions of the request's scope that its
// selection names, everything when nothing narrows it. The rows are what an act with the same
// flags reaches: a task by id; or, for a charter and a subject — an absent one being `*` — the
// live tasks a suspension in those words would hold, and the one suspension a resume in them
// would lift, the record whose id the flags spell.
func (s *Server) listFlows(w http.ResponseWriter, r *http.Request) {
	sel := selectionQuery(r)
	scope := r.URL.Query().Get("scope")
	rows := []api.FlowRow{}
	if sel.Task != "" {
		if sel.Charter != "" || sel.Subject != "" || sel.Recursive {
			http.Error(w, usageTask, http.StatusBadRequest)
			return
		}
		for _, eng := range s.flowEngines(scope) {
			if lt, ok := eng.Live(sel.Task); ok {
				rows = append(rows, taskRow(eng, lt))
			}
		}
		writeOut(w, r, http.StatusOK, rows)
		return
	}
	narrow, err := selectionOf(sel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, eng := range s.flowEngines(scope) {
		for _, lt := range eng.SelectFlow(narrow) {
			rows = append(rows, taskRow(eng, lt))
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Task < rows[j].Task })
	if tbl := s.suspensionsOf(scope); tbl != nil {
		// A listing that left the suspensions out while they restore would say nothing is held
		// while everything is.
		if !tbl.Loaded() {
			suspensionError(w, r, errSuspensionsLoading)
			return
		}
		switch {
		case sel.Charter == "" && sel.Subject == "":
			for _, sus := range tbl.List() {
				rows = append(rows, s.suspensionRow(sus))
			}
		default:
			if sus, ok := tbl.Get(narrow.ID()); ok {
				rows = append(rows, s.suspensionRow(sus))
			}
		}
	}
	writeOut(w, r, http.StatusOK, rows)
}

// taskRow is a live task as a row: named by its id, and by the charter and rule that spawned it.
func taskRow(eng *trigger.Engine, lt trigger.LiveTask) api.FlowRow {
	row := api.FlowRow{Kind: api.FlowRowTask, Task: lt.ID, Subject: lt.Subject, HeldBy: lt.HeldBy}
	if lt.Rule != "" {
		row.Charter = eng.At() + trigger.AtSep + lt.Rule
	}
	return row
}

// flowCount is how much work is live in a scope: its tasks, and the distinct subjects they are
// about, across every engine the scope may see (flowEngines) — a package's charter runs in an
// engine of its own, so baseline's registry alone is blind to the work a packaged rule started.
func (s *Server) flowCount(scope string) api.FlowCount {
	var out api.FlowCount
	subjects := map[string]bool{}
	for _, eng := range s.flowEngines(scope) {
		for _, lt := range eng.SelectFlow(everything) {
			out.Tasks++
			subjects[lt.Subject] = true
		}
	}
	out.Subjects = len(subjects)
	return out
}

// everything is the selection naming every charter about every subject.
var everything, _ = trigger.NewSuspension(trigger.Every, trigger.Every, trigger.Every, false)

// flowSuspend is `POST /flow/suspend`: a run held at its next boundary until `flow resume
// --task`, or the durable suspension a charter and a subject name.
func (s *Server) flowSuspend(w http.ResponseWriter, r *http.Request) {
	b, ok := readSelection(w, r)
	if !ok {
		return
	}
	if b.Task != "" {
		if row, ok := s.holdTask(w, r, b.Task, true); ok {
			writeOut(w, r, http.StatusOK, row)
		}
		return
	}
	sus, ok := holdSelection(w, b)
	if !ok {
		return
	}
	if b.Why == "" {
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
	if by == "" { // refused at the door already; never the system's hold for want of a name
		http.Error(w, "a suspension is held by somebody: "+api.PrincipalHeader+" is required", http.StatusBadRequest)
		return
	}
	held, err := s.hold(scope, sus, by, b.Why)
	if err != nil {
		suspensionError(w, r, err)
		return
	}
	writeOut(w, r, http.StatusOK, s.suspensionRow(held))
}

// flowResume is `POST /flow/resume`: the operator's hold on a run lifted, or the suspension a
// charter and a subject name lifted, whoever holds it.
func (s *Server) flowResume(w http.ResponseWriter, r *http.Request) {
	b, ok := readSelection(w, r)
	if !ok {
		return
	}
	if b.Task != "" {
		if row, ok := s.holdTask(w, r, b.Task, false); ok {
			writeOut(w, r, http.StatusOK, row)
		}
		return
	}
	sus, ok := holdSelection(w, b)
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
	if by == "" { // as above: nobody's release is not the system's
		http.Error(w, "a suspension is released by somebody: "+api.PrincipalHeader+" is required", http.StatusBadRequest)
		return
	}
	rest, _, err := s.release(scope, sus.ID(), by)
	if err != nil {
		suspensionError(w, r, err)
		return
	}
	out := s.suspensionRow(rest)
	// SUSPENSIONS ONLY ADD UP: lifting a pair a wider suspension also covers leaves the pair held,
	// and the answer says by what rather than letting the reader believe it runs.
	for _, other := range s.suspensionsOf(scope).List() {
		if other.ID() != sus.ID() && sus.Within(other) {
			out.Still = append(out.Still, other.ID())
		}
	}
	writeOut(w, r, http.StatusOK, out)
}

// holdSelection reads the suspension an act that holds or lifts names: a charter, a subject, or
// both, the one left out being every charter or every subject; naming neither is refused with the
// usage. False means refused, having written the response.
func holdSelection(w http.ResponseWriter, b api.FlowSelect) (trigger.Suspension, bool) {
	if b.Charter == "" && b.Subject == "" {
		http.Error(w, usageHold, http.StatusBadRequest)
		return trigger.Suspension{}, false
	}
	sus, err := selectionOf(b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return trigger.Suspension{}, false
	}
	return sus, true
}

// holdTask takes or lifts the operator's hold on one run, wherever it runs: `flow suspend|resume
// --task`, and the runtime's `/tasks/{id}/pause|resume`. The task is the request's scope's own
// and control over it is held, asked in that order as every control verb asks; a resume of a run
// a suspension holds is refused naming it, since it would run what the suspension covers. The
// engine tracking the run marks the hold the operator's, so a suspension's lift leaves it; a run
// no engine tracks is held through its handle. False means refused, having written the response.
func (s *Server) holdTask(w http.ResponseWriter, r *http.Request, id string, hold bool) (api.FlowRow, bool) {
	holder, ok := s.controlledTask(w, r, id)
	if !ok {
		return api.FlowRow{}, false
	}
	engines := s.flowEngines(holder)
	if !hold {
		for _, eng := range engines {
			if by := eng.TaskSuspended(id); len(by) > 0 {
				heldBySuspension(w, r, "task "+id, by)
				return api.FlowRow{}, false
			}
		}
	}
	for _, eng := range engines {
		if lt, ok := eng.HoldTask(id, hold); ok {
			return taskRow(eng, lt), true
		}
	}
	h := s.taskHandle(holder, id)
	if h == nil {
		noSuchTask(w, r, id)
		return api.FlowRow{}, false
	}
	if hold {
		h.Pause()
	} else {
		h.Resume()
	}
	return api.FlowRow{Kind: api.FlowRowTask, Task: id}, true
}

// controlledTask is what every control verb over one task asks first: that the task is the
// request's scope's own (owns.go), then that its principal holds control over it. It hands back
// the scope holding the task; false means refused, having written the response.
func (s *Server) controlledTask(w http.ResponseWriter, r *http.Request, id string) (string, bool) {
	holder, found, err := s.taskScope(id, r.URL.Query().Get("scope"))
	if !s.ownHere(w, r, "task "+id, holder, found, err) {
		return "", false
	}
	if refused(w, entity.Check(s.scopedStore(r), principalOf(r), entity.Control, mirror.Ref(id), "controlling it")) {
		return "", false
	}
	return holder, true
}

// taskHandle is the handle a task is controlled through, wherever it runs: the one the engine of
// the scope holding it tracks it by, else the local runtime's, else the runner hosting it.
func (s *Server) taskHandle(holder, id string) task.Handle {
	for _, eng := range s.flowEngines(holder) {
		if h, ok := eng.Handle(id); ok {
			return h
		}
	}
	if t := s.Taskd.Lookup(id); t != nil {
		return t
	}
	return s.attach(id)
}

// flowSay is `POST /flow/say`: an instruction said to every live task the selection names — one
// run, or what a charter and a subject select.
//
// A SCOPE STEERS ITS OWN. Its engine holds what it started and nothing inherited, so the say
// reaches there alone; when that reaches nothing and another scope's engine holds work the
// selection names — baseline's, typically, read through the overlay — it is refused naming it
// (inherited), since answering `affected: 0` over work that is running would be wrong twice. The
// same from baseline: a scope's work is not baseline's to steer unaware.
//
// CONTROL IS HELD over every task it reaches, asked of each before any is said to: one refused
// refuses the whole of it, naming that task.
func (s *Server) flowSay(w http.ResponseWriter, r *http.Request) {
	b, ok := readSelection(w, r)
	if !ok {
		return
	}
	// Said BY whoever the request is made as (actor), which is who control is asked of below.
	if b.By, ok = actor(w, s.scopedStore(r), b.By); !ok {
		return
	}
	if b.Task == "" && b.Charter == "" && b.Subject == "" {
		http.Error(w, usageSay, http.StatusBadRequest)
		return
	}
	if b.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	out := api.Steered{Task: b.Task, Charter: b.Charter, Subject: b.Subject, Verb: "say"}
	if b.Task != "" {
		holder, ok := s.controlledTask(w, r, b.Task)
		if !ok {
			return
		}
		h := s.taskHandle(holder, b.Task)
		if h == nil {
			noSuchTask(w, r, b.Task)
			return
		}
		h.Steer(b.Text, b.By)
		out.Affected = 1
		writeOut(w, r, http.StatusOK, out)
		return
	}
	sel, err := selectionOf(b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	scope := r.URL.Query().Get("scope")
	type reached struct {
		eng *trigger.Engine
		id  string
	}
	var tasks []reached
	for _, eng := range s.flowEngines(scope) {
		for _, lt := range eng.SelectFlow(sel) {
			tasks = append(tasks, reached{eng, lt.ID})
		}
	}
	if len(tasks) == 0 {
		if holders := s.flowHolders(scope, sel); len(holders) > 0 {
			inherited(w, r, "the live work "+sel.ID()+" names", scope, holders)
			return
		}
	}
	store := s.scopedStore(r)
	for _, t := range tasks {
		if refused(w, entity.Check(store, principalOf(r), entity.Control, mirror.Ref(t.id), "steering it")) {
			return
		}
	}
	for _, t := range tasks {
		if h, ok := t.eng.Handle(t.id); ok {
			h.Steer(b.Text, b.By)
			out.Affected++
		}
	}
	writeOut(w, r, http.StatusOK, out)
}

// flowCancel is `POST /flow/cancel`: one run cancelled. Cancelling everything a charter or a
// subject selects has no spelling; a suspension, then a cancel per run, is that.
func (s *Server) flowCancel(w http.ResponseWriter, r *http.Request) {
	b, ok := readSelection(w, r)
	if !ok {
		return
	}
	if b.Task == "" {
		http.Error(w, usageCancel, http.StatusBadRequest)
		return
	}
	holder, ok := s.controlledTask(w, r, b.Task)
	if !ok {
		return
	}
	h := s.taskHandle(holder, b.Task)
	if h == nil {
		noSuchTask(w, r, b.Task)
		return
	}
	h.Cancel("flow cancel")
	writeOut(w, r, http.StatusOK, api.Steered{Task: b.Task, Verb: "cancel", Affected: 1})
}
