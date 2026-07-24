package process

import (
	"context"
	"fmt"
	"sync/atomic"
)

// Vector is the ordered gate vector evaluated at each boundary. It is data, not
// structure (model invariant #2): a Supervisor can swap it on a running
// process without restart, which is how autonomy slides.
type Vector struct {
	Gates   []Gate
	Default Mode // mode when no gate matches (a permissive baseline; hard scope/deny is a separate layer)
}

// GateQuestion is what evaluating the vector against an action poses: the mode the
// gates determine (auto/ask/park), which gate was decisive, and why. It is the
// *question*, not the answer — when it blocks it is handed to a Resolver, which
// replies with a Resolution (proceed/deny + who + note). An auto GateQuestion needs
// no resolver and proceeds on its own.
type GateQuestion struct {
	ID     string // unique per posed question — addresses concurrent escalations
	Origin string // process that posed it (tree-path attribution)
	Mode   Mode
	Gate   string // name of the deciding gate, or "" for the default
	Reason string
}

// Evaluate returns the most restrictive matching gate's mode (Park > Ask >
// Auto). Because gates only raise restrictiveness, tightening any gate toward
// Ask/Park is exactly "sliding autonomy down."
func (v *Vector) Evaluate(tu ToolUse) GateQuestion {
	d := GateQuestion{Mode: v.Default, Gate: "", Reason: "no gate matched"}
	for _, g := range v.Gates {
		ok, reason := g.Matches(tu)
		if !ok {
			continue
		}
		if g.Mode >= d.Mode {
			d = GateQuestion{Mode: g.Mode, Gate: g.Name, Reason: reason}
		}
	}
	return d
}

// Supervisor holds a live-mutable gate vector behind an atomic pointer and
// binds a resolver. Rewriting the vector mid-run is the "slide autonomy on a
// running process" operation; swapping the resolver is supervisor-agnosticity
// — a human or another process answering the same gates through one interface.
type Supervisor struct {
	vec      atomic.Pointer[Vector]
	resolver atomic.Pointer[resolverBox]
	deriver  Deriver
	origin   string // process id, stamped onto each GateQuestion
	dc       uint64 // question counter (one Check goroutine per Supervisor)
}

type resolverBox struct{ r Resolver }

func NewSupervisor(v *Vector, r Resolver) *Supervisor {
	s := &Supervisor{deriver: BasicDeriver{}}
	s.vec.Store(v)
	s.resolver.Store(&resolverBox{r})
	return s
}

// SetVector slides autonomy on the running process — no restart.
func (s *Supervisor) SetVector(v *Vector) { s.vec.Store(v) }

// SetResolver rebinds who answers blocking gates (human ⇄ agent ⇄ rule).
func (s *Supervisor) SetResolver(r Resolver) { s.resolver.Store(&resolverBox{r}) }

// SetDeriver swaps the capability-classification step.
func (s *Supervisor) SetDeriver(d Deriver) { s.deriver = d }

// SetOrigin stamps this supervisor's questions with a process id (attribution).
func (s *Supervisor) SetOrigin(id string) { s.origin = id }

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
	d.ID = fmt.Sprintf("%s#d%d", s.origin, s.dc)
	if !d.Mode.Blocks() {
		return Resolution{Proceed: true, By: "auto", Question: d}, nil
	}
	return s.resolver.Load().r.Resolve(ctx, d, tu)
}
