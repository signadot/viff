package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/entity"

	"github.com/signadot/verse/adminface"
	"github.com/signadot/verse/codec"
)

// The help lists a command by the FIRST WORD OF ITS SYNOPSIS — cli's usage.go derives the
// name column from the prose rather than from cmd.Name. So a synopsis that opens with a
// description instead of the command's own name makes `verse <group>` advertise a command
// that does not exist: `verse flow` listed hold/release/stop/inject, and typing any of them
// got "no such command: hold". Nothing else enforces this, so walk the whole tree.
func TestEverySynopsisStartsWithItsCommandName(t *testing.T) {
	var walk func(path string, cmds []*cli.Command)
	walk = func(path string, cmds []*cli.Command) {
		for _, c := range cmds {
			first, _, _ := strings.Cut(c.Synopsis, " ")
			if first != c.Name {
				t.Errorf("%s%s: help lists it as %q — synopsis %q must start with %q",
					path, c.Name, first, c.Synopsis, c.Name)
			}
			walk(path+c.Name+" ", c.Children)
		}
	}
	walk("verse ", rootCmd().Children)
}

// --ifmt says how to READ what the operator typed. It was also used to RENDER the request
// body, and verse renders no json — so `--ifmt json`, which the tutorial recommends for
// pasting JSON, failed every command that sends a body (every write) with a complaint
// about rendering json to someone who had only asked to type it.
//
// The invariant is the fix, not the fallback: whatever --ifmt says, the wire format is one
// verse can actually write.
func TestWireFmtIsAlwaysWritable(t *testing.T) {
	defer func(f string) { g.IFmt = f }(g.IFmt)

	for _, in := range []string{"", "tony", "json", "yaml", "nonsense"} {
		g.IFmt = in
		if got := wireFmt(); !codec.Writable(got) {
			t.Errorf("--ifmt %q ⇒ wire format %v, which verse cannot render", in, got)
		}
	}
}

// A writable --ifmt is still honored on the wire: only the unwritable one is substituted,
// so the fix does not quietly flatten everyone's requests to one dialect.
func TestWireFmtKeepsAWritableInputFormat(t *testing.T) {
	defer func(f string) { g.IFmt = f }(g.IFmt)

	g.IFmt = "yaml"
	if got := wireFmt(); got != codec.YAML {
		t.Errorf("--ifmt yaml ⇒ wire format %v, want yaml", got)
	}
}

// collections pairs each collection's name — the SINGULAR, which is the command — with the
// plural that is merely an alias of it.
//
// `state` is deliberately absent: it is not a collection. It answers ONE document at a path
// rather than a list of things, so there is no plural to alias and the positional is an address
// rather than a verb it must refuse. See TestStateTakesAPath.
var collections = map[string]string{
	"gate": "gates", "task": "tasks",
	"flow": "flows", "charter": "charters",
	"resolver": "resolvers", "runner": "runners", "source": "sources",
}

// A collection is ONE command, named in the singular, and bare it lists. The plural is an
// alias of that same command rather than a second one: two commands for one collection put
// two names in the help and made every synopsis spend its tail pointing at the other.
//
// `<name> list` must be gone in both spellings — a listing that is also reachable as a verb
// is the two-ways-to-list this removed.
func TestCollectionsAreOneSingularCommand(t *testing.T) {
	root := rootCmd()
	cc := cli.DefaultContext()
	for singular, plural := range collections {
		s := root.FindSub(cc, singular)
		if s == nil {
			t.Errorf("verse %s: no such command", singular)
			continue
		}
		if s.Name != singular {
			t.Errorf("verse %s resolves to %q — the command is the singular", singular, s.Name)
		}
		// A group with no Run of its own answers `verse gate` with "no command provided".
		if s.Hooks.Run == nil {
			t.Errorf("verse %s: no listing — bare it must list", singular)
		}
		if got := root.FindSub(cc, plural); got != s {
			t.Errorf("verse %s resolves to %v, want the same command as verse %s", plural, got, singular)
		}
		if s.FindSub(cc, "list") != nil {
			t.Errorf("verse %s list still exists — bare `verse %s` is the listing", singular, singular)
		}
	}
	// The two short spellings are aliases of the same commands, so they list too. `ps` could
	// not be one while `task` was a verbs-only group: `verse ps` was "no command provided".
	for alias, name := range map[string]string{"ps": "task"} {
		if root.FindSub(cc, alias) != root.FindSub(cc, name) {
			t.Errorf("verse %s does not resolve to verse %s", alias, name)
		}
	}
}

