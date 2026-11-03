package versed

import (
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// THE DEPLOY ANNOUNCEMENT, as the example has it: a package that announces each new deploy/<i>.N
// tag in its own outbox, and leaves a tag the seed already wrote down silent. The seed and the
// latch are in the outbox, inside the package, where the rule reads them.
func TestTheDeployAnnouncementAnnouncesOnlyNewTags(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withSlackIO(t, srv)
	b, err := os.ReadFile("../docs/reference/examples/deploy-announce/deploy-announce.tony")
	if err != nil {
		t.Fatal(err)
	}
	box := func(tag string) entity.Ref { return entity.RefAt("announce", "out", "slack", tag) }

	// The seed, before the install and by the same person, as the example's header says to write
	// it with `verse data --put`.
	if resp := putAs(t, "user:"+tester, ts.URL, box("deploy/staging.1").KPath(),
		entity.Obj{"seen": entity.Obj{"name": "deploy/staging.1"}}.Payload()); resp.StatusCode >= 300 {
		t.Fatalf("seeding: %s: %s", resp.Status, bodyOf(t, resp))
	}
	installCharter(t, ts, "", string(b))
	for _, tag := range []string{"deploy/staging.1", "deploy/staging.2"} {
		entitytest.Set(t, srv.Store, entity.RefAt("git", "verse", "ref", tag), entity.Obj{"name": tag, "sha": "abc"}.Payload())
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		e, ok, _ := srv.Store.Get(box("deploy/staging.2"))
		if ok && entity.Str(e.Payload, "to") != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("deploy/staging.2 was not announced in the outbox: %v", e.Payload)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e, _, _ := srv.Store.Get(box("deploy/staging.1")); entity.Str(e.Payload, "to") != "" {
		t.Errorf("a seeded tag was announced: %v", entity.ToObj(e.Payload))
	}
}
