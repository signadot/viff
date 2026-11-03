package systems

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/encode"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/parse"
)

// readEnv reads an environment as a spawn writes it.
func readEnv(t *testing.T, text string) (Environment, error) {
	t.Helper()
	node, err := parse.Parse([]byte(text))
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	var e Environment
	err = gomap.FromTonyIR(node, &e)
	return e, err
}

const example = `
vars: {PR: "42"}
systems:
  git:
  - repo: acme/app
    fetch: {}
    push:
    - {ref: !glob "refs/heads/verse/auto/*", exists: false}
    - {ref: !glob "refs/heads/verse/auto/*", own: true}
  - repo: github.com/acme/docs
    fetch: {}
  slack:
    post: {channel: deploys}
`

// AN ENVIRONMENT READS AS A SPAWN WRITES IT: a list of blocks or one block, attributes and verbs
// side by side, a shape or a list of shapes, a boolean as its word, a repository in its one
// spelling; and it writes back the same way.
func TestAnEnvironmentReadsAsWritten(t *testing.T) {
	e, err := readEnv(t, example)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Check(); err != nil {
		t.Fatal(err)
	}
	git := e.Systems["git"]
	if len(git) != 2 || git[0].Attributes["repo"] != "github.com/acme/app" {
		t.Fatalf("git blocks: %+v", git)
	}
	if push := git[0].Verbs["push"]; len(push) != 2 || push[0]["exists"].Type != ir.BoolType || push[0]["exists"].Bool || !push[1]["own"].Bool {
		t.Errorf("push shapes: %+v", push)
	}
	if fetch := git[1].Verbs["fetch"]; len(fetch) != 1 || len(fetch[0]) != 0 {
		t.Errorf("fetch: {} is one shape that names nothing: %+v", fetch)
	}
	if s := e.Systems["slack"]; len(s) != 1 || s[0].Verbs["post"][0]["channel"].String != "deploys" {
		t.Errorf("slack: %+v", s)
	}
	node, err := gomap.ToTonyIR(e)
	if err != nil {
		t.Fatal(err)
	}
	text := encode.MustString(node)
	back, err := readEnv(t, text)
	if err != nil || back.Check() != nil || len(back.Systems["git"]) != 2 || len(back.Systems["git"][0].Verbs["push"]) != 2 {
		t.Errorf("written back as\n%s\nread %+v, %v", text, back, err)
	}
	if !strings.Contains(text, "slack:\n") || strings.Contains(text, "slack:\n  -") {
		t.Errorf("a system with no unique attribute is written as one block:\n%s", text)
	}
}

// AN ENVIRONMENT IS CHECKED AGAINST THE DOCUMENTS, and each refusal says where and why.
func TestAnEnvironmentIsChecked(t *testing.T) {
	for text, want := range map[string]string{
		`systems: {pager: {page: {}}}`:                                                          "systems.pager: not a system",
		`systems: {git: {repo: acme/app, branch: main}}`:                                        `no attribute or verb "branch"`,
		`systems: {git: {repo: acme/app, push: main}}`:                                          "push: a verb takes a shape",
		`systems: {git: {repo: acme/app, fetch: main}}`:                                         "a verb takes a shape",
		`systems: {git: {repo: "acme/*", fetch: {}}}`:                                           "not a pattern",
		`systems: {git: {repo: acme/app, push: []}}`:                                            "an empty list allows nothing",
		`systems: {git: {repo: acme/app, push: {branch: main}}}`:                                "push.branch: not a field of push",
		`systems: {git: {repo: acme/app, push: {ref: main}}}`:                                   "push.ref:",
		`systems: {git: {repo: acme/app, push: {ref: $[ref]}}}`:                                 "is not bound",
		`systems: {git: {repo: acme/app, push: [{ref: "refs/heads/a"}, {exists: maybe}]}}`:      "push[1].exists:",
		`systems: {git: {repo: acme/app, push: {exists: "false"}}}`:                             "write false, unquoted",
		`systems: {git: {repo: acme/app, push: {ref: "refs/heads/*"}}}`:                         "is written `!glob",
		`systems: {git: {repo: acme/app, push: {ref: "refs/heads/verse/**"}}}`:                  `!regexp "^refs/heads/verse/"`,
		`systems: {git: {repo: acme/app, push: {ref: !glob "refs/heads/verse/**"}}}`:            "`**` is `*`",
		`systems: {git: {repo: acme/app, push: {ref: !gloob "refs/heads/*"}}}`:                  "!gloob is no operator",
		`systems: {git: [{repo: acme/app, fetch: {}}, {repo: GitHub.com/Acme/App, fetch: {}}]}`: "one block per repo",
		`systems: {slack: [{post: {}}, {read: {}}]}`:                                            "one block",
		`systems: {github: {repo: acme/app, create-pr: {}}}`:                                    "is not checked out",
		`systems: {git: {repo: {}, fetch: {}}}`:                                                 "repo: an attribute takes a value",
		`systems: {git: [{repo: acme/app, fetch: {}}, {repo: other/app, fetch: {}}]}`:           "would both check out as app/",
		`systems: {git: {fetch: {}}}`:                                                           "systems.git: repo: required",
		`vars: {"1st": x}`:                                                                      "vars.1st: a variable's name",
	} {
		e, err := readEnv(t, text)
		if err == nil {
			err = e.Check()
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want it refused saying %q", text, err, want)
		}
	}
	// The environment checked is bound, so a `$` in a value is part of it: install stands a
	// charter's `$` in before it checks (trigger.installLaunch), and a payload a firing bound
	// is held as written.
	if e, err := readEnv(t, `systems: {git: {repo: $[subject.value.repo], fetch: {}}}`); err != nil || e.Check() == nil {
		t.Errorf("a $ attribute: %v, want it refused", err)
	}
}

