package entity

// The verse as ONE DOCUMENT, addressed by one kinded path.
//
// The store holds a tree. A read names a path into it — `demo`, `demo.pr.1`,
// `demo.pr.*.stage`, the empty path for the root — and answers the node standing there, with
// everything beneath it. Nothing in the answer says where an entity ends, because nothing in the
// store does: a question names its own depth by the path it asks.

import (
	"errors"
	"fmt"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
)

// Doc is the verse's state at a path: the subtree standing there, verbatim.
//
// It wraps a node rather than being one because it is a WIRE TYPE — the body of a read — and
// naming it is what lets the schema say what the route answers.
//
//tony:schemagen=verse-state,notag,codec=custom
type Doc struct{ Node *ir.Node }

// Paths is a LISTING: the addresses a read reaches, one per line when a command prints it.
//
// AS KINDED PATHS, one string each, because that is the spelling every address
// in this system has. A reader that wants steps splits the path where it stops
// naming (LiteralPrefix) and renders it, which is what the CLI does -- and
// `--kpaths` prints what arrived.
//
//tony:schemagen=verse-paths,notag
type Paths struct {
	Paths []string `tony:"field=paths"`
}

// PathsDoc is a listing as the wire carries it.
func PathsDoc(ps []Path) Paths {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.KPath())
	}
	return Paths{Paths: out}
}

// LiteralPrefix splits a kinded path at its first segment that is not a field name: the prefix
// a store is read at, and the rest, which is answered over what stands there.
func LiteralPrefix(at *kpath.KPath) (prefix []string, rest *kpath.KPath) {
	kp := at
	for ; kp != nil && kp.Field != nil; kp = kp.Next {
		prefix = append(prefix, *kp.Field)
	}
	return prefix, kp
}

// Reach narrows a node by a kinded path, and keeps what keep keeps of each node the path
// reaches. keep answers the node to put in its place and whether to put anything; nil keeps
// every node as it stands.
//
// reached is whether the path reached any node at all, kept or not. A path that reaches nothing
// names nothing, and one whose every node was dropped is an answer that kept none — the first is
// not-found and the second an empty document, and a caller has to be able to tell them apart.
//
// A WILDCARD KEEPS ITS CONTAINER'S SHAPE, which is what keeps the answer addressable: `.*` over
// an object answers an object under the same keys, so `demo.pr.*.stage` is
// `{1: open, 2: reviewed}` and every value in it can still be pointed at. A concrete segment is
// followed and not kept, since the path already says it. A wildcard level none of whose nodes
// was kept is left out, as the nodes are.
//
// A SEGMENT THAT DOES NOT FIT what it lands on reaches nothing — a field of a scalar, an element
// of an object — which is what the store answers for a literal path through a value of the
// wrong shape, so a wildcard over nodes of different shapes reaches the ones that have it.
func Reach(node *ir.Node, kp *kpath.KPath, keep func(*ir.Node) (*ir.Node, bool, error)) (out *ir.Node, reached bool, err error) {
	return reach(node, kp, keep, true)
}

