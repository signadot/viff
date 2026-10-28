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

	"github.com/signadot/verse/charterfile"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
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

// spelling is how a note names a charter's address: as the steps the command line takes, or as
// the kinded path a tool takes.
type spelling int

const (
	asSteps spelling = iota
	asKPaths
)

// of is a charter's address, as it is spelled; baseline's is "baseline".
func (sp spelling) of(at string) string { return charterfile.Spelled(at, sp == asKPaths) }

// renderCharterFile writes the charter at the address asked to w, as its file, and says to notes
// what the file cannot: the marks on its rules, and the charters under it, spelled as sp spells
// an address.
func renderCharterFile(w, notesTo io.Writer, tree *ir.Node, full string, f filters, out codec.Format, color bool, sp spelling) error {
	files, notes, under, err := charterfile.Read(tree, full, g.Scope, sp == asKPaths)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := renderIR(w, file, f, out, color); err != nil {
			return err
		}
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
		fmt.Fprintln(notesTo, charterfile.UnderNote(here, under))
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

// charterFileName is the file the writer gives a charter in its directory.
const charterFileName = "charter"

// writeCharterDir writes every charter at or under the address asked into dir, one file apiece
// at `<address as directories>/charter<suffix>`. Every path is worked out before anything is
// written, so an address the writer cannot spell as a directory refuses the whole write.
func writeCharterDir(cc *cli.Context, tree *ir.Node, full, dir string) error {
	segs, err := charterfile.AddrSegs(full)
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
	for _, c := range charterfile.Charters(tree, segs) {
		parts := []string{dir}
		for _, seg := range c.Segs {
			if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "/\\\x00") {
				return fmt.Errorf("charter -d: the charter at %s has an address segment %q that is "+
					"not a directory name; write it on its own with `verse c %s > <file>`",
					stepsOf(c.Addr()), seg, stepsOf(c.Addr()))
			}
			parts = append(parts, seg)
		}
		parts = append(parts, charterFileName+out.Suffix())
		file, said := charterfile.File(c.Node, charterfile.FileAt(c, g.Scope), false)
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
		"`sends:`, `ttl:`, `rules:`, `schedules:`, `strata:` — and a directory of them installs with -d", name, len(out))
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
	return docs, charterfile.Only(docs, names)
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
	return []*ir.Node{doc}, charterfile.Only([]*ir.Node{doc}, []string{name})
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
