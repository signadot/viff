package main

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
)

// triggerFlags narrow WHICH charter is listed. `verse trigger` answers one engine's rules, and
// with packages installed there are several — so the two flags are how a reader says which, and
// `--tree` is how they see that there are several at all.
type triggerFlags struct {
	At string `cli:"name=at desc='the charter package at this address, rather than baseline'"`
}

var trig = &triggerFlags{}

func triggerCmd() *cli.Command {
	trig = &triggerFlags{} // per-invocation, like the filter flags: one command runs per process
	return filteredCollectionPost("trigger", "triggers", "/triggers",
		"trigger — the installed charter; add|show|remove one rule, tree for all of them",
		mustOpts(trig),
		func() url.Values {
			q := url.Values{}
			if trig.At != "" {
				q.Set("at", trig.At)
			}
			return q
		},
		[]postPrint{elsewhereInPackages},
		triggerAddCmd(), triggerShowCmd(), triggerRemoveCmd(), triggerTreeCmd())
}

// AN EMPTY BASELINE IS NOT AN EMPTY CHARTER once packages exist, and this says so.
//
// `verse trigger` lists ONE engine's rules, and a charter installed at an address is another
// engine's — so installing comment-drift (two packages, eight rules) and then typing `verse
// trigger` answers `[]`, which reads as "nothing installed" and is the second thing anybody
// does. It cost two rounds of "the install said 3 and 5 and the listing says none".
//
// The note goes to STDERR so the listing stays a document: `verse trigger | o list …` keeps
// working, and a person at a terminal reads the line. It says nothing when the answer is not
// empty (there is a charter to read) or when `--at`/`--scope` asked about a particular engine
// (the question was specific, and an empty answer to it is an answer).
func elsewhereInPackages(cc *cli.Context) {
	if trig.At != "" || g.Scope != "" || !lastPrintEmpty {
		return
	}
	resp, err := httpGet("/packages", nil)
	if err != nil {
		return
	}
	var rows []api.CharterPackage
	decodeClose(resp, &rows)
	rules, pkgs := 0, 0
	for _, row := range rows {
		if row.At == api.BaselinePackage {
			continue
		}
		rules += row.Rules
		pkgs++
	}
	if rules == 0 {
		return
	}
	fmt.Fprintf(cc.Err, "(baseline holds no rules; %d in %d charter package(s) — "+
		"`verse trigger tree`, or `verse trigger --at <address>`)\n", rules, pkgs)
}

type triggerTreeConfig struct{ *cli.Command }

// The charter as a TREE, which is the view a package makes necessary: `verse trigger` answers
// ONE engine's rules, so with packages installed there was no way to see that there were
// several, what each may write, or the import graph between them.
//
// A listing like any other — an array, filtered by `--if`/`--trim` — rather than drawn glyphs:
// the shape is three fields wide, and a document is what a person can grep, diff and pipe.
func triggerTreeCmd() *cli.Command {
	cfg := &triggerTreeConfig{}
	return cli.NewCommandAt(&cfg.Command, "tree").
		WithSynopsis("tree — every charter: baseline and each package, what it writes and imports").
		WithOpts(filterOpts()...).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args) // its own --if/--trim, like every other listing
			if err != nil {
				return err
			}
			if err := noArgs(cfg.Command, "trigger tree", args); err != nil {
				return err
			}
			resp, err := httpGet("/packages", nil)
			if err != nil {
				return fmt.Errorf("trigger tree: %w", err)
			}
			return printBody(cc, resp)
		})
}

type triggerShowConfig struct{ *cli.Command }

func triggerShowCmd() *cli.Command {
	cfg := &triggerShowConfig{}
	return cli.NewCommandAt(&cfg.Command, "show").
		WithSynopsis("show a trigger: show <name>").WithRun(cfg.run)
}

