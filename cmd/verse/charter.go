package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/scott-cotton/cli"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/charterfile"
	"github.com/signadot/verse/pkg/client"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
)

// THE CHARTER IS ONE COMMAND, because it is one thing: rules and levels are installed from one
// file, confined by one address, and they come and go together. `verse charter` and `verse
// stratum` were two commands each answering a slice of it, and neither could show a charter.
//
//	verse c                                 baseline's charter, as its file; stderr names the rest
//	verse c verse-dev drift                 …the one installed there, as its file
//	verse c verse-dev -d <dir>              …every charter at or under it, a file apiece
//	verse c verse-dev drift rules           …its rules
//	verse c verse-dev drift rules watcher   …one of them, and `… watcher action` reaches in
//	verse c rules watcher                   baseline's, which has no address
//	verse c --depth 0                       this address alone
//	verse c -a demo pr -f <file>            install a charter there (or baseline, with no path)
//	verse c -a -d <dir>                     install every charter file in a directory, as one step
//	verse c -a demo -d <dir> --prune        …and remove what it leaves out at and under demo
//	verse c --check demo -d <dir>           whether that install would go, and what it would change
//	verse c --check demo -d <dir> --runners-to-come   …holding a spawn no runner offers yet
//	verse c -r verse-dev drift              take that package down whole
//	verse c -r verse-dev drift rules watcher   …or one rule out of it
//	verse c -R verse-dev                    …or a place and everything under it
//
// ONE PATH, and `show` is gone because of it: a path that reaches a rule is the same path that
// reaches the charter holding it, one step longer. What `show` had that a path does not is the
// SEARCH — it looked a name up in every charter — and that survives where it was useful, in the
// refusal: the server names the charter that holds it and gives the path that reaches it.
//
// The tree is the SERVER's (internal/versed/charter.go); this end asks for an address and a depth and
// renders what comes back as charter files (charterfile.go), which is what keeps the two from
// disagreeing about shape.

// WHAT TO DO IS A FLAG, and the positional is a path in every case. That is what retires the
// verb/address collision rather than escaping it: `add`, `rm` and `uninstall` were ordinary
// words, and a package's address is a system name, so `verse c rm` had to be told apart from the
// package at `rm` by a `--` the operator had to know about. There is nothing to tell apart now.
//
// It also retires two meanings of `--at`. Installing took `--at <address>`; reading took `--at`
// to say where an address ended. Both are the positional path now: `verse c -a demo pr -f f` is
// where to install, and `verse c verse-dev -- rules` is where the address ends.
type charterFlags struct {
	// Depth is how far under the address to go: 0 is that address alone, 1 adds its immediate
	// children, and -1 (the default) is everything under it. A charter is a program, and
	// printing all of one is often not the question.
	Depth int `cli:"name=depth desc='how far under the address to go: 0 is it alone, -1 (default) is everything'"`
	// Add installs; the path is where, and -f is what.
	Add bool `cli:"name=add aliases=a desc='install a charter (--file, or stdin) at the path'"`
	// Rm removes what the path names: a package at an address, or one rule or level in it.
	Rm bool `cli:"name=rm aliases=r desc='remove what the path names (a package, or a rule|level in one)'"`
	// RmR is Rm over a place and everything under it.
	//
	// A SEPARATE LETTER rather than `-r -R`, because the recursion is the dangerous half and
	// pairing them invites `-rR`. Note it is NOT the `-r` of `rm -r`: here `-r` is the removal
	// and `-R` is the removal that reaches under an address. A hand that types `-r` expecting
	// `rm`'s recursion gets the narrower thing and a refusal naming what is under it, which is
	// the direction that error should fail in.
	RmR bool `cli:"name=rm-recursive aliases=R desc='remove the charter at the path AND every charter under it'"`
	// File is what an install reads. `-f -` is stdin, which is also the default when nothing
	// says otherwise.
	File string `cli:"name=file aliases=f desc='install from FILE (- = stdin); with --add'"`
	// Dir is a TREE of charters, a file apiece: with --add, every charter file under it is
	// installed at its own `at:`; without, every charter at or under the path is written there.
	Dir string `cli:"name=dir aliases=d desc='with --add, install every charter file under DIR; without, write each charter at or under the path to a file in DIR'"`
	// Prune makes `-a -d` a sync: what stands at and under the path afterwards is what the
	// directory holds. It is `-R` of what the directory leaves out, then `-a -d`
	// (internal/versed/charterprune.go).
	Prune bool `cli:"name=prune desc='with --add --dir, remove what the directory leaves out at and under the path'"`
	// Wait holds an install's answer until its rules are armed, or that long: an install answers
	// once the charters are accepted, and a rule's watch opens at the store's pace. Without it
	// the answer says which rules are still arming; with it, one still arming when the time is
	// up is answered with exit status 3 (internal/versed/charter.go, api.InstalledPackage).
	Wait string `cli:"name=wait desc='with --add, hold the answer until the rules are armed, or this long (as 30s); exit 3 if one is still arming'"`
	// Check asks whether the charters of -d or -f would install at the path as `-a -d --prune`
	// installs them, and what would change, installing nothing (internal/versed/chartercheck.go). -f is a
	// directory holding that one file, the prune included. A refusal is its answer, as an
	// install's verdict, not an error.
	Check bool `cli:"name=check desc='with -d, or -f as a directory of that one file: whether the charters would install at the path as -a -d --prune would, and what would change; installs nothing'"`
	// RunnersToCome asks the check for the vet verse places a restored rule with: a spawn no
	// runner offers is held for a runner still to come, where an install refuses it. For a verse
	// whose runners are not up yet; without it the check answers as the install would.
	RunnersToCome bool `cli:"name=runners-to-come desc='with --check, hold a spawn no runner offers for a runner still to come, as verse does for a rule it restores, where an install refuses it'"`
	// Paths prints one path per line instead of the document, as `verse data --paths` does and
	// for the same reason: a document cannot be piped back into the command that answered it.
	Paths bool `cli:"name=paths desc='print one path per rule and level instead of the document, as steps'"`
	// KPaths is the same listing as one KINDED PATH per line — see the data command's flag of
	// the same name: steps are what this command takes and what `xargs -L1` hands back, and a
	// kinded path is what survives a shell variable as ONE argument.
	KPaths bool `cli:"name=kpaths desc='…the same listing as one kinded path per line'"`
}

