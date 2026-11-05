package verbs

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/systems"
)

// THE VERBS ARE THE VERSE SYSTEM'S: each one the verse document describes is defined here, and
// nothing else, so a task's tools and verse mcp's are the same set.
func TestTheVerbsAreTheVerseDocumentsVerbs(t *testing.T) {
	d, ok := systems.Get("verse")
	if !ok {
		t.Fatal("no verse document")
	}
	var documented, defined []string
	for _, v := range d.Verbs {
		documented = append(documented, v.Name)
	}
	for _, v := range All {
		defined = append(defined, v.Name)
	}
	slices.Sort(documented)
	slices.Sort(defined)
	if !slices.Equal(documented, defined) {
		t.Errorf("the verse document describes %v, and the verbs defined are %v", documented, defined)
	}
}

// EACH FACE READS ITS OWN WORDS: who the verb acts as, the command it mirrors (and to a task,
// verse mcp's tool), the other tools as it spells them, and what is said to it alone.
func TestEachFaceReadsItsOwnWords(t *testing.T) {
	mcp, task := AskCreate.Description(MCP), AskCreate.Description(Task)
	for _, c := range []struct {
		desc string
		want []string
		not  []string
	}{
		{mcp, []string{"Ask somebody something as you, as `verse ask [<name>] -q <question>` does: ", "verse_ask_get <id>", "verse_wait"},
			[]string{"verse_ask_create", "this task", "question-ask"}},
		{task, []string{"as the person this task runs for, as `verse ask [<name>] -q <question>` and verse_ask_create do: ",
			"verse-ask-get <id>", "question-ask"}, []string{"verse_wait", "as you"}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.desc, w) {
				t.Errorf("%q does not say %q", c.desc, w)
			}
		}
		for _, w := range c.not {
			if strings.Contains(c.desc, w) {
				t.Errorf("%q says %q", c.desc, w)
			}
		}
	}
	on := func(f Face) string { return AskCreate.Properties(f)["on"].(map[string]any)["description"].(string) }
	if strings.Contains(on(MCP), "this task") || !strings.Contains(on(Task), "no further than this task") {
		t.Errorf("on says %q to verse mcp and %q to a task", on(MCP), on(Task))
	}
	for _, v := range All {
		for _, f := range []Face{MCP, Task} {
			words := []string{v.Description(f)}
			for _, p := range v.Properties(f) {
				words = append(words, p.(map[string]any)["description"].(string))
			}
			for _, w := range words {
				for _, u := range All {
					if strings.Contains(w, "{"+u.Name+"}") {
						t.Errorf("%s: %q leaves {%s} unspelled", v.Name, w, u.Name)
					}
				}
			}
		}
	}
}

// A CALL IS READ BY ITS SCHEMA: a string, a boolean, an integer however the codec or JSON carried
// it. One of another type, or one the verb does not take, is refused naming it rather than read
// as nothing; so is one the verb needs and was not given, a flag's companion without it, and a
// negative count.
func TestACallIsReadByItsSchema(t *testing.T) {
	a, err := DataGet.Read(map[string]any{"at": " acme.notes ", "paths": true, "depth": int64(2), "if": nil})
	if n, _ := a.Num("depth"); err != nil || a.Word("at") != "acme.notes" || !a.Flag("paths") || n != 2 {
		t.Errorf("read as %+v, %v", a, err)
	}
	if a, err := DataGet.Read(map[string]any{"paths": true, "depth": float64(1)}); err != nil {
		t.Errorf("a whole float read as %+v, %v", a, err)
	}
	for _, c := range []struct {
		v    *Verb
		in   map[string]any
		want string
	}{
		{DataGet, map[string]any{"paths": "yes"}, "paths is a boolean"},
		{DataGet, map[string]any{"paths": true, "depth": 1.5}, "depth is an integer"},
		{DataGet, map[string]any{"at": 7}, "at is a string"},
		{DataGet, map[string]any{"bogus": "x"}, "bogus is not a parameter this tool takes"},
		{DataGet, map[string]any{"depth": 1}, "depth goes with paths"},
		{DataGet, map[string]any{"paths": true, "depth": -1}, "depth is never negative"},
		{CharterGet, map[string]any{"depth": -1}, "depth is never negative"},
		{DataPut, map[string]any{"at": "acme.notes.n1", "value": " "}, "value is what is written there"},
		{AskGet, map[string]any{}, "name the ask"},
		{CharterRemove, map[string]any{"path": ""}, "path names what to remove"},
	} {
		if _, err := c.v.Read(c.in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v = %v, want %q", c.v.Name, c.in, err, c.want)
		}
	}
	if _, err := CharterGet.Read(map[string]any{"depth": 0}); err != nil {
		t.Errorf("charter-get's depth goes without paths: %v", err)
	}
}

// AN ASK IS READ AS `verse ask` READS IT (api.NewAsk): `to` a comma-separated list, `on` a
// tony list of rows, `label` name=value pairs. What the CLI refuses, the verb
// refuses.
func TestAnAskIsReadAsTheCLIReadsIt(t *testing.T) {
	ask := func(in map[string]any) error {
		a, err := AskCreate.Read(in)
		if err != nil {
			return err
		}
		_, err = AskOf(a, time.Now())
		return err
	}
	a, err := AskCreate.Read(map[string]any{"question": "q", "to": "user:ana@example.com, group:leads",
		"label": "team=payments, kind=deploy", "on": `[{then: {spawn: {script: "true"}}}]`})
	if err != nil {
		t.Fatal(err)
	}
	got, err := AskOf(a, time.Now())
	if err != nil || len(got.To) != 2 || got.To[1] != "group:leads" || got.Labels["kind"] != "deploy" ||
		len(got.Labels) != 2 || len(entity.Fields(got.On)) != 0 || got.On == nil || got.Name != "" {
		t.Errorf("read as %+v, %v", got, err)
	}
	for _, bad := range []map[string]any{
		{"question": "q", "label": "team"}, {"question": "q", "to": "user:ana@example.com,,group:leads"},
		{"question": "q", "to": []any{"user:ana@example.com"}}, {"question": "q", "label": map[string]any{"team": "payments"}},
		{"question": "q", "on": "spawn it"}, {"question": "q", "on": "{then: {}}"}, {"name": "n"},
	} {
		if err := ask(bad); err == nil {
			t.Errorf("%v was taken", bad)
		}
	}
}
