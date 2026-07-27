package versed

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// post writes through the surface and returns the status plus the entity it answered with.
func post(t *testing.T, ts *httptest.Server, body api.PutEntity) (int, map[string]any) {
	t.Helper()
	node, err := body.ToTonyIR()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	buf, err := codec.Marshal(node, codec.Tony)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(ts.URL+"/entity", "application/tony", strings.NewReader(string(buf)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	var out struct {
		Payload map[string]any `tony:"field=payload"`
	}
	decodeResp(t, resp, &out)
	return resp.StatusCode, out.Payload
}

// The write surface merges at a place: a whole entity, or one field inside it, without the
// caller having to send — or even know — the rest of the payload.
func TestWriteEntityMergesAndGoesDeep(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, got := post(t, ts, api.PutEntity{
		Ref:     "demo:pr:1",
		Payload: entity.Obj{"stage": "open", "env": "prod"}.Payload(),
	}); code != 200 || got["stage"] != "open" {
		t.Fatalf("create: %d %+v", code, got)
	}

	// A second write mentioning one field leaves the others alone…
	code, got := post(t, ts, api.PutEntity{Ref: "demo:pr:1", Payload: entity.Obj{"stage": "reviewed"}.Payload()})
	if code != 200 || got["stage"] != "reviewed" || got["env"] != "prod" {
		t.Fatalf("merge: %d %+v", code, got)
	}

	// …and a write can address a field directly, answering with the whole entity.
	code, got = post(t, ts, api.PutEntity{Ref: "demo:pr:1", Field: []string{"labels", "urgent"}, Payload: ir.FromBool(true)})
	if code != 200 {
		t.Fatalf("deep write: %d", code)
	}
	labels, _ := got["labels"].(map[string]any)
	if labels["urgent"] != true || got["stage"] != "reviewed" {
		t.Fatalf("deep write = %+v, want urgent set and the rest intact", got)
	}
}

// Create and update are the same request with opposite conditions, and a refused one is a
// 409 that wrote nothing — the request was fine, the world just said no.
func TestWriteEntityConditions(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	yes, no := true, false

	if code, _ := post(t, ts, api.PutEntity{
		Ref:     "demo:pr:1",
		Payload: entity.Obj{"stage": "open"}.Payload(),
		When:    []api.Condition{{Exists: &yes}},
	}); code != http.StatusConflict {
		t.Fatalf("update of an absent entity = %d, want 409", code)
	}
	if code, _ := post(t, ts, api.PutEntity{
		Ref:     "demo:pr:1",
		Payload: entity.Obj{"stage": "open"}.Payload(),
		When:    []api.Condition{{Exists: &no}},
	}); code != 200 {
		t.Fatalf("create of an absent entity = %d, want 200", code)
	}
	if code, _ := post(t, ts, api.PutEntity{
		Ref:     "demo:pr:1",
		Payload: entity.Obj{"stage": "hijacked"}.Payload(),
		When:    []api.Condition{{Exists: &no}},
	}); code != http.StatusConflict {
		t.Fatalf("create of an existing entity = %d, want 409", code)
	}

	// A pattern condition, and a cross-entity one.
	if code, got := post(t, ts, api.PutEntity{
		Ref:     "demo:pr:1",
		Payload: entity.Obj{"stage": "reviewed"}.Payload(),
		When:    []api.Condition{{Pattern: entity.Obj{"stage": "open"}.Payload()}},
	}); code != 200 || got["stage"] != "reviewed" {
		t.Fatalf("conditional update: %d %+v", code, got)
	}
	if code, _ := post(t, ts, api.PutEntity{
		Ref:     "demo:pr:1",
		Payload: entity.Obj{"stage": "deployed"}.Payload(),
		When:    []api.Condition{{Ref: "demo:gate:1", Pattern: entity.Obj{"open": true}.Payload()}},
	}); code != http.StatusConflict {
		t.Fatalf("write gated on an absent entity = %d, want 409", code)
	}

	// The entity still says what the last successful write left.
	if _, got := post(t, ts, api.PutEntity{Ref: "demo:pr:1", Payload: entity.Obj{}.Payload()}); got["stage"] != "reviewed" {
		t.Fatalf("a refused write left a mark: %+v", got)
	}
}

// A condition that asks nothing is refused rather than ignored: sending one means intending
// a guard, and a guard silently not applied is worse than a rejected request.
func TestWriteEntityRejectsAnEmptyCondition(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	if code, _ := post(t, ts, api.PutEntity{
		Ref:     "demo:pr:1",
		Payload: entity.Obj{"stage": "open"}.Payload(),
		When:    []api.Condition{{}},
	}); code != http.StatusBadRequest {
		t.Fatalf("empty condition = %d, want 400", code)
	}
}

// Removal addresses a place too: a whole entity, or one field of it.
func TestRemoveEntityAndField(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	post(t, ts, api.PutEntity{Ref: "demo:pr:1", Payload: entity.Obj{"stage": "open", "tmp": "scratch"}.Payload()})

	rm := func(q url.Values) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/entity?"+q.Encode(), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := rm(url.Values{"ref": {"demo:pr:1"}, "field": {"tmp"}}); code != http.StatusNoContent {
		t.Fatalf("remove a field = %d, want 204", code)
	}
	e, ok, _ := srv.Store.Get(entity.Ref{System: "demo", Kind: "pr", ID: "1"})
	if !ok || entity.Str(e.Payload, "stage") != "open" {
		t.Fatalf("removing a field took the entity with it: %+v", e.Payload)
	}
	if _, still := entity.ToObj(e.Payload)["tmp"]; still {
		t.Fatalf("field not removed: %+v", e.Payload)
	}

	if code := rm(url.Values{"ref": {"demo:pr:1"}}); code != http.StatusNoContent {
		t.Fatalf("remove the entity = %d, want 204", code)
	}
	if _, ok, _ := srv.Store.Get(entity.Ref{System: "demo", Kind: "pr", ID: "1"}); ok {
		t.Fatal("entity still present after removal")
	}
	// Removing what is not there is not an error.
	if code := rm(url.Values{"ref": {"demo:pr:1"}}); code != http.StatusNoContent {
		t.Fatalf("remove an absent entity = %d, want 204", code)
	}
}
