package trigger

import (
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/pkg/entity"
)

// A RULE IS NOT WOKEN BY ITS OWN BOOKKEEPING. What the engine records about a rule on a subject,
// verse.charter.<rule> (a sequence's progress, a failed hook's record), is a fact about the
// rule's running, not a change to the subject the rule is about. A commit whose only change to
// what the rule reads is there does not fire that rule. Another rule may match on it ("when
// fix's hook is lost, ask a person"), and is woken by it as by any change.
//
// It matters because nothing else stops the loop it would make. A rule whose spawn has no patch
// beside it is not latched by its own action: the subject still matches when its task ends. When
// that task's hook fails, the failure is written on the subject (recordHookTrouble); were that
// commit a cause, the rule would fire again, spawn again (the old task is retired, so `working`
// no longer holds), fail again and write again, for as long as the hook fails.

// onlyOwnBookkeeping reports whether every change commit c made to what t reads is under
// verse.charter.<t> on some entity, so the rule has nothing new to evaluate. now is the rule's
// snapshot at c.
//
// The commit's delta cannot answer this alone: a subject is written whole (entity.Modify), so a
// delta that records a hook's failure also names every field the subject already had. So the
// delta only says whether the question is worth asking, and the answer is the rule's reads at
// the commit before, compared with now, the rule's own bookkeeping left out. A commit that does
// not reach verse.charter.<t> costs nothing more; one that does costs a read at the commit
// before, and being written whole, every write of a subject with t's bookkeeping on it does. A
// scoped watch's commit carries its delta as the root watch's does (entity's pump), so the cost
// is the same under either observation. Where that read is refused (history the store no longer holds), the commit is taken
// for a change, as it was before this check.
func (e *Engine) onlyOwnBookkeeping(t Trigger, c entity.Commit, store entity.Store, at [][]string, now entity.Snapshot) bool {
	if c.Net() {
		return false // a picture of the whole slice (an opening, a resync), a change to all of it
	}
	kp := ownBookkeepingPattern(t)
	if kp == nil || !c.Touches(kp) {
		return false
	}
	before, err := store.AsOf(c.Rev-1, at...)
	if err != nil {
		return false
	}
	for _, p := range at {
		b, _, berr := before.ReadAt(p)
		a, _, aerr := now.ReadAt(p)
		if berr != nil || aerr != nil {
			return false
		}
		if !sameBesides(b, a, p, t.Name) {
			return false
		}
	}
	return true
}

// ownBookkeepingPattern is where the rule's bookkeeping stands on its subjects:
// <subject path>.verse.charter.<rule>. nil where the rule has no subject path to put it under.
//
// Built from the subject's segments as patterns, so each keeps what it is: a `..` is a descent
// (a subject at any depth beneath it), a `*` or any other operator a field of any name, and a
// name itself. Read as names, a `..` would be a field called `..`, which no address has, and the
// check would never answer for a rule whose subject descends.
func ownBookkeepingPattern(t Trigger) *kpath.KPath {
	nodes := t.Condition.Subject.SegNodes()
	if len(nodes) == 0 {
		return nil
	}
	var head, tail *kpath.KPath
	add := func(seg *kpath.KPath) {
		if head == nil {
			head, tail = seg, seg
		} else {
			tail.Next, tail = seg, seg
		}
	}
	for _, n := range nodes {
		lit, ok := entity.PathLiteral(n)
		switch {
		case entity.PathIsDescend(n):
			add(&kpath.KPath{Descend: true})
		case !ok || lit == "*":
			add(&kpath.KPath{FieldAll: true})
		default:
			add(&kpath.KPath{Field: &lit})
		}
	}
	for _, name := range []string{VerseRoot, CharterKey, t.Name} {
		name := name
		add(&kpath.KPath{Field: &name})
	}
	return head
}

// sameBesides reports whether two nodes at path are the same, leaving out every
// verse.charter.<rule> beneath an entity. An object holding nothing but what is left out is the
// same as no object: removing a rule's record can leave its empty parents behind, or not.
func sameBesides(a, b *ir.Node, path []string, rule string) bool {
	if n := len(path); n > 3 && path[n-3] == VerseRoot && path[n-2] == CharterKey && path[n-1] == rule {
		return true
	}
	objectOrNone := func(n *ir.Node) bool { return n == nil || (n.Type == ir.ObjectType && n.Tag == "") }
	if (a != nil || b != nil) && objectOrNone(a) && objectOrNone(b) {
		keys, ok := fieldKeys(a, b)
		if !ok {
			return a != nil && b != nil && a.DeepEqual(b)
		}
		for _, k := range keys {
			next := append(append(make([]string, 0, len(path)+1), path...), k)
			if !sameBesides(childOf(a, k), childOf(b, k), next, rule) {
				return false
			}
		}
		return true
	}
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.DeepEqual(b)
}

// fieldKeys is the union of two objects' field names; false where a key is not a plain name.
func fieldKeys(a, b *ir.Node) ([]string, bool) {
	seen := map[string]bool{}
	var out []string
	for _, n := range [2]*ir.Node{a, b} {
		if n == nil {
			continue
		}
		for _, k := range n.Fields {
			if k == nil || k.Type != ir.StringType {
				return nil, false
			}
			if !seen[k.String] {
				seen[k.String] = true
				out = append(out, k.String)
			}
		}
	}
	return out, true
}

// childOf is an object's field by name; nil where it has none.
func childOf(n *ir.Node, name string) *ir.Node {
	if n == nil {
		return nil
	}
	for i, k := range n.Fields {
		if k != nil && k.Type == ir.StringType && k.String == name {
			return n.Values[i]
		}
	}
	return nil
}
