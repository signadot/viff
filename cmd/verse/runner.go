package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/codec"
)

type runnersConfig struct {
	Probe bool `cli:"name=probe desc='also ask each runner what it is, right now (not recorded)'"`
}

var runnersFlags = &runnersConfig{}

func runnerCmd() *cli.Command {
	opts, err := cli.StructOpts(runnersFlags)
	if err != nil {
		panic(err)
	}
	return filteredCollection("runner", "runners", "/runners",
		"runner — where work can run [--probe]; add|show|check|remove one",
		opts, func() url.Values {
			// Bare, the columns are what verse has RECORDED. --probe adds what the runners say
			// now, and nothing merges the two or keeps the second.
			q := url.Values{}
			if runnersFlags.Probe {
				q.Set("probe", "true")
			}
			return q
		}, runnerAddCmd(), runnerShowCmd(), runnerCheckCmd(), runnerRemoveCmd())
}

// runnerPath turns the address an operator types into the route's two segments — see pairPath,
// the one reader the three registries share.
func runnerPath(ref string) (string, error) { return pairPath("runners", "runners", ref) }

type runnerShowConfig struct{ *cli.Command }

func runnerShowCmd() *cli.Command {
	cfg := &runnerShowConfig{}
	return cli.NewCommandAt(&cfg.Command, "show").
		WithSynopsis("show a runner: show <ref>").WithRun(cfg.run)
}

func (cfg *runnerShowConfig) run(cc *cli.Context, args []string) error {
	ref, err := oneArg(cfg.Command, cc, args, "runner show wants a ref")
	if err != nil {
		return err
	}
	path, err := runnerPath(ref)
	if err != nil {
		return err
	}
	resp, err := httpGet(path, nil)
	if err != nil {
		return fmt.Errorf("runner show: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return fmt.Errorf("runner show: no such runner %q", ref)
	}
	return printBody(cc, resp)
}

type runnerCheckConfig struct{ *cli.Command }

func runnerCheckCmd() *cli.Command {
	cfg := &runnerCheckConfig{}
	return cli.NewCommandAt(&cfg.Command, "check").
		WithSynopsis("check <ref> — ask a runner what it is, right now").WithRun(cfg.run)
}

func (cfg *runnerCheckConfig) run(cc *cli.Context, args []string) error {
	ref, err := oneArg(cfg.Command, cc, args, "runner check wants a ref")
	if err != nil {
		return err
	}
	path, err := runnerPath(ref)
	if err != nil {
		return err
	}
	resp, err := httpGet(path+"/check", nil)
	if err != nil {
		return fmt.Errorf("runner check: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return fmt.Errorf("runner check: no such runner %q", ref)
	}
	return printBody(cc, resp)
}

type runnerRemoveConfig struct {
	*cli.Command
	Cancel string `cli:"name=cancel desc='the place is going away: end the work on it with this reason'"`
	Leave  bool   `cli:"name=leave desc='the place stays: leave the work running on the url already stamped on it'"`
}

func runnerRemoveCmd() *cli.Command {
	cfg := &runnerRemoveConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "remove").
		WithSynopsis("remove a runner: remove <ref> [--cancel \"<why>\" | --leave]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *runnerRemoveConfig) run(cc *cli.Context, args []string) error {
	ref, err := oneArg(cfg.Command, cc, args, "runner remove wants a ref")
	if err != nil {
		return err
	}
	path, err := runnerPath(ref)
	if err != nil {
		return err
	}
	// The two intents a delete cannot carry. Sent as given rather than defaulted here: the
	// refusal is the server's to make, so one message says what both flags mean.
	q := url.Values{}
	if cfg.Leave {
		q.Set("leave", "true")
	}
	// A reason is what --cancel is FOR — it is what the charters of the work being ended see
	// on their onCancelled — so an empty one is not a cancel, it is a flag with nothing in it.
	if cfg.Cancel != "" {
		q.Set("cancel", cfg.Cancel)
	}
	resp, err := httpDelete(path, q)
	if err != nil {
		return fmt.Errorf("runner remove: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return fmt.Errorf("runner remove: no such runner %q", ref)
	}
	return printBody(cc, resp)
}

type runnerAddConfig struct {
	*cli.Command
	File     string `cli:"name=file aliases=f desc='read the runner from FILE (- = stdin); also accepts a positional [file]'"`
	NoProbe  bool   `cli:"name=no-probe desc='install without asking the url what it is'"`
	Identity string `cli:"name=identity desc='the id of the principal the runner acts as, runner:<identity> (default: its name): what its reports are written as and what a spawn grants it'"`
}

func runnerAddCmd() *cli.Command {
	cfg := &runnerAddConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "add").
		WithSynopsis("add a runner: add <ref> <url>, or add --file <f> (- = stdin) | [file] | stdin").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *runnerAddConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	q := url.Values{}
	if cfg.NoProbe {
		q.Set("probe", "false")
	}
	// Two ways in, because a runner is three fields and one of them is its address. The
	// document form is what a deployment writes; the positional form is what a person types,
	// and refusing them that would make the common case the awkward one.
	if cfg.File == "" && len(args) == 2 {
		ref, rawURL := args[0], args[1]
		parts, ok := pairSegs("runners", ref)
		if !ok {
			return fmt.Errorf("%w: want `runner add <kind>.<name> <url>`, got %q",
				cli.ErrUsage, ref)
		}
		body := map[string]any{"kind": parts[0], "name": parts[1], "url": rawURL}
		if cfg.Identity != "" {
			body["identity"] = cfg.Identity
		}
		resp, err := httpPostTo("/runners", q, body)
		return runnerAdded(cc, resp, err)
	}
	// More positionals than either form takes. Worth its own message, because the fall-through
	// below reads args[0] as a FILENAME — so `runner add pool:code <url> <anything else>` failed
	// with "open pool:code: no such file or directory", which names a file nobody mentioned and
	// sends the reader looking for one.
	//
	// The way it actually happens is a pasted command with a trailing comment: interactive zsh
	// has INTERACTIVE_COMMENTS off, so `# prints: …` arrives as four more arguments. Naming that
	// here is cheap and it is what the reader needs.
	if cfg.File == "" && len(args) > 2 {
		return fmt.Errorf("%w: `runner add` takes `<kind>:<name> <url>` or one file, and got %d "+
			"arguments: %q.\nIf you pasted a line with a trailing `#` comment, zsh keeps it — it "+
			"is not stripped outside a script", cli.ErrUsage, len(args), strings.Join(args, " "))
	}
	src := cfg.File
	if src == "" && len(args) >= 1 {
		src = args[0]
	}
	data, srcFmt, err := codec.ReadSource(cc.In, src, inFmt())
	if err != nil {
		return fmt.Errorf("runner add: %w", err)
	}
	// Sent as the document it was written as, for the reason a resolver is: the server reads
	// it as a document too, so a field neither side knows is named by the one place that can
	// name it instead of being dropped twice on the way.
	node, err := codec.ParseNode(data, srcFmt)
	if err != nil {
		return fmt.Errorf("runner add: %w", err)
	}
	resp, err := httpPostNodeTo("/runners", q, node)
	return runnerAdded(cc, resp, err)
}

func runnerAdded(cc *cli.Context, resp *http.Response, err error) error {
	if err != nil {
		return fmt.Errorf("runner add: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return fmt.Errorf("runner add: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return printBody(cc, resp)
}