// A listing that IGNORES positionals turns a wrong command into a plausible answer:
// `verse c -a-it review` would print the charter rather than say the verb is not one.
// `list` is called out by name, because it is the mistake a bare-listing collection invites
// and it must not become a second way to list.
func TestCollectionsRefuseAVerbTheyDoNotHave(t *testing.T) {
	root := rootCmd()
	cc := cli.DefaultContext()
	for singular := range collections {
		// `charter` takes an ADDRESS where the others take a verb or nothing (`verse charter
		// verse-dev drift`), so a word it does not know is a place with no charter rather than
		// a verb it does not have — the same trade `verse data` makes, and the reason it is
		// not in this list either. Its own refusals are tested at TestTheCharterListsAsATree.
		if singular == "charter" {
			continue
		}
		cmd := root.FindSub(cc, singular)
		if cmd == nil {
			t.Fatalf("verse %s: no such command", singular)
		}
		// No server is running: a command that refuses the argument fails BEFORE dialing, so
		// a usage error is proof the guard fired rather than the request.
		for _, args := range [][]string{{"no-such-verb", "ship-it"}, {"list"}} {
			err := cmd.Hooks.Run(cc, args)
			if !errors.Is(err, cli.ErrUsage) {
				t.Errorf("verse %s %s: error = %v, want a usage error", singular, strings.Join(args, " "), err)
			}
		}
		if err := cmd.Hooks.Run(cc, []string{"list"}); err == nil || !strings.Contains(err.Error(), "verse "+singular) {
			t.Errorf("verse %s list: error = %v, want it to name `verse %s`", singular, err, singular)
		}
	}
}

// A gate is answered by a VERB — `gate approve <id>` — so the two states that are not a
// decision (neither flag, both flags) are unrepresentable rather than validated. Each verb
// carries the short forms the interactive prompt already teaches (`approve? [y/N]`), and
// --say/-s rides along on both, since "no, because X" is worth as much as "yes, but".
func TestGateAndTaskAnswerVerbs(t *testing.T) {
	root := rootCmd()
	for _, c := range []struct{ group, verb string }{
		{"gate", "approve"},
		{"gate", "deny"},
		{"task", "approve"},
		{"task", "deny"},
	} {
		group := root.FindSub(cli.DefaultContext(), c.group)
		if group == nil {
			t.Fatalf("no %q command", c.group)
		}
		cmd := group.FindSub(cli.DefaultContext(), c.verb)
		if cmd == nil {
			t.Errorf("%s %s: no such command", c.group, c.verb)
			continue
		}
		// The short forms must resolve to the SAME command, not merely exist.
		for _, alias := range map[string][]string{
			"approve": {"y", "yes"}, "deny": {"n", "no"},
		}[c.verb] {
			if got := group.FindSub(cli.DefaultContext(), alias); got != cmd {
				t.Errorf("%s %s: alias %q resolves to %v, want the %s command",
					c.group, c.verb, alias, got, c.verb)
			}
		}
		if opts := cmd.AllOpts(); opts["say"] == nil || opts["s"] != opts["say"] {
			t.Errorf("%s %s: want --say with the -s alias", c.group, c.verb)
		}
		// The flag pair is gone: a verb that also took --approve could be told to deny.
		for _, gone := range []string{"approve", "deny"} {
			if cmd.AllOpts()[gone] != nil {
				t.Errorf("%s %s: --%s still exists as a flag", c.group, c.verb, gone)
			}
		}
	}
	// `resolve` was replaced, not kept alongside: two ways to answer a gate is the thing
	// this removed.
	for _, group := range []string{"gate", "task"} {
		g := root.FindSub(cli.DefaultContext(), group)
		if g.FindSub(cli.DefaultContext(), "resolve") != nil {
			t.Errorf("%s resolve still exists", group)
		}
	}
}

