package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// spawnFor starts a run about a subject.
func spawnFor(t *testing.T, ts *httptest.Server, id, subject string) {
	t.Helper()
	resp := postTony(t, ts.URL+"/tasks", map[string]any{"id": id, "doer": map[string]any{"script": "true"}, "subject": subject})
	defer resp.Body.Close()
	// Loudly: a test whose run never started fails three assertions later as a timeout.
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("spawn %s: %s: %s", id, resp.Status, strings.TrimSpace(string(raw)))
	}
}

func pr(t *testing.T, srv *Server, id, area string) entity.Ref {
	t.Helper()
	ref := entity.RefAt("change", "pr", id)
	entitytest.Set(t, srv.Store, ref, entity.Obj{"area": area}.Payload())
	return ref
}

func addRule(t *testing.T, ts *httptest.Server, doc string) {
	t.Helper()
	resp, err := httpPost(ts.URL+"/charter", "application/tony", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("trigger add: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("trigger add = %s", resp.Status)
	}
}
