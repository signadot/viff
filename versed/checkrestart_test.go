package versed

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/signadot/verse/entity/entitytest"
)

// THE CHECK FINDS NOTHING TO CHANGE IN A TREE THAT STANDS, across a restart: what a verse
// restores from the store is what it installed, and the charter check against the same tree
// says so. The repository's own charters are the tree, installed as the git io installs them.
func TestTheCheckFindsNoChangeAfterARestart(t *testing.T) {
	files, err := charterFilesUnder(repoCharters)
	if err != nil {
		t.Fatal(err)
	}
	var docs []string
	for _, f := range files {
		doc, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, string(doc))
	}
	tree := strings.Join(docs, "\n---\n")

	store := entitytest.Store(t)
	first := NewWithStore(ScriptedBuilder, store)
	first.Start(t.Context())
	ts := httptest.NewServer(first.Handler())
	installCharter(t, ts, "", tree)
	before := checkAt(t, ts, repoChartersAt, tree)
	for _, p := range before.Packages {
		if len(p.Added)+len(p.Changed)+len(p.Removed) != 0 {
			t.Errorf("before a restart, %s would change: added %v changed %v removed %v", p.At, p.Added, p.Changed, p.Removed)
		}
	}
	ts.Close()

	second := NewWithStore(ScriptedBuilder, store)
	second.Start(t.Context())
	ts2 := httptest.NewServer(second.Handler())
	defer ts2.Close()
	after := checkAt(t, ts2, repoChartersAt, tree)
	if after.Verdict != "installs" {
		t.Fatalf("after a restart the tree is %s: %s", after.Verdict, after.Refusal)
	}
	for _, p := range after.Packages {
		if len(p.Added)+len(p.Changed)+len(p.Removed) != 0 {
			t.Errorf("after a restart, %s would change: added %v changed %v removed %v", p.At, p.Added, p.Changed, p.Removed)
		}
	}
}
