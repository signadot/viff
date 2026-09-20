package entity

import (
	"fmt"

	"github.com/signadot/tony-format/go-tony/ir"
)

// The mirror's migrations, which are two respellings: `owner` held out by a grant line is
// `child-owner`, and the field saying what a node created beneath holds out is `child-grants`
// where it was `default`.
//
// Each runs ONCE, at start, before anything reads the mirror, and from then on the grammar reads
// the new spelling and nothing else — so the old one is known here and nowhere, in the words
// this file rewrites rather than in a reader that would go on accepting them for the life of a
// deployment. A record the grammar refuses holds out nothing, so a store left half way is a
// verse where nobody can spawn a task or install a charter: the caller refuses to serve on an
// error (versed), as it does on a seed that did not land.
//
// BASELINE ONLY. Nothing enumerates scopes, and a scope's records stand in its own overlay, so a
// scope that outlived the upgrade keeps its old spelling and its places close — said in the log
// by the record that would not parse, and mended by dropping the scope, which is what made it.

// oldOwnerKey is the level key as it was written in a grant line and a default.
const oldOwnerKey = string(OwnerLevel)

// MigrateChildOwner rewrites every record whose grants or default hold out `owner` under the old
// key. It reports how many it rewrote and which it NARROWED: a line that named `owner` without
// `below` reached the node itself as well as what is beneath it, and there is no such grant any
// more — a node has one owner, fixed when it is created — so that half goes, and is said.
func MigrateChildOwner(s Store) (rewrote int, narrowed []string, err error) {
	return respell(s, oldOwnerKey, ChildOwnerKey, true)
}

// UnmigrateChildOwner is the way back, for a binary from before the migration: it cannot read
// the new key, and a line it cannot read refuses the whole field, so rolling the image back
// without this leaves every seed row holding out nothing. Run once, by the new binary, before
// the old one starts.
func UnmigrateChildOwner(s Store) (rewrote int, err error) {
	n, _, err := respell(s, ChildOwnerKey, oldOwnerKey, false)
	return n, err
}

// oldChildGrantsField is what ChildGrantsField was called: a record an older binary wrote says
// what is created beneath it holds out under this name.
const oldChildGrantsField = "default"

// MigrateChildGrants renames that field on every record carrying the old name. It runs BEFORE
// MigrateChildOwner, which reads the field by its new name to respell the levels inside it. A
// record left under the old name would say nothing the check reads, and what is created beneath
// it would hold out the root's instead of its own — a place set private creating what everyone
// reads — so it is a migration and not a field quietly dropped.
func MigrateChildGrants(s Store) (renamed int, err error) {
	return refield(s, oldChildGrantsField, ChildGrantsField)
}

// UnmigrateChildGrants is the way back, run with UnmigrateChildOwner and after it.
func UnmigrateChildGrants(s Store) (renamed int, err error) {
	return refield(s, ChildGrantsField, oldChildGrantsField)
}

// mirrorRefs is every record of the mirror, the root's included, which stands beside it.
func mirrorRefs(s Store) ([]Ref, error) {
	mirror := []string{VerseRoot, PermsSystem, PermsKind}
	recs, err := ListAtErr(s, mirror, len(mirror)+1, nil)
	if err != nil {
		return nil, fmt.Errorf("listing the perms mirror: %w", err)
	}
	refs := make([]Ref, 0, len(recs)+1)
	for _, e := range recs {
		refs = append(refs, e.Ref)
	}
	return append(refs, permsRoot), nil
}

// refield moves a record's field `from` to `to`, in a write guarded by what it read. A record
// that already says `to` keeps what it says there, and loses `from`.
func refield(s Store, from, to string) (int, error) {
	refs, err := mirrorRefs(s)
	if err != nil {
		return 0, err
	}
	var renamed int
	for _, ref := range refs {
		for {
			cur, ok, err := s.Get(ref)
			if err != nil {
				return renamed, fmt.Errorf("reading %s: %w", ref, err)
			}
			old := Get(cur.Payload, from)
			if !ok || old == nil {
				break
			}
			next := Obj{from: Removed()}
			if Get(cur.Payload, to) == nil {
				next[to] = AsData(old)
			}
			committed, _, err := s.Write(Write{
				At: ref.At(), Value: next.Payload(), Principal: SystemPerms,
				When: []Require{{Pattern: Exactly(cur.Payload)}},
			})
			if err != nil {
				return renamed, fmt.Errorf("rewriting %s: %w", ref, err)
			}
			if committed {
				renamed++
				break
			}
		}
	}
	return renamed, nil
}

// respell rewrites the level key `from` as `to` in every record's grants and default. forward
// says which way: going forward a line holding out nothing else drops its `below`, since
// `child-owner` says beneath by itself; going back every such line gains one, since `owner`
// without it reached the node too.
func respell(s Store, from, to string, forward bool) (int, []string, error) {
	refs, err := mirrorRefs(s)
	if err != nil {
		return 0, nil, err
	}
	var rewrote int
	var narrowed []string
	for _, ref := range refs {
		for {
			cur, ok, err := s.Get(ref)
			if err != nil {
				return rewrote, narrowed, fmt.Errorf("reading %s: %w", ref, err)
			}
			if !ok {
				break
			}
			next, lost, changed := respelled(cur.Payload, from, to, forward)
			if !changed {
				break
			}
			committed, _, err := s.Write(Write{
				At: ref.At(), Value: next, Principal: SystemPerms,
				When: []Require{{Pattern: Exactly(cur.Payload)}},
			})
			if err != nil {
				return rewrote, narrowed, fmt.Errorf("rewriting %s: %w", ref, err)
			}
			if committed {
				rewrote++
				if lost {
					key, _ := mirrorKey(ref)
					narrowed = append(narrowed, key)
				}
				break
			}
		}
	}
	return rewrote, narrowed, nil
}

