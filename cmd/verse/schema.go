package main

import (
	"fmt"
	"strings"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/api"
)

// schemaCmd prints the schema verse publishes about itself.
//
// Bare, it prints the ROOT: an object whose keys are the top level of the API and whose values
// are the shapes served there (`entities: .[array(verse-entity)]`). That is the entry point the
// published schema did not have — GET /schema is a flat set of thirty-odd named shapes, which
// answers "what is a verse-entity" and never "what is a verse", and an alphabetical list of
// type names is not a thing anyone navigates.
//
// With a name, it prints that one signature, so the root's references are followable by hand:
// `verse schema` then `verse schema verse-entity`. Both are one GET /schema/<name>, and the
// whole set is still at GET /schema for anything that wants to resolve the graph itself.
func schemaCmd() *cli.Command {
	cfg := &struct{ *cli.Command }{}
	return cli.NewCommandAt(&cfg.Command, "schema").
		WithSynopsis("schema — the root schema: what a verse looks like, by top-level path (`schema <name>` for one signature)").
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			name := api.RootSignature
			switch len(args) {
			case 0:
			case 1:
				name = args[0]
			default:
				return fmt.Errorf("%w: schema takes one signature name at most (got %q) — bare it prints the root",
					cli.ErrUsage, strings.Join(args, " "))
			}
			resp, err := httpGet("/schema/"+name, nil)
			if err != nil {
				return fmt.Errorf("schema: %w (is `verse up` running on %s?)", err, g.Addr)
			}
			return printBody(cc, resp)
		})
}