var trig = &charterFlags{Depth: -1}

func charterCmd() *cli.Command {
	cfg := &struct{ *cli.Command }{}
	trig = &charterFlags{Depth: -1} // per-invocation, like the filter flags
	return cli.NewCommandAt(&cfg.Command, "charter").
		WithAliases("charters", "c").
		WithSynopsis("charter — the charter at an address (steps); -a install, -r remove, -R remove under").
		WithOpts(append(mustOpts(trig), filterOpts()...)...).
		WithRun(func(cc *cli.Context, args []string) error {
			args, err := cfg.Parse(cc, args)
			if err != nil {
				return err
			}
			// THE POSITIONALS ARE A PATH, whatever is being done with it: the address, then a
			// reach into what stands there (`verse-dev.drift rules watcher`), which is the
			// grammar `verse data` has. A flag among them ends it — which is what lets
			// `--depth`/`-r`/`--trim` sit on either side — and a bare `--` is not a flag: it
			// says where the ADDRESS ends, which is the one thing the server's split cannot
			// work out on its own.
			var steps []string
			steps, args = args, nil
			for i, a := range steps {
				if strings.HasPrefix(a, "-") && a != "--" {
					steps, args = steps[:i], steps[i:]
					break
				}
			}
			if args, err = cfg.Parse(cc, args); err != nil {
				return err
			}
			if len(args) > 0 {
				return fmt.Errorf("%w: charter takes one path (an address, then rules|strata|schedules "+
					"and a name), and %q is after it", cli.ErrUsage, args[0])
			}
			switch {
			case trig.Add && (trig.Rm || trig.RmR), trig.Rm && trig.RmR:
				return fmt.Errorf("%w: charter does one thing at a time: --add installs, "+
					"--rm removes, --rm-recursive removes what is under an address too",
					cli.ErrUsage)
			case trig.Add && trig.Dir != "" && trig.File != "":
				return fmt.Errorf("%w: charter -a takes a file (-f) or a directory (-d), not both",
					cli.ErrUsage)
			case trig.Prune && (!trig.Add || trig.Dir == ""):
				return fmt.Errorf("%w: --prune syncs an address to a directory: "+
					"`verse c -a <address> -d <dir> --prune`", cli.ErrUsage)
			case trig.RunnersToCome && !trig.Check:
				return fmt.Errorf("%w: --runners-to-come is a way of asking --check; an install "+
					"refuses a spawn no runner offers", cli.ErrUsage)
			case trig.Check && (trig.Add || trig.Rm || trig.RmR):
				return fmt.Errorf("%w: --check asks what an install would do, and does nothing; "+
					"it goes with no --add or --rm", cli.ErrUsage)
			case trig.Check:
				at, err := charterPath(steps)
				if err != nil {
					return err
				}
				if at == "" {
					return fmt.Errorf("%w: a check names the address the charters would be installed "+
						"at, as `-a <address> -d <dir> --prune` does", cli.ErrUsage)
				}
				docs, err := charterSource(cc, trig.File, trig.Dir)
				if err != nil {
					return fmt.Errorf("charter --check: %w", err)
				}
				resp, err := charterCheckVia(apiClient(), charterfile.CheckQuery(at, trig.RunnersToCome), docs)
				if err != nil {
					return err
				}
				return printBody(cc, resp)
			case trig.Add:
				at, err := charterPath(steps)
				if err != nil {
					return err
				}
				if trig.Dir != "" {
					return charterAddDir(cc, at, trig.Dir, trig.Prune)
				}
				return charterAdd(cc, at, trig.File)
			case trig.Rm, trig.RmR:
				return charterRm(cc, steps, trig.RmR)
			}
			if trig.File != "" {
				return fmt.Errorf("%w: --file is what an install reads; add --add to install it",
					cli.ErrUsage)
			}
			return printCharter(cc, steps)
		})
}

