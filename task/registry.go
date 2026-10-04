package task

import (
	"context"
	"errors"
	"sync"
)

// Registry is a question-addressed Resolver: blocking Resolve calls from any
// number of concurrent tasks register a pending question keyed by
// GateQuestion.ID; an external caller (a human, an attach client, a parent) answers
// them by ID via Answer. This is the seam that makes concurrent escalation from
// parallel children coherent — each pending question carries its own ID and
// Origin, and resolutions are addressed, never mismatched. A single Registry
// shared as the resolver of a subtree collects the whole subtree's escalations.
type Registry struct {
	mu      sync.Mutex
	pending map[string]*pendingQuestion
	onPost  func(Pending)              // optional: notify a watcher when a question is posted
	onDrop  func(GateQuestion, string) // optional: notify a watcher when one leaves unanswered
	// optional: OnDrop with the cause, called in its place when set
	onDropCause func(GateQuestion, string, error)
	onCheck     func(GateQuestion) error // optional: refuse a question before it is posed
}

// pendingQuestion is one question standing on the docket, and everyone waiting on it. The
// answer is broadcast by closing `done` rather than sent on a channel, because a question may
// have several waiters: a re-fire about the same subject while it stands poses the SAME id, and
// joining the standing question is what makes that one decision instead of two.
//
// waiters is what keeps a cancelled waiter from taking the question with it. Without the
// count, one caller's context ending would delete a question others are still blocked on, and
// they would wait forever on something no longer on the docket to be answered.
type pendingQuestion struct {
	q       GateQuestion
	tool    string
	effects []Effect
	done    chan struct{} // closed when answered
	res     Resolution    // valid once done is closed
	waiters int
}

// Pending is the read-surface view of one blocked question.
//
// Effects rides along because this is the last place the ToolUse exists: the registry is
// handed one, and everything downstream sees only what Pending carries. A question published
// without its derived effects can be routed by gate name and by nothing else, which is the
// difference between a docket entry and a fact something can match on.
type Pending struct {
	Question GateQuestion
	Tool     string
	Effects  []Effect
}

func NewRegistry() *Registry {
	return &Registry{pending: map[string]*pendingQuestion{}}
}

// OnPost registers a callback invoked (outside the lock) whenever a question is
// posted — for a live read surface / attach stream. Optional.
func (r *Registry) OnPost(f func(Pending)) { r.onPost = f }

// OnDrop registers the counterpart: a question leaving the docket WITHOUT an answer, because
// the last caller waiting on it stopped waiting.
//
// It exists because OnPost had none. A publisher was told about every question that arrived
// and about none that left, so a question the registry silently discarded stayed published as
// `pending` — offered on a docket, answerable-looking, and undeliverable — until something
// else happened to notice.
//
// WHY it left comes with it, because there is now more than one reason and they read very
// differently to whoever was going to answer. The asker dying and an operator saying something
// that made the question moot both retire a question undecided; only the second means somebody
// is still there and chose this. The reason is the waiter's context CAUSE, which is how a
// caller says what it meant by giving up (see task.Task's withdrawal of a standing question).
//
// Only Answer's counterpart is missing here by design: an answered question is announced by
// the answer itself, which the answerer already has.
func (r *Registry) OnDrop(f func(GateQuestion, string)) { r.onDrop = f }

// OnDropCause is OnDrop with the cause itself beside its words, for a publisher that records WHAT
// kind of withdrawal it was rather than only saying it: a suspension withdraws a question to ask
// it again (Suspends), which a docket keeps, where the asker giving up does not. Set, it is called
// in OnDrop's place.
func (r *Registry) OnDropCause(f func(GateQuestion, string, error)) { r.onDropCause = f }

// OnCheck registers what may refuse a question before it is posed: Resolve answers the refusal as
// its error, to the gate or the action that asked, and nothing reaches the docket. A question
// already pending is joined without being asked again.
func (r *Registry) OnCheck(f func(GateQuestion) error) { r.onCheck = f }

// Suspends reports whether a withdrawal's cause is a suspension: work held, to be asked again when
// it lifts. The cause says so itself (trigger.SuspendedError).
func Suspends(cause error) bool {
	var s interface{ Suspends() bool }
	return errors.As(cause, &s) && s.Suspends()
}

