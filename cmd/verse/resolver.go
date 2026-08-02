package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/codec"
)

func resolverSubs() []*cli.Command {
	return []*cli.Command{resolverAddCmd(), resolverShowCmd(), resolverRemoveCmd()}
}

func resolversCmd() *cli.Command {
	return collection("resolvers", "/resolvers",
		"resolvers — list who answers questions (add or remove one with `verse resolver`)",
		"resolver", resolverSubs()...)
}

func resolverCmd() *cli.Command {
	return cli.NewCommand("resolver").
		WithSynopsis("resolver — one binding: add|show|remove (list them with `verse resolvers`)").
		WithSubs(resolverSubs()...)
}

// resolverPath turns the ref an operator types into the route's two segments. A resolver is
// addressed the way it is stored — `resolvers:security:security-review` — because an operator
// reading `verse entities` and one typing `verse resolver show` should not have to know two
// spellings of one thing.
func resolverPath(ref string) (string, error) {
	parts := strings.SplitN(strings.TrimPrefix(ref, "resolvers:"), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("%w: want a ref like resolvers:security:security-review "+
			"(or security:security-review), got %q", cli.ErrUsage, ref)
	}
	return "/resolvers/" + parts[0] + "/" + parts[1], nil
}

type resolverShowConfig struct{ *cli.Command }

func resolverShowCmd() *cli.Command {
	cfg := &resolverShowConfig{}
	return cli.NewCommandAt(&cfg.Command, "show").
		WithSynopsis("show a resolver: show <ref>").WithRun(cfg.run)
}

func (cfg *resolverShowConfig) run(cc *cli.Context, args []string) error {
	ref, err := oneArg(cfg.Command, cc, args, "resolver show wants a ref")
	if err != nil {
		return err
	}
	path, err := resolverPath(ref)
	if err != nil {
		return err
	}
	resp, err := httpGet(path, nil)
	if err != nil {
		return fmt.Errorf("resolver show: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return fmt.Errorf("resolver show: no such resolver %q", ref)
	}
	return printBody(cc, resp)
}

type resolverRemoveConfig struct{ *cli.Command }

func resolverRemoveCmd() *cli.Command {
	cfg := &resolverRemoveConfig{}
	return cli.NewCommandAt(&cfg.Command, "remove").
		WithSynopsis("remove a resolver: remove <ref>").WithRun(cfg.run)
}

func (cfg *resolverRemoveConfig) run(cc *cli.Context, args []string) error {
	ref, err := oneArg(cfg.Command, cc, args, "resolver remove wants a ref")
	if err != nil {
		return err
	}
	path, err := resolverPath(ref)
	if err != nil {
		return err
	}
	resp, err := httpDelete(path, nil)
	if err != nil {
		return fmt.Errorf("resolver remove: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("resolver remove: no such resolver %q", ref)
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("resolver remove: %s", resp.Status)
	}
	fmt.Fprintln(cc.Out, "removed", ref)
	return nil
}

type resolverAddConfig struct {
	*cli.Command
	File string `cli:"name=file aliases=f desc='read the resolver from FILE (- = stdin); also accepts a positional [file]'"`
}

func resolverAddCmd() *cli.Command {
	cfg := &resolverAddConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "add").
		WithSynopsis("add a resolver: add --file <f> (- = stdin) | [file] | stdin. " +
			"Format follows the file suffix (.tony/.json/.yaml)").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *resolverAddConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	src := cfg.File
	if src == "" && len(args) >= 1 {
		src = args[0]
	}
	data, srcFmt, err := codec.ReadSource(cc.In, src, inFmt())
	if err != nil {
		return fmt.Errorf("resolver add: %w", err)
	}
	// Sent as the document it was written as, rather than decoded here and re-encoded: the
	// server reads it as a document too, so a field neither side knows is named by the one
	// place that can name it, instead of being dropped twice on the way.
	node, err := codec.ParseNode(data, srcFmt)
	if err != nil {
		return fmt.Errorf("resolver add: %w", err)
	}
	resp, err := httpPostNode("/resolvers", node)
	if err != nil {
		return fmt.Errorf("resolver add: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return fmt.Errorf("resolver add: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return printBody(cc, resp)
}
