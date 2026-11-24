package viffd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.viff.dev/viff/internal/resolver"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	viffio "go.viff.dev/viff/pkg/io"
)

// A REGISTRATION THIS VIFF CAN'T READ is still there to see and to remove. One an older viff
// wrote in another shape used to come back as its raw payload; a listing that skipped it would
// leave an operator nothing but writing the store by hand. Each listing shows it as a row with
// its name and why, and the removal by name takes it out.
func TestARegistrationThisViffCantReadIsListedAndRemoved(t *testing.T) {
	for _, c := range []struct {
		route string
		ref   entity.Ref
	}{
		{"/runners", runnerEntityRef("old")},
		{"/iods", viffio.Ref("old")},
		{"/resolvers", resolver.Ref("old")},
	} {
		t.Run(c.route, func(t *testing.T) {
			srv := testServer(t)
			noLocalRunner(t, srv) // the one row listed is the unreadable one
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()
			// A name that is a list: no registration's `name` decodes from one.
			if _, _, err := entity.As(srv.Store, "user:"+tester).Write(entity.Write{
				At: c.ref.At(), Value: entity.Obj{"name": []any{"old"}}.Payload(),
			}); err != nil {
				t.Fatal(err)
			}

			var rows []map[string]any
			resp, err := ts.Client().Do(mustReq(t, http.MethodGet, ts.URL+c.route))
			if err != nil {
				t.Fatal(err)
			}
			if err := codec.Decode(resp.Body, &rows, codec.Tony); err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if len(rows) != 1 || rows[0]["name"] != "old" || rows[0]["at"] != c.ref.String() || rows[0]["unreadable"] == nil {
				t.Fatalf("%s = %v, want the one registration, named, with why it can't be read", c.route, rows)
			}

			resp, err = ts.Client().Do(mustReq(t, http.MethodDelete, ts.URL+c.route+"/old"))
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode >= 300 {
				t.Fatalf("removing it = %s: %s, want it removed", resp.Status, readBody(t, resp))
			}
			resp.Body.Close()
			if _, ok, _ := srv.Store.Get(c.ref); ok {
				t.Fatalf("%s still stands after its removal", c.ref)
			}
		})
	}
}

// A REGISTRATION NAMING NO SUBJECT is one an older viff wrote, and it decodes: `subject` may be
// absent. Read as it decodes, it listed as an ordinary runner, took a token minted for nobody at
// the door, and failed each spawn at the handshake pointing at a check that could never fix it.
// It is a registration this viff can't read: listed so, no runner at the door or to a spawn, and
// each surface that names it, a spawn passing it over among them, says to add it again.
func TestARegistrationNamingNoSubjectIsUnreadable(t *testing.T) {
	srv := testServer(t)
	noLocalRunner(t, srv)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	face, _ := offeringFace(t, ts.URL)
	if _, _, err := entity.As(srv.Store, "user:"+tester).Write(entity.Write{
		At: runnerEntityRef("old").At(), Value: entity.Obj{"name": "old", "url": face.URL}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	const readd = "viff runner add old"

	var rows []map[string]any
	resp, err := ts.Client().Do(mustReq(t, http.MethodGet, ts.URL+"/runners"))
	if err != nil {
		t.Fatal(err)
	}
	if err := codec.Decode(resp.Body, &rows, codec.Tony); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(rows) != 1 || !strings.Contains(fmt.Sprint(rows[0]["unreadable"]), readd) {
		t.Fatalf("/runners = %v, want the registration unreadable, saying to add it again", rows)
	}
	if list, err := srv.registrations(); err != nil || len(list) != 0 {
		t.Fatalf("the door's registrations = %v, %v; want none", list, err)
	}

	for _, c := range []struct {
		what string
		resp *http.Response
		code int
	}{
		{"show", getAs(t, "user:"+tester, ts.URL+"/runners/old"), http.StatusInternalServerError},
		{"check", getAs(t, "user:"+tester, ts.URL+"/runners/old/check"), http.StatusConflict},
		{"spawn", postTony(t, ts.URL+"/tasks", map[string]any{"id": "on-old", "spawn": map[string]any{"script": "true"}}), http.StatusBadRequest},
	} {
		if body := readBody(t, c.resp); c.resp.StatusCode != c.code || !strings.Contains(body, readd) {
			t.Errorf("%s = %s: %s, want %d saying to add it again", c.what, c.resp.Status, body, c.code)
		}
	}
	if _, ok, _ := srv.Store.Get(entity.ViffRef("task", "instance", "on-old")); ok {
		t.Error("a spawn on the unreadable registration left a task record")
	}

	// Added again with its subject, it is a runner.
	if resp := postTony(t, ts.URL+"/runners", face.reg("old")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("adding it again = %s: %s", resp.Status, readBody(t, resp))
	}
	if e, ok := findRunner(srv.Store, "old"); !ok || e.Subject != face.Subject {
		t.Fatalf("after adding it again, the registration = %+v, %v", e, ok)
	}
}