// Every flag combination `verse up` cannot honor must be refused from the flags ALONE —
// before a docd is spawned or a store opened. These checks used to sit past that point, so
// an impossible command spent forty seconds launching daemons to reach its own rejection.
func TestUpValidateRefusesContradictions(t *testing.T) {
	cases := map[string]upConfig{
		"--data with --docd-addr": {DocdAddr: "h:1", Data: "/tmp/x"},
		"mount face with no docd": {DocdMountAddr: "h:2"},
		"two runners":             {Runner: "http://x", Broker: true},
		"broker with no face":     {Broker: true},
	}
	for name, cfg := range cases {
		if err := cfg.validate(); !errors.Is(err, cli.ErrUsage) {
			t.Errorf("%s: validate() = %v, want a usage error", name, err)
		}
	}

	ok := map[string]upConfig{
		"defaults":           {},
		"running docd":       {DocdAddr: "h:1", DocdMountAddr: "h:2"},
		"data dir":           {Data: "/tmp/x"},
		"broker with a face": {Broker: true, BrokerAddr: ":9090"},
		"broker face only":   {BrokerAddr: ":9090"},
		"remote runner":      {Runner: "http://x"},
	}
	for name, cfg := range ok {
		if err := cfg.validate(); err != nil {
			t.Errorf("%s: validate() = %v, want nil", name, err)
		}
	}
}

// `verse data` takes an ADDRESS, which is what makes it not a collection.
//
// Every other listing refuses a positional, because there a positional could only be a verb it
// does not have. Here it is the whole point: one kinded path, asked at whatever depth, in place
// of the `--system`/`--kind` pair that could only ever ask at two.
func TestStateTakesAPath(t *testing.T) {
	cmd := rootCmd().FindSub(cli.DefaultContext(), "data")
	if cmd == nil {
		t.Fatal("verse data: no such command")
	}
	if cmd.Hooks.Run == nil {
		t.Fatal("verse data: no run — bare it answers the whole verse")
	}
	// EVERY POSITIONAL IS A STEP, so several of them are one address rather than an error —
	// which is what this used to assert, back when the positional was one token. No server is
	// running, so a well-formed address fails at the DIAL, and that is how this tells an
	// accepted address from a refused one.
	if err := cmd.Hooks.Run(cli.DefaultContext(), []string{"demo", "pr", "and-another"}); errors.Is(err, cli.ErrUsage) {
		t.Errorf("three steps gave %v — they are one address", err)
	}
	// And there is no such thing as a MALFORMED address any more: a step is a name, so a word
	// that used to be an unparseable path is now simply a system called that.
	if err := cmd.Hooks.Run(cli.DefaultContext(), []string{"demo.pr["}); errors.Is(err, cli.ErrUsage) {
		t.Errorf("a step holding punctuation gave %v — a step is a name", err)
	}
	// An EMPTY step is refused, because it names nothing and would vanish from the line.
	if err := cmd.Hooks.Run(cli.DefaultContext(), []string{"demo", "", "1"}); !errors.Is(err, cli.ErrUsage) {
		t.Errorf("an empty step gave %v, want a usage error", err)
	}
}

// A flag may follow the path, which takes a second Parse to pick up: a command with
// subcommands stops at its first positional, and `state`'s positional is an address rather than
// a verb, so nothing after it belongs to anyone else.
//
// It is asked of a ROOT option as well as one of state's own, because that is the case that
// prompted it — `verse data lineage --scope m` — and the second Parse runs up the command tree
// exactly as the first did.
func TestStateTakesFlagsAfterThePath(t *testing.T) {
	// The flags under test are GLOBAL ones, so parsing them writes to g — restored here, or
	// every test after this one runs in scope "m" rendering yaml.
	defer func(addr, scope string) { g.Addr, g.Scope = addr, scope }(g.Addr, g.Scope)
	defer func(f string) { g.OFmt = f }(g.OFmt)

	cmd := rootCmd().FindSub(cli.DefaultContext(), "data")
	for _, args := range [][]string{
		{"demo", "pr", "--paths"},
		{"--paths", "demo", "pr"},
		{"demo", "pr", "--if", "{stage: open}"},
		{"demo", "pr", "--scope", "m"},
		{"demo", "pr", "-o", "yaml"},
	} {
		// No server is running, so reaching the dial is proof the parse got that far: a usage
		// error would mean it did not.
		if err := cmd.Hooks.Run(cli.DefaultContext(), args); errors.Is(err, cli.ErrUsage) {
			t.Errorf("verse data %v: %v — the flag did not parse", args, err)
		}
	}
	// THERE IS NO "SECOND PATH" ANY MORE, and that follows from every positional being a step:
	// the parser takes the flags wherever they sit and hands over what is left, so `demo pr`
	// with a flag between them is the two-step address it reads as. It used to be an error,
	// back when the positional was one token and a second one could only be a mistake.
	if err := cmd.Hooks.Run(cli.DefaultContext(), []string{"demo", "--paths", "pr"}); errors.Is(err, cli.ErrUsage) {
		t.Errorf("steps either side of a flag gave %v — they are one address", err)
	}
}

