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
	// Answer admits a RESOLVER answering for whoever the node stands for: a place's asks, or a
	// person's at `verse.for.<person>` (9ce3bva0, stage 4). Like Control it stands beside the
	// record levels, is checked by the answer route and never by the store, and is held only by
	// being NAMED: owning the node confers nothing here, since a resolver acts as whoever
	// installed it, and an owner's own resolver would otherwise answer for them ungranted.
	Answer Level = "answer"
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
// Control and Answer are each satisfied only by themselves.
func (l Level) Admits(need Level) bool {
	if need == Control || need == Answer {
		return l == need
	}
	return l.rank() >= need.rank()
}

// ChildOwnerKey is how `owner` is HELD OUT: `child-owner: […]`, who may create beneath a node
// and own what they create. It is the one way ownership is ever granted, because a node has one
// owner and it is fixed when the node is created — by whoever created it, and never by somebody
// naming them. So there is no grant of `owner` at a node: nobody can be made the owner of
// anything except by their own write, which is what keeps a thing that acts (a rule, a source, a
// resolver) acting as whoever made it and as nobody its maker would rather it acted as.
//
// It always reaches what is BENEATH its path and never the path itself, so it takes no `below`.
// The level it confers is still OwnerLevel — what an owner holds — which is why a refusal says
// "needs owner"; this is only how that level is spelled where it is held out.
const ChildOwnerKey = "child-owner"

// levelKey is the key a level is written under in a grant line and a default.
func levelKey(l Level) string {
	if l == OwnerLevel {
		return ChildOwnerKey
	}
	return string(l)
}

// errOwnerHeldOut is what a line or a default naming `owner` is told.
func errOwnerHeldOut(where string) error {
	return fmt.Errorf("%s: `owner` is not something held out — a node has one owner, fixed when "+
		"it is created, by whoever created it. `%s: […]` is who may create beneath a node and "+
		"own what they create", where, ChildOwnerKey)
}

// GrantsField is the field of a perms record listing what its node's owner holds out.
const GrantsField = "grants"

// ChildGrantsField is the field of a perms record saying what a node CREATED BENEATH it holds
// out — the levels and grantees copied onto the new node's own record at creation, as a grant
// at the node. It is consulted where a record is written, which is where ownership changes and
// so where what a node holds out has to be decided; below an owner the owner's own grants
// cover everything. NAMED FOR WHAT IT IS, beside `child-owner`: that says who may own what is
// made beneath, this what each of those starts out holding out. It is copied once and is no
// fallback: changing it changes what is created afterwards and nothing that exists.
//
//	child-grants: {read: ["*"]}      # what is created beneath is readable by everyone
//	child-grants: {}                 # what is created beneath is held out to nobody
//
// Absent means inherit: the nearest `child-grants` up the tree, and the root's at the top.
const ChildGrantsField = "child-grants"

// Levels is what `child-grants` holds: grantees by level, the levels of a grant with no `at`.
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
		if l := field[strings.LastIndex(field, ".")+1:]; g.Kind == AudienceKind &&
			l != string(Read) && l != string(ReadWrite) {
			return nil, fmt.Errorf("%s: %q: an audience holds read or read-write, as it does at "+
				"its address; holding a place is not owning or controlling what is about it", field, n)
		} else if g.Kind == AnsweringKind && l != string(Read) {
			return nil, fmt.Errorf("%s: %q: whoever answers for a place reads what is put to it, "+
				"and holds nothing more by it", field, n)
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
		switch {
		case name == ChildOwnerKey:
			l = OwnerLevel
		case l == OwnerLevel:
			return nil, true, errOwnerHeldOut(field)
		case l == Read, l == ReadWrite, l == Control, l == Answer:
		default:
			return nil, true, fmt.Errorf("%s: %q is not a level (read, read-write, %s, control, answer)", field, name, ChildOwnerKey)
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
		o[levelKey(level)] = strList(names)
	}
	return o.Payload()
}

// Grant is one line of a record's grants: a path the grant covers, and who holds which level
// there.
//
//   - read-write: [verse-runner]            # the record's own node, and beneath it
//   - below: true                          # beneath the record's node, not the node itself
//     owner: ["*"]
//   - at: [spaces, walter, shared, "*"]     # somewhere else: a path, with at most a trailing *
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
	if g.Below && len(segs) == len(g.At) {
		return false
	}
	return Under(g.At, segs, Literal)
}

