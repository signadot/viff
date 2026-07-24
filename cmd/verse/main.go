// Command verse is the single CLI for the verse prototype: `up` starts the
// daemon(s); every other subcommand is a client that connects to a running verse.
// See docs/deployment-and-steering.md for the full command tree and the plane split.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/config"
)

const defaultAddr = "localhost:9797"

// globals are the flags common to every command — bound once on the root and, via
// the library's path-aware option resolution, available to every subcommand
// (parseable before or after positionals).
type globals struct {
	Addr  string `cli:"name=addr desc='verse server address'"`
	O     string `cli:"name=o desc='output format (tony-format formats; TODO — currently JSON)'"`
	Scope string `cli:"name=scope desc='operate in a copy-on-write scope (real isolation on --store docd; passthrough in-memory)'"`
}

var g = &globals{Addr: defaultAddr, O: "text"}

// appCfg is the layered file+env configuration (see package config), loaded in
// rootCmd. CLI flags override it via the flag defaults set below. (Named appCfg, not
// cfg, so a command's own *upConfig receiver named cfg doesn't shadow it.)
var appCfg = &config.Config{}

func main() {
	cli.MainContext(context.Background(), rootCmd())
}

func rootCmd() *cli.Command {
	// Config precedence: /etc ← ~/.config ← env ← CLI. Load the file+env layers, use
	// them as the flag defaults, and let an explicit flag win.
	c, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "verse: %v (using environment + defaults)\n", err)
	}
	appCfg = c // Load always returns a usable config (env applied), even on a file error
	if appCfg.Addr != "" {
		g.Addr = appCfg.Addr
	}
	if appCfg.Scope != "" {
		g.Scope = appCfg.Scope
	}
	gopts, err := cli.StructOpts(g)
	if err != nil {
		panic(err)
	}
	return cli.NewCommand("verse").
		WithSynopsis("one CLI for the verse prototype").
		WithDescription("`up` starts the daemon(s); every other subcommand is a client.").
		WithOpts(gopts...).
		WithSubs(
			upCmd(),
			watchCmd(),
			gateCmd(),
			entityCmd(),
			processCmd(),
			flowCmd(),
			triggerCmd(),
			goalsCmd(),
			voteCmd(),
			decisionsCmd(),
			providersCmd(),
			connectCmd(),
			disconnectCmd(),
			statusCmd(),
			downCmd(),
		)
}

// leaf builds a client subcommand that takes no positionals of its own but still
// parses trailing global flags (--addr/--scope/-o) before running fn — so e.g.
// `verse status --addr host:port` is honored, not just `verse --addr … status`.
// The synopsis must start with the command name: the cli help lists a command by the
// first word of its synopsis.
func leaf(name, synopsis string, fn func(cc *cli.Context, args []string) error) *cli.Command {
	cfg := &struct{ *cli.Command }{}
	return cli.NewCommandAt(&cfg.Command, name).
		WithSynopsis(synopsis).
		WithRun(func(cc *cli.Context, args []string) error {
			if _, err := cfg.Parse(cc, args); err != nil {
				return err
			}
			return fn(cc, args)
		})
}