// printCharter asks for the charter at a path and prints what comes back.
//
// THE SPLIT IS THE SERVER'S — which of these steps are the address and which reach into the node
// standing there — because that is the end that knows the tree. This one sends what was typed.
//
// A `--` AMONG THE STEPS says where the address ends, for the one case the server's greedy split
// cannot get right on its own: a package whose address ends in `rules` or `strata` shadows that
// key of the address above it. `verse c verse-dev -- rules` is the key; `verse c verse-dev rules`
// is the package. It rides as `?at=`, which is what that query has always meant.
func printCharter(cc *cli.Context, steps []string) error {
	names, at, err := charterNames(steps)
	if err != nil {
		return err
	}
	q, full := charterfile.Query(names, at, trig.Depth)
	out, err := outFmt()
	if err != nil {
		return err
	}
	resp, err := httpGet("/charter", q)
	if err != nil {
		return fmt.Errorf("charter: %w", err)
	}
	// Either flag asks for the listing; --kpaths only says which spelling.
	if (trig.Paths || trig.KPaths) && resp.StatusCode < http.StatusBadRequest {
		return renderCharterPaths(cc.Out, resp, full, trig.KPaths)
	}
	node, err := charterNode(resp, out)
	if err != nil {
		return err
	}
	// A NODE OF THE TREE prints as the charter file it is (charterfile.go); a reach into one —
	// a rule, the list of them — prints as it stands, which is already what installs it.
	switch {
	case !charterfile.IsNode(node):
		return printIR(cc, node)
	case trig.Dir != "":
		return writeCharterDir(cc, node, full, trig.Dir)
	}
	f, err := readFilters()
	if err != nil {
		return err
	}
	return renderCharterFile(cc.Out, cc.Err, node, full, f, out, colorize(cc.Out), asSteps)
}

// charterNames is the steps typed as names, and how many of them are the address: those before a
// `--`, or -1 when no `--` says.
func charterNames(steps []string) ([]string, int, error) {
	addr, reach, split := cutAt(steps, "--")
	names, err := entity.StepNames(append(append([]string{}, addr...), reach...))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: charter: %s", cli.ErrUsage, err)
	}
	if !split {
		return names, -1, nil
	}
	return names, len(addr), nil
}

// charterNode reads a charter answer: the node, ready to render in out, or the server's refusal.
func charterNode(resp *http.Response, out codec.Format) (*ir.Node, error) {
	if resp.StatusCode == http.StatusNotFound {
		// The SERVER's words: it knows which half of the path failed and where else a name it
		// was given actually lives, and repeating a guess over the top of that would be this
		// end pretending to know the tree.
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("charter: %s", strings.TrimSpace(string(msg)))
	}
	defer resp.Body.Close()
	body, err := okBody(resp)
	if err != nil {
		return nil, fmt.Errorf("charter: %w", err)
	}
	node, err := codec.ParseNode(body, respFmt(resp))
	if err != nil {
		return nil, fmt.Errorf("charter: decoding the response: %w", err)
	}
	if out != respFmt(resp) {
		node = codec.StripLayout(node) // see printBody
	}
	return node, nil
}

