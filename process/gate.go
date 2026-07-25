package process

import "strings"

// Gate is one rule in the vector: a match predicate plus the mode to apply when
// it matches. Gate *definition* (Match) is deliberately orthogonal to resolver
// *assignment* (held by the Supervisor), per the model's constraint that the
// two are independent.
// The tony field names are lowercase where the Go fields are not. There were no wire
// tags here before, so encoding/json spelled these `Name`/`Match`/`Mode` — odd next to
// every other verse document. Renaming them is a breaking change to
// `verse process vector --file`, and it ships with the default-format flip rather than
// drifting in ahead of it.
type Gate struct {
	Name  string    `tony:"field=name,omitzero"`
	Match GateMatch `tony:"field=match,omitzero"`
	Mode  Mode      `tony:"field=mode"`
}

// GateMatch predicates over an action. Every non-empty dimension must match
// (AND); empty dimensions are wildcards. It spans self-report (Tools), the
// derived typed capability (Verbs, Resources), the file-path convenience
// (Paths, à la 42), and git-repo state (Branches, OnPush).
type GateMatch struct {
	Tools     []string       `tony:"field=tools,omitzero"`     // glob(s) on ToolUse.Name (self-reported)
	Verbs     []Verb         `tony:"field=verbs,omitzero"`     // match if any derived effect has one of these verbs
	Resources []ResourcePred `tony:"field=resources,omitzero"` // typed resource predicates over derived effects
	Paths     []PathPred     `tony:"field=paths,omitzero"`     // file predicates over file-kind effects
	Branches  []string       `tony:"field=branches,omitzero"`  // glob(s) on Repo.Branch
	OnPush    bool           `tony:"field=onPush,omitzero"`    // matches only actions targeting the push / promotion ref
}

// ResourcePred matches a derived effect's resource.
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
