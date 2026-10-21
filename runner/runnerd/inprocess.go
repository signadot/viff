package runnerd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// Builder constructs a hosted task from a launch the handler has checked (CheckLaunch), plus the
// Registry that serves as its gate resolver. It is injected so the host stays decoupled from any
// particular doer: the builder picks the doer, and gives the task the launch's autonomy.
type Builder func(l runner.Launch, reg *task.Registry) *task.Task

// InProcessHandler is a task handler that runs its tasks in its own process: it hosts a set of
// running tasks, built by its runner's Builder, and says what the runner offers.
type InProcessHandler struct {
	mu    sync.Mutex
	tasks map[string]*hosted
	build Builder
	log   io.Writer
	// describe is what the runner offers, answered on GET /describe.
	describe runner.Description
	// owned is the variables the runner gives a task's shell itself (CheckLaunch).
	owned []string
	// taskProcess is set in a task's process (ServeTask), whose runner checked its repos out and
	// built its network before starting it. Otherwise a launch that names repos, or a network
	// other than open, is refused: a task in the runner's own process has no checkout, no
	// channel to push over, and the runner's network.
	taskProcess bool
	// onQuestion, when set, is called as each hosted task poses a gate question, with the task's
	// id. The host holds no store and should not: publishing a question is the job of whoever
	// owns one, and this is the seam that hands it over, with the step it is about.
	onQuestion func(taskID string, p task.Pending)
	onCheck    func(taskID string, q task.GateQuestion) error
	// onDrop is the counterpart: a question WITHDRAWN when its last waiter stops waiting (an
	// intervention, a pause, a deadline). Without it the record onQuestion posted stays
	// `pending` on the docket, offering a decision nothing is left to receive (git-issue
	// zdaqh9vkh12ksxneg9n0).
	onDrop func(taskID, questionID, why string)
}

// OnQuestion registers the publisher. Optional: runnerd runs standalone in tests and under
// `verse task run` with nothing behind it.
func (d *InProcessHandler) OnQuestion(f func(taskID string, p task.Pending)) { d.onQuestion = f }

// OnCheck registers what may refuse a hosted task's question before it is posed (Registry.OnCheck),
// told the task's id as onQuestion is.
func (d *InProcessHandler) OnCheck(f func(taskID string, q task.GateQuestion) error) { d.onCheck = f }

// OnDrop registers the publisher's other half — a question leaving the docket unanswered. A
// caller that registers one without the other publishes questions it never retires.
func (d *InProcessHandler) OnDrop(f func(taskID, questionID, why string)) { d.onDrop = f }

type hosted struct {
	task   *task.Task
	reg    *task.Registry
	prompt string
	// doer is who runs it (the agent's name, or "script"), reported on the snapshot; launch is
	// the launch, whose environment and autonomy a replacement is held to; autonomy is the
	// autonomy as last set.
	doer     string
	launch   *runner.Launch
	autonomy task.Autonomy
	// occurrence is which run this is (Launch.Occurrence), retained so the snapshot can say so:
	// the process, and not only the entity, knows which run it is.
	occurrence int64

	mu     sync.Mutex
	status task.Status
	reason string
	err    string
}

// done reports whether this run has finished — the question the launch dedupe is actually
// asking. A live task of the same id is the SAME work and joins; a finished one is a previous
// run, and the next launch is a new one.
func (h *hosted) done() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status.Terminal()
}

func (h *hosted) snapshot() task.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	pend := h.reg.Pending()
	pending := make([]task.PendingGate, 0, len(pend))
	for _, p := range pend {
		pending = append(pending, task.PendingGate{
			ID: p.Question.ID, Origin: p.Question.Origin, Tool: p.Tool, Action: p.Action,
			Gate: p.Question.Gate, Step: p.Question.Step.Name, Fields: p.Question.Step.Fields, Reason: p.Question.Reason,
		})
	}
	status := h.status
	if status == task.Running && h.task.Paused() {
		status = task.Paused
	}
	return task.Snapshot{
		ID: h.task.ID, Prompt: h.prompt, Agent: h.doer, Status: status,
		// The answer is read live from the driver, not from a copy taken when Run returns: the
		// terminal frame leaves from inside Run, and a reader it brings (versed's mirror) asks
		// for the snapshot before Run has returned. The driver has its answer before it ends.
		Err: h.err, Result: h.task.Result(), Pending: pending, Occurrence: h.occurrence, Settings: taskSettings(h.task),
		Cursor: h.task.Cursor(), // resume the event stream with ?since=<cursor>
	}
}

