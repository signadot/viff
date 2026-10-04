// Command verse is the single CLI for the verse prototype: `up` starts the
// daemon(s); every other subcommand is a client that connects to a running verse.
// See docs/reference/README.md for the command tree, and docs/deployment-and-steering.md for
// the plane split it encodes.
package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
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
	Scope string `cli:"name=scope desc='operate in a copy-on-write scope: a federation overlay isolated from baseline'"`
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
// Colour is a property of the DESTINATION. `verse charter list` into a terminal wants it;
// `verse charter list | o v` and `| jq` are broken by it, and so is anything reading the
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
	// A FRESH filter binding per tree, and it has to be here — before the builders below bind
	// their options to it. One command runs per process, so nothing in a shell notices; a
	// process that builds the tree twice (every test) would otherwise have the second command
	// filtering by what the first was asked for, which reads as a listing that lost its
	// contents.
	flt = &filterFlags{}
	// The same for who the tree acts as: a --as recorded by one command must not ride the
	// requests of the next, which only a process building the tree twice can see.
	asOverride = ""
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
		// Each collection is ONE command, named in the singular, and bare it lists:
		// `verse question` shows what waits on you, `verse question approve <id>` answers one.
		// The plural is an ALIAS of it rather than a second command, so both spellings run
		// the same thing and neither has to be learned as the one that works here.
		//
		// It spent a while as a genuine pair, the plural listing and the singular acting,
		// which put two names in the help for one collection and made every synopsis spend
		// its tail telling you about the other one. Bare-lists collapses that: one name, one
		// place its verbs are written down, and the plural still typed by anyone who reaches
		// for it.
		WithSubs(
			upCmd(),
			watchCmd(),
			gateCmd(),
			askCmd(),
			dataCmd(),
			taskCmd(),
			flowCmd(),
			scheduleCmd(),
			charterCmd(),
			resolverCmd(),
			runnerCmd(),
			permsCmd(),
			groupCmd(),
			ioCmd(),
			providerCmd(),
			connectCmd(),
			disconnectCmd(),
			statusCmd(),
			schemaCmd(),
			downCmd(),
		)
}

// collection builds the command for one collection: named in the SINGULAR, aliased to the
// plural the HTTP surface uses (GET /asks, GET /flows), listing when bare and acting on one
// member when given a verb (see dispatch).
func collection(name, plural, route, synopsis string, subs ...*cli.Command) *cli.Command {
	cfg := &struct{ *cli.Command }{}
	return cli.NewCommandAt(&cfg.Command, name).
		WithAliases(plural).
		WithSynopsis(synopsis).
		// Every collection filters. It is on the BUILDERS rather than on each command so that
		// the next listing cannot be added without them — which is how `verse ps` came to be a
		// thing people pipe through `o list` in the first place.
		WithOpts(filterOpts()...).
		WithSubs(subs...).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			if handled, err := dispatch(cfg.Command, cc, args); handled {
				return err
			}
			if err := noArgs(cfg.Command, name, args); err != nil {
				return err
			}
			resp, err := httpGet(route, nil)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			return printBody(cc, resp)
		})
}

// filteredCollection is collection with flags that narrow what it lists. The narrowing goes to
// the SERVER as query parameters rather than being applied to the rows here, because the server
// is where the record lives — a client-side filter would have to know which field a name is
// stamped on, and would be a second place that has to agree about it.
// post runs after a listing has printed, for a collection that has something to SAY about an
// answer rather than to add to it — a note on stderr, so the document on stdout stays a
// document. `verse charter` is the one: see elsewhereInPackages.
type postPrint func(*cli.Context)

func filteredCollectionPost(name, plural, route, synopsis string, opts []*cli.Opt,
	query func() url.Values, post []postPrint, subs ...*cli.Command,
) *cli.Command {
	cfg := &struct{ *cli.Command }{}
	return cli.NewCommandAt(&cfg.Command, name).
		WithAliases(plural).
		WithSynopsis(synopsis).
		// The collection's own flags, plus --if/--trim: every listing filters, and adding them
		// at the two builders is what stops the next one being added without them.
		WithOpts(append(opts, filterOpts()...)...).
		WithSubs(subs...).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			if handled, err := dispatch(cfg.Command, cc, args); handled {
				return err
			}
			if err := noArgs(cfg.Command, name, args); err != nil {
				return err
			}
			resp, err := httpGet(route, query())
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if err := printBody(cc, resp); err != nil {
				return err
			}
			for _, p := range post {
				p(cc)
			}
			return nil
		})
}

// filteredCollection is filteredCollectionPost with nothing to say afterwards.
func filteredCollection(name, plural, route, synopsis string, opts []*cli.Opt,
	query func() url.Values, subs ...*cli.Command,
) *cli.Command {
	return filteredCollectionPost(name, plural, route, synopsis, opts, query, nil, subs...)
}

// dispatch runs the subcommand named by args[0], when there is one. A collection LISTS when
// bare, which means it carries its own Run — and a command with its own Run never reaches
// the library's sub-dispatch, so the verbs have to be found here. It reports whether it
// handled the args at all; when it did not, the listing runs.
func dispatch(cmd *cli.Command, cc *cli.Context, args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	sub := cmd.FindSub(cc, args[0])
	if sub == nil {
		return false, nil
	}
	return true, sub.Run(cc, args[1:])
}

// noArgs refuses a positional the collection cannot dispatch, and names what it could have
// been.
//
// A listing that IGNORED its arguments would turn a typo into a plausible answer:
// `verse c -a-it review` printing your charter reads as success at the wrong thing.
func noArgs(cmd *cli.Command, name string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	// `list` is THE mistake a bare-listing collection invites, and it is worth its own line:
	// the answer is to drop the word, not to look for the command it was moved to.
	if args[0] == "list" {
		return fmt.Errorf("%w: there is no `verse %s list` — bare `verse %s` is the listing",
			cli.ErrUsage, name, name)
	}
	verbs := make([]string, 0, len(cmd.Children))
	for _, c := range cmd.Children {
		verbs = append(verbs, c.Name)
	}
	return fmt.Errorf("%w: verse %s: no such command %q — bare it lists, and its verbs are (%s)",
		cli.ErrUsage, name, args[0], strings.Join(verbs, ", "))
}

// leaf builds a client subcommand that takes no positionals of its own but still
// parses trailing global flags (--addr/--scope/-o) before running fn — so e.g.
// `verse status --addr host:port` is honored, not just `verse --addr … status`.
// The synopsis must start with the command name: the cli help lists a command by the
// first word of its synopsis (see TestEverySynopsisStartsWithItsCommandName).
//
// "No positionals of its own" is now enforced rather than assumed: these commands used to
// discard whatever they were handed, so `verse status demo.pr.1` printed the status of the
// whole verse and looked like it had answered the question asked.
// listing is leaf for a command that ANSWERS A COLLECTION — it carries --if/--trim, where a
// leaf that prints a line (`down`, `disconnect`) does not. A flag that silently does nothing is
// worse than one the parser refuses, which is the whole reason the two are separate builders.
func listing(name, synopsis string, fn func(cc *cli.Context, args []string) error) *cli.Command {
	return leaf(name, synopsis, fn).WithOpts(filterOpts()...)
}

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
