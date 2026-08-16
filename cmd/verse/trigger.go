package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// triggerFlags narrow WHICH charter is listed. `verse trigger` answers one engine's rules, and
// with packages installed there are several — so the two flags are how a reader says which, and
// `--tree` is how they see that there are several at all.
type triggerFlags struct {
	// Depth is how far under the address to go: 0 is that address alone, 1 adds its immediate
	// children, and -1 (the default) is everything under it. It exists because a charter is a
	// program and printing all of it is often not the question.
	Depth int `cli:"name=depth desc='how far under the address to go: 0 is it alone, -1 (default) is everything'"`
	// At names a charter for `show` and `remove`, where a rule NAME is the argument and a name
	// is unique within a charter rather than across them. The listing needs no such flag: its
	// address is the positional.
	At string `cli:"name=at desc='the charter holding this rule, when more than one has the name'"`
}

var trig = &triggerFlags{Depth: -1}

func triggerCmd() *cli.Command {
	cfg := &struct{ *cli.Command }{}
	// Depth defaults to EVERYTHING, which is -1 rather than the zero value: a struct's zero is
	// what the flags library reads as the default, and 0 means "this address alone".
	trig = &triggerFlags{Depth: -1} // per-invocation, like the filter flags: one command per process
	return cli.NewCommandAt(&cfg.Command, "trigger").
		WithAliases("triggers").
		WithSynopsis("trigger — the charter at an address: the rules there and under it; add|show|remove one").
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
			// THE POSITIONAL IS AN ADDRESS, as it is for `verse state`, and for the same
			// reason: the charter is one document with a place for every package, so the
			// question "what is installed here" is the same question at every depth. Steps
			// or a dotted path, whichever the shell makes easier.
			//
			// A FLAG AMONG THE POSITIONALS ENDS IT, and the tail is parsed after — which is
			// what lets `--depth`/`--trim` sit on either side of the address, exactly as
			// `verse state` allows. Without it `trigger drift --trim …` reads the flag as
			// two more steps and answers "no charter is installed at drift.--trim…".
			var addr []string
			addr, args = args, nil
			for i, a := range addr {
				if strings.HasPrefix(a, "-") {
					addr, args = addr[:i], addr[i:]
					break
				}
			}
			at, err := address(addr)
			if err != nil {
				return err
			}
			if args, err = cfg.Parse(cc, args); err != nil {
				return err
			}
			if len(args) > 0 {
				return fmt.Errorf("%w: trigger takes one address (as one token or as steps), "+
					"and %q is after it", cli.ErrUsage, args[0])
			}
			return printCharterTree(cc, at)
		})
}

// printCharterTree prints the charter standing at an address: what may be written there, what
// it reads, the rules and strata installed there, and the same again for every address under it.
//
//	verse trigger                 the whole charter — baseline at the root, packages below
//	verse trigger drift           the `drift` package and anything under it
//	verse trigger drift comments  …one deeper: the package at drift.comments
//	verse trigger --depth 0       …this address only, without what is under it
//
// ONE SHAPE AT EVERY LEVEL, which is what makes the address worth having: each node answers
// `writes`/`imports`/`rules`/`strata`/`children`, so a reader who learns the root has learned
// every node, and `--trim` cuts the same fields wherever it is asked.
//
// THE RULES THEMSELVES, not their names. This is the charter — the program the verse is running
// — and a listing of names would leave the question it exists to answer ("what does it do") to a
// second command per rule. `--depth 0` is how it gets shorter: the charter at this address and
// nothing under it.
//
// Assembled here rather than served: the wire carries the flat rows (one per charter, each
// naming its address) and one listing per charter, which are the two questions the server
// already answers. The shape is a rendering of them, as the docket's is.
func printCharterTree(cc *cli.Context, at string) error {
	rows, err := charterRows(cc)
	if err != nil || rows == nil {
		return err
	}
	want, err := entity.ParseAt(at) // the address as a path, so `drift comments` and `drift.comments` are one
	if err != nil {
		return fmt.Errorf("trigger: %w", err)
	}
	segs := kpathSegs(want)
	root := &charterNode{}
	found := at == ""
	for _, row := range rows {
		addr := []string{}
		if row.At != api.BaselinePackage {
			addr = strings.Split(row.At, ".")
		}
		rest, under := below(addr, segs)
		if !under {
			continue
		}
		found = true
		if trig.Depth >= 0 && len(rest) > trig.Depth {
			continue // deeper than asked for
		}
		node := root
		for _, seg := range rest {
			node = node.child(seg)
		}
		node.row, node.here = row, true
		if node.rules, node.strata, err = charterOf(row.At); err != nil {
			return fmt.Errorf("trigger: %s: %w", row.At, err)
		}
	}
	if !found {
		return fmt.Errorf("no charter is installed at %s, and none under it — `verse trigger` "+
			"lists every address that has one", at)
	}
	return printIR(cc, root.node())
}

// below reports whether a charter's address is at or under the address asked for, and what is
// left of it once the asked-for part is taken off — which is where it hangs in the answer.
func below(addr, want []string) ([]string, bool) {
	if len(addr) < len(want) {
		return nil, false
	}
	for i, seg := range want {
		if addr[i] != seg {
			return nil, false
		}
	}
	return addr[len(want):], true
}

// kpathSegs is a parsed address as plain segments.
func kpathSegs(kp *kpath.KPath) []string {
	var out []string
	for p := kp; p != nil; p = p.Next {
		if p.Field != nil {
			out = append(out, *p.Field)
		}
	}
	return out
}