// taskSettings is what a task's agent runs with, as its runner said it.
func taskSettings(t *task.Task) map[string]string {
	_, settings := t.Agent()
	return settings
}

// NewInProcessHandler returns an InProcessHandler that builds hosted tasks with build.
func NewInProcessHandler(build Builder) *InProcessHandler {
	return &InProcessHandler{tasks: map[string]*hosted{}, build: build, log: os.Stderr}
}

// Describe sets what the runner offers. Set it before the runner is served.
func (d *InProcessHandler) Describe(desc runner.Description) { d.describe = desc }

// Description is what the runner offers, as GET /describe answers it.
func (d *InProcessHandler) Description() runner.Description { return d.describe }

// OwnEnv names the variables the runner gives a task's shell itself, which a launch's `with.env`
// may not name. Unset is task.DefaultShellEnv. Set it before the runner is served.
func (d *InProcessHandler) OwnEnv(names []string) { d.owned = names }

// HTTPHandler returns the HTTP routes (Go 1.22 method+wildcard patterns): the task contract, and
// GET /describe.
func (d *InProcessHandler) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /describe", func(w http.ResponseWriter, r *http.Request) {
		writeOut(w, r, http.StatusOK, d.describe)
	})
	mux.HandleFunc("POST /tasks", d.launch)
	mux.HandleFunc("GET /tasks", d.list)
	mux.HandleFunc("GET /tasks/{id}", d.get)
	mux.HandleFunc("GET /tasks/{id}/events", d.events)
	mux.HandleFunc("POST /tasks/{id}/resolve", d.resolve)
	mux.HandleFunc("POST /tasks/{id}/say", d.say)
	mux.HandleFunc("POST /tasks/{id}/cancel", d.control("cancel"))
	mux.HandleFunc("POST /tasks/{id}/pause", d.control("pause"))
	mux.HandleFunc("POST /tasks/{id}/resume", d.control("resume"))
	mux.HandleFunc("PUT /tasks/{id}/autonomy", d.autonomy)
	return mux
}

func (d *InProcessHandler) lookup(r *http.Request) *hosted {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tasks[r.PathValue("id")]
}

// Lookup returns the live hosted task for id, or nil — the local re-attach path.
func (d *InProcessHandler) Lookup(id string) *task.Task {
	d.mu.Lock()
	defer d.mu.Unlock()
	if h := d.tasks[id]; h != nil {
		return h.task
	}
	return nil
}

// Launch checks a launch as POST /tasks does, then builds, hosts and runs its task — the
// programmatic seam, so a task started in process is hosted and controllable exactly like one
// posted over HTTP. It returns the running task, or why the launch was refused.
func (d *InProcessHandler) Launch(l runner.Launch) (*task.Task, error) {
	if err := d.check(&l); err != nil {
		return nil, err
	}
	h, _ := d.host(l)
	return h.task, nil
}

// check is CheckLaunch, and, outside a task's process, a refusal of repos and of any network but
// open. A task in this process has the process's network, which this handler can't take away.
//
// A network named and not open is refused here. So is an absent one, which means none, but only
// on a handler with no offers: with offers, an absent network is the offer's to refuse
// (checkBuilds), as `verse up`'s does, since it builds open alone. An offer that claims to build
// none on this handler would get past both; only test helpers such as versed's offeringAll write one.
func (d *InProcessHandler) check(l *runner.Launch) error {
	if err := CheckLaunch(l, d.describe, d.owned); err != nil {
		return err
	}
	switch n := l.Network(); {
	case d.taskProcess:
	case len(l.Repos()) > 0:
		return errNoRepos
	case n.Mode != runner.NetworkOpen && (len(l.Environment.Systems["network"]) > 0 || len(d.describe.Offers) == 0):
		return errNoNetwork
	}
	return nil
}

