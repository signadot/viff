package viffd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/entity"
)

// putBody sends one PUT as a principal, the way the CLI's perms edit does.
func putBody(t *testing.T, as, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(api.PrincipalHeader, as)
	req.Header.Set("Content-Type", "application/tony")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// The top is readable and editable through `viff perms`: a deployment seeds a policy and can
// change it, and anybody can read what it is. The admin identity owns the root, so it is the
// one that may set it — a root the system owned would be one nobody could.
func TestTheRootsRecordIsReadableAndItsOwnersToSet(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Bare `viff perms` carries the root's record, which stands beside the mirror.
	resp := getAs(t, "alice@example.com", ts.URL+"/perms/records")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), AdminPrincipal) {
		t.Errorf("the listing does not carry the root's record: %s", body)
	}

	// Reading the root's own record by address.
	resp = getAs(t, "alice@example.com", ts.URL+"/perms?at=")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), AdminPrincipal) {
		t.Errorf("reading the root's record: %d %s", resp.StatusCode, body)
	}

	// Setting it is the owner's. Alice is not it; the admin identity is.
	edit := `{at: "", grants: [{read: ["*"]}]}`
	resp = putBody(t, "alice@example.com", ts.URL+"/perms", edit)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("alice set the root's record: %d", resp.StatusCode)
	}
	resp = putBody(t, AdminPrincipal, ts.URL+"/perms", edit)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%s could not set the root's record: %d %s", AdminPrincipal, resp.StatusCode, body)
	}
	// …and the check reads it as it now stands: the open viff is closed.
	store := entity.As(srv.Store, entity.SystemDefault)
	if ok, _ := entity.MayCreate(store, "user:bob@example.com", entity.RefAt("spaces")); ok {
		t.Error("bob still takes a place after the top was closed")
	}
}
