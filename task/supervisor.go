package task

import (
	"github.com/signadot/verse/systems"

	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"unicode"
)

// GateSep separates a GateQuestion's id from the id of whatever posed it — a task
// (`run-33c7~1`) or a trigger gate (`trigger:review:review~1`). Splitting on it is how a
// resolve request finds the owner to route to, so it must be a character an owner id can
// never contain (see safeSeg in the trigger package, which strips it out of subject-derived
// ids) and one that survives the surfaces an id crosses on the way back in:
//
//   - a URL path — '~' is RFC 3986 *unreserved*, so no conformant client percent-encodes it,
//     and it lands in /tasks/{id} as itself rather than opening a fragment the way '#' did
//   - an argv word — no shell expands or splits on a '~' that is not word-initial
//   - a tony/YAML scalar — '~' is only meaningful as a whole-value null, not inside a token
const GateSep = "~"

// GateQuestion is what a gate asks about a step: which gate, and why. It is the *question*, not
// the answer: it is handed to a Resolver, which replies with a Resolution (proceed or deny, who,
// and a note). A step no gate names isn't asked about, and goes ahead on its own.
type GateQuestion struct {
	ID     string // unique per posed question — addresses concurrent escalations
	Origin string // task that posed it (tree-path attribution)
	Gate   string // the policy's row that asks (Policy.NameOf), or a tool's name for a question it raises
	Reason string
	// Step is the step the question is about, for a question a task's gate asks. Empty for a
	// question that is not about a step: a rule's gate, an ask, a conversation's turn.
	Step Step
	// Pose is the question in words, as the gate wrote it and the firing expanded it
	// (trigger.Gate.Pose). Empty for a gate that says nothing, and for every question a task
	// raises of its own accord — then `Reason` is all there is, which is what it was before
	// anything could be said in words.
	Pose string
	// Subject is the entity the question is ABOUT — the PR, the change, the release.
	// Origin says who asked and Gate says which rule; neither says what is at stake, and
	// that is the thing an answerer needs. Empty when the question is not about an entity
	// (a bare `task run` has no subject).
	Subject string
	// Spawns names the task(es) a "yes" will start, for a gate raised BEFORE the spawn.
	// The id is derived from the trigger and subject, so it is settled by the time the
	// question is posed and can be published with it — which is what lets an approver
	// (or a script) line a gate up with the work it authorises, ahead of that work
	// existing. Empty when the action starts nothing, and note these are ids-to-be: a
	// denied gate, or a provider selection that resolves to no doer, creates none of them.
	Spawns string
	// Answerers is who the gate held the question out to, when it named them — grantees, the
	// way a grant names them: a principal, a kind, a group's address, or PrincipalAnswerer for
	// whom the task acts for. Empty leaves the question to the docket's default. Written as the
	// question's `read-write` grant when it is posed.
	Answerers []string
	// At is the ADDRESS of the charter whose rule is asking — `verse-dev.merge` — empty for
	// baseline and for a question a task raised (a task knows what it is doing, not which
	// charter spawned it). Gate above names the rule, and a rule's name is unique within a
	// charter rather than across them, so without this a docket entry names a `review` and
	// leaves the reader to work out which package's.
	At string
	// Scope is the copy-on-write scope the asker acts in — "" for baseline. A question is published as
	// a record, and a record goes in a store; without this, one registry serving baseline and
	// every scoped engine has nothing to route by and publishes them all to baseline. The
	// docket a reader sees is then not the world a reader sees: a question about an entity
	// only one dev can read, on everybody's docket.
	//
	// Not written into the record — the store it lands in already says which scope it
	// belongs to, the way a task's instance record does.
	Scope string
	// Options are the outcomes this question can settle on, each with the number of ballots
	// that settles it. Empty means the two every gate has always had, `proceed` and `deny`,
	// each settled by one ballot — so a producer that says nothing poses exactly the
	// question it posed before options existed.
	//
	// Ordered: the first option to reach its threshold settles the question, and a record
	// whose options are not the default two is answered by option id rather than by yes or
	// no. See DefaultOptions and Need.
	Options []Option
	// Deadline bounds how long the question stands. When Within passes with no option at
	// its threshold, the question settles on Default with `by: deadline`. An empty Default
	// settles on NoOption: the clock decided, and it decided nothing, which a caller reads
	// as a refusal that nobody made (a provider selection reads it as no pick).
	Deadline *Deadline
	// Chain is the kpath of the ACTION this question is holding up, for a question a spawn
	// asks on its own behalf — which doer runs it. A gate's question is the firing's and
	// carries none: the firing comes back by the rule being evaluated again, where a
	// selection opens after the action has written its latch, so nothing re-fires it, and the
	// chain is what a restart navigates back through to re-enter it.
	Chain string
	// Edit is which edit of a person's ask this is: 1 as first asked, and one more each time
	// its asker changes what it asks. An answer is consent to what was asked, so it names the
	// edit it answered, and one to an earlier edit is refused. Zero for a rule's gate and a
	// task's question, which nobody edits.
	Edit int
	// Labels are what the question says about itself for whoever watches asks: `team: payments`,
	// `kind: deploy`. A convention between the raiser and the resolvers matching on them; verse
	// reads nothing into a label, and routes nothing by one.
	Labels map[string]string
}

