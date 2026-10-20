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

type iosConfig struct {
	Probe bool `cli:"name=probe desc='also ask each iod what it is, right now (not recorded)'"`
}

var iosFlags = &iosConfig{}

func ioCmd() *cli.Command {
	opts, err := cli.StructOpts(iosFlags)
	if err != nil {
		panic(err)
	}
	return filteredCollection("iod", "iods", "/io",
		"iod — a connector between the verse and an external system: it reflects in, and it acts "+
			"out [--probe]; add|show|check|remove one",
		opts, func() url.Values {
			// Bare, the columns are what verse has RECORDED. --probe adds what the iods say
			// now, and nothing merges the two or keeps the second.
			q := url.Values{}
			if iosFlags.Probe {
				q.Set("probe", "true")
			}
			return q
		}, ioAddCmd(), ioShowCmd(), ioCheckCmd(), ioRemoveCmd())
}

// ioPath turns the address an operator types into the route — see namePath,
// the one reader the three registries share.
func ioPath(ref string) (string, error) { return namePath("io", "iod", ref) }

type ioShowConfig struct{ *cli.Command }

func ioShowCmd() *cli.Command {
	cfg := &ioShowConfig{}
	return cli.NewCommandAt(&cfg.Command, "show").
		WithSynopsis("show an iod: show <ref>").WithRun(cfg.run)
}

func (cfg *ioShowConfig) run(cc *cli.Context, args []string) error {
	ref, err := oneArg(cfg.Command, cc, args, "iod show wants a ref")
	if err != nil {
		return err
	}
	path, err := ioPath(ref)
	if err != nil {
		return err
	}
	resp, err := httpGet(path, nil)
	if err != nil {
		return fmt.Errorf("iod show: %w", err)
	}
	return said(cc, resp, "iod show")
}

type ioCheckConfig struct{ *cli.Command }

func ioCheckCmd() *cli.Command {
	cfg := &ioCheckConfig{}
	return cli.NewCommandAt(&cfg.Command, "check").
		WithSynopsis("check <ref> — ask an iod what it is and what it claims, right now").
		WithRun(cfg.run)
}

func (cfg *ioCheckConfig) run(cc *cli.Context, args []string) error {
	ref, err := oneArg(cfg.Command, cc, args, "iod check wants a ref")
	if err != nil {
		return err
	}
	path, err := ioPath(ref)
	if err != nil {
		return err
	}
	resp, err := httpGet(path+"/check", nil)
	if err != nil {
		return fmt.Errorf("iod check: %w", err)
	}
	return said(cc, resp, "iod check")
}

type ioRemoveConfig struct {
	*cli.Command
	Forget bool `cli:"name=forget desc='the reflection is over: tombstone what this iod wrote'"`
	Keep   bool `cli:"name=keep desc='the same truth is moving elsewhere: leave the last known state, now frozen'"`
}

func ioRemoveCmd() *cli.Command {
	cfg := &ioRemoveConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "remove").
		WithSynopsis("remove an iod: remove <ref> [--forget | --keep]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *ioRemoveConfig) run(cc *cli.Context, args []string) error {
	ref, err := oneArg(cfg.Command, cc, args, "iod remove wants a ref")
	if err != nil {
		return err
	}
	path, err := ioPath(ref)
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
		return fmt.Errorf("iod remove: %w", err)
	}
	return said(cc, resp, "iod remove")
}

type ioAddConfig struct {
	*cli.Command
	File         string `cli:"name=file aliases=f desc='read the iod from FILE (- = stdin); also accepts a positional [file]'"`
	Dir          string `cli:"name=dir desc='reflect this local git repository, in task — instead of a url'"`
	Root         string `cli:"name=root desc='the root of the tree this iod writes, which is which connector it is: git, github, linear, slack (default with --dir: git)'"`
	NoProbe      bool   `cli:"name=no-probe desc='install without asking it what it is'"`
	AllowOverlap bool   `cli:"name=allow-overlap desc='install even though another iod may write the same slice (both will tombstone what the other reflected)'"`
	Out          string `cli:"name=out desc='the place its OUT half reads what is asked of it (repeatable, comma-separated): inside what it claims, outside what it reflects'"`
	As           string `cli:"name=as desc='the principal the out half acts as, and the owner of every status it writes (default: agent:<root>-iod)'"`
	Requesters   string `cli:"name=requesters desc='who may ask it for something (comma-separated grantees): they get read-write below each out place, and never on the statuses'"`
}

