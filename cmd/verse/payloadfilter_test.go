package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// --if/--if-at as the operator meets them: `watch` renders them into its query (tested here
// against watchConfig, which is where the rendering happens), and `entity` sends the same
// pair to the listing route (tested end to end, against a real verse).

// query renders the predicate as tony whatever dialect it was typed in, because a GET has no
// body and so no Content-Type to declare one with. JSON is the case that matters: --ifmt json
// is what the tutorial suggests for pasting patterns, and a client that passed the text
// straight through would be asking the server to guess.
func TestWatchQueryRendersThePredicateAsTony(t *testing.T) {
	defer func(f string) { g.IFmt = f }(g.IFmt)

	want := entity.Obj{"stage": "open"}.Payload()
	for _, c := range []struct{ ifmt, pattern string }{
		{"tony", "{stage: open}"},
		{"json", `{"stage": "open"}`},
		{"yaml", "stage: open"},
	} {
		t.Run(c.ifmt, func(t *testing.T) {
			g.IFmt = c.ifmt
			cfg := &watchConfig{Since: "0", If: c.pattern}
			q, err := cfg.query("")
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			got, err := codec.ParseNode([]byte(q.Get("if")), codec.Tony)
			if err != nil {
				t.Fatalf("the server would not parse %q: %v", q.Get("if"), err)
			}
			// Compared as a MATCH rather than as bytes: the point is that the server asks the
			// same question, not that the rendering is character-for-character anything.
			if !(entity.Pattern{Predicate: got}).MatchEvent(entity.Event{
				Ref: entity.RefAt("demo", "pr", "1"), Op: entity.Set, To: want,
			}) {
				t.Errorf("--ifmt %s: %q reached the wire as %q, which does not match %v",
					c.ifmt, c.pattern, q.Get("if"), want)
			}
		})
	}
}

// A tag is the reason the canonical form is tony rather than JSON: `!glob` is not JSON, and
// an operator is most of what makes the predicate a match instead of a bag of equalities. It
// has to survive the parse-and-re-render the wire format costs.
func TestWatchQueryKeepsOperators(t *testing.T) {
	defer func(f string) { g.IFmt = f }(g.IFmt)
	g.IFmt = "tony"

	cfg := &watchConfig{Since: "0", If: `{id: !glob "hotfix-*"}`}
	q, err := cfg.query("")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.Contains(q.Get("if"), "!glob") {
		t.Fatalf("the operator did not survive: %q", q.Get("if"))
	}
}

// --if-at goes on the wire a SEGMENT AT A TIME, as a removal sends its path: the
// address is a list there, so a field name is never re-split at the far end.
func TestWatchQuerySendsThePathBySegment(t *testing.T) {
	defer func(f string) { g.IFmt = f }(g.IFmt)
	g.IFmt = "tony"

	cfg := &watchConfig{Since: "0", If: "true", IfAt: "labels.urgent"}
	q, err := cfg.query("")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got := q["if-at"]; len(got) != 2 || got[0] != "labels" || got[1] != "urgent" {
		t.Errorf("if-at = %v, want [labels urgent]", got)
	}
}

// --if-at names where a predicate is rooted, so without one it roots nothing. Sending it
// alone would be a filter that reads as narrowing and does not narrow — the store answers the
// same stream with it and without it — so it is a usage error, said before anything is sent.
func TestWatchRejectsAPathWithoutAPredicate(t *testing.T) {
	cfg := &watchConfig{Since: "0", IfAt: "labels"}
	_, err := cfg.query("")
	if !errors.Is(err, cli.ErrUsage) {
		t.Fatalf("--if-at alone gave %v, want a usage error", err)
	}
}

// A predicate that does not parse is the operator's typo, and it is caught HERE rather than
// becoming a stream that silently matches nothing.
func TestWatchRejectsAMalformedPredicate(t *testing.T) {
	defer func(f string) { g.IFmt = f }(g.IFmt)
	g.IFmt = "tony"

	cfg := &watchConfig{Since: "0", If: "{stage: "}
	if _, err := cfg.query(""); err == nil {
		t.Fatal("a malformed --if was accepted")
	}
}