// ParseGrants reads a record's grants field, for the record at node — which a caller that has
// one passes non-nil, the root included (`SegsOf`), since a line with no `at` is about it. A record with none has nil grants and no error; a line that cannot be read
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
		gs, err := parseGrant(node, v, read)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", GrantsField, i, err)
		}
		out = append(out, gs...)
	}
	return out, nil
}

// parseGrant reads one line, which is two grants where it holds out `child-owner` beside a level
// that is about the node itself: the first reaches what is beneath and the second does not.
func parseGrant(node []string, v Payload, read func(string, []string) ([]string, error)) ([]Grant, error) {
	g, owners, err := parseLine(node, v, read)
	if err != nil {
		return nil, err
	}
	switch {
	case owners == nil:
		return []Grant{g}, nil
	case len(g.To) == 0 || g.Below:
		if g.To == nil {
			g.To = map[Level][]string{}
		}
		g.To[OwnerLevel], g.Below = owners, true
		return []Grant{g}, nil
	}
	return []Grant{{At: g.At, Below: true, To: map[Level][]string{OwnerLevel: owners}}, g}, nil
}

// parseLine is a line's path, its levels other than `child-owner`, and who holds that one.
func parseLine(node []string, v Payload, read func(string, []string) ([]string, error)) (Grant, []string, error) {
	v = ir.Uncomment(v)
	if v == nil || v.Type != ir.ObjectType {
		return Grant{}, nil, fmt.Errorf("a grant is an object holding out a level, with `at` when it is not about this node")
	}
	var g Grant
	at := ir.Uncomment(Get(v, "at"))
	var segs []string
	switch {
	// NO NODE is a caller holding a grants field that belongs nowhere yet — nil, not empty. A
	// record ON the root has a node, and it is the empty path: `Segs` of the root is no
	// segments, which is why the two are told apart by nil and not by length.
	case at == nil && node == nil:
		return Grant{}, nil, fmt.Errorf("a grant names the path it covers in `at`")
	case at == nil:
		// This node. `below` is the only other thing such a line says about where.
		g.At = append([]string{}, node...)
		if b := ir.Uncomment(Get(v, "below")); b != nil {
			if b.Type != ir.BoolType {
				return Grant{}, nil, fmt.Errorf("below: true or false")
			}
			g.Below = b.Bool
		}
	case at.Type == ir.StringType:
		var err error
		if segs, err = ExprSegs(at.String); err != nil {
			return Grant{}, nil, fmt.Errorf("at: %w", err)
		}
	case at.Type == ir.ArrayType:
		segs = Strs(v, "at")
		if len(segs) != len(at.Values) {
			return Grant{}, nil, fmt.Errorf("at: every segment is a name")
		}
	default:
		return Grant{}, nil, fmt.Errorf("at: a path, spelled as a list or a kinded path")
	}
	// An `at` that spells NO segments is the root, because the empty path is the root — which
	// is the one node a record's own address cannot say for it (`at: []`, the list with nothing
	// in it). A missing `at` is the case above and means this node; a malformed one was refused
	// already.
	if at != nil && at.Type != ir.ArrayType && len(segs) == 0 {
		return Grant{}, nil, fmt.Errorf("at: a path, spelled as a list or a kinded path")
	}
	for i, s := range segs {
		if s == "" {
			return Grant{}, nil, fmt.Errorf("at: a segment is a name, and none may be empty")
		}
		if strings.Contains(s, "*") {
			if s != "*" || i != len(segs)-1 {
				return Grant{}, nil, fmt.Errorf("at: a `*` is a whole TRAILING segment, covering what is " +
					"beneath the path — a `*` anywhere else is refused rather than approximated")
			}
			g.Below = true
			segs = segs[:i]
		}
	}
	if at != nil {
		g.At = segs
	}
	if Get(v, string(OwnerLevel)) != nil {
		return Grant{}, nil, errOwnerHeldOut("a grant")
	}
	var owners []string
	for _, l := range []Level{Read, ReadWrite, OwnerLevel, Control, Answer} {
		key := levelKey(l)
		to := ir.Uncomment(Get(v, key))
		if to == nil {
			continue
		}
		if to.Type != ir.ArrayType {
			return Grant{}, nil, fmt.Errorf("%s: a list of grantees — %s", key, Grammar())
		}
		names := Strs(v, key)
		if len(names) != len(to.Values) {
			return Grant{}, nil, fmt.Errorf("%s: every grantee is a string", key)
		}
		kept, err := read(key, names)
		if err != nil {
			return Grant{}, nil, err
		}
		if l == OwnerLevel {
			owners = kept
			continue
		}
		if g.To == nil {
			g.To = map[Level][]string{}
		}
		g.To[l] = kept
	}
	if len(g.To) == 0 && owners == nil {
		return Grant{}, nil, fmt.Errorf("a grant holds out a level: read, read-write, %s, control or answer", ChildOwnerKey)
	}
	return g, owners, nil
}

