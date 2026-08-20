package trigger

import (
	"os"
	"strings"
	"testing"
)

// A charter can say `runner:`, and the field survives being parsed.
//
// The docs test proves a charter INSTALLS, which an unknown field would also do — the grammar
// drops what it does not recognise, so a doc teaching `runner:` could install cleanly and mean
// nothing. This reads the field back off the parsed rule, and it reads it out of the doc rather
// than a copy, so the two cannot drift apart.
func TestDocsCharterCarriesRunner(t *testing.T) {
	src, err := os.ReadFile("../docs/runners.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var found bool
	for _, blk := range charterBlocks(string(src)) {
		if !strings.Contains(blk.text, "runner:") {
			continue
		}
		trigs, _, _, _, ok := parseBlock(blk.text)
		if !ok || len(trigs) == 0 {
			continue
		}
		for _, tr := range trigs {
			if tr.Action.Spawn == nil {
				continue
			}
			found = true
			if got := tr.Action.Spawn.Runner; got != "verse.runners.pool.gpu" {
				t.Fatalf("%s: spawn.runner parsed as %q, want verse.runners.pool.gpu — the docs teach "+
					"a field the grammar drops", tr.Name, got)
			}
		}
	}
	if !found {
		t.Fatal("docs/runners.md no longer has a charter naming a runner; this test is the only " +
			"thing checking that the field the docs teach is the field the grammar reads")
	}
}