// runState drives `verse data …` through the COMMAND's own Run, rather than walking to
// the leaf as runCLI does. The walk is what makes runCLI usable for the leaves, and it is also
// what makes it blind here: a flag on a group, and a group's decision about what to do with a
// flag meant for its subcommand, both happen in the parent's Parse — which the walk skips.
func runState(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cc := &cli.Context{
		Out: nopCloser{&out}, Err: nopCloser{&errOut},
		In: io.NopCloser(strings.NewReader("")), Go: t.Context(),
	}
	cmd := rootCmd()
	g.Addr = testAddr // after rootCmd, which re-reads it from the config layers
	sub := cmd.FindSub(cc, "data")
	if sub == nil {
		t.Fatal("no `state` command")
	}
	// Run FIRST — see runCLI: `return out.String(), sub.Run(...)` would snapshot the buffer
	// before the command wrote to it.
	err := sub.Run(cc, args)
	return out.String(), err
}

// `verse data --if` reads by what an entity SAYS. It is the same filter `verse watch --if`
// streams by, and the reason to have it on the read too is that the two answer different
// questions about one selector: what is true now, and what becomes true next.
func TestStateFiltersOnThePayload(t *testing.T) {
	serve(t)
	// Written as STEPS, which is also what --paths answers with: the write and the listing
	// speak the one spelling a command line takes.
	for _, e := range []struct{ ref, payload string }{
		{"demo pr 1", `{stage: open, labels: {urgent: true}}`},
		{"demo pr 2", `{stage: merged, labels: {urgent: false}}`},
		{"demo pr 3", `{stage: open}`},
	} {
		args := append([]string{"data"}, strings.Fields(e.ref)...)
		if _, err := runCLI(t, append(args, "--put", e.payload)...); err != nil {
			t.Fatalf("put %s: %v", e.ref, err)
		}
	}

	// --paths, because the question is WHICH ENTITIES the filter names and that is exactly what
	// --paths answers. The document itself spells an id as a key rather than as a ref, so
	// asserting on `demo.pr.1` in the body would be asserting on a rendering.
	out, err := runState(t, "--paths", "--if", "{stage: open}")
	if err != nil {
		t.Fatalf("state --if: %v", err)
	}
	// Per LINE: `--paths` walks into the payload too, so a substring check would match
	// `demo pr 1 stage` and assert nothing about the entity. In steps a numeric id needs no
	// quoting at all — it is a name, where a bare number in a kpath is an index.
	if !has(lines(out), "demo pr 1") || !has(lines(out), "demo pr 3") {
		t.Errorf("--if {stage: open} did not name the open PRs:\n%s", out)
	}
	if has(lines(out), "demo pr 2") {
		t.Errorf("--if {stage: open} named the merged PR:\n%s", out)
	}

	// Rooted at a path, and the entity that has no such path is not named.
	out, err = runState(t, "--paths", "--if", "{urgent: true}", "--if-at", "labels")
	if err != nil {
		t.Fatalf("state --if-at: %v", err)
	}
	if !has(lines(out), "demo pr 1") {
		t.Errorf("--if-at labels did not name the urgent PR:\n%s", out)
	}
	if has(lines(out), "demo pr 2") || has(lines(out), "demo pr 3") {
		t.Errorf("--if-at labels named an entity that does not satisfy it:\n%s", out)
	}
}

// `verse data` and `verse data put` both take an --if, and they mean different things:
// a filter on the listing, a precondition on the write. They do not collide, because a parent
// stops parsing flags at its first positional — so `data …` hands the whole tail,
// flags included, to `put`.
//
// This is the pin for that, and it is worth pinning rather than trusting: if the parent ever
// swallowed the flag, the precondition would not fail — it would VANISH, and a guarded write
// would silently become an unguarded one.
func TestAWritesPreconditionIsNotTheReadsFilter(t *testing.T) {
	store := serve(t)
	ref := entity.RefAt("demo", "pr", "1")
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", `{stage: open}`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A precondition that does NOT hold, with the flag after the positionals, exactly as
	// someone would type it. It is `--expect` rather than `--if` because the two used to be
	// told apart by a subcommand boundary — `put` had an --if of its own — and folding the
	// verbs into flags took that boundary away. --create expects nothing, --update expects
	// something, --expect names what.
	if _, err := runState(t, "demo", "pr", "1", "--put", `{stage: deployed}`, "--expect", `{stage: closed}`); err == nil {
		t.Error("a write whose --expect does not hold was reported as succeeding")
	}
	if got := payloadOf(t, store, ref)["stage"]; got != "open" {
		t.Fatalf("stage = %v, want open — the precondition was dropped and the write went through", got)
	}

	// And one that does hold still writes, so the pin is not just "everything fails".
	if _, err := runState(t, "demo", "pr", "1", "--put", `{stage: reviewed}`, "--expect", `{stage: open}`); err != nil {
		t.Fatalf("a write whose --expect holds was refused: %v", err)
	}
	if got := payloadOf(t, store, ref)["stage"]; got != "reviewed" {
		t.Fatalf("stage = %v, want reviewed", got)
	}
}

