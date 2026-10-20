package entity

import (
	"fmt"
	"strings"
	"unicode"
)

// The kinds a name carries before its colon. A principal is `<kind>:<id>` for an identity and
// `via:<path>` for a place acting; a grantee is a principal, a kind with `*` for every principal
// of it, a group, or `*` for everyone. The kind is IN THE NAME, so nothing consults a listing to
// tell a runner called verse-runner from a person called that: `runner:verse-runner` and
// `user:verse-runner` are different strings.
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
	// ViaKind is a place acting — a rule, a source, a resolver — named by its address.
	ViaKind = "via"
	// AudienceKind is A PLACE'S AUDIENCE: `audience:<address>`, in a grant's `read` or
	// `read-write`, admits whoever holds that level at the address, whoever they turn out to be.
	// It is how a record says it is read and written as something else is — an ask read and
	// answered as what it is about (9ce3bva0, D3) — where those principals cannot be written
	// down: ownership above, inherited grants, groups and kinds all decide them. It lends neither
	// `owner` nor `control`: holding a place is not holding what is about it. A set, so never a
	// principal.
	AudienceKind = "audience"
	// AnsweringKind is WHOEVER ANSWERS FOR A PLACE: `answering:<address>`, in a grant's `read`,
	// admits whoever holds `answer` at the address (Answer) — the resolvers answering for a
	// person at `verse.for.<person>`, or for a place. It is how an ask is read by the resolver
	// serving it (9ce3bva0, S2). A set, so never a principal.
	AnsweringKind = "answering"
	// PairKind is a rule's task acting for whoever wrote the record it is about, as
	// `pair:<asker> <rule>` (a spawn's `as: subject`). Only the engine makes one — it is no
	// grantee and parses as nothing, so no request can present it — and what it may do is what
	// its asker may do (Pair).
	PairKind = "pair"
)

// Pair is the principal of a rule's task acting for the person whose record fired it: the asker
// supplied the intent, the rule the machinery. It acts on the asker's authority alone — the
// rule is named so the record says what the task acted through. The asker cannot borrow the
// rule's authority by writing a record, since the rule adds none; and a rule's author cannot
// borrow a record owner's by selecting their records, since such a rule may select only in a
// place its installer owns (checked at install).
//
// A space separates the two because no principal holds one: an id holds no space, and neither
// does an address.
func Pair(asker, bound string) string { return PairKind + ":" + asker + " " + bound }

// PairOf reads a pair back into its parties, and false for anything else.
func PairOf(principal string) (asker, bound string, ok bool) {
	rest, isPair := strings.CutPrefix(principal, PairKind+":")
	if !isPair {
		return "", "", false
	}
	asker, bound, ok = strings.Cut(rest, " ")
	if !ok || asker == "" || bound == "" {
		return "", "", false
	}
	return asker, bound, true
}

// Everyone is the grantee that names every principal — a request that names nobody included,
// which is what lets a read made from curl with no `user:` behind it see an open verse.
const Everyone = "*"

// actors is where in verse's half a place acts, by system, and the kind beneath it that does
// ("" for any): a rule fires as `verse.trigger.rule.<name>`, a source reflects as
// `verse.iods.<name>`, a resolver answers as `verse.resolvers.<name>`. A
// `via:` naming anywhere else names nobody — a runner has an address and acts as
// `runner:<identity>`, so `via:verse.runners.verse-runner` would look right and admit no one.
// The owning packages import this one, so the names are spelled here and a test in versed holds
// them to the packages' own.
var actors = map[string]string{"trigger": "rule", "iods": "", "resolvers": ""}

// Grantee is a name in the grammar, parsed: the kind before the colon and what follows it.
type Grantee struct {
	// Kind is UserKind, AgentKind, RunnerKind, SystemKind, GroupKind, ViaKind, AudienceKind, or
	// Everyone.
	Kind string
	// ID is what follows the colon for every kind but via, and `*` for a whole kind.
	ID string
	// Path is a via's or an audience's address as segments, a via's trailing `*` kept as the last
	// one.
	Path []string
}

// Grammar is the sentence every refusal of a name quotes, so the grammar is said once.
func Grammar() string {
	return "a principal is user:<email>, agent:<name>, runner:<identity>, system:<part> or " +
		"via:<address> (a rule, a source or a resolver acting); a grantee is a principal, " +
		"<kind>:* for every principal of a kind, group:<name> for a group's members, " +
		"audience:<address> for whoever holds the level at that address, answering:<address> for " +
		"the resolvers answering for it, or * for everyone; a " +
		"bare email is user:<email>"
}