// charterRows reads which charters exist, or nil when the answer was an error this has already
// printed (an unreadable store says so in its own words).
func charterRows(cc *cli.Context) ([]api.CharterPackage, error) {
	resp, err := httpGet("/packages", nil)
	if err != nil {
		return nil, fmt.Errorf("trigger: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, printBody(cc, resp)
	}
	var rows []api.CharterPackage
	decodeClose(resp, &rows)
	return rows, nil
}

// charterOf reads one charter's rules and strata in full — the two listings the server already
// answers, asked per address.
func charterOf(at string) (rules, strata *ir.Node, err error) {
	q := url.Values{}
	if at != api.BaselinePackage {
		q.Set("at", at)
	}
	if rules, err = listingNode(q, "/triggers"); err != nil {
		return nil, nil, err
	}
	strata, err = listingNode(q, "/strata")
	return rules, strata, err
}

// listingNode reads one of the server's listings as a document, rather than printing it: the
// tree holds several, and each has to be placed rather than emitted where it arrived.
func listingNode(q url.Values, route string) (*ir.Node, error) {
	resp, err := httpGet(route, q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, errors.New(strings.TrimSpace(string(body)))
	}
	return codec.ParseNode(body, respFmt(resp))
}

// charterNode is one place in the address tree: the charter installed there (or none, for a
// segment that only leads somewhere) and whatever is installed below it.
type charterNode struct {
	row      api.CharterPackage
	here     bool // a charter is installed AT this address, rather than only under it
	rules    *ir.Node
	strata   *ir.Node
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
	var fields, values []*ir.Node
	add := func(key string, v *ir.Node) {
		fields = append(fields, ir.FromString(key))
		values = append(values, v)
	}
	if n.here {
		add("writes", ir.FromString(n.row.Writes))
		if n.row.Imports != nil {
			add("imports", strs(n.row.Imports))
		}
		add("rules", orEmpty(n.rules))
		if n.strata != nil && len(n.strata.Values) > 0 {
			add("strata", n.strata)
		}
	}
	if len(n.kidOrder) > 0 {
		sort.Strings(n.kidOrder)
		var kf, kv []*ir.Node
		for _, seg := range n.kidOrder {
			kf = append(kf, ir.FromString(seg))
			kv = append(kv, n.kids[seg].node())
		}
		add("children", &ir.Node{Type: ir.ObjectType, Fields: kf, Values: kv})
	}
	return &ir.Node{Type: ir.ObjectType, Fields: fields, Values: values}
}

func orEmpty(n *ir.Node) *ir.Node {
	if n == nil {
		return ir.FromSlice(nil)
	}
	return n
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
		WithSynopsis("show a trigger: show <name> [--at <address>]").
		WithOpts(mustOpts(trig)...).
		WithRun(cfg.run)
}

func (cfg *triggerShowConfig) run(cc *cli.Context, args []string) error {
	name, err := oneArg(cfg.Command, cc, args, "trigger show wants a name")
	if err != nil {
		return err
	}
	at, err := whichCharter(cc, "trigger show", name)
	if err != nil {
		return err
	}
	resp, err := httpGet("/triggers/"+name, atQuery(at))
	if err != nil {
		return fmt.Errorf("trigger show: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return fmt.Errorf("trigger show: no such trigger %q", name)
	}
	return printBody(cc, resp)
}

// whichCharter finds the charter holding a rule by name, since a name is unique WITHIN a charter
// and not across them: two packages may each have a `review`, which is most of what packages are
// for. It answers the address to ask at.
//
// `--at` settles it when the operator knows. Otherwise the tree is read and the name looked for:
// exactly one charter holding it is no ambiguity at all and is answered without ceremony, and
// several is a question only the operator can settle — so it is asked, naming them.
//
// Baseline first, because a charter with no address is where a rule is until somebody packages
// it, and because `verse trigger show <name>` meant baseline before packages existed.
func whichCharter(cc *cli.Context, what, name string) (string, error) {
	if trig.At != "" {
		return trig.At, nil
	}
	rows, err := charterRows(cc)
	if err != nil || rows == nil {
		return "", err
	}
	var holds []string
	for _, row := range rows {
		for _, rule := range row.Rules {
			if rule != name {
				continue
			}
			if row.At == api.BaselinePackage {
				return "", nil // baseline: ask with no address, as this always did
			}
			holds = append(holds, row.At)
		}
	}
	switch len(holds) {
	case 0:
		return "", nil // let the request 404 and say so in the caller's words
	case 1:
		return holds[0], nil
	}
	return "", fmt.Errorf("%w: %s %s: %d charters hold a rule called %q (%s) — say which with "+
		"`--at <address>`", cli.ErrUsage, what, name, len(holds), name, strings.Join(holds, ", "))
}

// atQuery is `?at=` when there is an address, and nothing when the charter is baseline's.
func atQuery(at string) url.Values {
	if at == "" {
		return nil
	}
	return url.Values{"at": []string{at}}
}

type triggerRemoveConfig struct{ *cli.Command }

func triggerRemoveCmd() *cli.Command {
	cfg := &triggerRemoveConfig{}
	return cli.NewCommandAt(&cfg.Command, "remove").
		WithSynopsis("remove a trigger: remove <name> [--at <address>]").
		WithOpts(mustOpts(trig)...).
		WithRun(cfg.run)
}

func (cfg *triggerRemoveConfig) run(cc *cli.Context, args []string) error {
	name, err := oneArg(cfg.Command, cc, args, "trigger remove wants a name")
	if err != nil {
		return err
	}
	at, err := whichCharter(cc, "trigger remove", name)
	if err != nil {
		return err
	}
	resp, err := httpDelete("/triggers/"+name, atQuery(at))
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
