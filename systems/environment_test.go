package systems

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/encode"
	"github.com/signadot/tony-format/go-tony/gomap"
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
    - {ref: "refs/heads/verse/auto/*", exists: false}
    - {ref: "refs/heads/verse/auto/*", own: true}
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
	if push := git[0].Verbs["push"]; len(push) != 2 || push[0]["exists"] != "false" || push[1]["own"] != "true" {
		t.Errorf("push shapes: %+v", push)
	}
	if fetch := git[1].Verbs["fetch"]; len(fetch) != 1 || len(fetch[0]) != 0 {
		t.Errorf("fetch: {} is one shape that names nothing: %+v", fetch)
	}
	if s := e.Systems["slack"]; len(s) != 1 || s[0].Verbs["post"][0]["channel"] != "deploys" {
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
		`systems: {git: {repo: acme/app, push: {ref: $ref}}}`:                                   "is not bound",
		`systems: {git: {repo: acme/app, push: [{ref: "refs/heads/a"}, {exists: maybe}]}}`:      "push[1].exists:",
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
	if e, err := readEnv(t, `systems: {git: {repo: $subject.value.repo, fetch: {}}}`); err != nil || e.Check() == nil {
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
		if got := e.Allows(c.step, c.fields); got != c.within {
			t.Errorf("%s %v: within %v, want %v", c.step, c.fields, got, c.within)
		}
	}
}