func reach(node *ir.Node, kp *kpath.KPath, keep func(*ir.Node) (*ir.Node, bool, error), literal bool) (*ir.Node, bool, error) {
	if kp == nil {
		if keep == nil {
			return node, true, nil
		}
		v, ok, err := keep(node)
		if err != nil || !ok {
			return nil, err == nil, err
		}
		return v, true, nil
	}
	// A comment WRAPS the value it precedes, and a path is about structure: `demo.pr.1.stage`
	// means the same thing whether or not somebody wrote a note above the payload.
	node = ir.Uncomment(node)
	if node == nil {
		return nil, false, nil
	}
	switch {
	case kp.Descend:
		return descend(node, kp.Next, keep)
	case kp.FieldAll:
		if node.Type != ir.ObjectType {
			return nil, false, nil
		}
		out, reached := map[string]*ir.Node{}, false
		for i, f := range node.Fields {
			sub, r, err := reach(node.Values[i], kp.Next, keep, false)
			if err != nil {
				return nil, false, err
			}
			reached = reached || r
			if sub != nil {
				out[fieldName(f)] = sub
			}
		}
		if len(out) == 0 {
			return nil, reached, nil
		}
		return ir.FromMap(out), true, nil
	case kp.IndexAll, kp.SparseIndexAll, kp.KeyAll:
		if node.Type != ir.ArrayType {
			return nil, false, nil
		}
		if kp.KeyAll {
			if _, keyed := node.KeyField(); !keyed {
				if !literal {
					return nil, false, nil
				}
				return nil, false, fmt.Errorf("(*): the array is not keyed, so it has no elements named by identity")
			}
		}
		var out []*ir.Node
		reached := false
		for _, v := range node.Values {
			sub, r, err := reach(v, kp.Next, keep, false)
			if err != nil {
				return nil, false, err
			}
			reached = reached || r
			if sub != nil {
				out = append(out, sub)
			}
		}
		if len(out) == 0 {
			return nil, reached, nil
		}
		return ir.FromSlice(out), true, nil
	}
	child, err := step(node, kp, literal)
	if err != nil || child == nil {
		return nil, false, err
	}
	return reach(child, kp.Next, keep, literal)
}

// descend answers `..` and whatever follows it: the rest of the path tried at this node and at
// every node beneath it, each match placed AT ITS OWN PATH below the node the descent started
// from — `demo..stage` answers `{stage: …, pr: {"1": {stage: …}}}`.
//
// IN FULL, where a wildcard's answer strips the concrete segments after it (`demo.*.stage` is
// `{"1": open}`). A descent reaches nodes at DIFFERENT depths, so stripping would collide two
// matches into one key and lose which is which; spelling each in full is what keeps the answer
// addressable, which is the whole point of nesting it.
//
// A node the rest of the path answers WHOLE is not descended into: what is beneath it is already
// inside it, so every node is answered once.
func descend(node *ir.Node, rest *kpath.KPath, keep func(*ir.Node) (*ir.Node, bool, error)) (*ir.Node, bool, error) {
	here, reached, err := reach(node, rest, keep, false)
	if err != nil {
		return nil, false, err
	}
	if here != nil && rest == nil {
		return here, true, nil // this node itself: it holds what is beneath it
	}
	out := map[string]*ir.Node{}
	if here != nil {
		// The match at this node stands at the path the rest of the pattern names, so that a
		// deeper match of the same shape nests below rather than over it.
		if at := restPath(rest); at != nil {
			out[at[0]] = nestUnder(here, at[1:])
		}
	}
	if n := ir.Uncomment(node); n != nil && n.Type == ir.ObjectType {
		for i, f := range n.Fields {
			sub, r, err := descend(n.Values[i], rest, keep)
			if err != nil {
				return nil, false, err
			}
			reached = reached || r
			if sub != nil {
				out[fieldName(f)] = sub
			}
		}
	}
	if len(out) == 0 {
		return nil, reached, nil
	}
	return ir.FromMap(out), true, nil
}

// restPath is the names a path spells, for placing a match where it stands. A path that reaches
// through an array has no such spelling, and a match under one is placed by its names alone.
func restPath(kp *kpath.KPath) []string {
	var out []string
	for ; kp != nil; kp = kp.Next {
		if kp.Field == nil {
			break
		}
		out = append(out, *kp.Field)
	}
	return out
}

// nestUnder puts a node under a path of names.
func nestUnder(node *ir.Node, segs []string) *ir.Node {
	for i := len(segs) - 1; i >= 0; i-- {
		node = ir.FromMap(map[string]*ir.Node{segs[i]: node})
	}
	return node
}

