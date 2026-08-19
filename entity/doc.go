package entity

// The verse as ONE DOCUMENT, addressed by one kinded path.
//
// The store never held a list. It holds `{system: {kind: {id: payload}}}` — docSnapshot says
// so in its own field comment, trigger/path.go grounds the charter grammar on it, and every
// walk in logdstore.go descends exactly those three levels. What the read face published was a
// flattening of it: `[{ref, payload}, …]`, out of which every consumer then rebuilt the map
// the store had thrown away.
//
// So this is not a new shape. It is the shape, stopped from being flattened, plus the one
// thing the flattening made unsayable: an address that CONTINUES past the id. `demo.pr.1` is
// an entity, `demo.pr` a kind, `demo` a system, the empty path the whole verse, and
// `demo.pr.1.votes(scott)` a place inside a payload — one grammar, asked at different depths,
// where the read face had three (`--system`/`--kind` to narrow, a ref to address, `--at` to
// reach inside).
//
// A ref is that path with a different delimiter, which is why nothing about identity moves
// here: `Ref.String()` still spells `demo:pr:1`, charters still say `demo:pr:*`, and
// source/own.go's globs still mean what they meant.

import (
	"fmt"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
)

// Doc is the verse's state at a path: the subtree standing there, verbatim.
//
// It wraps a node rather than being one because it is a WIRE TYPE — the body of a read — and
// naming it is what lets the schema say what the route answers. A nil Node means nothing
// stands at the path, which a caller distinguishes from an empty object (a slice with no
// members, i.e. something that exists and is empty).
//
//tony:schemagen=verse-state,notag,codec=custom
type Doc struct{ Node *ir.Node }

// Nest builds the document from entities and narrows it to at, which together are the answer
// to a read: the entities are what the store holds, at is what was asked for.
//
// The two steps are one function because the narrowing is not a filter over the answer, it IS
// the answer — `demo.pr` does not mean "the whole verse, please ignore the rest", it means the
// subtree standing at demo.pr. A nil at is the empty path: the whole verse.
//
// Every level is built with ir.FromMap, which SORTS, so the document is deterministic top to
// bottom and two reads of an unchanged verse are byte-identical. (It also re-parents what it
// is handed, so payloads are cloned on the way in: a payload read out of a snapshot belongs to
// that snapshot's tree, and adopting it would rewire the tree it came from.)
func Nest(ents []Entity, at *kpath.KPath) (Doc, error) {
	node, err := narrow(nestAll(ents), at)
	if err != nil {
		return Doc{}, err
	}
	return Doc{Node: node}, nil
}

// nestAll builds the whole {system: {kind: {id: payload}}} document — one level per segment,
// so an address in verse's own half nests one deeper and lands under the one reserved key.
func nestAll(ents []Entity) *ir.Node {
	tree := map[string]any{} // each level: map[string]any, leaves *ir.Node
	for _, e := range ents {
		segs := e.Ref.Segs()
		at := tree
		for _, s := range segs[:len(segs)-1] {
			next, ok := at[s].(map[string]any)
			if !ok {
				next = map[string]any{}
				at[s] = next
			}
			at = next
		}
		// A payload that is Go-nil still occupies its key: the entity stands there, and what it
		// holds is null. Absence is the key not being here at all.
		if e.Payload == nil {
			at[segs[len(segs)-1]] = ir.Null()
			continue
		}
		at[segs[len(segs)-1]] = clone(e.Payload)
	}
	return treeNode(tree)
}

// treeNode turns the nested map nestAll builds into a document.
func treeNode(tree map[string]any) *ir.Node {
	out := make(map[string]*ir.Node, len(tree))
	for k, v := range tree {
		switch t := v.(type) {
		case map[string]any:
			out[k] = treeNode(t)
		case *ir.Node:
			out[k] = t
		}
	}
	return ir.FromMap(out)
}