// charterPath is a charter path's steps as the one token the server reads back with
// entity.PathSegs: the names they carry, joined by JoinSegs, which is that reader's inverse.
//
// STEPS, AS `verse data` READS THEM, so a word means the same thing on every command that takes
// a path: a word is one segment whatever is inside it, the quoted clause is a name, and an access
// or a wildcard is refused — a charter path names one place, made of names. The path is written
// `verse c verse-dev drift rules watcher`. A word holding a dot is one segment, and JoinSegs
// quotes it, so `verse c verse-dev.drift` asks about a place called `"verse-dev.drift"` and the
// server answers that nothing is installed there (versed TestOneSegmentReachesNoTwoSegmentPackage).
func charterPath(steps []string) (string, error) {
	names, err := entity.StepNames(steps)
	if err != nil {
		return "", fmt.Errorf("%w: charter: %s", cli.ErrUsage, err)
	}
	return entity.JoinSegs(names), nil
}

// renderCharterPaths writes what is installed, ONE PATH PER LINE, each of them a path this same
// command takes back:
//
//	rules base-rule                       baseline's, which has no address
//	verse-dev drift rules watcher
//	verse-dev drift strata base
//	notes                                 a package with a declaration and nothing in it yet
//
// AS STEPS, the same spelling `verse data --paths` answers with and the same one this command
// takes on the way in — so a line hands straight back:
//
//	verse c --paths | xargs -L1 verse c
//
// A NODE THAT HOLDS NOTHING PRINTS ITSELF, which is the case a listing of rules alone would
// hide: a package whose levels installed and whose rules were refused is still a package, and
// one that lists as nothing is one nobody can see to take down. That is the same reason
// packagesSeen reads all three kinds of record.
// stepsOf renders a charter address as the steps a command line takes, or as it stands when it
// is not a plain path.
func stepsOf(at string) string { return charterfile.Steps(at) }

func renderCharterPaths(w io.Writer, resp *http.Response, at string, kpaths bool) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("charter --paths: %w", err)
	}
	node, err := codec.ParseNode(body, respFmt(resp))
	if err != nil {
		return fmt.Errorf("charter --paths: decoding the response: %w", err)
	}
	paths, err := charterfile.Paths(node, at)
	if err != nil {
		return fmt.Errorf("charter --paths: %w", err)
	}
	for _, p := range paths {
		fmt.Fprintln(w, renderAddr(p, kpaths))
	}
	return nil
}

// cutAt splits a list of steps at the first sep, reporting whether there was one.
func cutAt(steps []string, sep string) (before, after []string, found bool) {
	for i, s := range steps {
		if s == sep {
			return steps[:i], steps[i+1:], true
		}
	}
	return steps, nil, false
}

// charterAdd installs one file: a charter (`at:`, `reads:`, `strata:`, `schedules:`, `rules:`),
// or the shorthand — a rule, a level, a list of them — at a path.
//
// THE PATH IS THE ADDRESS, which is what `--at` used to be. A charter installed at an address is
// a PACKAGE: it runs in an engine of its own and may write only inside that address. An empty
// path installs into the baseline charter, which may write anywhere (docs/trigger-model.md).
//
// The file is `--file`, or stdin. It is NOT the positional any more — `verse c add f.tony` used
// to read one — because the positional means a path everywhere else in this command, and a
// command whose argument changes meaning with a flag is the thing this whole grammar removes.
func charterAdd(cc *cli.Context, at, file string) error {
	// The file's suffix names its dialect; stdin and an extensionless file use --ifmt or
	// the configured default.
	data, srcFmt, err := codec.ReadSource(cc.In, file, inFmt())
	if err != nil {
		return fmt.Errorf("charter -a: %w", err)
	}
	name := file
	if name == "" || name == "-" {
		name = "stdin"
	}
	// ONE FILE, ONE CHARTER: a fielded document, or the shorthand — a rule, a level, a list of
	// them — installed at the path. Several charters are a directory (-d).
	doc, err := oneDocument(data, srcFmt, name)
	if err != nil {
		return fmt.Errorf("charter -a: %w", err)
	}
	defs := []*ir.Node{doc}
	if !charterfile.IsDoc(doc) {
		defs = charterfile.Defs(doc)
	}
	if len(defs) == 0 {
		return fmt.Errorf("charter -a: no trigger found in the input")
	}
	// ONE REQUEST, and the split is the server's, and so is the report: the one that prints is
	// the one that installed.
	q := url.Values{}
	if at != "" {
		q.Set("at", at)
	}
	return installReply(cc, "charter -a", q, defs)
}

