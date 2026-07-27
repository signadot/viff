package process

import (
	"fmt"
	"sort"
	"strings"
)

// The authority axes by NAME — the vocabulary a spawn's `vector:` and `grant:` resolve
// against, and the hard check that finally gives a grant a consumer.
//
// Both fields have been on launch.Spec since it was written: declared, parsed, persisted,
// expanded by Bind, and rendered back by `trigger show`. Nothing read either one. A charter
// that said `grant: read-only` got an agent that ran shell commands, because the string had
// no vocabulary to resolve against and no enforcement point to reach if it had. That is the
// worst state an authority field can be in — it reads back correctly, so you believe it —
// and it is the state docs/roadmap/authority.md describes at lines 70-72.
//
// What is implemented here is the OVERRIDE half of that note (its "The override" section):
// an explicitly declared vector or grant is honored. The derived envelope — inferring
// authority from the request text — is deliberately not here. The note's own risk section
// says why: a trigger's task is a template whose $vars expand from an entity payload, so
// anyone who can write a field could widen an envelope by writing into it. Until derivation
// can be pinned to the authored template and its author, a trigger spawn takes a DECLARED
// grant only. This is that declaration, and nothing more.

// NamedVector resolves a spawn's `vector:` to a gate vector.
//
// The vocabulary is the three modes, and deliberately only the three: it is exactly what
// `verse process vector <id> <auto|ask|park>` already accepts, so the name a charter writes
// and the name an operator types mean the same thing. A richer named vector (a stored,
// reusable set of gates) wants a registry of vector entities, which is a different feature;
// until then a full vector reaches a running process through `process vector --file`.
//
// An empty name is not an error and not a vector: it means the spawn said nothing about
// autonomy, so the runtime's own default stands. That is what nil signals.
func NamedVector(name string) (*Vector, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil
	}
	var m Mode
	if err := m.UnmarshalText([]byte(name)); err != nil {
		return nil, fmt.Errorf("vector %q: %w", name, err)
	}
	// A named vector is a bare default with no gates, which is the point of naming one:
	// `vector: auto` on a trigger spawn says "this firing already answered its gates, do
	// not re-ask per action" — the double-ask authority.md names under "Where this meets
	// the rest". Restraint then comes from the grant, which refuses rather than asks.
	return &Vector{Default: m}, nil
}

// namedGrants is the grant vocabulary. It is small on purpose: every name here is a
// promise the enforcement below actually keeps, and a name that over-promises is worse
// than no name at all.
var namedGrants = map[string]Grant{
	// "all" is the wildcard — today's universal root scope, statable so a charter can say
	// it means to be unbounded rather than merely omitting the field.
	"all": {Allow: []GateMatch{{}}},
	// "read-only" is the one the complaint reached for: reads pass, everything else is
	// refused. Note it refuses rather than asks — see Grant.Permits.
	"read-only": {Allow: []GateMatch{{Verbs: []Verb{Read}}}},
	// "no-delegate" is the whole envelope minus delegation: the useful shape for an
	// unattended firing, where a sub-agent nobody can approve is a park waiting to happen.
	"no-delegate": {Allow: []GateMatch{
		{Verbs: []Verb{Read}}, {Verbs: []Verb{Write}}, {Verbs: []Verb{Create}},
		{Verbs: []Verb{Delete}}, {Verbs: []Verb{Exec}}, {Verbs: []Verb{Network}},
	}},
	// "none" grants nothing. A process under it can only report, which makes it the
	// honest spelling of "I want this to fail if it tries to act".
	"none": {},
}

// NamedGrant resolves a spawn's `grant:` to a hard envelope.
//
// An empty name returns ok=false with no error: the spawn declared no envelope, which is
// NOT the same as declaring an empty one. Grant's zero value permits nothing, so a caller
// that confused the two would refuse every action of every process that did not mention a
// grant. The two-return shape is what makes that confusion unspellable.
func NamedGrant(name string) (Grant, bool, error) {
	if strings.TrimSpace(name) == "" {
		return Grant{}, false, nil
	}
	g, ok := namedGrants[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return Grant{}, false, fmt.Errorf("grant %q: unknown (want %s)", name, strings.Join(GrantNames(), ", "))
	}
	return g, true, nil
}

// GrantNames lists the vocabulary, sorted — for the error above and for `verse` to show.
func GrantNames() []string {
	out := make([]string, 0, len(namedGrants))
	for k := range namedGrants {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Permits reports whether the envelope admits this action — the hard check, evaluated
// before the soft vector.
//
// It did not exist. Grant.Subset was the only reader of a process's own scope, at the
// call_agent boundary in spawnChild, so a grant bounded what a process could DELEGATE and
// never what it could DO. Wiring `grant:` through without this would have produced the same
// defect one layer deeper: a field that resolves, installs, and still does not stop
// anything.
//
// It matches by the same predicates a gate does, over the same derived effects, because
// authority and gating must classify an action identically or the envelope means something
// different from what it appears to say.
//
// It READS effects and never produces them: the caller derives first (the loop does, via
// Supervisor.Derive, immediately before this call). That is not an oversight to be papered
// over by deriving here — a Grant has no deriver, and one that classified actions with a
// different deriver than the vector uses would be enforcing a different envelope than the
// one the gates describe.
//
// The consequence is that an action with no derived effects matches no verb predicate and
// is refused under any non-wildcard grant. That is fail-closed, which authority.md asks
// for: an action nothing could classify is exactly the one an envelope should not admit.
func (g Grant) Permits(tu ToolUse) bool {
	if g.allowsAll() {
		return true
	}
	for _, m := range g.Allow {
		if ok, _ := (Gate{Match: m}).Matches(tu); ok {
			return true
		}
	}
	return false
}
