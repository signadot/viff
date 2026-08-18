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

// THE CHARTER IS ONE COMMAND, because it is one thing: rules and levels are installed from one
// file, confined by one address, and they come and go together. `verse charter` and `verse
// stratum` were two commands each answering a slice of it, and neither could show a charter.
//
//	verse charter                       the whole charter, at every address
//	verse charter verse-dev drift       …the one installed there, and anything under it
//	verse charter --depth 0             …this address alone
//	verse charter add -f <file>         install one (rules, levels and reads, in order)
//	verse charter uninstall <address>   take a package down whole
//	verse charter show <name>           one rule or one level
//	verse charter remove <name>         …and how it goes
//
// The tree it prints is the SERVER's (versed/charter.go); this end asks for an address and a
// depth and renders what comes back, which is what keeps the two from disagreeing about shape.

type charterFlags struct {
	// Depth is how far under the address to go: 0 is that address alone, 1 adds its immediate
	// children, and -1 (the default) is everything under it. A charter is a program, and
	// printing all of one is often not the question.
	Depth int `cli:"name=depth desc='how far under the address to go: 0 is it alone, -1 (default) is everything'"`
	// At names a charter for `show` and `remove`, where a NAME is the argument and a name is
	// unique within a charter rather than across them. The listing needs no such flag: its
	// address is the positional.
	At string `cli:"name=at desc='the charter holding this rule or level, when more than one has the name'"`
}

var trig = &charterFlags{Depth: -1}

func charterCmd() *cli.Command {
	cfg := &struct{ *cli.Command }{}
	trig = &charterFlags{Depth: -1} // per-invocation, like the filter flags
	return cli.NewCommandAt(&cfg.Command, "charter").
		WithAliases("charters", "c").
		WithSynopsis("charter — the installed charter at an address; add|show|remove|uninstall").
		WithOpts(append(mustOpts(trig), filterOpts()...)...).
		WithSubs(charterAddCmd(), charterShowCmd(), charterRemoveCmd(), charterUninstallCmd()).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			// `--` ENDS THE VERBS, so an address that collides with one is still reachable: a
			// package at `remove` is `verse charter -- remove`, and without it the verb wins
			// every time. The collision is real rather than theoretical — a package's address
			// is a system name, and `add`, `show` and `remove` are ordinary words.
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			} else if handled, err := dispatch(cfg.Command, cc, args); handled {
				return err
			}
			// THE POSITIONAL IS AN ADDRESS, as it is for `verse state`, and a flag among the
			// positionals ends it — which is what lets `--depth`/`--trim` sit on either side.
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
				return fmt.Errorf("%w: charter takes one address (as one token or as steps), "+
					"and %q is after it", cli.ErrUsage, args[0])
			}
			return printCharter(cc, at)
		})
}

