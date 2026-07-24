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

type pendingQuestion struct {
	q      GateQuestion
	tool   string
	result chan Resolution
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

// Resolve (the Resolver interface) posts the question and blocks until it is
// answered by ID or the context is cancelled.
func (r *Registry) Resolve(ctx context.Context, q GateQuestion, tu ToolUse) (Resolution, error) {
	pq := &pendingQuestion{q: q, tool: tu.Name, result: make(chan Resolution, 1)}
	r.mu.Lock()
	r.pending[q.ID] = pq
	onPost := r.onPost
	r.mu.Unlock()
	if onPost != nil {
		onPost(Pending{Question: q, Tool: tu.Name})
	}
	select {
	case res := <-pq.result:
		return res, nil
	case <-ctx.Done():
		r.mu.Lock()
		delete(r.pending, q.ID)
		r.mu.Unlock()
		return Resolution{Proceed: false, By: "cancelled", Question: q}, ctx.Err()
	}
}

// Answer resolves a pending question by ID. It returns false for an unknown ID
// (already answered, cancelled, or never posted). The original GateQuestion is
// preserved on the returned Resolution.
func (r *Registry) Answer(id string, res Resolution) bool {
	r.mu.Lock()
	pq, ok := r.pending[id]
	if ok {
		delete(r.pending, id)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	res.Question = pq.q
	pq.result <- res // buffered(1): never blocks, even if Resolve raced to ctx.Done
	return true
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
