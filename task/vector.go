package task

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// Vector is the ordered gate vector evaluated at each boundary. It is data, not
// structure (model invariant #2): a Supervisor can swap it on a running
// task without restart, which is how autonomy slides.
//
//tony:schemagen=verse-vector,notag
type Vector struct {
	Gates   []Gate `tony:"field=gates,omitzero"`
	Default Mode   `tony:"field=default"` // mode when no gate matches (a permissive baseline; hard scope/deny is a separate layer)
}

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

// GateQuestion is what evaluating the vector against an action poses: the mode the
// gates determine (auto/ask/park), which gate was decisive, and why. It is the
// *question*, not the answer — when it blocks it is handed to a Resolver, which
// replies with a Resolution (proceed/deny + who + note). An auto GateQuestion needs
// no resolver and proceeds on its own.
type GateQuestion struct {
	ID     string // unique per posed question — addresses concurrent escalations
	Origin string // task that posed it (tree-path attribution)
	Mode   Mode
	Gate   string // name of the deciding gate, or "" for the default
	Reason string
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
	// Boundto is the resolver BINDING that owns this question — `resolvers:<kind>:<name>` —
	// empty when nobody is bound and the question belongs to whoever reads the docket.
	//
	// The binding rather than the live instance, because a question is owned by what was
	// declared and answered by whatever is currently watching. The instance is derivable from
	// the two (binding, origin) and would be a second spelling of a pair already here.
	BoundTo string
	// At is the ADDRESS of the charter whose rule is asking — `verse-dev.merge` — empty for
	// baseline and for a question a task raised (a task knows what it is doing, not which
	// charter spawned it). Gate above names the rule, and a rule's name is unique within a
	// charter rather than across them, so without this a docket entry names a `review` and
	// leaves the reader to work out which package's.
	At string
	// Scope is the federation the asker acts in — "" for baseline. A question is published as
	// a record, and a record goes in a store; without this, one registry serving baseline and
	// every scoped engine has nothing to route by and publishes them all to baseline. The
	// docket a reader sees is then not the world a reader sees: a question about an entity
	// only one dev can read, on everybody's docket.
	//
	// Not written into the record — the store it lands in already says which federation it
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
}

// Deadline is how long a question stands, and what it settles on when the clock passes.
//
//tony:schemagen=verse-deadline,notag
type Deadline struct {
	Within  string `tony:"field=within"`
	Default string `tony:"field=default,omitzero"`
}

// After is the deadline's duration.
func (d *Deadline) After() (time.Duration, error) {
	if d == nil {
		return 0, nil
	}
	return time.ParseDuration(d.Within)
}

// The `by` of a settle that nobody made. A person or a resolver answers under a name of
// their own; these are the runtime's, and none of them casts a ballot.
const (
	ByAuto      = "auto"      // the vector let the action through without asking
	ByCancelled = "cancelled" // the wait ended before anyone answered
	ByDeadline  = "deadline"  // the clock passed with no decision
)

// NoOption is what a deadline settles on when it was given no default: the question closed
// with nothing chosen. It is never an option a ballot can name.
const NoOption = "none"

// Option is one outcome a question can settle on, and how many ballots settle it.
//
//tony:schemagen=verse-option,notag
type Option struct {
	ID   string `tony:"field=id"`
	Need int    `tony:"field=need,omitzero"`
	// Grant and Tools are the ENVELOPE this option would run under, for a question whose
	// options are doers. Once options differ in what they may do, choosing between two names
	// is choosing between two authorities, and a ballot that showed only names would have the
	// room authorise a capability nobody put in front of them. Empty for an option that is
	// not a doer, or a doer that adds nothing to the spawn.
	Grant string   `tony:"field=grant,omitzero"`
	Tools []string `tony:"field=tools,omitzero"`
}

