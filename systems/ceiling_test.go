package systems

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/encode"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/parse"
)

func readCeiling(t *testing.T, text string) Ceiling {
	t.Helper()
	node, err := parse.Parse([]byte(text))
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	var c Ceiling
	if err := gomap.FromTonyIR(node, &c); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return c
}

const gitCeiling = `
- repo: [signadot/**, github.com/acme/app]
  fetch: {}
  push:
  - {ref: "refs/heads/verse/**"}
  - {ref: "refs/heads/release/*", force: "false"}
- repo: github.com/verse/verse
  fetch: {}
  push: {ref: refs/heads/main}
`

// A CEILING READS AS A CONFIG WRITES IT, attributes as lists of patterns (one written bare), and
// is checked against its document.
func TestACeilingReadsAndIsChecked(t *testing.T) {
	c := readCeiling(t, gitCeiling)
	if err := c.Check("git"); err != nil {
		t.Fatal(err)
	}
	if len(c) != 2 || len(c[0].Attributes["repo"]) != 2 || c[1].Attributes["repo"][0] != "github.com/verse/verse" {
		t.Fatalf("read as %+v", c)
	}
	back := encode.MustString(c.Node())
	if !strings.Contains(back, "repo: github.com/verse/verse") {
		t.Errorf("one pattern is written bare:\n%s", back)
	}
	for text, want := range map[string]string{
		`{repo: x/y, fetch: {}, branch: main}`:        `no attribute or verb "branch"`,
		`{repo: [], fetch: {}}`:                       "an empty list allows nothing",
		`{repo: "github.com/a/*", push: []}`:          "an empty list allows nothing",
		`{repo: "github.com/a/*", fetch: main}`:       "a verb takes a shape",
		`{repo: "github.com/a/*", push: {ref: main}}`: "push[0].ref",
		`{repo: "$subject", fetch: {}}`:               "binds nothing",
	} {
		if err := readCeiling(t, text).Check("git"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want it refused saying %q", text, err, want)
		}
	}
	if err := readCeiling(t, `{mode: [none, allow], tcp-client: [{host: "*.github.com"}]}`).Check("network"); err != nil {
		t.Errorf("a network ceiling: %v", err)
	}
	if err := readCeiling(t, `{mode: [none, sometimes]}`).Check("network"); err == nil {
		t.Error("a mode the document has not")
	}
}

// AN ENVIRONMENT'S BLOCK IS WITHIN THE CEILING when one ceiling block holds it whole: its
// attributes within the patterns, its verbs given, each shape within one of the ceiling's, field
// by field; a shape leaving open a field the ceiling names is beyond it. The block is bound, so a
// `$` in a value is part of it: install stands a charter's `$` in before it checks.
func TestABlockIsWithinTheCeiling(t *testing.T) {
	c := readCeiling(t, gitCeiling)
	block := func(text string) Block {
		t.Helper()
		e, err := readEnv(t, `systems: {git: `+text+`}`)
		if err != nil {
			t.Fatal(err)
		}
		return e.Systems["git"][0]
	}
	for text, want := range map[string]string{
		`{repo: signadot/app, fetch: {}}`:                                             "",
		`{repo: signadot/app, push: {ref: "refs/heads/verse/auto/*"}}`:                "",
		`{repo: signadot/app, push: {ref: "refs/heads/release/1.0", force: "false"}}`: "",
		`{repo: github.com/acme/app, fetch: {}}`:                                      "",
		`{repo: verse/verse, push: {ref: refs/heads/main}}`:                           "",
		`{repo: $subject.value.repo, fetch: {}}`:                                      "is beyond",
		`{repo: "$a/../..", fetch: {}}`:                                               "is beyond",
		`{repo: "github.com/signadot/$subject.value.name", fetch: {}}`:                "",
		`{repo: other/app, fetch: {}}`:                                                "repo github.com/other/app is beyond",
		`{repo: "github.com/other/$subject.value.name", fetch: {}}`:                   "is beyond",
		`{repo: signadot/app, push: {ref: refs/heads/main}}`:                          "push {ref: refs/heads/main} is within no shape",
		`{repo: signadot/app, push: {ref: "refs/heads/release/1.0"}}`:                 "within no shape", // force left open
		`{repo: signadot/app, push: {}}`:                                              "within no shape", // the bare verb
		`{repo: signadot/app, push: {ref: "refs/heads/**"}}`:                          "within no shape", // wider
	} {
		err := c.Contains("git", block(text))
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", text, err, want)
		}
	}
	// As a charter writes it, at install, a `$` attribute is within where some binding of it
	// could be.
	for text, want := range map[string]string{
		`{repo: $subject.value.repo, fetch: {}}`:                       "",
		`{repo: "github.com/signadot/$subject.value.name", fetch: {}}`: "",
		`{repo: "github.com/other/$subject.value.name", fetch: {}}`:    "is beyond",
	} {
		err := c.ContainsAsWritten("git", block(text))
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("as written, %s: %v, want %q", text, err, want)
		}
	}
	// The refusal names the block the environment's is for, not the one for another repository.
	if err := c.Contains("git", block(`{repo: signadot/app, push: {ref: refs/heads/main}}`)); err == nil ||
		!strings.HasPrefix(err.Error(), "[0]: ") || strings.Contains(err.Error(), "verse/verse") {
		t.Errorf("the nearest block alone: %v", err)
	}
	// An attribute a block bounds, with no default, is beyond it where the environment's is unsaid.
	from := readCeiling(t, `{repo: "signadot/**", from: main, fetch: {}}`)
	if err := from.Contains("git", block(`{repo: signadot/app, fetch: {}}`)); err == nil || !strings.Contains(err.Error(), "its from is unsaid") {
		t.Errorf("an unsaid from under a ceiling bounding it: %v", err)
	}
	net := readCeiling(t, `{mode: [none, allow], tcp-client: [{host: "*.github.com"}]}`)
	for text, want := range map[string]string{
		`{mode: allow, tcp-client: {host: api.github.com}}`: "",
		`{mode: none}`: "",
		`{}`:           "", // the document's default, none
		`{mode: open}`: "mode open is beyond none, allow",
		`{mode: allow, tcp-client: {host: pypi.org}}`: "within no shape",
	} {
		e, err := readEnv(t, `systems: {network: `+text+`}`)
		if err != nil {
			t.Fatal(err)
		}
		err = net.Contains("network", e.Systems["network"][0])
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("network %s: %v, want %q", text, err, want)
		}
	}
}

