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
// `verse charter add-it review` would print the charter rather than say the verb is not one.
// `list` is called out by name, because it is the mistake a bare-listing collection invites
// and it must not become a second way to list.
func TestCollectionsRefuseAVerbTheyDoNotHave(t *testing.T) {
	root := rootCmd()
	cc := cli.DefaultContext()
	for singular := range collections {
		// `charter` takes an ADDRESS where the others take a verb or nothing (`verse charter
		// verse-dev drift`), so a word it does not know is a place with no charter rather than
		// a verb it does not have — the same trade `verse state` makes, and the reason it is
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

// `verse state` takes an ADDRESS, which is what makes it not a collection.
//
// Every other listing refuses a positional, because there a positional could only be a verb it
// does not have. Here it is the whole point: one kinded path, asked at whatever depth, in place
// of the `--system`/`--kind` pair that could only ever ask at two.
func TestStateTakesAPath(t *testing.T) {
	cmd := rootCmd().FindSub(cli.DefaultContext(), "state")
	if cmd == nil {
		t.Fatal("verse state: no such command")
	}
	if cmd.Hooks.Run == nil {
		t.Fatal("verse state: no run — bare it answers the whole verse")
	}
	// No server is running, so a path that PARSES fails at the dial and a path that does not
	// fails before it — which is how this tells the guard from the request.
	if err := cmd.Hooks.Run(cli.DefaultContext(), []string{"demo.pr", "and-another"}); !errors.Is(err, cli.ErrUsage) {
		t.Errorf("two positionals gave %v, want a usage error — state takes one path", err)
	}
	if err := cmd.Hooks.Run(cli.DefaultContext(), []string{"demo.pr["}); err == nil {
		t.Error("a malformed path was accepted")
	} else if errors.Is(err, cli.ErrUsage) == false && !strings.Contains(err.Error(), "path") {
		t.Errorf("a malformed path gave %v, want it named as a path", err)
	}
}

// A flag may follow the path, which takes a second Parse to pick up: a command with
// subcommands stops at its first positional, and `state`'s positional is an address rather than
// a verb, so nothing after it belongs to anyone else.
//
// It is asked of a ROOT option as well as one of state's own, because that is the case that
// prompted it — `verse state lineage --scope m` — and the second Parse runs up the command tree
// exactly as the first did.
func TestStateTakesFlagsAfterThePath(t *testing.T) {
	// The flags under test are GLOBAL ones, so parsing them writes to g — restored here, or
	// every test after this one runs in scope "m" rendering yaml.
	defer func(addr, scope string) { g.Addr, g.Scope = addr, scope }(g.Addr, g.Scope)
	defer func(f string) { g.OFmt = f }(g.OFmt)

	cmd := rootCmd().FindSub(cli.DefaultContext(), "state")
	for _, args := range [][]string{
		{"demo.pr", "--paths"},
		{"--paths", "demo.pr"},
		{"demo.pr", "--if", "{stage: open}"},
		{"demo.pr", "--scope", "m"},
		{"demo.pr", "-o", "yaml"},
	} {
		// No server is running, so reaching the dial is proof the parse got that far: a usage
		// error would mean it did not.
		if err := cmd.Hooks.Run(cli.DefaultContext(), args); errors.Is(err, cli.ErrUsage) {
			t.Errorf("verse state %v: %v — the flag did not parse", args, err)
		}
	}
	// A second POSITIONAL is still an error: one path is what it takes.
	if err := cmd.Hooks.Run(cli.DefaultContext(), []string{"demo.pr", "demo.other"}); !errors.Is(err, cli.ErrUsage) {
		t.Errorf("two paths gave %v, want a usage error", err)
	}
}

// AN ADDRESS AS STEPS is the spelling this CLI takes on the way in, and `--paths` is what it
// answers with — so a listing pipes back into a read without anybody quoting anything.
//
// What is asserted is the ROUND TRIP rather than the rendering: the steps go in, the path comes
// out, and parsing it back yields the same steps. How each one is quoted along the way is
// kpath's business (a numeric-looking id is quoted, a colon-bearing one is not) and pinning it
// here would be pinning somebody else's decision.
func TestAnAddressMayBeWrittenAsSteps(t *testing.T) {
	for _, steps := range [][]string{
		{"demo", "pr", "1"},
		{"demo", "pr", "a.b"},
		{"demo", "pr", "a.b", "labels", "urgent"},
		{"git", "ref", "x@git:ref:y"},
		{"verse", "task", "instance", "run-1"},
	} {
		at, err := address(steps)
		if err != nil {
			t.Errorf("address(%q): %v", steps, err)
			continue
		}
		p, err := entity.ParsePath(at)
		if err != nil {
			t.Errorf("address(%q) = %q, which does not parse: %v", steps, at, err)
			continue
		}
		got := append(p.Ref.Segs(), p.Field...)
		if strings.Join(got, "\x00") != strings.Join(steps, "\x00") {
			t.Errorf("address(%q) = %q → %q, want the steps back", steps, at, got)
		}
	}
	// One token stays one token, in either delimiter, and the whole verse is no token at all.
	for _, tc := range []struct{ in, want string }{
		{"demo.pr.1", "demo.pr.1"},
		{"demo:pr:1", "demo:pr:1"},
	} {
		if got, err := address([]string{tc.in}); err != nil || got != tc.want {
			t.Errorf("address([%q]) = (%q, %v), want %q", tc.in, got, err, tc.want)
		}
	}
	if got, err := address(nil); err != nil || got != "" {
		t.Errorf("address(nil) = (%q, %v), want the empty path", got, err)
	}
}

// Mixing the spellings is refused rather than read as something else: `verse state demo.pr 1`
// would otherwise address a system literally called `demo.pr`, which is not what anybody typed.
// The refusal names the step and offers the one-token form.
func TestMixingTheAddressSpellingsIsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"demo.pr", "1"},
		{"demo:pr", "1"},
		{"demo", "pr.x", "1"},
		{"verse", "task.instance", "run-1"},
	} {
		got, err := address(args)
		if !errors.Is(err, cli.ErrUsage) {
			t.Errorf("address(%q) = (%q, %v), want a usage error", args, got, err)
		}
	}
}

