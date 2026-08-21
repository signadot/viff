package main

import (
	"fmt"

	"github.com/scott-cotton/cli"
	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// --if AND --trim, on everything that answers with a collection.
//
// They are what an operator was doing anyway: `verse ps | o list -if '{status: running}' -trim
// '{id: null, runner: null}' '[*]'` is in the state-ergonomics note as the thing every listing
// gets piped through. The two halves are asked separately on purpose — the path and `--if` say
// WHICH items, `--trim` says how much of each — which is `o list`'s own split, and the reason
// it is two flags rather than one query language.
//
// The predicates are tony's, not verse's: `tony.Match` decides what an item satisfies and
// `tony.Trim` decides what a projection keeps, so an operator who knows the match language from
// a charter's `value:` knows this one. Nothing here interprets a pattern.
//
// WHAT AN ITEM IS depends on what the command lists, and that is the whole design:
//
//   - a listing (`ps`, `motion`, `gate`, `trigger`, …) answers an ARRAY: an item is an element.
//   - `state` answers a document keyed by ADDRESS: an item is one entity's payload, found by
//     the same walk a read uses, so it works at whatever depth the path reaches and across both
//     halves of the tree.
//   - `watch` answers a stream: an item is one frame.
//   - anything else answers one document, which is one item — so `data --trim '{stage:
//     null}'` projects a payload, and `--if` on it prints the entity or nothing.
//
// EXIT 0 EITHER WAY, unlike `o list`, which exits 1 when nothing survived. An empty docket is a
// true answer to a question, not a failure to answer it — and a script asking `verse motion --if
// …` is asking, not asserting. (`o list`'s convention is right for a grep, which is what it is.)

// filterFlags is the pair, bound to ONE instance shared by every command that offers them: only
// one command runs per invocation, and the print funnel is package-level, so a shared binding is
// what lets the funnel see what was asked without every command threading it through.
type filterFlags struct {
	If   string `cli:"name=if desc='keep only the items matching this tony pattern'"`
	Trim string `cli:"name=trim desc='write only the parts this match document names'"`
}

var flt = &filterFlags{}

// filterOpts are both flags, for a command whose own `--if` is not something else.
func filterOpts() []*cli.Opt { return mustOpts(flt) }

// trimOpt is `--trim` alone, for `state` and `watch`: their `--if` is the SERVER's selector —
// it narrows which entities the read is about, before any of them cross the wire — and one flag
// name meaning two things on one command would be worse than the asymmetry.
func trimOpt() []*cli.Opt {
	for _, o := range filterOpts() {
		if o.Name == "trim" {
			return []*cli.Opt{o}
		}
	}
	panic("no --trim among the filter options") // the tag above is what defines it
}

func mustOpts(v any) []*cli.Opt {
	opts, err := cli.StructOpts(v)
	if err != nil {
		panic(err) // a malformed tag is a programming error, found at start
	}
	return opts
}

// filters is the pair, parsed. Both are read in the INPUT format, so a pattern pasted out of a
// charter (tony) and one written as JSON both work, the same way `--if` already does on watch.
type filters struct{ keep, project *ir.Node }

func readFilters() (filters, error) {
	var f filters
	var err error
	if f.keep, err = parseFilter("--if", flt.If); err != nil {
		return f, err
	}
	f.project, err = parseFilter("--trim", flt.Trim)
	return f, err
}

func parseFilter(name, src string) (*ir.Node, error) {
	if src == "" {
		return nil, nil
	}
	n, err := codec.ParseNode([]byte(src), inFmt())
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", cli.ErrUsage, name, err)
	}
	return n, nil
}

func (f filters) none() bool { return f.keep == nil && f.project == nil }

// item applies both halves to one item: whether it survives, and what of it is written.
func (f filters) item(n *ir.Node) (*ir.Node, bool, error) {
	if f.keep != nil {
		ok, err := tony.Match(n, f.keep)
		if err != nil {
			return nil, false, fmt.Errorf("%w: --if: %v", cli.ErrUsage, err)
		}
		if !ok {
			return nil, false, nil
		}
	}
	if f.project != nil {
		return tony.Trim(f.project, n), true, nil
	}
	return n, true, nil
}

// apply filters a printed document: an array by its elements, anything else as one item.
func (f filters) apply(node *ir.Node) (*ir.Node, error) {
	if f.none() || node == nil {
		return node, nil
	}
	if node.Type != ir.ArrayType {
		out, keep, err := f.item(node)
		if err != nil || !keep {
			return ir.FromSlice(nil), err // an empty list: the question was asked and nothing kept
		}
		return out, nil
	}
	var kept []*ir.Node
	for _, v := range node.Values {
		out, keep, err := f.item(v)
		if err != nil {
			return nil, err
		}
		if keep {
			kept = append(kept, out)
		}
	}
	return ir.FromSlice(kept), nil
}

// applyDoc filters `state`'s answer, whose items are ENTITIES rather than elements: it reads
// the addresses out of the document the same way a read does, filters the payload standing at
// each, and rebuilds the document from what survived.
//
// Going through the entity walk rather than the raw node is what makes it work at every depth
// and across both halves of the tree: which keys are address levels and which are payload is
// exactly the question `Doc.Entities` answers, and answering it twice would be answering it
// differently one day.
//
// A path INSIDE a payload names no entities, and there the document itself is the one item.
func (f filters) applyDoc(node *ir.Node, at *kpath.KPath) (*ir.Node, error) {
	if f.none() || node == nil {
		return node, nil
	}
	ents := (entity.Doc{Node: node}).Entities(at)
	if len(ents) == 0 {
		out, keep, err := f.item(node)
		if err != nil || !keep {
			return ir.FromMap(map[string]*ir.Node{}), err
		}
		return out, nil
	}
	kept := make([]entity.Entity, 0, len(ents))
	for _, e := range ents {
		out, keep, err := f.item(e.Payload)
		if err != nil {
			return nil, err
		}
		if keep {
			kept = append(kept, entity.Entity{Ref: e.Ref, Payload: out})
		}
	}
	doc, err := entity.Nest(kept, at)
	if err != nil {
		return nil, err
	}
	if doc.Node == nil {
		return ir.FromMap(map[string]*ir.Node{}), nil
	}
	return doc.Node, nil
}

// frame applies the filters to one streamed event, answering the text to print.
//
// A line that does not PARSE is passed through untouched and kept: on a live stream a
// surprising line is better seen than swallowed, which is renderFrame's rule too — and a filter
// is not a reason to start hiding what the server said.
func (f filters) frame(data string, in codec.Format) (string, bool, error) {
	if f.none() {
		return data, true, nil
	}
	n, err := codec.ParseNodeOrNil([]byte(data), in)
	if err != nil || n == nil {
		return data, true, nil
	}
	out, keep, err := f.item(n)
	if err != nil || !keep {
		return "", false, err
	}
	b, err := codec.NodeBytesWire(out, in)
	if err != nil {
		return data, true, nil // unrenderable: say what the server said
	}
	return string(b), true, nil
}