// A STEP IS WITHIN THE CEILING when a block its attributes match gives a shape it matches; one
// beyond is refused saying what the ceiling gives.
func TestAStepBeyondTheCeilingIsRefused(t *testing.T) {
	c := readCeiling(t, gitCeiling)
	push := func(repo, ref, force string) map[string]string {
		return map[string]string{"repo": repo, "ref": ref, "force": force, "delete": "false", "exists": "true", "own": "true"}
	}
	if _, out := c.Refuses("git.push", push("github.com/signadot/app", "refs/heads/verse/x", "true")); out {
		t.Error("a push within")
	}
	if _, out := c.Refuses("git.push", push("github.com/verse/verse", "refs/heads/main", "false")); out {
		t.Error("main, where its block gives it")
	}
	why, out := c.Refuses("git.push", push("github.com/signadot/app", "refs/heads/main", "false"))
	if !out || !strings.Contains(why, "it gives {ref: refs/heads/verse/**}") {
		t.Errorf("main, where no block gives it: %q %v", why, out)
	}
	if why, out := c.Refuses("git.fetch", map[string]string{"repo": "github.com/other/app"}); !out || !strings.Contains(why, "gives no `fetch`") {
		t.Errorf("a repository no block is for: %q %v", why, out)
	}
}

// THE EMPTY CEILING BLOCK ALLOWS EVERY VERB, any call, for the attribute patterns it names:
// Open is it, and it holds an environment's empty block and any shape.
func TestTheEmptyCeilingBlockAllowsTheWholeSystem(t *testing.T) {
	c := readCeiling(t, `{repo: "github.com/acme/**"}`)
	if err := c.Check("git-issue"); err != nil {
		t.Fatal(err)
	}
	e, err := readEnv(t, `systems: {git-issue: {repo: acme/app}, files: {write: {path: "docs/**"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Contains("git-issue", e.Systems["git-issue"][0]); err != nil {
		t.Errorf("an empty block under an empty ceiling block: %v", err)
	}
	if err := Open().Contains("files", e.Systems["files"][0]); err != nil {
		t.Errorf("a shape under Open: %v", err)
	}
	if _, out := c.Refuses("git-issue.close", map[string]string{"repo": "github.com/acme/app", "issue": "1"}); out {
		t.Error("the empty ceiling block refused a verb")
	}
	scoped := readCeiling(t, `{repo: "github.com/acme/**", fetch: {}, push: {ref: "refs/heads/verse/**"}}`)
	g, _ := readEnv(t, `systems: {git: {repo: acme/app}}`)
	if err := scoped.Contains("git", g.Systems["git"][0]); err == nil || !strings.Contains(err.Error(), "push {} is within no shape") {
		t.Errorf("an empty git block under a ceiling scoping push: %v", err)
	}
}
