package trigger

import (
	"fmt"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/entity"
)

// WHAT A PACKAGE READS, DECLARED — and with it the thing confinement alone could not give:
// isolation from INTERFERENCE, not only from damage.
//
// A confinement bounds what a package may write (confine.go). Two packages that write different
// places are still free to read each other, and two of those are a feedback loop neither
// charter's author can see in their own file:
//
//	package a   reads b:*   writes a:*
//	package b   reads a:*   writes b:*
//
// Nothing catches that today: the level discipline is per package, so a cross-package derive has
// no ordering to violate, and the cycle throttle bounds a chain within one engine. It was filed
// as the limit of what packages promise (git-issue dpz3jxzdh12ksb6mg5n0), and this is the answer
// that issue names: a package DECLARES what it reads, and the graph over packages is required to
// be acyclic at install.
//
//	reads: [ !verse-path [git], drift ]
//	---
//	- name: proposal-says
//	  condition: {subject: {system: merge, kind: proposal}}
//	  action: {patch: {says: !get-path {system: drift, kind: head, id: …, path: result.report}}}
//
// THREE THINGS AT ONCE, which is why it is a declaration rather than an inference. A cycle is
// refused rather than throttled; a reader sees a package's dependencies without reading its
// rules; and "what does changing this affect" becomes a question about the graph.
//
// AN ABSENT `reads:` IS NOT "EVERYTHING", it is nothing: a package with no declaration reads
// only inside its own address. The other default would leave every package's edges undeclared,
// which is the state this replaces — and a package that reads nothing outside itself is exactly
// the one that cannot interfere, so the safe default is also the strict one. The refusal names
// the line to add.
//
// WHAT IT DOES NOT COVER, stated rather than implied:
//
//   - BASELINE. The unpackaged charter may write anywhere, so a loop through baseline is not a
//     loop between packages and this cannot see it. Baseline is the operator's own charter,
//     which is the same reason it is unconfined.
//   - A SPAWNED TASK. Its reads are its own, under its grant; store reads are what this bounds.
//   - THE WORLD OUTSIDE ANY PACKAGE. Importing `git` when no package is installed at `git` adds
//     a node nothing writes, so it adds no edge — and if somebody later installs a package
//     there, THAT install is where the cycle appears, and it is checked then too.
//
// The check is STATIC and complete at the granularity it checks, which is the address prefix. A
// read's system and kind are always literal — a `$reference` or a glob reaches the id and no
// further (path.go: "System, Kind string // always literal") — so where a read LANDS is knowable
// at install even when which entity it lands on is not. That is what lets this be an install-time
// refusal with no run-time half, unlike the write confinement, whose derive targets are templated.

// SetReads declares the subtrees this engine's charter may read outside its own confinement.
//
// Set before the rules are installed, like the confinement itself. Re-setting REPLACES the
// declared set rather than accumulating into it, so an operator narrows a package by setting a
// smaller list; it does not itself check what is already installed against the new set —
// checkReads does that, at install.
func (e *Engine) SetReads(in []Subtree) {
	e.reads = append([]Subtree(nil), in...)
}

// Imports is what this engine declares it reads beyond its own address.
func (e *Engine) Reads() []Subtree { return append([]Subtree(nil), e.reads...) }

// readable is everything this engine may read: its own address, and what it declares it reads.
//
// Its own address is always readable and is never written down — a package reading what it
// wrote is not a dependency on anybody, and requiring it to be declared would make every
// charter carry a line that says nothing.
func (e *Engine) readable() Matches {
	out := Matches{e.confine.match()}
	for _, im := range e.reads {
		out = append(out, im.match())
	}
	return out
}

// checkReads refuses a rule that reads outside what its package declares.
//
// Unconfined engines are unchecked: baseline has no address to be outside of, and a charter that
// may write anywhere has nothing a reads graph would tell anybody.
func (e *Engine) checkReads(t Trigger) error {
	if !e.confine.confined() {
		return nil
	}
	allowed := e.readable()
	for _, st := range t.outsideReads() {
		if allowed.contains(st.match()) {
			continue
		}
		return fmt.Errorf("rule %q reads %s, which %s does not declare: a charter installed at an "+
			"address reads inside it and what it declares, so that the graph over packages can be "+
			"checked for a cycle. Add it: `reads: [%s]`%s",
			t.Name, st.readText(), e.confine, st.stamp(), readsHint(e.reads))
	}
	return nil
}

// readsHint says what is declared already, so a refusal does not send somebody to look it up.
func readsHint(in []Subtree) string {
	if len(in) == 0 {
		return " (this package reads nothing)"
	}
	var names []string
	for _, im := range in {
		names = append(names, im.stamp())
	}
	sort.Strings(names)
	return " (it reads " + strings.Join(names, ", ") + ")"
}

// readText is a subtree as a refusal names it — the address prefix a read landed in.
func (s Subtree) readText() string {
	if !s.confined() {
		return "anywhere (a read with no system names every entity there is)"
	}
	return s.String()
}

