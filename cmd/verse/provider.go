package main

import (
	"fmt"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/entity"
)

// providerCmd lists the provider registry — the attribute-rich doers a select gate draws
// from (provider.<capability>.<id>). Providers are registered as entities (e.g.
// `verse data provider:review:claude --put '{"driver":"llm","tier":"premium"}'`).
//
// Singular with a plural alias, like every other collection, even though it has no verbs to
// act through: the rule an operator learns is "the command is the singular", and one listing
// that answers only to the plural is one exception to remember.
func providerCmd() *cli.Command {
	return listing("provider", "provider — the provider registry (provider.<capability>.<id>)",
		func(cc *cli.Context, args []string) error {
			// The capability is the KIND, read out of the document rather than sliced back off
			// the front of a rendered ref.
			ents, err := stateAt("provider.*.*")
			if err != nil {
				return fmt.Errorf("provider: %w", err)
			}
			if len(ents) == 0 {
				fmt.Fprintln(cc.Out, "no providers")
				return nil
			}
			for _, e := range ents {
				payload := entity.ToObj(e.Payload)
				// The provider registry's own layout: provider.<capability>.<name>.
				capability := ""
				if segs := e.Ref.Segs(); len(segs) == 3 {
					capability = segs[1]
				}
				line := fmt.Sprintf("%-10s %-14s driver=%v", capability, e.Ref.Name(), payload["driver"])
				for k, v := range payload {
					if k != "driver" {
						line += fmt.Sprintf(" %s=%v", k, v)
					}
				}
				fmt.Fprintln(cc.Out, line)
			}
			return nil
		}).WithAliases("providers")
}
