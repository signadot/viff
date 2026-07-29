package main

import (
	"fmt"
	"strings"

	"github.com/scott-cotton/cli"
)

// schemaCmd prints the schema verse publishes about itself.
//
// Bare, it prints the WHOLE thing: one tony schema whose `define` opens with the root — the
// top-level paths and the shape served at each — and then every definition those shapes name.
// It printed only the root for a while, which answered "what is a verse" and left everything it
// named a lookup away, so the command that exists to show you the schema showed you an index.
//
// With a name, it prints that one definition — `verse schema verse-entity` — for when you know
// what you are looking for and do not want the other thirty.
//
// This asks a running verse, which is the convenient way to ask and not the only one: the
// schema is assembled in package schema from the generated and curated parts, so it can be had
// with no server at all. That is deliberate — see the package doc there.
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
				return fmt.Errorf("%w: schema takes one definition name at most (got %q) — bare it prints all of them",
					cli.ErrUsage, strings.Join(args, " "))
			}
			resp, err := httpGet(path, nil)
			if err != nil {
				return fmt.Errorf("schema: %w (is `verse up` running on %s?)", err, g.Addr)
			}
			return printBody(cc, resp)
		})
}