// AN ADDRESS IS STEPS, one segment per word, and `--paths` answers the same way — so a listing
// pipes back into a read without anybody quoting anything.
//
// What is asserted is the ROUND TRIP: the words go in, the path comes out, and parsing it back
// yields the same words. How each is quoted along the way is kpath's business and pinning it
// here would be pinning somebody else's decision.
func TestAnAddressIsItsSteps(t *testing.T) {
	for _, steps := range [][]string{
		{"demo", "pr", "1"},
		{"demo", "pr", "a.b"},
		{"demo", "pr", "a.b", "labels", "urgent"},
		{"git", "ref", "x@git:ref:y"},
		// The system holding a colon — which the one-token spelling could not say at all.
		{"a:b:c", "pr", "1"},
		{"verse", "task", "instance", "run-1"},
	} {
		segs, err := address(steps)
		if err != nil {
			t.Errorf("address(%q): %v", steps, err)
			continue
		}
		p, err := entity.PathOf(segs)
		if err != nil {
			t.Errorf("address(%q) = %q, which does not name a path: %v", steps, segs, err)
			continue
		}
		got := append(p.Ref.Segs(), p.Field...)
		if strings.Join(got, "\x00") != strings.Join(steps, "\x00") {
			t.Errorf("address(%q) → %q, want the steps back", steps, got)
		}
	}
	// ONE WORD IS ONE SEGMENT, whatever is in it. The token spellings are not read as addresses
	// any more: `demo.pr.1` names a system called `demo.pr.1`, which is what the step form MEANS
	// and is why it is not ambiguous.
	for _, in := range []string{"demo.pr.1", "demo:pr:1"} {
		got, err := address([]string{in})
		if err != nil || len(got) != 1 || got[0] != in {
			t.Errorf("address([%q]) = (%q, %v), want it whole as one step", in, got, err)
		}
	}
	if got, err := address(nil); err != nil || len(got) != 0 {
		t.Errorf("address(nil) = (%q, %v), want no steps", got, err)
	}
}

// A REF IS TAKEN WHERE IT IS MARKED, which is the escape hatch for an address copied out of a
// document (`subject: demo:pr:1`). It is unambiguous for the reason the positional is not: a
// flag is a different PLACE on the line, not a second reading of the same word.
func TestARefIsTakenAsAFlag(t *testing.T) {
	got, err := addressOf(nil, "demo:pr:1", "")
	if err != nil || strings.Join(got, " ") != "demo pr 1" {
		t.Errorf("--ref demo:pr:1 = (%q, %v), want the three steps", got, err)
	}
	if _, err := addressOf([]string{"demo", "pr", "1"}, "demo:pr:1", ""); !errors.Is(err, cli.ErrUsage) {
		t.Error("--ref beside positional steps names the address twice and must be refused")
	}
	if _, err := addressOf(nil, "not-a-ref", ""); !errors.Is(err, cli.ErrUsage) {
		t.Error("--ref takes a ref, and a word that is not one is a usage error")
	}
}

// A PATH IS TAKEN WHERE IT IS MARKED TOO, and it is the one spelling that carries a WILDCARD:
// steps cannot, since a `*` step is the id that IS an asterisk. `--at` declares the grammar, so
// the `*` is read as the selector it is rather than guessed at.
func TestAPathIsTakenAsAFlag(t *testing.T) {
	got, err := addressWire(nil, "", `demo.pr.*.stage`)
	if err != nil || got != `demo.pr.*.stage` {
		t.Errorf("--at = (%q, %v), want it through verbatim — the wildcard is the point", got, err)
	}
	// As SEGMENTS it is refused, which is right: that view is for a write, and a wildcard
	// names several places where a write names one.
	if _, err := addressOf(nil, "", `demo.pr.*.stage`); !errors.Is(err, cli.ErrUsage) {
		t.Error("a wildcard reached the segment view, which a write would then use")
	}
	// And the three spellings are exclusive.
	if _, err := addressOf([]string{"demo"}, "", "demo.pr.1"); !errors.Is(err, cli.ErrUsage) {
		t.Error("steps beside --at name the address twice and must be refused")
	}
	if _, err := addressOf(nil, "demo:pr:1", "demo.pr.1"); !errors.Is(err, cli.ErrUsage) {
		t.Error("--ref beside --at name the address twice and must be refused")
	}
}