// --if-at roots --if on the read too, so alone it is the same usage error it is on watch.
func TestStateRejectsAPathWithoutAPredicate(t *testing.T) {
	serve(t)
	if _, err := runState(t, "--if-at", "labels"); !errors.Is(err, cli.ErrUsage) {
		t.Fatalf("--if-at alone gave %v, want a usage error", err)
	}
}

// --if AND --trim ON A LISTING, which is the pipeline the state-ergonomics note asks to be
// lifted: `verse ps | o list -if … -trim … '[*]'` becomes `verse ps --if … --trim …`.
//
// Asserted on `trigger`, because it is a listing whose items are documents a test can seed
// exactly — the same funnel every collection prints through.
func TestAListingFiltersAndProjects(t *testing.T) {
	serve(t)
	for _, src := range []string{
		`{name: alpha, condition: {subject: {system: demo, kind: pr}}, action: {patch: {seen: "yes"}}}`,
		`{name: beta, condition: {subject: {system: other, kind: thing}}, action: {patch: {seen: "yes"}}}`,
	} {
		f := filepath.Join(t.TempDir(), "rule.tony")
		if err := os.WriteFile(f, []byte(src), 0o600); err != nil {
			t.Fatalf("write charter: %v", err)
		}
		if _, err := runCLI(t, "charter", "-a", "-f", f); err != nil {
			t.Fatalf("install: %v", err)
		}
	}

	// --if names WHICH rules, over the same match language a charter's `value:` takes.
	//
	// `verse charter` answers the charter as a TREE — one document rather than a list of rules
	// — so the filters apply to it whole: `--if` keeps or drops the answer, and `--trim` cuts
	// fields wherever they sit. The rules are in it either way, which is what this checks.
	out, err := runCLI(t, "charter")
	if err != nil {
		t.Fatalf("charter: %v", err)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("the tree does not hold the rules: %s", out)
	}
	// --trim says how much of the answer to write, and it reaches wherever the field sits.
	out, err = runCLI(t, "charter", "--trim", `{writes: null}`)
	if err != nil {
		t.Fatalf("charter --trim: %v", err)
	}
	if !strings.Contains(out, "anywhere") || strings.Contains(out, "condition") {
		t.Errorf("--trim did not project: %s", out)
	}
	// --if keeps or drops the answer, and NOTHING KEPT IS NOT AN ERROR: an empty answer is a
	// true answer to the question asked.
	out, err = runCLI(t, "charter", "--if", `{writes: nowhere}`)
	if err != nil {
		t.Errorf("an empty result was an error: %v", err)
	}
	if strings.Contains(out, "alpha") || strings.Contains(out, "beta") {
		t.Errorf("--if kept what it should not: %s", out)
	}
}

// `state`'s items are ENTITIES, not the document's top-level keys: a trim names payload fields
// and applies to each entity standing at the path, whatever depth the path reaches.
func TestStateTrimsEachEntitysPayload(t *testing.T) {
	serve(t)
	for _, e := range [][]string{
		{"demo", "pr", "1", `{stage: open, owner: ana}`},
		{"demo", "pr", "2", `{stage: merged, owner: dee}`},
	} {
		args := append([]string{"data"}, e[:len(e)-1]...)
		if _, err := runCLI(t, append(args, "--put", e[len(e)-1])...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// Through the state command itself: the harness walks subcommands by name, so a path
	// positional has to go to the command rather than through the walk.
	out, err := runState(t, "demo", "pr", "--trim", `{stage: null}`)
	if err != nil {
		t.Fatalf("state --trim: %v", err)
	}
	if !strings.Contains(out, "stage") || strings.Contains(out, "owner") {
		t.Errorf("--trim did not project each payload: %s", out)
	}
	// Both entities survive: --trim says how much, not which.
	if !strings.Contains(out, `"1"`) || !strings.Contains(out, `"2"`) {
		t.Errorf("--trim dropped an entity: %s", out)
	}
}