// host builds, registers, wires the read plane, and runs a task. Shared
// by Launch and POST /tasks. The bool reports whether it BUILT one; false means
// the id was already hosted and that task is what came back.
//
// An id is launched ONCE. A second launch of one that is already hosted returns the existing
// task rather than building beside it — because the alternative is not two tasks but
// one lost one: registration is a map assignment, so the newcomer took the id and the
// original kept running with nothing able to reach it, address it, or stop it.
//
// That makes the launch safe to RETRY, which is the point. A caller whose launch request
// timed out cannot know whether the far side acted, and until now its only options were to
// abandon real work or to destroy it.
func (d *InProcessHandler) host(l runner.Launch) (*hosted, bool) {
	d.mu.Lock()
	if h, ok := d.tasks[l.ID]; ok && !h.done() {
		d.mu.Unlock()
		return h, false
	}
	d.mu.Unlock()

	reg := task.NewRegistry()
	tk := d.build(l, reg)

	if l.Converse {
		tk.SetConverse(reg) // the person's turn is a question on this task's own docket
	}
	lc := l
	h := &hosted{task: tk, reg: reg, prompt: l.Prompt, doer: doerName(&l, d.describe), launch: &lc, autonomy: l.Autonomy,
		occurrence: l.Occurrence, status: task.Running}

	// Surface pending decisions on the same read plane as the loop's events, so an
	// attached client sees what a gate is blocked on and can resolve it by id.
	reg.OnCheck(func(q task.GateQuestion) error {
		if d.onCheck != nil {
			return d.onCheck(l.ID, q)
		}
		return nil
	})
	reg.OnPost(func(p task.Pending) {
		if d.onQuestion != nil {
			d.onQuestion(l.ID, p)
		}
		// Whom it asks crosses too: verse poses a remote task's question from this frame, and a
		// turn or an ask naming $principal is the task's person's, not the docket's.
		tk.Emit(task.Event{
			Kind: task.EventPending, ID: p.Question.ID, Tool: p.Tool, Action: p.Action,
			Gate: p.Question.Gate, Step: p.Question.Step.Name, Fields: p.Question.Step.Fields, Text: p.Question.Reason,
			Options: p.Question.Options, Deadline: p.Question.Deadline, Answerers: p.Question.Answerers,
		})
	})
	// And the counterpart, on the SAME registry. This is the one that was missing: the
	// registry's own drop wiring works and is tested, and a hosted task's questions simply had
	// nobody registered to hear it.
	reg.OnDrop(func(q task.GateQuestion, why string) {
		if d.onDrop != nil {
			d.onDrop(l.ID, q.ID, why)
		}
	})

	// Re-checked under the lock: two concurrent launches of one id race the check above, and
	// the loser must not start a second loop. Whoever registered first is the task.
	//
	// A TERMINATED ONE IS NOT A WINNER OF THAT RACE. Nothing reaps this map, so a task that
	// finished stayed in it forever and every later launch of the same id was answered with it:
	// the call returned instantly, carrying the FIRST run's answer, with the second run's
	// prompt on the record beside it. The model was never called, and a completion hook then
	// ran on the replayed answer and advanced the flow as though the work had been done
	// (git-issue 6j911yhzh12kswgjgsn0).
	//
	// The id is stable by design — it is what makes a task addressable and what stops a re-fire
	// spawning twice — so any rule whose subject OUTLIVES one task (a cursor, a queue head,
	// anything that walks) got one real run and then unbounded silent no-ops that each looked
	// like success. The dedupe is meant to be against a task that is still running, which is
	// what "the engine dedups a launch against a live task" says everywhere else; this is the
	// one place that read it as "a task that ever existed".
	d.mu.Lock()
	if existing, ok := d.tasks[l.ID]; ok && !existing.done() {
		d.mu.Unlock()
		return existing, false
	}
	d.tasks[l.ID] = h
	d.mu.Unlock()

	go func() {
		if limit, err := time.ParseDuration(l.Timeout); err == nil && limit > 0 {
			ended := make(chan struct{})
			defer close(ended)
			go timeout(tk, reg, limit, ended)
		}
		// A hosted task outlives its launch call, so it must NOT derive from a
		// request context. Its own subtree cancel (task.Cancel, via POST /cancel) is
		// the stop path.
		out, err := tk.Run(context.Background())
		h.mu.Lock()
		h.reason, h.status = out.Reason, task.Status(out.Reason)
		// AN ERROR IS A CAUSE, NOT A STATUS, and conflating the two made every cancel read as a
		// failure. Run returns the run's cause alongside its outcome — that is how a cancel's
		// reason reaches a caller at all — so `err != nil` is true for a task somebody stopped
		// on purpose, and overwriting the status with Failed threw away what the machine had
		// just decided (exit(Cancelled), emitted as its terminal frame).
		//
		// It is not cosmetic: verse distinguishes the two everywhere it matters. A charter's
		// onCancelled and onFailed are different actions, and verse-dev's merge train says
		// why in its own words — a cancel decided nothing, so the gate is asked again,
		// where a failed run must not re-ask anybody anything. With this, a cancelled run fired
		// onFailed and the question was never re-posed.
		//
		// So the error is recorded and the terminal status stands. Failed remains the fallback
		// for a run that ended with an error and NO terminal status of its own — a loop that
		// broke rather than one that ended.
		if err != nil {
			h.err = err.Error()
			if !h.status.Terminal() {
				h.status = task.Failed
			}
		}
		h.mu.Unlock()
	}()
	return h, true
}

