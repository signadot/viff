package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

func putAs(t *testing.T, as, url, at string, payload entity.Payload) *http.Response {
	t.Helper()
	put, err := codec.Marshal(api.PutEntity{At: at, Payload: payload}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return postAs(t, as, url+"/entity", "application/tony", string(put))
}

func statusOf(t *testing.T, ts *httptest.Server) api.Status {
	t.Helper()
	resp, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	var st api.Status
	decodeResp(t, resp, &st)
	return st
}

// A write the check refuses is a 403 carrying the check's own words, lands nowhere, and is
// counted on /status.
func TestARefusedWriteIsA403AndCounted(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := putAs(t, "alice@example.com", ts.URL, "demo.pr.1", entity.Obj{"stage": "open"}.Payload())
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("alice's put: %s", resp.Status)
	}
	if st := statusOf(t, ts); st.Perms == nil || len(st.Perms.Refused) != 0 {
		t.Fatalf("before any refusal, perms = %+v", st.Perms)
	}

	resp = putAs(t, "bob@example.com", ts.URL, "demo.pr.1", entity.Obj{"stage": "merged"}.Payload())
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bob's put: %s %s", resp.Status, body)
	}
	if !strings.Contains(string(body), "bob@example.com holds read at") || // read: the open verse's
		!strings.Contains(string(body), "alice@example.com at") {
		t.Fatalf("the refusal does not say who or what decided: %s", body)
	}
	e, ok, err := srv.Store.Get(entity.RefAt("demo", "pr", "1"))
	if err != nil || !ok || entity.Str(e.Payload, "stage") != "open" {
		t.Fatalf("the refused write landed: stage = %q (ok=%v err=%v)", entity.Str(e.Payload, "stage"), ok, err)
	}
	if st := statusOf(t, ts); st.Perms.Refused[string(entity.ReadWrite)] != 1 {
		t.Fatalf("perms = %+v", st.Perms)
	}
	// alice, the owner, still writes.
	resp = putAs(t, "alice@example.com", ts.URL, "demo.pr.1", entity.Obj{"stage": "merged"}.Payload())
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("alice's put: %s", resp.Status)
	}
}
