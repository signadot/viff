package runner

import (
	"fmt"
	"slices"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
)

// Description is what a runner answers on GET /describe: what it offers. Verse reads it when the
// runner is registered, and whenever the runner restarts or reports a change, and keeps it with
// the registration.
//
// The steps verse controls (task.start, repos.push, repos.pr) and repos aren't listed: every
// runner supports them, as part of the runner contract.
type Description struct {
	Offers []Offer `tony:"field=offers"`
}

// Offer is one kind of doer a runner runs: an agent, by name, or scripts. An agent name is
// offered by exactly one registered runner, and only one runner offers scripts.
type Offer struct {
	// Agent is the agent's name, as a spawn says it (`agent: verse`). Empty for scripts.
	Agent string `tony:"field=agent,omitzero"`
	// Script is set on the offer for scripts.
	Script bool `tony:"field=script,omitzero"`
	// Version is what the runner runs for this offer (a Claude Code version, say), and it goes
	// on the record of every task the offer runs.
	Version string `tony:"field=version,omitzero"`
	// With is the `with` values the offer accepts, by key. A key not listed is not accepted.
	With map[string]Accepts `tony:"field=with,omitzero"`
	// Intercepts is the steps the offer can stop before they happen, so a gate may name them:
	// files.write, commands.run, network.fetch.
	Intercepts []string `tony:"field=intercepts,omitzero"`
	// Annotations is the annotations the offer accepts, by name, with the type of their value.
	Annotations map[string]string `tony:"field=annotations,omitzero"`
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