// A STEP IS WITHIN AN ENVIRONMENT when its system's block (by its unique attribute, or the one
// block) gives the verb and a shape of it matches.
func TestAStepIsWithinTheEnvironmentByItsBlockAndShape(t *testing.T) {
	e, err := readEnv(t, example)
	if err != nil {
		t.Fatal(err)
	}
	push := func(repo, ref, exists, own string) map[string]string {
		return map[string]string{"repo": repo, "ref": ref, "delete": "false", "force": "false", "exists": exists, "own": own}
	}
	for _, c := range []struct {
		step   string
		fields map[string]string
		within bool
	}{
		{"git.push", push("github.com/acme/app", "refs/heads/verse/auto/x", "false", "false"), true},
		{"git.push", push("github.com/acme/app", "refs/heads/verse/auto/x", "true", "true"), true},
		{"git.push", push("github.com/acme/app", "refs/heads/verse/auto/x", "true", "false"), false}, // another task's branch
		{"git.push", push("github.com/acme/app", "refs/heads/main", "true", "true"), false},
		{"git.push", push("github.com/acme/docs", "refs/heads/verse/auto/x", "false", "false"), false}, // docs gives no push
		{"git.fetch", map[string]string{"repo": "acme/docs"}, true},
		{"git.fetch", map[string]string{"repo": "github.com/acme/other"}, false},
		{"slack.post", map[string]string{"channel": "deploys"}, true},
		{"slack.post", map[string]string{"channel": "general"}, false},
		{"slack.read", map[string]string{"channel": "deploys"}, false},
		{"files.write", map[string]string{"path": "x"}, false},
	} {
		if got := e.Allows(c.step, FieldsDoc(c.step, c.fields)); got != c.within {
			t.Errorf("%s %v: within %v, want %v", c.step, c.fields, got, c.within)
		}
	}
}

// pat is a pattern as a row, a shape or a ceiling writes it.
func pat(t *testing.T, text string) *ir.Node {
	t.Helper()
	n, err := parse.Parse([]byte(text))
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return Plain(n)
}