// CheckLabels refuses a label a match could not name: an empty name, or one holding a space.
func CheckLabels(labels map[string]string) error {
	for k := range labels {
		if k == "" || strings.ContainsFunc(k, unicode.IsSpace) {
			return fmt.Errorf("labels: %q: a label's name is a word, with no space in it", k)
		}
	}
	return nil
}

// The `by` of a settle that nobody made. A person or a resolver answers under a name of
// their own; these are the runtime's, and none of them casts a ballot.
const (
	ByAuto      = "auto"      // the policy allowed the step, so it went ahead without asking
	ByPolicy    = "policy"    // the policy denied the step, so nobody was asked
	ByCeiling   = "ceiling"   // the runner's ceiling refused the step, so nobody was asked
	ByCancelled = "cancelled" // the wait ended before anyone answered
	ByDeadline  = "deadline"  // the clock passed with no decision
)

// The two options every question has unless it says otherwise.
const (
	Proceed = "proceed"
	Deny    = "deny"
	// Reply and End are the options of a question that wants WORDS: an `ask` the model poses,
	// or the turn the runtime hands a person in a conversation. Reply carries the words as the
	// answer's say; End is the person ending the conversation.
	Reply = "reply"
	End   = "end"
)

// DefaultOptions is the question a gate poses when it names no options: yes or no, each
// settled by one ballot.
func DefaultOptions() []Option {
	return []Option{{ID: Proceed, Need: 1}, {ID: Deny, Need: 1}}
}

// OptionsOrDefault is the question's options, or the default two when it names none.
func (d GateQuestion) OptionsOrDefault() []Option {
	if len(d.Options) == 0 {
		return DefaultOptions()
	}
	return d.Options
}

// Need reports how many ballots settle `choice` among opts, and whether the choice is one of
// them at all. A threshold below one reads as one: an option nobody has to choose is not an
// option, and a zero here is a producer that left the field empty rather than one that asked
// for nothing.
func Need(opts []Option, choice string) (int, bool) {
	for _, o := range opts {
		if o.ID == choice {
			if o.Need < 1 {
				return 1, true
			}
			return o.Need, true
		}
	}
	return 0, false
}

// OptionIDs lists the option ids in order, for a refusal that names what could have been
// chosen.
func OptionIDs(opts []Option) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.ID
	}
	return out
}

// Supervisor asks the task's policy about each step before it goes ahead. Its policy can be
// replaced while the task runs, and a sub-agent's supervisor shares it, so a change reaches the
// sub-agents too.
type Supervisor struct {
	policy   *atomic.Pointer[Policy]
	ceilings map[string]Policy // the runner's, by system: read after the policy, at each step
	resolver Resolver
	stepOf   func(ToolUse) (Step, bool) // StepOf; a test's own tools map to steps its way
	origin   string                     // task id, stamped onto each GateQuestion
	dc       uint64                     // question counter (one Check goroutine per Supervisor)
}

func NewSupervisor(p Policy, r Resolver) *Supervisor {
	s := &Supervisor{policy: &atomic.Pointer[Policy]{}, resolver: r, stepOf: StepOf}
	p = p.OrDefault()
	s.policy.Store(&p)
	return s
}

