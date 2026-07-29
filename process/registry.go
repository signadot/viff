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
	onPost  func(Pending) // optional: notify a watcher when a question is posted
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
	done    chan struct{} // closed when answered
	res     Resolution    // valid once done is closed
	waiters int
}

// Pending is the read-surface view of one blocked question.
type Pending struct {
	Question GateQuestion
	Tool     string
}

func NewRegistry() *Registry {
	return &Registry{pending: map[string]*pendingQuestion{}}
}

// OnPost registers a callback invoked (outside the lock) whenever a question is
// posted — for a live read surface / attach stream. Optional.
func (r *Registry) OnPost(f func(Pending)) { r.onPost = f }

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
		pq = &pendingQuestion{q: q, tool: tu.Name, done: make(chan struct{})}
		r.pending[q.ID] = pq
	}
	pq.waiters++
	onPost := r.onPost
	r.mu.Unlock()
	// Only a NEW question is announced: a join adds a waiter to something already on the
	// docket, and re-announcing it would show a watcher a question appearing twice.
	if !joined && onPost != nil {
		onPost(Pending{Question: q, Tool: tu.Name})
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
		if pq.waiters == 0 && r.pending[q.ID] == pq {
			delete(r.pending, q.ID)
		}
		r.mu.Unlock()
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
		out = append(out, Pending{Question: pq.q, Tool: pq.tool})
	}
	return out
}

// waiterCount reports how many callers are blocked on a question (0 if it is not pending).
// It exists for tests that need to wait until every joiner has actually joined — the count is
// the only observable that says so, and Pending() cannot: one waiter and four look identical
// on the docket, which is exactly the point of joining.
func (r *Registry) waiterCount(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if pq, ok := r.pending[id]; ok {
		return pq.waiters
	}
	return 0
}
