// Command verse is the single CLI for the verse prototype: `up` starts the
// daemon(s); every other subcommand is a client that connects to a running verse.
// See docs/deployment-and-steering.md for the full command tree and the plane split.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/config"
)

const defaultAddr = "localhost:9797"

// globals are the flags common to every command — bound once on the root and, via
// the library's path-aware option resolution, available to every subcommand
// (parseable before or after positionals).
type globals struct {
	Addr  string `cli:"name=addr desc='verse server address'"`
	O     string `cli:"name=o desc='output format: tony | json | yaml'"`
	IFmt  string `cli:"name=ifmt desc='input format for stdin and extensionless files (a file suffix wins)'"`
	Color string `cli:"name=color desc='colorize output: auto (a terminal only) | always | never'"`
	Scope string `cli:"name=scope desc='operate in a copy-on-write scope (real isolation on --store docd; passthrough in-memory)'"`
}

var g = &globals{Addr: defaultAddr}

// outFmt and inFmt resolve the formats for THIS invocation.
//
// They are functions, not a codec.SetDefault at startup, and that is load-bearing: config
// is loaded while the command tree is being built, but the cli library parses flags inside
// Run — so a package global written before the tree exists is stale by the time -o is
// known. Reading the flag at use time is the only way it can win.
//
// A malformed -o is an ERROR, not a quiet fall back to the default. `-o tny` printing
// perfectly good JSON is indistinguishable from `-o tony` not being implemented, which is
// precisely the state this flag spent its whole life in.
func outFmt() (codec.Format, error) {
	if g.O != "" {
		f, err := codec.Parse(g.O)
		if err != nil {
			return f, fmt.Errorf("%w: -o: %v", cli.ErrUsage, err)
		}
		return f, nil
	}
	return appCfg.Format.ResolveOutput(), nil
}

// colorize reports whether output should carry ANSI colour.
//
// Colour is a property of the DESTINATION. `verse trigger list` into a terminal wants it;
// `verse trigger list | o v` and `| jq` are broken by it, and so is anything reading the
// output as a document. So the default is auto — a character device on stdout and nothing
// saying otherwise.
//
// NO_COLOR is honored because it is the convention users already have
// (https://no-color.org); --color overrides both directions for the cases auto cannot see,
// like a pager that does want escapes.
//
// The palette go-tony uses comes from fatih/color, which keeps its OWN global
// terminal-detection switch. Auto therefore needs nothing from us — that library already
// suppresses escapes on a pipe. Only `--color always` has to reach in and turn it off,
// because overriding auto-detection is the entire point of asking for it.
func colorize(w io.Writer) bool {
	switch strings.ToLower(g.Color) {
	case "always", "yes", "force":
		color.NoColor = false
		return true
	case "never", "no", "none":
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// inFmt is the format for input that does not name its own — stdin, an extensionless file,
// a payload typed on the command line. A file suffix always wins over it; see codec.ForPath.
func inFmt() codec.Format {
	if f, err := codec.Parse(g.IFmt); err == nil && g.IFmt != "" {
		return f
	}
	return appCfg.Format.ResolveDefault()
}

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
			docketCmd(),
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