// ONE PATTERN IS WITHIN ANOTHER when every value it matches, the other matches too, and the
// check never says so when it isn't.
func TestAPatternWithinAnother(t *testing.T) {
	for _, c := range []struct {
		outer, inner string
		crossing     bool
		within       bool
	}{
		{"refs/heads/verse/**", "refs/heads/verse/auto/*", false, true},
		{"refs/heads/verse/**", "refs/heads/verse/auto/x", false, true},
		{"refs/heads/*", "refs/heads/main", false, true},
		{"refs/heads/*", "refs/heads/**", false, false},
		{"refs/heads/*", "refs/heads/a/b", false, false},
		{"refs/heads/verse/auto/*", "refs/heads/verse/**", false, false},
		{"github.com/acme/**", "github.com/acme/app", false, true},
		{"github.com/acme/*", "github.com/acme/*", false, true},
		{"github.com/acme/a*", "github.com/acme/*", false, false},
		{"*", "x/y", true, true},
		{"make *", "make test", true, true},
		{"make test", "make *", true, false},
		{"**", "", false, true},
		{"x", "y", false, false},
	} {
		if got := PatternContains(c.outer, c.inner, c.crossing); got != c.within {
			t.Errorf("%q within %q: %v, want %v", c.inner, c.outer, got, c.within)
		}
		// Sound: what it calls within, Match agrees on for a sample of the inner's values.
		if c.within && !strings.Contains(c.inner, "*") && !Match(c.outer, c.inner, c.crossing) {
			t.Errorf("%q within %q, but Match refuses it", c.inner, c.outer)
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
		if !e.Allows("git.fetch", map[string]string{"repo": "github.com/acme/app"}) {
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

// A FIELD OF THE FORM TEXT CROSSES `/` WITH A `*`; any other does not.
func TestTextCrosses(t *testing.T) {
	if !Crossing("commands.run", "command") || Crossing("git.push", "ref") || Crossing("files.write", "path") {
		t.Error("crossing by form")
	}
	e, err := readEnv(t, `systems: {commands: {run: {command: "make *"}}}`)
	if err != nil || e.Check() != nil {
		t.Fatal(err, e.Check())
	}
	if !e.Allows("commands.run", map[string]string{"command": "make test/unit"}) {
		t.Error("a command's * crosses /")
	}
}

// TWO PATTERNS MEET when some value matches both, though neither contains the other.
func TestPatternsIntersect(t *testing.T) {
	for _, c := range []struct {
		a, b     string
		crossing bool
		meet     bool
	}{
		{"refs/*/x", "refs/heads/*", false, true},
		{"refs/heads/verse/auto/*", "refs/heads/verse/**", false, true},
		{"refs/tags/*", "refs/heads/*", false, false},
		{"refs/heads/*", "refs/heads/a/b", false, false},
		{"refs/heads/**", "refs/heads/a/b", false, true},
		{"*", "a/b", false, false},
		{"*", "a/b", true, true},
		{"make *", "* test", true, true},
		{"true", "false", false, false},
		{"", "**", false, true},
		{"a*b", "*c*", false, true},
	} {
		if got := Intersect(c.a, c.b, c.crossing); got != c.meet {
			t.Errorf("%q ∩ %q: %v, want %v", c.a, c.b, got, c.meet)
		}
		if got := Intersect(c.b, c.a, c.crossing); got != c.meet {
			t.Errorf("%q ∩ %q: %v, want %v (the other way)", c.b, c.a, got, c.meet)
		}
	}
}

// INTERSECT IS SOUND: a value two patterns both match is never said to be in neither, over
// random patterns and values from a small alphabet where `/` and the wildcards collide often.
func TestIntersectAgreesWithMatch(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	gen := func(alphabet string, n int) string {
		var b strings.Builder
		for range r.IntN(n) {
			b.WriteByte(alphabet[r.IntN(len(alphabet))])
		}
		return b.String()
	}
	for range 20000 {
		a, b, v := gen("ab/*", 6), gen("ab/*", 6), gen("ab/", 6)
		crossing := r.IntN(2) == 0
		if Match(a, v, crossing) && Match(b, v, crossing) && !Intersect(a, b, crossing) {
			t.Fatalf("%q and %q both match %q (crossing %v), but Intersect says they meet nowhere", a, b, v, crossing)
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
		{"git.push", push, "it gives {exists: false, ref: refs/heads/verse/auto/*}, {own: true, ref: refs/heads/verse/auto/*}"},
	} {
		why, refused := e.Refuses(c.step, c.fields)
		if !refused || !strings.Contains(why, c.says) {
			t.Errorf("%s %v: %q (%v), want it saying %q", c.step, c.fields, why, refused, c.says)
		}
	}
	if why, refused := e.Refuses("slack.post", map[string]string{"channel": "deploys"}); refused {
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
	for _, c := range []struct {
		system, verb string
		params       map[string]string
		reaches      bool
	}{
		{"git", "", nil, true},
		{"git", "push", nil, true},
		{"git", "push", map[string]string{"ref": "refs/heads/**"}, true},          // wider: the environment bounds it
		{"git", "push", map[string]string{"ref": "refs/*/verse/auto/x"}, true},    // overlaps, contains neither way
		{"git", "push", map[string]string{"ref": "refs/tags/*"}, false},           // no value in common
		{"git", "push", map[string]string{"repo": "github.com/acme/docs"}, false}, // that block gives no push
		{"git", "fetch", map[string]string{"repo": "github.com/acme/*"}, true},
		{"git", "fetch", map[string]string{"repo": "github.com/other/*"}, false},   // no block for it
		{"git", "push", map[string]string{"own": "true", "exists": "false"}, true}, // each in some shape... and both free in neither
		{"slack", "post", map[string]string{"channel": "general"}, false},
		{"files", "write", nil, false},
		{"git", "", map[string]string{"ref": "refs/tags/*"}, false}, // fetch carries no ref, and no push shape meets it
		{"git", "", map[string]string{"ref": "refs/heads/verse/auto/x"}, true},
	} {
		if got := e.Reaches(c.system, c.verb, c.params); got != c.reaches {
			t.Errorf("%s.%s %v: reaches %v, want %v", c.system, c.verb, c.params, got, c.reaches)
		}
	}
	bound, _ := readEnv(t, `systems: {git: {repo: $subject.value.repo, push: {ref: "refs/heads/*"}}}`)
	if !bound.Reaches("git", "push", map[string]string{"repo": "github.com/acme/app"}) {
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
		if !e.Allows(step, fields) || !e.Gives(step) {
			t.Errorf("%s %v: the empty block does not give it", step, fields)
		}
	}
	if e.Allows("git.push", map[string]string{"repo": "github.com/acme/app", "ref": "refs/heads/x"}) {
		t.Error("a block naming fetch gave a push: once a verb is written, only the verbs written")
	}
	if e.Gives("network.tcp-client") || e.Reaches("network", "tcp-client", nil) {
		t.Error("a network block of a mode alone gave a connection")
	}
	if !e.Reaches("git-issue", "comment", map[string]string{"issue": "7"}) {
		t.Error("a row on a verb the empty block gives does not reach it")
	}
	narrow, _ := readEnv(t, `systems: {git-issue: {repo: acme/app, pull: {}}}`)
	if why, out := narrow.Refuses("git-issue.create", map[string]string{"repo": "github.com/acme/app"}); !out || !strings.Contains(why, "it gives: pull") {
		t.Errorf("the guard: %q %v", why, out)
	}
	none, _ := readEnv(t, `systems: {question: {}, files: {write: {}}}`)
	if why, out := none.Refuses("commands.run", map[string]string{"command": "ls"}); !out || !strings.Contains(why, "it gives: files, question") {
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
	if err := readCeiling(t, `{mode: allow, tcp-client: {host: "*.GitHub.com"}}`).Check("network"); err == nil || !strings.Contains(err.Error(), "*.github.com") {
		t.Errorf("a mixed-case host in a ceiling: %v, want it refused naming *.github.com", err)
	}
	if e, _ := readEnv(t, `systems: {network: {mode: allow, tcp-client: {host: api.github.com}}}`); e.Check() != nil {
		t.Errorf("a lower-case host: %v", e.Check())
	}
}
