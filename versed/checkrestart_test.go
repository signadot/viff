package versed

import (
	"bytes"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity/entitytest"
)

// THE SAME DEFINITION IS THE SAME BYTES, however its containers were ordered: an object's keys
// in any order, a sparse array's numbers in any order, at any depth, with or without comments;
// and an array in another order is another definition.
func TestTheCheckEncodesADefinitionCanonically(t *testing.T) {
	parse := func(src string) *ir.Node {
		t.Helper()
		n, err := codec.ParseNode([]byte(src), codec.Tony)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		return n
	}
	sparse := func(kvs ...ir.KeyVal) *ir.Node { return ir.FromKeyVals(kvs) }
	a := parse("stage: idle\nsha: abc\nin: {zeta: [1, 2], alpha: 2}\n")
	b := parse("# the same, written the other way round\nin: {alpha: 2, zeta: [1, 2]}\nsha: abc\nstage: idle\n")
	if !bytes.Equal(encoded(a), encoded(b)) {
		t.Errorf("objects in another key order encode differently:\n%s\n%s", encoded(a), encoded(b))
	}
	c := sparse(ir.KeyVal{Key: ir.FromInt(10), Val: parse("z: 1\ny: 2\n")}, ir.KeyVal{Key: ir.FromInt(2), Val: ir.FromString("b")})
	d := sparse(ir.KeyVal{Key: ir.FromInt(2), Val: ir.FromString("b")}, ir.KeyVal{Key: ir.FromInt(10), Val: parse("y: 2\nz: 1\n")})
	if !bytes.Equal(encoded(c), encoded(d)) {
		t.Errorf("sparse arrays in another key order encode differently:\n%s\n%s", encoded(c), encoded(d))
	}
	if bytes.Equal(encoded(parse("in: [1, 2]\n")), encoded(parse("in: [2, 1]\n"))) {
		t.Error("an array in another order encoded the same")
	}
}

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
	registerRunner(t, ts, "verse-runner", "verse") // the tree's spawns name it
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