// step follows one concrete segment: the value it addresses, or nil when nothing is there.
//
// An array with no key to look an element up by is an ERROR where no wildcard came before it:
// the question can never be answered by that array, and a reader who asked it about one place
// is told why rather than that nothing is there. Beneath a wildcard it is one of the nodes that
// lack what the path asks for.
func step(node *ir.Node, kp *kpath.KPath, literal bool) (*ir.Node, error) {
	switch {
	case kp.Field != nil:
		if node.Type != ir.ObjectType {
			return nil, nil
		}
		return ir.Get(node, *kp.Field), nil
	case kp.Index != nil, kp.SparseIndex != nil:
		i := kp.Index
		if i == nil {
			i = kp.SparseIndex
		}
		if node.Type != ir.ArrayType || *i < 0 || *i >= len(node.Values) {
			return nil, nil
		}
		return node.Values[*i], nil
	case kp.Key != nil:
		if node.Type != ir.ArrayType {
			return nil, nil
		}
		// The array says what it is keyed BY (`!key(voter)`); without the annotation there is
		// no key to look one up by.
		//
		// The annotation has to have been STORED, which is the part worth saying out loud: `!key`
		// is a registered merge operator, so an ordinary write EXECUTES it — the array is merged
		// by key and kept without the tag. A payload meant to stay addressable this way is
		// written as data (entity.Raw / AsData), which is what the engine's own writers do.
		field, keyed := node.KeyField()
		if !keyed {
			if !literal {
				return nil, nil
			}
			return nil, fmt.Errorf("(%s): the array is not keyed. It carries no `!key(<field>)`, "+
				"and an ordinary write does not leave one — `!key` is a merge operator, so a write "+
				"executes it and stores the merged array untagged. The payload has to be written "+
				"as data for the annotation to stand", *kp.Key)
		}
		for _, el := range node.Values {
			if k, ok := ir.ElemKey(el, field); ok && k == *kp.Key {
				return el, nil
			}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("%s: not a segment a read follows", kp)
}

// HasDescend reports whether a path reaches across depths — the question a surface asks when it
// can answer at one depth and not at every one.
func HasDescend(kp *kpath.KPath) bool { return hasDescend(kp) }

// hasDescend reports whether a path reaches across depths.
func hasDescend(kp *kpath.KPath) bool {
	for ; kp != nil; kp = kp.Next {
		if kp.Descend {
			return true
		}
	}
	return false
}

// answerPath is the path an ANSWER is walked by: the segments of rest that left a level in it.
// A read answered at `demo.pr.*.stage` is `{1: open}` — the wildcard kept its keys and `stage`
// was followed — so the answer is walked by `*` alone.
func answerPath(rest *kpath.KPath) *kpath.KPath {
	var head, tail *kpath.KPath
	for kp := rest; kp != nil; kp = kp.Next {
		if !kp.FieldAll && !kp.IndexAll && !kp.SparseIndexAll && !kp.KeyAll {
			continue
		}
		seg := *kp
		seg.Next = nil
		if head == nil {
			head, tail = &seg, &seg
		} else {
			tail.Next, tail = &seg, &seg
		}
	}
	return head
}

// Each rebuilds the answer to a read at `at` with keep applied to each node the path reached —
// what a client-side filter or projection is applied to. What is left is `{}` when keep kept
// nothing.
func (d Doc) Each(at *kpath.KPath, keep func(*ir.Node) (*ir.Node, bool, error)) (*ir.Node, error) {
	if d.Node == nil {
		return nil, nil
	}
	_, rest := LiteralPrefix(at)
	out, _, err := Reach(d.Node, answerPath(rest), keep)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return ir.FromMap(map[string]*ir.Node{}), nil
	}
	return out, nil
}

// Reached is each node the read at `at` reached, at the address it stands at in the store. A
// node reached through an array has no address of names, and is not among them.
//
// Under a `..` the answer holds every match at its own path (descend), so the walk there is the
// answer's own shape: each node in it is one the descent reached, and the node it stands under
// is another.
func (d Doc) Reached(at *kpath.KPath) []Entity {
	prefix, rest := LiteralPrefix(at)
	var out []Entity
	var rec func(n *ir.Node, kp *kpath.KPath, segs []string)
	all := func(n *ir.Node, segs []string) {
		var walk func(n *ir.Node, segs []string)
		walk = func(n *ir.Node, segs []string) {
			out = append(out, Entity{Ref: RefAt(segs...), Payload: n})
			if n = ir.Uncomment(n); n == nil || n.Type != ir.ObjectType {
				return
			}
			for i, f := range n.Fields {
				walk(n.Values[i], append(append([]string{}, segs...), fieldName(f)))
			}
		}
		walk(n, segs)
	}
	rec = func(n *ir.Node, kp *kpath.KPath, segs []string) {
		if kp == nil {
			out = append(out, Entity{Ref: RefAt(segs...), Payload: n})
			return
		}
		switch {
		case kp.Descend:
			all(n, segs)
		case kp.Field != nil:
			rec(n, kp.Next, append(append([]string{}, segs...), *kp.Field))
		case kp.FieldAll:
			if n = ir.Uncomment(n); n == nil || n.Type != ir.ObjectType {
				return
			}
			for i, f := range n.Fields {
				rec(n.Values[i], kp.Next, append(append([]string{}, segs...), fieldName(f)))
			}
		}
	}
	if d.Node != nil {
		rec(d.Node, rest, prefix)
	}
	return out
}

// Unbounded is the depth a listing has when nobody gave one: everything beneath the address,
// into payloads and arrays. It is not a number a reader types — 0 is the address alone and a
// negative depth is refused where it arrives — it is what a caller holds when none was given,
// which is the distinction go-tony's own depth draws (ir/kpath.Unbounded).
const Unbounded = -1

// Paths is every address in the answer to a read at `at`, in order, deepest last within each
// branch — the walk `verse data --paths` prints: each node the path reached where it reached
// several, and everything beneath each.
//
// AS ADDRESSES (Path), so the caller renders them: `--paths` joins with JoinStepsAt and
// `--kpaths` with Path.KPath, and the walk has no opinion about which.
//
// EVERY node beneath, not the ones at some depth: nothing in a document says where an entity
// ends, so asking for one depth answers with the addresses that happen to live there and
// silently omits everything deeper. `depth` bounds how far the walk goes BENEATH the node the
// path names — 0 is that node alone, 1 its children, and Unbounded is everything, which is the
// default. A path naming one node does not print that node, which is the address the reader
// typed.
//
// COUNTED FROM THE ADDRESS, not from the root, so the same number reads the same way at every
// level: `demo.pr` at 1 and `demo.pr."1"` at 1 are each one level down, and walking deeper is a
// step rather than a step and a bigger number.
//
// THIS IS NOT HOW A LISTING IS ANSWERED. The store answers one — a place, a path with a
// wildcard or a descent, a predicate — with the depth handed to it as given (Store.PathsAt,
// PathsMatching), and what a depth means over a path with a `..` in it is logd's to say. This
// walk is what is left for a document somebody already holds, which for a listing is a path
// rooted in a MOUNTED place: the store does not descend into one. Its count is an approximation
// of the store's there — every segment past the literal prefix, where the store counts only
// what a descent took — and is not to be grown into a second implementation of that rule.
func (d Doc) Paths(at *kpath.KPath, depth int) []Path {
	prefix, rest := LiteralPrefix(at)
	descend := hasDescend(rest)
	wild := answerPath(rest) != nil || descend
	var out []Path
	seen := map[string]bool{}
	for _, e := range d.Reached(at) {
		// WHICH NODE THE PATH NAMES: the one an answer stands at, and for a descent the literal
		// prefix it strayed from — what the descent took is itself what the bound is about, so
		// counting from each node it reached would bound nothing.
		base := len(e.Ref.Segs())
		if descend {
			base = len(prefix)
		}
		max := 0
		if depth >= 0 {
			max = base + depth
		}
		// A path naming one node does not print that node, which is the address the reader
		// typed; a wildcard or a descent prints each node it reached.
		if wild {
			appendPath(&out, seen, Path{Ref: e.Ref}, max)
		}
		walkPaths(e.Payload, e.Ref.Segs(), "", max, seen, &out)
	}
	return out
}

// PathsUnder is every address BENEATH the node n standing at segs, deepest last within each
// branch — Paths' own walk, over a node a caller already holds. The node's own address is not
// among them: a caller that wants it has it.
//
// It is what the store's listing uses where it has read a node rather than walked it
// (LogdStore.PathsAt at an array), so the two answer the same addresses by construction rather
// than by two sets of rules kept in step.
func PathsUnder(n *ir.Node, segs []string, access string, depth int) []Path {
	max := 0
	if depth >= 0 {
		max = len(segs) + accessDepth(access) + depth
	}
	var out []Path
	walkPaths(n, segs, access, max, map[string]bool{}, &out)
	return out
}

// walkPaths emits every address beneath n, which stands at segs (+access). It does not emit that
// address itself. `max` is the ceiling its entry point computed from the depth it was asked for
// — the number of steps an address may have — and 0 is no bound; the relative number the reader
// gave is turned into this one in Doc.Paths and PathsUnder, which are the two ways in.
func walkPaths(n *ir.Node, segs []string, access string, max int, seen map[string]bool, out *[]Path) {
	n = ir.Uncomment(n)
	if n == nil {
		return
	}
	switch n.Type {
	case ir.ObjectType:
		for i, f := range n.Fields {
			// Below an element everything is inside a node, so the name extends the ACCESS
			// rather than the address of names (Path.Access).
			child := Path{Ref: RefAt(segs...), Access: access}
			childSegs, childAccess := segs, ""
			if access != "" {
				childAccess = access + "." + seg(fieldName(f))
			} else {
				childSegs = append(append([]string{}, segs...), fieldName(f))
			}
			child = Path{Ref: RefAt(childSegs...), Access: childAccess}
			if !appendPath(out, seen, child, max) {
				continue
			}
			walkPaths(n.Values[i], childSegs, childAccess, max, seen, out)
		}
	case ir.ArrayType:
		// AN ELEMENT IS ADDRESSED BY POSITION. Identity belongs to the store — logd keys an array
		// in the schema — and a `!key` tag in a payload is refused on the way in
		// (checkNotKeyed), so nothing standing here is keyed by anything else. When the store's
		// keying is surfaced, this is where its names go.
		for i, el := range n.Values {
			step := fmt.Sprintf("[%d]", i)
			child := Path{Ref: RefAt(segs...), Access: access + step}
			if !appendPath(out, seen, child, max) {
				continue
			}
			walkPaths(el, segs, access+step, max, seen, out)
		}
	}
}

// appendPath records one address, and says whether the walk goes on beneath it: an address past
// the bound is not printed, and nothing under it is either.
func appendPath(out *[]Path, seen map[string]bool, p Path, max int) bool {
	if max > 0 && len(p.Ref.Segs())+accessDepth(p.Access) > max {
		return false
	}
	if kp := p.KPath(); kp != "" && !seen[kp] {
		seen[kp] = true
		*out = append(*out, p)
	}
	return true
}

// accessDepth is how many steps an access fragment is, so a `--depth` bound counts an element
// and a field of it as the two steps a reader types.
func accessDepth(access string) int {
	if access == "" {
		return 0
	}
	kp, err := kpath.Parse(access)
	if err != nil {
		return 1
	}
	n := 0
	for p := kp; p != nil; p = p.Next {
		n++
	}
	return n
}

// Reaches reports whether a path reaches the node at ref — the ref half of "is this change one
// this read is about". A path of N name-or-`*` segments reaches nodes N segments down and
// nothing else: not an ancestor, which is a different node, and not a descendant, which a read
// of this path answers inside the node it reaches rather than at its own address.
//
// A segment that is not a name or a `*` — an index, a keyed element — is a place INSIDE the node
// the segments before it reach, so the path reaches that node and the walk stops there.
func Reaches(at *kpath.KPath, ref Ref) bool { return reaches(at, ref.Segs()) }

func reaches(kp *kpath.KPath, segs []string) bool {
	for ; kp != nil; kp = kp.Next {
		if kp.Descend {
			// Every depth beneath, the node itself included: zero or more segments here.
			for i := 0; i <= len(segs); i++ {
				if reaches(kp.Next, segs[i:]) {
					return true
				}
			}
			return false
		}
		if kp.Field == nil && !kp.FieldAll {
			return len(segs) == 0
		}
		if len(segs) == 0 || !KPathAdmits(kp, segs[0]) {
			return false
		}
		segs = segs[1:]
	}
	return len(segs) == 0
}

// KPathSegments is a kinded path as its segments, in order, each still the kpath segment it was —
// so a quoted field named `*` stays a name and the wildcard stays a wildcard.
func KPathSegments(at *kpath.KPath) []*kpath.KPath {
	var out []*kpath.KPath
	for kp := at; kp != nil; kp = kp.Next {
		out = append(out, kp)
	}
	return out
}

// KPathDisjoint reports whether two kinded-path segments can name no common segment — the
// per-segment half of Overlap for patterns kpath spells. It is kpath's own Matches asked of the
// two segments alone, each way: a wildcard meets any segment of its kind, two concrete segments
// only when equal, and kinds never cross.
func KPathDisjoint(a, b *kpath.KPath) bool {
	x, y := *a, *b
	x.Next, y.Next = nil, nil
	return !x.Matches(&y) && !y.Matches(&x)
}

// KPathAdmits is one kinded-path segment asked of one address segment: `*` admits any name, a
// field its own; an index or a keyed element admits none, since no address is spelled that way.
func KPathAdmits(kp *kpath.KPath, seg string) bool {
	switch {
	case kp.FieldAll:
		return true
	case kp.Field != nil:
		return *kp.Field == seg
	}
	return false
}

// ToTonyIR emits the document itself. The Go type is a struct so the schema can name what a
// read answers with; the wire is the subtree, with nothing wrapped around it — which is the
// whole claim, that the answer is what the store holds rather than a shape assembled for the
// wire. Hence codec=custom: there is no field here for a generated codec to emit.
func (d Doc) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	if d.Node == nil {
		return ir.Null(), nil
	}
	return d.Node, nil
}