// ONE PATTERN IS WITHIN ANOTHER when every value it matches, the other matches too, and the
// check never says so when it isn't.
func TestAPatternWithinAnother(t *testing.T) {
	for _, c := range []struct {
		form, outer, inner string
		within             bool
	}{
		{FormRef, `!regexp "^refs/heads/verse/"`, `!glob "refs/heads/verse/auto/*"`, true},
		{FormRef, `!regexp "^refs/heads/verse/"`, `refs/heads/verse/auto/x`, true},
		{FormRef, `!regexp "^refs/heads/verse/"`, `!regexp "^refs/heads/verse/auto/"`, true},
		{FormRef, `!glob "refs/heads/*"`, `refs/heads/main`, true},
		{FormRef, `!glob "refs/heads/*"`, `!regexp "^refs/heads/"`, false},
		{FormRef, `!glob "refs/heads/*"`, `refs/heads/a/b`, false},
		{FormRef, `!glob "refs/heads/verse/auto/*"`, `!regexp "^refs/heads/verse/"`, false},
		{FormRepo, `!glob "acme/*"`, `github.com/acme/app`, true},
		{FormRepo, `!glob "github.com/acme/*"`, `!glob "github.com/acme/*"`, true},
		{FormRepo, `!glob "github.com/acme/a*"`, `!glob "github.com/acme/*"`, false},
		{FormText, `!regexp "make "`, `"make test"`, true},
		{FormText, `!regexp "^make "`, `!glob "make *"`, true},
		{FormText, `"make test"`, `!glob "make *"`, false},
		{FormText, `!regexp "git push"`, `!regexp "^cd x && git push"`, true},
		{FormText, `!regexp "^git (push|fetch)"`, `!regexp "^git push"`, false}, // not exact: what follows the prefix is unread
		{"", `x`, `y`, false},
		{FormBool, `false`, `false`, true},
		{FormBool, `!not true`, `false`, true},
		{FormBool, `!not true`, `true`, false},
		{FormLower, `!or [none, allow]`, `allow`, true},
		{FormLower, `!or [none, allow]`, `!or [none, allow]`, true},
		{FormLower, `!or [none, allow]`, `!or [none, open]`, false},
		{FormLower, `null`, `!glob "x*"`, true},
		{FormLower, `!glob "x*"`, `null`, false},
		{FormPath, `[play, issues]`, `play.issues.issue.*`, true},
		{FormPath, `play.issues.*`, `[play, issues, issue]`, true},
		{FormPath, `play.issues.*`, `play.issues`, false},
		{FormPath, `play.*.x`, `play.issues.*`, false},
		{FormDoc, `{a: null}`, `{a: 1, b: 2}`, true},
		{FormDoc, `{a: 1, b: 2}`, `{a: 1}`, false},
	} {
		outer, inner := pat(t, c.outer), pat(t, c.inner)
		if got := FieldContains(c.form, outer, inner); got != c.within {
			t.Errorf("%s within %s (%s): %v, want %v", c.inner, c.outer, c.form, got, c.within)
		}
		// Sound: what it calls within, a value the inner is agrees on.
		if c.within && c.form != FormPath && isLiteral(inner) && !MatchField(c.form, outer, inner) {
			t.Errorf("%s within %s, but the value does not match", c.inner, c.outer)
		}
	}
}

// AN ENVIRONMENT BOUND AT LAUNCH IS CHECKED AND MATCHED IN CANONICAL SPELLING: a block built in
// code, or a $ replaced by a binder, says `acme/app` or `Acme/App`, and is still the repository
// github.com/acme/app, to the uniqueness check and to a step.
func TestABoundEnvironmentIsCanonical(t *testing.T) {
	for _, repo := range []string{"acme/app", "Acme/App", "github.com/acme/app"} {
		e := Environment{Systems: Systems{"git": {{Attributes: map[string]string{"repo": repo}, Verbs: map[string][]Shape{"fetch": {{}}}}}}}
		if err := e.Check(); err != nil {
			t.Errorf("%s: %v", repo, err)
		}
		if !e.Allows("git.fetch", FieldsDoc("git.fetch", map[string]string{"repo": "github.com/acme/app"})) {
			t.Errorf("%s: the canonical step is refused", repo)
		}
	}
	twice := Environment{Systems: Systems{"git": {
		{Attributes: map[string]string{"repo": "acme/app"}, Verbs: map[string][]Shape{"fetch": {{}}}},
		{Attributes: map[string]string{"repo": "GitHub.com/Acme/App"}, Verbs: map[string][]Shape{"fetch": {{}}}},
	}}}
	if err := twice.Check(); err == nil || !strings.Contains(err.Error(), "one block per repo") {
		t.Errorf("one repository in two spellings: %v", err)
	}
}

