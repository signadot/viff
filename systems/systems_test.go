package systems

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// THE DOCUMENTS READ, AND NAME WHAT THE CODE NAMES: every system has verbs, a step's fields are
// its block's attributes and its verb's fields, and the names split back into the pair they
// were made from.
func TestTheDocumentsDescribeTheSystems(t *testing.T) {
	all := All()
	if len(all) < 10 {
		t.Fatalf("%d systems", len(all))
	}
	for _, want := range []string{"git", "github", "git-issue", "github-issue", "linear-issue", "slack", "question", "files", "commands", "agent", "network"} {
		if _, ok := Get(want); !ok {
			t.Errorf("no document describes %s", want)
		}
	}
	for step, want := range map[string]string{
		"git.push":              "repo,from,ref,delete,force,exists,own",
		"github.create-pr":      "repo,base,head,own,draft",
		"github.update-pr":      "repo,head,base,own",
		"git-issue.push":        "repo",
		"github-issue.comment":  "repo,issue",
		"linear-issue.create":   "team",
		"files.write":           "path",
		"commands.run":          "command",
		"agent.call-sub-agent":  "agent",
		"question.ask":          "",
		"verse.ask-create":      "to,labels,on",
		"verse.ask-get":         "",
		"verse.ask-list":        "",
		"verse.data-get":        "path",
		"verse.data-put":        "path,value",
		"verse.data-rm":         "path",
		"verse.charter-get":     "",
		"verse.charter-check":   "",
		"verse.charter-install": "at",
		"verse.charter-remove":  "",
		"slack.post":            "channel",
		"slack.reply":           "channel,thread",
		"slack.read":            "channel,thread",
		"slack.react":           "channel,emoji",
		"network.tcp-client":    "mode,host,port",
	} {
		fields, ok := Fields(step)
		if !ok || strings.Join(fields, ",") != want {
			t.Errorf("%s has the fields %v (%v), want %s", step, fields, ok, want)
		}
	}
	for _, bad := range []string{"repos" + ".push", "git", "git.", ".push", "git.push.now", "agents" + ".delegate"} {
		if _, ok := Fields(bad); ok {
			t.Errorf("%q names a step", bad)
		}
	}
	if s, v, ok := Split("linear-issue.update-pr"); !ok || s != "linear-issue" || v != "update-pr" {
		t.Errorf("split: %q %q %v", s, v, ok)
	}
	if ToolName("git", "push") != "git-push" || StepName("git", "push") != "git.push" {
		t.Error("the names")
	}
	if steps := Steps(); steps[0] != "agent.call-sub-agent" || len(steps) != 36 {
		t.Errorf("steps: %v", steps)
	}
}

