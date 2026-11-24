// Package charterfile is how a charter is read back and handed in, by `viff charter`, viff
// mcp's viff_charter and viff_charter_check, and the viff system's viff-charter-get and
// viff-charter-check alike: one way to read GET /charter's tree as the files that install it,
// to list what is installed, and to read a text of charters, so a task reads the charters as its
// owner would.
//
// WHAT `viff c` PRINTS IS WHAT `viff c -a` INSTALLS, one charter at a time. A FILE IS ONE
// CHARTER: a fielded document whose keys are `at`, `reads`, `sends`, `ttl`, `rules`, `schedules`
// and `strata` — the node the server's tree holds for one address, less what a file cannot
// install. `writes:` is the address plus `.*`. `children:` is what a directory is for. The marks a
// listing puts on a rule — held, suspended, in trouble, installed by a task — are state, and are
// said as notes beside the file: as comments they would install as the rule's reason, and a round
// trip would bake "held" into the author's text. So is when a charter with a `ttl:` expires: the ttl is the
// author's and installs back as written, renewing it, and the instant is the install's.
package charterfile

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/schedule"
)

// IsNode reports whether a document is a node of the charter tree — a place or a charter — rather
// than a reach into one (a rule, a list of them).
func IsNode(n *ir.Node) bool {
	return field(n, "writes") != nil || field(n, "children") != nil
}

// IsDoc reports whether a document is a fielded charter rather than the shorthand: an object with
// a charter's keys and none of a definition's.
func IsDoc(n *ir.Node) bool {
	n = codec.Unwrap(n)
	if n == nil || n.Type != ir.ObjectType {
		return false
	}
	charter := false
	for _, key := range n.Fields {
		if key == nil || key.Type != ir.StringType {
			continue
		}
		switch key.String {
		case "condition", "level", "every":
			return false
		case "at", "reads", "sends", "ttl", "rules", "schedules", "strata":
			charter = true
		}
	}
	return charter
}

// Defs is the definitions a document holds: one, or each element of a list.
//
// SENT ONE DOCUMENT APIECE rather than as the list: an element of a list is re-rendered one
// indent in on the way out, and a comment beside a flow map is lost on the way back through the
// parser. A definition sent as a document is what the file wrote.
//
// UNWRAPPED TO LOOK AT, KEPT AS IT WAS TO SEND: a comment wraps the value it precedes, and it is
// the author's reason for the rule (trigger.Trigger's Doc field). A `!key(name)` tag on the list
// is carried through and ignored — it is an array either way.
func Defs(doc *ir.Node) []*ir.Node {
	inner := codec.Unwrap(doc)
	if inner == nil || inner.Type == ir.NullType {
		return nil
	}
	if inner.Type != ir.ArrayType {
		return []*ir.Node{doc}
	}
	var defs []*ir.Node
	for _, elem := range inner.Values {
		if in := codec.Unwrap(elem); in != nil && in.Type != ir.NullType {
			defs = append(defs, elem)
		}
	}
	return defs
}

// Only refuses what a tree of charters may not hold: a document that is no charter, and two
// charters at one address. names[i] is what to call docs[i] in the refusal.
func Only(docs []*ir.Node, names []string) error {
	byAt := map[string]string{}
	for i, doc := range docs {
		if !IsDoc(doc) {
			return fmt.Errorf("%s is no charter: a charter says `at:`, `reads:`, `sends:`, `ttl:`, "+
				"`rules:`, `schedules:` or `strata:`", names[i])
		}
		addr := str(doc, "at")
		if prev, ok := byAt[addr]; ok {
			where := "baseline"
			if addr != "" {
				where = addr
			}
			return fmt.Errorf("%s and %s are both the charter at %s", prev, names[i], where)
		}
		byAt[addr] = names[i]
	}
	return nil
}

// Text is the documents of a text of charters, read in the format in, and what to call each: its
// place in the text, a null document counted.
func Text(text string, in codec.Format) ([]*ir.Node, []string, error) {
	parsed, err := codec.ParseNodes([]byte(text), in)
	if err != nil {
		return nil, nil, fmt.Errorf("charters: %w", err)
	}
	var docs []*ir.Node
	var names []string
	for i, d := range parsed {
		if in := codec.Unwrap(d); in == nil || in.Type == ir.NullType {
			continue
		}
		docs, names = append(docs, d), append(names, fmt.Sprintf("document %d", i+1))
	}
	return docs, names, nil
}