// respelled is a record's payload with the key rewritten, as a value that REPLACES the fields it
// touches when written over the record (an array write merges by position, so each line says
// what to remove from the one it lands on). lost reports a line narrowed on the way forward.
func respelled(rec Payload, from, to string, forward bool) (next Payload, lost, changed bool) {
	out := Obj{}
	if grants := ir.Uncomment(Get(rec, GrantsField)); grants != nil && grants.Type == ir.ArrayType {
		arr := &ir.Node{Type: ir.ArrayType}
		for _, line := range grants.Values {
			ls, narrowed, touched := respelledLine(ir.Uncomment(line), from, to, forward)
			lost, changed = lost || narrowed, changed || touched
			arr.Values = append(arr.Values, ls...)
		}
		// BY POSITION, which is how the array is merged: a line going back may become two, so
		// line i of the new list is not always line i of the old, and what it has to remove is
		// whatever stands where it lands.
		for i := range arr.Values {
			if i < len(grants.Values) {
				arr.Values[i] = withRemovals(arr.Values[i], ir.Uncomment(grants.Values[i]))
			}
		}
		out[GrantsField] = arr
	}
	if def := ir.Uncomment(Get(rec, ChildGrantsField)); def != nil && def.Type == ir.ObjectType && Get(def, from) != nil {
		d := Obj{}
		for name := range Fields(def) {
			key := name
			if name == from {
				key = to
			}
			d[key] = Get(def, name)
		}
		out[ChildGrantsField] = withRemovals(d.Payload(), def)
		changed = true
	}
	if !changed {
		return nil, false, false
	}
	return out.Payload(), lost, true
}

// respelledLine is one grant line with the key rewritten — as two lines, going back, where it
// holds out `child-owner` beside a level that is about the node itself: the old `owner` reached
// the node unless the WHOLE line was about what is beneath it, so keeping them together would
// hold `owner` out at the node, which is wider than what stood and the wrong way to be wrong on
// a rollback.
//
// BENEATH IS SPELLED TWO WAYS, and the old grammar read each in one place only: `below: true` on
// a line about the record's own node, and a trailing `*` on a line that names its path in `at`,
// where `below` was not read at all. So a line with an `at` goes back under `at: [..., "*"]`.
func respelledLine(line *ir.Node, from, to string, forward bool) (next []*ir.Node, narrowed, changed bool) {
	if line == nil || line.Type != ir.ObjectType || Get(line, from) == nil {
		return []*ir.Node{line}, false, false
	}
	at, star, hasAt := atOf(line)
	below := false
	if b := ir.Uncomment(Get(line, "below")); !hasAt && b != nil && b.Type == ir.BoolType {
		below = b.Bool
	}
	owner, rest := Obj{to: Get(line, from)}, Obj{}
	for name := range Fields(line) {
		switch name {
		case from, "below":
		case "at":
			owner[name], rest[name] = Get(line, name), Get(line, name)
		default:
			rest[name] = Get(line, name)
		}
	}
	others := len(rest)
	if _, has := rest["at"]; has {
		others--
	}
	whole := func() []*ir.Node {
		for name, v := range rest {
			owner[name] = v
		}
		return []*ir.Node{owner.Payload()}
	}
	if forward {
		// `child-owner` says beneath by itself, so it carries no `below`; the levels beside it
		// keep theirs. Without `below` or a trailing `*` the old line reached the node too, and
		// that half is what goes.
		if below && others > 0 {
			owner["below"] = ir.FromBool(true)
		}
		return whole(), !(below || star), true
	}
	switch {
	case star:
		// The whole line is beneath its path already, and says so where the old grammar reads it.
		return whole(), false, true
	case hasAt:
		owner["at"] = strList(append(append([]string{}, at...), "*"))
	default:
		owner["below"] = ir.FromBool(true)
	}
	if others == 0 {
		return []*ir.Node{owner.Payload()}, false, true
	}
	if below {
		// The whole line was beneath already, so it goes back as one.
		return whole(), false, true
	}
	return []*ir.Node{owner.Payload(), rest.Payload()}, false, true
}

// atOf is a line's `at` as segments, without the trailing `*` that says beneath, and whether it
// had one. A line with no `at`, or one that does not read, has none: the grammar refuses that
// line where it is read, and it goes back as it stands.
func atOf(line *ir.Node) (segs []string, star, ok bool) {
	at := ir.Uncomment(Get(line, "at"))
	switch {
	case at == nil:
		return nil, false, false
	case at.Type == ir.StringType:
		var err error
		if segs, err = ExprSegs(at.String); err != nil {
			return nil, false, false
		}
	case at.Type == ir.ArrayType:
		segs = Strs(line, "at")
	default:
		return nil, false, false
	}
	if n := len(segs); n > 0 && segs[n-1] == "*" {
		return segs[:n-1], true, true
	}
	return segs, false, true
}