// timeout cancels a task once it has run for limit (Launch.Timeout), not counting the time it
// spends waiting for an answer: while one of its questions is open, the clock stands still.
func timeout(tk *task.Task, reg *task.Registry, limit time.Duration, ended <-chan struct{}) {
	tick := time.NewTicker(timeoutTick)
	defer tick.Stop()
	var used time.Duration
	last := time.Now()
	for {
		select {
		case <-ended:
			return
		case now := <-tick.C:
			if len(reg.Pending()) == 0 {
				used += now.Sub(last)
			}
			last = now
			if used >= limit {
				tk.Cancel(fmt.Sprintf("timed out: it ran for %s, not counting time waiting for an answer", limit), "")
				return
			}
		}
	}
}

// timeoutTick is how often a task's clock is read.
const timeoutTick = 100 * time.Millisecond

// launch is POST /tasks: the launch checked, its task launched, and its snapshot the answer.
func (d *InProcessHandler) launch(w http.ResponseWriter, r *http.Request) {
	l, ok := readLaunch(w, r, d.check)
	if !ok {
		return
	}
	h, created := d.host(l)
	// 201 built it, 200 found it already running. A retrying client needs no more than
	// that, and a client that does not care can ignore it: both mean "this id is running".
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeOut(w, r, status, h.snapshot())
}

func (d *InProcessHandler) list(w http.ResponseWriter, r *http.Request) {
	writeOut(w, r, http.StatusOK, d.Snapshots())
}

// Snapshots returns every hosted task in the shape the task API returns — for versed
// to union the local runtime with a runner's own tasks in one list.
func (d *InProcessHandler) Snapshots() []task.Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]task.Snapshot, 0, len(d.tasks))
	for _, h := range d.tasks {
		out = append(out, h.snapshot())
	}
	return out
}

// SnapshotOf returns one hosted task's snapshot, or false if this runner doesn't host it.
func (d *InProcessHandler) SnapshotOf(id string) (task.Snapshot, bool) {
	d.mu.Lock()
	h := d.tasks[id]
	d.mu.Unlock()
	if h == nil {
		return task.Snapshot{}, false
	}
	return h.snapshot(), true
}

func (d *InProcessHandler) get(w http.ResponseWriter, r *http.Request) {
	h := d.lookup(r)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	writeOut(w, r, http.StatusOK, h.snapshot())
}

func (d *InProcessHandler) events(w http.ResponseWriter, r *http.Request) {
	h := d.lookup(r)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	sse, err := codec.NewSSE(w, r)
	if err != nil {
		codec.HTTPError(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	var ch <-chan task.Event
	var unsub func()
	if s := r.URL.Query().Get("since"); s != "" {
		// Resumable: replay the retained window after the client's cursor, then live.
		// The ring already carries any EventPending, so no separate gate replay is needed.
		since, _ := strconv.ParseInt(s, 10, 64)
		ch, unsub = h.task.SubscribeSince(since)
	} else {
		// Live attach from now: replay currently-blocked gates so it knows what to resolve.
		// A finished task needs no special case here — the bus hands a late subscriber
		// the terminal and closes, so an attach after completion exits rather than blocking
		// on a stream that will never emit again.
		ch, unsub = h.task.Subscribe()
		for _, p := range h.reg.Pending() {
			_ = sse.Send(task.Event{
				Kind: task.EventPending, ID: p.Question.ID, Tool: p.Tool, Action: p.Action,
				Gate: p.Question.Gate, Step: p.Question.Step.Name, Fields: p.Question.Step.Fields, Text: p.Question.Reason,
				Options: p.Question.Options, Deadline: p.Question.Deadline, Answerers: p.Question.Answerers,
			})
		}
	}
	defer unsub()
	sse.Flush()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			_ = sse.Send(ev)
		case <-r.Context().Done():
			return
		}
	}
}

// Resolve answers a hosted task's pending decision by id — the Go entry versed's unified
// gate surface uses to resolve a task gate (write/shell/delegate) the same way it resolves
// an engine gate. Reports whether the decision matched.
func (d *InProcessHandler) Resolve(taskID, decisionID string, res task.Resolution) bool {
	d.mu.Lock()
	h := d.tasks[taskID]
	d.mu.Unlock()
	return h != nil && h.reg.Answer(decisionID, res)
}

