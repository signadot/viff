package systems

import (
	"fmt"
	"maps"
	"slices"
)

// Within is why e reaches beyond outer, or nil: every system e gives, outer gives, and each block
// of e's lies whole within one of outer's, read as a ceiling whose attribute patterns are outer's
// values (Ceiling.Contains).
func (e Environment) Within(outer Environment) error {
	for _, system := range slices.Sorted(maps.Keys(e.Systems)) {
		blocks, ok := outer.Systems[system]
		if !ok {
			return fmt.Errorf("systems.%s: the environment it must lie within reaches no %s", system, system)
		}
		c := ceilingOf(blocks)
		for i, b := range e.Systems[system] {
			if err := c.Contains(system, b); err != nil {
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

// ceilingOf is an environment's blocks of one system as a ceiling: each attribute's value its one
// pattern, and the verbs with their shapes as given.
func ceilingOf(blocks []Block) Ceiling {
	c := make(Ceiling, 0, len(blocks))
	for _, b := range blocks {
		cb := CeilingBlock{Attributes: map[string][]string{}, Verbs: b.Verbs}
		for k, v := range b.Attributes {
			cb.Attributes[k] = []string{v}
		}
		c = append(c, cb)
	}
	return c
}
