package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/scott-cotton/cli"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
)

// --if/--if-at as the operator meets them: `watch` renders them into its query (tested here
// against watchConfig, which is where the rendering happens), and `entity` sends the same
// pair to the listing route (tested end to end, against a real viff).

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

// runState drives `viff data …` through the COMMAND's own Run, rather than walking to
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

// `viff data --if` reads by what an entity SAYS. It is the same filter `viff watch --if`
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
	// At `demo pr '*'`, each pr: the filter is asked of what the path reached.
	out, err := runState(t, "demo", "pr", "*", "--paths", "--if", "{stage: open}")
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
	out, err = runState(t, "demo", "pr", "*", "--paths", "--if", "{urgent: true}", "--if-at", "labels")
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

// --if and --trim ask of the entities at the depth the PATH asks at, and a reflected issue is
// four segments down (`git-issue.viff.issue.<id>`, a repository being a level). Read at three
// segments the map of issues is the one entity, and an --if about an issue's status keeps
// nothing. The `*` is what says "each issue", in either spelling: the kinded path's segment wildcard, or
// a whole `*` step. The id that is an asterisk is the quoted step `"*"`.
func TestAFilterAsksAtThePathsDepth(t *testing.T) {
	serve(t)
	for _, e := range []struct{ id, payload string }{
		{"a", `{id: a, status: open, updatedAt: "t1", title: one}`},
		{"b", `{id: b, status: closed, updatedAt: "t2", title: two}`},
		{"c", `{id: c, status: open, updatedAt: "t3", title: three}`},
	} {
		if _, err := runCLI(t, "data", "tracker", "viff", "issue", e.id, "--put", e.payload); err != nil {
			t.Fatalf("put %s: %v", e.id, err)
		}
	}

	for _, spelling := range [][]string{
		{"--at", "tracker.viff.issue.*"},
		{"tracker", "viff", "issue", "*"},
	} {
		out, err := runState(t, append(spelling,
			"--if", "{status: open}", "--trim", "{id: null, updatedAt: null}")...)
		if err != nil {
			t.Fatalf("data %q --if --trim: %v", spelling, err)
		}
		doc, err := codec.ParseNode([]byte(out), codec.Tony)
		if err != nil {
			t.Fatalf("parse %q: %v", out, err)
		}
		// The answer is rooted at the path, so its keys are the ids.
		got := map[string]string{}
		for i, f := range doc.Fields {
			issue := doc.Values[i]
			if len(issue.Fields) != 2 {
				t.Errorf("%q: issue %s kept %d fields, want the 2 --trim names:\n%s", spelling, f.String, len(issue.Fields), out)
			}
			for j, k := range issue.Fields {
				if k.String == "updatedAt" {
					got[f.String] = issue.Values[j].String
				}
			}
		}
		if want := map[string]string{"a": "t1", "c": "t3"}; !reflect.DeepEqual(got, want) {
			t.Errorf("data %q --if {status: open} = %v, want %v:\n%s", spelling, got, want, out)
		}
	}

	// The id that IS an asterisk is written and read with the quoted step, a `*` step is refused
	// by a write, and the listing prints the quoted step so the line reads back as that one id.
	if _, err := runCLI(t, "data", "tracker", "viff", "issue", "*", "--put", `{status: open}`); err == nil {
		t.Error("a write took a `*` step, which names every issue rather than one")
	}
	if _, err := runCLI(t, "data", "tracker", "viff", "issue", `"*"`, "--put", `{id: star, status: open}`); err != nil {
		t.Fatalf(`put the id "*": %v`, err)
	}
	if out, err := runState(t, "tracker", "viff", "issue", `"*"`); err != nil || !strings.Contains(out, "id: star") ||
		strings.Contains(out, "title: one") {
		t.Errorf(`data tracker viff issue "*" = (%q, %v), want the one issue whose id is an asterisk`, out, err)
	}
	if out, err := runState(t, "--paths", "--depth", "4", "tracker", "viff", "issue"); err != nil ||
		!has(lines(out), `tracker viff issue '"*"'`) {
		t.Errorf("--paths = (%q, %v), want the asterisk id printed as the quoted step", out, err)
	}

	// One literal id four segments down is that issue, not a place inside the map of them.
	for pattern, kept := range map[string]bool{"{status: closed}": true, "{status: open}": false} {
		out, err := runState(t, "tracker", "viff", "issue", "b", "--if", pattern)
		if err != nil {
			t.Fatalf("data --if %s at one issue: %v", pattern, err)
		}
		if got := strings.Contains(out, "title: two"); got != kept {
			t.Errorf("--if %s at issue b kept it = %v, want %v:\n%s", pattern, got, kept, out)
		}
	}
}

// `viff data` and `viff data put` both take an --if, and they mean different things:
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
// lifted: `viff ps | o list -if … -trim … '[*]'` becomes `viff ps --if … --trim …`.
//
// Asserted on `trigger`, because it is a listing whose items are documents a test can seed
// exactly — the same funnel every collection prints through.
func TestAListingFiltersAndProjects(t *testing.T) {
	serve(t)
	for _, src := range []string{
		`{name: alpha, condition: {subject: {path: "demo.pr.*"}}, action: {patch: {seen: "yes"}}}`,
		`{name: beta, condition: {subject: {path: "other.thing.*"}}, action: {patch: {seen: "yes"}}}`,
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
	// `viff charter` answers one charter as its FILE — one document rather than a list of
	// rules — so the filters apply to it whole: `--if` keeps or drops the answer, and `--trim`
	// cuts fields wherever they sit. The rules are in it either way, which is what this checks.
	out, err := runCLI(t, "charter")
	if err != nil {
		t.Fatalf("charter: %v", err)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("the charter does not hold the rules: %s", out)
	}
	// --trim says how much of the answer to write, and it reaches wherever the field sits.
	out, err = runCLI(t, "charter", "--trim", `{rules: [{name}]}`)
	if err != nil {
		t.Fatalf("charter --trim: %v", err)
	}
	if !strings.Contains(out, "alpha") || strings.Contains(out, "condition") {
		t.Errorf("--trim did not project: %s", out)
	}
	// --if keeps or drops the answer, and NOTHING KEPT IS NOT AN ERROR: an empty answer is a
	// true answer to the question asked.
	out, err = runCLI(t, "charter", "--if", `{at: nowhere}`)
	if err != nil {
		t.Errorf("an empty result was an error: %v", err)
	}
	if strings.Contains(out, "alpha") || strings.Contains(out, "beta") {
		t.Errorf("--if kept what it should not: %s", out)
	}
}

// `state`'s items are THE NODES THE PATH REACHED, not the document's top-level keys: a trim names
// fields of each, whatever depth the path reaches.
func TestStateTrimsEachReachedNode(t *testing.T) {
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
	out, err := runState(t, "demo", "pr", "*", "--trim", `{stage: null}`)
	if err != nil {
		t.Fatalf("state --trim: %v", err)
	}
	if !strings.Contains(out, "stage") || strings.Contains(out, "owner") {
		t.Errorf("--trim did not project each payload: %s", out)
	}
	// Both prs survive: --trim says how much, not which.
	if !strings.Contains(out, `"1"`) || !strings.Contains(out, `"2"`) {
		t.Errorf("--trim dropped a pr: %s", out)
	}
	// And at `demo pr` the one node reached is the map of prs, which holds no stage.
	if out, err := runState(t, "demo", "pr", "--trim", `{stage: null}`); err != nil || strings.Contains(out, "open") {
		t.Errorf("--trim at demo pr = %q (%v), want the map of prs projected as one node", out, err)
	}
}