// importReads is every subtree this rule READS: its condition's, and the path reads anywhere in
// it — an action's `!get-path`, a gate's, a hook's.
//
// `reads()` is the condition side and is what a watch is built from; the action side is not
// watched (a read inside an action is answered when the action fires, not before) and is a
// dependency all the same. So this walks the rule's own document for path reads rather than
// enumerating the places an action can hold one — a walk cannot fall behind a new field.
func (t Trigger) outsideReads() []Subtree {
	seen := map[Subtree]bool{}
	var out []Subtree
	add := func(st Subtree) {
		if !seen[st] {
			seen[st] = true
			out = append(out, st)
		}
	}
	for _, st := range t.reads() {
		add(st)
	}
	if doc, err := t.ToTonyIR(); err == nil {
		specs, err := readTargets(doc)
		if err == nil {
			for _, sp := range specs {
				// The read's PLACE, which is its address minus the entity it names — and which
				// a path read carries as a path rather than as two dimensions. Reading
				// System/Kind here answered "" for an `at:` read, so a declared read of
				// `verse-dev.drift.finding` looked like a read of everything.
				add(SubtreeAt(sp.place().Segs()...))
			}
		}
	}
	return out
}

// ParseReads reads the `reads:` declaration — a list of addresses in either spelling, the
// dotted one an operator types (`drift`, `demo.pr`) or the steps one a path prints
// (`!verse-path [demo pr]`), which trigger.Match already takes for the same reason.
func ParseReads(in []string) ([]Subtree, error) {
	out := make([]Subtree, 0, len(in))
	for _, s := range in {
		st, err := ParseAt(s)
		if err != nil {
			return nil, fmt.Errorf("reads: %w", err)
		}
		if !st.confined() {
			return nil, fmt.Errorf("reads: an empty address reads the whole verse, which is "+
				"what having a declaration is for; name a system (`git`) or a system and a kind "+
				"(`demo.pr`) — got %q", s)
		}
		out = append(out, st)
	}
	return out, nil
}

// StampReads renders the declaration the way a record stores them: the dotted address each, in the
// order they were declared.
func StampReads(in []Subtree) []string {
	out := make([]string, 0, len(in))
	for _, st := range in {
		out = append(out, st.stamp())
	}
	return out
}

// Overlaps reports whether two subtrees can name one address — which is the question a read
// graph asks: package P depends on package Q exactly when what P reads meets what Q may write.
//
// Level by level, where an empty level is a wildcard: `demo` overlaps `demo.pr` because one
// contains the other, and `demo.pr` and `demo.issue` overlap nowhere.
func (s Subtree) Overlaps(o Subtree) bool {
	// SEGMENT BY SEGMENT, as far as the shorter goes, each asked of kpath (entity.Overlap,
	// entity.KPathDisjoint): `demo` meets `demo.pr` because one extends the other, `demo.pr`
	// misses `demo.issue`, and `demo.*.x` meets `*.pr.x` at `demo.pr.x` — which neither is a
	// prefix of, so asking MatchesPrefix each way would miss it. Parsing rather than splitting is
	// what keeps a quoted segment (`demo."a.b"`) one segment.
	mine, err := kpath.Parse(s.kp)
	if err != nil {
		return false
	}
	theirs, err := kpath.Parse(o.kp)
	if err != nil {
		return false
	}
	return entity.Overlap(entity.KPathSegments(mine), entity.KPathSegments(theirs), entity.KPathDisjoint)
}

// PackageKind is where a package's own declaration lives: `verse:trigger:package:<address>`,
// beside the rules and the strata that carry the same `at`.
//
// A RECORD OF ITS OWN, rather than a field stamped on each rule, because the reads declaration are a
// property of the PACKAGE and not of any rule in it: two installs into one address would
// otherwise each carry their own answer, and the graph would depend on which rule you read. The
// id is the address, so a package has exactly one.
const PackageKind = "package"

// PackageRef names the entity holding one package's declaration.
func PackageRef(at string) entity.Ref { return entity.VerseRef(CharterSystem, PackageKind, at) }

// IsCharterPackage reports whether an address is a package declaration's.
func IsCharterPackage(r entity.Ref) bool {
	return r.Reserved() && r.System() == CharterSystem && r.Kind() == PackageKind
}

// ReadsField is where the declaration keeps them, as the dotted addresses StampReads writes.
const ReadsField = "reads"

// ── the reads graph ─────────────────────────────────────────────────────────

