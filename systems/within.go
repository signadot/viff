package systems

import (
	"fmt"
	"maps"
	"slices"

	"github.com/signadot/tony-format/go-tony/ir"
)

// Within is why e reaches beyond outer, or nil: every system e gives, outer gives, and each block
// of e's lies whole within one of outer's, read as a ceiling whose attribute patterns are outer's
// values (Ceiling.Contains).
func (e Environment) Within(outer Environment) error { return e.within(outer, false) }

// WithinAsWritten is Within for an environment as a charter writes it, before a firing binds it:
// an attribute still to be bound (`$…`) is within where some value of it could be
// (Ceiling.ContainsAsWritten). The launch, bound, is held to outer again by Within.
func (e Environment) WithinAsWritten(outer Environment) error { return e.within(outer, true) }

func (e Environment) within(outer Environment, written bool) error {
	for _, system := range slices.Sorted(maps.Keys(e.Systems)) {
		blocks, ok := outer.Systems[system]
		if !ok {
			return fmt.Errorf("systems.%s: the environment it must lie within reaches no %s", system, system)
		}
		c := ceilingOf(blocks)
		for i, b := range e.Systems[system] {
			if err := c.contains(system, b, written); err != nil {
				at := "systems." + system
				if len(e.Systems[system]) > 1 {
					at = fmt.Sprintf("%s[%d]", at, i)
				}
				return fmt.Errorf("%s: beyond the environment it must lie within: %v", at, err)
			}
		}
	}
	return nil
}

// ceilingOf is an environment's blocks of one system as a ceiling: each attribute's value its
// pattern, a literal, and the verbs with their shapes as given.
func ceilingOf(blocks []Block) Ceiling {
	c := make(Ceiling, 0, len(blocks))
	for _, b := range blocks {
		cb := CeilingBlock{Attributes: map[string]*ir.Node{}, Verbs: b.Verbs}
		for k, v := range b.Attributes {
			cb.Attributes[k] = ir.FromString(v)
		}
		c = append(c, cb)
	}
	return c
}

// Without is e less one verb of a system: taken from every block that gives it, a block giving
// others still giving them, and one giving nothing else gone, since a block that names no verb
// gives them all. A block that does not give the verb stands as written. A system left with no
// block is not reachable. e is not changed.
func (e Environment) Without(system, verb string) Environment {
	blocks, ok := e.Systems[system]
	if !ok {
		return e
	}
	out := Environment{Vars: e.Vars, Systems: maps.Clone(e.Systems)}
	var kept []Block
	for _, b := range blocks {
		given := b.Given(system)
		if _, gives := given[verb]; !gives {
			kept = append(kept, b)
			continue
		}
		verbs := maps.Clone(given)
		delete(verbs, verb)
		if len(verbs) > 0 {
			kept = append(kept, Block{Attributes: b.Attributes, Verbs: verbs})
		}
	}
	if len(kept) == 0 {
		delete(out.Systems, system)
	} else {
		out.Systems[system] = kept
	}
	return out
}