// A command that takes ONE token refuses a second rather than answering about args[0].
//
// `verse data` takes an address as separate steps and these take one, so the spelling that
// works next door is the one somebody types here — and `verse flow show demo seq 1` used to
// send the subject `demo` and answer, truthfully, with every flow in the system. A wrong answer
// that looks right is the failure mode worth a test.
func TestOneTokenCommandsRefuseASecondPositional(t *testing.T) {
	serve(t)

	for _, argv := range [][]string{
		{"flow", "show", "demo", "seq", "1"},
		{"flow", "pause", "demo", "seq", "1"},
		{"trigger", "show", "a", "b"},
		{"stratum", "remove", "a", "b"},
		{"runner", "show", "pool", "code"},
		{"source", "show", "git", "verse"},
		{"task", "get", "run-1", "extra"},
	} {
		out, err := runCLI(t, argv...)
		if err == nil {
			t.Errorf("%v was accepted, answering about %q alone:\n%s", argv, argv[2], out)
		}
	}

	// And one token still works, which is the half that must not break.
	if _, err := runCLI(t, "flow", "show", "demo:seq:1"); err != nil {
		t.Errorf("one token was refused: %v", err)
	}
}

// `verse charter` answers the charter as a TREE — baseline at the root and each package under
// the address it writes in — which is what makes an installed package visible at all. It listed
// one engine's rules before, so installing comment-drift (two packages, eight rules) and typing
// `verse charter` answered `[]`, which reads as a failed install and did, twice.
func TestTheCharterListsAsATree(t *testing.T) {
	serve(t)
	if err := installPackageRule(t); err != nil {
		t.Fatalf("installing a package: %v", err)
	}

	out, err := runCLI(t, "charter")
	if err != nil {
		t.Fatalf("charter: %v", err)
	}
	for _, want := range []string{
		"writes: anywhere", // baseline, at the root
		"children:",
		"notes:",         // the package, under the address it writes in
		"writes: notes.", // …and what it may write
		"name: note",     // its rule, in full
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the tree does not show %q:\n%s", want, out)
		}
	}

	// The address reaches one charter, at any depth, and answers the same shape there.
	one, err := runCLI(t, "charter", "notes")
	if err != nil {
		t.Fatalf("charter notes: %v", err)
	}
	if !strings.Contains(one, "writes: notes.") || !strings.Contains(one, "name: note") {
		t.Errorf("the address did not reach the package: %s", one)
	}
	if strings.Contains(one, "anywhere") {
		t.Errorf("the address answered about baseline too: %s", one)
	}

	// An address nobody installed at is an error rather than an empty document: it is a
	// question about a place, and "nothing is there" is worth saying out loud.
	if _, err := runCLI(t, "charter", "nowhere"); err == nil {
		t.Error("an address with no charter answered as though it had one")
	}
}

// installPackageRule installs one rule into a package, by posting it the way `trigger add` does.
func installPackageRule(t *testing.T) error { return installPackageRuleAt(t, "notes") }

