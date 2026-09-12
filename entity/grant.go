package entity

import (
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
)

// Level is what a principal may do at a node: nothing, read it, write it, own it. The first
// three nest — an owner may write, a writer may read — and Control stands beside them rather
// than above: it is run, cancel, pause, resume, the task-control contract, and holding it says
// nothing about the record.
type Level string

const (
	// NoAccess is the level of a principal that holds nothing at a node.
	NoAccess Level = ""
	// Read admits reading the node and what is beneath it.
	Read Level = "read"
	// ReadWrite admits writing into the node and creating beneath it; what such a creation
	// brings into being is owned by the node's owner, not the writer.
	ReadWrite Level = "read-write"
	// OwnerLevel admits everything ReadWrite does, and deleting the node whole; what an owner-
	// level creation brings into being is owned by the creator.
	OwnerLevel Level = "owner"
	// Control admits the task-control verbs on a task. It is granted beside the record levels
	// and checked by the control routes, never by the store.
	Control Level = "control"
)

// rank orders the record levels for Admits. Control is not in the order.
func (l Level) rank() int {
	switch l {
	case Read:
		return 1
	case ReadWrite:
		return 2
	case OwnerLevel:
		return 3
	}
	return 0
}

// Admits reports whether holding l is enough for what need asks: the record levels nest, and
// Control is only ever satisfied by Control.
func (l Level) Admits(need Level) bool {
	if need == Control {
		return l == Control
	}
	return l.rank() >= need.rank()
}

// GrantsField is the field of a perms record listing what its node's owner holds out.
const GrantsField = "grants"

// Everyone is the grantee that names every principal — the unattributed one included, which is
// what makes an open verse open to a request made from curl with no `user:` behind it.
const Everyone = "*"

// The principal kinds a grantee may name instead of a principal: every principal listed under
// `verse.principal.<kind>.*`. The three words are reserved as grantees for that reason, and a
// person whose email is one of them is not a case any address grammar produces.
const (
	RunnerKind = "runner"
	AgentKind  = "agent"
)

// Grant is one line of a record's grants: a path the grant covers, and who holds which level
// there.
//
//   - at: [verse, task, instance, *]
//     owner: ["*"]
//
// `at` is a path with at most a TRAILING `*`, the claim grammar (source.ParseClaim), and for
// the same reason a claim refuses a `*` anywhere else: a grant meaning something wider than it
// looks is authority resting on a typo. Without the star the grant covers the node itself and
// everything beneath it, which is what a runner's grant on its task needs; with it, the
// children and beneath, which is what a grant about creating needs.
type Grant struct {
	At    []string
	Below bool
	To    map[Level][]string
}

// Covers reports whether the grant reaches the node at segs.
func (g Grant) Covers(segs []string) bool {
	if len(segs) < len(g.At) || (g.Below && len(segs) == len(g.At)) {
		return false
	}
	for i, s := range g.At {
		if segs[i] != s {
			return false
		}
	}
	return true
}

// ParseGrants reads a record's grants field. A record with none has nil grants and no error;
// a line that cannot be read is an error naming it, so a policy edit that would silently
// grant nothing is refused where it is written rather than discovered where it is checked.
func ParseGrants(rec Payload) ([]Grant, error) {
	f := ir.Uncomment(Get(rec, GrantsField))
	if f == nil {
		return nil, nil
	}
	if f.Type != ir.ArrayType {
		return nil, fmt.Errorf("%s: a list of grants, one `at` per line", GrantsField)
	}
	var out []Grant
	for i, v := range f.Values {
		g, err := parseGrant(v)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", GrantsField, i, err)
		}
		out = append(out, g)
	}
	return out, nil
}

func parseGrant(v Payload) (Grant, error) {
	v = ir.Uncomment(v)
	if v == nil || v.Type != ir.ObjectType {
		return Grant{}, fmt.Errorf("a grant is an object with `at` and a level")
	}
	var g Grant
	at := ir.Uncomment(Get(v, "at"))
	var segs []string
	switch {
	case at == nil:
		return Grant{}, fmt.Errorf("a grant names the path it covers in `at`")
	case at.Type == ir.StringType:
		var err error
		if segs, err = ExprSegs(at.String); err != nil {
			return Grant{}, fmt.Errorf("at: %w", err)
		}
	case at.Type == ir.ArrayType:
		segs = Strs(v, "at")
		if len(segs) != len(at.Values) {
			return Grant{}, fmt.Errorf("at: every segment is a name")
		}
	default:
		return Grant{}, fmt.Errorf("at: a path, spelled as a list or a kinded path")
	}
	if len(segs) == 0 {
		return Grant{}, fmt.Errorf("at: a path, spelled as a list or a kinded path")
	}
	for i, s := range segs {
		if s == "" {
			return Grant{}, fmt.Errorf("at: a segment is a name, and none may be empty")
		}
		if strings.Contains(s, "*") {
			if s != "*" || i != len(segs)-1 {
				return Grant{}, fmt.Errorf("at: a `*` is a whole TRAILING segment, covering what is " +
					"beneath the path — a `*` anywhere else is refused rather than approximated")
			}
			g.Below = true
			segs = segs[:i]
		}
	}
	g.At = segs
	for _, l := range []Level{Read, ReadWrite, OwnerLevel, Control} {
		to := ir.Uncomment(Get(v, string(l)))
		if to == nil {
			continue
		}
		if to.Type != ir.ArrayType {
			return Grant{}, fmt.Errorf("%s: a list of grantees — principals, `*`, or a principal kind", l)
		}
		names := Strs(v, string(l))
		if len(names) != len(to.Values) {
			return Grant{}, fmt.Errorf("%s: every grantee is a string", l)
		}
		if g.To == nil {
			g.To = map[Level][]string{}
		}
		g.To[l] = names
	}
	if len(g.To) == 0 {
		return Grant{}, fmt.Errorf("a grant holds out a level: read, read-write, owner or control")
	}
	return g, nil
}

// Payload spells the grant back as a record line, the inverse of parseGrant.
func (g Grant) Payload() Payload {
	at := append([]string{}, g.At...)
	if g.Below {
		at = append(at, "*")
	}
	o := Obj{"at": strList(at)}
	for l, names := range g.To {
		o[string(l)] = strList(names)
	}
	return o.Payload()
}

// GrantsPayload spells a list of grants as the record field's value.
func GrantsPayload(gs []Grant) Payload {
	arr := &ir.Node{Type: ir.ArrayType}
	for _, g := range gs {
		arr.Values = append(arr.Values, g.Payload())
	}
	return arr
}

func strList(ss []string) Payload {
	arr := &ir.Node{Type: ir.ArrayType}
	for _, s := range ss {
		arr.Values = append(arr.Values, ir.FromString(s))
	}
	return arr
}
