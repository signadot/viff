package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/scott-cotton/cli"
)

// A RELATIVE CURSOR IS `--ago N`, because `--since -N` is not spellable at a command line: a
// leading dash is what an option looks like, so `verse watch -since -3` reads as `-since`
// handed no value followed by a flag named `-3`. The wire is unchanged — a query parameter has
// no dash to trip over — so `--ago` renders as the `since=-N` the store already resolves.
func TestAgoIsTheNegativeCursorOnTheWire(t *testing.T) {
	for _, c := range []struct{ ago, since string }{
		{"3", "-3"},
		{"50", "-50"},
		// The last ZERO commits is from now, and it must NOT go out as `since=-0`: the wire
		// reads that as 0, the whole retained log, which is the opposite of no history.
		{"0", ""},
	} {
		q, err := (&watchConfig{Ago: c.ago}).query("")
		if err != nil {
			t.Errorf("--ago %s: %v", c.ago, err)
			continue
		}
		if got := q.Get("since"); got != c.since {
			t.Errorf("--ago %s reached the wire as since=%q, want %q", c.ago, got, c.since)
		}
	}
}

// --since stays what it always was: an absolute revision, absent for a watch from now.
func TestSinceStaysTheAbsoluteCursor(t *testing.T) {
	for _, c := range []struct{ since, want string }{
		{"", ""},   // absent: stream from now
		{"0", "0"}, // …which is not `since=0`, the whole retained log
		{"42", "42"},
		// Not a number is the SERVER's refusal to make, so it travels rather than being
		// second-guessed here.
		{"yesterday", "yesterday"},
	} {
		q, err := (&watchConfig{Since: c.since}).query("")
		if err != nil {
			t.Errorf("--since %q: %v", c.since, err)
			continue
		}
		if got := q.Get("since"); got != c.want {
			t.Errorf("--since %q reached the wire as since=%q, want %q", c.since, got, c.want)
		}
	}
}

// A negative --since is refused where it is typed, naming the flag that means it — rather than
// travelling to a wire that would have taken it and leaving the spelling ambiguous forever.
func TestNegativeSinceSaysAgoInstead(t *testing.T) {
	_, err := (&watchConfig{Since: "-3"}).query("")
	if err == nil {
		t.Fatal("--since -3 was sent as a cursor")
	}
	if !errors.Is(err, cli.ErrUsage) {
		t.Errorf("not a usage error: %v", err)
	}
	if !strings.Contains(err.Error(), "--ago 3") {
		t.Errorf("the refusal does not spell the flag that means it: %v", err)
	}
}

// --ago takes a COUNT, so the minus sign it spells for you is not typed twice.
func TestAgoRefusesWhatIsNotACount(t *testing.T) {
	for _, raw := range []string{"-3", "yesterday", "3.5"} {
		if _, err := (&watchConfig{Ago: raw}).query(""); err == nil {
			t.Errorf("--ago %s was accepted as a count", raw)
		}
	}
}

// One cursor, two spellings: asking with both is a question with two answers.
func TestSinceAndAgoTogetherAreRefused(t *testing.T) {
	_, err := (&watchConfig{Since: "42", Ago: "3"}).query("")
	if err == nil {
		t.Fatal("--since 42 --ago 3 was accepted")
	}
	if !errors.Is(err, cli.ErrUsage) {
		t.Errorf("not a usage error: %v", err)
	}
}
