package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/trigger"
)

// postAs is postTony with the request made by somebody: the principal header the CLI sends
// from its config's `user:`.
func postAs(t *testing.T, as, url, contentType, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if as != "" {
		req.Header.Set(api.PrincipalHeader, as)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

func owner(t *testing.T, srv *Server, ref entity.Ref) string {
	t.Helper()
	rec, ok, err := srv.Store.Get(entity.PermsRef(ref))
	if err != nil {
		t.Fatalf("reading the owner of %s: %v", ref, err)
	}
	if !ok {
		return ""
	}
	return entity.Str(rec.Payload, entity.OwnerField)
}

// A request names who makes it, and everything it writes is theirs: a raw put and a charter
// install alike leave the requester as the owner of what they created, and the requester
// on the verse's list of users. A request that names nobody leaves no owner and no user.
func TestARequestsPrincipalOwnsWhatItCreates(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	put, err := codec.Marshal(api.PutEntity{At: "demo.pr.1", Payload: entity.Obj{"stage": "open"}.Payload()}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	resp := postAs(t, "alice@example.com", ts.URL+"/entity", "application/tony", string(put))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("put: %s", resp.Status)
	}
	pr := entity.RefAt("demo", "pr", "1")
	if got := owner(t, srv, pr); got != "user:alice@example.com" {
		t.Fatalf("owner of %s = %q, want the requester", pr, got)
	}
	if _, ok, err := srv.Store.Get(entity.UserRef("alice@example.com")); err != nil || !ok {
		t.Fatalf("alice is not on the list of users (ok=%v err=%v)", ok, err)
	}

	charter := `{ name: review,
	  condition: { subject: { path: "demo.pr.*", value: { stage: open } } },
	  action: { patch: { stage: reviewing } } }`
	resp = postAs(t, "scott@example.com", ts.URL+"/charter", "application/tony", charter)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	if got := owner(t, srv, trigger.CharterRefAt("", "review")); got != "user:scott@example.com" {
		t.Fatalf("the rule record is owned by %q, want its installer", got)
	}

	// The question nobody asked as anybody: no owner, and no user.
	put2, _ := codec.Marshal(api.PutEntity{At: "demo.pr.2", Payload: entity.Obj{"stage": "draft"}.Payload()}, codec.Tony)
	resp = postAs(t, "", ts.URL+"/entity", "application/tony", string(put2))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("anonymous put: %s", resp.Status)
	}
	if got := owner(t, srv, entity.RefAt("demo", "pr", "2")); got != "" {
		t.Fatalf("an unattributed put recorded %q as owner", got)
	}
}
