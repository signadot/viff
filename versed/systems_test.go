package versed

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/gomap"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/systems"
)

// THE SYSTEMS' DOCUMENTS ARE IN THE STORE ONCE VERSE HAS STARTED, each at verse.systems.<name>,
// as the binary ships it: a reader of the store sees the same attributes, verbs and fields a
// row is checked against.
func TestTheSystemsDocumentsAreInstalledAtStart(t *testing.T) {
	srv, _ := startedServer(t)
	for _, want := range systems.All() {
		e, ok, err := srv.Store.Get(systemRef(want.Name))
		if err != nil || !ok {
			t.Fatalf("verse.systems.%s: %v %v", want.Name, ok, err)
		}
		var got systems.Document
		if err := gomap.FromTonyIR(e.Payload, &got); err != nil {
			t.Fatalf("verse.systems.%s: %v", want.Name, err)
		}
		if got.Name != want.Name || got.Description != want.Description || len(got.Verbs) != len(want.Verbs) {
			t.Errorf("verse.systems.%s holds %+v, want %+v", want.Name, got, want)
		}
	}
}

// A SYSTEM THE BINARY NO LONGER SHIPS LEAVES THE STORE AT START: a store carried from a verse
// that had it keeps no document of verbs nothing checks a policy against.
func TestADocumentOfASystemNotShippedIsRemovedAtStart(t *testing.T) {
	srv := testServer(t)
	stale := systemRef("pager")
	if _, _, err := entity.As(srv.Store, entity.SystemCharter).Write(entity.Write{At: stale.At(),
		Value: entity.Obj{"name": "pager", "verbs": []any{entity.Obj{"name": "page"}}}.Payload()}); err != nil {
		t.Fatal(err)
	}
	srv.Start(t.Context())
	if _, ok, err := srv.Store.Get(stale); err != nil || ok {
		t.Errorf("verse.systems.pager after start: present %v, %v; want it removed", ok, err)
	}
	if _, ok, _ := srv.Store.Get(systemRef("git")); !ok {
		t.Error("verse.systems.git is gone too")
	}
}