// Payload spells the grant back as a line of the record at node, the inverse of parseGrant: a
// grant about that node says no `at`, and `below: true` when it is about what is beneath it.
func (g Grant) Payload(node []string) Payload {
	o := Obj{}
	// `child-owner` says beneath by itself, so a line holding out nothing else says no `below`.
	_, owns := g.To[OwnerLevel]
	below := g.Below && !(owns && len(g.To) == 1)
	if len(node) > 0 && slices.Equal(g.At, node) {
		if below {
			o["below"] = ir.FromBool(true)
		}
	} else {
		at := append([]string{}, g.At...)
		if below {
			at = append(at, "*")
		}
		o["at"] = strList(at)
	}
	for l, names := range g.To {
		o[levelKey(l)] = strList(names)
	}
	return o.Payload()
}

// check refuses a grant the model has no meaning for: `owner` held out AT a node. It is what a
// caller building grants in code is told, where a parsed line was told already.
func (g Grant) check() error {
	if _, owns := g.To[OwnerLevel]; owns && !g.Below {
		return errOwnerHeldOut("a grant at " + JoinSegs(g.At))
	}
	return nil
}

// checkGrants is check over a list: what an owner may set as the grants of their own node's
// record, which is what a creator may give the node they create (Write.Grants).
func checkGrants(gs []Grant) error {
	for _, g := range gs {
		if err := g.check(); err != nil {
			return err
		}
	}
	return nil
}

// GrantsPayload spells a list of grants as the field's value on the record at node.
func GrantsPayload(node []string, gs []Grant) Payload {
	arr := &ir.Node{Type: ir.ArrayType}
	for _, g := range gs {
		arr.Values = append(arr.Values, g.Payload(node))
	}
	return arr
}

// GrantsOver spells a list of grants so that writing it REPLACES the list standing in cur, in
// one write. A write of an array merges it by position and truncates: line i of the new list is
// merged onto line i of the old, so a line that lost a level, or a line that moved up because
// the one above it went, keeps whatever stood at that position — a revoke that reported success
// and revoked nothing, and a grant that migrated onto another line's path. Each line therefore
// says what to REMOVE from the line it lands on, which is every field that line had and this
// one does not.
//
// One write rather than a removal and then a write, for two reasons that are the same reason: a
// record with an owner and no grants field at all reads as one from before defaults existed and
// holds out the root's default, so the moment between the two would be a private place readable
// by everyone — for good, if the second write never landed. And a caller guarding its
// write with Exactly(cur) cannot make a change before it.
func GrantsOver(node []string, next []Grant, cur Payload) Payload {
	arr := GrantsPayload(node, next)
	old := ir.Uncomment(Get(cur, GrantsField))
	if old == nil || old.Type != ir.ArrayType {
		return arr
	}
	for i := range arr.Values {
		if i < len(old.Values) {
			arr.Values[i] = withRemovals(arr.Values[i], ir.Uncomment(old.Values[i]))
		}
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

// SegsOf is a node's segments as a grants parse wants them: never nil, so the ROOT — whose
// address is the empty path — is told from a caller that has no node at all.
func SegsOf(ref Ref) []string { return append([]string{}, ref.Segs()...) }
