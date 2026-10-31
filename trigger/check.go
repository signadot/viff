package trigger

import (
	"fmt"

	"github.com/signadot/verse/entity"
)

// Check answers whether one package's levels and rules would install, without installing them.
//
// It is the charter check's engine half (versed/chartercheck.go): the checks an install makes of
// a stratum and of a rule, made in an engine of the check's own, confined to the package's
// address and given what it declares it reads, which is never started, never registered and
// never writes. The strata go in without being written down, so the rules face the file's levels
// and not whatever stood; each rule is vetted as an install vets it, reading the store as the
// installer. Nothing about the check reaches the engine that runs the package.
//
// It answers the first refusal, as an install does, and the rules that would be HELD: installed
// but waiting for a stratum the file does not declare, which an install admits and says.
func Check(store entity.Store, confine Subtree, reads []Subtree, as string, strata []Stratum, rules []Trigger) (held []string, err error) {
	e := New(store, nil)
	e.Confine(confine)
	e.SetReads(reads)
	for _, st := range strata {
		if err := e.addStratum(st); err != nil {
			return nil, fmt.Errorf("stratum %q: %w", st.Name, err)
		}
	}
	seen := map[string]bool{}
	for _, t := range rules {
		if seen[t.Name] {
			return nil, fmt.Errorf("the package names rule %q twice", t.Name)
		}
		seen[t.Name] = true
		hold, err := e.vet(t, as)
		if err != nil {
			return nil, err
		}
		if hold != nil {
			held = append(held, t.Name)
		}
	}
	return held, nil
}
