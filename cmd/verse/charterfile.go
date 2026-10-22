package main

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/schedule"
)

// WHAT `verse c` PRINTS IS WHAT `verse c -a` INSTALLS, one charter at a time:
//
//	verse c ask > ask.tony          the charter at ask, `at: ask` and all
//	verse c -a -f ask.tony          …installs it back where it came from
//
//	verse c verse-dev -d out/       every charter at or under verse-dev, a file apiece
//	verse c -a -d out/              …installs them all, each at its own `at:`, in one step
//	verse c -a verse-dev -d out/ --prune   …and removes what out/ leaves out under verse-dev
//
// A FILE IS ONE CHARTER: a fielded document whose keys are `at`, `reads`, `rules`, `schedules`
// and `strata` — the node the server's tree holds for one address, less what a file cannot
// install. `writes:` is the address plus `.*`. `children:` is what a directory is for. The marks
// a listing puts on a rule — held, suspended, in trouble — are state, and go to stderr: as
// comments they would install as the rule's reason, and a round trip would bake "held" into the
// author's text.
//
// A TREE IS A DIRECTORY, and the file's `at:` is the address, not where the file sits. A rule
// names absolute paths, so a layout cannot move a charter and should not look as if it could;
// the writer's layout (`<address as directories>/charter.tony`) is a default, and a directory
// laid out any other way — by project, with READMEs beside — installs the same.

// isCharterNode reports whether a document is a node of the charter tree — a place or a charter
// — rather than a reach into one (a rule, a list of them).
func isCharterNode(n *ir.Node) bool {
	return nodeField(n, "writes") != nil || nodeField(n, "children") != nil
}

// charterFile renders the charter a tree node holds as the file that installs it at `at`, and
// the notes that say what the node carried that a file cannot.
func charterFile(node *ir.Node, at string, sp spelling) (*ir.Node, []string) {
	var fields, values []*ir.Node
	add := func(key string, v *ir.Node) {
		fields = append(fields, ir.FromString(key))
		values = append(values, v)
	}
	if at != "" {
		add("at", ir.FromString(at))
	}
	if reads := nodeField(node, "reads"); reads != nil {
		add("reads", reads)
	}
	if sends := nodeField(node, "sends"); sends != nil {
		add("sends", sends)
	}
	var notes []string
	if rules := codec.Unwrap(nodeField(node, "rules")); rules != nil && rules.Type == ir.ArrayType && len(rules.Values) > 0 {
		out := make([]*ir.Node, 0, len(rules.Values))
		for _, r := range rules.Values {
			r, said := unmarked(r, at, sp)
			out = append(out, r)
			notes = append(notes, said...)
		}
		add("rules", ir.FromSlice(out))
	}
	if rows := codec.Unwrap(nodeField(node, "schedules")); rows != nil && rows.Type == ir.ArrayType && len(rows.Values) > 0 {
		out := make([]*ir.Node, 0, len(rows.Values))
		for _, row := range rows.Values {
			out = append(out, scheduleDecl(row))
		}
		add("schedules", ir.FromSlice(out))
	}
	if strata := codec.Unwrap(nodeField(node, "strata")); strata != nil && strata.Type == ir.ArrayType && len(strata.Values) > 0 {
		add("strata", strata)
	}
	return &ir.Node{Type: ir.ObjectType, Fields: fields, Values: values}, notes
}

// markKeys are the fields a listing adds to a rule that are not the rule: what the engine knows
// about it (versed/suspend.go).
var markKeys = map[string]bool{"held": true, "suspended": true, "trouble": true}

// spelling is how a note names a charter's address: as the steps the command line takes, or as
// the kinded path a tool takes.
type spelling int

const (
	asSteps spelling = iota
	asKPaths
)

// of is a charter's address, as it is spelled; baseline's is "baseline".
func (sp spelling) of(at string) string {
	switch {
	case at == "":
		return "baseline"
	case sp == asKPaths:
		return at
	}
	return stepsOf(at)
}

// tree and file are what a prune compares with, for the reader sp spells for: the command's
// directory and its files, or the tool's charters.
func (sp spelling) tree() string {
	if sp == asKPaths {
		return "the charters given"
	}
	return "the directory"
}