// printCharter asks for the charter at an address and prints what comes back.
func printCharter(cc *cli.Context, at string) error {
	q := url.Values{}
	if at != "" {
		q.Set("at", at)
	}
	if trig.Depth >= 0 {
		q.Set("depth", fmt.Sprint(trig.Depth))
	}
	resp, err := httpGet("/charter", q)
	if err != nil {
		return fmt.Errorf("charter: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		where := "at " + at
		if at == "" {
			where = "anywhere"
		}
		return fmt.Errorf("charter: no charter is installed %s, and none under it", where)
	}
	return printBody(cc, resp)
}

type charterUninstallConfig struct {
	*cli.Command
	Recurse bool `cli:"name=recurse desc='and every charter installed under this address'"`
}

// uninstall takes a whole charter package down — its rules, its levels, the declaration of what
// it declares it reads, and the engine running them.
//
// A VERB OF ITS OWN rather than `remove` with an address, because the two arguments are
// different KINDS of thing: `remove` takes a rule's name, this takes an address, and one
// command reading its positional as either would guess wrong the first time somebody names a
// rule after the system it acts on. It pairs with `add --at`, which is what installed it.
//
// What it leaves standing is what the package WROTE — a charter's writes are the world's. The
// server's removePackage says why at length.
func charterUninstallCmd() *cli.Command {
	cfg := &charterUninstallConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "uninstall").
		WithSynopsis("uninstall a charter package: uninstall <address> [--recurse]").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *charterUninstallConfig) run(cc *cli.Context, args []string) error {
	args, err := cfg.Parse(cc, args)
	if err != nil {
		return err
	}
	at, err := address(args)
	if err != nil {
		return err
	}
	if at == "" {
		return fmt.Errorf("%w: charter uninstall wants the address of a charter package "+
			"(`drift`, `demo pr`)", cli.ErrUsage)
	}
	q := url.Values{"at": []string{at}}
	if cfg.Recurse {
		q.Set("recurse", "true")
	}
	resp, err := httpDelete("/charter", q)
	if err != nil {
		return fmt.Errorf("charter uninstall: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("charter uninstall: no charter is installed at %s", at)
	}
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("charter uninstall: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	fmt.Fprintf(cc.Out, "uninstalled the charter at %s; what it wrote is still there\n", at)
	return nil
}

type charterShowConfig struct{ *cli.Command }

func charterShowCmd() *cli.Command {
	cfg := &charterShowConfig{}
	return cli.NewCommandAt(&cfg.Command, "show").
		WithSynopsis("show a trigger: show <name> [--at <address>]").
		WithOpts(mustOpts(trig)...).
		WithRun(cfg.run)
}

func (cfg *charterShowConfig) run(cc *cli.Context, args []string) error {
	name, err := oneArg(cfg.Command, cc, args, "charter show wants a name")
	if err != nil {
		return err
	}
	at, err := whichCharter(cc, "charter show", name)
	if err != nil {
		return err
	}
	resp, err := httpGet("/charter/"+name, atQuery(at))
	if err != nil {
		return fmt.Errorf("charter show: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return fmt.Errorf("charter show: no such trigger %q", name)
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
// it, and because `verse charter show <name>` meant baseline before packages existed.
func whichCharter(cc *cli.Context, what, name string) (string, error) {
	if trig.At != "" {
		return trig.At, nil
	}
	resp, err := httpGet("/charter", nil)
	if err != nil {
		return "", fmt.Errorf("%s: %w", what, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode >= http.StatusBadRequest {
		return "", nil // no tree to search: let the request itself answer
	}
	tree, err := codec.ParseNode(body, respFmt(resp))
	if err != nil {
		return "", nil
	}
	holds := charterHolding(tree, name, nil)
	switch len(holds) {
	case 0:
		return "", nil // let the request 404 and say so in the caller's words
	case 1:
		return holds[0], nil
	}
	// Baseline first, because a charter with no address is where a rule is until somebody
	// packages it, and because `verse charter show <name>` meant baseline before packages.
	for _, at := range holds {
		if at == "" {
			return "", nil
		}
	}
	return "", fmt.Errorf("%w: %s %s: %d charters hold a rule or level called %q (%s) — say which with `--at <address>`",
		cli.ErrUsage, what, name, len(holds), name, strings.Join(holds, ", "))
}

// charterHolding walks the tree for the addresses whose charter holds a rule or a level of this
// name, which is the question `show` and `remove` have to answer before they can ask anything:
// the name is theirs, and the address is what says whose.
func charterHolding(node *ir.Node, name string, at []string) []string {
	node = codec.Unwrap(node)
	if node == nil || node.Type != ir.ObjectType {
		return nil
	}
	var out []string
	for i, key := range node.Fields {
		if key == nil || key.Type != ir.StringType {
			continue
		}
		v := codec.Unwrap(node.Values[i])
		switch key.String {
		case "triggers", "strata":
			if v != nil && v.Type == ir.ArrayType && namedIn(v, name) {
				out = append(out, strings.Join(at, "."))
			}
		case "children":
			if v == nil || v.Type != ir.ObjectType {
				continue
			}
			for j, seg := range v.Fields {
				if seg == nil || seg.Type != ir.StringType {
					continue
				}
				out = append(out, charterHolding(v.Values[j], name, append(append([]string{}, at...), seg.String))...)
			}
		}
	}
	return dedupeStrings(out)
}

// namedIn reports whether a list of rules or levels holds one with this name.
func namedIn(list *ir.Node, name string) bool {
	for _, el := range list.Values {
		el = codec.Unwrap(el)
		if el == nil || el.Type != ir.ObjectType {
			continue
		}
		for i, key := range el.Fields {
			if key != nil && key.Type == ir.StringType && key.String == "name" {
				if v := codec.Unwrap(el.Values[i]); v != nil && v.String == name {
					return true
				}
			}
		}
	}
	return false
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// atQuery is `?at=` when there is an address, and nothing when the charter is baseline's.
func atQuery(at string) url.Values {
	if at == "" {
		return nil
	}
	return url.Values{"at": []string{at}}
}

type charterRemoveConfig struct{ *cli.Command }

func charterRemoveCmd() *cli.Command {
	cfg := &charterRemoveConfig{}
	return cli.NewCommandAt(&cfg.Command, "remove").
		WithSynopsis("remove a trigger: remove <name> [--at <address>]").
		WithOpts(mustOpts(trig)...).
		WithRun(cfg.run)
}

func (cfg *charterRemoveConfig) run(cc *cli.Context, args []string) error {
	name, err := oneArg(cfg.Command, cc, args, "charter remove wants a name")
	if err != nil {
		return err
	}
	at, err := whichCharter(cc, "charter remove", name)
	if err != nil {
		return err
	}
	resp, err := httpDelete("/charter/"+name, atQuery(at))
	if err != nil {
		return fmt.Errorf("charter remove: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("charter remove: no such trigger %q", name)
	}
	if resp.StatusCode != http.StatusNoContent {
		return statusError(resp, "charter remove")
	}
	// In a scope this is a TOMBSTONE hiding an inherited rule from this scope alone, and in
	// baseline it is the rule going away — the same word for two different facts, told apart
	// only by a flag the operator typed a while ago. So say which one happened.
	if g.Scope != "" {
		fmt.Fprintf(cc.Out, "removed %s in scope %s; baseline is untouched\n", name, g.Scope)
		return nil
	}
	fmt.Fprintln(cc.Out, "removed", name)
	return nil
}

type charterAddConfig struct {
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

func charterAddCmd() *cli.Command {
	cfg := &charterAddConfig{}
	opts, err := cli.StructOpts(cfg)
	if err != nil {
		panic(err)
	}
	return cli.NewCommandAt(&cfg.Command, "add").
		WithSynopsis("add trigger(s): add --file <f> (- = stdin) | [file] | stdin; one trigger, a list, or `---`-separated documents. Format follows the file suffix (.tony/.json/.yaml)").
		WithOpts(opts...).
		WithRun(cfg.run)
}

func (cfg *charterAddConfig) run(cc *cli.Context, args []string) error {
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
		return fmt.Errorf("charter add: %w", err)
	}

	// A charter file may hold one trigger, a list of them, or several documents separated
	// by `---` — and, since rules declare the stratum they live in, the strata themselves.
	defs, err := charterDefs(data, srcFmt)
	if err != nil {
		return fmt.Errorf("charter add: %w", err)
	}
	if len(defs) == 0 {
		return fmt.Errorf("charter add: no trigger found in the input")
	}

	// ONE FILE MAY HOLD SEVERAL PACKAGES, and a `---` document carrying `at:` is where each
	// begins. That is what keeps a charter that spans two addresses ONE program in one file:
	//
	//	at: drift
	//	reads: [git]
	//	---
	//	- name: head-moved …
	//	---
	//	at: merge
	//	reads: [git, drift]
	//	---
	//	- name: proposal-appeared …
	//
	// Everything after a declaration belongs to it, until the next one. A file with no
	// declaration is one charter, installed where `--at` says (or baseline, which is most of
	// them), and that is the case this started as.
	groups, err := charterGroups(defs, cfg.At)
	if err != nil {
		return fmt.Errorf("charter add: %w", err)
	}
	packaged := false
	// Read before the loop, whose `g` is a group and shadows the globals this lives on.
	inScope := ""
	if g.Scope != "" {
		inScope = " in scope " + g.Scope
	}
	for _, g := range groups {
		q := url.Values{}
		if g.at != "" {
			q.Set("at", g.at)
		}
		if g.reads != nil && g.at == "" {
			return fmt.Errorf("charter add: `reads:` belongs to a charter package — give " +
				"it an address (`at:` in the declaration, or `--at`), or drop it: the " +
				"baseline charter may write anywhere, so there is no graph an import would " +
				"be an edge in")
		}
		if err := postGroup("/charter", q, g.defs()); err != nil {
			return fmt.Errorf("charter add: %w", err)
		}
		// WHERE, in both coordinates. "baseline" is the unpackaged charter — the one that may
		// write anywhere — and it is also the word for the default SCOPE, so a scoped install
		// that says only "baseline" reads as the one place it did not go. A scope is the
		// coordinate somebody carries in a flag and forgets, which is exactly the one a report
		// owes them back.
		where := "baseline"
		if g.at != "" {
			where = "package " + g.at
		}
		where += inScope
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
	// leaves `verse charter` — the next thing anybody types — answering baseline's rules, which
	// is an empty list. The listing says so too (elsewhereInPackages); saying it here as well is
	// what keeps somebody from having to be surprised first.
	if packaged {
		fmt.Fprintln(cc.Out, "`verse charter` shows every charter and where each may write")
	}
	return nil
}

const (
	defTrigger = "a trigger"
	defStratum = "a stratum"
	defReads   = "a package declaration"
	defUnknown = "none of a trigger, a stratum and a package declaration"
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
	// `at:` alone is a declaration too — a package that reads nothing outside itself still says where it is,
	// which is what lets one file open several packages and what makes "reads nothing outside
	// itself" a thing somebody can write down rather than only a default.
	case has("reads"), has("at"):
		return defReads
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

// postGroup installs a package's worth of a charter in ONE request, which is what makes it one
// step: the server applies the whole file or none of it, in the order the three kinds have to go
// in (versed/charter.go). `q` carries `?at=`, which is what picks the engine they land in.
//
// Sent one document at a time, this is what left a file HALF-APPLIED: the first document installed,
// the second was refused, and the operator was told the command failed while some of the file
// stood. Worse where the half that stood was the levels — a package with levels and no rules is a
// package, its stratum could not be removed by re-applying a corrected file, and `charter
// uninstall` was the only way out of a state nobody wrote down.
func postGroup(route string, q url.Values, defs []*ir.Node) error {
	resp, err := httpPostNodesTo(route, q, defs)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// charterGroup is one package's worth of a charter file: where it goes, what it declares it
// reads, and the definitions that belong to it.
type charterGroup struct {
	at     string
	reads  *ir.Node // the declaration document, nil when there is none
	strata []*ir.Node
	rules  []*ir.Node
}

// defs is the group as one list, in the order the three kinds install: the declaration, because a
// rule is checked against what its package declares it reads as it installs; then the levels, because a rule
// naming a stratum that is not here yet is HELD rather than refused; then the rules.
//
// The server sorts them again and does not depend on this order — a client posting a charter says
// what it has by writing it, not by choosing an order — but sending them the right way round is
// what keeps the two ends agreeing about what a charter file is.
func (g charterGroup) defs() []*ir.Node {
	out := make([]*ir.Node, 0, len(g.strata)+len(g.rules)+1)
	if g.reads != nil {
		out = append(out, g.reads)
	}
	out = append(out, g.strata...)
	return append(out, g.rules...)
}

// charterGroups splits a charter file into the packages it installs.
//
// A declaration document (`reads:`, optionally with `at:`) OPENS a group, and everything after
// it belongs to that group until the next one. Definitions before any declaration are the file's
// own charter, installed at `--at` — which is the shape every charter had before packages, and
// the shape most still have.
//
// STRATA AND RULES ARE SEPARATED WITHIN A GROUP, whatever order the file put them in: a rule
// naming a stratum that is not installed yet is HELD rather than refused — right for two
// independent installs, and wrong for one document whose author wrote both, where it would leave
// half the charter dormant because of how it was laid out.
func charterGroups(defs []*ir.Node, flagAt string) ([]charterGroup, error) {
	cur := charterGroup{at: flagAt}
	var out []charterGroup
	seen := map[string]bool{}
	for _, def := range defs {
		switch defKind(def) {
		case defStratum:
			cur.strata = append(cur.strata, def)
		case defTrigger:
			cur.rules = append(cur.rules, def)
		case defReads:
			at, err := declaredAt(def)
			if err != nil {
				return nil, err
			}
			switch {
			case at == "" && flagAt == "":
				return nil, fmt.Errorf("a package declaration says what a package READS, so it " +
					"needs to say which one: give it `at: <address>`, or install the file with " +
					"`--at <address>`")
			case at == "":
				at = flagAt
			case flagAt != "" && at != flagAt:
				return nil, fmt.Errorf("the file installs a package at %q and --at says %q: the "+
					"declaration is the one that travels with the charter, so drop the flag", at, flagAt)
			}
			if seen[at] {
				return nil, fmt.Errorf("the file declares %q twice: `reads:` is what the PACKAGE "+
					"reads, and two declarations have no order that would settle which wins", at)
			}
			seen[at] = true
			// A group opened before any definitions is the FIRST group rather than a second
			// one, so a file that starts with its declaration does not install an empty charter.
			if cur.reads != nil || len(cur.strata) > 0 || len(cur.rules) > 0 {
				out = append(out, cur)
			}
			cur = charterGroup{at: at, reads: def}
		default:
			return nil, fmt.Errorf("a definition is %s: a trigger has a `condition`, a stratum "+
				"has a `level` and a `match`, a package declaration has `reads`", defUnknown)
		}
	}
	if cur.reads != nil || len(cur.strata) > 0 || len(cur.rules) > 0 {
		out = append(out, cur)
	}
	return out, nil
}

// declaredAt reads the `at:` a package declaration carries, empty when it names none.
func declaredAt(def *ir.Node) (string, error) {
	n := codec.Unwrap(def)
	if n == nil || n.Type != ir.ObjectType {
		return "", nil
	}
	for i, key := range n.Fields {
		if key == nil || key.Type != ir.StringType || key.String != "at" {
			continue
		}
		v := codec.Unwrap(n.Values[i])
		if v == nil || v.Type != ir.StringType || v.String == "" {
			return "", fmt.Errorf("a package declaration's `at:` is an address (`drift`, `demo.pr`)")
		}
		return v.String, nil
	}
	return "", nil
}