// installReply installs defs as q says (`at`, or `prune`) and prints what went where. sending
// names the command in a failure to send (`charter -a -d`); a refusal is `charter -a`'s whatever
// the command, as it has always read.
func installReply(cc *cli.Context, sending string, q url.Values, defs []*ir.Node) error {
	if trig.Wait != "" {
		q.Set("wait", trig.Wait) // read by the server, the one reader of it (internal/versed/charter.go)
	}
	c := apiClient()
	resp, err := c.PostNodes("/charter", q, defs)
	if err != nil {
		return fmt.Errorf("%s: %w", sending, err)
	}
	arming, err := installAnswer(cc.Out, resp, c.Scope, asSteps)
	if err != nil {
		return fmt.Errorf("charter -a: %w", err)
	}
	// Waited for, and still arming when the time was up: said above, and said again as the
	// exit status, so a script can tell "not yet" from "refused" (1) without reading the lines.
	if arming && trig.Wait != "" {
		return cli.ExitCodeErr(3)
	}
	return nil
}

// installAnswer writes what an install answered to w, naming addresses as sp spells them, and
// says whether any rule is still arming; a refusal is the error, in the server's words.
func installAnswer(w io.Writer, resp *http.Response, scope string, sp spelling) (arming bool, err error) {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return false, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return reportInstalled(w, body, respFmt(resp), scope, sp), nil
}

// reportInstalled says what an install answered, as charterfile.Installed says it, and whether
// any rule is still arming.
func reportInstalled(w io.Writer, body []byte, format codec.Format, scope string, sp spelling) bool {
	node, err := codec.ParseNode(body, format)
	if err != nil {
		// Installed, and the report is unreadable: say the first and do not pretend the second.
		fmt.Fprintln(w, "installed")
		return false
	}
	return charterfile.Installed(w, node, scope, sp == asKPaths)
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
	case has("reads"), has("sends"), has("at"):
		return defReads
	}
	return defUnknown
}

// charterRm takes something out of the charter, and WHAT is said by the path rather than by a
// verb.
//
//	verse c -r verse-dev drift                take the package down whole
//	verse c -r verse-dev drift rules watcher  take one rule out of it
//	verse c -r verse-dev drift strata base    …or one level
//	verse c -r verse-dev drift schedules nightly   …or one schedule
//	verse c -r rules watcher                  baseline's, which has no address
//	verse c -R verse-dev                      a place, and every charter under it
//
// IT WAS TWO VERBS: `uninstall <address>` and `remove <name>`, which differ in what they remove
// rather than in the word, and whose arguments were different KINDS of thing — so knowing one
// told you nothing about the other. A path says which without being asked: it ends at an
// address, or it goes on to name something in the charter there.
//
// What a removal leaves standing is what the charter WROTE — a charter's writes are the world's.
// The server's removePackage says why at length.
func charterRm(cc *cli.Context, steps []string, recurse bool) error {
	if len(steps) == 0 {
		return fmt.Errorf("%w: -r wants a path — an address (`drift`, `demo pr`), or "+
			"one with `rules <name>`, `strata <name>` or `schedules <name>` after it", cli.ErrUsage)
	}
	names, at, err := charterNames(steps)
	if err != nil {
		return err
	}
	said, err := charterRmVia(apiClient(), names, at, recurse, asSteps)
	if err != nil {
		return fmt.Errorf("charter -r: %w", err)
	}
	fmt.Fprintln(cc.Out, said)
	return nil
}

// charterRmVia removes, through c, what a path of names reaches: a package, or a rule or level
// of one, and with recurse every charter at and under an address. at is how many of the names
// are the address, or -1 to leave that to the server. It answers what went, naming the address
// as sp spells it, or the refusal in the server's words.
func charterRmVia(c *client.Client, names []string, at int, recurse bool, sp spelling) (string, error) {
	q, _ := charterfile.Query(names, at, -1)
	if recurse {
		q.Set("recurse", "true")
	}
	resp, err := c.Delete("/charter", q)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		msg, _ := io.ReadAll(resp.Body)
		return "", errors.New(strings.TrimSpace(string(msg)))
	}
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return charterfile.Removed(names, at, c.Scope, recurse, sp == asKPaths), nil
}
