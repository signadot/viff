package versed

import (
	"io"
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
	resp, err := httpPost(ts.URL+"/entity", "application/tony", strings.NewReader(string(buf)))
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
		At:      "demo.pr.1",
		Payload: entity.Obj{"stage": "open", "env": "prod"}.Payload(),
	}); code != 200 || got["stage"] != "open" {
		t.Fatalf("create: %d %+v", code, got)
	}

	// A second write mentioning one field leaves the others alone…
	code, got := post(t, ts, api.PutEntity{At: "demo.pr.1", Payload: entity.Obj{"stage": "reviewed"}.Payload()})
	if code != 200 || got["stage"] != "reviewed" || got["env"] != "prod" {
		t.Fatalf("merge: %d %+v", code, got)
	}

	// …and a write can address a place INSIDE a payload, which is the same request with a
	// longer address: the wire has no field list any more, because nothing on it says where an
	// entity ends.
	// It answers with what stands at the address WRITTEN, which here is a bool rather than an
	// object — so this one goes through postTony rather than post's map decode.
	deep := postTony(t, ts.URL+"/entity", api.PutEntity{At: "demo.pr.1.labels.urgent", Payload: ir.FromBool(true)})
	deep.Body.Close()
	if deep.StatusCode != 200 {
		t.Fatalf("deep write: %s", deep.Status)
	}
	// The merge is what a read at the entity shows: the field landed and the rest is untouched.
	e, ok, _ := srv.Store.Get(entity.RefAt("demo", "pr", "1"))
	if !ok {
		t.Fatal("the entity is gone after a write inside it")
	}
	labels, _ := entity.ToObj(e.Payload)["labels"].(map[string]any)
	if labels["urgent"] != true || entity.Str(e.Payload, "stage") != "reviewed" {
		t.Fatalf("deep write = %+v, want urgent set and the rest intact", e.Payload)
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
		At:      "demo.pr.1",
		Payload: entity.Obj{"stage": "open"}.Payload(),
		When:    []api.Condition{{Exists: &yes}},
	}); code != http.StatusConflict {
		t.Fatalf("update of an absent entity = %d, want 409", code)
	}
	if code, _ := post(t, ts, api.PutEntity{
		At:      "demo.pr.1",
		Payload: entity.Obj{"stage": "open"}.Payload(),
		When:    []api.Condition{{Exists: &no}},
	}); code != 200 {
		t.Fatalf("create of an absent entity = %d, want 200", code)
	}
	if code, _ := post(t, ts, api.PutEntity{
		At:      "demo.pr.1",
		Payload: entity.Obj{"stage": "hijacked"}.Payload(),
		When:    []api.Condition{{Exists: &no}},
	}); code != http.StatusConflict {
		t.Fatalf("create of an existing entity = %d, want 409", code)
	}

	// A pattern condition, and a cross-entity one.
	if code, got := post(t, ts, api.PutEntity{
		At:      "demo.pr.1",
		Payload: entity.Obj{"stage": "reviewed"}.Payload(),
		When:    []api.Condition{{Pattern: entity.Obj{"stage": "open"}.Payload()}},
	}); code != 200 || got["stage"] != "reviewed" {
		t.Fatalf("conditional update: %d %+v", code, got)
	}
	if code, _ := post(t, ts, api.PutEntity{
		At:      "demo.pr.1",
		Payload: entity.Obj{"stage": "deployed"}.Payload(),
		When:    []api.Condition{{At: "demo.gate.1", Pattern: entity.Obj{"open": true}.Payload()}},
	}); code != http.StatusConflict {
		t.Fatalf("write gated on an absent entity = %d, want 409", code)
	}

	// The entity still says what the last successful write left.
	if _, got := post(t, ts, api.PutEntity{At: "demo.pr.1", Payload: entity.Obj{}.Payload()}); got["stage"] != "reviewed" {
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
		At:      "demo.pr.1",
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
	post(t, ts, api.PutEntity{At: "demo.pr.1", Payload: entity.Obj{"stage": "open", "tmp": "scratch"}.Payload()})

	rm := func(q url.Values) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/entity?"+q.Encode(), nil)
		req.Header.Set(api.PrincipalHeader, tester)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := rm(url.Values{"at": {"demo.pr.1.tmp"}}); code != http.StatusNoContent {
		t.Fatalf("remove a field = %d, want 204", code)
	}
	e, ok, _ := srv.Store.Get(entity.RefAt("demo", "pr", "1"))
	if !ok || entity.Str(e.Payload, "stage") != "open" {
		t.Fatalf("removing a field took the entity with it: %+v", e.Payload)
	}
	if _, still := entity.ToObj(e.Payload)["tmp"]; still {
		t.Fatalf("field not removed: %+v", e.Payload)
	}

	if code := rm(url.Values{"at": {"demo.pr.1"}}); code != http.StatusNoContent {
		t.Fatalf("remove the entity = %d, want 204", code)
	}
	if _, ok, _ := srv.Store.Get(entity.RefAt("demo", "pr", "1")); ok {
		t.Fatal("entity still present after removal")
	}
	// Removing what is not there is not an error.
	if code := rm(url.Values{"at": {"demo.pr.1"}}); code != http.StatusNoContent {
		t.Fatalf("remove an absent entity = %d, want 204", code)
	}
}

