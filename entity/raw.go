package entity

// The escape from operator interpretation.
//
// A write is a tony PATCH — the property that makes the store good at everything else — so a
// value whose tag names a merge operation is EXECUTED by the store rather than stored by it.
// Anything verse wants to keep which is ITSELF written in tony's operator grammar could
// therefore not be written down at all: a trigger's rule, a goal's definition, any match or
// patch held as data. `id: !glob "hotfix-*"` does not read back wrong, it leaves an entity
// whose own patch log no longer applies ("glob patching null gave cannot patch with glob
// operation"), which is why a charter used to be stored as its TEXT.
//
// `!raw` is the escape (go-tony v0.0.100). As a patch it returns its subtree verbatim:
// nothing beneath it is interpreted, at any depth, and the `!raw` tag itself is consumed, so
// the data lands carrying its own tags. As a match it compares its subtree to the document
// literally — tags compared rather than evaluated. One escape with both faces, which is what
// lets the same wrap serve a write's value and a precondition built from what was read.
//
// The escape belongs to the WRITE, never to what is stored. A payload read back is ordinary
// data with no `!raw` anywhere in it, so putting it back means escaping it again — see
// AsData, which is what Modify does for its callers.

import (
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/mergeop"
)

// rawName is the escape operation's tag label.
const rawName = "raw"

// Raw marks a whole value as data: it is stored exactly as it stands, operator tags and all,
// and it REPLACES what stands at its path instead of merging into it.
//
// That is the shape a record carrying a DOCUMENT wants — a rule, a goal definition — where a
// merge would leave fields of the version before it standing in the version after. It is also
// why the wrap is a deliberate call at such a write rather than something the store does for
// every value: an ordinary write means its merge, and a caller that sends a patch (versed's
// write surface does) means its operators.
func Raw(p Payload) Payload {
	if p == nil {
		return nil
	}
	out := clone(p)
	out.Tag = rawTag(out.Tag)
	return out
}

// AsData escapes every TAGGED value inside p, and leaves everything else meaning what it says
// — so a payload that came OUT of the store can go back into it without executing itself, and
// can stand as a precondition without evaluating itself.
//
// Every tagged value, not only the operators, because a merge does not leave a tag alone
// either: patching a tagged value onto a tagged value COMPOSES the two, so a goal record
// rewritten six times came back holding `doneWhen: !fact.fact.fact.fact.fact.fact`. A tag in a
// payload is data whatever it names — the write must not interpret it, and the merge must not
// accumulate it — and `!raw` says both at once, since it installs its subtree verbatim rather
// than merging into what stands there. Layout tags are exempt: `!bracket` records how a value
// was written, and escaping on it would put half of every JSON-shaped payload out of reach of
// a merge for no reason.
//
// It is otherwise SELECTIVE, and has to be. Wrapping the whole payload would make its ordinary
// fields replace rather than merge, and as a precondition it would demand exact equality
// against a document rather than a merge-compatible one.
//
// It escapes EVERY tag, verse's own removal marker included, and the callers add that marker
// afterwards for exactly that reason (write.go's withRemovals). Exempting it here would be the
// obvious alternative and is wrong twice: a charter legitimately STORES `patch: {tmp: !delete
// null}` as data, so the exemption would make a stored operator executable — and AsData also
// builds the compare-and-swap PRECONDITION, where a delete is not something a match can be
// evaluated with at all ("cannot match with delete operation").
func AsData(p Payload) Payload {
	if p == nil {
		return nil
	}
	out := clone(p)
	escapeTags(out)
	return out
}

// escapeTags wraps every subtree a patch would interpret or a merge would compose, in place.
func escapeTags(n *ir.Node) {
	if n == nil {
		return
	}
	if opIn(n.Tag) == rawName {
		return // already escaped: everything beneath it is data, this one included
	}
	if dataTag(n.Tag) != "" {
		n.Tag = rawTag(n.Tag) // and stop: what it holds is data by that very fact
		return
	}
	for _, v := range n.Values {
		escapeTags(v)
	}
}

// dataTag is a node's tag less the labels that record how it was WRITTEN rather than what it
// is, which are the only ones a write may leave to the merge.
func dataTag(tag string) string {
	return ir.TagRemove(ir.TagRemove(tag, "!bracket"), "!literal")
}

// opIn returns the first label of tag which names a merge operation — the one a patch would
// execute, since mergeop.SplitChild dispatches on the first REGISTERED label rather than on
// the head, so `!bracket.glob` globs. Empty when the tag names no operation: a tag the parser
// attached to record how a value was written (`!bracket`), one of verse's own (`!irtype`), or
// simply somebody's vocabulary — an unregistered tag round-trips as data already.
func opIn(tag string) string {
	for tag != "" {
		head, _, rest := ir.TagArgs(tag)
		name := strings.TrimPrefix(head, "!")
		if mergeop.Lookup(name) != nil {
			return name
		}
		tag = rest
	}
	return ""
}

// rawTag composes !raw over whatever tag the node already carries. Only a tag's FIRST label
// is written with the "!", so the rest follow it bare — and they stay on the node, which is
// what "the escape is consumed and the data keeps its tags" leaves behind.
func rawTag(tag string) string {
	if tag == "" {
		return "!" + rawName
	}
	return "!" + rawName + "." + strings.TrimPrefix(tag, "!")
}