// A GLOB'S `*` STOPS AT A `/`, in a command as in a ref, and a regexp crosses it; an address is at
// or under its pattern, segment by segment.
func TestAFieldIsMatchedAsItsFormReadsIt(t *testing.T) {
	e, err := readEnv(t, `systems: {commands: {run: [{command: !glob "make *"}, {command: !regexp "^go test "}]}}`)
	if err != nil || e.Check() != nil {
		t.Fatal(err, e.Check())
	}
	run := func(cmd string) *ir.Node { return FieldsDoc("commands.run", map[string]string{"command": cmd}) }
	if !e.Allows("commands.run", run("make test")) || e.Allows("commands.run", run("make test/unit")) {
		t.Error("a glob's * crosses /, or takes no word")
	}
	if !e.Allows("commands.run", run("go test ./x/...")) || e.Allows("commands.run", run("cd x && go test ./...")) {
		t.Error("an anchored regexp")
	}
	e, err = readEnv(t, `systems: {verse: {charter-install: {at: "play.issues.*"}}}`)
	if err != nil || e.Check() != nil {
		t.Fatal(err, e.Check())
	}
	at := func(a string) *ir.Node { return FieldsDoc("verse.charter-install", map[string]string{"at": a}) }
	if !e.Allows("verse.charter-install", at("play.issues.issue")) || !e.Allows("verse.charter-install", at("play.issues.issue.x")) {
		t.Error("an address under the pattern is refused")
	}
	if e.Allows("verse.charter-install", at("play.issues")) || e.Allows("verse.charter-install", at("play")) {
		t.Error("an address above the pattern is allowed")
	}
}

// TWO PATTERNS MEET when some value matches both, though neither contains the other.
func TestPatternsMeet(t *testing.T) {
	for _, c := range []struct {
		form, a, b string
		meet       bool
	}{
		{FormRef, `!glob "refs/*/x"`, `!glob "refs/heads/*"`, true},
		{FormRef, `!glob "refs/heads/verse/auto/*"`, `!regexp "^refs/heads/verse/"`, true},
		{FormRef, `!glob "refs/tags/*"`, `!glob "refs/heads/*"`, false},
		{FormRef, `!glob "refs/heads/*"`, `refs/heads/a/b`, false},
		{FormRef, `!regexp "^refs/heads/"`, `refs/heads/a/b`, true},
		{FormRef, `!regexp "^refs/tags/"`, `!regexp "^refs/heads/"`, false},
		{FormText, `!glob "*"`, `a/b`, false},
		{FormText, `!regexp "x"`, `a/b`, false},
		{FormText, `!glob "make *"`, `!glob "* test"`, true},
		{FormBool, `true`, `false`, false},
		{FormBool, `!not true`, `false`, true},
		{FormBool, `!not true`, `true`, false},
		{FormLower, `!or [none, allow]`, `open`, false},
		{FormLower, `!or [none, allow]`, `!or [open, allow]`, true},
		{FormPath, `play.*.x`, `play.issues.*`, true},
		{FormPath, `play.a`, `play.b.c`, false},
		{FormPath, `play`, `play.b.c`, true},
	} {
		a, b := pat(t, c.a), pat(t, c.b)
		if got := FieldMeets(c.form, a, b); got != c.meet {
			t.Errorf("%s ∩ %s (%s): %v, want %v", c.a, c.b, c.form, got, c.meet)
		}
		if got := FieldMeets(c.form, b, a); got != c.meet {
			t.Errorf("%s ∩ %s (%s): %v, want %v (the other way)", c.b, c.a, c.form, got, c.meet)
		}
	}
}

