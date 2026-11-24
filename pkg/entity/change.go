package entity

import (
	"sort"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
)

// ONE CHANGE: a change a commit made under a pattern is every
// path the pattern reaches, before the commit or after it, whose node differs. The question
// carries the depth — the pattern's own — and nothing here picks one for it.
//
// A live watch, a replay and a subtree watch all hold the document before a commit and after it,
// so all three answer by this walk. The commit's delta, where there is one, only prunes it: a
// branch the delta does not name is a branch the commit did not change.

// changesAt is the changes between before and after at the nodes each pattern reaches, in path
// order, stamped with rev. The documents are the viff instance (the store root's key already stripped).
// delta, when given, is the commit's patch at the same rooting, and the walk goes only where it
// reaches; a nil delta compares everything the patterns reach.
func changesAt(patterns []*kpath.KPath, before, after, delta *ir.Node, rev int64) []Event {
	seen := map[string]bool{}
	var out []Event
	for _, p := range patterns {
		walkChange(p, nil, before, after, delta, delta == nil, func(segs []string, b, a *ir.Node) {
			ref := RefAt(segs...)
			key := ref.String()
			if seen[key] {
				return
			}
			seen[key] = true
			switch {
			case a == nil:
				out = append(out, Event{Ref: ref, Op: Delete, Rev: rev})
			case b == nil || !b.DeepEqual(a):
				out = append(out, Event{Ref: ref, Op: Set, To: nodeToPayload(a), Rev: rev})
			}
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.String() < out[j].Ref.String() })
	return out
}

// walkChange walks a pattern over the two documents together, calling fn at each path it reaches
// where a node stands on either side. whole is true beneath a place the delta replaced rather
// than merged into, where anything may differ and only the documents can say what does.
//
// A segment that is not a name or a `*` — an index, a keyed element — ends the walk at the node
// holding the array: a change inside one is a change to the node it is inside, and an address
// cannot name further.
func walkChange(kp *kpath.KPath, segs []string, before, after, delta *ir.Node, whole bool, fn func([]string, *ir.Node, *ir.Node)) {
	var merge *ir.Node
	if !whole {
		var how deltaKind
		merge, how = mergeInto(delta)
		switch how {
		case untouched:
			return // the commit did not touch this branch
		case statement:
			// The node's comments changed and its value did not, so nothing beneath it did.
			if kp == nil {
				fn(segs, before, after)
			}
			return
		case replacement:
			whole = true
		}
	}
	if kp == nil || (kp.Field == nil && !kp.FieldAll && !kp.Descend) {
		if before != nil || after != nil {
			fn(segs, before, after)
		}
		return
	}
	child := func(name string) {
		var d *ir.Node
		if merge != nil {
			d = childByKey(merge, name)
		}
		walkChange(kp.Next, append(append(make([]string, 0, len(segs)+1), segs...), name),
			childByKey(before, name), childByKey(after, name), d, whole, fn)
	}
	if kp.Field != nil {
		child(*kp.Field)
		return
	}
	if kp.Descend {
		// The rest of the path tried HERE and at every node beneath, which is what a descent
		// reaches. changesAt reports each path once, however many ways the walk arrives at it.
		walkChange(kp.Next, segs, before, after, delta, whole, fn)
	}
	var names []string
	if merge != nil {
		names = fieldNames(merge)
	} else {
		names = unionNames(before, after)
	}
	for _, name := range names {
		if kp.Descend {
			var d *ir.Node
			if merge != nil {
				d = childByKey(merge, name)
			}
			walkChange(kp, append(append(make([]string, 0, len(segs)+1), segs...), name),
				childByKey(before, name), childByKey(after, name), d, whole, fn)
			continue
		}
		child(name)
	}
}

// deltaKind is what a delta does at the node it stands on.
type deltaKind int

const (
	untouched   deltaKind = iota // nothing here changed
	merged                       // an object merged in: its fields are the branches it touches
	replacement                  // what stands is replaced: a scalar, an array, `!delete`, `!replace`
	statement                    // a `!comment` with no value: the node's comments, and nothing beneath
)

// mergeInto reads a delta as one of those, and answers the object to descend when it merges.
// A `!comment` stated WITH a value is that value, which is a delta like any other.
func mergeInto(delta *ir.Node) (*ir.Node, deltaKind) {
	d := ir.Uncomment(delta)
	if v, ok := commentOperand(d); ok {
		if v == nil {
			return nil, statement
		}
		d = ir.Uncomment(v)
	}
	if d == nil {
		if delta == nil {
			return nil, untouched
		}
		return nil, replacement
	}
	if d.Type != ir.ObjectType || opIn(d.Tag) != "" {
		return nil, replacement
	}
	return d, merged
}

func fieldNames(n *ir.Node) []string {
	n = ir.Uncomment(n)
	if n == nil || n.Type != ir.ObjectType {
		return nil
	}
	out := make([]string, 0, len(n.Fields))
	for _, f := range n.Fields {
		out = append(out, fieldName(f))
	}
	return out
}

func unionNames(a, b *ir.Node) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range [2]*ir.Node{a, b} {
		for _, name := range fieldNames(n) {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// Touches reports whether this commit changed anything a pattern reaches, or anything above or
// beneath one of those nodes — the question a consumer asks before reading the store again,
// since a commit that touched none of that cannot have changed what the read answers.
//
// From the delta where the commit carries one: the pattern is walked through it, and it touches
// wherever the delta holds something along the way — a merge reaching the pattern's end, or a
// replacement at or above it, a `..` reaching any depth. A commit with no delta (a subtree
// watch's opening picture or resync) answers from its Changes. Either way what the reader may not see is left out (Only).
func (c Commit) Touches(pattern *kpath.KPath) bool {
	if c.delta == nil {
		segs := KPathSegments(pattern)
		for _, ev := range c.Changes {
			got := ev.Ref.Segs()
			if Under(segs, got, KPathAdmits) || Above(segs, got, KPathAdmits) {
				return true
			}
		}
		return false
	}
	return c.touches(pattern, nil, c.delta)
}

// Changed reports whether this commit touched the node at ref: Touches, asked of one path.
func (c Commit) Changed(ref Ref) bool {
	segs := ref.Segs()
	var head, tail *kpath.KPath
	for i := range segs {
		seg := &kpath.KPath{Field: &segs[i]}
		if head == nil {
			head, tail = seg, seg
		} else {
			tail.Next, tail = seg, seg
		}
	}
	return c.Touches(head)
}

func (c Commit) touches(kp *kpath.KPath, segs []string, delta *ir.Node) bool {
	visible := func() bool { return c.hidden == nil || !c.hidden(RefAt(segs...)) }
	merge, how := mergeInto(delta)
	switch how {
	case untouched:
		return false
	case replacement:
		return visible() // it took what stood here, and everything beneath it with it
	case statement:
		return kp == nil && visible() // its comments, and nothing beneath
	}
	if kp == nil {
		return visible()
	}
	if kp.Descend {
		if kp.Next == nil {
			return visible() // every depth beneath here: whatever the delta holds under it counts
		}
		// `..` then more: zero segments here, or one more name with the descent still standing.
		if c.touches(kp.Next, segs, delta) {
			return true
		}
		for i, f := range merge.Fields {
			if c.touches(kp, append(append(make([]string, 0, len(segs)+1), segs...), fieldName(f)), merge.Values[i]) {
				return true
			}
		}
		return false
	}
	if kp.Field == nil && !kp.FieldAll {
		return visible() // an access into an array: the node holding it is what is touched
	}
	for i, f := range merge.Fields {
		name := fieldName(f)
		if kp.Field != nil && *kp.Field != name {
			continue
		}
		if c.touches(kp.Next, append(append(make([]string, 0, len(segs)+1), segs...), name), merge.Values[i]) {
			return true
		}
	}
	return false
}
