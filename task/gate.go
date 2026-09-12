package task

import (
	"fmt"
	"strings"
)

// Gate is one rule in the vector: a match predicate plus the mode to apply when
// it matches. Gate *definition* (Match) is deliberately orthogonal to resolver
// *assignment* (held by the Supervisor), per the model's constraint that the
// two are independent.
// The tony field names are lowercase where the Go fields are not. There were no wire
// tags here before, so encoding/json spelled these `Name`/`Match`/`Mode` — odd next to
// every other verse document. Renaming them is a breaking change to
// `verse task vector --file`, and it ships with the default-format flip rather than
// drifting in ahead of it.
//
// A gate that asks poses a QUESTION, and the three fields after Mode are the question's
// shape, spelled as a charter gate spells them (trigger.Gate): how many ballots settle it,
// the outcomes it can settle on, and how long it stands. A gate that says none of these
// poses `proceed` or `deny`, one ballot each, which is the question a gate has always asked.
//
//tony:schemagen=verse-gate,notag
type Gate struct {
	Name string `tony:"field=name,omitzero"`
	// omitzero is correct intent but currently inert on a struct-valued field: generated
	// code emits `match: {}` on a gate with no predicate (tony-format item 12). The tag
	// stays — it starts working the moment that lands. See TestCodegenStillEmitsEmptyStructFields.
	Match GateMatch `tony:"field=match,omitzero"`
	Mode  Mode      `tony:"field=mode"`
	// Need is how many ballots settle `proceed`; one `deny` still refuses. It is the short
	// spelling of `options: [{id: proceed, need: N}, {id: deny, need: 1}]`, and the two
	// are not written together.
	Need int `tony:"field=need,omitzero"`
	// Options are the gate's own outcomes, each with its threshold, for a question that is
	// not yes or no. Ordered: the first to reach its threshold settles the question. An
	// option whose id names a child mode (`careful`, `autonomous`) is read as one when the
	// action is a delegation — see DelegateOptions.
	Options []Option `tony:"field=options,omitzero"`
	// Deadline is how long the question stands, and the option it settles on when the clock
	// passes with none at its threshold. An empty default settles on nothing, which the loop
	// reads as a refusal nobody made: the action is held, and the agent re-plans around it.
	//
	// It is the question's own clock. A bound resolver's `deadline` is the resolver's, and
	// hands the question up rather than settling it; when both are set the earlier fires
	// first, as on every other question record.
	Deadline *Deadline `tony:"field=deadline,omitzero"`
	// Answerers is who this gate's question is held out to, when the spawn says — grantees, as
	// a grant names them. See trigger.Gate.Answerers; a task's own gates take the same line.
	Answerers []string `tony:"field=answerers,omitzero"`
}

// Validate refuses a gate whose question cannot be posed as written. Every refusal names
// the spelling that would work, because each of these is a plausible thing to write. It is
// the same set of refusals a charter gate meets at install (trigger.Gate), for the same
// document shape.
func (g Gate) Validate() error {
	if g.Need < 0 {
		return fmt.Errorf("need %d: a threshold is at least one", g.Need)
	}
	if g.Need > 0 && len(g.Options) > 0 {
		return fmt.Errorf("`need` and `options` are written together; a threshold per " +
			"option is spelled on the option")
	}
	seen := map[string]bool{}
	for _, o := range g.Options {
		if o.ID == "" {
			return fmt.Errorf("an option has no id")
		}
		if seen[o.ID] {
			return fmt.Errorf("option %q is written twice", o.ID)
		}
		seen[o.ID] = true
		if o.Need < 0 {
			return fmt.Errorf("option %q: need %d: a threshold is at least one", o.ID, o.Need)
		}
	}
	if g.Deadline != nil {
		d, err := g.Deadline.After()
		if err != nil {
			return fmt.Errorf("deadline: %w (quote the duration: within: \"24h\")", err)
		}
		if d <= 0 {
			return fmt.Errorf("deadline: within %q: a deadline is in the future", g.Deadline.Within)
		}
		if dflt := g.Deadline.Default; dflt != "" {
			if _, ok := Need(g.question(), dflt); !ok {
				return fmt.Errorf("deadline default %q is not one of this gate's options (%s)",
					dflt, strings.Join(OptionIDs(g.question()), ", "))
			}
		}
	}
	if g.asks() && !g.Mode.Blocks() {
		return fmt.Errorf("mode %s asks nobody, so a threshold, options or a deadline have "+
			"nothing to settle; drop them or make it ask", strings.ToLower(g.Mode.String()))
	}
	return nil
}

// asks reports whether the gate says anything about how its question settles, which only a
// posed question can honour.
func (g Gate) asks() bool {
	return g.Need > 0 || len(g.Options) > 0 || g.Deadline != nil
}