// ParseGrantee reads a name in the grammar. A bare string holding an `@` and no colon is a
// person's email, `user:<s>` — unless it is an address in verse's own half, which is no
// person. Every other
// bare word names nothing and is refused — which is where `user`, `agent`, `verse-runner` and
// `verse.principal.group.<name>` stop meaning anything.
func ParseGrantee(s string) (Grantee, error) {
	if s == Everyone {
		return Grantee{Kind: Everyone}, nil
	}
	kind, rest, found := strings.Cut(s, ":")
	if !found {
		switch {
		case strings.HasPrefix(s, VerseRoot+"."):
			return Grantee{}, refuseName(s, "an address in verse's half is no person; a place "+
				"acting is via:<address>, and a group is group:<name>")
		case strings.Contains(s, "@"):
			kind, rest = UserKind, s
		default:
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
	case AudienceKind, AnsweringKind:
		path, err := PathSegs(rest)
		if err != nil || len(path) == 0 {
			return Grantee{}, refuseName(s, kind+": names one address, the place it admits for")
		}
		return Grantee{Kind: kind, Path: path}, nil
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
			"a source as verse.iods.<name>, a resolver as verse.resolvers.<name>", s)
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
	case g.Kind == AudienceKind, g.Kind == AnsweringKind:
		return Grantee{}, refuseName(s, "who holds a level at a place is a set of principals, not one principal")
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
	case AudienceKind:
		return Audience(RefAt(g.Path...))
	case AnsweringKind:
		return Answering(RefAt(g.Path...))
	}
	return g.Kind + ":" + g.ID
}

// ForSystem is where a person says whom they let answer for them: `verse.for.<principal>`, their
// own node, whose `answer` grants name the resolvers that do (Answer).
const ForSystem = "for"

// ForRef is the node a principal's `answer` grants stand on.
func ForRef(principal string) Ref { return VerseRef(ForSystem, principal) }

// Answering spells the grantee that admits whoever holds `answer` at ref.
func Answering(ref Ref) string { return AnsweringKind + ":" + ref.String() }

// Audience spells the grantee that admits whoever holds, at ref, the level it is listed under.
func Audience(ref Ref) string { return AudienceKind + ":" + ref.String() }

// Via spells the principal of a place acting: whoever writes as a rule, a source or a resolver.
func Via(ref Ref) string { return ViaKind + ":" + ref.String() }

// The parts of versed that write on its own behalf, each a name so a record says which act
// wrote it. They are attribution, not authority: any principal of SystemKind owns the root and
// passes every check, and nothing grants to one part rather than another.
const (
	// SystemDocket poses, settles, escalates and reowns a question, and releases it from a resolver.
	SystemDocket = SystemKind + ":docket"
	// SystemMirror projects a task and grants the worker that claimed it.
	SystemMirror = SystemKind + ":mirror"
	// SystemPerms writes the perms mirror: an owner taken, a grant added, `under`, and who has
	// been met.
	SystemPerms = SystemKind + ":perms"
	// SystemSeed writes the deployment's policy at start.
	SystemSeed = SystemKind + ":seed"
	// SystemCharter installs the charter the deployment stands behind, so its rules resolve to it.
	SystemCharter = SystemKind + ":charter"
	// SystemIdentity writes identity claims and the bindings verified from them: which external
	// account is which principal (versed/identity.go).
	SystemIdentity = SystemKind + ":identity"
	// SystemDefault is what remains: the restore at start, and whatever else names no part. It
	// is the owner of the root.
	SystemDefault = SystemKind + ":default"
)

// IsSystem reports whether a principal is versed itself, whichever part.
func IsSystem(principal string) bool { return KindOf(principal) == SystemKind }

// KindOf is the kind a name carries before its colon, and "" for a name that carries none.
func KindOf(name string) string {
	kind, _, found := strings.Cut(name, ":")
	if !found {
		return ""
	}
	return kind
}

// Canonical is a name as the grammar spells it — a bare email as `user:<email>` — and the name
// as written where it does not parse, which admits nobody. Every list of grantees a record holds
// is written through it, so one form is stored whichever form was typed.
func Canonical(names []string) []string {
	if names == nil {
		return nil
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n
		if g, err := ParseGrantee(n); err == nil {
			out[i] = g.String()
		}
	}
	return out
}
