package runner

import (
	"fmt"
	"github.com/signadot/verse/task"
	"slices"
	"strings"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
)

// Description is what a runner answers on GET /describe: what it offers. Verse reads it when the
// runner is registered, and whenever the runner restarts or reports a change, and keeps it with
// the registration.
//
// What an offer says about the world is its Systems: each system the offer intercepts for its
// tasks, and the ceiling over it. A SYSTEM OFFERED IS A SYSTEM INTERCEPTED: the runner performs
// or stops each of its calls for the policy to decide (`git`, `slack`, `files`, `commands`).
// What a run is set up with, its repositories and its network, is a fact of the launch's
// `with`, not a system. There is no list of steps beside it: a row of a launch's policy on a system the offer does
// not hold is refused, since nothing there could honour it. A task's start is no step: verse
// decides it before it launches the task.
type Description struct {
	Offers []Offer `tony:"field=offers"`
}

// Check is what a description must say for verse to register the runner: at least one offer,
// and each offer names the networks it builds, under `with.network`, by mode. A runner silent
// on its offers, or an offer silent on its network, would leave a spawn's network unchecked
// where the spawn is written.
func (d Description) Check() error {
	modes := []string{NetworkOpen, NetworkNone, NetworkAllow}
	if len(d.Offers) == 0 {
		return fmt.Errorf("it offers nothing; a runner offers an agent or scripts, each saying which of %s it builds under with.network",
			strings.Join(modes, ", "))
	}
	for _, o := range d.Offers {
		name := o.Agent
		if o.Script {
			name = "script"
		}
		a, ok := o.With["network"]
		if !ok || a.Any || len(a.Values) == 0 {
			return fmt.Errorf("offer %s: with.network names no network the runner builds; an offer says which of %s it builds",
				name, strings.Join(modes, ", "))
		}
		for _, v := range a.Values {
			if !slices.Contains(modes, v) {
				return fmt.Errorf("offer %s: with.network: %q is not a network mode (%s)", name, v, strings.Join(modes, ", "))
			}
		}
	}
	return nil
}

// Offer is one kind of doer a runner runs: an agent, by name, or scripts. An agent name is
// offered by exactly one registered runner, and only one runner offers scripts.
type Offer struct {
	// Agent is the agent's name, as a launch says it (`agent: verse`). Empty for scripts.
	Agent string `tony:"field=agent,omitzero"`
	// Script is set on the offer for scripts.
	Script bool `tony:"field=script,omitzero"`
	// Version is what the runner runs for this offer (a Claude Code version, say), and it goes
	// on the record of every task the offer runs.
	Version string `tony:"field=version,omitzero"`
	// With is the `with` values the offer accepts, by key: the doer's own parameters (`model`,
	// `effort`), and the networks the runner builds (`network`: none, open, allow). A key not
	// listed is not accepted. A system's facts (`with.git`) come with the
	// system, under Systems, not here.
	With map[string]Accepts `tony:"field=with,omitzero"`
	// Annotations is the annotations the offer accepts, by name, with the type of their value.
	Annotations map[string]string `tony:"field=annotations,omitzero"`
	// Systems are the systems the offer intercepts for its tasks, by name (package systems),
	// each with its ceiling. A system absent here is not intercepted for the offer: a launch
	// naming it in a row, or giving its facts, is refused.
	Systems map[string]System `tony:"field=systems,omitzero"`
	// Converse is whether a launch for this offer may ask for a conversation (Launch.Converse):
	// the agent hands the person the turn when it goes quiet. A script offer never does.
	Converse bool `tony:"field=converse,omitzero"`
}

// System is one system an offer intercepts, and the ceiling over it.
//
//tony:schemagen=verse-runner-system,notag
type System struct {
	// Ceiling is the operator's policy over the system's calls: `allow` and `deny` rows
	// (task.Policy, never `ask`), the last the default. It bounds what a launch's policy may
	// allow or ask about, and what a task may do: the runner reads it again at each act.
	Ceiling task.Policy `tony:"field=ceiling"`
}

// Accepts is the values a `with` key accepts: any value, which is passed through as is
// (`model: any`), or one of a list (`effort: [low, medium, high]`).
type Accepts struct {
	Any    bool
	Values []string
}

// AcceptsAny is the Accepts of a key whose values are passed through as is.
var AcceptsAny = Accepts{Any: true}

// AcceptsOneOf is the Accepts of a key that takes one of values.
func AcceptsOneOf(values ...string) Accepts { return Accepts{Values: values} }

// Accept reports whether a value is accepted.
func (a Accepts) Accept(v string) bool {
	return a.Any || slices.Contains(a.Values, v)
}

// ToTonyIR writes `any`, or the list of values.
func (a Accepts) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	if a.Any {
		return ir.FromString("any"), nil
	}
	vs := make([]*ir.Node, len(a.Values))
	for i, v := range a.Values {
		vs[i] = ir.FromString(v)
	}
	return ir.FromSlice(vs), nil
}

// FromTonyIR reads `any`, or a list of values.
func (a *Accepts) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	for node != nil && node.Type == ir.CommentType && len(node.Values) > 0 {
		node = node.Values[0]
	}
	switch {
	case node == nil:
		return fmt.Errorf("accepts: no value (want any, or a list of values)")
	case node.Type == ir.StringType && node.String == "any":
		*a = AcceptsAny
		return nil
	case node.Type == ir.ArrayType:
		out := Accepts{Values: make([]string, 0, len(node.Values))}
		for _, v := range node.Values {
			if v.Type != ir.StringType {
				return fmt.Errorf("accepts: a value must be a string, not %s", v.Type)
			}
			out.Values = append(out.Values, v.String)
		}
		*a = out
		return nil
	default:
		return fmt.Errorf("accepts: want any, or a list of values, not %s", node.Type)
	}
}
