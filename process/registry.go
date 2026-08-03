package process

import (
	"context"
	"sync"
)

// Registry is a question-addressed Resolver: blocking Resolve calls from any
// number of concurrent processes register a pending question keyed by
// GateQuestion.ID; an external caller (a human, an attach client, a parent) answers
// them by ID via Answer. This is the seam that makes concurrent escalation from
// parallel children coherent — each pending question carries its own ID and
// Origin, and resolutions are addressed, never mismatched. A single Registry
// shared as the resolver of a subtree collects the whole subtree's escalations.
type Registry struct {
	mu      sync.Mutex
	pending map[string]*pendingQuestion
	onPost  func(Pending)      // optional: notify a watcher when a question is posted
	onDrop  func(GateQuestion) // optional: notify a watcher when one leaves unanswered
}

// pendingQuestion is one question standing on the docket, and everyone waiting on it. The
// answer is broadcast by closing `done` rather than sent on a channel, because a question may
// have several waiters: a re-fire about the same subject poses the SAME id, and joining the
// standing question is what makes that one decision instead of two.
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
// the last caller waiting on it was cancelled.
//
// It exists because OnPost had none. A publisher was told about every question that arrived
// and about none that left, so a question the registry silently discarded stayed published as
// `pending` — offered on a docket, answerable-looking, and undeliverable — until something
// else happened to notice.
//
// Only Answer's counterpart is missing here by design: an answered question is announced by
// the answer itself, which the answerer already has.
func (r *Registry) OnDrop(f func(GateQuestion)) { r.onDrop = f }

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
		onDrop := r.onDrop
		r.mu.Unlock()
		// Announced outside the lock, and only when the question actually LEFT: a joiner
		// giving up while others still wait has taken nothing off the docket, and saying
		// otherwise would retire a question three people are still blocked on.
		if dropped && onDrop != nil {
			onDrop(q)
		}
		return Resolution{Proceed: false, By: "cancelled", Question: q}, ctx.Err()
	}
}

// Answer resolves a pending question by ID, releasing EVERY caller waiting on it. It returns
// false for an unknown ID (already answered, cancelled, or never posted). The original
// GateQuestion is preserved on the returned Resolution.
func (r *Registry) Answer(id string, res Resolution) bool {
	r.mu.Lock()
	pq, ok := r.pending[id]
	if ok {
		delete(r.pending, id)
		res.Question = pq.q
		pq.res = res
		close(pq.done) // broadcast: one answer, however many joined the question
	}
	r.mu.Unlock()
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
