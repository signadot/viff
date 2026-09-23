package entity_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
)

// Every name the docs show being typed is in the grammar: a `-as`, a `VERSE_USER`, a `-to`, a
// grantee list on a grant, a default or a gate, a group's members, a resolver's `who` and `group`.
// The examples are what people copy, and a spelling the grammar refuses teaches a refusal.
//
// Only fenced blocks are read, and a name holding a placeholder (`<who>`, `$USER`, `…`) is
// skipped: it is a shape, not a spelling.
func TestTheDocsSpellNamesInTheGrammar(t *testing.T) {
	docs, _ := filepath.Glob("../docs/*.md")
	ref, _ := filepath.Glob("../docs/reference/*.md")
	docs = append(docs, ref...)
	if len(docs) == 0 {
		t.Fatal("no docs found")
	}
	principals := []*regexp.Regexp{
		regexp.MustCompile(`(?:^|[\s(])--?as[ =]("[^"]*"|'[^']*'|[^\s)]+)`),
		regexp.MustCompile(`VERSE_USER=("[^"]*"|'[^']*'|\S+)`),
		regexp.MustCompile(`driver:\s*person,\s*who:\s*([^\s,}]+)`),
	}
	grantees := []*regexp.Regexp{
		regexp.MustCompile(`\b(?:read|read-write|owner|control|answerers)\s*:\s*\[([^\]\n]*)\]`),
		regexp.MustCompile(`(?:^|[\s(])--?to[ =]([^\s)]+)`),
		regexp.MustCompile(`driver:\s*group,\s*group:\s*([^\s,}]+)`),
		regexp.MustCompile(`verse group add \S+ ([^#\n]+)`),
	}
	checked := 0
	for _, path := range docs {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, blk := range fenced(string(src)) {
			for _, re := range principals {
				for _, m := range re.FindAllStringSubmatch(blk, -1) {
					name := strings.Trim(m[1], `"'`)
					if placeholder(name) {
						continue
					}
					checked++
					if _, err := entity.ParsePrincipal(name); err != nil {
						t.Errorf("%s: %q: %v", filepath.Base(path), m[0], err)
					}
				}
			}
			for _, re := range grantees {
				for _, m := range re.FindAllStringSubmatch(blk, -1) {
					for _, name := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' }) {
						name = strings.Trim(name, `"'`)
						if name == "" || placeholder(name) {
							continue
						}
						checked++
						if _, err := entity.ParseGrantee(name); err != nil {
							t.Errorf("%s: %q: %v", filepath.Base(path), m[0], err)
						}
					}
				}
			}
		}
	}
	t.Logf("%d names checked", checked)
	if checked < 20 {
		t.Fatalf("only %d names were checked; the extraction has stopped finding the examples", checked)
	}
}

// fenced is the text of every fenced block in a markdown document.
func fenced(src string) []string {
	var out []string
	var cur []string
	in := false
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if in {
				out = append(out, strings.Join(cur, "\n"))
				cur = nil
			}
			in = !in
			continue
		}
		if in {
			cur = append(cur, line)
		}
	}
	return out
}

func placeholder(name string) bool {
	return strings.ContainsAny(name, "<$…{") || strings.Contains(name, "...")
}
