package main

import (
	"fmt"

	"github.com/scott-cotton/cli"
)

// statusCmd prints a one-shot summary of the running verse.
func statusCmd() *cli.Command {
	return listing("status", "status — summarize the running verse: store rev, rules, flows, gates, tasks",
		func(cc *cli.Context, args []string) error {
			resp, err := httpGet("/status", nil)
			if err != nil {
				return fmt.Errorf("status: %w (is `verse up` running on %s?)", err, g.Addr)
			}
			return printBody(cc, resp)
		}).WithAliases("st")
}