// options is what the gate's question offers: its own options when it names them, the two
// default ones with `proceed` at the threshold `need` asks for, or nothing — the default
// two, one ballot each — when it says neither.
func (g Gate) options() []Option {
	if len(g.Options) > 0 {
		return g.Options
	}
	if g.Need > 1 {
		return []Option{{ID: Proceed, Need: g.Need}, {ID: Deny, Need: 1}}
	}
	return nil
}

// question is options with the default filled in, for a check that needs the list.
func (g Gate) question() []Option {
	if opts := g.options(); opts != nil {
		return opts
	}
	return DefaultOptions()
}

// Validate refuses a vector one of whose gates cannot be posed as written, naming the gate.
func (v *Vector) Validate() error {
	if v == nil {
		return nil
	}
	for i, g := range v.Gates {
		if err := g.Validate(); err != nil {
			name := g.Name
			if name == "" {
				name = fmt.Sprintf("#%d", i+1)
			}
			return fmt.Errorf("gate %s: %w", name, err)
		}
	}
	return nil
}

// GateMatch predicates over an action. Every non-empty dimension must match
// (AND); empty dimensions are wildcards. It spans self-report (Tools), the
// derived typed capability (Verbs, Resources), the file-path convenience
// (Paths, à la 42), and git-repo state (Branches, OnPush).
//
//tony:schemagen=verse-gate-match,notag
type GateMatch struct {
	Tools     []string       `tony:"field=tools,omitzero"`     // glob(s) on ToolUse.Name (self-reported)
	Verbs     []Verb         `tony:"field=verbs,omitzero"`     // match if any derived effect has one of these verbs
	Resources []ResourcePred `tony:"field=resources,omitzero"` // typed resource predicates over derived effects
	Paths     []PathPred     `tony:"field=paths,omitzero"`     // file predicates over file-kind effects
	Branches  []string       `tony:"field=branches,omitzero"`  // glob(s) on Repo.Branch
	OnPush    bool           `tony:"field=onPush,omitzero"`    // matches only actions targeting the push / promotion ref
}

// ResourcePred matches a derived effect's resource.
//
//tony:schemagen=verse-resource-pred,notag
type ResourcePred struct {
	Kind    ResourceKind `tony:"field=kind,omitzero"`
	Pattern string       `tony:"field=pattern,omitzero"` // glob on Resource.Ref ("" = any ref of the kind)
}

// Matches reports whether the gate applies to the action, with a human reason.
func (g Gate) Matches(tu ToolUse) (bool, string) {
	var reasons []string

	if len(g.Match.Tools) > 0 {
		ok, pat := anyGlob(g.Match.Tools, tu.Name)
		if !ok {
			return false, ""
		}
		reasons = append(reasons, "tool "+pat)
	}

	if len(g.Match.Verbs) > 0 {
		v, ok := matchVerbs(g.Match.Verbs, tu.Effects)
		if !ok {
			return false, ""
		}
		reasons = append(reasons, "verb "+string(v))
	}

	if len(g.Match.Resources) > 0 {
		r, ok := matchResources(g.Match.Resources, tu.Effects)
		if !ok {
			return false, ""
		}
		reasons = append(reasons, string(r.Kind)+" "+r.Ref)
	}

	if len(g.Match.Paths) > 0 {
		files := tu.FilesTouched()
		var hit []string
		for _, s := range g.Match.Paths {
			hit = append(hit, s.MatchFiles(files)...)
		}
		if len(hit) == 0 {
			return false, ""
		}
		reasons = append(reasons, "files "+strings.Join(trunc(hit, 3), ", "))
	}

	if len(g.Match.Branches) > 0 {
		br := ""
		if tu.Repo != nil {
			br = tu.Repo.Branch
		}
		ok, pat := anyGlob(g.Match.Branches, br)
		if !ok {
			return false, ""
		}
		reasons = append(reasons, "branch "+pat)
	}

	if g.Match.OnPush {
		if !tu.isPush() {
			return false, ""
		}
		reasons = append(reasons, "push target")
	}

	// An entirely empty match matches everything (a default gate).
	if len(reasons) == 0 {
		reasons = append(reasons, "default")
	}
	return true, strings.Join(reasons, "; ")
}

func matchVerbs(vs []Verb, effs []Effect) (Verb, bool) {
	for _, e := range effs {
		for _, v := range vs {
			if e.Verb == v {
				return v, true
			}
		}
	}
	return "", false
}

func matchResources(preds []ResourcePred, effs []Effect) (Resource, bool) {
	for _, e := range effs {
		for _, p := range preds {
			if p.Kind != "" && p.Kind != e.Resource.Kind {
				continue
			}
			if p.Pattern != "" && !matchGlob(p.Pattern, e.Resource.Ref) {
				continue
			}
			return e.Resource, true
		}
	}
	return Resource{}, false
}

func anyGlob(patterns []string, s string) (bool, string) {
	for _, p := range patterns {
		if matchGlob(p, s) {
			return true, p
		}
	}
	return false, ""
}

func trunc(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(xs[:n:n], "...")
}