// FromTonyIR takes the document as it stands. Nothing is validated: the answer is any tony
// value, since a path may land on a payload, on a field inside one, or on a slice of the verse.
func (d *Doc) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	d.Node = node
	return nil
}

// ParseAt parses the positional path a read is addressed by. The empty string is the empty
// path — the whole verse — which is what a bare `verse data` asks for.
func ParseAt(s string) (*kpath.KPath, error) {
	at, err := kpath.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("path %q: %w", s, err)
	}
	return at, nil
}

// PrefixPattern is the pattern a prefix and a depth spell: the prefix's names, then a `*` for each
// level down to the depth — what a subtree watch of that depth under that prefix is asked about.
func PrefixPattern(prefix []string, depth int) *kpath.KPath {
	var head, tail *kpath.KPath
	add := func(seg *kpath.KPath) {
		if head == nil {
			head, tail = seg, seg
		} else {
			tail.Next, tail = seg, seg
		}
	}
	for i := range prefix {
		add(&kpath.KPath{Field: &prefix[i]})
	}
	for i := len(prefix); i < depth; i++ {
		add(&kpath.KPath{FieldAll: true})
	}
	return head
}

// AtDepth counts a path's segments: the depth of the nodes it names.
func AtDepth(at *kpath.KPath) int {
	n := 0
	for kp := at; kp != nil; kp = kp.Next {
		n++
	}
	return n
}

