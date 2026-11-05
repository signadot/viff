package main

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/scott-cotton/cli"
	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
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
//   - a listing (`ps`, `motion`, `question`, `trigger`, …) answers an ARRAY: an item is an element.
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
	if f.keep, err = parseFilter("--if", flt.If, inFmt()); err != nil {
		return f, err
	}
	f.project, err = parseFilter("--trim", flt.Trim, inFmt())
	return f, err
}

// parseFilter reads a match document, in the format in.
func parseFilter(name, src string, in codec.Format) (*ir.Node, error) {
	if src == "" {
		return nil, nil
	}
	n, err := codec.ParseNode([]byte(src), in)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", cli.ErrUsage, name, err)
	}
	return n, nil
}

func (f filters) none() bool { return f.keep == nil && f.project == nil }

// item applies both halves to one item (api.Filter), saying a pattern that does not apply as
// the flag it came from.
func (f filters) item(n *ir.Node) (*ir.Node, bool, error) {
	out, keep, err := f.shared().Item(n)
	return out, keep, filterErr(err)
}

// apply filters a printed document: an array by its elements, anything else as one item.
func (f filters) apply(node *ir.Node) (*ir.Node, error) {
	out, err := f.shared().Apply(node)
	return out, filterErr(err)
}

// shared is the pair as api applies it.
func (f filters) shared() api.Filter { return api.Filter{Keep: f.keep, Trim: f.project} }

// filterErr is a filter's error as the command line says it: a usage error naming the flag.
func filterErr(err error) error {
	var fe *api.FieldError
	if errors.As(err, &fe) {
		return fmt.Errorf("%w: --%s: %v", cli.ErrUsage, fe.Field, fe.Err)
	}
	return err
}

// applyDoc filters `state`'s answer, whose items are the NODES THE PATH REACHED rather than
// elements: `demo pr` reached one node, `demo pr '*'` each pr. It walks the answer by the path
// that was asked (entity.Doc.Each), so what --trim projects and --if keeps is what the read was
// about at any depth, and `{}` is an answer that kept nothing.
func (f filters) applyDoc(node *ir.Node, at *kpath.KPath) (*ir.Node, error) {
	if f.none() || node == nil {
		return node, nil
	}
	return (entity.Doc{Node: node}).Each(at, f.item)
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

// --until, on the streams.
//
// It answers a third question, next to --if's WHICH changes and --trim's HOW MUCH of each:
// when the stream is over. The first frame that matches is printed, and then the watch ends.
//
// PRINTED FIRST, which is what settles the exit code. The matching frame on stdout is the
// answer, so an --until watch exits 0 whether or not it ever matched — the same convention
// every filter here already keeps, where an empty result is a true answer to a question rather
// than a failure to answer it. Had the frame been swallowed, the exit status would have been
// the only evidence the thing happened, and --until would have had to become an assertion with
// a non-zero code for "did not", which is not what the rest of this CLI means by an exit code.
//
// IT MATCHES THE FRAME — `{at op to rev gap}`, the same document --trim names, with the
// entity's payload under `to`. That is the coordinate system every client-side per-frame flag
// on watch already uses. --if is the one that is different and says so: it is the SERVER's
// selector over entities, answered before there is a frame at all. So the spelling is
// `--until '{to: {stage: merged}}'`, and the payload-rooted `--until '{stage: merged}'` — what
// somebody who has just typed --if writes next — is refused rather than left to hang.
type until struct{ pat *ir.Node }

func readUntil(src string) (until, error) {
	pat, err := parseFilter("--until", src, inFmt())
	if err != nil {
		return until{}, err
	}
	if err := frameShaped(pat); err != nil {
		return until{}, err
	}
	return until{pat: pat}, nil
}

// reached reports whether this frame is the one the watch was waiting for.
//
// The frame is read AS IT ARRIVED, before --trim projected it: --trim says how much of an event
// to show, and letting a display flag decide which events can end the stream would mean
// `--trim '{at}' --until '{to: {stage: merged}}'` never matching anything.
//
// A line that does not parse never matches. It is still printed — a surprising line on a live
// stream is better seen than swallowed, which is the rule everywhere else here — but it cannot
// answer a question asked of a document.
func (u until) reached(data string, in codec.Format) (bool, error) {
	if u.pat == nil {
		return false, nil
	}
	n, err := codec.ParseNodeOrNil([]byte(data), in)
	if err != nil || n == nil {
		return false, nil
	}
	ok, err := tony.Match(n, u.pat)
	if err != nil {
		return false, fmt.Errorf("%w: --until: %v", cli.ErrUsage, err)
	}
	return ok, nil
}

// frameFields are the keys a frame has, read off entity.Event's tony tags so that the refusal
// below cannot come to describe a frame verse no longer sends.
var frameFields = func() map[string]bool {
	fields := map[string]bool{}
	t := reflect.TypeOf(entity.Event{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(strings.TrimPrefix(t.Field(i).Tag.Get("tony"), "field="), ",")
		if name != "" {
			fields[name] = true
		}
	}
	return fields
}()

// frameShaped refuses an --until written against the payload instead of the frame.
//
// `--until '{stage: merged}'` is what somebody who has just written `--if '{stage: merged}'`
// reaches for, and against a frame it matches nothing: the watch would stream until the server
// closed it and then exit 0, having silently never asked the question. That failure has no
// symptom — a watch waiting is what a watch looks like — so it is worth one refusal at the
// point of typing, which can name the spelling that means it.
//
// ONLY A PLAIN OBJECT IS JUDGED. A tagged one is an operator (`!or [...]`), whose keys are not
// field names; a non-object is matched against a frame that is always an object, which is a
// different mistake and not a silent one. Layout tags do not count as tags: `{a: b}` written in
// flow style carries !bracket, and that records how it was typed rather than what it means.
func frameShaped(pat *ir.Node) error {
	pat = codec.Unwrap(pat)
	if pat == nil || pat.Type != ir.ObjectType {
		return nil
	}
	if ir.TagRemove(ir.TagRemove(pat.Tag, "!bracket"), "!literal") != "" {
		return nil
	}
	var strays []string
	for _, f := range pat.Fields {
		if f != nil && !frameFields[f.String] {
			strays = append(strays, f.String)
		}
	}
	// EVERY key has to be a stray. A pattern naming even one of a frame's fields is written in
	// the frame's coordinates, and what else it says is the operator's business.
	if len(strays) == 0 || len(strays) != len(pat.Fields) {
		return nil
	}
	return fmt.Errorf("%w: --until %s: a frame is %s, and the entity's payload is under `to` — "+
		"so the payload match is `--until '{to: {%s: …}}'`. (--if is the one that takes a bare "+
		"payload pattern: it is the server's selector, and it selects entities rather than "+
		"frames)", cli.ErrUsage, strings.Join(strays, ", "), frameShape(), strays[0])
}

// frameShape names a frame's keys, in a fixed order so the refusal above reads the same twice.
func frameShape() string {
	keys := make([]string, 0, len(frameFields))
	for k := range frameFields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "{" + strings.Join(keys, " ") + "}"
}