// The generic entity write is for what the world is about. What verse writes about its own
// running is refused there, because before the boundary had a name anyone who could reach the
// store could install a rule, stamp a task record, or answer a question on verse's behalf.
//
// A BALLOT IS NOT REFUSED, and that is the line rather than an exception to it: casting one is
// an act somebody takes, so `vote:*` is the world's half — while the motion it answers is
// verse's, and cannot be written from here.
func TestVerseOwnHalfRefusesAClientWrite(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, ref := range []string{
		"verse.trigger.rule.sneaky",
		"verse.task.instance.run-1",
		`verse.ask."run-1~d1"`,
		"verse.motion.channel.abcd",
		"verse.resolvers.mine",
	} {
		resp := postTony(t, ts.URL+"/entity", api.PutEntity{At: ref, Payload: entity.Obj{"x": 1}.Payload()})
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("writing %s = %s, want 403: %s", ref, resp.Status, strings.TrimSpace(string(body)))
		}
		if _, ok, _ := srv.Store.Get(mustRef(t, ref)); ok {
			t.Errorf("%s was written anyway", ref)
		}
	}

	// And the two halves of a vote: the ballot lands, the motion does not.
	if resp := postTony(t, ts.URL+"/entity", api.PutEntity{
		At: "vote.abcd.scott", Payload: entity.Obj{"choice": "approve"}.Payload(),
	}); resp.StatusCode >= http.StatusBadRequest {
		t.Errorf("casting a ballot = %s, want it accepted", resp.Status)
	} else {
		resp.Body.Close()
	}
}

// A WRITE NAMES A PLACE, at whatever depth — one kinded path, which is what let the wire drop
// the three-segment `ref` it carried beside it. A write four segments down is an ordinary write.
//
// The answer is what stands at the address written, which is what makes a merge visible: a
// second write mentioning one field leaves the others alone and shows them back.
func TestAWriteMayNameAPlaceAtAnyDepth(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const deep = "verse-dev.drift.head.abc"
	if code, got := post(t, ts, api.PutEntity{
		At:      deep,
		Payload: entity.Obj{"sha": "deadbeef"}.Payload(),
	}); code != 200 || got["sha"] != "deadbeef" {
		t.Fatalf("a four-segment write: %d %+v", code, got)
	}
	code, got := post(t, ts, api.PutEntity{At: deep, Payload: entity.Obj{"reviewed": "done"}.Payload()})
	if code != 200 || got["reviewed"] != "done" || got["sha"] != "deadbeef" {
		t.Fatalf("the merge at depth: %d %+v", code, got)
	}

	// It is one entity, four segments down — not a field of a three-segment one.
	if ents := srv.Store.ListAt([]string{"verse-dev", "drift", "head"}, 4, nil); len(ents) != 1 {
		t.Errorf("the store holds %d entities at depth 4, want the one written", len(ents))
	}

	// A path with an empty segment names no place, and is refused rather than written somewhere
	// close to what was meant.
	if code, _ := post(t, ts, api.PutEntity{
		At:      `verse-dev."".head`,
		Payload: entity.Obj{"x": 1}.Payload(),
	}); code != 400 {
		t.Errorf("an empty segment was accepted (%d)", code)
	}
}
