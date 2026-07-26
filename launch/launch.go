// Package launch is the wire form of an action: a declarative, serializable
// description of a process to launch when a trigger fires — the "action as data",
// not a Go closure. This is what lets an action take the deployment shape: a Spec
// can cross a network to a process runtime (processd), be persisted, or be
// reflected as an entity (triggers-as-data). Nothing here depends on the process
// substrate; it is pure data + binding.
//
// Every field is a *reference* to something named elsewhere (a policy, a grant, an
// environment) rather than the thing inlined — the same index-not-warehouse
// discipline as the rest of the verse: the spec points at native semantics; the
// runtime that builds the process resolves the references against its own
// registries. It maps onto the process substrate's axes:
//
//	Driver      → which runtime/Driver builds the process (LLM harness, scripted, …)
//	Task        → the prompt/task the driver is seeded with
//	Policy      → the gate vector (the autonomy dial) — soft, slidable
//	Resolver    → how ASK gates are answered (human/auto/scm/…)
//	Grant       → hard authority — what it may do at all
//	Environment → the sandbox/workspace floor — unbypassable
//
// The authority axis is the GRANT, not the "scope": "scope" is taken everywhere else in
// verse for the entity-store isolation overlay (entity.Store.Scope, config.Scope, --scope,
// VERSE_SCOPE, connect's nonce), and this field is the one place it meant authority. The
// word the type (process.Grant) and the delegation check (Grant.Subset) already use is the
// right one. See docs/expose.md.
package launch

//go:generate go tool tony-codegen -dir .

import (
	"sort"
	"strings"
)

// Spec is the declarative launch descriptor. It is a *template*: string fields may
// contain $vars (e.g. "$subject") that Bind expands against a concrete event,
// turning a generic action into a specific launch. All fields are optional; a
// runtime supplies defaults for any left blank.
//
//tony:schemagen=verse-launch-spec,notag
type Spec struct {
	Driver      string `tony:"field=driver,omitzero"`
	Task        string `tony:"field=task,omitzero"`
	Policy      string `tony:"field=policy,omitzero"`
	Resolver    string `tony:"field=resolver,omitzero"`
	Grant       string `tony:"field=grant,omitzero"`
	Environment string `tony:"field=environment,omitzero"`
	// Subject is the flow this launch belongs to (an entity ref, e.g. a PR). The
	// engine tracks a flow's processes by subject so it can be steered/cancelled as
	// a unit. Usually the concrete matched ref, not a template.
	Subject string `tony:"field=subject,omitzero"`
	// Chain is the kpath of the ACTION that spawned this, inside its trigger: "review" for
	// the trigger's own spawn, "review.onDone" for its completion hook. Engine-owned like
	// Subject — a charter that writes it is overwritten — and carried here only because the
	// Spawner is what calls mirror.Reflect, which owns the process entity's payload. It is
	// what lets a process say where in its trigger's action tree it came from.
	Chain string `tony:"field=chain,omitzero"`
	// Provider, when set, makes the who-axis (Driver) a GOVERNED SELECTION rather than a
	// fixed authoring-time value: the engine selects one of Eligible via the Select
	// surface, reifies it as a motion, and binds the winner into Driver before spawn
	// (no live swap — reassign = cancel+respawn). Orthogonal to the controlPolicy that
	// then transports the chosen Driver.
	Provider *Provider `tony:"field=provider,omitzero"`
}

// Provider declares the who-axis as a governed 1-of-N selection: the eligible doers (a
// hard set, like Grant) and the surface that selects among them (the soft who-dial, like
// Policy). Absent = today's fixed Driver.
//
// Eligibility takes one of two forms: an inline `Eligible` list of doer ids (simple), or
// the registry form `Capability` (+ optional `Where`) which selects the `provider:<cap>:*`
// entities whose attributes match — attribute-rich, discoverable doers resolved at fire
// time. A matched provider entity carries the `driver` bound into the spawn.
//
//tony:schemagen=verse-launch-provider,notag
type Provider struct {
	Eligible   []string       `tony:"field=eligible,omitzero"`   // inline doer ids (option == driver)
	Capability string         `tony:"field=capability,omitzero"` // registry: select provider:<capability>:*
	Match      map[string]any `tony:"field=match,omitzero"`      // registry: a tony match over provider attributes
	// Select is the who-dial: how the doer is picked among the eligible options —
	// "auto" (a rule picks the first eligible), "ask" (one party picks; a threshold of 1),
	// or "vote" (the group picks; ≥ Need). Empty defaults to auto. It is the who-axis
	// counterpart of a whether-gate's Formula; both compile to the same governed motion.
	Select string `tony:"field=select,omitzero"`
	Need   int    `tony:"field=need,omitzero"`   // vote: plurality threshold
	Within string `tony:"field=within,omitzero"` // vote/ask: selection deadline (e.g. "4h") — no pick by then = no spawn
}

// bind expands $vars in the template-ish fields (eligible ids, capability). Select/Need
// and the Match pattern are carried as-is (the engine evaluates Match as a tony match
// over provider entities via tony.Match; $var expansion inside a match is future work).
func (p *Provider) bind(b Binding) *Provider {
	if p == nil {
		return nil
	}
	np := &Provider{Select: p.Select, Need: p.Need, Within: p.Within, Capability: expand(p.Capability, b), Match: p.Match}
	for _, e := range p.Eligible {
		np.Eligible = append(np.Eligible, expand(e, b))
	}
	return np
}

// Binding maps template variable names to concrete values — built when a trigger
// fires ("subject" → the matched entity's ref). It is the generic→specific bind.
type Binding map[string]string

// Bind expands $vars in every string field of the spec against b, returning the
// instantiated (specific) spec. Unbound $vars are left as-is.
func (s Spec) Bind(b Binding) Spec {
	return Spec{
		Driver:      expand(s.Driver, b),
		Task:        expand(s.Task, b),
		Policy:      expand(s.Policy, b),
		Resolver:    expand(s.Resolver, b),
		Grant:       expand(s.Grant, b),
		Environment: expand(s.Environment, b),
		Subject:     expand(s.Subject, b),
		Provider:    s.Provider.bind(b),
	}
}

// Expand substitutes $vars in s from b — exported for callers building strings
// (e.g. a target ref) from a binding, outside a Spec.
func Expand(s string, b Binding) string { return expand(s, b) }

// expand replaces each "$name" with b[name]. Longer names are substituted first so
// "$subject" is not shadowed by a shorter "$sub" binding. This is a v0 stand-in for
// the richer reference/derivation grammar ("$subject.pr" via a deref) noted for
// later — deliberately trivial, since the "when"/binding is a complexity sink.
func expand(s string, b Binding) string {
	if s == "" || !strings.Contains(s, "$") {
		return s
	}
	keys := make([]string, 0, len(b))
	for k := range b {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		s = strings.ReplaceAll(s, "$"+k, b[k])
	}
	return s
}
