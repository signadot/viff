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
// `!raw` is the escape (go-tony v0.0.100): nothing beneath it is interpreted, at any depth,
// and the `!raw` tag itself is consumed, so the data lands carrying its own tags. It ESCAPES
// and does nothing else — as a patch it merges, the way a patch of the same shape without it
// would (tony-format mgg9nvt6h12krn6dksn0; the go-tony releases before that change replaced
// instead, and nothing here depends on which). A record that carries a document wants the
// document REPLACED, not merged into, and that is a second operation said in the same chain:
// `!insert` applies its child against absence and answers the result whatever was there, so
// `!insert.raw` is "exactly this, as data". That is the escape a WRITE gets here. A match
// cannot hold `!insert` and does not need to: `!raw` in a pattern compares its subtree to the
// document literally, tags compared rather than evaluated, which is what a precondition built
// from what was read wants (AsPattern).
//
// The escape belongs to the WRITE, never to what is stored. A payload read back is ordinary
// data with no escape anywhere in it, so putting it back means escaping it again — see
// AsData, which is what Modify does for its callers.

import (
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/mergeop"
	logdapi "github.com/signadot/tony-format/go-tony/system/logd/api"
)

// rawName is the escape operation's tag label, and insertName the one that makes it replace.
const (
	rawName    = "raw"
	insertName = "insert"
)