// THE ANALYSIS IS SOUND: two patterns that both match a value are never said to meet nowhere, and
// a pattern said to hold another matches every value the other does, over random globs, regexps
// and literals from a small alphabet where the separator and the wildcards collide often.
func TestTheAnalysisAgreesWithMatch(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	gen := func(alphabet string, n int) string {
		var b strings.Builder
		for range r.IntN(n) {
			b.WriteByte(alphabet[r.IntN(len(alphabet))])
		}
		return b.String()
	}
	pattern := func() *ir.Node {
		switch r.IntN(4) {
		case 0:
			return ir.FromString(gen("ab/", 5))
		case 1:
			return ir.FromString(gen("ab/*", 6)).WithTag("!glob")
		case 2:
			return ir.FromString("^" + gen("ab/", 4)).WithTag("!regexp")
		}
		return ir.FromString(gen("ab/", 3)).WithTag("!regexp")
	}
	for range 20000 {
		a, b, v := pattern(), pattern(), ir.FromString(gen("ab/", 6))
		if MatchField(FormText, a, v) && MatchField(FormText, b, v) && !FieldMeets(FormText, a, b) {
			t.Fatalf("%s and %s both match %q, but FieldMeets says they meet nowhere", Words(a), Words(b), v.String)
		}
		if FieldContains(FormText, a, b) && MatchField(FormText, b, v) && !MatchField(FormText, a, v) {
			t.Fatalf("%s is said to hold %s, but %q matches the inner alone", Words(a), Words(b), v.String)
		}
	}
}

// A STEP OUTSIDE THE ENVIRONMENT IS REFUSED SAYING WHAT IS THERE, so the call can be corrected:
// the system, the block, the verb, or the shapes the block gives.
func TestARefusalSaysWhatTheEnvironmentGives(t *testing.T) {
	e, err := readEnv(t, example)
	if err != nil {
		t.Fatal(err)
	}
	push := map[string]string{"repo": "github.com/acme/app", "ref": "refs/heads/main", "delete": "false", "force": "false", "exists": "true", "own": "false"}
	for _, c := range []struct {
		step   string
		fields map[string]string
		says   string
	}{
		{"files.write", map[string]string{"path": "x"}, "gives no `files`"},
		{"git.fetch", map[string]string{"repo": "github.com/acme/other"}, "no block for repo github.com/acme/other (it has: github.com/acme/app, github.com/acme/docs)"},
		{"git.push", map[string]string{"repo": "github.com/acme/docs", "ref": "refs/heads/x"}, "`git` for github.com/acme/docs gives no `push` (it gives: fetch)"},
		{"git.push", push, `it gives {exists: false, ref: !glob refs/heads/verse/auto/*}, {own: true, ref: !glob refs/heads/verse/auto/*}`},
	} {
		why, refused := e.Refuses(c.step, FieldsDoc(c.step, c.fields))
		if !refused || !strings.Contains(why, c.says) {
			t.Errorf("%s %v: %q (%v), want it saying %q", c.step, c.fields, why, refused, c.says)
		}
	}
	if why, refused := e.Refuses("slack.post", FieldsDoc("slack.post", map[string]string{"channel": "deploys"})); refused {
		t.Errorf("an allowed step refused: %s", why)
	}
}

// A ROW REACHES THE ENVIRONMENT when some step it matches is one the environment gives; a row
// wider than the environment does, and one that can match nothing does not.
func TestARowReachesTheEnvironment(t *testing.T) {
	e, err := readEnv(t, example)
	if err != nil {
		t.Fatal(err)
	}
	params := func(kv ...string) map[string]*ir.Node {
		out := map[string]*ir.Node{}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i]] = pat(t, kv[i+1])
		}
		return out
	}
	verbs := func(v string) []string {
		if v == "" {
			return nil
		}
		return []string{v}
	}
	for _, c := range []struct {
		system, verb string
		params       map[string]*ir.Node
		reaches      bool
	}{
		{"git", "", nil, true},
		{"git", "push", nil, true},
		{"git", "push", params("ref", `!regexp "^refs/heads/"`), true},      // wider: the environment bounds it
		{"git", "push", params("ref", `!glob "refs/*/verse/auto/x"`), true}, // overlaps, contains neither way
		{"git", "push", params("ref", `!glob "refs/tags/*"`), false},        // no value in common
		{"git", "push", params("repo", `github.com/acme/docs`), false},      // that block gives no push
		{"git", "fetch", params("repo", `!glob "github.com/acme/*"`), true},
		{"git", "fetch", params("repo", `!glob "github.com/other/*"`), false}, // no block for it
		{"git", "push", params("own", `true`, "exists", `false`), true},       // each in some shape... and both free in neither
		{"slack", "post", params("channel", `general`), false},
		{"files", "write", nil, false},
		{"git", "", params("ref", `!glob "refs/tags/*"`), false}, // fetch carries no ref, and no push shape meets it
		{"git", "", params("ref", `refs/heads/verse/auto/x`), true},
	} {
		if got := e.Reaches(c.system, verbs(c.verb), c.params); got != c.reaches {
			t.Errorf("%s.%s %v: reaches %v, want %v", c.system, c.verb, c.params, got, c.reaches)
		}
	}
	bound, _ := readEnv(t, `systems: {git: {repo: $[subject.value.repo], push: {ref: !glob "refs/heads/*"}}}`)
	if !bound.Reaches("git", []string{"push"}, params("repo", `github.com/acme/app`)) {
		t.Error("a repo bound at the firing could be any, so a row naming one reaches it")
	}
}