// Cycle reports the cycle a package's reads would close, or "" for none.
//
// THE GRAPH IS OVER PACKAGES and its edges are dependencies: P → Q when what P READS meets what
// Q may WRITE, which is Q's address, because a package writes nowhere else. So a read of an
// address no package is installed at adds no edge at all — `git` is a source's slice, and
// reading a source is not depending on a package.
//
// A CYCLE IS THE FEEDBACK LOOP, exactly: P reads what Q writes and Q reads what P writes, so a
// write on either side can wake the other, and round. Refusing it at install is what makes
// packages compose — the alternative is the cycle throttle noticing later, in a chain that
// crosses two engines and belongs to neither charter's author.
//
// It is asked of the WHOLE graph rather than of the new package's neighbours, because an install
// changes one node's edges and a cycle is a property of the graph — and it is asked on every
// install, including a re-install that narrows what it reads, since the graph moves either way.
func Cycle(pkgs map[string][]Subtree) string {
	// The nodes, in one order, so two runs report the same cycle out of several.
	names := make([]string, 0, len(pkgs))
	for at := range pkgs {
		names = append(names, at)
	}
	sort.Strings(names)
	at := map[string]Subtree{}
	for _, n := range names {
		st, err := ParseAt(n)
		if err != nil {
			continue // a record nobody can parse is not an edge; the restore reports it
		}
		at[n] = st
	}
	edges := map[string][]string{}
	for _, p := range names {
		for _, im := range pkgs[p] {
			for _, q := range names {
				if q == p {
					continue
				}
				if im.Overlaps(at[q]) {
					edges[p] = append(edges[p], q)
				}
			}
		}
	}
	// Depth-first, keeping the path so the refusal can SHOW the loop: "a → b → a" is the whole
	// explanation, and a bare "there is a cycle" would send somebody to draw it by hand.
	const (
		white = 0
		grey  = 1
		black = 2
	)
	colour := map[string]int{}
	var path []string
	var walk func(string) string
	walk = func(n string) string {
		colour[n] = grey
		path = append(path, n)
		for _, m := range edges[n] {
			switch colour[m] {
			case grey:
				// The loop is the tail of the path from m onward, closed back onto m.
				for i, x := range path {
					if x == m {
						return strings.Join(append(append([]string{}, path[i:]...), m), " → ")
					}
				}
			case white:
				if c := walk(m); c != "" {
					return c
				}
			}
		}
		path = path[:len(path)-1]
		colour[n] = black
		return ""
	}
	for _, n := range names {
		if colour[n] == white {
			if c := walk(n); c != "" {
				return c
			}
		}
	}
	return ""
}

// RecheckReads asks the reads check of every rule already installed, which is what an operator
// narrowing a declaration needs: a rule that was allowed under the old declaration and is not under
// the new one must refuse the CHANGE rather than be quietly left standing outside it.
func (e *Engine) RecheckReads() error {
	for _, t := range e.Triggers() {
		if err := e.checkReads(t); err != nil {
			return fmt.Errorf("%w — it is installed, so widen the declaration or remove the rule "+
				"first", err)
		}
	}
	return nil
}

// ReadsFromNode reads the `reads:` declaration out of the document it was written in,
// taking either spelling of an address per element:
//
//	reads: [drift, "demo.pr"]              the dotted path an operator types
//	reads: [!verse-path [demo pr], drift]  an address as a list of segments
//
// The second is why this exists rather than a `[]string` decode: `!verse-path` is how an
// address is written as steps everywhere else in a charter (tonycodec.go), and a declaration
// that took only the dotted form would be the one place in the grammar where the printed form
// of an address cannot be pasted back.
func ReadsFromNode(node *ir.Node) ([]Subtree, error) {
	node = unwrap(node)
	if node == nil {
		return nil, nil
	}
	var field *ir.Node
	if node.Type == ir.ObjectType {
		for i, key := range node.Fields {
			if key != nil && key.Type == ir.StringType && key.String == ReadsField {
				field = node.Values[i]
			}
		}
	}
	field = unwrap(field)
	if field == nil {
		return nil, nil
	}
	if field.Type != ir.ArrayType {
		return nil, fmt.Errorf("reads: a list of addresses (`reads: [git, drift]`), not %v",
			field.Type)
	}
	out := make([]Subtree, 0, len(field.Values))
	for _, v := range field.Values {
		st, err := importSubtree(v)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// importSubtree reads one element of the list, in either spelling.
func importSubtree(v *ir.Node) (Subtree, error) {
	if tagName(v) == VersePathTag {
		m, err := stepsMatch(v)
		if err != nil {
			return Subtree{}, fmt.Errorf("reads: %w", err)
		}
		if m.ID() != "" {
			return Subtree{}, fmt.Errorf("reads: %s names an ENTITY; a read is a place — a "+
				"system (`git`), or a system and a kind (`demo.pr`)", subtreeOf(m).String())
		}
		st := subtreeOf(m)
		if !st.confined() {
			return Subtree{}, fmt.Errorf("reads: an empty address reads the whole verse, " +
				"which is what having a declaration is for")
		}
		return st, nil
	}
	text, ok := stepText(unwrap(v))
	if !ok {
		return Subtree{}, fmt.Errorf("reads: an address is a name or a !%s vector; got %v",
			VersePathTag, v.Type)
	}
	sts, err := ParseReads([]string{text})
	if err != nil {
		return Subtree{}, err
	}
	return sts[0], nil
}
