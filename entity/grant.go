package entity

import (
	"fmt"
	"slices"
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

// DefaultField is the field of a perms record saying what a node CREATED BENEATH it holds out —
// the levels and grantees copied onto the new node's own record at creation, as a grant at
// the node. It is consulted where a record is written, which is where ownership changes and
// so where what a node holds out has to be decided; below an owner the owner's own grants
// cover everything. A directory's default ACL rather than a process's umask, since a place is
// what verse addresses.
//
//	default: {read: ["*"]}      # what is created beneath is readable by everyone
//	default: {}                 # what is created beneath is held out to nobody
//
// Absent means inherit: the nearest default up the tree, and the root's at the top.
const DefaultField = "default"

// Levels is what a default holds: grantees by level, the levels of a grant with no `at`.
type Levels map[Level][]string

// ParseLevels reads grantees by level out of an object: `{read: ["*"], control: [runner:bot]}`.
// present reports whether the field was there at all, since `{}` and absence say different
// things — nothing, and inherit. Every grantee is read in the grammar and stored as it spells
// one; a name it does not read is refused where it is typed.
func ParseLevels(rec Payload, field string) (levels Levels, present bool, err error) {
	return parseLevels(rec, field, grantees)
}

// readLevels is ParseLevels over what a record already holds: a name the grammar does not read
// is kept as written, so the check meets it, admits nobody by it and says so.
func readLevels(rec Payload, field string) (Levels, bool, error) {
	return parseLevels(rec, field, keepNames)
}

// grantees reads a list of grantee names in the grammar, canonically.
func grantees(field string, names []string) ([]string, error) {
	out := make([]string, len(names))
	for i, n := range names {
		g, err := ParseGrantee(n)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		out[i] = g.String()
	}
	return out, nil
}

// keepNames is grantees for a stored record: every name that parses canonically, and the rest
// as written.
func keepNames(_ string, names []string) ([]string, error) { return Canonical(names), nil }

func parseLevels(rec Payload, field string, read func(string, []string) ([]string, error)) (levels Levels, present bool, err error) {
	f := ir.Uncomment(Get(rec, field))
	if f == nil {
		return nil, false, nil
	}
	if f.Type != ir.ObjectType {
		return nil, true, fmt.Errorf("%s: grantees by level, `{read: [...]}`", field)
	}
	levels = Levels{}
	for name := range Fields(f) {
		l := Level(name)
		switch l {
		case Read, ReadWrite, OwnerLevel, Control:
		default:
			return nil, true, fmt.Errorf("%s: %q is not a level (read, read-write, owner, control)", field, name)
		}
		to := ir.Uncomment(Get(f, name))
		if to == nil || to.Type != ir.ArrayType {
			return nil, true, fmt.Errorf("%s.%s: a list of grantees", field, name)
		}
		names := Strs(f, name)
		if len(names) != len(to.Values) {
			return nil, true, fmt.Errorf("%s.%s: every grantee is a string", field, name)
		}
		if levels[l], err = read(field+"."+name, names); err != nil {
			return nil, true, err
		}
	}
	return levels, true, nil
}

// Payload spells the levels as a default's value.
func (l Levels) Payload() Payload {
	o := Obj{}
	for level, names := range l {
		o[string(level)] = strList(names)
	}
	return o.Payload()
}

// Grant is one line of a record's grants: a path the grant covers, and who holds which level
// there.
//
//   - read-write: [coderunner]            # the record's own node, and beneath it
//   - below: true                          # beneath the record's node, not the node itself
//     owner: ["*"]
//   - at: [spaces, scott, shared, "*"]     # somewhere else: a path, with at most a trailing *
//     read: [ana@example.com]
//
// A line with no `at` is about the node the record is on, which is what nearly every line is:
// the default copied at creation, a runner's grant on its task, a question's answerers. It is
// spelled without one so a record does not repeat its own address on every line. `below` on
// such a line covers the children and beneath — what a grant about creating needs — and not
// the node. `at` names any other path, with at most a TRAILING `*` for the same meaning: the
// claim grammar (source.ParseClaim), and for the same reason a claim refuses a `*` anywhere
// else, since a grant meaning something wider than it looks is authority resting on a typo.
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

// ParseGrants reads a record's grants field, for the record at node: a line with no `at` is
// about that node. A record with none has nil grants and no error; a line that cannot be read
// is an error naming it, so a policy edit that would silently grant nothing is refused where
// it is written rather than discovered where it is checked — a grantee the grammar does not read
// included. With no node — a caller holding a grants field that belongs nowhere yet — every line
// has to say where it is.
func ParseGrants(node []string, rec Payload) ([]Grant, error) {
	return parseGrants(node, rec, grantees)
}

// readGrants is ParseGrants over what a record already holds, keeping a name the grammar does not
// read as written (readLevels), so a record rewritten around one does not lose its other lines.
func readGrants(node []string, rec Payload) ([]Grant, error) {
	return parseGrants(node, rec, keepNames)
}

func parseGrants(node []string, rec Payload, read func(string, []string) ([]string, error)) ([]Grant, error) {
	f := ir.Uncomment(Get(rec, GrantsField))
	if f == nil {
		return nil, nil
	}
	if f.Type != ir.ArrayType {
		return nil, fmt.Errorf("%s: a list of grants, one `at` per line", GrantsField)
	}
	var out []Grant
	for i, v := range f.Values {
		g, err := parseGrant(node, v, read)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", GrantsField, i, err)
		}
		out = append(out, g)
	}
	return out, nil
}