// A command that takes ONE token refuses a second rather than answering about args[0].
//
// `verse state` takes an address as separate steps and these take one, so the spelling that
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
	if _, err := runCLI(t, "charter", "rm", "notes", "rules", "note"); err != nil {
		t.Errorf("charter rm: %v", err)
	}
	if out, err := runCLI(t, "charter", "notes"); err == nil && strings.Contains(out, "name: note") {
		t.Errorf("the rule is still installed: %s", out)
	}
}

// `verse c` takes a PATH, and its verbs are ordinary words — so a package at `rm` is shadowed by
// `verse c rm`. `--` ends the verbs, which is the shell's own convention for exactly this.
//
// The collision moves with the verbs: this used to be about a package at `remove`, which is not
// a verb any more, so the test follows the words that are.
func TestADashDashReachesAnAddressThatLooksLikeAVerb(t *testing.T) {
	serve(t)
	if err := installPackageRuleAt(t, "rm"); err != nil {
		t.Fatalf("installing at `rm`: %v", err)
	}

	// Without it, the verb wins — which is what makes the escape necessary rather than tidy.
	if _, err := runCLI(t, "charter", "rm"); err == nil {
		t.Error("`charter rm` with no path was read as an address")
	}
	out, err := runCLI(t, "charter", "--", "rm")
	if err != nil {
		t.Fatalf("charter -- rm: %v", err)
	}
	if !strings.Contains(out, "writes: rm.") {
		t.Errorf("`--` did not reach the package: %s", out)
	}
}
