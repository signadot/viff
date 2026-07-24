package versed

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/example/infra/verse/entity"
	"github.com/example/infra/verse/trigger"
)

// /status summarizes the running verse: it reports the store revision and live
// counts, which move as the verse is driven.
func TestStatus(t *testing.T) {
	srv := New(ScriptedBuilder) // in-memory, no daemons needed
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
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return m
	}

	m := get()
	for _, k := range []string{"storeRev", "triggers", "flows", "gates", "processes", "fired"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("status missing %q: %v", k, m)
		}
	}
	if n := m["triggers"].(float64); n != 0 {
		t.Fatalf("triggers = %v, want 0 on a fresh verse", n)
	}

	// Install a trigger and write an entity; the counts move.
	if err := srv.Eng.AddDef(trigger.Def{
		Name:    "t",
		Matches: []trigger.EventMatch{{System: "demo"}},
	}); err != nil {
		t.Fatal(err)
	}
	srv.Store.Put(entity.Ref{System: "demo", Kind: "x", ID: "1"}, entity.Payload{"k": "v"})

	m = get()
	if n := m["triggers"].(float64); n != 1 {
		t.Fatalf("triggers = %v, want 1 after install", n)
	}
	if rev := m["storeRev"].(float64); rev < 1 {
		t.Fatalf("storeRev = %v, want >= 1 after a Put", rev)
	}
}
