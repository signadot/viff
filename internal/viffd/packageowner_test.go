package viffd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

const ownedPackage = `
at: notes
reads: [demo]
strata:
- name: src
  level: 0
  match:
    path: "demo"
- name: rows
  level: 10
  match:
    path: "notes.row"
rules:
- name: copy
  stratum: src
  condition:
    subject:
      path: "demo.item.*"
  action:
    derive:
    - path: "notes.row"
      id: .[subject.value.id]
      value:
        seen: .[subject.value.n]
`

// A charter package's address is its installer's, taken at install: one owner record at the
// address, and none for what its rules derive beneath it, since each row is owned through the
// address. Somebody else installing there is refused with the check's words; the installer
// installing again is not.
func TestAPackageAddressIsItsInstallersAndItsRowsNeedNoRecord(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	install := func(as string) (int, string) {
		t.Helper()
		resp := postAs(t, as, ts.URL+"/charter", "application/tony", ownedPackage)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := install("alice@example.com"); code != http.StatusCreated {
		t.Fatalf("alice's install = %d: %s", code, body)
	}

	address, err := entity.ParseAddr("notes")
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, err := srv.Store.Get(entity.PermsRef(address))
	if err != nil || !ok {
		t.Fatalf("no owner record at the package address: ok=%v err=%v", ok, err)
	}
	if got := entity.Str(rec.Payload, entity.OwnerField); got != "user:alice@example.com" {
		t.Fatalf("the package address is owned by %q, want its installer", got)
	}

	// The rule derives a row; the row is owned through the address and has no record of its own.
	entitytest.Set(t, srv.Store, entity.RefAt("demo", "item", "a"), entity.Obj{"id": "a", "n": 1}.Payload())
	row := entity.RefAt("notes", "row", "a")
	waitUntil(t, "the package to derive its row", func() bool {
		_, ok, _ := srv.Store.Get(row)
		return ok
	})
	if rec, ok, _ := srv.Store.Get(entity.PermsRef(row)); ok {
		t.Fatalf("a derived row has a record of its own: %v", rec.Payload)
	}
	if ok, err := entity.Holds(srv.Store, "user:alice@example.com", entity.OwnerLevel, row); err != nil || !ok {
		t.Fatalf("alice does not own the row through the address: ok=%v err=%v", ok, err)
	}

	// Somebody else may not install at a place alice owns.
	code, body := install("bob@example.com")
	if code != http.StatusForbidden {
		t.Fatalf("bob's install at alice's address = %d: %s", code, body)
	}
	if !strings.Contains(body, "bob@example.com") || !strings.Contains(body, "installing a charter at it") {
		t.Fatalf("the refusal does not say who wanted what: %s", body)
	}

	// Alice installing again is an ordinary re-install.
	if code, body := install("alice@example.com"); code != http.StatusCreated {
		t.Fatalf("alice's re-install = %d: %s", code, body)
	}
}
