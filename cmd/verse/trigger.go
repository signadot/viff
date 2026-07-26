package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
)

func triggerCmd() *cli.Command {
	return cli.NewCommand("trigger").
		WithSynopsis("trigger — the charter (engine rules)").
		WithSubs(triggerListCmd(), triggerAddCmd(), triggerShowCmd(), triggerRemoveCmd())
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

func triggerListCmd() *cli.Command {
	return leaf("list", "list installed triggers",
		func(cc *cli.Context, args []string) error {
			resp, err := httpGet("/triggers", nil)
			if err != nil {
				return fmt.Errorf("trigger list: %w", err)
			}
			return printBody(cc, resp)
		})
}

type triggerAddConfig struct {
	*cli.Command
	File string `cli:"name=f desc='read charter from FILE (- for stdin); also accepts a positional [file]'"`
}

func triggerAddCmd() *cli.Command {
	cfg := &triggerAddConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "add").
		WithSynopsis("add trigger(s): add -f <file> (- = stdin) | [file] | stdin; one trigger, a list, or `---`-separated documents. Format follows the file suffix (.tony/.json/.yaml)").
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
	// by `---`. Shape comes from the parsed document, not from a leading character: tony
	// writes a list either as `[a, b]` or as a block `- a`, so the old "starts with [" test
	// would read a perfectly good block list as a single trigger.
	docs, err := codec.ParseNodes(data, srcFmt)
	if err != nil {
		return fmt.Errorf("trigger add: %w", err)
	}
	var defs []*ir.Node
	for _, doc := range docs {
		doc = codec.Unwrap(doc)
		if doc == nil || doc.Type == ir.NullType {
			continue // a trailing `---`, or an empty document
		}
		if doc.Type == ir.ArrayType {
			for _, elem := range doc.Values {
				defs = append(defs, elem)
			}
			continue
		}
		defs = append(defs, doc)
	}
	if len(defs) == 0 {
		return fmt.Errorf("trigger add: no trigger found in the input")
	}

	for _, def := range defs {
		// The server still speaks JSON; Stage 5 negotiates. Rendering each definition
		// back out here is what lets a tony charter be installed against it today.
		body, err := codec.NodeBytes(def, codec.JSON)
		if err != nil {
			return fmt.Errorf("trigger add: %w", err)
		}
		resp, err := httpPost("/triggers", json.RawMessage(body))
		if err != nil {
			return fmt.Errorf("trigger add: %w", err)
		}
		if resp.StatusCode != http.StatusCreated {
			msg, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("trigger add: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
		}
		resp.Body.Close()
	}
	fmt.Fprintf(cc.Out, "installed %d trigger(s)\n", len(defs))
	return nil
}
