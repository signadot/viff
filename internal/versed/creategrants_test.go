package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/entity"
)

// `POST /entity` with `grants` creates the node holding out what its creator said, in the list
// `verse perms --grants` takes: a stranger the default would admit is refused it, a grantee
// reads it, a node that stands is 409 and unchanged, and a grant no owner could give is 400.
func TestAWriteWithGrantsCreatesTheNodeHeldOutAsItSays(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	post := func(as, body string) (int, string) {
		t.Helper()
		resp := postAs(t, as, ts.URL+"/entity", "application/tony", body)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	read := func(as, at string) int {
		t.Helper()
		resp := getAs(t, as, ts.URL+"/entity?at="+at)
		resp.Body.Close()
		return resp.StatusCode
	}

	if code, body := post("alice@example.com", `{at: demo.priv.1, payload: {n: 1}, grants: [{read: [carol@example.com]}]}`); code/100 != 2 {
		t.Fatalf("alice's create with grants: %d %s", code, body)
	}
	if code := read("bob@example.com", "demo.priv.1"); code != http.StatusForbidden {
		t.Errorf("bob reading a node held out to carol alone: %d, want 403", code)
	}
	if code := read("carol@example.com", "demo.priv.1"); code != http.StatusOK {
		t.Errorf("carol reading a node held out to her: %d, want 200", code)
	}

	// A plain create beside it is the open verse's, as ever.
	if code, body := post("alice@example.com", `{at: demo.pub.1, payload: {n: 1}}`); code/100 != 2 {
		t.Fatalf("alice's plain create: %d %s", code, body)
	}
	code, body := post("alice@example.com", `{at: demo.pub.1, payload: {n: 2}, grants: []}`)
	if code != http.StatusConflict || !strings.Contains(body, "verse perms") {
		t.Errorf("grants on a node that stands: %d %s, want 409 naming verse perms", code, body)
	}
	if e, ok, err := srv.Store.Get(entity.RefAt("demo", "pub", "1")); err != nil || !ok || !entity.Equal(e.Payload, entity.Obj{"n": 1}.Payload()) {
		t.Errorf("the refused write changed the node: %v %v %v", entity.ToObj(e.Payload), ok, err)
	}
	if code := read("bob@example.com", "demo.pub.1"); code != http.StatusOK {
		t.Errorf("bob reading the node the refused write named: %d, want 200", code)
	}

	for name, body := range map[string]string{
		"owner held out at the node":          `{at: demo.priv.2, payload: {n: 1}, grants: [{owner: [bob@example.com]}]}`,
		"a grants field that is no list":      `{at: demo.priv.2, payload: {n: 1}, grants: {read: ["*"]}}`,
		"a grantee the grammar does not read": `{at: demo.priv.2, payload: {n: 1}, grants: [{read: ["no such thing"]}]}`,
		"an element of an array":              `{at: demo.priv.2, sub: "[0]", payload: {n: 1}, grants: []}`,
	} {
		if code, msg := post("alice@example.com", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, code, msg)
		}
	}
	if _, found, _ := srv.Store.Get(entity.RefAt("demo", "priv", "2")); found {
		t.Error("a refused create landed")
	}
}
