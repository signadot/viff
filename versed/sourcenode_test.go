package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/source"
)

// A source's install takes the nodes its claims name for its installer, like a package's address:
// what it reflects beneath them is owned through that one record and carries none of its own,
// somebody else may not write it, and somebody else may not install a source over the same node.
// A second repository is a second node, so anybody may install a source over that.
func TestASourceTakesTheNodesItsClaimsName(t *testing.T) {
	requireGit(t)
	raw := entitytest.Store(t)
	srv := NewWithStore(ScriptedBuilder, raw)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	t.Cleanup(srv.stopAllSources)

	install := func(as, name, dir string) (int, string) {
		t.Helper()
		body, err := codec.Marshal(map[string]any{"system": "git", "name": name, "dir": dir}, codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		resp := postAs(t, as, ts.URL+"/sources?allow-overlap=true", "application/tony", string(body))
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(msg)
	}

	first := repoWithCommit(t)
	if code, msg := install("alice@example.com", "first", first); code != http.StatusCreated {
		t.Fatalf("alice's install = %d: %s", code, msg)
	}
	e, _ := source.Find(srv.Store, entity.JoinSegs([]string{"git", "first"}))
	claims, err := source.ParseOwns(e.Owns)
	if err != nil || len(claims) == 0 {
		t.Fatalf("the registration claims nothing: %+v %v", e.Owns, err)
	}
	nodes := source.Nodes(claims)
	if len(nodes) == 0 {
		t.Fatalf("its claims name no node: %+v", e.Owns)
	}
	for _, node := range nodes {
		if o, ok := ownerOfRecord(t, raw, node); !ok || o != "user:alice@example.com" {
			t.Fatalf("node %s is owned by %q (record %v), want its installer", node, o, ok)
		}
	}

	head := entity.RefAt(source.GitSystem, baseOf(first), "head")
	waitForRef(t, raw, head)
	if _, ok := ownerOfRecord(t, raw, head); ok {
		t.Fatalf("a reflected entity has an owner record of its own")
	}
	// Written via the source, which is how it comes to be alice's.
	commits, err := raw.Since(0, entity.PrefixPattern(head.Segs(), len(head.Segs())))
	if err != nil {
		t.Fatal(err)
	}
	reflected := 0
	for _, c := range commits {
		if !c.Changed(head) {
			continue
		}
		reflected++
		if c.Author != entity.Via(source.Ref("git", "first")) {
			t.Fatalf("a reflection of %s was written by %q, want via the source", head, c.Author)
		}
	}
	if reflected == 0 {
		t.Fatalf("no commit reflected %s", head)
	}
	if ok, err := entity.Holds(srv.Store, "user:alice@example.com", entity.OwnerLevel, head); err != nil || !ok {
		t.Fatalf("alice does not own what her source reflects: %v %v", ok, err)
	}
	resp := putAs(t, "bob@example.com", ts.URL, head.String(), entity.Obj{"sha": "forged"}.Payload())
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bob's write to a reflected entity = %s", resp.Status)
	}

	// Bob may not install a source over a node alice owns, and is told who does — even past the
	// overlap refusal, which is the operator's to waive and not the owner's.
	code, msg := install("bob@example.com", "bobs", first)
	if code != http.StatusForbidden || !strings.Contains(msg, "installing a source that owns it") || !strings.Contains(msg, "alice@example.com") {
		t.Fatalf("bob's install over alice's node = %d: %s", code, msg)
	}
	// Another repository is another node, and bob takes it.
	other := repoWithCommit(t)
	if code, msg := install("bob@example.com", "bobs", other); code != http.StatusCreated {
		t.Fatalf("bob's install of another repository = %d: %s", code, msg)
	}
	if o, ok := ownerOfRecord(t, raw, entity.RefAt(source.GitSystem, baseOf(other))); !ok || o != "user:bob@example.com" {
		t.Fatalf("bob's repository is owned by %q (record %v)", o, ok)
	}
}

func ownerOfRecord(t *testing.T, store entity.Store, ref entity.Ref) (string, bool) {
	t.Helper()
	rec, ok, err := store.Get(entity.PermsRef(ref))
	if err != nil {
		t.Fatalf("reading the record of %s: %v", ref, err)
	}
	if !ok {
		return "", false
	}
	return entity.Str(rec.Payload, entity.OwnerField), true
}
