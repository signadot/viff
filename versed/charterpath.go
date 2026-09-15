package versed

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/trigger"
)

// ONE PATH INTO THE CHARTER.
//
// `?path=` is an address followed by a reach INTO the document standing at it, in one list of
// segments — the grammar `verse data demo.pr.1.stage` already has, where nobody had to be told
// that `.stage` was a different kind of step:
//
//	verse-dev.drift                 the charter installed there
//	verse-dev.drift.rules           its rules
//	verse-dev.drift.rules.watcher   one rule
//	verse-dev.drift.rules.watcher.action   …and into it
//	verse-dev.drift.writes          what it may write
//	rules.watcher                   baseline's `watcher`
//
// THE SPLIT IS RESOLVED HERE, not by the client, because this is the end that knows the tree —
// the same reason the tree is built here. The address is the LONGEST prefix that names a place
// something is installed at or under; what remains indexes the node.
//
// Greedy, so a package at an address ending in `rules` shadows the key of the address above it.
// `?at=` settles that: given, it IS the address and `path=` is only the reach, which is what the
// CLI's `--` will send. Two spellings for one question, and the second exists for the case the
// first cannot express rather than as a second way to ask.

// splitCharterPath is the greedy split: the longest prefix of segs naming a place, and the rest.
//
// A place is an installed address OR a prefix of one — `verse-dev` is a place when
// `verse-dev.drift` is installed, because the tree has a node there and `verse charter
// verse-dev` has always answered about it.
func (s *Server) splitCharterPath(segs []string) (at string, rest []string) {
	// SEGMENT BY SEGMENT: an installed address is a kinded path, so `verse-dev.drift` typed as
	// ONE step is a place whose name holds a dot and reaches no two-segment package.
	var places [][]string
	for _, addr := range s.packagesSeen() {
		if p, err := entity.PathSegs(addr); err == nil {
			places = append(places, p)
		}
	}
	longest := 0
	for n := 1; n <= len(segs); n++ {
		for _, addr := range places {
			if len(addr) >= n && slices.Equal(addr[:n], segs[:n]) {
				longest = n
				break
			}
		}
	}
	return entity.JoinSegs(segs[:longest]), segs[longest:]
}

// charterReach walks the node document by the remaining segments.
//
// One special case, and it is the one that makes `rules watcher` read as a path at all: a LIST
// is indexed by the `name` of its elements, because that is how a charter addresses a rule or a
// level. Everything else is ordinary object indexing, which is what lets a path keep going —
// `rules.watcher.action` is a question somebody will ask the moment the first three work.
func charterReach(node *ir.Node, rest []string) *ir.Node {
	for _, seg := range rest {
		node = codec.Unwrap(node)
		if node == nil {
			return nil
		}
		switch node.Type {
		case ir.ObjectType:
			var next *ir.Node
			for i, key := range node.Fields {
				if key != nil && key.Type == ir.StringType && key.String == seg {
					next = node.Values[i]
					break
				}
			}
			if next == nil {
				return nil
			}
			node = next
		case ir.ArrayType:
			next := namedElement(node, seg)
			if next == nil {
				return nil
			}
			node = next
		default:
			return nil // a scalar has nothing under it
		}
	}
	return node
}

// namedElement finds the element of a rules/strata list whose `name` is this.
func namedElement(list *ir.Node, name string) *ir.Node {
	for _, el := range list.Values {
		e := codec.Unwrap(el)
		if e == nil || e.Type != ir.ObjectType {
			continue
		}
		for i, key := range e.Fields {
			if key == nil || key.Type != ir.StringType || key.String != "name" {
				continue
			}
			if v := codec.Unwrap(e.Values[i]); v != nil && v.String == name {
				return el
			}
		}
	}
	return nil
}

// charterPathNotFound is the 404, and it is where the old `show`'s search earns its keep.
//
// `verse charter show <name>` read the whole tree and looked the name up in every charter, which
// is a second way to address things — so it goes. What was USEFUL about it was never the
// addressing, it was being told where the thing you named actually is, and that is worth exactly
// as much said in the refusal. So: say which half of the path failed, and when a name is a rule
// or a level somewhere else, say where and give the path that reaches it.
func (s *Server) charterPathNotFound(at string, rest []string) string {
	where := "the baseline charter"
	if at != "" {
		where = at
	}
	if len(rest) == 0 {
		return fmt.Sprintf("no charter is installed at %s, and none under it", where)
	}
	// A FIRST SEGMENT THAT IS NOT A KEY is a question about a PLACE, not about something
	// inside one: `verse c nowhere.at.all` reached no address, so answering "baseline holds no
	// nowhere.at.all" describes the wrong failure — nobody asked baseline anything.
	if !charterKeys[rest[0]] {
		return fmt.Sprintf("no charter is installed at %s, and none under it",
			strings.Join(rest, "."))
	}
	// The last segment is the name somebody was reaching for; the ones before it say what kind
	// of thing they expected it to be.
	name := rest[len(rest)-1]
	msg := fmt.Sprintf("%s holds no %s", where, strings.Join(rest, "."))
	if elsewhere := s.charterHolding(name); len(elsewhere) > 0 {
		var says []string
		for _, e := range elsewhere {
			if e.at == "" {
				says = append(says, fmt.Sprintf("baseline has a %s called %q (`verse c %s %s`)",
					oneOf(e.kind), name, e.kind, name))
				continue
			}
			says = append(says, fmt.Sprintf("%s has a %s called %q (`verse c %s %s %s`)",
				e.at, oneOf(e.kind), name, e.at, e.kind, name))
		}
		msg += " — " + strings.Join(says, ", ")
	}
	return msg
}

// charterKeys is what a node document holds, which is what a path may reach for after the
// address ends. Named here rather than derived from a node, because the question is asked when
// there is no node — a path that reached no address at all.
var charterKeys = map[string]bool{
	"writes": true, "reads": true, "rules": true, "strata": true, "children": true,
}

// oneOf is the key said about a single thing: a charter has RULES and LEVELS in it, and telling
// somebody they have "a rules called watcher" is the kind of sentence that reads as a bug in
// the thing that printed it.
func oneOf(kind string) string {
	if kind == "strata" {
		return "level"
	}
	return "rule"
}

// held is one answer to "who has something by this name".
type held struct {
	at   string
	kind string // "rules" or "strata", the key it is under
}

// charterHolding is the addresses whose charter holds a rule or a level by this name, asked of
// the ENGINES rather than of the tree: they hold the installed charter in memory, so this costs
// no read, and it is the same question ResumeSelections asks when a selection names a rule.
func (s *Server) charterHolding(name string) []held {
	var out []held
	look := func(at string, eng *trigger.Engine) {
		if eng == nil {
			return
		}
		if eng.Knows(name) {
			out = append(out, held{at: at, kind: "rules"})
		}
		if _, ok := eng.Stratum(name); ok {
			out = append(out, held{at: at, kind: "strata"})
		}
	}
	look("", s.Eng)
	for _, at := range s.packagesSeen() {
		eng, err := s.packageEngine(at)
		if err != nil {
			continue
		}
		look(at, eng)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out
}
