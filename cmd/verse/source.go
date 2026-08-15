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

type sourcesConfig struct {
	Probe bool `cli:"name=probe desc='also ask each source what it is, right now (not recorded)'"`
}

var sourcesFlags = &sourcesConfig{}

func sourceCmd() *cli.Command {
	opts, err := cli.StructOpts(sourcesFlags)
	if err != nil {
		panic(err)
	}
	return filteredCollection("source", "sources", "/sources",
		"source — what reflects an external system into the verse [--probe]; "+
			"add|show|check|remove one",
		opts, func() url.Values {
			// Bare, the columns are what verse has RECORDED. --probe adds what the sources say
			// now, and nothing merges the two or keeps the second.
			q := url.Values{}
			if sourcesFlags.Probe {
				q.Set("probe", "true")
			}
			return q
		}, sourceAddCmd(), sourceShowCmd(), sourceCheckCmd(), sourceRemoveCmd())
}

// sourcePath turns the ref an operator types into the route's two segments, accepting it with
// or without the `sources:` prefix — an operator reading `verse state` and one typing
// `verse source show` should not have to know two spellings of one thing.
func sourcePath(ref string) (string, error) {
	parts := strings.SplitN(strings.TrimPrefix(ref, "sources:"), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("%w: want a ref like sources:git:laptop (or git:laptop), got %q",
			cli.ErrUsage, ref)
	}
	return "/sources/" + parts[0] + "/" + parts[1], nil
}

type sourceShowConfig struct{ *cli.Command }

func sourceShowCmd() *cli.Command {
	cfg := &sourceShowConfig{}
	return cli.NewCommandAt(&cfg.Command, "show").
		WithSynopsis("show a source: show <ref>").WithRun(cfg.run)
}

func (cfg *sourceShowConfig) run(cc *cli.Context, args []string) error {
	ref, err := oneArg(cfg.Command, cc, args, "source show wants a ref")
	if err != nil {
		return err
	}
	path, err := sourcePath(ref)
	if err != nil {
		return err
	}
	resp, err := httpGet(path, nil)
	if err != nil {
		return fmt.Errorf("source show: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return fmt.Errorf("source show: no such source %q", ref)
	}
	return printBody(cc, resp)
}

type sourceCheckConfig struct{ *cli.Command }

func sourceCheckCmd() *cli.Command {
	cfg := &sourceCheckConfig{}
	return cli.NewCommandAt(&cfg.Command, "check").
		WithSynopsis("check <ref> — ask a source what it is and what it claims, right now").
		WithRun(cfg.run)
}

func (cfg *sourceCheckConfig) run(cc *cli.Context, args []string) error {
	ref, err := oneArg(cfg.Command, cc, args, "source check wants a ref")
	if err != nil {
		return err
	}
	path, err := sourcePath(ref)
	if err != nil {
		return err
	}
	resp, err := httpGet(path+"/check", nil)
	if err != nil {
		return fmt.Errorf("source check: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return fmt.Errorf("source check: no such source %q", ref)
	}
	return printBody(cc, resp)
}

type sourceRemoveConfig struct {
	*cli.Command
	Forget bool `cli:"name=forget desc='the reflection is over: tombstone what this source wrote'"`
	Keep   bool `cli:"name=keep desc='the same truth is moving elsewhere: leave the last known state, now frozen'"`
}

func sourceRemoveCmd() *cli.Command {
	cfg := &sourceRemoveConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "remove").
		WithSynopsis("remove a source: remove <ref> [--forget | --keep]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *sourceRemoveConfig) run(cc *cli.Context, args []string) error {
	ref, err := oneArg(cfg.Command, cc, args, "source remove wants a ref")
	if err != nil {
		return err
	}
	path, err := sourcePath(ref)
	if err != nil {
		return err
	}
	// The two intents a delete cannot carry. Sent as given rather than defaulted here: the
	// refusal is the server's to make, so one message says what both flags mean.
	q := url.Values{}
	if cfg.Forget {
		q.Set("forget", "true")
	}
	if cfg.Keep {
		q.Set("keep", "true")
	}
	resp, err := httpDelete(path, q)
	if err != nil {
		return fmt.Errorf("source remove: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return fmt.Errorf("source remove: no such source %q", ref)
	}
	return printBody(cc, resp)
}

type sourceAddConfig struct {
	*cli.Command
	File         string `cli:"name=file aliases=f desc='read the source from FILE (- = stdin); also accepts a positional [file]'"`
	Dir          string `cli:"name=dir desc='reflect this local git repository, in task — instead of a url'"`
	NoProbe      bool   `cli:"name=no-probe desc='install without asking it what it is'"`
	AllowOverlap bool   `cli:"name=allow-overlap desc='install even though another source may write the same slice (both will tombstone what the other reflected)'"`
}

func sourceAddCmd() *cli.Command {
	cfg := &sourceAddConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "add").
		WithSynopsis("add a source: add <system>:<name> <url>, add --dir <d> <system>:<name>, " +
			"or add --file <f> (- = stdin) | [file] | stdin").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *sourceAddConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	q := url.Values{}
	if cfg.NoProbe {
		q.Set("probe", "false")
	}
	if cfg.AllowOverlap {
		q.Set("allow-overlap", "true")
	}
	// Two ways in, because a source is a few fields and one of them is how to reach it. The
	// document form is what a deployment writes; the positional form is what a person types,
	// and refusing them that would make the common case the awkward one.
	if cfg.File == "" && len(args) >= 1 && (len(args) == 2 || cfg.Dir != "") {
		ref := args[0]
		parts := strings.SplitN(strings.TrimPrefix(ref, "sources:"), ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("%w: want `source add <system>:<name> <url>`, got %q",
				cli.ErrUsage, ref)
		}
		body := map[string]any{"system": parts[0], "name": parts[1]}
		if cfg.Dir != "" {
			body["dir"] = cfg.Dir
		} else {
			body["url"] = args[1]
		}
		resp, err := httpPostTo("/sources", q, body)
		return sourceAdded(cc, resp, err)
	}
	src := cfg.File
	if src == "" && len(args) >= 1 {
		src = args[0]
	}
	data, srcFmt, err := codec.ReadSource(cc.In, src, inFmt())
	if err != nil {
		return fmt.Errorf("source add: %w", err)
	}
	// Sent as the document it was written as, for the reason a runner is: the server reads it
	// as a document too, so a field neither side knows is named by the one place that can name
	// it instead of being dropped twice on the way.
	node, err := codec.ParseNode(data, srcFmt)
	if err != nil {
		return fmt.Errorf("source add: %w", err)
	}
	resp, err := httpPostNodeTo("/sources", q, node)
	return sourceAdded(cc, resp, err)
}

func sourceAdded(cc *cli.Context, resp *http.Response, err error) error {
	if err != nil {
		return fmt.Errorf("source add: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return fmt.Errorf("source add: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return printBody(cc, resp)
}
