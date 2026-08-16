package main

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
)

// triggerFlags narrow WHICH charter is listed. `verse trigger` answers one engine's rules, and
// with packages installed there are several — so the two flags are how a reader says which, and
// `--tree` is how they see that there are several at all.
type triggerFlags struct {
	At    string `cli:"name=at desc='the rules of the charter package at this address, in full'"`
	Rules bool   `cli:"name=rules desc='the rules in full rather than the tree — baseline, or --at'"`
}

var trig = &triggerFlags{}

func triggerCmd() *cli.Command {
	cfg := &struct{ *cli.Command }{}
	trig = &triggerFlags{} // per-invocation, like the filter flags: one command runs per process
	return cli.NewCommandAt(&cfg.Command, "trigger").
		WithAliases("triggers").
		WithSynopsis("trigger — the installed charter as a tree; add|show|remove one rule").
		WithOpts(append(mustOpts(trig), filterOpts()...)...).
		WithSubs(triggerAddCmd(), triggerShowCmd(), triggerRemoveCmd()).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			if handled, err := dispatch(cfg.Command, cc, args); handled {
				return err
			}
			if err := noArgs(cfg.Command, "trigger", args); err != nil {
				return err
			}
			// ONE CHARTER'S RULES when the question named one, the whole tree otherwise.
			// `--at drift`, `--scope me` and `--rules` are specific questions and get the
			// rules themselves — which is also where `--if`/`--trim` mean what they always
			// did, since a rule is the item there. Bare, the answer is what is installed and
			// where, and there is no rule document in it to filter.
			if trig.At != "" || trig.Rules || g.Scope != "" {
				q := url.Values{}
				if trig.At != "" {
					q.Set("at", trig.At)
				}
				resp, err := httpGet("/triggers", q)
				if err != nil {
					return fmt.Errorf("trigger: %w", err)
				}
				return printBody(cc, resp)
			}
			return printCharterTree(cc)
		})
}

// printCharterTree renders the whole charter as a TREE, which is what an address makes it: the
// baseline charter at the root, and each package under the segments of the address it writes in.
//
//	writes: anywhere              # baseline: no address, so it may write anywhere
//	rules: [land-auto, land-ask]
//	children:
//	  drift:
//	    writes: drift.*
//	    imports: [git]
//	    rules: [comment-drift, gather, head-moved]
//	  demo:
//	    children:                 # nothing is installed at `demo` itself
//	      pr:
//	        writes: demo.pr.*
//	        rules: [review]
//
// BY NAME rather than by body, because this is the answer to "what is installed": a name is what
// every other command takes, and comment-drift's rules are pages long. `verse trigger --at
// drift` is the rules themselves, one charter at a time, and `trigger show <name>` is one rule.
//
// It answers the tree even when there are no packages at all, where it is baseline's rules under
// `rules:` and no `children:` — one shape, so a reader who learns it once does not meet a
// different one the first time somebody installs a package.
//
// Assembled here rather than served, for the reason the docket is: the wire carries the flat
// rows (one per charter, each naming its address) and the shape is a rendering of them. A tree
// on the wire would be a second, recursive type for the same facts.
func printCharterTree(cc *cli.Context) error {
	resp, err := httpGet("/packages", nil)
	if err != nil {
		return fmt.Errorf("trigger: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return printBody(cc, resp) // a 503 from an unreadable store says so in its own words
	}
	var rows []api.CharterPackage
	decodeClose(resp, &rows)
	root := &charterNode{}
	for _, row := range rows {
		node := root
		if row.At != api.BaselinePackage {
			for _, seg := range strings.Split(row.At, ".") {
				node = node.child(seg)
			}
		}
		node.row, node.here = row, true
	}
	return printIR(cc, root.node())
}

// charterNode is one place in the address tree: a charter installed there (or not, for a
// segment that only leads somewhere) and whatever is installed below it.
type charterNode struct {
	row      api.CharterPackage
	here     bool // a charter is installed AT this address, rather than only under it
	kids     map[string]*charterNode
	kidOrder []string
}

func (n *charterNode) child(seg string) *charterNode {
	if n.kids == nil {
		n.kids = map[string]*charterNode{}
	}
	if c, ok := n.kids[seg]; ok {
		return c
	}
	c := &charterNode{}
	n.kids[seg] = c
	n.kidOrder = append(n.kidOrder, seg)
	return c
}

// node renders the subtree in a DELIBERATE field order — what it may write, what it reads, what
// it holds, then what is under it. Alphabetical (which is what a map gives) would put `children`
// first and `writes` last, so the shallowest fact about a charter would be the one furthest down
// every entry.
func (n *charterNode) node() *ir.Node {
	fields := []*ir.Node{}
	values := []*ir.Node{}
	add := func(key string, v *ir.Node) {
		fields = append(fields, ir.FromString(key))
		values = append(values, v)
	}
	if n.here {
		add("writes", ir.FromString(n.row.Writes))
		if n.row.Imports != nil {
			add("imports", strs(n.row.Imports))
		}
		add("rules", strs(n.row.Rules))
		if len(n.row.Strata) > 0 {
			add("strata", strs(n.row.Strata))
		}
	}
	if len(n.kidOrder) > 0 {
		sort.Strings(n.kidOrder)
		kf, kv := []*ir.Node{}, []*ir.Node{}
		for _, seg := range n.kidOrder {
			kf = append(kf, ir.FromString(seg))
			kv = append(kv, n.kids[seg].node())
		}
		add("children", &ir.Node{Type: ir.ObjectType, Fields: kf, Values: kv})
	}
	return &ir.Node{Type: ir.ObjectType, Fields: fields, Values: values}
}

func strs(in []string) *ir.Node {
	out := make([]*ir.Node, 0, len(in))
	for _, s := range in {
		out = append(out, ir.FromString(s))
	}
	return ir.FromSlice(out)
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
		fmt.Fprintln(cc.Out, "`verse trigger` shows every charter and where each may write")
	}
	return nil
}
