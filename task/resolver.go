package task

import "context"

// Resolver answers a blocking gate. It is late-bound and substitutable (model
// invariant #3): the boundary neither knows nor cares whether a human, another
// task, or a rule answers. This substitutability is what makes autonomy
// compositional — "autonomy agnostic" implies "supervisor agnostic."
type Resolver interface {
	Resolve(ctx context.Context, q GateQuestion, tu ToolUse) (Resolution, error)
}

// Resolution records how a blocking gate was answered — for attribution (every
// resolution is attributable) and to drive proceed/deny at the boundary.
type Resolution struct {
	Proceed  bool
	By       string // who resolved: "auto", "human:scott", "task:ci", ...
	Note     string
	Question GateQuestion
	// ChildMode: for a CallAgent ASK, how the child is supervised (default
	// ChildInheritGates). Ignored for non-CallAgent questions.
	ChildMode ChildMode
	// Steer: context/instructions the resolver folds into the task's conversation
	// at its next turn — a gate that *informs*, not just gates (e.g. an SCM
	// meta-task injecting org conventions, or a human adding guidance). Delivered
	// whether or not Proceed, so a denial can carry a "do this instead". Requires a
	// driver that holds a Conversation; ignored otherwise.
	Steer []string
}

// AutoResolver promotes every blocking gate to proceed. A vector of Ask gates
// resolved by an AutoResolver is a fully autonomous loop (all questions routed
// to proceed).
type AutoResolver struct{ Who string }

func (a AutoResolver) Resolve(_ context.Context, q GateQuestion, _ ToolUse) (Resolution, error) {
	return Resolution{Proceed: true, By: nonEmpty(a.Who, "auto"), Question: q}, nil
}

// ChannelResolver poses each blocking question on Out and waits for the answer on
// In — the present human at a console, or a parent task standing at a child's
// gate. It models liveness coming from a resolver actually acting, not from a
// notification: it blocks until answered or the context is cancelled. This is
// the in-the-loop resolver — every block has a resolver in attendance, so PARK
// never arises.
type ChannelResolver struct {
	Who string
	Out chan<- GateQuestion
	In  <-chan bool
}

func (c ChannelResolver) Resolve(ctx context.Context, q GateQuestion, _ ToolUse) (Resolution, error) {
	select {
	case c.Out <- q:
	case <-ctx.Done():
		return Resolution{Proceed: false, By: c.Who, Question: q}, ctx.Err()
	}
	select {
	case ok := <-c.In:
		return Resolution{Proceed: ok, By: c.Who, Question: q}, nil
	case <-ctx.Done():
		return Resolution{Proceed: false, By: c.Who, Question: q}, ctx.Err()
	}
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
