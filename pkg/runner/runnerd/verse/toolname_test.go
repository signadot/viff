package verse

import (
	"strings"
	"testing"
)

// ONE BOUND FOR EVERY DOER: each agent's TakesTool is TakesToolNamed, so a tool offered to one
// is not silently dropped from another, and a refusal of the length names the name as the doer's
// model reads it.
func TestEveryDoerTakesAToolByTheOneBound(t *testing.T) {
	long := strings.Repeat("a", toolNameMost+1)
	prefixed := strings.Repeat("a", toolNameMost-10)
	for _, c := range []struct{ doer, name, reads, want string }{
		{"codex", "a tool", "a tool", "codex takes a tool's name of letters, digits, `_` and `-`"},
		{"codex", long, long, "codex takes a tool's name of 64 characters at most"},
		{"claude", prefixed, "mcp__verse__" + prefixed,
			"claude reads the tool as mcp__verse__" + prefixed + ", 66 characters, and takes a name of 64 at most"},
		{"the verse agent", "fine_tool-1", "fine_tool-1", ""},
	} {
		err := TakesToolNamed(c.doer, c.name, c.reads)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s on %q: %v, want nil", c.doer, c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s on %q: %v, want %q", c.doer, c.name, err, c.want)
		}
	}
}
