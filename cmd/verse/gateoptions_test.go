package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/scott-cotton/cli"
)

// `--options id[:need],…` on `task do gate` is read the way a charter gate's options are
// written: a threshold left out is one, and a threshold that is not a number at least one is
// refused as usage.
func TestParseOptions(t *testing.T) {
	opts, err := parseOptions("canary, full:2 ,abort")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(opts) != 3 || opts[0].ID != "canary" || opts[0].Need != 1 || opts[1].ID != "full" || opts[1].Need != 2 || opts[2].ID != "abort" {
		t.Fatalf("options = %+v", opts)
	}
	for _, bad := range []string{"canary:zero", "full:0", ":2", "a,,b"} {
		if _, err := parseOptions(bad); !errors.Is(err, cli.ErrUsage) {
			t.Errorf("%q: err = %v, want a usage error", bad, err)
		}
	}
	if opts, err := parseOptions(""); err != nil || opts != nil {
		t.Errorf("empty = (%+v, %v), want nothing", opts, err)
	}
	d, err := parseDeadline("4h:abort")
	if err != nil || d.Within != "4h" || d.Default != "abort" {
		t.Fatalf("deadline = (%+v, %v)", d, err)
	}
	if _, err := parseDeadline("soon"); !errors.Is(err, cli.ErrUsage) {
		t.Errorf("an unparseable duration: err = %v, want usage", err)
	}
}

// `verse gate <id> --answer <option> --as <who>`: the flags come AFTER the id, and a command
// with subcommands stops parsing at its first positional so a verb can be dispatched. The
// id's flags are parsed on their own, which these two refusals prove reach the flags: one is
// the missing --as, which only a parsed --answer asks for, and one is a second positional.
func TestGateAnswersWithFlagsAfterTheID(t *testing.T) {
	t.Setenv("VERSE_USER", "") // nobody configured, which is who the missing --as is about
	root := rootCmd()
	cc := cli.DefaultContext()
	gate := root.FindSub(cc, "gate")
	if gate == nil {
		t.Fatal("no gate command")
	}
	err := gate.Hooks.Run(cc, []string{"run-1~d1", "--answer", "canary"})
	if !errors.Is(err, cli.ErrUsage) || !strings.Contains(err.Error(), "--as") {
		t.Fatalf("--answer without --as: err = %v, want the usage error asking for --as", err)
	}
	err = gate.Hooks.Run(cc, []string{"run-1~d1", "--answer", "canary", "--as", "lead", "extra"})
	if !errors.Is(err, cli.ErrUsage) || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("a second positional: err = %v, want a usage error naming it", err)
	}
}