// narrow walks a document to a kpath and returns what stands there — nil when nothing does.
//
// A WILDCARD KEEPS ITS CONTAINER'S SHAPE, which is what keeps the answer addressable: `.*` over
// an object answers an object under the same keys, so `demo.pr.*.stage` is
// `{1: open, 2: reviewed}` and every value in it can still be pointed at. Collecting the
// matches into a flat list instead — which is what ir.ListKPath does — would answer with values
// nobody can say the address of, and the whole point of the path is that the answer keeps it.
func narrow(node *ir.Node, kp *kpath.KPath) (*ir.Node, error) {
	if kp == nil {
		return node, nil
	}
	// A comment WRAPS the value it precedes, and a path is about structure: `demo.pr.1.stage`
	// means the same thing whether or not somebody wrote a note above the payload.
	node = ir.Uncomment(node)
	if node == nil {
		return nil, nil
	}
	switch {
	case kp.FieldAll:
		if node.Type != ir.ObjectType {
			return nil, nil // a wildcard over what has no fields matches nothing
		}
		out := map[string]*ir.Node{}
		for i, f := range node.Fields {
			sub, err := narrow(node.Values[i], kp.Next)
			if err != nil {
				return nil, err
			}
			if sub != nil {
				out[f.String] = sub
			}
		}
		if len(out) == 0 {
			return nil, nil
		}
		return ir.FromMap(out), nil
	case kp.IndexAll, kp.SparseIndexAll:
		if node.Type != ir.ArrayType {
			return nil, nil
		}
		var out []*ir.Node
		for _, v := range node.Values {
			sub, err := narrow(v, kp.Next)
			if err != nil {
				return nil, err
			}
			if sub != nil {
				out = append(out, sub)
			}
		}
		if len(out) == 0 {
			return nil, nil
		}
		return ir.FromSlice(out), nil
	}
	child, err := step(node, kp)
	if err != nil {
		return nil, err
	}
	return narrow(child, kp.Next)
}

