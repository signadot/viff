package main

import (
	"errors"
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
