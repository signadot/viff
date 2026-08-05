package trigger

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir/kpath"
)

// A CHAIN is the kpath of an action inside its trigger — `review`, `review.onDone`,
// `review.seq[2].onFailed`. It is the join key the whole model hangs off: it names a spawned
// task (the id is `<chain>@<subject>`), it is published on the task entity, and it is a
// LOOKUP KEY back into the charter, which is what navigate below makes good on.
//
// It has two renderings, and the reason is not aesthetics:
//
//	canonical   review.seq[2].onFailed    a real kpath — parses, prefix-matches, types its
//	                                      segments (a field is a field, an index an index)
//	id form     review.seq.2.onFailed     what goes in a task id
//
// The id cannot carry brackets. Under zsh `review.seq[2]@demo:pr:1` dies with "no matches
// found" BEFORE verse runs, and ids are pasted back onto command lines constantly — the same
// principle shortID states (cmd/verse/task.go). The flattening is lossless because
// Action's field set is closed: a segment following `seq` is an element, everything else is a
// field name, so nothing has to be guessed.

// seqField is the action field whose elements a chain indexes. Named once because both
// renderings and the schema walk have to agree on it.
const seqField = "seq"

// chainID renders a chain for a task id: `review.seq[2]` → `review.seq.2`.
func chainID(canonical string) string {
	r := strings.NewReplacer("[", ".", "]", "")
	return r.Replace(canonical)
}

// chainKPath is chainID's inverse: `review.seq.2` → `review.seq[2]`. A numeric segment is an
// INDEX only where the schema says an index belongs — directly after `seq` — so a field that
// happens to be named `2` elsewhere stays a field, and the round trip is exact.
func chainKPath(id string) string {
	segs := strings.Split(id, ".")
	var b strings.Builder
	for i, s := range segs {
		if i > 0 && segs[i-1] == seqField {
			if _, err := strconv.Atoi(s); err == nil {
				fmt.Fprintf(&b, "[%s]", s)
				continue
			}
		}
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s)
	}
	return b.String()
}

// seqChain extends a carrier's chain with an element index.
func seqChain(carrier string, i int) string {
	return fmt.Sprintf("%s.%s[%d]", carrier, seqField, i)
}

// seqIndexOf reads a chain that addresses a sequence ELEMENT, returning the carrier's chain
// and the index. ok is false for any other chain — a hook, a root, or a chain whose last
// segments are not `seq[n]`.
func seqIndexOf(chain string) (carrier string, idx int, ok bool) {
	kp, err := kpath.Parse(chain)
	if err != nil {
		return "", 0, false
	}
	last := kp.LastSegment()
	if last == nil || last.Index == nil {
		return "", 0, false
	}
	parent := kp.Parent()
	if parent == nil {
		return "", 0, false
	}
	// The segment BEFORE the index must be the seq field: `review.seq[2]`, never a bare
	// `review[2]`, which the grammar has no way to produce but a hand-built chain could.
	pl := parent.LastSegment()
	if pl == nil || pl.Field == nil || *pl.Field != seqField {
		return "", 0, false
	}
	// The carrier is the action HOLDING the seq — everything above the `seq` field itself.
	gp := parent.Parent()
	if gp == nil {
		return "", 0, false
	}
	return gp.String(), *last.Index, true
}

// navigate resolves a chain to the Action it addresses inside t. This is what makes a chain a
// lookup key into the charter rather than a label: given `review.seq[2].onFailed` off a
// completed task, the engine can fetch exactly the action that spawned it — which is what
// advancing a sequence needs, and what rehydrating hooks on resume needs
// (docs/trigger-model.md).
//
// Only the shapes a chain can actually have are walked: the root is the trigger name, a field
// segment is one of the four completion hooks, and an index segment selects a `seq` element.
// Anything else is not a chain this engine built.
func navigate(t Trigger, chain string) (*Action, bool) {
	kp, err := kpath.Parse(chain)
	if err != nil || kp == nil || kp.Field == nil || *kp.Field != t.Name {
		return nil, false
	}
	cur := &t.Action
	for seg := kp.Next; seg != nil; seg = seg.Next {
		switch {
		case seg.Field != nil && *seg.Field == seqField:
			// `seq` names the list, not an element — the element is the NEXT segment, so
			// this one is consumed there. A chain ending at `.seq` addresses no action.
			next := seg.Next
			if next == nil || next.Index == nil {
				return nil, false
			}
			if *next.Index < 0 || *next.Index >= len(cur.Seq) {
				return nil, false // the charter changed under a live chain
			}
			cur = &cur.Seq[*next.Index]
			seg = next
		case seg.Field != nil:
			hook := cur.hookNamed(*seg.Field)
			if hook == nil {
				return nil, false
			}
			cur = hook
		default:
			return nil, false // an index with no `seq` before it, a wildcard, a key
		}
	}
	return cur, true
}

// hookNamed resolves a completion hook by its FIELD name — the inverse of hookField, and the
// half navigate needs, since a chain carries the name rather than the status.
func (a *Action) hookNamed(field string) *Action {
	switch field {
	case "onDone":
		return a.OnDone
	case "onFailed":
		return a.OnFailed
	case "onCancelled":
		return a.OnCancelled
	case "onOrphaned":
		return a.OnOrphaned
	}
	return nil
}
