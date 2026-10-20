package task

import "context"

// Resolver answers a blocking gate. It is late-bound and substitutable (model
// invariant #3): the boundary neither knows nor cares whether a human, another
// task, or a rule answers. This substitutability is what makes a policy
// compositional — "policy agnostic" implies "supervisor agnostic."
type Resolver interface {
	Resolve(ctx context.Context, q GateQuestion, tu ToolUse) (Resolution, error)
}

// Resolution records how a blocking gate was answered — for attribution (every
// resolution is attributable) and to drive proceed/deny at the boundary.
type Resolution struct {
	// Choice is the option chosen, by id. Proceed is the same fact asked as a yes-or-no
	// question, which is what the task loop asks of every gate it raises: every question a
	// runtime poses has the two default options, so `proceed` and `deny` are the only
	// choices it will see, and Proceed is Choice == "proceed". A question with options of
	// its own is settled by Choice, and Proceed there says only whether the chosen option
	// happened to be named proceed. Chosen reads whichever was set.
	Choice   string
	Proceed  bool
	By       string // who resolved: "auto", "user:walter@example.com", "via:verse.resolvers.review", ...
	Note     string
	Question GateQuestion
	// Steer: context/instructions the resolver folds into the task's conversation
	// at its next turn — a gate that *informs*, not just gates (e.g. an SCM
	// meta-task injecting org conventions, or a human adding guidance). Delivered
	// whether or not Proceed, so a denial can carry a "do this instead". Requires a
	// driver that holds a Conversation; ignored otherwise.
	Steer []string
	// Edit is the edit of the ask this answers, where the answerer named one (GateQuestion.Edit);
	// zero is the edit standing when it arrives.
	Edit int
	// Via is the party an answer came through, where By is whose answer it is. A resolver
	// answering for a party it serves (9ce3bva0, stage 4), `via:verse.resolvers.<name>`: the
	// ballot is the party's, given by the resolver they granted, and nothing authenticated them.
	// Or an io relaying a person it identified, `agent:slack-iod`: the ballot is the person's, who
	// the io's provider vouched for, and Authenticated says so. Empty for an answer somebody gave
	// as themselves. The two read apart by Authenticated.
	Via string
	// Authenticated is how the answerer was authenticated: the identity provider that verified
	// them, the provider whose io relayed them, or `asserted` for one who was whoever their
	// request said. Empty where nobody said: an answer a resolver gave for a party, whom nothing
	// authenticated, and one the runtime settled.
	Authenticated string
}

// Chosen is the option this resolution settled on: Choice when it was set, and the default
// option Proceed spells otherwise — so a resolution built by a caller that only knows yes or
// no reads the same as one that named its option.
func (r Resolution) Chosen() string {
	if r.Choice != "" {
		return r.Choice
	}
	if r.Proceed {
		return Proceed
	}
	return Deny
}

// Choose builds the resolution for one chosen option, with Proceed set to agree with it.
func Choose(choice, by string) Resolution {
	return Resolution{Choice: choice, Proceed: choice == Proceed, By: by}
}

// Cast reports whether this resolution is somebody's ballot: an answerer under a name of
// their own, rather than the runtime settling a question nobody answered.
func (r Resolution) Cast() bool {
	switch r.By {
	case "", ByAuto, ByPolicy, ByCancelled, ByDeadline:
		return false
	}
	return true
}

// AutoResolver answers every question proceed. Gates resolved by an AutoResolver make a fully
// autonomous loop.
type AutoResolver struct{ Who string }

func (a AutoResolver) Resolve(_ context.Context, q GateQuestion, _ ToolUse) (Resolution, error) {
	return Resolution{Choice: Proceed, Proceed: true, By: nonEmpty(a.Who, ByAuto), Question: q}, nil
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