// Below lists the nodes exactly `levels` segments beneath the place at — the records of a registry
// whose layout the system decides (`verse.gate.question.<id>` is one level below
// `verse.gate.question`; `verse.resolvers.<name>` two below `verse.resolvers`). The
// registry says how far below its place its records stand; nothing supplies a default.
func Below(r Reader, pred func(Ref) bool, levels int, at ...string) []Entity {
	return r.ListAt(at, len(at)+levels, pred)
}

// BelowErr is Below, reporting its own read.
func BelowErr(r Reader, pred func(Ref) bool, levels int, at ...string) ([]Entity, error) {
	return ListAtErr(r, at, len(at)+levels, pred)
}

// ListErr and ListAtErr are the listings, each answering with the error
// THIS read failed with rather than with whatever the store last remembered.
//
// A LISTING CANNOT SAY SO IN ITS SIGNATURE — List answers a slice, so a read that did not happen
// and a verse holding nothing are the same value — and ReadFailed below was the answer to that
// for a while. It asks the STORE, which keeps one error for every reader sharing it, so what a
// caller gets back depends on who else read in between: a neighbour's success hides this read's
// failure, and a neighbour's failure condemns a read that was fine. A request that could not
// read its own store answered 200 with an empty document (git-issue tyr7f6dhh12ksdd4gnn0).
//
// A store that reports per read says so (`ListErr`/`ListAtErr` — LogdStore does); one that
// cannot falls back to the store-wide flag, which is exactly what the caller had before, so
// adopting these is never worse and is right wherever the capability is there.
func ListErr(r Reader, pred func(Ref) bool) ([]Entity, error) {
	if h, ok := r.(interface {
		ListErr(func(Ref) bool) ([]Entity, error)
	}); ok {
		return h.ListErr(pred)
	}
	out := r.List(pred)
	return out, ReadFailed(r)
}