func ioAddCmd() *cli.Command {
	cfg := &ioAddConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "add").
		WithSynopsis("add an iod: add <name> <url> --root <root>, add --dir <d> <name>, " +
			"or add --file <f> (- = stdin) | [file] | stdin").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *ioAddConfig) run(cc *cli.Context, args []string) error {
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
	// Two ways in, because an iod is a few fields and one of them is how to reach it. The
	// document form is what a deployment writes; the positional form is what a person types,
	// and refusing them that would make the common case the awkward one.
	if cfg.File == "" && len(args) >= 1 && (len(args) == 2 || cfg.Dir != "") {
		ref := args[0]
		name, ok := nameSeg("iod", ref)
		if !ok {
			return fmt.Errorf("%w: want `iod add <name> <url> --root <root>`, got %q",
				cli.ErrUsage, ref)
		}
		// WHICH CONNECTOR IT IS is said, not discovered: the install checks it against what the
		// iod says of itself, and a check against something read off the same iod checks
		// nothing. `--dir` is the in-process git iod and can only be that.
		root := cfg.Root
		if root == "" && cfg.Dir != "" {
			root = "git"
		}
		if root == "" {
			return fmt.Errorf("%w: `iod add %s <url>` wants --root: the root of the tree it "+
				"writes, which is which connector it is (git, github, linear, slack)", cli.ErrUsage, name)
		}
		body := map[string]any{"root": root, "name": name}
		if cfg.Dir != "" {
			body["dir"] = cfg.Dir
		} else {
			body["url"] = args[1]
		}
		if out := cfg.outHalf(root); out != nil {
			body["out"] = out
		}
		resp, err := httpPostTo("/io", q, body)
		return ioAdded(cc, resp, err)
	}
	src := cfg.File
	if src == "" && len(args) >= 1 {
		src = args[0]
	}
	data, srcFmt, err := codec.ReadSource(cc.In, src, inFmt())
	if err != nil {
		return fmt.Errorf("iod add: %w", err)
	}
	// Sent as the document it was written as, for the reason a runner is: the server reads it
	// as a document too, so a field neither side knows is named by the one place that can name
	// it instead of being dropped twice on the way.
	node, err := codec.ParseNode(data, srcFmt)
	if err != nil {
		return fmt.Errorf("iod add: %w", err)
	}
	resp, err := httpPostNodeTo("/io", q, node)
	return ioAdded(cc, resp, err)
}

// outHalf is the out half an operator spelled on the command line, or nil for an iod that only
// reflects. `--as` defaults to the connector's own name, since one deployment per external system
// is the shape and `agent:slack-iod` is what its grants are written against.
func (cfg *ioAddConfig) outHalf(root string) map[string]any {
	if cfg.Out == "" {
		return nil
	}
	as := cfg.As
	if as == "" {
		as = "agent:" + root + "-iod"
	}
	out := map[string]any{"places": splitList(cfg.Out), "as": as}
	if cfg.Requesters != "" {
		out["requesters"] = splitList(cfg.Requesters)
	}
	return out
}

// splitList reads a comma-separated flag as the list it spells, dropping the empties a trailing
// comma leaves.
func splitList(s string) []any {
	var out []any
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func ioAdded(cc *cli.Context, resp *http.Response, err error) error {
	if err != nil {
		return fmt.Errorf("iod add: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return fmt.Errorf("iod add: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return printBody(cc, resp)
}
