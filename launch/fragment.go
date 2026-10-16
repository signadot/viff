package launch

import (
	"fmt"

	"github.com/signadot/verse/task"
)

// Fragment is what a selected doer contributes to the spawn that selected it — the
// provider-settable subset of a Spec, named field-for-field after the Spec fields it
// overlays.
//
// It exists because a selection used to bind ONE field. `provider:` picks a doer at fire
// time from the registry; what the winner contributed was its `driver`, so two doers could
// differ by which runtime ran the prompt and by nothing else. Once a spawn could say `frame`,
// `tools` and `grant` — what the agent is told, what it may attempt, what it may do at all —
// that was most of what distinguishes one doer from another, and none of it could cross.
// See docs/provider-selection.md.
//
// The fields NOT here are as deliberate as the ones that are. `vector` is absent because a
// provider entity could otherwise take every action boundary off the docket without touching
// authority (`vector: auto` says "this firing already answered its gates", which is the
// charter's claim to make, not a doer's). `resolver` and `environment` are absent because
// neither has semantics to resolve to yet — see versed.launchOf. A provider that sets any of
// them is refused rather than trimmed; the engine's eligibility filter is where that happens,
// because a doer running under an envelope different from the one its entity states is the
// failure this whole design is closing.
type Fragment struct {
	// ID is who this is — the doer's own identity, carried so the spawned task can say
	// WHICH doer was selected and not only which driver ran. Two doers sharing a driver is
	// the case this whole feature exists for, so the driver alone no longer identifies the
	// pick. An entity ref ("provider:treat:fixer") for a registry doer; the bare name for an
	// inline `eligible` list, which names no entity.
	//
	// It is provenance rather than configuration: IsZero ignores it, because a doer that
	// contributes nothing to the launch still has a name.
	ID     string
	Driver string
	Frame  string
	Grant  string
	Tools  []string
	// VirtualKey is which model credential the doer runs under, by ADDRESS. It belongs here
	// rather than only on the spawn because it is the clearest thing that distinguishes two
	// doers sharing a driver: the same harness, a different budget and a different route.
	//
	// A provider entity may set it although it is adjacent to spending, because the address is
	// not the authority — the READ of it is, checked against whoever named it before the task
	// starts. A doer naming an address nobody may read is a doer that cannot run.
	VirtualKey string
}

// IsZero reports whether the fragment contributes nothing — a provider that is only
// attributes, which is every provider written before this existed.
func (f Fragment) IsZero() bool {
	return f.Driver == "" && f.Frame == "" && f.Grant == "" && len(f.Tools) == 0 &&
		f.VirtualKey == ""
}

// Overlay returns the spec the selection produces: the charter's spawn as the base, the
// winner's fragment over it. The spawn is the ceiling and the fragment may only narrow —
// which for three of the four fields is a statement about authority the caller enforces
// (Grant ⊆ the spawn's, checked at eligibility), and for Tools is arithmetic done here.
//
// Per field, and the reason each rule is not the other one:
//
//   - Driver, Frame, Grant, VirtualKey: set replaces, absent inherits. A provider that says `tools` and
//     not `grant` keeps the spawn's grant rather than losing it — "absent" and "empty" are
//     different values on both sides, which is exactly why task.NamedGrant returns an ok
//     rather than a zero Grant.
//   - Tools: INTERSECT, never replace. An unnamed list is all of the driver's tools (see
//     Spec.Tools), so a replace would let `tools: [read_file, run_bash]` on a provider WIDEN a
//     spawn that offered only [read_file]. "Narrows by construction" is true of a tool list read on its
//     own and false of one overlaid onto another.
//
// The error is the case where both sides name tools and they share none: the intersection is
// spellable, as `[]`, but a doer that may use no tool the spawn offers cannot do the work, and
// running it with nothing would say so late. The caller excludes such a provider from the
// eligible set instead, so it never reaches a ballot. A provider's list only narrows, so an
// empty one — which a provider entity's payload cannot tell from an absent one — does not
// narrow; `tools: []` means no tools on the SPAWN alone.
func (s Spec) Overlay(f Fragment) (Spec, error) {
	out := s
	if f.ID != "" {
		out.Doer = f.ID
	}
	if f.Driver != "" {
		out.Driver = f.Driver
	}
	if f.Frame != "" {
		out.Frame = f.Frame
	}
	if f.Grant != "" {
		out.Grant = f.Grant
	}
	// A name, not a secret, so replacing one is not widening anything: what the name is
	// ALLOWED to resolve to is the runtime's, and who may name it is the same grants
	// question the doer's own writability already is.
	if f.VirtualKey != "" {
		out.VirtualKey = f.VirtualKey
	}
	tools, err := intersectTools(s.Tools, f.Tools)
	if err != nil {
		return Spec{}, err
	}
	out.Tools = tools
	return out, nil
}

// intersectTools narrows a spawn's tool list by a provider's. Order follows the SPAWN, so
// what the charter wrote reads back in the order it wrote it. A spawn that named none (nil)
// takes the provider's list as the narrowing; one that named `[]` has no tools for a
// provider to narrow.
func intersectTools(spawnP *[]string, provider []string) (*[]string, error) {
	switch {
	case len(provider) == 0: // the provider does not narrow
		return task.CloneTools(spawnP), nil
	case spawnP == nil: // the spawn named none; the provider's list IS the narrowing
		return task.ToolsPtr(provider), nil
	case len(*spawnP) == 0:
		// The spawn named no tools. That is the author's choice and not a contradiction
		// with the doer: a provider may only narrow, and there is nothing to narrow.
		return task.CloneTools(spawnP), nil
	}
	spawn := *spawnP
	allowed := make(map[string]bool, len(provider))
	for _, t := range provider {
		allowed[t] = true
	}
	var out []string
	for _, t := range spawn {
		if allowed[t] {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		// Spellable now, as `[]`, and still refused: a doer that shares no tool with the spawn
		// is a doer that cannot do the work, and running it with nothing says so late.
		return nil, fmt.Errorf("the spawn offers %v and the doer offers %v, which share no tool",
			spawn, provider)
	}
	return task.ToolsPtr(out), nil
}
