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

// Every collection is addressed twice and the two spellings mean different things: the
// plural lists it, the singular acts on one member. The plural must exist at the TOP level
// (it replaced `<group> list`, which must be gone — two ways to list is what this removed),
// and the singular group must still be there to act through.
func TestCollectionsArePluralAtTheTop(t *testing.T) {
	root := rootCmd()
	for plural, singular := range map[string]string{
		"gates": "gate", "entities": "entity", "processes": "process",
		"flows": "flow", "triggers": "trigger", "goals": "goal",
	} {
		p := root.FindSub(cli.DefaultContext(), plural)
		if p == nil {
			t.Errorf("verse %s: no such command", plural)
			continue
		}
		if len(p.Children) != 0 {
			t.Errorf("verse %s: is a group, want a bare listing", plural)
		}
		s := root.FindSub(cli.DefaultContext(), singular)
		if s == nil {
			t.Errorf("verse %s: no such command", singular)
			continue
		}
		if s.FindSub(cli.DefaultContext(), "list") != nil {
			t.Errorf("verse %s list still exists — the listing moved to `verse %s`", singular, plural)
		}
	}
	// ps means "list the processes" wherever else it is typed; as a GROUP alias it meant
	// nothing runnable at all.
	if root.FindSub(cli.DefaultContext(), "ps") != root.FindSub(cli.DefaultContext(), "processes") {
		t.Error("ps does not resolve to the processes listing")
	}
}

// A listing that IGNORES positionals turns a wrong command into a plausible answer:
// `verse goals add ship-it` would print the goals rather than say `goal add` was meant.
func TestCollectionsRefuseArguments(t *testing.T) {
	root := rootCmd()
	cc := cli.DefaultContext()
	for _, plural := range []string{"gates", "entities", "processes", "flows", "triggers", "goals"} {
		cmd := root.FindSub(cc, plural)
		if cmd == nil {
			t.Fatalf("verse %s: no such command", plural)
		}
		// No server is running: a command that refuses the argument fails BEFORE dialing, so
		// a usage error is proof the guard fired rather than the request.
		err := cmd.Hooks.Run(cc, []string{"add", "ship-it"})
		if !errors.Is(err, cli.ErrUsage) {
			t.Errorf("verse %s add ship-it: error = %v, want a usage error", plural, err)
		}
	}
}

// A gate is answered by a VERB — `gate approve <id>` — so the two states that are not a
// decision (neither flag, both flags) are unrepresentable rather than validated. Each verb
// carries the short forms the interactive prompt already teaches (`approve? [y/N]`), and
// --say/-s rides along on both, since "no, because X" is worth as much as "yes, but".
func TestGateAndProcessAnswerVerbs(t *testing.T) {
	root := rootCmd()
	for _, c := range []struct{ group, verb string }{
		{"gate", "approve"}, {"gate", "deny"},
		{"process", "approve"}, {"process", "deny"},
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
	for _, group := range []string{"gate", "process"} {
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
		"two run policies":        {Policy: "http://x", Broker: true},
		"broker with no face":     {Broker: true},
	}
	for name, cfg := range cases {
		if err := cfg.validate(); !errors.Is(err, cli.ErrUsage) {
			t.Errorf("%s: validate() = %v, want a usage error", name, err)
		}
	}

	ok := map[string]upConfig{
		"defaults":            {},
		"running docd":        {DocdAddr: "h:1", DocdMountAddr: "h:2"},
		"data dir":            {Data: "/tmp/x"},
		"broker with a face":  {Broker: true, BrokerAddr: ":9090"},
		"broker face only":    {BrokerAddr: ":9090"},
		"remote controlPolic": {Policy: "http://x"},
	}
	for name, cfg := range ok {
		if err := cfg.validate(); err != nil {
			t.Errorf("%s: validate() = %v, want nil", name, err)
		}
	}
}
