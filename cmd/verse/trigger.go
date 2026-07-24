package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/scott-cotton/cli"
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
		WithSynopsis("add trigger(s) from JSON: add -f <file> (- = stdin) | [file] | stdin; one Def or an array").
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
	var data []byte
	if src == "" || src == "-" {
		if data, err = io.ReadAll(cc.In); err != nil {
			return fmt.Errorf("trigger add: reading stdin: %w", err)
		}
	} else if data, err = os.ReadFile(src); err != nil {
		return fmt.Errorf("trigger add: %w", err)
	}

	// Accept a single Def object or an array of them (a charter file).
	var raws []json.RawMessage
	if strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
		if err := json.Unmarshal(data, &raws); err != nil {
			return fmt.Errorf("trigger add: %w", err)
		}
	} else {
		raws = []json.RawMessage{json.RawMessage(data)}
	}

	for _, raw := range raws {
		resp, err := httpPost("/triggers", raw)
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
	fmt.Fprintf(cc.Out, "installed %d trigger(s)\n", len(raws))
	return nil
}
