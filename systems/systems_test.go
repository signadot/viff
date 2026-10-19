package systems

import (
	"strings"
	"testing"
)

// THE DOCUMENTS READ, AND NAME WHAT THE CODE NAMES: every system has verbs, a step's fields are
// its verb's params, and the names split back into the pair they were made from.
func TestTheDocumentsDescribeTheSystems(t *testing.T) {
	all := All()
	if len(all) < 10 {
		t.Fatalf("%d systems", len(all))
	}
	for _, want := range []string{"git", "github", "git-issue", "github-issue", "linear-issue", "slack", "question", "files", "commands", "agent"} {
		if _, ok := Get(want); !ok {
			t.Errorf("no document describes %s", want)
		}
	}
	for step, want := range map[string]string{
		"git.push":             "repo,ref,delete,force,exists,own",
		"github.create-pr":     "repo,base,head,own",
		"github.update-pr":     "repo,head,base,own",
		"git-issue.push":       "repo",
		"github-issue.comment": "repo,issue",
		"linear-issue.create":  "team",
		"files.write":          "path",
		"commands.run":         "command",
		"agent.call-sub-agent": "agent",
		"question.ask":         "",
		"slack.post":           "channel",
		"slack.reply":          "channel,thread",
		"slack.read":           "channel,thread",
		"slack.react":          "channel,emoji",
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
	if steps := Steps(); steps[0] != "agent.call-sub-agent" || len(steps) != 23 {
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
		{Name: "git", Verbs: []Verb{{Name: "push", Params: []Param{{Name: "a"}, {Name: "a"}}}}},
		{Name: "git", Verbs: []Verb{{Name: "push", Params: []Param{{Name: "a.b"}}}}},
	} {
		if err := bad.check(); err == nil {
			t.Errorf("%+v passed", bad)
		}
	}
	docs := []Document{{Name: "x", Verbs: []Verb{{Name: "a", Params: []Param{{Name: "p"}, {Name: "q"}}}, {Name: "b"}}}}
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
}