// Resolve (the Resolver interface) posts the question and blocks until it is answered by ID
// or the context is cancelled.
//
// A question whose ID is ALREADY PENDING is joined, not re-posted: one docket entry, one
// decision, delivered to everyone waiting on it. That is what a deterministic id buys — a
// poser that re-asks about the same subject (a re-fire, a level-triggered re-evaluation)
// asks the standing question again rather than filling the docket with copies of it, and an
// approver answers a thing once.
//
// Joining also closes a hole a deterministic id would otherwise open. `pending` is keyed by
// ID, so a second post used to REPLACE the first — leaving that caller blocked on a question
// no longer on the docket, waiting for an answer nothing could deliver.
func (r *Registry) Resolve(ctx context.Context, q GateQuestion, tu ToolUse) (Resolution, error) {
	r.mu.Lock()
	_, standing := r.pending[q.ID]
	check := r.onCheck
	r.mu.Unlock()
	if !standing && check != nil {
		if err := check(q); err != nil {
			return Resolution{}, err
		}
	}
	r.mu.Lock()
	pq, joined := r.pending[q.ID]
	if !joined {
		pq = &pendingQuestion{q: q, tool: tu.Name, effects: tu.Effects, done: make(chan struct{})}
		r.pending[q.ID] = pq
	}
	pq.waiters++
	onPost := r.onPost
	r.mu.Unlock()
	// Only a NEW question is announced: a join adds a waiter to something already on the
	// docket, and re-announcing it would show a watcher a question appearing twice.
	if !joined && onPost != nil {
		onPost(Pending{Question: q, Tool: tu.Name, Effects: tu.Effects})
	}

	select {
	case <-pq.done:
		return pq.res, nil
	case <-ctx.Done():
		r.mu.Lock()
		pq.waiters--
		// Take the question off the docket only when nobody is left waiting on it, and only
		// if it is still ours — an Answer may have landed and a later Resolve re-posted the
		// same id in between.
		dropped := pq.waiters == 0 && r.pending[q.ID] == pq
		if dropped {
			delete(r.pending, q.ID)
		}
		onDrop, onDropCause := r.onDrop, r.onDropCause
		r.mu.Unlock()
		// Announced outside the lock, and only when the question actually LEFT: a joiner
		// giving up while others still wait has taken nothing off the docket, and saying
		// otherwise would retire a question three people are still blocked on.
		switch {
		case dropped && onDropCause != nil:
			onDropCause(q, dropReason(ctx), context.Cause(ctx))
		case dropped && onDrop != nil:
			onDrop(q, dropReason(ctx))
		}
		return Resolution{Proceed: false, By: ByCancelled, Question: q}, ctx.Err()
	}
}

// dropReason renders why a waiter stopped waiting, from its context's cause. A cancel carrying
// no cause of its own is the plain case and gets the plain words; anything else said what it
// meant, and that is the sentence somebody reading the docket needs.
func dropReason(ctx context.Context) string {
	switch c := context.Cause(ctx); {
	case c == nil, errors.Is(c, context.Canceled):
		return "the asker stopped waiting for an answer"
	case errors.Is(c, context.DeadlineExceeded):
		return "the asker's deadline passed before this was answered"
	default:
		return c.Error()
	}
}

// ErrStaleEdit is an answer to an edit of an ask that has since been asked again.
var ErrStaleEdit = errors.New("the ask was asked again since")

// Answer resolves a pending question by ID, releasing EVERY caller waiting on it. It returns
// false for an unknown ID (already answered, cancelled, or never posted), and for an answer to an
// earlier edit of the question (Deliver says which). The original GateQuestion is preserved on
// the returned Resolution.
func (r *Registry) Answer(id string, res Resolution) bool { return r.Deliver(id, res) == nil }

// Deliver is Answer, saying why an answer was not delivered: ErrNoSuchDecision for an id nobody
// waits on, ErrStaleEdit for an answer naming an edit the question has moved past.
//
// THE REGISTRY IS WHERE A STALE EDIT IS REFUSED, because an answer is delivered here before the
// record is written: a check of the record alone would let an answer read before an edit and
// delivered after it through.
func (r *Registry) Deliver(id string, res Resolution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	pq, ok := r.pending[id]
	if !ok {
		return ErrNoSuchDecision
	}
	if res.Edit != 0 && res.Edit != pq.q.Edit {
		return ErrStaleEdit
	}
	delete(r.pending, id)
	res.Question = pq.q
	pq.res = res
	close(pq.done) // broadcast: one answer, however many joined the question
	return nil
}

// Revise replaces what a pending question asks, keeping whoever waits on it: a person's ask
// asked again. It reports false for an id nobody waits on.
func (r *Registry) Revise(q GateQuestion) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	pq, ok := r.pending[q.ID]
	if ok {
		pq.q = q
	}
	return ok
}

// Pending lists the currently-blocked questions (the read surface of what the
// running fleet is waiting on).
func (r *Registry) Pending() []Pending {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Pending, 0, len(r.pending))
	for _, pq := range r.pending {
		out = append(out, Pending{Question: pq.q, Tool: pq.tool, Effects: pq.effects})
	}
	return out
}

// WaiterCount reports how many callers are blocked on a question (0 if it is not pending).
//
// The count is the only observable that distinguishes one waiter from four — Pending() cannot,
// which is exactly the point of joining — and that distinction is what OnDrop turns on: a
// question is retired when the LAST waiter leaves, not the first.
func (r *Registry) WaiterCount(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if pq, ok := r.pending[id]; ok {
		return pq.waiters
	}
	return 0
}