func parseGrant(node []string, v Payload, read func(string, []string) ([]string, error)) (Grant, error) {
	v = ir.Uncomment(v)
	if v == nil || v.Type != ir.ObjectType {
		return Grant{}, fmt.Errorf("a grant is an object holding out a level, with `at` when it is not about this node")
	}
	var g Grant
	at := ir.Uncomment(Get(v, "at"))
	var segs []string
	switch {
	case at == nil && len(node) == 0:
		return Grant{}, fmt.Errorf("a grant names the path it covers in `at`")
	case at == nil:
		// This node. `below` is the only other thing such a line says about where.
		g.At = append([]string{}, node...)
		if b := ir.Uncomment(Get(v, "below")); b != nil {
			if b.Type != ir.BoolType {
				return Grant{}, fmt.Errorf("below: true or false")
			}
			g.Below = b.Bool
		}
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
	if at != nil && len(segs) == 0 {
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
	if at != nil {
		g.At = segs
	}
	for _, l := range []Level{Read, ReadWrite, OwnerLevel, Control} {
		to := ir.Uncomment(Get(v, string(l)))
		if to == nil {
			continue
		}
		if to.Type != ir.ArrayType {
			return Grant{}, fmt.Errorf("%s: a list of grantees — %s", l, Grammar())
		}
		names := Strs(v, string(l))
		if len(names) != len(to.Values) {
			return Grant{}, fmt.Errorf("%s: every grantee is a string", l)
		}
		kept, err := read(string(l), names)
		if err != nil {
			return Grant{}, err
		}
		if g.To == nil {
			g.To = map[Level][]string{}
		}
		g.To[l] = kept
	}
	if len(g.To) == 0 {
		return Grant{}, fmt.Errorf("a grant holds out a level: read, read-write, owner or control")
	}
	return g, nil
}

// Payload spells the grant back as a line of the record at node, the inverse of parseGrant: a
// grant about that node says no `at`, and `below: true` when it is about what is beneath it.
func (g Grant) Payload(node []string) Payload {
	o := Obj{}
	if len(node) > 0 && slices.Equal(g.At, node) {
		if g.Below {
			o["below"] = ir.FromBool(true)
		}
	} else {
		at := append([]string{}, g.At...)
		if g.Below {
			at = append(at, "*")
		}
		o["at"] = strList(at)
	}
	for l, names := range g.To {
		o[string(l)] = strList(names)
	}
	return o.Payload()
}

// GrantsPayload spells a list of grants as the field's value on the record at node.
func GrantsPayload(node []string, gs []Grant) Payload {
	arr := &ir.Node{Type: ir.ArrayType}
	for _, g := range gs {
		arr.Values = append(arr.Values, g.Payload(node))
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