func (sp spelling) file() string {
	if sp == asKPaths {
		return "its charter"
	}
	return "its file"
}

// charters is what reads every charter, for the reader sp spells for.
func (sp spelling) charters() string {
	if sp == asKPaths {
		return "verse_charter"
	}
	return "`verse c`"
}

// unmarked is a rule without the listing's marks, and the marks as notes naming the charter's
// address as sp spells it.
func unmarked(rule *ir.Node, at string, sp spelling) (*ir.Node, []string) {
	rule = rule.Clone()
	inner := codec.Unwrap(rule)
	if inner == nil || inner.Type != ir.ObjectType {
		return rule, nil
	}
	where := sp.of(at)
	name := nodeStr(inner, "name")
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
			notes = append(notes, fmt.Sprintf("note: %s: rule %s is held: it waits for stratum %s, "+
				"which is not installed", where, name, v.String))
		case "suspended":
			notes = append(notes, fmt.Sprintf("note: %s: rule %s is suspended by %s", where, name,
				strings.Join(strValues(v), ", ")))
		case "trouble":
			if v == nil || v.Type != ir.ArrayType {
				continue
			}
			for _, t := range v.Values {
				line := fmt.Sprintf("note: %s: rule %s: %s ×%d, last at %s: %s", where, name,
					nodeStr(t, "kind"), nodeInt(t, "count"), nodeStr(t, "at"), nodeStr(t, "why"))
				if c := nodeStr(t, "chain"); c != "" {
					line += " (in " + c + ")"
				}
				if s := nodeStr(t, "subject"); s != "" {
					line += " about " + s
				}
				notes = append(notes, line)
			}
		}
	}
	inner.Fields, inner.Values = fields, values
	return rule, notes
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

// scheduleDecl is a schedule as the charter declares it, from the row `verse schedule` lists:
// its name, period and phase. When it last ticked, and by whose install, is `verse schedule`'s.
//
// The row's `id` is the record's, which carries a package's address (`drift:hourly`); the name
// the file declares is its tail, and the `at:` beside it puts the address back.
func scheduleDecl(row *ir.Node) *ir.Node {
	var fields, values []*ir.Node
	for _, k := range []string{"every", "from"} {
		if v := nodeField(row, k); v != nil {
			fields = append(fields, ir.FromString(k))
			values = append(values, v)
		}
	}
	if id := nodeStr(row, "id"); id != "" {
		fields = append(fields, ir.FromString("name"))
		values = append(values, ir.FromString(schedule.NameOf(id)))
	}
	return &ir.Node{Type: ir.ObjectType, Fields: fields, Values: values}
}

// charterAt is one charter the tree holds, at its address.
type charterAt struct {
	segs []string
	node *ir.Node
}

// addr is the charter's address as its file's `at:` spells it.
func (c charterAt) addr() string { return entity.JoinSegs(c.segs) }

// treeCharters lists the charters a tree node holds, the node's own first, then what is under
// it in the tree's order.
func treeCharters(node *ir.Node, segs []string) []charterAt {
	var out []charterAt
	if nodeField(node, "writes") != nil {
		out = append(out, charterAt{segs: segs, node: node})
	}
	kids := codec.Unwrap(nodeField(node, "children"))
	if kids == nil || kids.Type != ir.ObjectType {
		return out
	}
	for i, seg := range kids.Fields {
		if seg == nil || seg.Type != ir.StringType {
			continue
		}
		out = append(out, treeCharters(kids.Values[i], append(append([]string{}, segs...), seg.String))...)
	}
	return out
}

