package systems

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// A LIST SPELLS AS ONE LINE AND READS BACK AS ITSELF, two lists never spell the same, and the line
// is what a pattern on `commands.run` would read: bare words where a shell reads them as they
// are, quotes where it does not, so where one argument ends is never in doubt.
func TestArgvSpellsOneLineAndReadsBack(t *testing.T) {
	for _, c := range []struct {
		args []string
		line string
	}{
		{[]string{"npm", "publish", "--ignore-scripts"}, "npm publish --ignore-scripts"},
		{[]string{"npm", "publish", "--tag", "a b"}, "npm publish --tag 'a b'"},
		{[]string{"sh", "-c", "echo $S"}, "sh -c 'echo $S'"},
		{[]string{"echo", "it's"}, `echo 'it'\''s'`},
		{[]string{"echo", ""}, "echo ''"},
		{[]string{"echo", "*"}, "echo '*'"},
		{[]string{"go", "test", "./..."}, "go test ./..."},
		{[]string{"x", "a=b,c:d@e%f+g"}, "x a=b,c:d@e%f+g"},
		{[]string{"x", "tab\there", "nl\nhere"}, "x 'tab\there' 'nl\nhere'"},
	} {
		if line := Argv(c.args); line != c.line {
			t.Errorf("%q spelled %q, want %q", c.args, line, c.line)
		}
		back, err := ArgvList(c.line)
		if err != nil || !slices.Equal(back, c.args) {
			t.Errorf("%q read back as %q, %v", c.line, back, err)
		}
	}
	for _, bad := range []string{"npm 'publish", "echo $S", `a "b"`} {
		if _, err := ArgvList(bad); err == nil {
			t.Errorf("%q read as a list", bad)
		}
	}
	// A pattern anchored on the command reads the arguments as they were given: an argument that
	// holds the rest of the line is one argument, quoted, and no pattern on the bare line admits it.
	anchored := Regexp("^npm publish --ignore-scripts( --tag [a-z]+)?$")
	for _, c := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"npm", "publish", "--ignore-scripts"}, true},
		{[]string{"npm", "publish", "--ignore-scripts", "--tag", "beta"}, true},
		{[]string{"npm", "publish --ignore-scripts"}, false},
		{[]string{"npm", "publish", "--ignore-scripts", "--registry", "https://evil.example"}, false},
		{[]string{"npm", "publish", "--ignore-scripts --registry https://evil.example"}, false},
	} {
		if got := MatchField(FormText, anchored, ir.FromString(Argv(c.args))); got != c.ok {
			t.Errorf("%q against the anchored pattern: %v, want %v", c.args, got, c.ok)
		}
	}
}

// TWO LISTS NEVER SPELL THE SAME, over random lists from an alphabet where the quoted and the
// bare collide often, and each reads back as itself.
func TestArgvIsOneToOne(t *testing.T) {
	alphabet := []rune("ab _'\\$*-=")
	r := rand.New(rand.NewPCG(7, 11))
	seen := map[string][]string{}
	for range 5000 {
		args := make([]string, r.IntN(4))
		for i := range args {
			var b strings.Builder
			for range r.IntN(4) {
				b.WriteRune(alphabet[r.IntN(len(alphabet))])
			}
			args[i] = b.String()
		}
		line := Argv(args)
		if was, dup := seen[line]; dup && !slices.Equal(was, args) {
			t.Fatalf("%q and %q both spell %q", was, args, line)
		}
		seen[line] = args
		back, err := ArgvList(line)
		if err != nil || !slices.Equal(back, args) {
			t.Fatalf("%q spelled %q, read back %q, %v", args, line, back, err)
		}
	}
}
