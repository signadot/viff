// Command verse is the single CLI for the verse prototype: `up` starts the
// daemon(s); every other subcommand is a client that connects to a running verse.
// See docs/reference.md for the command tree, and docs/deployment-and-steering.md for
// the plane split it encodes.
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
//
// The two format flags are a PAIR, and named as one: --ifmt reads, --ofmt writes. They
// spent a while as `--ifmt` and `-o`, which read as unrelated knobs in the same help
// listing; -o survives as an alias because it is the convention and every doc uses it.
type globals struct {
	Addr string `cli:"name=addr desc='verse server address'"`
	// The writable set is NOT the readable one: verse reads json and refuses to render it
	// (codec.Writable), so advertising json here promised something -o then rejected.
	OFmt  string `cli:"name=ofmt aliases=o desc='output format: tony | yaml (verse does not render json)'"`
	IFmt  string `cli:"name=ifmt aliases=i desc='input format for stdin, extensionless files and typed payloads: tony | json | yaml (a file suffix wins)'"`
	Color string `cli:"name=color default=auto desc='colorize output: auto (a terminal only) | always | never'"`
	Scope string `cli:"name=scope desc='operate in a copy-on-write scope (real isolation on the default docd store; passthrough under --store memory)'"`
}

var g = &globals{Addr: defaultAddr}

// outFmt and inFmt resolve the formats for THIS invocation.
//
// They are functions, not a codec.SetDefault at startup, and that is load-bearing: config
// is loaded while the command tree is being built, but the cli library parses flags inside
// Run — so a package global written before the tree exists is stale by the time -o is
// known. Reading the flag at use time is the only way it can win.
//
// A malformed --ofmt is an ERROR, not a quiet fall back to the default. `-o tny` printing
// perfectly good tony is indistinguishable from `-o tony` not being implemented, which is
// precisely the state this flag spent its whole life in. `-o json` is refused for the same
// reason: verse reads JSON but does not write it, and answering in another format under
// the operator's nose is the failure this flag exists to prevent.
func outFmt() (codec.Format, error) {
	if g.OFmt != "" {
		f, err := codec.ParseOutput(g.OFmt)
		if err != nil {
			return f, fmt.Errorf("%w: --ofmt: %v", cli.ErrUsage, err)
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

// wireFmt is the format the client RENDERS a request body in.
//
// It is deliberately not inFmt(). --ifmt says how to READ what the operator supplied, and
// json is perfectly readable; a request body has to be WRITTEN, and verse renders no json
// (codec.Writable). Using inFmt for both meant `--ifmt json` — which the tutorial suggests
// for pasting JSON — failed every command that sends a body, i.e. every write, with a
// complaint about RENDERING json to someone who had only asked to type it.
//
// Falling back to tony loses nothing: the server reads all three, and tony is the one that
// can carry a tagged document through unflattened.
func wireFmt() codec.Format {
	if f := inFmt(); codec.Writable(f) {
		return f
	}
	return codec.Tony
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
	// Show each global's EFFECTIVE default in `verse --help`. These four are resolved from
	// the config layers a moment ago, so a static `default=` tag could not carry them: it
	// would print a fixed value that the file or the environment has already overridden,
	// and — because the tag also assigns — would clobber what was just loaded. WithDefault
	// only annotates for the usage text; it does not touch the value.
	for _, o := range gopts {
		switch o.Name {
		case "addr":
			o.WithDefault(g.Addr)
		case "scope":
			if g.Scope != "" {
				o.WithDefault(g.Scope)
			}
		case "ofmt":
			o.WithDefault(appCfg.Format.ResolveOutput().String())
		case "ifmt":
			o.WithDefault(appCfg.Format.ResolveDefault().String())
		}
	}
	return cli.NewCommand("verse").
		WithSynopsis("one CLI for the verse prototype").
		WithDescription("`up` starts the daemon(s); every other subcommand is a client.").
		WithOpts(gopts...).
		// Each collection appears TWICE and means two different things: the plural lists it
		// (`verse gates`), the singular acts on one member of it (`verse gate approve <id>`).
		// They are listed as pairs so the rule is visible in the help rather than something
		// you have to be told. `docket` and `providers` were already bare plurals; the rest
		// spent their lives as `<group> list`, which is the same idea in more words.
		WithSubs(
			upCmd(),
			watchCmd(),
			gatesCmd(), gateCmd(),
			entitiesCmd(), entityCmd(),
			processesCmd(), processCmd(),
			flowsCmd(), flowCmd(),
			triggersCmd(), triggerCmd(),
			goalsCmd(), goalCmd(),
			voteCmd(),
			docketCmd(),
			providersCmd(),
			connectCmd(),
			disconnectCmd(),
			statusCmd(),
			downCmd(),
		)
}

// collection builds a bare plural command that lists one collection — `verse gates`,
// `verse flows` — which is also how the HTTP surface addresses it (GET /gates, GET /flows).
func collection(name, route, synopsis, singular string) *cli.Command {
	cfg := &struct{ *cli.Command }{}
	return cli.NewCommandAt(&cfg.Command, name).
		WithSynopsis(synopsis).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			if err := noArgs(name, singular, args); err != nil {
				return err
			}
			resp, err := httpGet(route, nil)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			return printBody(cc, resp)
		})
}

// noArgs refuses positionals on a command that takes none, and names the singular that does.
//
// It is load-bearing for exactly the commands that moved: `gate list` → `gates` retires a
// lot of muscle memory, and a listing that IGNORED its arguments would turn `verse goals add
// ship-it` into a silent listing of your goals — the command doing something plausible
// instead of saying it was the wrong command.
func noArgs(name, singular string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	// `verse goals list` is THE mistake this rename creates, and forwarding it to
	// `verse goal list` would name a second command that does not exist either.
	if args[0] == "list" {
		return fmt.Errorf("%w: `verse %s list` was replaced by `verse %s` — the plural is the listing",
			cli.ErrUsage, singular, name)
	}
	return fmt.Errorf("%w: %s takes no arguments — did you mean `verse %s %s`?",
		cli.ErrUsage, name, singular, strings.Join(args, " "))
}

// leaf builds a client subcommand that takes no positionals of its own but still
// parses trailing global flags (--addr/--scope/-o) before running fn — so e.g.
// `verse status --addr host:port` is honored, not just `verse --addr … status`.
// The synopsis must start with the command name: the cli help lists a command by the
// first word of its synopsis (see TestEverySynopsisStartsWithItsCommandName).
//
// "No positionals of its own" is now enforced rather than assumed: these commands used to
// discard whatever they were handed, so `verse status demo:pr:1` printed the status of the
// whole verse and looked like it had answered the question asked.
func leaf(name, synopsis string, fn func(cc *cli.Context, args []string) error) *cli.Command {
	cfg := &struct{ *cli.Command }{}
	return cli.NewCommandAt(&cfg.Command, name).
		WithSynopsis(synopsis).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			if len(args) > 0 {
				return fmt.Errorf("%w: %s takes no arguments (got %q)",
					cli.ErrUsage, name, strings.Join(args, " "))
			}
			return fn(cc, args)
		})
}