// renderCharterFile writes the charter at the address asked to w, as its file, and says to notes
// what the file cannot: the marks on its rules, and the charters under it, spelled as sp spells
// an address.
func renderCharterFile(w, notesTo io.Writer, tree *ir.Node, full string, f filters, out codec.Format, color bool, sp spelling) error {
	segs, err := addrSegs(full)
	if err != nil {
		return err
	}
	all := treeCharters(tree, segs)
	var notes, under []string
	for _, c := range all {
		if len(c.segs) == len(segs) {
			file, said := charterFile(c.node, fileAt(c), sp)
			if err := renderIR(w, file, f, out, color); err != nil {
				return err
			}
			notes = append(notes, said...)
			continue
		}
		under = append(under, sp.of(c.addr()))
	}
	for _, n := range notes {
		fmt.Fprintln(notesTo, n)
	}
	if len(under) == 0 {
		return nil
	}
	here := "here"
	if full != "" {
		here = sp.of(full)
	}
	switch sp {
	case asKPaths:
		fmt.Fprintf(notesTo, "note: under %s: %s — each is read at its own path\n", here, strings.Join(under, ", "))
	default:
		cmd := "verse c -d <dir>"
		if full != "" {
			cmd = "verse c " + here + " -d <dir>"
		}
		fmt.Fprintf(notesTo, "note: under %s: %s — `%s` writes every charter to a file of its own\n",
			here, strings.Join(under, ", "), cmd)
	}
	return nil
}

// fileAt is the `at:` a charter's file carries: its address, or none for baseline — and none in
// a scope, whose charter is the scope's whole copy rather than a place in this tree.
func fileAt(c charterAt) string {
	if g.Scope != "" {
		return ""
	}
	return c.addr()
}

// addrSegs splits an address the way the server does.
func addrSegs(at string) ([]string, error) {
	if at == "" {
		return nil, nil
	}
	return entity.PathSegs(at)
}

// charterFileName is the file the writer gives a charter in its directory.
const charterFileName = "charter"

// writeCharterDir writes every charter at or under the address asked into dir, one file apiece
// at `<address as directories>/charter<suffix>`. Every path is worked out before anything is
// written, so an address the writer cannot spell as a directory refuses the whole write.
func writeCharterDir(cc *cli.Context, tree *ir.Node, full, dir string) error {
	segs, err := addrSegs(full)
	if err != nil {
		return err
	}
	out, err := outFmt()
	if err != nil {
		return err
	}
	type planned struct {
		path string
		file *ir.Node
	}
	var plan []planned
	var notes []string
	for _, c := range treeCharters(tree, segs) {
		parts := []string{dir}
		for _, seg := range c.segs {
			if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "/\\\x00") {
				return fmt.Errorf("charter -d: the charter at %s has an address segment %q that is "+
					"not a directory name; write it on its own with `verse c %s > <file>`",
					stepsOf(c.addr()), seg, stepsOf(c.addr()))
			}
			parts = append(parts, seg)
		}
		parts = append(parts, charterFileName+out.Suffix())
		file, said := charterFile(c.node, fileAt(c), asSteps)
		if len(file.Fields) == 0 {
			continue // baseline with nothing in it: `{}` installs nothing and says nothing
		}
		plan = append(plan, planned{path: filepath.Join(parts...), file: file})
		notes = append(notes, said...)
	}
	if len(plan) == 0 {
		return fmt.Errorf("charter -d: no charter stands at or under %s", stepsOf(full))
	}
	for _, p := range plan {
		if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
			return fmt.Errorf("charter -d: %w", err)
		}
		f, err := os.Create(p.path)
		if err != nil {
			return fmt.Errorf("charter -d: %w", err)
		}
		err = codec.EncodeNode(f, p.file, out)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("charter -d: %s: %w", p.path, err)
		}
		fmt.Fprintln(cc.Out, p.path)
	}
	for _, n := range notes {
		fmt.Fprintln(cc.Err, n)
	}
	return nil
}

// isCharterDoc reports whether a document is a fielded charter rather than the shorthand: an
// object with a charter's keys and none of a definition's.
func isCharterDoc(n *ir.Node) bool {
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
		case "at", "reads", "sends", "rules", "schedules", "strata":
			charter = true
		}
	}
	return charter
}

// oneDocument is the one document a charter file holds, or the refusal that says why a file
// holding several is not one charter.
func oneDocument(data []byte, format codec.Format, name string) (*ir.Node, error) {
	docs, err := codec.ParseNodes(data, format)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var out []*ir.Node
	for _, d := range docs {
		if in := codec.Unwrap(d); in != nil && in.Type != ir.NullType {
			out = append(out, d)
		}
	}
	switch len(out) {
	case 0:
		return nil, fmt.Errorf("%s: no charter in it", name)
	case 1:
		return out[0], nil
	}
	return nil, fmt.Errorf("%s holds %d documents: a file is one charter — `at:`, `reads:`, "+
		"`sends:`, `rules:`, `schedules:`, `strata:` — and a directory of them installs with -d", name, len(out))
}

