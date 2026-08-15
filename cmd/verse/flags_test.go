package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/scott-cotton/cli"

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
	"flow": "flows", "trigger": "triggers", "goal": "goals",
	"resolver": "resolvers", "runner": "runners", "source": "sources",
	"stratum": "strata",
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
// `verse goal add-it ship` would print the goals rather than say the verb is not one.
// `list` is called out by name, because it is the mistake a bare-listing collection invites
// and it must not become a second way to list.
func TestCollectionsRefuseAVerbTheyDoNotHave(t *testing.T) {
	root := rootCmd()
	cc := cli.DefaultContext()
	for singular := range collections {
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

// Flags come BEFORE the path, and saying so is the whole of this test.
//
// A command that has subcommands stops parsing flags at its first positional — cli's
// Command.Parse passes all=true only for a leaf — which is the rule that routes
// `state put <ref> <payload> --if <pattern>` to put's own --if rather than to the read's
// filter. The cost is that `state demo.pr --paths` reads --paths as a second path, and the
// error has to name the cause or it looks like the path was rejected.
func TestStateNamesTheFlagOrder(t *testing.T) {
	cmd := rootCmd().FindSub(cli.DefaultContext(), "state")
	err := cmd.Hooks.Run(cli.DefaultContext(), []string{"demo.pr", "--paths"})
	if !errors.Is(err, cli.ErrUsage) {
		t.Fatalf("state demo.pr --paths gave %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "verse state --paths demo.pr") {
		t.Errorf("the error does not say what to type instead: %v", err)
	}
}
