package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/trigger"
)

// /status summarizes the running verse: it reports the store revision and live
// counts, which move as the verse is driven.
func TestStatus(t *testing.T) {
	srv := testServer(t) // no daemons needed
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	get := func() map[string]any {
		t.Helper()
		resp, err := http.Get(ts.URL + "/status")
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		var m map[string]any
		decodeResp(t, resp, &m)
		return m
	}

	m := get()
	for _, k := range []string{"storeRev", "triggers", "flows", "gates", "tasks", "fired"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("status missing %q: %v", k, m)
		}
	}
	if n, _ := codec.AsInt64(m["triggers"]); n != 0 {
		t.Fatalf("triggers = %v, want 0 on a fresh verse", n)
	}

	// Install a trigger and write an entity; the counts move.
	if err := srv.Eng.AddTrigger(trigger.Trigger{
		Name:      "t",
		Condition: trigger.Condition{Subject: trigger.Match{"system": "demo"}},
	}); err != nil {
		t.Fatal(err)
	}
	entitytest.Set(t, srv.Store, entity.Ref{System: "demo", Kind: "x", ID: "1"}, entity.Obj{"k": "v"}.Payload())

	m = get()
	if n, _ := codec.AsInt64(m["triggers"]); n != 1 {
		t.Fatalf("triggers = %v, want 1 after install", n)
	}
	if rev, _ := codec.AsInt64(m["storeRev"]); rev < 1 {
		t.Fatalf("storeRev = %v, want >= 1 after a Put", rev)
	}
}