// CheckDocs is the charters a check is given as text, read in the format in, refused as a
// directory of them would be.
func CheckDocs(text string, in codec.Format) ([]*ir.Node, error) {
	docs, names, err := Text(text, in)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("charters: there is none to check")
	}
	if err := Only(docs, names); err != nil {
		return nil, fmt.Errorf("charters: %w", err)
	}
	return docs, nil
}

// Query is the read of the charter tree at a path of names, depth levels deep (-1 for all), as
// GET /charter takes it, and the path whole, which a listing prefixes its lines with. at is how
// many of the names are the address, or -1 to leave that split to the server.
func Query(names []string, at, depth int) (url.Values, string) {
	q := url.Values{}
	addr, reach := SplitAt(names, at)
	if at >= 0 {
		q.Set("at", entity.JoinSegs(addr))
	}
	if len(reach) > 0 {
		q.Set("path", entity.JoinSegs(reach))
	}
	if depth >= 0 {
		q.Set("depth", fmt.Sprint(depth))
	}
	return q, entity.JoinSegs(names)
}

// SplitAt is a path of names as the address and what reaches into it: the first at names, or with
// at -1 none, the split being the server's.
func SplitAt(names []string, at int) (addr, reach []string) {
	if at < 0 {
		return nil, names
	}
	return names[:at], names[at:]
}

// Paths is what is installed at the path at, one address per rule and level, each a path the
// read takes back, from the node GET /charter answered there.
//
// A NODE THAT HOLDS NOTHING LISTS ITSELF, which is the case a listing of rules alone would hide: a
// package whose levels installed and whose rules were refused is still a package, and one that
// lists as nothing is one nobody can see to take down.
func Paths(node *ir.Node, at string) ([]entity.Path, error) {
	// The path asked at is the prefix of everything listed: the answer's keys are relative to the
	// node it answered, and a path that does not say where it starts reads back the wrong
	// charter (or none).
	prefix, err := AddrSegs(at)
	if err != nil {
		return nil, err
	}
	var out []entity.Path
	// A LIST is what a path into `rules` or `strata` answers, and its elements are named rather
	// than nested — so the prefix already says which kind they are, and each line is one name
	// away from being the path that reaches it.
	if n := codec.Unwrap(node); n != nil && n.Type == ir.ArrayType {
		for _, el := range n.Values {
			if name := str(el, "name"); name != "" {
				out = append(out, entity.RefAt(append(append([]string{}, prefix...), name)...).At())
			}
		}
		return out, nil
	}
	return walk(out, node, prefix), nil
}

// walk adds one address per rule and level under a node, and the node's own when it holds
// neither.
func walk(out []entity.Path, node *ir.Node, at []string) []entity.Path {
	n := codec.Unwrap(node)
	if n == nil || n.Type != ir.ObjectType {
		return out
	}
	here, listed := false, false
	var children *ir.Node
	for i, key := range n.Fields {
		if key == nil || key.Type != ir.StringType {
			continue
		}
		v := codec.Unwrap(n.Values[i])
		switch key.String {
		case "writes":
			here = true
		case "rules", "strata":
			if v == nil || v.Type != ir.ArrayType {
				continue
			}
			for _, el := range v.Values {
				if name := str(el, "name"); name != "" {
					out = append(out, entity.RefAt(append(append([]string{}, at...), key.String, name)...).At())
					listed = true
				}
			}
		case "children":
			children = v
		}
	}
	if here && !listed && len(at) > 0 {
		out = append(out, entity.RefAt(at...).At())
	}
	if children == nil || children.Type != ir.ObjectType {
		return out
	}
	for i, seg := range children.Fields {
		if seg == nil || seg.Type != ir.StringType {
			continue
		}
		out = walk(out, children.Values[i], append(append([]string{}, at...), seg.String))
	}
	return out
}

// Charter is one charter the tree holds, at its address.
type Charter struct {
	Segs []string
	Node *ir.Node
}

// Addr is the charter's address as its file's `at:` spells it.
func (c Charter) Addr() string { return entity.JoinSegs(c.Segs) }

// Charters lists the charters a tree node holds, the node's own first, then what is under it in
// the tree's order.
func Charters(node *ir.Node, segs []string) []Charter {
	var out []Charter
	if field(node, "writes") != nil {
		out = append(out, Charter{Segs: segs, Node: node})
	}
	kids := codec.Unwrap(field(node, "children"))
	if kids == nil || kids.Type != ir.ObjectType {
		return out
	}
	for i, seg := range kids.Fields {
		if seg == nil || seg.Type != ir.StringType {
			continue
		}
		out = append(out, Charters(kids.Values[i], append(append([]string{}, segs...), seg.String))...)
	}
	return out
}