func ListAtErr(r Reader, at []string, depth int, pred func(Ref) bool) ([]Entity, error) {
	if h, ok := r.(interface {
		ListAtErr([]string, int, func(Ref) bool) ([]Entity, error)
	}); ok {
		return h.ListAtErr(at, depth, pred)
	}
	out := r.ListAt(at, depth, pred)
	return out, ReadFailed(r)
}

// ReadFailed answers the error a store's last read failed with, or nil.
//
// It exists for the callers that must not read an empty answer as an empty verse. List returns
// nil BOTH when a slice holds nothing and when the read did not happen, and the difference is
// the whole question at startup: a charter that restored nothing because there is nothing, and
// one that restored nothing because the store was busy, are the same value and opposite facts.
// The second silently brings a verse back running no rules.
//
// IT ASKS ABOUT THE STORE, not about one read, and the difference matters wherever several
// readers share one: use ListErr and friends above when the question is "did MY read happen".
// This is right for the caller whose question really is about the store — a restore deciding
// whether an empty charter is empty.
func ReadFailed(r Reader) error {
	if h, ok := r.(interface{ ReadError() error }); ok {
		return Unread(h.ReadError())
	}
	return nil
}

// unread is a read that did not happen. The store did not answer, so what it holds is unknown
// rather than empty — and unknown is the one answer that is worth asking for again.
type unread struct{ err error }

func (u unread) Error() string { return u.err.Error() }
func (u unread) Unwrap() error { return u.err }

// Unread marks err as a read that did not happen, or returns nil for nil. ReadFailed marks
// what it reports; a caller whose own read failed marks the error itself.
func Unread(err error) error {
	if err == nil {
		return nil
	}
	return unread{err}
}

// IsUnread reports whether err, or anything it wraps or joins, is a read that did not happen.
//
// It is the question a restore asks: a rule that will not parse is reported and skipped, and a
// listing the store did not answer is waited for and asked again — because the second says
// nothing about the charter, and a verse that took it as empty came back running no rules.
func IsUnread(err error) bool {
	var u unread
	return errors.As(err, &u)
}