// The two options every question has unless it says otherwise.
const (
	Proceed = "proceed"
	Deny    = "deny"
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

// Evaluate returns the most restrictive matching gate's mode (Park > Ask >
// Auto). Because gates only raise restrictiveness, tightening any gate toward
// Ask/Park is exactly "sliding autonomy down."
//
// The deciding gate's question rides with its mode: the options it settles on and the
// clock it stands under are the gate's to say, and a question that lost them here would be
// posed as yes-or-no however the vector was written.
func (v *Vector) Evaluate(tu ToolUse) GateQuestion {
	d := GateQuestion{Mode: v.Default, Gate: "", Reason: "no gate matched"}
	for _, g := range v.Gates {
		ok, reason := g.Matches(tu)
		if !ok {
			continue
		}
		if g.Mode >= d.Mode {
			d = GateQuestion{Mode: g.Mode, Gate: g.Name, Reason: reason,
				Options: g.options(), Deadline: g.Deadline}
		}
	}
	return d
}

// Supervisor holds a live-mutable gate vector behind an atomic pointer and
// binds a resolver. Rewriting the vector mid-run is the "slide autonomy on a
// running task" operation; swapping the resolver is supervisor-agnosticity
// — a human or another task answering the same gates through one interface.
type Supervisor struct {
	vec      atomic.Pointer[Vector]
	resolver atomic.Pointer[resolverBox]
	deriver  Deriver
	origin   string // task id, stamped onto each GateQuestion
	boundTo  string // resolver binding that owns this task's questions, if any
	dc       uint64 // question counter (one Check goroutine per Supervisor)
}

type resolverBox struct{ r Resolver }

func NewSupervisor(v *Vector, r Resolver) *Supervisor {
	s := &Supervisor{deriver: BasicDeriver{}}
	s.vec.Store(v)
	s.resolver.Store(&resolverBox{r})
	return s
}

// SetVector slides autonomy on the running task — no restart.
func (s *Supervisor) SetVector(v *Vector) { s.vec.Store(v) }

// SetResolver rebinds who answers blocking gates (human ⇄ agent ⇄ rule).
func (s *Supervisor) SetResolver(r Resolver) { s.resolver.Store(&resolverBox{r}) }

// SetDeriver swaps the capability-classification step.
func (s *Supervisor) SetDeriver(d Deriver) { s.deriver = d }

// Derive classifies an action into typed effects, in place — the step Check does first,
// exposed because the loop must classify BEFORE it can check the action against the hard
// envelope, and the envelope is refused rather than asked about (so it cannot ride inside
// Check, which routes to a resolver). Deriving is idempotent — a deriver returns early
// once an action carries effects — so the Check that follows does not repeat it.
func (s *Supervisor) Derive(tu *ToolUse) {
	if s.deriver != nil {
		s.deriver.Derive(tu)
	}
}

// SetOrigin stamps this supervisor's questions with a task id (attribution).
func (s *Supervisor) SetOrigin(id string) { s.origin = id }

// SetBoundTo names the resolver binding that owns this task's questions. Set at spawn from
// the launch, before the loop runs; empty means nobody is bound and the questions belong to
// whoever reads the docket.
func (s *Supervisor) SetBoundTo(ref string) { s.boundTo = ref }

// Vector returns the current gate vector (for read-surface display).
func (s *Supervisor) Vector() *Vector { return s.vec.Load() }

// Resolver returns the currently bound resolver (used to hand a child the
// parent's resolver under the inherit/careful child modes).
func (s *Supervisor) Resolver() Resolver { return s.resolver.Load().r }

// Check derives the action's capabilities, evaluates the boundary, and — if
// the question blocks — routes it to the bound resolver. It returns how the
// boundary was resolved. This is the one call the owned in-the-loop loop makes
// before executing each action.
func (s *Supervisor) Check(ctx context.Context, tu ToolUse) (Resolution, error) {
	if s.deriver != nil {
		s.deriver.Derive(&tu)
	}
	d := s.vec.Load().Evaluate(tu)
	s.dc++
	d.Origin = s.origin
	d.BoundTo = s.boundTo
	d.ID = fmt.Sprintf("%s%sd%d", s.origin, GateSep, s.dc)
	if !d.Mode.Blocks() {
		return Resolution{Choice: Proceed, Proceed: true, By: ByAuto, Question: d}, nil
	}
	// A delegation's question offers the child modes, unless the gate wrote outcomes of its
	// own. Here rather than on the gate because it follows from the ACTION: a gate matching
	// `verbs: [call_agent]` under any name is asking how the child is supervised, and that
	// is a choice the answerer has to be able to see.
	if tu.CallAgent != nil && !named(d.Options) {
		need, _ := Need(d.OptionsOrDefault(), Proceed)
		d.Options = DelegateOptions(need)
	}
	return s.resolver.Load().r.Resolve(ctx, d, tu)
}

// named reports whether a question's options are outcomes the gate wrote, as opposed to
// none or the default two at some threshold.
func named(opts []Option) bool {
	for _, o := range opts {
		if o.ID != Proceed && o.ID != Deny {
			return true
		}
	}
	return false
}
