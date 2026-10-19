package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
)

// An id is the prefix of every gate question a run raises, and answering one recovers the owner
// by cutting at the FIRST `~` — so an id carrying one hands its questions to a task that is
// not it. The trigger side has stripped these all along; the ad-hoc face never had to, because
// until `--name` it never let a caller near an id.
func TestASpawnIdMayNotBreakTheSurfacesItCrosses(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, bad := range []string{"a~b", "a/b", "a b", "a#b", "a?b"} {
		resp := postTony(t, ts.URL+"/tasks", api.Spawn{ID: bad, Doer: runner.Doer{Script: "true"}})
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body := string(raw)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("id %q was accepted: %s", bad, resp.Status)
		}
		if !strings.Contains(body, "name") {
			t.Fatalf("the refusal of %q does not point at the safe way: %s", bad, body)
		}
	}
}

// A name is a LABEL: the runtime derives the address from it, so it is readable, unique, and
// cannot be malformed however it was typed.
func TestANameBecomesASafeUniqueID(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ids := map[string]bool{}
	for range 2 {
		resp := postTony(t, ts.URL+"/tasks", api.Spawn{Name: "re~view me", Doer: runner.Doer{Script: "true"}})
		sp := spawned(t, resp)
		if strings.ContainsAny(sp.ID, "~/ ") {
			t.Fatalf("a derived id kept a character the id space cannot carry: %q", sp.ID)
		}
		if !strings.HasPrefix(sp.ID, "re_view_me-") {
			t.Fatalf("the derived id does not read as its label: %q", sp.ID)
		}
		if ids[sp.ID] {
			t.Fatalf("two runs with one name got one id: %q", sp.ID)
		}
		ids[sp.ID] = true
	}
	// And with no name at all, the id it always minted.
	resp := postTony(t, ts.URL+"/tasks", api.Spawn{Doer: runner.Doer{Script: "true"}})
	sp := spawned(t, resp)
	if !strings.HasPrefix(sp.ID, "run-") {
		t.Fatalf("an unnamed run is %q, want run-…", sp.ID)
	}
}

func spawned(t *testing.T, resp *http.Response) api.Spawned {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var sp api.Spawned
	if err := codec.Unmarshal(raw, &sp, codec.Tony); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	return sp
}