// step follows one concrete segment: the value it addresses, or nil when nothing is there.
//
// A segment whose KIND does not fit what it lands on is an ERROR rather than a miss —
// `demo.pr[0]` asks for the first element of an object, which is not a question about an empty
// verse but a malformed one, and answering "nothing there" would let a typo read as a fact.
func step(node *ir.Node, kp *kpath.KPath) (*ir.Node, error) {
	switch {
	case kp.Field != nil:
		if node.Type != ir.ObjectType {
			return nil, fmt.Errorf("%q: a field of a %s", *kp.Field, node.Type)
		}
		return ir.Get(node, *kp.Field), nil
	case kp.Index != nil, kp.SparseIndex != nil:
		i := kp.Index
		if i == nil {
			i = kp.SparseIndex
		}
		if node.Type != ir.ArrayType {
			return nil, fmt.Errorf("[%d]: an element of a %s", *i, node.Type)
		}
		if *i < 0 || *i >= len(node.Values) {
			return nil, nil
		}
		return node.Values[*i], nil
	case kp.Key != nil:
		if node.Type != ir.ArrayType {
			return nil, fmt.Errorf("(%s): a keyed element of a %s", *kp.Key, node.Type)
		}
		// The array says what it is keyed BY (`!key(voter)`); without the annotation there is
		// no key to look one up by, and every element answers no.
		//
		// The annotation has to have been STORED, which is the part worth saying out loud: `!key`
		// is a registered merge operator, so an ordinary write EXECUTES it — the array is merged
		// by key and kept without the tag. A payload meant to stay addressable this way is
		// written as data (entity.Raw / AsData), which is what the engine's own writers do.
		field, keyed := node.KeyField()
		if !keyed {
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
	return node, nil
}

// Entities reads the entities back out of a document that Nest rooted at `at` — the inverse of
// the nesting, and the same walk the store's own materialized read does (walkEntityDoc is this
// at the whole-verse depth).
//
// It is what `--paths` is derived from, client-side: a caller that wants the addresses does not
// need the payloads sent again, and an id is a KEY now rather than a token inside a rendered
// ref, so grepping one back out of the answer is no longer a thing scripts should be doing.
//
// Which levels come from the path and which from the document is decided by the path: a
// concrete segment FIXES that level and it does not appear in the answer, a `.*` leaves it in
// the answer as a key. So `demo.*.1` answers `{pr: <payload>}` and the kind is read back out of
// the one key standing there.
//
// It answers nil when the path reaches INSIDE a payload (more than three segments), or names a
// level in a way an entity address cannot be recovered from (an index, a keyed element). Below
// the id there are no entities to recover — only places inside one — and saying so with nil is
// better than naming an entity whose payload is not what the document holds.
func (d Doc) Entities(at *kpath.KPath) []Entity {
	fixed, wild, ok := levels(at)
	if !ok {
		return nil
	}
	var out []Entity
	var rec func(n *ir.Node, seen []string)
	rec = func(n *ir.Node, seen []string) {
		// The path fixed some segments and the document carries the rest, in order:
		// interleaving them is what turns a leaf into the address it stands at.
		segs := make([]string, 0, len(fixed))
		next := 0
		for _, f := range fixed {
			if f == "" {
				if next >= len(seen) {
					break
				}
				f = seen[next]
				next++
			}
			segs = append(segs, f)
		}
		// AT EACH DEPTH, not at one: the two halves of the verse are different depths, so a
		// read that spans both — the whole verse, or a wildcard at the top — reaches an address
		// three levels down on one side and four on the other. RefOf is what says which, and
		// asking it here rather than counting levels is what lets one walk serve both.
		if ref, ok := RefOf(segs); ok {
			out = append(out, Entity{Ref: ref, Payload: n})
			return
		}
		if len(seen) >= wild {
			return
		}
		if n = ir.Uncomment(n); n == nil || n.Type != ir.ObjectType {
			return
		}
		for i, f := range n.Fields {
			rec(n.Values[i], append(append([]string{}, seen...), f.String))
		}
	}
	rec(d.Node, nil)
	return out
}

// EntitiesAt is Entities at a depth the CALLER names, counted in segments from the root — which
// is the only way to ask once an address may be as deep as the tree it lives in.
//
// `Entities` answers at the depth an address has always had (three, four in verse's own half),
// which is what every listing written before this wants. This one answers "what stands at depth
// N under the path I read", and the caller has that number: a pattern from its own path, a
// person from `--depth`.
//
// A node shallower than N contributes nothing rather than being reported short: the question was
// about a depth, and a half-address is not an answer to it.
func (d Doc) EntitiesAt(at *kpath.KPath, depth int) []Entity {
	prefix, err := pathPrefix(at)
	if err != nil || depth < len(prefix) {
		return nil
	}
	var out []Entity
	var rec func(n *ir.Node, segs []string)
	rec = func(n *ir.Node, segs []string) {
		if len(segs) == depth {
			out = append(out, Entity{Ref: RefAt(segs...), Payload: n})
			return
		}
		if n = ir.Uncomment(n); n == nil || n.Type != ir.ObjectType {
			return
		}
		for i, f := range n.Fields {
			rec(n.Values[i], append(append([]string{}, segs...), f.String))
		}
	}
	rec(d.Node, prefix)
	return out
}

// Paths is every address in a document, in order, deepest last within each branch — the walk
// `verse data --paths` prints.
//
// EVERY node, not the ones at some depth, and that is the difference between a listing that can
// be wrong and one that cannot. Entity-hood is a LENS rather than a stored fact
// (docs/working/entity-tree.md), so nothing in a document says where an entity ends: asking for
// one depth answers with the addresses that happen to live there and silently omits everything
// deeper, which is how `--paths` came to print the place it was asked about and nothing else. A
// walk omits nothing; `max` bounds how far it goes when the reader knows what they want, and
// bounding a walk is a convenience where picking a level was a requirement.
//
// max is the total number of segments an address may have, so it is comparable with the depths
// the rest of the model counts in (`entity.Depth`); 0 means no bound. The prefix the read fixed
// is part of every address, so a max below it yields nothing rather than truncating into a lie.
func (d Doc) Paths(at *kpath.KPath, max int) []string {
	prefix, err := pathPrefix(at)
	if err != nil {
		return nil
	}
	if max > 0 && max < len(prefix) {
		return nil
	}
	var out []string
	var rec func(n *ir.Node, segs []string)
	rec = func(n *ir.Node, segs []string) {
		if len(segs) > len(prefix) {
			out = append(out, JoinSegs(segs))
		}
		if max > 0 && len(segs) >= max {
			return
		}
		if n = ir.Uncomment(n); n == nil || n.Type != ir.ObjectType {
			return
		}
		for i, f := range n.Fields {
			rec(n.Values[i], append(append([]string{}, segs...), fieldName(f)))
		}
	}
	rec(d.Node, prefix)
	return out
}

// pathPrefix is the literal segments a read path fixes — what the document below it hangs from.
// A wildcard has no literal, so a path carrying one fixes nothing past it.
func pathPrefix(at *kpath.KPath) ([]string, error) {
	var out []string
	for kp := at; kp != nil; kp = kp.Next {
		if kp.Field == nil {
			return out, nil // a wildcard: the document carries the rest
		}
		out = append(out, *kp.Field)
	}
	return out, nil
}

// levels splits a path into the entity levels it fixes and the number it leaves for the
// document to carry. ok is false for a path that cannot address entities at all.
func levels(at *kpath.KPath) (fixed []string, wild int, ok bool) {
	fixed = make([]string, atDepth(at))
	// A path that has not said which half it is in — the whole verse, or a wildcard at the top
	// — must leave room for the deeper one, and Entities stops at whichever depth is an address.
	if at == nil || at.Field == nil {
		fixed = make([]string, Depth+1)
	}
	depth := 0
	for kp := at; kp != nil; kp = kp.Next {
		if depth >= len(fixed) {
			return nil, 0, false // inside a payload: no entities down here
		}
		switch {
		case kp.Field != nil:
			fixed[depth] = *kp.Field
		case kp.FieldAll: // stays empty: the document carries it
		default:
			return nil, 0, false // an index or a keyed element is not an entity level
		}
		depth++
	}
	for _, f := range fixed {
		if f == "" {
			wild++
		}
	}
	return fixed, wild, true
}

// MatchesAt reports whether a ref falls at or under a path — the ref half of "which entities
// is this read about", which is what `--system`/`--kind` used to be asked as.
//
// It is here rather than on Pattern because Pattern is a WIRE type: a source's Covers carries
// one, so a dimension added for the read face would appear in a document where it means
// nothing. And it is what makes the read face and the watch face the same selector even though
// one answers a document and the other a stream — they narrow by this, and by the same
// Pattern, and a test can say so (TestStateAndWatchNameTheSameSet).
//
// Only the segments an address HAS are asked about: below them a path is inside a payload, and
// an entity is still the thing a change is about.
func MatchesAt(ref Ref, at *kpath.KPath) bool {
	segs := ref.Segs()
	i := 0
	for kp := at; kp != nil && i < len(segs); kp, i = kp.Next, i+1 {
		switch {
		case kp.FieldAll: // every key at this level
		case kp.Field != nil:
			if *kp.Field != segs[i] {
				return false
			}
		default:
			return false // an index or a keyed element: no entity is addressed that way
		}
	}
	return true
}

// EntityPaths spells one address per ENTITY in the answer, at the depth the read asked at — the
// lens, where Paths above is the whole document.
//
// A ref IS a kinded path, spelled with `:` (see the package comment), so these go straight back
// into the commands that take one.
func (d Doc) EntityPaths(at *kpath.KPath) []string {
	ents := d.Entities(at)
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Ref.String())
	}
	return out
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

// AtDepth counts a path's segments, which is what decides whether a path that names nothing is
// an empty SLICE (a system or kind that holds nothing — 0 to 2 segments) or a thing that is not
// there (3 or more).
func AtDepth(at *kpath.KPath) int {
	n := 0
	for kp := at; kp != nil; kp = kp.Next {
		n++
	}
	return n
}

// AtPrefix answers how a read at this path may be NARROWED: the literal segments it can be
// asked under, and the depth entities beneath it sit at (Store.ListAt's two arguments).
//
// It exists because a read at a path was reading the whole verse and filtering. `ListAt`
// narrows and `List` does not, and the difference is not small — a read of one registration
// took 0.74s where the substrate answers the narrow form in under 50ms, and the time barely
// moved with the path because the work never did.
//
// The prefix stops at the first segment that is not a literal name (`*`, an index, a keyed
// element): nothing below a wildcard is a literal to read under. It is capped at the entity
// depth, because a path may reach INSIDE a payload (`demo.pr.1.labels.urgent`) and the entity
// to read is still `demo:pr:1` — the rest is what Nest trims afterwards.
//
// ok is false for a path that names nothing, and the caller must fall back to List there:
// the whole verse spans both halves, whose entities sit at different depths, and one ListAt
// answers for only one depth.
func AtPrefix(at *kpath.KPath) (prefix []string, depth int, ok bool) {
	if at == nil {
		return nil, 0, false
	}
	depth = atDepth(at)
	for kp := at; kp != nil && len(prefix) < depth; kp = kp.Next {
		if kp.FieldAll || kp.Field == nil {
			break
		}
		prefix = append(prefix, *kp.Field)
	}
	if len(prefix) == 0 {
		return nil, 0, false
	}
	return prefix, depth, true
}

// atDepth is how many segments an address under this path has: one more in verse's own half,
// where the reserved root is a level of its own.
//
// It reads the path's FIRST segment, which is the only thing that can say which half a read is
// about before anything has been walked. A path that names no segment at all (the whole verse)
// spans both halves, and the walks handle that by asking again at each top-level key.
func atDepth(at *kpath.KPath) int {
	if at != nil && at.Field != nil && *at.Field == VerseRoot {
		return Depth + 1
	}
	return Depth
}

// ListUnder enumerates the entities beneath a literal prefix, at the depth that prefix implies:
// three segments in the world's half, four in verse's own where the reserved root is a level of
// its own.
//
// It exists because Reader.List is a Match on the ROOT — the whole verse across the wire,
// filtered by the caller — and almost every caller knows the slice it wants. That difference is
// not small on a store carrying reflected repositories: verse spent 88 seconds restoring itself
// before it served anything, and the phases that cost it were exactly the ones reading at the
// root for records that live in one (system, kind).
//
// A caller that genuinely spans slices — an ownership claim with a wildcard system, a read at a
// path naming nothing — keeps List, which is what it is for.
func ListUnder(r Reader, pred func(Ref) bool, at ...string) []Entity {
	depth := Depth
	if len(at) > 0 && at[0] == VerseRoot {
		depth = Depth + 1
	}
	return r.ListAt(at, depth, pred)
}

// ReadFailed answers the error a store's last read failed with, or nil.
//
// It exists for the callers that must not read an empty answer as an empty verse. List returns
// nil BOTH when a slice holds nothing and when the read did not happen, and the difference is
// the whole question at startup: a charter that restored nothing because there is nothing, and
// one that restored nothing because the store was busy, are the same value and opposite facts.
// The second silently brings a verse back running no rules.
func ReadFailed(r Reader) error {
	if h, ok := r.(interface{ ReadError() error }); ok {
		return h.ReadError()
	}
	return nil
}