func installPackageRuleAt(t *testing.T, at string) error {
	t.Helper()
	g.Addr = testAddr // runCLI does this on its way in; this posts before any command runs
	node, err := codec.ParseNode([]byte(`{
		name: note,
		condition: {subject: {system: `+at+`, kind: seen, id: head}},
		action: {patch: {saw: "yes"}},
	}`), codec.Tony)
	if err != nil {
		return err
	}
	resp, err := httpPostNodeTo("/charter", url.Values{"at": []string{at}}, node)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// A rule in a PACKAGE is reachable by its PATH, and two charters holding one name is no longer
// a question anybody has to settle — which is the point of the path.
//
// A name is unique WITHIN a charter and not across them, which is most of what packages are for.
// `show` answered that by SEARCHING: one charter holding the name was answered, several was a
// question it asked back with `--at`. A path says which charter in the same breath as the name,
// so there is nothing to ask — and when somebody reaches into the wrong one, the search survives
// as the refusal, naming where the name actually is.
func TestARuleInAPackageIsReachableByItsPath(t *testing.T) {
	serve(t)
	if err := installPackageRule(t); err != nil {
		t.Fatalf("installing a package: %v", err)
	}

	out, err := runCLI(t, "charter", "notes", "rules", "note")
	if err != nil {
		t.Fatalf("charter path: %v", err)
	}
	if !strings.Contains(out, "name: note") {
		t.Errorf("the path did not reach the package's rule: %s", out)
	}

	// A second charter with a rule of the same name settles nothing and asks nothing: each
	// path names its own.
	if err := installPackageRuleAt(t, "other"); err != nil {
		t.Fatalf("installing a second package: %v", err)
	}
	for _, at := range []string{"notes", "other"} {
		if out, err := runCLI(t, "charter", at, "rules", "note"); err != nil {
			t.Errorf("%s rules note: %v (%s)", at, err, out)
		} else if !strings.Contains(out, "name: note") {
			t.Errorf("%s rules note did not answer with the rule: %s", at, out)
		}
	}
	// …and the path that reaches for it where it is not says where it is.
	_, err = runCLI(t, "charter", "rules", "note")
	if err == nil {
		t.Error("baseline answered for a rule only the packages hold")
	} else if !strings.Contains(err.Error(), "notes") || !strings.Contains(err.Error(), "other") {
		t.Errorf("the refusal does not name the charters that hold it: %v", err)
	}

	// And removing reaches it by the same path, which is the whole of `rm`'s argument.
	if _, err := runCLI(t, "charter", "-r", "notes", "rules", "note"); err != nil {
		t.Errorf("charter rm: %v", err)
	}
	if out, err := runCLI(t, "charter", "notes"); err == nil && strings.Contains(out, "name: note") {
		t.Errorf("the rule is still installed: %s", out)
	}
}

// THERE IS NO VERB TO COLLIDE WITH, which is what saying the action in a FLAG buys.
//
// A package's address is a system name and `add`, `remove` and `rm` are ordinary words, so
// `verse c rm` had to be told apart from the package installed at `rm` — by a `--` the operator
// had to know existed, and which every command taking an address had to accept. With `-a`/`-r`
// there is nothing to tell apart: a positional is a path, always.
func TestAPackageAtAVerbLikeAddressNeedsNoEscape(t *testing.T) {
	serve(t)
	if err := installPackageRuleAt(t, "rm"); err != nil {
		t.Fatalf("installing at `rm`: %v", err)
	}
	out, err := runCLI(t, "charter", "rm")
	if err != nil {
		t.Fatalf("charter rm (the package at `rm`): %v", err)
	}
	if !strings.Contains(out, "writes: rm.") {
		t.Errorf("the package at `rm` was not reached: %s", out)
	}
}

// `--` KEEPS ONE JOB: it says where the ADDRESS ends, for the one case the server's greedy split
// cannot work out — a package whose own address ends in `rules` or `strata`, which shadows that
// key of the address above it.
func TestADashDashSaysWhereTheAddressEnds(t *testing.T) {
	serve(t)
	// A charter AT `shadow` too: a place holds no keys of its own, so `shadow -- rules` has
	// something to reach only when a charter is installed there.
	g.Addr = testAddr
	above, err := codec.ParseNode([]byte(`{
		name: above,
		condition: {subject: {system: shadow, kind: x, id: head}},
		action: {patch: {saw: "yes"}},
	}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := httpPostNodeTo("/charter", url.Values{"at": []string{"shadow"}}, above); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != 201 {
			t.Fatalf("installing at `shadow`: %s", resp.Status)
		}
	}
	// A rule whose subject is inside the two-segment address, since a package writes only there.
	node, err := codec.ParseNode([]byte(`{
		name: note,
		condition: {subject: {system: shadow, kind: rules, id: head}},
		action: {patch: {saw: "yes"}},
	}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := httpPostNodeTo("/charter", url.Values{"at": []string{"shadow.rules"}}, node)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("installing at `shadow.rules`: %s", resp.Status)
	}
	// Greedy: the package is a longer place than `shadow`, so it wins.
	out, err := runCLI(t, "charter", "shadow.rules")
	if err != nil {
		t.Fatalf("charter shadow.rules: %v", err)
	}
	if !strings.Contains(out, "writes: shadow.rules.") {
		t.Errorf("the package was not reached: %s", out)
	}
	// …and `--` reaches the KEY of the address above it instead.
	out, err = runCLI(t, "charter", "shadow", "--", "rules")
	if err != nil {
		t.Fatalf("charter shadow -- rules: %v", err)
	}
	if strings.Contains(out, "shadowed") {
		t.Errorf("`--` answered the package at shadow.rules rather than shadow's own key: %s", out)
	}
	if !strings.Contains(out, "above") {
		t.Errorf("`--` did not answer shadow's rules: %s", out)
	}
}

// `--paths` PRINTS WHAT PIPES BACK, one line per rule and level, as `verse data --paths` does
// and for the same reason: a document cannot be fed to the command that answered it.
//
// AS STEPS, so nothing in the listing needs quoting to be usable, and each line is a path this
// same command takes — to show one, or to remove it.
func TestCharterPathsPrintsWhatPipesBack(t *testing.T) {
	serve(t)
	if err := installPackageRuleAt(t, "notes"); err != nil {
		t.Fatalf("installing a package: %v", err)
	}
	out, err := runCLI(t, "charter", "--paths")
	if err != nil {
		t.Fatalf("charter --paths: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		t.Fatal("--paths printed nothing for an installed package")
	}
	want := "notes rules note"
	if lines[0] != want {
		t.Fatalf("--paths printed %q, want %q", lines[0], want)
	}
	// The line IS a path, in the STEPS this command takes: the shell splits it into arguments,
	// which is what `xargs -L1 verse c` does with it.
	back, err := runCLI(t, append([]string{"charter"}, strings.Fields(lines[0])...)...)
	if err != nil {
		t.Fatalf("the printed path did not pipe back: %v", err)
	}
	if !strings.Contains(back, "name: note") {
		t.Errorf("the path answered something else:\n%s", back)
	}
}

// A PACKAGE THAT HOLDS NOTHING YET PRINTS ITSELF. A listing of rules alone would hide the one
// state somebody most needs to see: a package whose declaration installed and whose rules were
// refused is still a package, and one that lists as nothing is one nobody can see to take down.
func TestCharterPathsShowsAPackageWithNothingInIt(t *testing.T) {
	serve(t)
	g.Addr = testAddr
	node, err := codec.ParseNode([]byte(`{at: notes, reads: [demo.pr]}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := httpPostNodeTo("/charter", url.Values{"at": []string{"notes"}}, node)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("installing a declaration: %s", resp.Status)
	}
	out, err := runCLI(t, "charter", "--paths")
	if err != nil {
		t.Fatalf("charter --paths: %v", err)
	}
	if strings.TrimSpace(out) != "notes" {
		t.Errorf("--paths = %q, want the package that holds nothing yet", strings.TrimSpace(out))
	}
}

// A DERIVED ADMIN ADDRESS YIELDS TO A PORT SOMEBODY ASKED FOR BY NAME.
//
// The admin face derives to the API port plus one and starts before anything that can wedge —
// which means before the broker face. `--broker-addr` is very often that same next port, so the
// derived face won the bind and the named one lost it, silently: the log said "address already
// in use" and every spawn got a dialURL that answered 404. Found by a harness that asked the
// kernel for two ports and got consecutive ones.
func TestADerivedAdminAddressYieldsToTheBroker(t *testing.T) {
	for _, c := range []struct{ explicit, api, broker, want string }{
		{"", "localhost:9797", "", ""},                                           // nothing asked for: derive
		{"", "localhost:9797", "localhost:9090", ""},                             // a broker elsewhere: derive
		{"", "localhost:9797", "localhost:9798", adminface.Off},                  // …the derived port itself: yield
		{"localhost:9999", "localhost:9797", "localhost:9798", "localhost:9999"}, // explicit wins over both
		{"", "127.0.0.1:55727", "127.0.0.1:55728", adminface.Off},                // the consecutive-port case
	} {
		if got := adminAddrFor(c.explicit, c.api, c.broker); got != c.want {
			t.Errorf("adminAddrFor(%q, %q, %q) = %q, want %q", c.explicit, c.api, c.broker, got, c.want)
		}
	}
}