// charterAddDir installs every charter file under dir, each at its own `at:`, in one request.
//
// A FILE IS A CHARTER HERE, never the shorthand: a bare list says "at the path", and in a
// directory there is no path — each file says where it goes. Two files at one address are
// refused by name, before anything is sent.
//
// WITH --prune THE PATH IS BACK, as the address the directory stands for: what is at and under
// it and not in the directory goes, in the same request, and a file outside it is refused.
func charterAddDir(cc *cli.Context, at, dir string, prune bool) error {
	switch {
	case prune && at == "":
		return fmt.Errorf("%w: --prune names the address it clears: "+
			"`verse c -a <address> -d <dir> --prune`", cli.ErrUsage)
	case !prune && at != "":
		return fmt.Errorf("%w: charter -a -d installs each file at its own `at:`; drop the path "+
			"(or add --prune, to remove what the directory leaves out under it)", cli.ErrUsage)
	}
	docs, err := charterDirDocs(dir)
	if err != nil {
		return fmt.Errorf("charter -a -d: %w", err)
	}
	q := url.Values{}
	if prune {
		q.Set("prune", at)
	}
	return installReply(cc, "charter -a -d", q, docs)
}

// charterDirDocs reads every charter file under dir, a document apiece: a file whose suffix is no
// format's is no charter, one that is not a charter is refused, and two charters at one address
// are.
func charterDirDocs(dir string) ([]*ir.Node, error) {
	var docs []*ir.Node
	var names []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		format, ok := codec.ForPathStrict(path)
		if !ok {
			return nil // a README, a script: not a charter
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		doc, err := oneDocument(data, format, path)
		if err != nil {
			return err
		}
		docs, names = append(docs, doc), append(names, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("no charter file under %s (%s)", dir, strings.Join(codec.Suffixes(), ", "))
	}
	return docs, chartersOnly(docs, names)
}

// chartersOnly refuses what a tree of charters may not hold: a document that is no charter, and
// two charters at one address. names[i] is what to call docs[i] in the refusal.
func chartersOnly(docs []*ir.Node, names []string) error {
	byAt := map[string]string{}
	for i, doc := range docs {
		if !isCharterDoc(doc) {
			return fmt.Errorf("%s is no charter: a charter says `at:`, `reads:`, `sends:`, `rules:`, "+
				"`schedules:` or `strata:`", names[i])
		}
		addr := nodeStr(doc, "at")
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

// charterSource is the charters a check reads: every file under dir, or the one charter in file
// (- or none is stdin), which says its own `at:` as a file in a directory does.
func charterSource(cc *cli.Context, file, dir string) ([]*ir.Node, error) {
	if dir != "" {
		if file != "" {
			return nil, fmt.Errorf("%w: a file (-f) or a directory (-d), not both", cli.ErrUsage)
		}
		return charterDirDocs(dir)
	}
	data, srcFmt, err := codec.ReadSource(cc.In, file, inFmt())
	if err != nil {
		return nil, err
	}
	name := file
	if name == "" || name == "-" {
		name = "stdin"
	}
	doc, err := oneDocument(data, srcFmt, name)
	if err != nil {
		return nil, err
	}
	return []*ir.Node{doc}, chartersOnly([]*ir.Node{doc}, []string{name})
}

// charterCheckVia asks, through c, whether docs would install at at with a prune, and what would
// change: POST /charter/check.
func charterCheckVia(c *client.Client, at string, docs []*ir.Node) (*http.Response, error) {
	if at == "" {
		return nil, fmt.Errorf("a check names the address the charters would be installed at")
	}
	resp, err := c.PostNodes("/charter/check", url.Values{"at": {at}}, docs)
	if err != nil {
		return nil, fmt.Errorf("charter check: %w", err)
	}
	return resp, nil
}
