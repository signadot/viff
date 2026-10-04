package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/resolver"
)

// `verse resolver grant <ref>` lets the resolver answer for you, at your own verse.for node, and
// `revoke` takes it back; `--for` names a place instead.
func TestAResolverIsGrantedAndRevokedFromTheCLI(t *testing.T) {
	store := serveAsking(t)
	as(t, daniel)
	doc := filepath.Join(t.TempDir(), "helper.tony")
	if err := os.WriteFile(doc, []byte(`{name: helper, match: {}, method: {driver: service}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "resolver", "add", doc); err != nil {
		t.Fatalf("resolver add: %v\n%s", err, out)
	}
	via := entity.Via(resolver.Ref("helper"))
	granted := func() bool {
		t.Helper()
		e, ok, err := store.Get(entity.PermsRef(entity.ForRef(daniel)))
		if err != nil || !ok {
			return false
		}
		gs, _ := entity.ParseGrants(entity.ForRef(daniel).Segs(), e.Payload)
		for _, g := range gs {
			for _, n := range g.To[entity.Answer] {
				if n == via {
					return true
				}
			}
		}
		return false
	}
	if out, err := runCLI(t, "resolver", "grant", "helper"); err != nil || !strings.Contains(out, "answers for you") {
		t.Fatalf("resolver grant: %v\n%s", err, out)
	}
	if !granted() {
		t.Fatal("the grant is not on daniel's node")
	}
	if out, err := runCLI(t, "resolver", "revoke", "helper"); err != nil || !strings.Contains(out, "no longer answers for you") {
		t.Fatalf("resolver revoke: %v\n%s", err, out)
	}
	if granted() {
		t.Fatal("the revoked grant stands")
	}
	if _, err := runCLI(t, "resolver", "grant", "helper", "--for", "nobodys.place"); err == nil {
		t.Fatal("a grant at a place daniel does not own was taken")
	}
}

// WHAT A RESOLVER SERVES FOR YOU is marked where you read it and asked about where you answer it
// (9ce3bva0, stage 5): the watch says it waits on you, served by the resolver; the docket shows it
// with --served; answering it yourself takes --force.
func TestAnAskYourResolverServesIsMarkedAndAnsweredWithForce(t *testing.T) {
	serveAsking(t)
	as(t, davido)
	doc := filepath.Join(t.TempDir(), "desk.tony")
	if err := os.WriteFile(doc, []byte(`{name: desk, match: {}, method: {driver: service}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "resolver", "add", doc); err != nil {
		t.Fatalf("resolver add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "resolver", "grant", "desk"); err != nil {
		t.Fatalf("resolver grant: %v\n%s", err, out)
	}
	id := askAs(t, daniel, "land", "-q", "Review verse/ask?", "--to", "oscar@example.com")
	as(t, davido)

	his, _ := runCLIIn(t, strings.NewReader(""), "question", "--watch", "--since", "0")
	waitFor(t, his, "waiting on you "+id+": Review verse/ask? (served by verse.resolvers.desk)")

	if out, err := runCLI(t, "gate"); err != nil || strings.Contains(out, id) {
		t.Fatalf("davido's docket lists what his resolver serves: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "gate", "--served"); err != nil || !strings.Contains(out, id) || !strings.Contains(out, "verse.resolvers.desk") {
		t.Fatalf("davido's docket --served: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "gate", id, "--answer", "proceed"); err == nil {
		t.Fatalf("davido answered what his resolver serves without --force:\n%s", out)
	}
	if out, err := runCLI(t, "gate", id, "--answer", "proceed", "--force"); err != nil {
		t.Fatalf("davido's --force answer: %v\n%s", err, out)
	}
}