// Raw marks a whole value as data: it is stored exactly as it stands, operator tags and all,
// and it REPLACES what stands at its path instead of merging into it — `!insert.raw`, the
// insert for the replacing and the raw for the data.
//
// That is the shape a record carrying a DOCUMENT wants — a rule, a goal definition — where a
// merge would leave fields of the version before it standing in the version after. It is also
// why the wrap is a deliberate call at such a write rather than something the store does for
// every value: an ordinary write means its merge, and a caller that sends a patch (versed's
// write surface does) means its operators.
//
// THE TAG GOES ON THE VALUE, NOT ON THE COMMENT ABOVE IT. A head comment WRAPS the value it
// precedes, so a document somebody wrote a note above arrives here as a CommentType node
// holding the real one — and tagging the wrapper escapes something that is not the value. The
// write still lands, and it lands lossy: the head comments survive (they are the wrappers
// being copied) while every LINE comment inside is dropped, which is the arbitrary half of a
// document surviving that this tree keeps having to rule out. A charter installed from a file
// came back having lost `name: r # the short name` and kept `# why this rule exists`.
//
// It is the same distinction the navigation split makes (ir/nav.go, canonicalFields): a
// comment is not a container, and an escape is about what the value IS.
func Raw(p Payload) Payload {
	if p == nil {
		return nil
	}
	out := clone(p)
	value := out
	for value.Type == ir.CommentType && len(value.Values) == 1 {
		value = value.Values[0]
	}
	value.Tag = writeEscape(value.Tag)
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
// accumulate it — and `!insert.raw` says both at once: the raw makes the tags data, and the
// insert installs the value whole rather than merging into what stands there. Layout tags are
// exempt: `!bracket` records how a value
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
// null}` as data, so the exemption would make a stored operator executable — and AsPattern,
// the same walk for the compare-and-swap PRECONDITION, would otherwise carry a delete into a
// match, where it cannot be evaluated at all ("cannot match with delete operation").
func AsData(p Payload) Payload {
	if p == nil {
		return nil
	}
	out := clone(p)
	escapeTags(out, writeEscape)
	return out
}

// AsPattern is AsData for a PRECONDITION: the same values escaped, with the bare `!raw` a
// match understands — its subtree compared to the document literally, tags compared rather
// than evaluated. A pattern cannot hold `!insert`, so the write's escape is not this one.
func AsPattern(p Payload) Payload {
	if p == nil {
		return nil
	}
	out := clone(p)
	escapeTags(out, patternEscape)
	return out
}

// escapeTags wraps every subtree a patch would interpret or a merge would compose, in place,
// with the escape the caller's use wants: writeEscape for a value being stored, patternEscape
// for one being matched.
func escapeTags(n *ir.Node, esc func(string) string) {
	if n == nil {
		return
	}
	if escaped(n.Tag) {
		return // already escaped: everything beneath it is data, this one included
	}
	if dataTag(n.Tag) != "" {
		n.Tag = esc(n.Tag) // and stop: what it holds is data by that very fact
		return
	}
	for _, v := range n.Values {
		escapeTags(v, esc)
	}
}

// escaped says a tag chain is already one of the two escapes: the first operation it names is
// `raw`, or it is `insert` with `raw` directly behind it. Everything beneath such a node is
// data, this one included.
func escaped(tag string) bool {
	for tag != "" {
		head, _, rest := ir.TagArgs(tag)
		name := strings.TrimPrefix(head, "!")
		if mergeop.Lookup(name) == nil {
			tag = rest
			continue
		}
		if name == rawName {
			return true
		}
		if name == insertName {
			next, _, _ := ir.TagArgs(rest)
			return strings.TrimPrefix(next, "!") == rawName
		}
		return false
	}
	return false
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

// patternEscape composes !raw over whatever tag the node already carries, and writeEscape
// composes !insert.raw. Only a tag's FIRST label is written with the "!", so the rest follow
// it bare — and they stay on the node, which is what "the escape is consumed and the data
// keeps its tags" leaves behind.
func patternEscape(tag string) string { return compose("!"+rawName, tag) }

func writeEscape(tag string) string { return compose("!"+insertName+"."+rawName, tag) }

func compose(head, tag string) string {
	if tag == "" {
		return head
	}
	return head + "." + strings.TrimPrefix(tag, "!")
}

// CheckPatchable refuses a value the store could accept and then never read back.
//
// This is the third time a match operator reached a write, and the first two cost a staging
// deployment each. The shape never changes: a write is a tony PATCH, so a tag naming an
// operation is EXECUTED — and `!irtype`, `!or`, `!glob` and their kind are MATCH operators,
// whose Patch method can only ever return "cannot patch with X operation". The write itself
// succeeds, because the store appends before it applies. Every later read replays the log,
// hits that operator, and fails. From then on the store cannot be read and cannot be repaired,
// while writes go on succeeding — which is a verse that looks alive and answers nothing.
//
// The two earlier fixes were at WRITERS: the mirror escapes an agent's answer (AsData), and a
// charter is stored with Raw. Both were right and neither is enough, because the defect is not
// that some caller forgot — it is that the store accepts a document it will choke on. A caller
// that means its operators is ordinary (versed's write surface is one) and stays ordinary: a
// merge operator patches, and that is the grammar. What is refused is the operator that CANNOT
// patch, and no correct write has ever contained one.
//
// It is exact rather than a guess: mergeop.Symbol embeds Name, which answers IsPatch for the
// operation the tag actually names — the same lookup a patch does, asked before rather than
// after. An escape — `!raw`, or the `!insert.raw` a write gets, both patchable — stops the walk
// by construction, so escaped data is untouched however many operators it holds, which is what
// makes AsData and Raw still the right thing at the writers.
//
// IsPatch IS NOT THE WHOLE QUESTION, and asking only it is how this got narrower without anyone
// editing it. "Can this operation appear in a patch" and "can this value be STORED" are two
// questions that happened to agree: `!let` is a patch operator — it substitutes names and yields
// a body — and a stored one is still executed on the document it later meets, so it is written,
// accepted, and gone. Same for `!get-path` and `!list-path`, which answer with a value read from
// somewhere else and therefore answer differently against a base that has moved.
//
// So the storage vocabulary is asked as well, and it is the substrate's own: logd holds a SCOPED
// write to it (tx.checkStorableInScope) and lets a baseline write past, so a baseline write is
// exactly the case nothing else covers. Asking both keeps the verse-specific message — with its
// AsData/Raw hint — for the operators it already named, and adds the ones whose IsPatch says
// yes while the store says no.
func CheckPatchable(p Payload) error {
	if err := checkPatchable(p, ""); err != nil {
		return err
	}
	return logdapi.ValidateForStorage(p)
}

func checkPatchable(n *ir.Node, at string) error {
	if n == nil {
		return nil
	}
	if escaped(n.Tag) {
		return nil // escaped: everything beneath it is data, however it is tagged
	}
	if name := opIn(n.Tag); name != "" {
		sym := mergeop.Lookup(name)
		if sym != nil && !sym.IsPatch() {
			where := at
			if where == "" {
				where = "the value itself"
			}
			return fmt.Errorf("%s: `!%s` is a MATCH operator and cannot be written — a write is "+
				"a patch, so this would be executed rather than stored, fail on every later read, "+
				"and leave the store unreadable. Escape it as data (entity.AsData / entity.Raw) "+
				"if it is a pattern being stored rather than an operation being performed",
				where, name)
		}
	}
	for i, v := range n.Values {
		field := at
		if i < len(n.Fields) && n.Fields[i] != nil {
			if field != "" {
				field += "."
			}
			field += n.Fields[i].String
		}
		if err := checkPatchable(v, field); err != nil {
			return err
		}
	}
	return nil
}
