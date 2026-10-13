package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/scott-cotton/cli"
)

// `verse gate <id> --answer <option> --as <who>`: the flags come AFTER the id, and a command
// with subcommands stops parsing at its first positional so a verb can be dispatched. The
// id's flags are parsed on their own, which these two refusals prove reach the flags: one is
// the missing --as, which only a parsed --answer asks for, and one is a second positional.
func TestGateAnswersWithFlagsAfterTheID(t *testing.T) {
	// Nobody configured, toward a shared verse: that is who the missing --as is about. Toward a
	// verse on this machine the answer would be the local user's.
	t.Setenv("VERSE_USER", "")
	t.Setenv("VERSE_ADDR", "verse.example.com:9797")
	at(t, g.Addr) // rootCmd points the CLI there; put it back after
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
