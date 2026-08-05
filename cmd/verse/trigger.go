package main

import (
	"fmt"
	"net/http"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
)

func triggerSubs() []*cli.Command {
	return []*cli.Command{triggerAddCmd(), triggerShowCmd(), triggerRemoveCmd()}
}

func triggersCmd() *cli.Command {
	return collection("triggers", "/triggers",
		"triggers — list the installed charter (add or remove one with `verse trigger`)", "trigger",
		triggerSubs()...)
}

func triggerCmd() *cli.Command {
	return cli.NewCommand("trigger").
		WithSynopsis("trigger — one charter rule: add|show|remove (list them with `verse triggers`)").
		WithSubs(triggerSubs()...)
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
	// --file everywhere a command reads a document (goals add, task vector), -f kept as
	// the alias: this was `-f` here, `-f` on goals and `--file` on vector, so the same idea
	// had three spellings and knowing one told you nothing about the next.
	File string `cli:"name=file aliases=f desc='read the charter from FILE (- = stdin); also accepts a positional [file]'"`
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

	// STRATA FIRST, whatever order the file put them in. A rule naming a stratum that is not
	// installed yet is HELD rather than refused — which is right, because two independent
	// installs must not depend on their order — but a file is not two independent installs. It
	// is one document whose author wrote both, and silently leaving half of it dormant because
	// of how it was laid out would be the tool making the author responsible for something it
	// knows perfectly well.
	var strata, rules []*ir.Node
	for _, def := range defs {
		switch defKind(def) {
		case defStratum:
			strata = append(strata, def)
		case defTrigger:
			rules = append(rules, def)
		default:
			return fmt.Errorf("trigger add: a definition is %s: a trigger has a `condition`, "+
				"a stratum has a `level` and a `match`", defUnknown)
		}
	}
	if err := postDefs(cc, "/strata", strata); err != nil {
		return fmt.Errorf("trigger add: installing strata: %w", err)
	}
	if err := postDefs(cc, "/triggers", rules); err != nil {
		return fmt.Errorf("trigger add: %w", err)
	}
	switch {
	case len(strata) == 0:
		fmt.Fprintf(cc.Out, "installed %d trigger(s)\n", len(rules))
	default:
		fmt.Fprintf(cc.Out, "installed %d stratum(a) and %d trigger(s)\n", len(strata), len(rules))
	}
	return nil
}