func (d *InProcessHandler) resolve(w http.ResponseWriter, r *http.Request) {
	h := d.lookup(r)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	var body runner.ResolveRequest
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// No choice is no answer. Read as a deny it would turn a caller's approval, sent in a shape
	// this runner does not know, into a refusal nobody meant; refused, the gate stays open.
	if body.Choice == "" {
		http.Error(w, "a resolve carries a `choice`", http.StatusBadRequest)
		return
	}
	decision := body.ID
	res := task.Resolution{
		Choice: body.Choice, Proceed: body.Choice == task.Proceed, By: body.By, Steer: body.Say,
	}
	if !h.reg.Answer(decision, res) {
		http.Error(w, "unknown or already-answered decision", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *InProcessHandler) say(w http.ResponseWriter, r *http.Request) {
	h := d.lookup(r)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	var body runner.SayRequest
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, fmt.Sprintf("the say does not parse: %v", err), http.StatusBadRequest)
		return
	}
	if body.Instruction == "" {
		http.Error(w, "a say carries an `instruction`", http.StatusBadRequest)
		return
	}
	// Empty `by` is an unattributed caller, said as such. An ended task hears nothing, and says
	// so, as one whose driver has no turn for words does.
	if err := h.task.Steer(body.Instruction, body.By, body.Ask); err != nil {
		status := http.StatusGone
		if errors.Is(err, task.ErrUnsteerable) {
			status = http.StatusUnprocessableEntity
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// autonomy is PUT /tasks/{id}/autonomy: the running task's autonomy, replaced from its next step.
func (d *InProcessHandler) autonomy(w http.ResponseWriter, r *http.Request) {
	h := d.lookup(r)
	if h == nil {
		http.NotFound(w, r)
		return
	}
	var body runner.AutonomyRequest
	if err := codec.ReadStrict(r, &body); err != nil {
		http.Error(w, fmt.Sprintf("the autonomy does not parse: %v", err), http.StatusBadRequest)
		return
	}
	if err := d.setAutonomy(h, body.Autonomy, body.By); errors.Is(err, task.ErrEnded) {
		http.Error(w, fmt.Sprintf("task %s has ended", h.task.ID), http.StatusGone)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetAutonomy replaces a hosted task's autonomy as PUT /tasks/{id}/autonomy does, for the caller
// that hosts this handler in its own process: checked against what the runner keeps fixed
// (CheckAutonomy) and then set, so a task is held the same way however its autonomy is changed.
func (d *InProcessHandler) SetAutonomy(id string, p task.Autonomy, by string) error {
	d.mu.Lock()
	h := d.tasks[id]
	d.mu.Unlock()
	if h == nil {
		return task.ErrNoSuchTask
	}
	return d.setAutonomy(h, p, by)
}

func (d *InProcessHandler) setAutonomy(h *hosted, p task.Autonomy, by string) error {
	// Held to the launch's autonomy, not the last one set: a verb a replacement dropped is still
	// one the run asked of its runner when it started, and may come back.
	if err := CheckAutonomy(p, h.launch); err != nil {
		return err
	}
	if err := h.task.SetAutonomy(p, by); err != nil {
		return err
	}
	h.mu.Lock()
	h.autonomy = p
	h.mu.Unlock()
	return nil
}

// control returns a handler for the subtree ops: a cancel carries why and who
// (runner.CancelRequest), a pause and a resume who (PauseRequest, ResumeRequest). Each is a
// command all the same with no body.
func (d *InProcessHandler) control(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := d.lookup(r)
		if h == nil {
			http.NotFound(w, r)
			return
		}
		switch op {
		case "cancel":
			// The reason is what the task's charters see on `onCancelled`. A cancel with no
			// body is a cancel all the same; one whose body does not parse is refused, since
			// the reason it meant to give is not known and the task would end under another.
			var body runner.CancelRequest
			if err := codec.Read(r, &body); err != nil {
				http.Error(w, fmt.Sprintf("the cancel does not parse: %v", err), http.StatusBadRequest)
				return
			}
			h.task.Cancel(body.Reason, body.By)
		case "pause":
			var body runner.PauseRequest
			if err := codec.Read(r, &body); err != nil {
				http.Error(w, fmt.Sprintf("the pause does not parse: %v", err), http.StatusBadRequest)
				return
			}
			h.task.Pause(body.By)
		case "resume":
			var body runner.ResumeRequest
			if err := codec.Read(r, &body); err != nil {
				http.Error(w, fmt.Sprintf("the resume does not parse: %v", err), http.StatusBadRequest)
				return
			}
			h.task.Resume(body.By)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeOut(w http.ResponseWriter, r *http.Request, code int, v any) {
	codec.Write(w, r, code, v)
}
