package trigger

import (
	"os"
	"strings"
	"testing"
)

func adviseSrc(t *testing.T, src string) []string {
	t.Helper()
	return advise(mustParseCharter(t, src))
}

// THE TAG-KEYED SPELLING IS INERT INSIDE `value`, and the charter is told at install.
//
// On a dimension `{glob: "hot-*"}` and `!glob "hot-*"` are one grammar — the tag-keyed object is
// how a JSON-shaped charter reaches an operator, since JSON has no tag syntax. Under `value`
// only the tag works, so the absence latch written the JSON way asks for a payload EQUAL to
// `{not: {reviewed: null}}` and matches nothing, forever, with no error
// (git-issue srrwnc0kh12kr1r1e1n0).
func TestAnOperatorShapedValueIsAdvised(t *testing.T) {
	for _, tc := range []struct{ name, charter, want string }{
		{
			name: "the absence latch, JSON-spelled",
			charter: `
- name: r
  condition: {subject: {path: [demo, pr, *], value: {not: {reviewed: null}}}}
  action: {patch: {stage: seen}}
`,
			want: "{not: …}",
		},
		{
			name: "an operator nested under a field",
			charter: `
- name: r
  condition: {subject: {path: [demo, pr, *], value: {stage: {glob: "a*"}}}}
  action: {patch: {stage: seen}}
`,
			want: "{glob: …}",
		},
		{
			name: "in a when",
			charter: `
- name: r
  condition:
    subject: {path: [demo, pr, *]}
    when: {path: [demo, ci, *], value: {not: {failed: null}}}
  action: {patch: {stage: seen}}
`,
			want: "{not: …}",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notes := adviseSrc(t, tc.charter)
			var found string
			for _, n := range notes {
				if strings.Contains(n, "READ AS DATA") {
					found = n
				}
			}
			if found == "" {
				t.Fatalf("no note:\n%s", strings.Join(notes, "\n"))
			}
			if !strings.Contains(found, tc.want) {
				t.Errorf("the note does not name %s:\n%s", tc.want, found)
			}
		})
	}
}

// The TAG spelling is what a charter should write, and earns no note. Neither does a dimension,
// where the tag-keyed object is a legitimate second spelling.
func TestATaggedValueAndATagKeyedDimensionAreNotAdvised(t *testing.T) {
	for _, src := range []string{
		`
- name: r
  condition: {subject: {path: [demo, pr, *], value: !not {reviewed: null}}}
  action: {patch: {stage: seen}}
`,
		`
- name: r
  condition: {subject: {path: [demo, pr, {glob: "hot-*"}]}}
  action: {patch: {stage: seen}}
`,
	} {
		for _, n := range adviseSrc(t, src) {
			if strings.Contains(n, "READ AS DATA") {
				t.Errorf("%s\nearned: %s", src, n)
			}
		}
	}
}

// And the note is not noise: no charter in the tree earns one.
func TestTheCharactersInTheTreeEarnNoOperatorShapedNote(t *testing.T) {
	for _, f := range []string{
		"../docs/working/comment-drift.tony",
		"../docs/working/verse-dev/drift-local.tony",
		"../docs/working/verse-dev/docs-drift.tony",
		"../docs/working/verse-dev/once.tony",
	} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		trigs, _, _, _, ok := parseBlock(string(src))
		if !ok {
			t.Fatalf("%s: did not parse as a charter", f)
		}
		for _, tr := range trigs {
			for _, n := range adviseTagKeyedInValue(tr) {
				t.Errorf("%s: %s: %s", f, tr.Name, n)
			}
		}
	}
}
