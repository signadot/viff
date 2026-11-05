package io

import (
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/codec"
)

// A STORED `lastError: null` is nothing recorded, and goes back out as nothing recorded: the
// generated decoder allocates the Failure before reading it, so the encoder is where null is
// kept.
func TestANullFailureStaysNull(t *testing.T) {
	var e Entity
	if err := codec.Unmarshal([]byte("{name: a, root: git, dir: /x, lastError: null}"), &e, codec.Tony); err != nil {
		t.Fatal(err)
	}
	out, err := codec.Marshal(e, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "error:") {
		t.Fatalf("a null lastError came back as %q", out)
	}
}