// Read is the charter at the address full, from the tree GET /charter answered there, as the file
// that installs it, the notes that say what the file cannot (the marks on its rules), and the
// addresses of the charters under it, each read at its own path. A notes's address, and those
// under, are kinded paths where kpaths, and the steps a command line takes otherwise. In a scope
// a file carries no `at:`, since the scope's charter is its whole copy rather than a place in the
// tree.
func Read(tree *ir.Node, full, scope string, kpaths bool) (files []*ir.Node, notes, under []string, err error) {
	segs, err := AddrSegs(full)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, c := range Charters(tree, segs) {
		if len(c.Segs) == len(segs) {
			file, said := File(c.Node, FileAt(c, scope), kpaths)
			files, notes = append(files, file), append(notes, said...)
			continue
		}
		under = append(under, Spelled(c.Addr(), kpaths))
	}
	return files, notes, under, nil
}

// UnderNote is the note a reader of kinded paths is given about the charters under here: each is
// read at its own path.
func UnderNote(here string, under []string) string {
	return fmt.Sprintf("note: under %s: %s — each is read at its own path", here, strings.Join(under, ", "))
}

// FileAt is the `at:` a charter's file carries: its address, or none for baseline — and none in a
// scope.
func FileAt(c Charter, scope string) string {
	if scope != "" {
		return ""
	}
	return c.Addr()
}

// File renders the charter a tree node holds as the file that installs it at `at`, and the notes
// that say what the node carried that a file cannot, naming the address as kpaths spells it.
func File(node *ir.Node, at string, kpaths bool) (*ir.Node, []string) {
	var fields, values []*ir.Node
	add := func(key string, v *ir.Node) {
		fields = append(fields, ir.FromString(key))
		values = append(values, v)
	}
	if at != "" {
		add("at", ir.FromString(at))
	}
	if reads := field(node, "reads"); reads != nil {
		add("reads", reads)
	}
	if sends := field(node, "sends"); sends != nil {
		add("sends", sends)
	}
	var notes []string
	if ttl := field(node, "ttl"); ttl != nil {
		add("ttl", ttl)
		if expires := str(node, "expires"); expires != "" {
			notes = append(notes, fmt.Sprintf("note: %s: its ttl ends at %s, and it is removed then; "+
				"installed again, it stands its ttl from then", Spelled(at, kpaths), expires))
		}
	}
	if rules := codec.Unwrap(field(node, "rules")); rules != nil && rules.Type == ir.ArrayType && len(rules.Values) > 0 {
		out := make([]*ir.Node, 0, len(rules.Values))
		for _, r := range rules.Values {
			r, said := unmarked(r, at, kpaths)
			out = append(out, r)
			notes = append(notes, said...)
		}
		add("rules", ir.FromSlice(out))
	}
	if rows := codec.Unwrap(field(node, "schedules")); rows != nil && rows.Type == ir.ArrayType && len(rows.Values) > 0 {
		out := make([]*ir.Node, 0, len(rows.Values))
		for _, row := range rows.Values {
			out = append(out, scheduleDecl(row))
		}
		add("schedules", ir.FromSlice(out))
	}
	if strata := codec.Unwrap(field(node, "strata")); strata != nil && strata.Type == ir.ArrayType && len(strata.Values) > 0 {
		add("strata", strata)
	}
	return &ir.Node{Type: ir.ObjectType, Fields: fields, Values: values}, notes
}

// Spelled is a charter's address as a reader is given it: a kinded path where kpaths, the steps a
// command line takes otherwise; baseline's is "baseline".
func Spelled(at string, kpaths bool) string {
	switch {
	case at == "":
		return "baseline"
	case kpaths:
		return at
	}
	return Steps(at)
}

// Steps renders a charter address as the steps a command line takes, or as it stands when it is
// not a plain path.
func Steps(at string) string {
	segs, err := entity.PathSegs(at)
	if err != nil || len(segs) == 0 {
		return at
	}
	return entity.JoinSteps(segs)
}

// markKeys are the fields a listing adds to a rule that are not the rule: what the engine knows
// about it (internal/viffd/suspend.go).
var markKeys = map[string]bool{"held": true, "suspended": true, "trouble": true, "task": true}

