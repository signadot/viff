package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
)

// The alias is irregular rather than invented — a stratum is one level, strata are the
// ordering — so the plural anyone typing the Latin reaches for lands on this command too.
func stratumCmd() *cli.Command {
	return collection("stratum", "strata", "/strata",
		"stratum — the declared level ordering; add|show|remove one level",
		stratumAddCmd(), stratumShowCmd(), stratumRemoveCmd())
}

type stratumShowConfig struct{ *cli.Command }

func stratumShowCmd() *cli.Command {
	cfg := &stratumShowConfig{}
	return cli.NewCommandAt(&cfg.Command, "show").
		WithSynopsis("show a stratum: show <name>").WithRun(cfg.run)
}

func (cfg *stratumShowConfig) run(cc *cli.Context, args []string) error {
	name, err := oneArg(cfg.Command, cc, args, "stratum show wants a name")
	if err != nil {
		return err
	}
	resp, err := httpGet("/strata/"+name, nil)
	if err != nil {
		return fmt.Errorf("stratum show: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return fmt.Errorf("stratum show: no such stratum %q", name)
	}
	return printBody(cc, resp)
}

type stratumRemoveConfig struct{ *cli.Command }

func stratumRemoveCmd() *cli.Command {
	cfg := &stratumRemoveConfig{}
	return cli.NewCommandAt(&cfg.Command, "remove").
		WithSynopsis("remove a stratum: remove <name> (refused while rules still name it)").
		WithRun(cfg.run)
}

func (cfg *stratumRemoveConfig) run(cc *cli.Context, args []string) error {
	name, err := oneArg(cfg.Command, cc, args, "stratum remove wants a name")
	if err != nil {
		return err
	}
	resp, err := httpDelete("/strata/"+name, nil)
	if err != nil {
		return fmt.Errorf("stratum remove: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("stratum remove: no such stratum %q", name)
	}
	// A 409 carries the reason — which rules still name it — and that reason is the whole
	// value of the refusal, so it is relayed rather than flattened into the status line.
	if resp.StatusCode == http.StatusConflict {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stratum remove: %s", strings.TrimSpace(string(msg)))
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("stratum remove: %s", resp.Status)
	}
	fmt.Fprintln(cc.Out, "removed", name)
	return nil
}

type stratumAddConfig struct {
	*cli.Command
	File string `cli:"name=file aliases=f desc='read the stratum from FILE (- = stdin); also accepts a positional [file]'"`
}

func stratumAddCmd() *cli.Command {
	cfg := &stratumAddConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "add").
		WithSynopsis("add stratum(a): add --file <f> (- = stdin) | [file] | stdin; one stratum or a list. " +
			"A charter file holding both strata and rules goes to `verse trigger add`, which orders them").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *stratumAddConfig) run(cc *cli.Context, args []string) error {
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
		return fmt.Errorf("stratum add: %w", err)
	}
	defs, err := charterDefs(data, srcFmt)
	if err != nil {
		return fmt.Errorf("stratum add: %w", err)
	}
	if len(defs) == 0 {
		return fmt.Errorf("stratum add: no stratum found in the input")
	}
	for _, def := range defs {
		if kind := defKind(def); kind != defStratum {
			return fmt.Errorf("stratum add: this is not a stratum (a stratum is {name, level, "+
				"match}); it looks like %s — install it with `verse trigger add`", kind)
		}
	}
	if err := postDefs(cc, "/strata", defs); err != nil {
		return fmt.Errorf("stratum add: %w", err)
	}
	fmt.Fprintf(cc.Out, "installed %d stratum(a)\n", len(defs))
	return nil
}

// ── the shared charter-file reader ────────────────────────────────────────────────

// A charter file holds rules, strata, or both, in any mix of documents and lists. What tells
// them apart is their FIELDS, which is how this package already tells a formula from a match: a
// rule has a `condition`, a stratum has a `level` and a `match`.
//
// Classifying beats requiring separate files or a fixed document order, because the ordering
// matters — a rule installed before the stratum it names is held rather than installed — and
// making the author responsible for it would be making them responsible for something the tool
// knows. See postCharter, which sorts them.
const (
	defTrigger = "a trigger"
	defStratum = "a stratum"
	defUnknown = "neither a trigger nor a stratum"
)

func defKind(n *ir.Node) string {
	// Unwrapped HERE rather than by the caller, because this is the question a comment gets in
	// the way of: a definition somebody wrote a note above is a CommentType node, and asking a
	// wrapper what fields it has answers none. The caller keeps the wrapper, since the note is
	// part of the rule it installs.
	n = codec.Unwrap(n)
	has := func(field string) bool {
		if n == nil || n.Type != ir.ObjectType {
			return false
		}
		for _, f := range n.Fields {
			if f.String == field {
				return true
			}
		}
		return false
	}
	switch {
	case has("condition"):
		return defTrigger
	case has("level") && has("match"):
		return defStratum
	}
	return defUnknown
}

// charterDefs flattens a charter source into its definitions: several documents, each holding
// one definition or a list of them.
//
// Shape comes from the parsed document rather than from a leading character: tony writes a list
// either as `[a, b]` or as a block `- a`, so a "starts with [" test would read a perfectly good
// block list as a single definition. A `!key(name)` tag on the list is carried through and
// ignored here — it is an ARRAY either way, and the tag is what makes re-sending an edited file
// merge by name rather than by position.
func charterDefs(data []byte, srcFmt codec.Format) ([]*ir.Node, error) {
	docs, err := codec.ParseNodes(data, srcFmt)
	if err != nil {
		return nil, err
	}
	// UNWRAPPED TO LOOK AT, KEPT AS IT WAS TO SEND. A comment wraps the value it precedes, so
	// deciding what a document IS means stepping past the wrapper — and installing it means not
	// throwing the wrapper away, because that comment is the author's reason for the rule and
	// the record is meant to be the rule as they left it (trigger.Trigger's Doc field).
	//
	// Unwrap used to be applied to the value itself here, which was invisible for as long as
	// nothing parsed comments and lossy the moment something did.
	var defs []*ir.Node
	for _, doc := range docs {
		inner := codec.Unwrap(doc)
		if inner == nil || inner.Type == ir.NullType {
			continue // a trailing `---`, or an empty document
		}
		if inner.Type == ir.ArrayType {
			for _, elem := range inner.Values {
				if in := codec.Unwrap(elem); in != nil && in.Type != ir.NullType {
					defs = append(defs, elem)
				}
			}
			continue
		}
		defs = append(defs, doc)
	}
	return defs, nil
}

// postDefs installs each definition at one route, stopping at the first refusal.
func postDefs(cc *cli.Context, route string, defs []*ir.Node) error {
	return postDefsTo(cc, route, nil, defs)
}

// postDefsTo is postDefs with query parameters — `?at=` for a charter package, which the
// install routes read to pick the engine a definition lands in.
func postDefsTo(cc *cli.Context, route string, q url.Values, defs []*ir.Node) error {
	for _, def := range defs {
		resp, err := httpPostNodeTo(route, q, def)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusCreated {
			msg, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
		}
		resp.Body.Close()
	}
	return nil
}