func (cfg *triggerShowConfig) run(cc *cli.Context, args []string) error {
	name, err := oneArg(cfg.Command, cc, args, "trigger show wants a name")
	if err != nil {
		return err
	}
	resp, err := httpGet("/triggers/"+name, nil)
	if err != nil {
		return fmt.Errorf("trigger show: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return fmt.Errorf("trigger show: no such trigger %q", name)
	}
	return printBody(cc, resp)
}

type triggerRemoveConfig struct{ *cli.Command }

func triggerRemoveCmd() *cli.Command {
	cfg := &triggerRemoveConfig{}
	return cli.NewCommandAt(&cfg.Command, "remove").
		WithSynopsis("remove a trigger: remove <name>").WithRun(cfg.run)
}

func (cfg *triggerRemoveConfig) run(cc *cli.Context, args []string) error {
	name, err := oneArg(cfg.Command, cc, args, "trigger remove wants a name")
	if err != nil {
		return err
	}
	resp, err := httpDelete("/triggers/"+name, nil)
	if err != nil {
		return fmt.Errorf("trigger remove: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("trigger remove: no such trigger %q", name)
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("trigger remove: %s", resp.Status)
	}
	fmt.Fprintln(cc.Out, "removed", name)
	return nil
}

type triggerAddConfig struct {
	*cli.Command
	// --file everywhere a command reads a document (stratum add, task vector), -f kept as
	// the alias: this was `-f` here and `--file` on vector, so the same idea had more than
	// one spelling and knowing one told you nothing about the next.
	File string `cli:"name=file aliases=f desc='read the charter from FILE (- = stdin); also accepts a positional [file]'"`
	// A charter installed at an address is a PACKAGE: it runs in an engine of its own and may
	// write only inside that address. Empty installs into the baseline charter, which may
	// write anywhere — see docs/trigger-model.md.
	At string `cli:"name=at desc='install as a charter package at this address (demo, demo.pr) — its rules may write only inside it'"`
}

func triggerAddCmd() *cli.Command {
	cfg := &triggerAddConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "add").
		WithSynopsis("add trigger(s): add --file <f> (- = stdin) | [file] | stdin; one trigger, a list, or `---`-separated documents. Format follows the file suffix (.tony/.json/.yaml)").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *triggerAddConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	// Source: -f FILE, else a positional [file], else stdin. "-" (as -f or positional)
	// is stdin.
	src := cfg.File
	if src == "" && len(args) >= 1 {
		src = args[0]
	}
	// The file's suffix names its dialect; stdin and an extensionless file use --ifmt or
	// the configured default.
	data, srcFmt, err := codec.ReadSource(cc.In, src, inFmt())
	if err != nil {
		return fmt.Errorf("trigger add: %w", err)
	}

	// A charter file may hold one trigger, a list of them, or several documents separated
	// by `---` — and, since rules declare the stratum they live in, the strata themselves.
	defs, err := charterDefs(data, srcFmt)
	if err != nil {
		return fmt.Errorf("trigger add: %w", err)
	}
	if len(defs) == 0 {
		return fmt.Errorf("trigger add: no trigger found in the input")
	}

	// ONE FILE MAY HOLD SEVERAL PACKAGES, and a `---` document carrying `at:` is where each
	// begins. That is what keeps a charter that spans two addresses ONE program in one file:
	//
	//	at: drift
	//	imports: [git]
	//	---
	//	- name: head-moved …
	//	---
	//	at: merge
	//	imports: [git, drift]
	//	---
	//	- name: proposal-appeared …
	//
	// Everything after a declaration belongs to it, until the next one. A file with no
	// declaration is one charter, installed where `--at` says (or baseline, which is most of
	// them), and that is the case this started as.
	groups, err := charterGroups(defs, cfg.At)
	if err != nil {
		return fmt.Errorf("trigger add: %w", err)
	}
	packaged := false
	for _, g := range groups {
		q := url.Values{}
		if g.at != "" {
			q.Set("at", g.at)
		}
		// THE DECLARATION FIRST, before either: a rule is checked against what its package
		// imports as it installs, so sending it after would refuse the rules it is there to
		// allow. Then the strata, for the reason `up` restores them first — a rule naming a
		// stratum that is not installed yet is HELD rather than refused, and a package's levels
		// are its own.
		if g.imports != nil {
			if g.at == "" {
				return fmt.Errorf("trigger add: `imports:` belongs to a charter package — give " +
					"it an address (`at:` in the declaration, or `--at`), or drop it: the " +
					"baseline charter may write anywhere, so there is no graph an import would " +
					"be an edge in")
			}
			if err := postDefsTo(cc, "/packages", q, []*ir.Node{g.imports}); err != nil {
				return fmt.Errorf("trigger add: declaring what %s imports: %w", g.at, err)
			}
		}
		if err := postDefsTo(cc, "/strata", q, g.strata); err != nil {
			return fmt.Errorf("trigger add: installing strata: %w", err)
		}
		if err := postDefsTo(cc, "/triggers", q, g.rules); err != nil {
			return fmt.Errorf("trigger add: %w", err)
		}
		where := "baseline"
		if g.at != "" {
			where = "package " + g.at
		}
		switch {
		case len(g.strata) == 0:
			fmt.Fprintf(cc.Out, "%s: installed %d trigger(s)\n", where, len(g.rules))
		default:
			fmt.Fprintf(cc.Out, "%s: installed %d stratum(a) and %d trigger(s)\n",
				where, len(g.strata), len(g.rules))
		}
		packaged = packaged || g.at != ""
	}
	// WHERE TO LOOK, said at the moment it is not obvious: a charter installed into packages
	// leaves `verse trigger` — the next thing anybody types — answering baseline's rules, which
	// is an empty list. The listing says so too (elsewhereInPackages); saying it here as well is
	// what keeps somebody from having to be surprised first.
	if packaged {
		fmt.Fprintln(cc.Out, "`verse trigger tree` lists every charter; `verse trigger` alone is baseline's")
	}
	return nil
}
