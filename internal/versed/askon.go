package versed

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/task"
)

// WHAT FOLLOWS AN ASK'S ANSWER is its `on`: rows read in order, each a tony match over the answer
// and what follows where it is the first row to match.
//
//	on:
//	- {match: {verdict: proceed}, then: {spawn: {…}}}
//	- {match: {by: deadline}, then: {ask: {name: ship-lead, …}}}
//	- {then: {answer: {ask: ship-3f2a…, choice: deny}}}
//
// The answer is matched as the record's `followed` spells it (mirror.ClaimRecord): `verdict`, the
// option it settled on, `by`, who settled it (`deadline` for silence at the date), and `via`,
// `why` and `say` where it has them. A row with no match matches every answer. The first row
// that matches is the only one whose then runs; a then that is null or `{}` does nothing, so a
// row can stop the rows after it. No row matching is a vote.

// onRowFields are the fields a row of `on` takes.
var onRowFields = []string{"match", "then"}

// onSample is an answer a row's match is tried against when the ask is raised: a pattern tony
// cannot apply (an operator it does not know) is refused then, where the asker hears it, not when
// the answer comes and nobody is there to.
var onSample = entity.Obj{"verdict": task.Proceed, "by": "user:someone@example.com"}.Payload()

// onRecord reads an ask's `on` as the record keeps it, checked: a list of rows, each a match and a
// then of one key (followRecord), a then of nothing kept as no then.
func onRecord(on entity.Payload, asker string, now time.Time) (entity.Payload, error) {
	on = ir.Uncomment(on)
	if on == nil || on.Type == ir.NullType {
		return nil, nil
	}
	if on.Type != ir.ArrayType {
		return nil, fmt.Errorf("on is a list of rows, each {match, then}: the first whose match the answer meets runs its then")
	}
	rows := make([]*ir.Node, 0, len(on.Values))
	for i, row := range on.Values {
		field := fmt.Sprintf("on[%d]", i)
		row = ir.Uncomment(row)
		if row == nil || row.Type != ir.ObjectType {
			return nil, fmt.Errorf("%s: a row is {match, then}", field)
		}
		kept := map[string]*ir.Node{}
		for k, v := range entity.Fields(row) {
			if !slices.Contains(onRowFields, k) {
				return nil, fmt.Errorf("%s: %q is not something a row says (%s)", field, k, strings.Join(onRowFields, ", "))
			}
			switch k {
			case "match":
				if _, err := tony.Match(onSample, v); err != nil {
					return nil, fmt.Errorf("%s.match: %w", field, err)
				}
				kept[k] = v.Clone()
			case "then":
				if doesNothing(v) {
					continue
				}
				follow, err := followRecord(field+".then", v, asker, now)
				if err != nil {
					return nil, err
				}
				kept[k] = follow.Payload()
			}
		}
		rows = append(rows, ir.FromMap(kept))
	}
	if len(rows) == 0 {
		return nil, nil // no rows is no `on`: the ask is a vote
	}
	return ir.FromSlice(rows), nil
}

// doesNothing is whether a row's then is nothing: null, or an object with no key.
func doesNothing(then *ir.Node) bool {
	then = ir.Uncomment(then)
	return then == nil || then.Type == ir.NullType || (then.Type == ir.ObjectType && len(then.Fields) == 0)
}

// onRows is the rows of the `on` an ask's record, or a `then: {ask}` within one, keeps.
func onRows(p entity.Payload) []entity.Payload {
	on := ir.Uncomment(entity.Get(p, "on"))
	if on == nil || on.Type != ir.ArrayType {
		return nil
	}
	return on.Values
}

// eachThen calls fn with each row's then that does something, by its row, stopping at an error.
func eachThen(p entity.Payload, fn func(row int, then entity.Payload) error) error {
	for i, row := range onRows(p) {
		if then := entity.Get(row, "then"); !doesNothing(then) {
			if err := fn(i, then); err != nil {
				return err
			}
		}
	}
	return nil
}

// answerDoc is an answered ask's answer as its rows match it: its claim (`followed`), or, where
// none stands, the answer in the keys the answered record spells it in.
func answerDoc(p entity.Payload) entity.Payload {
	if c := entity.Get(p, "followed"); entity.IsObj(c) {
		return c
	}
	doc := entity.Obj{}
	for _, k := range []string{"verdict", "by", "via", "why", "say"} {
		if v := entity.Get(p, k); v != nil {
			doc[k] = v
		}
	}
	return doc.Payload()
}

// onMatch is the row of an ask's `on` its answer matches first, and its then, nil where that does
// nothing; ok is false where no row matches. A match tony cannot apply matches nothing: it was
// tried when the ask was raised (onRecord), so it is a pattern the record no longer reads as it
// did, and running a then on it would be running one nobody chose.
func onMatch(p entity.Payload) (row int, then entity.Payload, ok bool) {
	doc := answerDoc(p)
	for i, r := range onRows(p) {
		if m := entity.Get(r, "match"); m != nil {
			if hit, err := tony.Match(doc, m); err != nil || !hit {
				continue
			}
		}
		if then = entity.Get(r, "then"); doesNothing(then) {
			return i, nil, true
		}
		return i, then, true
	}
	return 0, nil, false
}

// storeOn makes the `on` of a record about to be written data (entity.Raw): its matches are tony
// match patterns, `{verdict: !not proceed}`, which a write, being a patch, would otherwise run
// rather than keep; and it takes the place of the rows that stood, whole. After the checks, which
// fill in the edit an answer consents to on the rows as they stand (checkThen).
func storeOn(rec entity.Obj) {
	// A removal (entity.Removed) is the store's to carry out, not data to keep.
	if on, ok := rec["on"].(*ir.Node); ok && on != nil && !on.DeepEqual(entity.Removed()) {
		rec["on"] = entity.Raw(on)
	}
}