// A DOCUMENT THAT CANNOT BE HONOURED IS A MISTAKE NAMED AT LOAD, and a verb nothing claimed is
// named by Check.
func TestADocumentIsCheckedAndAVerbMustBeClaimed(t *testing.T) {
	for _, bad := range []Document{
		{Name: "Git", Verbs: []Verb{{Name: "push"}}},
		{Name: "git"},
		{Name: "git", Verbs: []Verb{{Name: "push"}, {Name: "push"}}},
		{Name: "git", Verbs: []Verb{{Name: "push", Fields: []Field{{Name: "a"}, {Name: "a"}}}}},
		{Name: "git", Verbs: []Verb{{Name: "push", Fields: []Field{{Name: "a.b"}}}}},
		// A name means one thing in a document: an attribute, a verb, or a field.
		{Name: "git", Attributes: []Attribute{{Name: "push"}}, Verbs: []Verb{{Name: "push"}}},
		{Name: "git", Attributes: []Attribute{{Name: "repo"}}, Verbs: []Verb{{Name: "push", Fields: []Field{{Name: "repo"}}}}},
		{Name: "git", Verbs: []Verb{{Name: "push"}, {Name: "fetch", Fields: []Field{{Name: "push"}}}}},
		// At most one unique attribute, defaults and values of their form.
		{Name: "git", Attributes: []Attribute{{Name: "a", Unique: true}, {Name: "b", Unique: true}}, Verbs: []Verb{{Name: "push"}}},
		{Name: "git", Attributes: []Attribute{{Name: "a", Form: FormBool, Default: "yes"}}, Verbs: []Verb{{Name: "push"}}},
		{Name: "git", Verbs: []Verb{{Name: "push", Fields: []Field{{Name: "a", Form: FormUpper, Values: []string{"eng"}}}}}},
		{Name: "git", Verbs: []Verb{{Name: "push", Fields: []Field{{Name: "a", Values: []string{"*"}}}}}},
		{Name: "git", Attributes: []Attribute{{Name: "a", Form: "shout"}}, Verbs: []Verb{{Name: "push"}}},
		{Name: "git", Attributes: []Attribute{{Name: "a", Required: true, Default: "x"}}, Verbs: []Verb{{Name: "push"}}},
		{Name: "git", Attributes: []Attribute{{Name: "a", Unique: true, Default: "x"}}, Verbs: []Verb{{Name: "push"}}},
	} {
		if err := bad.check(); err == nil {
			t.Errorf("%+v passed", bad)
		}
	}
	docs := []Document{{Name: "x", Verbs: []Verb{{Name: "a", Fields: []Field{{Name: "p"}, {Name: "q"}}}, {Name: "b"}}}}
	if err := check(docs, map[string][]string{"x.a": {"p", "q"}}); err == nil || !strings.Contains(err.Error(), "x.b: nothing") || strings.Contains(err.Error(), "x.a") {
		t.Errorf("a verb nothing claimed: %v", err)
	}
	if err := check(docs, map[string][]string{"x.a": {"p", "q"}, "x.b": {}}); err != nil {
		t.Errorf("claims that agree: %v", err)
	}
	// The fields a step names are the params, neither more nor fewer, and a Tool's claim alone
	// names none.
	for want, claims := range map[string]map[string][]string{
		"x.a: the document has q, which the code never fills":     {"x.a": {"p"}, "x.b": {}},
		"x.a: the code fills r, which the document does not name": {"x.a": {"p", "q", "r"}, "x.b": {}},
		"x.b: no step names its fields":                           {"x.a": {"p", "q"}, "x.b": nil},
	} {
		if err := check(docs, claims); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("check(%v) = %v, want it to say %q", claims, err, want)
		}
	}
	func() {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(r.(string), "nothing") {
				t.Errorf("an undocumented verb was named: %v", r)
			}
		}()
		Tool("git", "nothing")
	}()
	if Tool("git", "push") != "git-push" || Step("git", "push") != "git.push" {
		t.Error("the claimed names")
	}
	// A step carries its block's attributes, then its verb's fields, and the code must fill both.
	withAttr := []Document{{Name: "x", Attributes: []Attribute{{Name: "repo", Unique: true}}, Verbs: []Verb{{Name: "a", Fields: []Field{{Name: "p"}}}}}}
	if err := check(withAttr, map[string][]string{"x.a": {"p"}}); err == nil || !strings.Contains(err.Error(), "the document has repo") {
		t.Errorf("an attribute the code never fills: %v", err)
	}
	if err := check(withAttr, map[string][]string{"x.a": {"repo", "p"}}); err != nil {
		t.Errorf("a step with its attribute: %v", err)
	}
}

// A FIELD THAT LISTS ITS VALUES TAKES ONE OF THEM, `null` for any, or an operator that matches
// one; an attribute is a field of the step, matched by its form.
func TestValuesAndAttributesAreMatchedAsFields(t *testing.T) {
	owner := Field{Name: "owner", Values: []string{"task", "other"}}
	for pattern, ok := range map[string]bool{`task`: true, `other`: true, `null`: true, `"*"`: false, `mine`: false, `""`: false,
		`!or [task, other]`: true, `!or [task, mine]`: false, `!glob "t*"`: true, `!glob "x*"`: false, `!not task`: true} {
		if err := owner.Accepts(pat(t, pattern)); (err == nil) != ok {
			t.Errorf("owner %s: %v, want accepted=%v", pattern, err, ok)
		}
	}
	repo := Attribute{Name: "repo", Form: FormRepo}.Field()
	if repo.Accepts(ir.FromString("github.com/acme/app")) != nil || repo.Accepts(ir.FromString("GitHub.com/acme/app")) == nil {
		t.Error("an attribute is matched by its form")
	}
	d, _ := Get("git")
	fields, ok := d.StepFields("push")
	if !ok || fields[0].Name != "repo" || fields[1].Name != "from" || len(fields) != 7 {
		t.Errorf("git.push's step fields: %+v", fields)
	}
	if u, ok := d.Unique(); !ok || u.Name != "repo" {
		t.Errorf("git's unique attribute: %+v %v", u, ok)
	}
}

// AN OPERATOR IN A SEGMENT OF AN ADDRESS IS TRIED when the pattern is checked, as an operator over
// text is: a regexp that does not compile matches no name, which in a deny row protects nothing,
// so it is refused where its author hears it rather than passing over every address at a match.
func TestAnAddressSegmentsOperatorIsTried(t *testing.T) {
	path := Field{Name: "path", Form: FormPath}
	for pattern, ok := range map[string]bool{`[play, !regexp "iss.*"]`: true, `[play, !regexp "("]`: false,
		`[play, !gloob x]`: false, `[play, !or [a, b]]`: true, `play.issues.*`: true} {
		if err := path.Accepts(pat(t, pattern)); (err == nil) != ok {
			t.Errorf("path %s: %v, want accepted=%v", pattern, err, ok)
		}
	}
	if err := path.Accepts(pat(t, `[play, !regexp "("]`)); err == nil || !strings.Contains(err.Error(), "segment 1") {
		t.Errorf("a refusal names the segment: %v", err)
	}
}