// unmarked is a rule without the listing's marks, and the marks as notes naming the charter's
// address as kpaths spells it.
func unmarked(rule *ir.Node, at string, kpaths bool) (*ir.Node, []string) {
	rule = rule.Clone()
	inner := codec.Unwrap(rule)
	if inner == nil || inner.Type != ir.ObjectType {
		return rule, nil
	}
	where := Spelled(at, kpaths)
	name := str(inner, "name")
	var notes []string
	var fields, values []*ir.Node
	for i, key := range inner.Fields {
		if key == nil || key.Type != ir.StringType || !markKeys[key.String] {
			fields = append(fields, key)
			values = append(values, inner.Values[i])
			continue
		}
		v := codec.Unwrap(inner.Values[i])
		switch key.String {
		case "held":
			notes = append(notes, fmt.Sprintf("note: %s: rule %s is held: %s", where, name, v.String))
		case "suspended":
			notes = append(notes, fmt.Sprintf("note: %s: rule %s is suspended by %s", where, name,
				strings.Join(strValues(v), ", ")))
		case "task":
			notes = append(notes, fmt.Sprintf("note: %s: rule %s was installed by task %s: what it "+
				"spawns stays within that task, less installing charters", where, name, v.String))
		case "trouble":
			if v == nil || v.Type != ir.ArrayType {
				continue
			}
			for _, t := range v.Values {
				line := fmt.Sprintf("note: %s: rule %s: %s ×%d, last at %s: %s", where, name,
					str(t, "kind"), num(t, "count"), str(t, "at"), str(t, "why"))
				if c := str(t, "chain"); c != "" {
					line += " (in " + c + ")"
				}
				if s := str(t, "subject"); s != "" {
					line += " about " + s
				}
				notes = append(notes, line)
			}
		}
	}
	inner.Fields, inner.Values = fields, values
	return rule, notes
}

// scheduleDecl is a schedule as the charter declares it, from the row `viff schedule` lists: its
// name, period and phase. When it last ticked, and by whose install, is `viff schedule`'s.
//
// The row's `id` is the record's, which carries a package's address (`drift:hourly`); the name the
// file declares is its tail, and the `at:` beside it puts the address back.
func scheduleDecl(row *ir.Node) *ir.Node {
	var fields, values []*ir.Node
	for _, k := range []string{"every", "from"} {
		if v := field(row, k); v != nil {
			fields = append(fields, ir.FromString(k))
			values = append(values, v)
		}
	}
	if id := str(row, "id"); id != "" {
		fields = append(fields, ir.FromString("name"))
		values = append(values, ir.FromString(schedule.NameOf(id)))
	}
	return &ir.Node{Type: ir.ObjectType, Fields: fields, Values: values}
}

// AddrSegs splits an address the way the server does: baseline's, "", is no segments.
func AddrSegs(at string) ([]string, error) {
	if at == "" {
		return nil, nil
	}
	return entity.PathSegs(at)
}

// strValues is a list of strings off a node.
func strValues(n *ir.Node) []string {
	if n == nil || n.Type != ir.ArrayType {
		return nil
	}
	var out []string
	for _, v := range n.Values {
		if v = codec.Unwrap(v); v != nil && v.Type == ir.StringType {
			out = append(out, v.String)
		}
	}
	return out
}

func field(n *ir.Node, name string) *ir.Node {
	n = codec.Unwrap(n)
	if n == nil || n.Type != ir.ObjectType {
		return nil
	}
	for i, key := range n.Fields {
		if key != nil && key.Type == ir.StringType && key.String == name {
			return n.Values[i]
		}
	}
	return nil
}

func str(n *ir.Node, name string) string {
	if v := codec.Unwrap(field(n, name)); v != nil && v.Type == ir.StringType {
		return v.String
	}
	return ""
}

func boolean(n *ir.Node, name string) bool {
	v := codec.Unwrap(field(n, name))
	return v != nil && v.Type == ir.BoolType && v.Bool
}

// num reads a number off the NODE, which is not what codec.AsInt64 does: that takes a Go value,
// and handing it an *ir.Node answers 0 for every number there is.
func num(n *ir.Node, name string) int64 {
	v := codec.Unwrap(field(n, name))
	if v == nil || v.Type != ir.NumberType {
		return 0
	}
	if v.Int64 != nil {
		return *v.Int64
	}
	i, err := strconv.ParseInt(v.Number, 10, 64)
	if err != nil {
		return 0
	}
	return i
}