// THE EMPTY BLOCK GIVES EVERY VERB OF ITS SYSTEM, any call; once a verb is written, only the
// verbs written. The network is apart: a block of a mode alone gives no connection. A refusal
// quoting an empty block names the verbs it gives, and it writes back as it was written.
func TestTheEmptyBlockGivesTheWholeSystem(t *testing.T) {
	e, err := readEnv(t, `systems: {git: {repo: acme/app, fetch: {}}, git-issue: {repo: acme/app}, files: {}, network: {mode: open}}`)
	if err != nil || e.Check() != nil {
		t.Fatal(err, e.Check())
	}
	for step, fields := range map[string]map[string]string{
		"files.write":      {"path": "anything/at/all"},
		"git-issue.create": {"repo": "github.com/acme/app"},
		"git-issue.push":   {"repo": "github.com/acme/app"},
	} {
		if !e.Allows(step, FieldsDoc(step, fields)) || !e.Gives(step) {
			t.Errorf("%s %v: the empty block does not give it", step, fields)
		}
	}
	if e.Allows("git.push", FieldsDoc("git.push", map[string]string{"repo": "github.com/acme/app", "ref": "refs/heads/x"})) {
		t.Error("a block naming fetch gave a push: once a verb is written, only the verbs written")
	}
	if e.Gives("network.tcp-client") || e.Reaches("network", []string{"tcp-client"}, nil) {
		t.Error("a network block of a mode alone gave a connection")
	}
	if !e.Reaches("git-issue", []string{"comment"}, map[string]*ir.Node{"issue": ir.FromString("7")}) {
		t.Error("a row on a verb the empty block gives does not reach it")
	}
	narrow, _ := readEnv(t, `systems: {git-issue: {repo: acme/app, pull: {}}}`)
	if why, out := narrow.Refuses("git-issue.create", FieldsDoc("git-issue.create", map[string]string{"repo": "github.com/acme/app"})); !out || !strings.Contains(why, "it gives: pull") {
		t.Errorf("the guard: %q %v", why, out)
	}
	none, _ := readEnv(t, `systems: {question: {}, files: {write: {}}}`)
	if why, out := none.Refuses("commands.run", FieldsDoc("commands.run", map[string]string{"command": "ls"})); !out || !strings.Contains(why, "it gives: files, question") {
		t.Errorf("a missing system: %q %v", why, out)
	}
	node, err := gomap.ToTonyIR(e)
	if err != nil {
		t.Fatal(err)
	}
	if text := encode.MustString(node); !strings.Contains(text, "files: {}") {
		t.Errorf("the empty block is written back empty:\n%s", text)
	}
}

// A HOST IS WRITTEN IN LOWER CASE, as the proxy reads it: one in another case would install and
// match no connection, so it is refused where it is written, in an environment and in a ceiling.
func TestAHostIsWrittenInLowerCase(t *testing.T) {
	e, err := readEnv(t, `systems: {network: {mode: allow, tcp-client: {host: API.GitHub.com}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Check(); err == nil || !strings.Contains(err.Error(), "api.github.com") {
		t.Errorf("a mixed-case host in an environment: %v, want it refused naming api.github.com", err)
	}
	if err := readCeiling(t, `{mode: allow, tcp-client: {host: !glob "*.GitHub.com"}}`).Check("network"); err == nil || !strings.Contains(err.Error(), ".github.com") {
		t.Errorf("a mixed-case host in a ceiling: %v, want it refused naming *.github.com", err)
	}
	if e, _ := readEnv(t, `systems: {network: {mode: allow, tcp-client: {host: api.github.com}}}`); e.Check() != nil {
		t.Errorf("a lower-case host: %v", e.Check())
	}
}
