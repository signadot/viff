package main

import (
	"fmt"
	"strings"

	"github.com/scott-cotton/cli"
)

// schemaCmd prints the schema verse publishes about itself.
//
// Bare, it prints the WHOLE thing: the root — an object whose keys are the top level of the API
// and whose values are the shapes served there — and then every signature those values name. It
// is one document because that is the question being asked. It printed only the root for a
// while, which answered "what is a verse" and left the shapes it named a second lookup away, so
// the command that exists to show you the schema showed you eight references to it.
//
// With a name, it prints that one signature — `verse schema verse-entity` — for when you know
// what you are looking for and do not want the other thirty.
func schemaCmd() *cli.Command {
	cfg := &struct{ *cli.Command }{}
	return cli.NewCommandAt(&cfg.Command, "schema").
		WithSynopsis("schema — what a verse looks like: the top-level paths and every shape they name (`schema <name>` for one)").
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			path := "/schema"
			switch len(args) {
			case 0:
			case 1:
				path += "/" + args[0]
			default:
				return fmt.Errorf("%w: schema takes one signature name at most (got %q) — bare it prints all of them",
					cli.ErrUsage, strings.Join(args, " "))
			}
			resp, err := httpGet(path, nil)
			if err != nil {
				return fmt.Errorf("schema: %w (is `verse up` running on %s?)", err, g.Addr)
			}
			return printBody(cc, resp)
		})
}