// child is a sub-agent's supervisor: the same policy, the same resolver.
func (s *Supervisor) child() *Supervisor {
	return &Supervisor{policy: s.policy, ceilings: s.ceilings, resolver: s.resolver, stepOf: s.stepOf}
}

// SetCeilings gives the supervisor the runner's ceilings, by system (runner.Offer.Systems): THE
// RUNNER'S OWN LINE, read at each step after the policy, whatever the policy said and whoever
// answered its questions. A step a ceiling's `deny` row matches is refused with the row named,
// and nobody is asked. Its sub-agents' supervisors share them.
func (s *Supervisor) SetCeilings(c map[string]Policy) { s.ceilings = c }

// SetStepOf sets which step an action is, for a driver whose tools are steps StepOf doesn't
// know: a runtime's own tools (a push is git.push). Its sub-agents' supervisors share it.
func (s *Supervisor) SetStepOf(f func(ToolUse) (Step, bool)) { s.stepOf = f }

// SetOrigin stamps this supervisor's questions with a task id (attribution).
func (s *Supervisor) SetOrigin(id string) { s.origin = id }

// Policy is the policy the next step is decided under.
func (s *Supervisor) Policy() Policy { return *s.policy.Load() }

// SetPolicy replaces the policy, from the next step on.
func (s *Supervisor) SetPolicy(p Policy) {
	p = p.OrDefault()
	s.policy.Store(&p)
}

// Check asks before an action, if it is a step a gate names (StepOf). It returns how the action
// was let through or held. This is the one call the loop makes before executing each action.
func (s *Supervisor) Check(ctx context.Context, tu ToolUse) (Resolution, error) {
	st, ok := s.stepOf(tu)
	if !ok {
		return Resolution{Choice: Proceed, Proceed: true, By: ByAuto, Question: GateQuestion{
			Origin: s.origin, Reason: "not a step"}}, nil
	}
	return s.Ask(ctx, st, tu)
}

// Ask decides a step by the policy: a verb the policy does not provide is refused, and
// otherwise the row that matches it allows it, denies it with its name and nobody asked, or
// asks, and the answer decides.
func (s *Supervisor) Ask(ctx context.Context, st Step, tu ToolUse) (Resolution, error) {
	p := *s.policy.Load()
	row, i := p.Decide(st)
	// A withhold holds wherever it stands: a row above it does not bring the verb back.
	if w, at, ok := p.withholding(st.Name); ok {
		row, i = w, at
	}
	name := p.NameOf(i)
	s.dc++
	q := row.question(name, st)
	q.Step, q.Origin = st, s.origin
	q.ID = fmt.Sprintf("%s%sd%d", s.origin, GateSep, s.dc)
	switch row.Then.Mode {
	case ThenDeny:
		q.Reason = "denied by " + name
		return Resolution{Choice: Deny, By: ByPolicy, Note: "denied by the policy's row " + name + ": " + st.String(), Question: q}, nil
	case ThenWithhold:
		// A withheld verb has no tool, so a call of it is a driver's mistake; refused as a
		// deny is, in the row's name.
		q.Reason = "withheld by " + name
		return Resolution{Choice: Deny, By: ByPolicy, Note: "withheld by the policy's row " + name + ": " + st.String(), Question: q}, nil
	}
	// A verb no row names is not the task's: it has no tool, and the default row does not
	// decide it.
	if !p.Provides(st.Name) {
		q.Reason = "not provided: no row of the policy names " + st.Name
		return Resolution{Choice: Deny, By: ByPolicy, Note: "not provided by the policy, which names no " + st.Name + ": " + st.String(), Question: q}, nil
	}
	if system, _, ok := systems.Split(st.Name); ok {
		if c, has := s.ceilings[system]; has {
			if crow, at := c.Decide(st); crow.Then.Mode == ThenDeny {
				q.Reason = "refused by the runner's ceiling, its row " + c.NameOf(at)
				return Resolution{Choice: Deny, By: ByCeiling, Note: "refused by the runner's ceiling, its row " +
					c.NameOf(at) + " for " + system + ": " + st.String(), Question: q}, nil
			}
		}
	}
	if row.Then.Mode == ThenAllow {
		q.Reason = "allowed by " + name
		return Resolution{Choice: Proceed, Proceed: true, By: ByAuto, Question: q}, nil
	}
	return s.resolver.Resolve(ctx, q, tu)
}
