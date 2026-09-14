package entity

import (
	"fmt"
	"strings"
	"unicode"
)

// The kinds a name carries before its colon. A principal is `<kind>:<id>` for an identity and
// `via:<path>` for a place acting; a grantee is a principal, a kind with `*` for every principal
// of it, a group, or `*` for everyone. The kind is IN THE NAME, so nothing consults a listing to
// tell a runner called coderunner from a person called that: `runner:coderunner` and
// `user:coderunner` are different strings.
const (
	// UserKind is a person, by email.
	UserKind = "user"
	// AgentKind is an operator agent: a harness driving verse through the api, a principal of
	// its own and not the person whose terminal it runs in.
	AgentKind = "agent"
	// RunnerKind is a runner, by the identity it acts as.
	RunnerKind = "runner"
	// SystemKind is versed itself, and the part after the colon says which of its own acts
	// wrote (SystemDocket and the rest). Any principal of the kind owns the root.
	SystemKind = "system"
	// GroupKind is a SET: `verse.principal.group.<name>` lists its members, and `group:<name>`
	// admits them, each as themselves. Nothing acts as a group, so it is never a principal.
	GroupKind = "group"
	// ViaKind is a place acting — a rule, a source, a binding — named by its address.
	ViaKind = "via"
)

// Everyone is the grantee that names every principal — the unattributed one included, which is
// what makes an open verse open to a request made from curl with no `user:` behind it.
const Everyone = "*"

// actors is where in verse's half a place acts, by system, and the kind beneath it that does
// ("" for any): a rule fires as `verse.trigger.rule.<name>`, a source reflects as
// `verse.sources.<system>.<name>`, a binding answers as `verse.resolvers.<kind>.<name>`. A
// `via:` naming anywhere else names nobody — a runner has an address and acts as
// `runner:<identity>`, so `via:verse.runners.pool.coderunner` would look right and admit no one.
// The owning packages import this one, so the names are spelled here and a test in versed holds
// them to the packages' own.
var actors = map[string]string{"trigger": "rule", "sources": "", "resolvers": ""}

// Grantee is a name in the grammar, parsed: the kind before the colon and what follows it.
type Grantee struct {
	// Kind is UserKind, AgentKind, RunnerKind, SystemKind, GroupKind, ViaKind, or Everyone.
	Kind string
	// ID is what follows the colon for every kind but via, and `*` for a whole kind.
	ID string
	// Path is a via's address as segments, a trailing `*` kept as the last one.
	Path []string
}

// Grammar is the sentence every refusal of a name quotes, so the grammar is said once.
func Grammar() string {
	return "a principal is user:<email>, agent:<name>, runner:<identity>, system:<part> or " +
		"via:<address> (a rule, a source or a binding acting); a grantee is a principal, " +
		"<kind>:* for every principal of a kind, group:<name> for a group's members, or * for " +
		"everyone; a bare email is user:<email>"
}

// ParseGrantee reads a name in the grammar. A bare string holding an `@` and no colon is a
// person's email, `user:<s>`; every other bare word names nothing and is refused — which is
// where `user`, `agent`, `coderunner` and `verse.principal.group.<name>` stop meaning anything.
func ParseGrantee(s string) (Grantee, error) {
	if s == Everyone {
		return Grantee{Kind: Everyone}, nil
	}
	kind, rest, found := strings.Cut(s, ":")
	if !found {
		if strings.Contains(s, "@") {
			kind, rest = UserKind, s
		} else {
			return Grantee{}, refuseName(s, "a bare word names nothing")
		}
	}
	switch kind {
	case UserKind, AgentKind, RunnerKind, SystemKind, GroupKind:
		if rest == "" {
			return Grantee{}, refuseName(s, "the id after the colon is empty")
		}
		if rest == Everyone {
			if kind == GroupKind {
				return Grantee{}, refuseName(s, "a group is a set to be named, not a kind to list "+
					"under, so group:* names nothing")
			}
			return Grantee{Kind: kind, ID: Everyone}, nil
		}
		if strings.Contains(rest, Everyone) {
			return Grantee{}, refuseName(s, "a `*` is the whole id or not in it")
		}
		if strings.ContainsFunc(rest, unicode.IsSpace) {
			return Grantee{}, refuseName(s, "an id holds no space")
		}
		return Grantee{Kind: kind, ID: rest}, nil
	case ViaKind:
		path, err := viaPath(rest)
		if err != nil {
			return Grantee{}, refuseName(s, err.Error())
		}
		return Grantee{Kind: ViaKind, Path: path}, nil
	}
	return Grantee{}, refuseName(s, fmt.Sprintf("%q is not a kind", kind))
}

// viaPath reads a via's address: a path in verse's own half, where a place acts, with at most a
// trailing `*`.
func viaPath(s string) ([]string, error) {
	if s == "" {
		return nil, fmt.Errorf("the address after via: is empty")
	}
	segs, err := ExprSegs(s)
	if err != nil {
		return nil, err
	}
	for i, seg := range segs {
		if seg == Everyone && i != len(segs)-1 {
			return nil, fmt.Errorf("a `*` is a whole TRAILING segment, covering what is beneath")
		}
	}
	if len(segs) < 2 || segs[0] != VerseRoot {
		return nil, fmt.Errorf("a place that acts is in verse's own half, verse.<system>...")
	}
	kind, acts := actors[segs[1]]
	if !acts || (kind != "" && len(segs) > 2 && segs[2] != Everyone && segs[2] != kind) {
		return nil, fmt.Errorf("nothing acts through %s: a rule acts as verse.trigger.rule.<name>, "+
			"a source as verse.sources.<system>.<name>, a binding as verse.resolvers.<kind>.<name>", s)
	}
	return segs, nil
}

func refuseName(s, why string) error {
	return fmt.Errorf("%q: %s — %s", s, why, Grammar())
}

// ParsePrincipal reads a name that is one principal: a grantee that is not everyone, not a whole
// kind, and not a group, since none of those is anybody a write can be made as.
func ParsePrincipal(s string) (Grantee, error) {
	g, err := ParseGrantee(s)
	if err != nil {
		return Grantee{}, err
	}
	switch {
	case g.Kind == Everyone, g.ID == Everyone, g.Kind == ViaKind && g.Path[len(g.Path)-1] == Everyone:
		return Grantee{}, refuseName(s, "a set of principals is not one principal")
	case g.Kind == GroupKind:
		return Grantee{}, refuseName(s, "nothing acts as a group; its members act as themselves")
	}
	return g, nil
}

// String spells the name canonically: what is stored, and what every read gives back.
func (g Grantee) String() string {
	switch g.Kind {
	case Everyone:
		return Everyone
	case ViaKind:
		if n := len(g.Path); n > 0 && g.Path[n-1] == Everyone {
			return ViaKind + ":" + JoinSegs(g.Path[:n-1]) + "." + Everyone
		}
		return ViaKind + ":" + JoinSegs(g.Path)
	}
	return g.Kind + ":" + g.ID
}

// Via spells the principal of a place acting: whoever writes as a rule, a source or a binding.
func Via(ref Ref) string { return ViaKind + ":" + ref.String() }
